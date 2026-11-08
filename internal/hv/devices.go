package hv

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Disk is one of a machine's disks or CD/DVD drives.
type Disk struct {
	Target   string `json:"target"` // vda, sdb, …
	Bus      string `json:"bus"`
	Device   string `json:"device"` // disk, cdrom, floppy, lun
	Source   string `json:"source"` // pool/volume, a path, protocol:name; "" for an empty drive
	Format   string `json:"format,omitempty"`
	Capacity uint64 `json:"capacity,omitempty"` // bytes, when virt-meow could read it
	ReadOnly bool   `json:"readonly,omitempty"`
	Boot     uint   `json:"boot,omitempty"` // boot order, 0 when it isn't booted from
}

// NIC is one of a machine's network interfaces.
type NIC struct {
	MAC    string `json:"mac"`
	Kind   string `json:"kind"` // bridge, network, user, direct, …
	Source string `json:"source"`
	Model  string `json:"model"`
	Link   string `json:"link"` // up or down
	Boot   uint   `json:"boot,omitempty"`
}

// Devices are a machine's devices, as its definition (what it boots with
// next) has them.
type Devices struct {
	UUID     string     `json:"uuid"`
	Name     string     `json:"name"`
	Active   bool       `json:"active"`
	Disks    []Disk     `json:"disks"`
	NICs     []NIC      `json:"nics"`
	Video    []Video    `json:"video"`
	Graphics []Graphics `json:"graphics"`
	TPMs     []TPM      `json:"tpm"`
	Sound    []string   `json:"sound"` // models
	RNGs     []RNG      `json:"rng"`
	// HostDevs are the host's USB and PCI devices the machine is given.
	HostDevs []AttachedHostDevice `json:"hostdevs"`
	// Filesystems are the host's folders it is given (virtiofs).
	Filesystems []Filesystem `json:"filesystems"`
	// Clipboard says it shares its clipboard with the console (a
	// qemu-vdagent channel); the guest needs spice-vdagent.
	Clipboard bool `json:"clipboard"`
	BootMenu  bool `json:"boot_menu"`
	// BootDevs is the older, per-type boot order (<os><boot dev='hd'/>);
	// empty when the devices carry their own order.
	BootDevs []string `json:"boot_devs,omitempty"`
}

// GetDevices reads a machine's devices, with each disk's size (from the
// running machine when it runs, from its volume otherwise) and the ports its
// consoles got when it runs.
func GetDevices(ctx context.Context, sess *conn.Session, uuid string) (Devices, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return Devices{}, err
	}
	var out Devices
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		cur, err := definition(lv, dom)
		if err != nil {
			return err
		}
		var d libvirtxml.Domain
		if err := d.Unmarshal(cur.XML); err != nil {
			return err
		}
		out, err = readDevices(lv, dom, cur, &d)
		return err
	})
	return out, err
}

// readDevices reads the devices of the machine dom, whose definition cur
// parses to d.
func readDevices(lv *libvirt.Libvirt, dom libvirt.Domain, cur Definition, d *libvirtxml.Domain) (Devices, error) {
	out := Devices{UUID: cur.UUID, Name: cur.Name, Active: cur.Active, Disks: []Disk{}, NICs: []NIC{}}
	var live *libvirtxml.Domain
	if cur.Active {
		if x, err := lv.DomainGetXMLDesc(dom, 0); err == nil {
			live = &libvirtxml.Domain{}
			if live.Unmarshal(x) != nil {
				live = nil
			}
		}
	}
	peripherals(d, live, &out)
	out.HostDevs = attachedHostDevices(d)
	out.Filesystems = filesystems(d)
	if d.OS != nil {
		out.BootMenu = d.OS.BootMenu != nil && d.OS.BootMenu.Enable == "yes"
		for _, b := range d.OS.BootDevices {
			out.BootDevs = append(out.BootDevs, b.Dev)
		}
	}
	if d.Devices == nil {
		return out, nil
	}
	for _, dk := range d.Devices.Disks {
		x := Disk{Device: cmpOr(dk.Device, "disk"), Source: diskSource(dk), ReadOnly: dk.ReadOnly != nil, Boot: bootOrder(dk.Boot)}
		if dk.Target != nil {
			x.Target, x.Bus = dk.Target.Dev, dk.Target.Bus
		}
		if dk.Driver != nil {
			x.Format = dk.Driver.Type
		}
		if x.Source != "" {
			if cur.Active {
				if _, c, _, err := lv.DomainGetBlockInfo(dom, x.Target, 0); err == nil {
					x.Capacity = c
				}
			} else if v, err := diskVolume(lv, dk); err == nil {
				if _, c, _, err := lv.StorageVolGetInfo(v); err == nil {
					x.Capacity = c
				}
			}
		}
		out.Disks = append(out.Disks, x)
	}
	for _, i := range d.Devices.Interfaces {
		n := NIC{Link: "up", Boot: bootOrder(i.Boot)}
		n.Kind, n.Source = nicSource(i)
		if i.MAC != nil {
			n.MAC = i.MAC.Address
		}
		if i.Model != nil {
			n.Model = i.Model.Type
		}
		if i.Link != nil && i.Link.State != "" {
			n.Link = i.Link.State
		}
		out.NICs = append(out.NICs, n)
	}
	return out, nil
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func bootOrder(b *libvirtxml.DomainDeviceBoot) uint {
	if b == nil {
		return 0
	}
	return b.Order
}

func diskSource(d libvirtxml.DomainDisk) string {
	s := d.Source
	switch {
	case s == nil:
		return ""
	case s.Volume != nil:
		return s.Volume.Pool + "/" + s.Volume.Volume
	case s.File != nil:
		return s.File.File
	case s.Block != nil:
		return s.Block.Dev
	case s.Network != nil:
		return s.Network.Protocol + ":" + s.Network.Name
	}
	return ""
}

func nicSource(i libvirtxml.DomainInterface) (kind, source string) {
	s := i.Source
	switch {
	case s == nil:
		return "", ""
	case s.Bridge != nil:
		return "bridge", s.Bridge.Bridge
	case s.Network != nil:
		return "network", s.Network.Network
	case s.User != nil:
		return "user", ""
	case s.Direct != nil:
		return "direct", s.Direct.Dev
	}
	return "other", ""
}

// diskVolume is the storage volume behind a disk, when it is one.
func diskVolume(lv *libvirt.Libvirt, d libvirtxml.DomainDisk) (libvirt.StorageVol, error) {
	s := d.Source
	switch {
	case s != nil && s.Volume != nil:
		p, err := lv.StoragePoolLookupByName(s.Volume.Pool)
		if err != nil {
			return libvirt.StorageVol{}, err
		}
		return lv.StorageVolLookupByName(p, s.Volume.Volume)
	case s != nil && s.File != nil:
		return lv.StorageVolLookupByPath(s.File.File)
	case s != nil && s.Block != nil:
		return lv.StorageVolLookupByPath(s.Block.Dev)
	}
	return libvirt.StorageVol{}, errors.New("not a storage volume")
}

// targets are the disk targets a machine uses, in its definition and, when
// it runs, on the running machine.
func (e *edit) targets() (map[string]bool, error) {
	used := map[string]bool{}
	add := func(d *libvirtxml.Domain) {
		if d.Devices == nil {
			return
		}
		for _, dk := range d.Devices.Disks {
			if dk.Target != nil {
				used[dk.Target.Dev] = true
			}
		}
	}
	add(e.def)
	if e.active {
		x, err := e.lv.DomainGetXMLDesc(e.dom, 0)
		if err != nil {
			return nil, err
		}
		var live libvirtxml.Domain
		if err := live.Unmarshal(x); err != nil {
			return nil, err
		}
		add(&live)
	}
	return used, nil
}

// nextTarget is the first free target name with prefix: vda…vdz, vdaa….
func nextTarget(prefix string, used map[string]bool) string {
	for i := 0; ; i++ {
		name, n := "", i
		for {
			name = string(rune('a'+n%26)) + name
			n = n/26 - 1
			if n < 0 {
				break
			}
		}
		if !used[prefix+name] {
			return prefix + name
		}
	}
}

func targetPrefix(bus string) string {
	switch bus {
	case "virtio":
		return "vd"
	case "ide":
		return "hd"
	}
	return "sd"
}

func (e *edit) disk(target string) (int, error) {
	if e.def.Devices != nil {
		for i, d := range e.def.Devices.Disks {
			if d.Target != nil && d.Target.Dev == target {
				return i, nil
			}
		}
	}
	return -1, fmt.Errorf("%s has no disk %s", e.def.Name, target)
}

// attach adds a device to the definition through libvirt, and to the
// running machine once the definition is saved.
func (e *edit) attach(what string, dev interface{ Marshal() (string, error) }) error {
	x, err := dev.Marshal()
	if err != nil {
		return err
	}
	if err := e.lv.DomainAttachDeviceFlags(e.dom, x, uint32(libvirt.DomainAffectConfig)); err != nil {
		return fmt.Errorf("libvirt refused %s: %w", what, err)
	}
	e.saved(what, func() error { return e.lv.DomainAttachDeviceFlags(e.dom, x, uint32(libvirt.DomainAffectLive)) })
	return nil
}

func (e *edit) detach(what string, dev interface{ Marshal() (string, error) }) error {
	x, err := dev.Marshal()
	if err != nil {
		return err
	}
	if err := e.lv.DomainDetachDeviceFlags(e.dom, x, uint32(libvirt.DomainAffectConfig)); err != nil {
		return fmt.Errorf("libvirt refused removing %s: %w", what, err)
	}
	// The guest is asked to let go of it; libvirt waits a few seconds for
	// the answer and leaves the request pending if there is none.
	e.saved(what+" removed", func() error { return e.lv.DomainDetachDeviceFlags(e.dom, x, uint32(libvirt.DomainAffectLive)) })
	return nil
}

func (e *edit) update(what string, dev interface{ Marshal() (string, error) }) error {
	x, err := dev.Marshal()
	if err != nil {
		return err
	}
	if err := e.lv.DomainUpdateDeviceFlags(e.dom, x, libvirt.DomainDeviceModifyFlags(libvirt.DomainAffectConfig)); err != nil {
		return fmt.Errorf("libvirt refused %s: %w", what, err)
	}
	e.saved(what, func() error {
		return e.lv.DomainUpdateDeviceFlags(e.dom, x, libvirt.DomainDeviceModifyFlags(libvirt.DomainAffectLive))
	})
	return nil
}

// NewDisk is a disk to add: a new volume (Pool, SizeGiB), an existing one
// (VolPool, Volume), or a file or block device on the host (Path, Format).
type NewDisk struct {
	Pool    string `json:"pool,omitempty"`
	SizeGiB int    `json:"size_gib,omitempty"`
	VolPool string `json:"vol_pool,omitempty"`
	Volume  string `json:"volume,omitempty"`
	Path    string `json:"path,omitempty"`
	Format  string `json:"format,omitempty"` // for Path: raw or qcow2
	Bus     string `json:"bus,omitempty"`    // virtio, sata, scsi, usb; default: the bus of its first disk
}

// AddDisk adds a disk; a new volume is named <machine>-<target>.qcow2. A
// running machine gets it now where its bus can take it (virtio, SCSI, USB;
// not SATA).
func AddDisk(ctx context.Context, sess *conn.Session, uuid string, nd NewDisk) (Edited, error) {
	n := 0
	for _, set := range []bool{nd.Pool != "", nd.Volume != "", nd.Path != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		return Edited{}, errors.New("one disk: a new volume, an existing one, or a path")
	}
	return modify(ctx, sess, uuid, func(e *edit) error {
		lv := e.lv
		bus := nd.Bus
		if bus == "" {
			bus = "virtio"
			if e.def.Devices != nil {
				for _, d := range e.def.Devices.Disks {
					if d.Device == "disk" && d.Target != nil && d.Target.Bus != "" {
						bus = d.Target.Bus
						break
					}
				}
			}
		}
		if !slices.Contains([]string{"virtio", "sata", "scsi", "usb"}, bus) {
			return fmt.Errorf("bus %q: virtio, sata, scsi or usb", bus)
		}
		used, err := e.targets()
		if err != nil {
			return err
		}
		dev := nextTarget(targetPrefix(bus), used)
		disk := libvirtxml.DomainDisk{Device: "disk", Target: &libvirtxml.DomainDiskTarget{Dev: dev, Bus: bus}}
		var made *libvirt.StorageVol
		var label string
		switch {
		case nd.Pool != "":
			if nd.SizeGiB < 1 || nd.SizeGiB > 64<<10 {
				return errors.New("disk size: 1 GiB to 64 TiB")
			}
			p, err := lv.StoragePoolLookupByName(nd.Pool)
			if err != nil {
				return fmt.Errorf("pool %s: %w", nd.Pool, err)
			}
			name := e.def.Name + "-" + dev + ".qcow2"
			if _, err := lv.StorageVolLookupByName(p, name); err == nil {
				return fmt.Errorf("pool %s already has a volume %s: add it as an existing disk, or delete it", nd.Pool, name)
			}
			vx, err := (&libvirtxml.StorageVolume{
				Name:     name,
				Capacity: &libvirtxml.StorageVolumeSize{Unit: "GiB", Value: uint64(nd.SizeGiB)},
				Target:   &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: "qcow2"}},
			}).Marshal()
			if err != nil {
				return err
			}
			v, err := lv.StorageVolCreateXML(p, vx, libvirt.StorageVolCreatePreallocMetadata)
			if err != nil {
				return fmt.Errorf("creating the disk: %w", err)
			}
			made = &v
			disk.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "qcow2", Discard: "unmap"}
			disk.Source = &libvirtxml.DomainDiskSource{Volume: &libvirtxml.DomainDiskSourceVolume{Pool: nd.Pool, Volume: name}}
			label = fmt.Sprintf("%s/%s, %d GiB, new", nd.Pool, name, nd.SizeGiB)
		case nd.Volume != "":
			p, err := lv.StoragePoolLookupByName(nd.VolPool)
			if err != nil {
				return fmt.Errorf("pool %s: %w", nd.VolPool, err)
			}
			sv, err := lv.StorageVolLookupByName(p, nd.Volume)
			if err != nil {
				return fmt.Errorf("%s/%s: %w", nd.VolPool, nd.Volume, err)
			}
			v, err := volumeOf(lv, sv)
			if err != nil {
				return err
			}
			if v.IsISO() {
				return fmt.Errorf("%s/%s is an ISO: put it in a CD/DVD drive (virt-mm cdrom insert)", nd.VolPool, nd.Volume)
			}
			if e.def.Devices != nil {
				for _, d := range e.def.Devices.Disks {
					if diskSource(d) == nd.VolPool+"/"+nd.Volume || diskSource(d) == v.Path {
						return fmt.Errorf("%s already has %s/%s as %s", e.def.Name, nd.VolPool, nd.Volume, d.Target.Dev)
					}
				}
			}
			// Without the format libvirt reads the volume as raw: a qcow2
			// image would reach the guest as its container's bytes.
			format := v.Format
			if format != "qcow2" {
				format = "raw"
			}
			disk.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu", Type: format}
			disk.Source = &libvirtxml.DomainDiskSource{Volume: &libvirtxml.DomainDiskSourceVolume{Pool: nd.VolPool, Volume: nd.Volume}}
			label = fmt.Sprintf("%s/%s, %s", nd.VolPool, nd.Volume, format)
		default:
			if nd.Format != "raw" && nd.Format != "qcow2" {
				return errors.New("a path needs its format: raw or qcow2")
			}
			disk.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu", Type: nd.Format}
			disk.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: nd.Path}}
			if strings.HasPrefix(nd.Path, "/dev/") {
				disk.Source = &libvirtxml.DomainDiskSource{Block: &libvirtxml.DomainDiskSourceBlock{Dev: nd.Path}}
			}
			label = fmt.Sprintf("%s, %s", nd.Path, nd.Format)
		}
		undo := func(cause error) error {
			if cause != nil && made != nil {
				if err := lv.StorageVolDelete(*made, 0); err != nil {
					return fmt.Errorf("%w (and removing the new volume failed: %v)", cause, err)
				}
			}
			return cause
		}
		if bus == "scsi" && !e.hasController("scsi") {
			c := libvirtxml.DomainController{Type: "scsi", Model: "virtio-scsi"}
			if err := e.attach("SCSI controller (virtio-scsi)", &c); err != nil {
				return undo(err)
			}
		}
		return undo(e.attach(fmt.Sprintf("disk %s on %s: %s", dev, bus, label), &disk))
	})
}

func (e *edit) hasController(kind string) bool {
	if e.def.Devices == nil {
		return false
	}
	return slices.ContainsFunc(e.def.Devices.Controllers, func(c libvirtxml.DomainController) bool { return c.Type == kind })
}

// RemoveDisk removes a disk or drive. With deleteVolume its volume goes too,
// which only a machine that is off allows: a running one may still hold it
// open while the guest lets go.
func RemoveDisk(ctx context.Context, sess *conn.Session, uuid, target string, deleteVolume bool) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		i, err := e.disk(target)
		if err != nil {
			return err
		}
		d := e.def.Devices.Disks[i]
		var vol libvirt.StorageVol
		if deleteVolume {
			if e.active {
				return fmt.Errorf("%s is running: remove the disk now and delete its volume once it is off", e.def.Name)
			}
			if d.Device == "cdrom" || d.ReadOnly != nil || d.Shareable != nil {
				return fmt.Errorf("%s is read-only or shared: its volume isn't this machine's to delete", target)
			}
			if vol, err = diskVolume(e.lv, d); err != nil {
				return fmt.Errorf("%s (%s) is not a volume in a storage pool: remove it without deleting", target, diskSource(d))
			}
		}
		what := fmt.Sprintf("%s %s", map[bool]string{true: "drive", false: "disk"}[d.Device == "cdrom"], target)
		if err := e.detach(what, &d); err != nil {
			return err
		}
		if deleteVolume {
			e.outside("deleted "+diskSource(d), func() error { return e.lv.StorageVolDelete(vol, 0) })
		}
		return nil
	})
}

// ResizeDisk grows a disk to size bytes: through the running machine, which
// tells the guest, or its volume when it is off. The guest's partitions and
// file systems are the guest's to grow.
func ResizeDisk(ctx context.Context, sess *conn.Session, uuid, target string, size uint64) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		i, err := e.disk(target)
		if err != nil {
			return err
		}
		d := e.def.Devices.Disks[i]
		if d.Device != "disk" {
			return fmt.Errorf("%s is a %s, not a disk", target, d.Device)
		}
		var have uint64
		var grow func() error
		if e.active {
			if _, have, _, err = e.lv.DomainGetBlockInfo(e.dom, target, 0); err != nil {
				return err
			}
			grow = func() error { return e.lv.DomainBlockResize(e.dom, target, size, libvirt.DomainBlockResizeBytes) }
		} else {
			vol, err := diskVolume(e.lv, d)
			if err != nil {
				return fmt.Errorf("%s (%s) is not a volume in a storage pool", target, diskSource(d))
			}
			if _, have, _, err = e.lv.StorageVolGetInfo(vol); err != nil {
				return err
			}
			grow = func() error { return e.lv.StorageVolResize(vol, size, 0) }
		}
		switch {
		case size < have:
			return fmt.Errorf("%s is %s: shrinking a disk loses what is past the new end, so virt-meow only grows them", target, bytesText(have))
		case size == have:
			return nil
		}
		e.outside(fmt.Sprintf("disk %s %s → %s", target, bytesText(have), bytesText(size)), grow)
		return nil
	})
}

func bytesText(b uint64) string {
	if b%(1<<30) == 0 {
		return fmt.Sprintf("%d GiB", b>>30)
	}
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d MiB", b>>20)
}

// cdrom finds a CD/DVD drive: target's, or the first.
func (e *edit) cdrom(target string) int {
	if e.def.Devices == nil {
		return -1
	}
	for i, d := range e.def.Devices.Disks {
		if d.Device == "cdrom" && (target == "" || (d.Target != nil && d.Target.Dev == target)) {
			return i
		}
	}
	return -1
}

// InsertCD puts an ISO volume in a CD/DVD drive (target's, or the first);
// a machine without one gets a drive, which a running machine has from its
// next start.
func InsertCD(ctx context.Context, sess *conn.Session, uuid, target, pool, volume string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		p, err := e.lv.StoragePoolLookupByName(pool)
		if err != nil {
			return fmt.Errorf("pool %s: %w", pool, err)
		}
		if _, err := e.lv.StorageVolLookupByName(p, volume); err != nil {
			return fmt.Errorf("%s/%s: %w", pool, volume, err)
		}
		src := &libvirtxml.DomainDiskSource{Volume: &libvirtxml.DomainDiskSourceVolume{Pool: pool, Volume: volume}}
		drv := &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"}
		if i := e.cdrom(target); i >= 0 {
			d := e.def.Devices.Disks[i]
			d.Source, d.Driver = src, drv
			return e.update(fmt.Sprintf("%s: %s/%s inserted", d.Target.Dev, pool, volume), &d)
		}
		if target != "" {
			return fmt.Errorf("%s has no CD/DVD drive %s", e.def.Name, target)
		}
		bus := "ide"
		if e.def.OS != nil && e.def.OS.Type != nil && strings.Contains(e.def.OS.Type.Machine, "q35") {
			bus = "sata"
		}
		used, err := e.targets()
		if err != nil {
			return err
		}
		dev := nextTarget(targetPrefix(bus), used)
		d := libvirtxml.DomainDisk{Device: "cdrom", Driver: drv, Source: src, ReadOnly: &libvirtxml.DomainDiskReadOnly{},
			Target: &libvirtxml.DomainDiskTarget{Dev: dev, Bus: bus}}
		return e.attach(fmt.Sprintf("CD/DVD drive %s on %s: %s/%s", dev, bus, pool, volume), &d)
	})
}

// EjectCD empties a CD/DVD drive (target's, or the first).
func EjectCD(ctx context.Context, sess *conn.Session, uuid, target string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		i := e.cdrom(target)
		if i < 0 {
			return fmt.Errorf("%s has no CD/DVD drive %s", e.def.Name, target)
		}
		d := e.def.Devices.Disks[i]
		if d.Source == nil {
			return nil
		}
		was := diskSource(d)
		d.Source = nil
		return e.update(fmt.Sprintf("%s: %s ejected", d.Target.Dev, was), &d)
	})
}

// NewNIC is an interface to add: Kind bridge, network or user, with its
// Source (the bridge or network); Model and MAC are chosen when empty.
type NewNIC struct {
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	Model  string `json:"model,omitempty"`
	MAC    string `json:"mac,omitempty"`
}

func (e *edit) nic(mac string) (int, error) {
	if e.def.Devices != nil {
		for i, n := range e.def.Devices.Interfaces {
			if n.MAC != nil && strings.EqualFold(n.MAC.Address, mac) {
				return i, nil
			}
		}
	}
	return -1, fmt.Errorf("%s has no interface with the MAC %s", e.def.Name, mac)
}

// AddNIC adds a network interface. Its MAC is fixed here, so that the
// definition and the running machine get the same one.
func AddNIC(ctx context.Context, sess *conn.Session, uuid string, n NewNIC) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		i := libvirtxml.DomainInterface{}
		switch n.Kind {
		case "bridge":
			i.Source = &libvirtxml.DomainInterfaceSource{Bridge: &libvirtxml.DomainInterfaceSourceBridge{Bridge: n.Source}}
		case "network":
			i.Source = &libvirtxml.DomainInterfaceSource{Network: &libvirtxml.DomainInterfaceSourceNetwork{Network: n.Source}}
		case "user":
			i.Source = &libvirtxml.DomainInterfaceSource{User: &libvirtxml.DomainInterfaceSourceUser{}}
		default:
			return errors.New("network: bridge, network or user")
		}
		if n.Kind != "user" && n.Source == "" {
			return fmt.Errorf("network: which %s", n.Kind)
		}
		// libvirt defines an interface on a network it doesn't have, and
		// the machine then fails to start.
		if n.Kind == "network" {
			if _, err := e.lv.NetworkLookupByName(n.Source); err != nil {
				return fmt.Errorf("no virtual network %q", n.Source)
			}
		}
		model := n.Model
		if model == "" {
			model = "virtio"
			if e.def.Devices != nil && len(e.def.Devices.Interfaces) > 0 && e.def.Devices.Interfaces[0].Model != nil {
				model = e.def.Devices.Interfaces[0].Model.Type
			}
		}
		i.Model = &libvirtxml.DomainInterfaceModel{Type: model}
		mac := strings.ToLower(n.MAC)
		if mac == "" {
			for {
				b := make([]byte, 3)
				rand.Read(b)
				mac = fmt.Sprintf("52:54:00:%02x:%02x:%02x", b[0], b[1], b[2])
				if _, err := e.nic(mac); err != nil {
					break
				}
			}
		} else if hw, err := net.ParseMAC(mac); err != nil || len(hw) != 6 {
			return fmt.Errorf("MAC %q: six hex bytes, 52:54:00:12:34:56", n.MAC)
		} else if _, err := e.nic(mac); err == nil {
			return fmt.Errorf("%s already has the MAC %s", e.def.Name, mac)
		}
		i.MAC = &libvirtxml.DomainInterfaceMAC{Address: mac}
		what := fmt.Sprintf("interface %s: %s %s, %s", mac, n.Kind, n.Source, model)
		if n.Kind == "user" {
			what = fmt.Sprintf("interface %s: user, %s", mac, model)
		}
		return e.attach(what, &i)
	})
}

// RemoveNIC removes the interface with the MAC mac.
func RemoveNIC(ctx context.Context, sess *conn.Session, uuid, mac string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		i, err := e.nic(mac)
		if err != nil {
			return err
		}
		n := e.def.Devices.Interfaces[i]
		return e.detach("interface "+n.MAC.Address, &n)
	})
}

// SetLink plugs (up) or unplugs (down) an interface's virtual cable.
func SetLink(ctx context.Context, sess *conn.Session, uuid, mac string, up bool) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		i, err := e.nic(mac)
		if err != nil {
			return err
		}
		n := e.def.Devices.Interfaces[i]
		state := map[bool]string{true: "up", false: "down"}[up]
		if (n.Link == nil && up) || (n.Link != nil && n.Link.State == state) {
			return nil
		}
		n.Link = &libvirtxml.DomainInterfaceLink{State: state}
		return e.update(fmt.Sprintf("interface %s link %s", n.MAC.Address, state), &n)
	})
}

// SetBoot sets the boot order: devs are disk targets and interface MACs,
// first to last; the devices not named aren't booted from. menu, when not
// nil, turns the firmware's boot menu on or off. It takes effect at the
// next start.
func SetBoot(ctx context.Context, sess *conn.Session, uuid string, devs []string, menu *bool) (Edited, error) {
	if len(devs) == 0 && menu == nil {
		return Edited{}, errors.New("nothing to change")
	}
	return modify(ctx, sess, uuid, func(e *edit) error {
		d := e.def
		if menu != nil {
			on := map[bool]string{true: "yes", false: "no"}[*menu]
			if d.OS == nil {
				d.OS = &libvirtxml.DomainOS{}
			}
			if d.OS.BootMenu == nil || d.OS.BootMenu.Enable != on {
				d.OS.BootMenu = &libvirtxml.DomainBootMenu{Enable: on}
				e.changed("boot menu "+map[bool]string{true: "on", false: "off"}[*menu], nil)
			}
		}
		if len(devs) == 0 {
			return nil
		}
		if d.Devices == nil {
			return errors.New("the machine has no devices to boot from")
		}
		order := map[string]uint{}
		for i, dev := range devs {
			k := strings.ToLower(dev)
			if _, dup := order[k]; dup {
				return fmt.Errorf("%s named twice", dev)
			}
			order[k] = uint(i + 1)
		}
		boot := func(key string) *libvirtxml.DomainDeviceBoot {
			o, ok := order[strings.ToLower(key)]
			if !ok {
				return nil
			}
			delete(order, strings.ToLower(key))
			return &libvirtxml.DomainDeviceBoot{Order: o}
		}
		before := bootList(d)
		for i := range d.Devices.Disks {
			if t := d.Devices.Disks[i].Target; t != nil {
				d.Devices.Disks[i].Boot = boot(t.Dev)
			}
		}
		for i := range d.Devices.Interfaces {
			if m := d.Devices.Interfaces[i].MAC; m != nil {
				d.Devices.Interfaces[i].Boot = boot(m.Address)
			} else {
				d.Devices.Interfaces[i].Boot = nil
			}
		}
		// Per-device order replaces every other kind: libvirt takes one.
		cleared := false
		for i := range d.Devices.Hostdevs {
			cleared = cleared || d.Devices.Hostdevs[i].Boot != nil
			d.Devices.Hostdevs[i].Boot = nil
		}
		for i := range d.Devices.RedirDevs {
			cleared = cleared || d.Devices.RedirDevs[i].Boot != nil
			d.Devices.RedirDevs[i].Boot = nil
		}
		for i := range d.Devices.Filesystems {
			cleared = cleared || d.Devices.Filesystems[i].Boot != nil
			d.Devices.Filesystems[i].Boot = nil
		}
		if d.OS != nil {
			d.OS.BootDevices = nil
		}
		if len(order) > 0 {
			var left []string
			for k := range order {
				left = append(left, k)
			}
			slices.Sort(left)
			return fmt.Errorf("%s has no disk or interface %s", d.Name, strings.Join(left, ", "))
		}
		if after := bootList(d); after != before || cleared {
			e.changed(fmt.Sprintf("boot order %s → %s", before, after), nil)
		}
		return nil
	})
}

// bootList describes a definition's boot order.
func bootList(d *libvirtxml.Domain) string {
	if d.OS != nil && len(d.OS.BootDevices) > 0 {
		var devs []string
		for _, b := range d.OS.BootDevices {
			devs = append(devs, b.Dev)
		}
		return strings.Join(devs, ", ")
	}
	type item struct {
		order uint
		name  string
	}
	var items []item
	if d.Devices != nil {
		for _, dk := range d.Devices.Disks {
			if dk.Boot != nil && dk.Target != nil {
				items = append(items, item{dk.Boot.Order, dk.Target.Dev})
			}
		}
		for _, n := range d.Devices.Interfaces {
			if n.Boot != nil && n.MAC != nil {
				items = append(items, item{n.Boot.Order, n.MAC.Address})
			}
		}
	}
	if len(items) == 0 {
		return "(none)"
	}
	slices.SortFunc(items, func(a, b item) int { return int(a.order) - int(b.order) })
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.name
	}
	return strings.Join(names, ", ")
}
