package hv

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// The osinfo-db virt-meow ships, for hosts without one (osinfo-db/README.md).
//
//go:embed osinfo-db/os.tar.gz
var shippedOSInfo []byte

const shippedOSInfoVersion = "20250619"

// OSVariant is an operating system osinfo-db knows, and what a new machine
// running it on a host should get (Propose).
type OSVariant struct {
	ID       string   `json:"id"` // http://debian.org/debian/12
	ShortID  string   `json:"short_id"`
	Name     string   `json:"name"`
	Family   string   `json:"family"` // linux, winnt, freebsd…
	Distro   string   `json:"distro,omitempty"`
	Version  string   `json:"version,omitempty"`
	Codename string   `json:"codename,omitempty"`
	Released string   `json:"released,omitempty"`
	EOL      string   `json:"eol,omitempty"`
	Propose  Proposal `json:"propose"`

	shortIDs  []string
	derives   []string
	media     []osMedia
	images    []osImage
	min, rec  osResources
	firmware  map[string]string // type (efi, bios) → "yes" or "no", for x86_64 and "all"
	devices   map[string]bool
	resolved  bool
	ownDevice bool // lists devices itself or through what it derives from
}

type osMedia struct {
	arch    string
	pattern string
	re      *regexp.Regexp
}

type osImage struct {
	arch, file string
	cloudInit  bool
}

type osResources struct {
	cpus    int
	ram     uint64
	storage uint64
}

// Proposal is what the form and virt-mm create --os-variant fill in for an
// operating system on a host.
type Proposal struct {
	OS        string `json:"os"` // virt-meow's: linux, windows, other
	VCPUs     int    `json:"vcpus"`
	MemoryMiB int    `json:"memory_mib"`
	DiskGiB   int    `json:"disk_gib"`
	Firmware  string `json:"firmware"`
	TPM       bool   `json:"tpm"`
	DiskBus   string `json:"disk_bus"`
	NICModel  string `json:"nic_model"`
}

// OSDB is an osinfo-db, read.
type OSDB struct {
	Source  string // "host" (the host's own) or "virt-mm" (the copy virt-meow ships)
	Version string
	Note    string // why the host's wasn't used
	list    []*OSVariant
	byID    map[string]*OSVariant
}

// The XML of an osinfo-db os/ file, the parts virt-meow reads.
type osinfoXML struct {
	OS []struct {
		ID       string   `xml:"id,attr"`
		ShortIDs []string `xml:"short-id"`
		Names    []struct {
			Lang string `xml:"lang,attr"`
			Text string `xml:",chardata"`
		} `xml:"name"`
		Version   string  `xml:"version"`
		Family    string  `xml:"family"`
		Distro    string  `xml:"distro"`
		Codename  string  `xml:"codename"`
		Released  string  `xml:"release-date"`
		EOL       string  `xml:"eol-date"`
		Derives   []idRef `xml:"derives-from"`
		Resources []struct {
			Arch string         `xml:"arch,attr"`
			Min  osResourcesXML `xml:"minimum"`
			Rec  osResourcesXML `xml:"recommended"`
		} `xml:"resources"`
		Firmware []struct {
			Arch      string `xml:"arch,attr"`
			Type      string `xml:"type,attr"`
			Supported string `xml:"supported,attr"`
		} `xml:"firmware"`
		Devices []idRef `xml:"devices>device"`
		Media   []struct {
			Arch     string `xml:"arch,attr"`
			VolumeID string `xml:"iso>volume-id"`
		} `xml:"media"`
		Images []struct {
			Arch      string `xml:"arch,attr"`
			CloudInit string `xml:"cloud-init,attr"`
			URL       string `xml:"url"`
		} `xml:"image"`
	} `xml:"os"`
}

type idRef struct {
	ID string `xml:"id,attr"`
}

type osResourcesXML struct {
	CPUs    int    `xml:"n-cpus"`
	RAM     uint64 `xml:"ram"`
	Storage uint64 `xml:"storage"`
}

// parseOSDB reads the os/ files of an osinfo-db from fsys.
func parseOSDB(fsys fs.FS, source, version string) (*OSDB, error) {
	db := &OSDB{Source: source, Version: version, byID: map[string]*OSVariant{}}
	err := fs.WalkDir(fsys, "os", func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && strings.HasSuffix(p, ".d") {
			return fs.SkipDir // drivers for an OS defined beside it
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".xml") {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		var doc osinfoXML
		if err := xml.Unmarshal(b, &doc); err != nil {
			return nil // a file virt-meow can't read is one OS fewer, not none
		}
		for _, o := range doc.OS {
			if o.ID == "" {
				continue
			}
			v := &OSVariant{ID: o.ID, Family: o.Family, Distro: o.Distro, Version: o.Version,
				Codename: o.Codename, Released: o.Released, EOL: o.EOL, shortIDs: o.ShortIDs,
				firmware: map[string]string{}, devices: map[string]bool{}}
			if len(o.ShortIDs) > 0 {
				v.ShortID = o.ShortIDs[0]
			}
			for _, n := range o.Names {
				if n.Lang == "" {
					v.Name = strings.TrimSpace(n.Text)
				}
			}
			v.Name = cmp.Or(v.Name, v.ShortID, v.ID)
			for _, r := range o.Derives {
				v.derives = append(v.derives, r.ID)
			}
			for _, r := range o.Resources {
				if r.Arch == "all" || r.Arch == "x86_64" || r.Arch == "" {
					v.min = osResources{r.Min.CPUs, r.Min.RAM, r.Min.Storage}
					v.rec = osResources{r.Rec.CPUs, r.Rec.RAM, r.Rec.Storage}
				}
			}
			for _, f := range o.Firmware {
				if f.Arch == "x86_64" || f.Arch == "all" || f.Arch == "" {
					v.firmware[f.Type] = map[bool]string{true: "no", false: "yes"}[f.Supported == "false"]
				}
			}
			for _, d := range o.Devices {
				v.devices[d.ID] = true
			}
			for _, m := range o.Media {
				if m.VolumeID == "" {
					continue
				}
				// osinfo's patterns are GRegex (PCRE); the few RE2 can't
				// take are left out.
				if re, err := regexp.Compile(m.VolumeID); err == nil {
					v.media = append(v.media, osMedia{m.Arch, m.VolumeID, re})
				}
			}
			for _, im := range o.Images {
				if im.URL != "" {
					v.images = append(v.images, osImage{im.Arch, path.Base(im.URL), im.CloudInit == "true"})
				}
			}
			db.byID[v.ID] = v
			db.list = append(db.list, v)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(db.list) == 0 {
		return nil, errors.New("osinfo-db: no operating systems in it")
	}
	for _, v := range db.list {
		db.resolve(v, 0)
	}
	// Each distribution together, its newest first.
	slices.SortFunc(db.list, func(a, b *OSVariant) int {
		if c := strings.Compare(cmp.Or(a.Distro, a.Family), cmp.Or(b.Distro, b.Family)); c != 0 {
			return c
		}
		return -compareVersions(a.Version, b.Version)
	})
	return db, nil
}

// resolve fills in what v inherits from the OSes it derives from: their
// devices and firmware, and their resources when it lists none.
func (db *OSDB) resolve(v *OSVariant, depth int) {
	if v.resolved || depth > 30 {
		return
	}
	v.resolved = true
	v.ownDevice = len(v.devices) > 0
	for _, id := range v.derives {
		p := db.byID[id]
		if p == nil {
			continue
		}
		db.resolve(p, depth+1)
		for d := range p.devices {
			v.devices[d] = true
		}
		v.ownDevice = v.ownDevice || p.ownDevice
		for k, s := range p.firmware {
			if _, ok := v.firmware[k]; !ok {
				v.firmware[k] = s
			}
		}
		if v.min == (osResources{}) && v.rec == (osResources{}) {
			v.min, v.rec = p.min, p.rec
		}
	}
}

// compareVersions orders versions such as 9, 12, 22.04 and 15.5 by their numbers.
func compareVersions(a, b string) int {
	pa, pb := strings.FieldsFunc(a, notDigit), strings.FieldsFunc(b, notDigit)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return cmp.Compare(x, y)
		}
	}
	if len(pa) != len(pb) {
		return cmp.Compare(len(pa), len(pb))
	}
	return strings.Compare(a, b)
}

func notDigit(r rune) bool { return r < '0' || r > '9' }

// parseOSDBTarGz reads an osinfo-db from a tar.gz of its directory.
func parseOSDBTarGz(r io.Reader, source string) (*OSDB, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("osinfo-db: %w", err)
	}
	mem := memFS{}
	version := ""
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("osinfo-db: %w", err)
		}
		name := strings.TrimPrefix(path.Clean(h.Name), "./")
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 4<<20))
		if err != nil {
			return nil, err
		}
		if name == "VERSION" {
			version = strings.TrimSpace(string(b))
			continue
		}
		mem[name] = b
	}
	return parseOSDB(mem, source, version)
}

// memFS is files by name, enough of an fs.FS for fs.WalkDir over os/.
type memFS map[string][]byte

func (m memFS) Open(name string) (fs.File, error) {
	if b, ok := m[name]; ok {
		return &memFile{Reader: bytes.NewReader(b), name: name, size: int64(len(b))}, nil
	}
	var ents []fs.DirEntry
	seen := map[string]bool{}
	prefix := name + "/"
	if name == "." {
		prefix = ""
	}
	for p := range m {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		first, _, isDir := strings.Cut(rest, "/")
		if !seen[first] {
			seen[first] = true
			ents = append(ents, memEntry{first, isDir})
		}
	}
	if len(ents) == 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	slices.SortFunc(ents, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return &memDir{name: name, ents: ents}, nil
}

type memFile struct {
	*bytes.Reader
	name string
	size int64
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	return memEntry{path.Base(f.name), false}.info(f.size), nil
}
func (f *memFile) Close() error { return nil }

type memDir struct {
	name string
	ents []fs.DirEntry
	read bool
}

func (d *memDir) Stat() (fs.FileInfo, error) { return memEntry{path.Base(d.name), true}.info(0), nil }
func (d *memDir) Read([]byte) (int, error)   { return 0, io.EOF }
func (d *memDir) Close() error               { return nil }
func (d *memDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.read {
		return nil, io.EOF
	}
	d.read = true
	return d.ents, nil
}

type memEntry struct {
	name string
	dir  bool
}

func (e memEntry) Name() string { return e.name }
func (e memEntry) IsDir() bool  { return e.dir }
func (e memEntry) Type() fs.FileMode {
	if e.dir {
		return fs.ModeDir
	}
	return 0
}
func (e memEntry) Info() (fs.FileInfo, error) { return e.info(0), nil }
func (e memEntry) info(size int64) fs.FileInfo {
	return memInfo{e, size}
}

type memInfo struct {
	memEntry
	size int64
}

func (i memInfo) Size() int64        { return i.size }
func (i memInfo) Mode() fs.FileMode  { return i.Type() | 0o444 }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) Sys() any           { return nil }

// ShippedOSDB is the osinfo-db virt-meow ships, read once.
var ShippedOSDB = sync.OnceValues(func() (*OSDB, error) {
	db, err := parseOSDBTarGz(bytes.NewReader(shippedOSInfo), "virt-mm")
	if db != nil {
		db.Version = shippedOSInfoVersion
	}
	return db, err
})

// osinfoDir is where a host keeps its osinfo-db, and osinfoCmd how virt-meow
// reads it over SSH: a fixed, read-only command (docs/adr/0002).
const (
	osinfoDir = "/usr/share/osinfo"
	osinfoCmd = "tar -C " + osinfoDir + " -czf - VERSION os"
)

// LoadOSDB reads the host's own osinfo-db, over the connection's SSH client
// or, on a local Linux connection, from this computer, and falls back on
// the copy virt-meow ships when the host has none.
func LoadOSDB(ctx context.Context, sess *conn.Session) (*OSDB, error) {
	host, err := hostOSDB(ctx, sess)
	if err == nil {
		return host, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	shipped, serr := ShippedOSDB()
	if serr != nil {
		return nil, serr
	}
	db := *shipped
	db.Note = "the host has no osinfo-db (" + err.Error() + "): virt-meow's copy"
	return &db, nil
}

func hostOSDB(ctx context.Context, sess *conn.Session) (*OSDB, error) {
	b, err := sess.Run(ctx, osinfoCmd)
	switch {
	case err == nil:
		return parseOSDBTarGz(bytes.NewReader(b), "host")
	case errors.Is(err, conn.ErrNotSSH) && runtime.GOOS == "linux":
		fsys := os.DirFS(osinfoDir)
		v, _ := fs.ReadFile(fsys, "VERSION")
		if _, err := fs.Stat(fsys, "os"); err != nil {
			return nil, err
		}
		return parseOSDB(fsys, "host", strings.TrimSpace(string(v)))
	case errors.Is(err, conn.ErrNotSSH):
		return nil, errors.New("not readable on a local connection here")
	default:
		return nil, fmt.Errorf("%s: %w", osinfoDir, err)
	}
}

// Lookup finds an operating system by its ID or any of its short IDs.
func (db *OSDB) Lookup(id string) *OSVariant {
	if v := db.byID[id]; v != nil {
		return v
	}
	for _, v := range db.list {
		if slices.Contains(v.shortIDs, id) {
			return v
		}
	}
	return nil
}

// Variants are the operating systems, each with its proposal for host h.
func (db *OSDB) Variants(h Host) []OSVariant {
	out := make([]OSVariant, len(db.list))
	for i, v := range db.list {
		out[i] = v.For(h)
	}
	return out
}

// For is v with its proposal for host h.
func (v *OSVariant) For(h Host) OSVariant {
	c := *v
	c.Propose = v.propose(h)
	return c
}

var virtioBlock = []string{"http://pcisig.com/pci/1af4/1001", "http://pcisig.com/pci/1af4/1042"}
var virtioNet = []string{"http://pcisig.com/pci/1af4/1000", "http://pcisig.com/pci/1af4/1041"}

func (v *OSVariant) has(ids []string) bool {
	return slices.ContainsFunc(ids, func(id string) bool { return v.devices[id] })
}

// windows11 says whether v wants what Windows 11 does: UEFI with Secure
// Boot and a TPM 2.0 (osinfo-db says UEFI, not the rest).
func (v *OSVariant) windows11() bool {
	return v.Family == "winnt" && v.Distro == "win" && compareVersions(v.Version, "11") >= 0
}

func (v *OSVariant) propose(h Host) Proposal {
	p := Proposal{OS: "other", VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20, Firmware: "bios", DiskBus: "virtio", NICModel: "virtio"}
	switch v.Family {
	case "linux":
		p.OS = "linux"
	case "winnt", "win9x", "msdos":
		p.OS = "windows"
	}
	res := osResources{max(v.rec.cpus, v.min.cpus), max(v.rec.ram, v.min.ram), max(v.rec.storage, v.min.storage)}
	if res.ram > 0 {
		p.MemoryMiB = int(max(res.ram>>20, 256))
		if h.MemoryKiB > 0 && uint64(p.MemoryMiB)<<10 > h.MemoryKiB && v.min.ram > 0 {
			p.MemoryMiB = int(max(v.min.ram>>20, 256))
		}
	}
	if res.storage > 0 {
		p.DiskGiB = int((res.storage + 1<<30 - 1) >> 30)
	}
	p.VCPUs = max(2, res.cpus)
	if h.CPUs > 0 {
		p.VCPUs = min(p.VCPUs, h.CPUs)
	}
	// Devices: virtio where the OS has the drivers, as the devices it
	// lists say; one that lists none gets virtio on Linux only. Windows
	// has none until they are installed.
	switch {
	case p.OS == "windows":
		p.DiskBus, p.NICModel = "sata", "e1000e"
	case v.ownDevice:
		if !v.has(virtioBlock) {
			p.DiskBus = "sata"
		}
		if !v.has(virtioNet) {
			p.NICModel = "e1000e"
		}
	case p.OS != "linux":
		p.DiskBus, p.NICModel = "sata", "e1000e"
	}
	// Firmware: UEFI unless osinfo says it can't, or it is older than UEFI
	// in practice (Windows before 8, anything released before it);
	// Secure Boot and a TPM for Windows 11.
	uefi := h.UEFI || h.VirtType == "test"
	switch {
	case v.firmware["efi"] == "no":
	case v.windows11():
		p.Firmware = "uefi"
		if h.SecureBoot {
			p.Firmware = "uefi-secure"
		}
		p.TPM = h.TPM
	case v.Family == "winnt" && compareVersions(v.Version, "6.2") < 0:
	case v.Family == "win9x" || v.Family == "msdos":
	case v.Released != "" && v.Released < "2012-10":
	default:
		if uefi {
			p.Firmware = "uefi"
		}
	}
	if p.Firmware == "uefi" && !uefi {
		p.Firmware = "bios"
	}
	return p
}

// Detected is what an ISO or a disk image was recognised as.
type Detected struct {
	Variant  *OSVariant `json:"variant"`             // nil: not recognised
	By       string     `json:"by,omitempty"`        // volume-id, image, name
	VolumeID string     `json:"volume_id,omitempty"` // the ISO's
	Source   string     `json:"source"`              // the osinfo-db's: host, virt-mm
}

// MatchISO finds the operating system whose installation media has the
// volume ID id: the longest pattern that matches, on the host's
// architecture first.
func (db *OSDB) MatchISO(id, arch string) *OSVariant {
	var best *OSVariant
	score := -1
	for _, v := range db.list {
		for _, m := range v.media {
			bonus := 0
			switch {
			case m.arch == arch || m.arch == "" || m.arch == "all":
				bonus = 1000
			case arch == "x86_64" && m.arch == "i686":
			default:
				continue
			}
			if m.re.MatchString(id) && len(m.pattern)+bonus > score {
				best, score = v, len(m.pattern)+bonus
			}
		}
	}
	return best
}

var nameSplit = regexp.MustCompile(`[^a-z0-9.]+`)

// MatchImage finds the operating system a disk or cloud image is, by its
// file name: one osinfo-db lists as that OS's image, else the name's
// distribution and version (debian-12-…, Fedora-Cloud-Base-41-…,
// AlmaLinux-9-…) or its codename alone (jammy-server-cloudimg-…).
func (db *OSDB) MatchImage(name, arch string) *OSVariant {
	lower := strings.ToLower(name)
	tokens := nameSplit.Split(lower, -1)
	hasToken := func(s string) bool { return s != "" && slices.Contains(tokens, s) }
	var best *OSVariant
	score := 0
	for _, v := range db.list {
		s := 0
		for _, im := range v.images {
			if strings.EqualFold(im.file, name) && (im.arch == arch || im.arch == "") {
				s = 1000
			}
		}
		code, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(v.Codename)), " ")
		distro := strings.ToLower(v.Distro)
		switch {
		case s > 0:
		case distro != "" && strings.Contains(lower, distro) && v.Version != "" && versionIn(lower, v.Version):
			s = 100 + len(v.Version)
		case distro != "" && strings.Contains(lower, distro) && len(code) >= 4 && hasToken(code):
			s = 90
		case len(code) >= 4 && hasToken(code) && len(v.images) > 0:
			s = 50 // a codename alone, for an OS that has cloud images
		}
		if s > score || s == score && s > 0 && best != nil && best.EOL != "" && v.EOL == "" {
			best, score = v, s
		}
	}
	return best
}

// versionIn says whether version is in name as a number of its own: 9 in
// almalinux-9-genericcloud, not in debian-9.13 when asked for 9.1.
func versionIn(name, version string) bool {
	re := regexp.MustCompile(`(^|[^0-9.])` + regexp.QuoteMeta(version) + `($|[^0-9])`)
	return re.MatchString(name)
}

// ISOVolumeID reads the volume ID of an ISO volume: 32 bytes of its primary
// volume descriptor, at 32 KiB, fetched through libvirt's stream.
func ISOVolumeID(ctx context.Context, sess *conn.Session, pool, name string) (string, error) {
	var buf bytes.Buffer
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		v, err := volume(lv, pool, name)
		if err != nil {
			return err
		}
		return lv.StorageVolDownload(v, &buf, 16*sector, sector, 0)
	})
	if err != nil {
		return "", err
	}
	b := buf.Bytes()
	if len(b) < 72 || string(b[1:6]) != "CD001" {
		return "", fmt.Errorf("%s/%s is not an ISO 9660 image", pool, name)
	}
	return strings.TrimRight(string(b[40:72]), " \x00"), nil
}

// DetectOS recognises what a volume holds: an ISO by its volume ID, a disk
// image by its name.
func DetectOS(ctx context.Context, sess *conn.Session, db *OSDB, h Host, pool, name string) (Detected, error) {
	out := Detected{Source: db.Source}
	var v *OSVariant
	if strings.HasSuffix(strings.ToLower(name), ".iso") {
		id, err := ISOVolumeID(ctx, sess, pool, name)
		if err != nil {
			return out, err
		}
		out.VolumeID, out.By = id, "volume-id"
		v = db.MatchISO(id, h.Arch)
	} else {
		out.By = "name"
		v = db.MatchImage(name, h.Arch)
	}
	if v != nil {
		r := v.For(h)
		out.Variant = &r
	}
	return out, nil
}

// osinfoMetadata is the <metadata> libosinfo and virt-manager put in a
// definition to say which OS it runs.
func osinfoMetadata(id string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(id))
	return `<libosinfo:libosinfo xmlns:libosinfo="http://libosinfo.org/xmlns/libvirt/domain/1.0"><libosinfo:os id="` +
		strings.ReplaceAll(b.String(), `"`, "&#34;") + `"/></libosinfo:libosinfo>`
}
