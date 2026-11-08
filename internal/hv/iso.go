package hv

import (
	"encoding/binary"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
)

// isoFile is a file in the root directory of an ISO image.
type isoFile struct {
	Name string
	Data []byte
}

const sector = 2048

// makeISO writes an ISO 9660 image of files in its root directory, labelled
// label, with Joliet names beside the 8.3 ones: Linux (and cloud-init with
// it) reads "user-data" from Joliet, where plain ISO 9660 has USER_DAT. It
// is what genisoimage -J -V label makes for a handful of small files, and
// all a NoCloud seed needs: one directory, no Rock Ridge.
func makeISO(label string, files []isoFile, now time.Time) []byte {
	// Sectors: 16 primary descriptor, 17 Joliet's, 18 the terminator,
	// 19-22 the path tables (L and M, each), 23 and 24 the root
	// directories, then the files.
	const pvdAt, svdAt, endAt, rootAt, jolietRootAt, dataAt = 16, 17, 18, 23, 24, 25
	type placed struct {
		isoFile
		at   uint32
		iso  string // its 8.3 name
		utf2 []byte // its Joliet name, UCS-2 big-endian
	}
	ps := make([]placed, len(files))
	next := uint32(dataAt)
	taken := map[string]bool{}
	for i, f := range files {
		ps[i] = placed{isoFile: f, at: next, iso: isoName(f.Name, taken), utf2: ucs2(f.Name)}
		next += uint32((len(f.Data) + sector - 1) / sector)
	}
	total := next
	img := make([]byte, int(total)*sector)
	put := func(lba uint32, b []byte) { copy(img[int(lba)*sector:], b) }

	date := dirDate(now)
	record := func(name []byte, at, size uint32, dir bool) []byte {
		n := 33 + len(name)
		if len(name)%2 == 0 {
			n++ // records have even lengths
		}
		r := make([]byte, n)
		r[0] = byte(n)
		both32(r[2:], at)
		both32(r[10:], size)
		copy(r[18:], date)
		if dir {
			r[25] = 2
		}
		both16(r[28:], 1)
		r[32] = byte(len(name))
		copy(r[33:], name)
		return r
	}
	directory := func(self uint32, names func(placed) []byte) []byte {
		sorted := slices.Clone(ps)
		slices.SortFunc(sorted, func(a, b placed) int { return strings.Compare(string(names(a)), string(names(b))) })
		d := append(record([]byte{0}, self, sector, true), record([]byte{1}, self, sector, true)...)
		for _, p := range sorted {
			d = append(d, record(names(p), p.at, uint32(len(p.Data)), false)...)
		}
		return d
	}
	put(rootAt, directory(rootAt, func(p placed) []byte { return []byte(p.iso) }))
	put(jolietRootAt, directory(jolietRootAt, func(p placed) []byte { return p.utf2 }))

	// The path tables: the root alone, as directory 1, its own parent.
	pathTable := func(at uint32, order binary.ByteOrder) []byte {
		t := make([]byte, 10)
		t[0] = 1
		order.PutUint32(t[2:], at)
		order.PutUint16(t[6:], 1)
		return t
	}
	put(19, pathTable(rootAt, binary.LittleEndian))
	put(20, pathTable(rootAt, binary.BigEndian))
	put(21, pathTable(jolietRootAt, binary.LittleEndian))
	put(22, pathTable(jolietRootAt, binary.BigEndian))

	descriptor := func(kind byte, root, lpath uint32, text func(string, int) []byte) []byte {
		d := make([]byte, sector)
		d[0] = kind
		copy(d[1:], "CD001")
		d[6] = 1
		copy(d[8:], text("", 32))     // system
		copy(d[40:], text(label, 32)) // volume
		both32(d[80:], total)
		if kind == 2 {
			copy(d[88:], "%/E") // Joliet, UCS-2 level 3
		}
		both16(d[120:], 1)
		both16(d[124:], 1)
		both16(d[128:], sector)
		both32(d[132:], 10)
		binary.LittleEndian.PutUint32(d[140:], lpath)
		binary.BigEndian.PutUint32(d[148:], lpath+1)
		copy(d[156:], record([]byte{0}, root, sector, true))
		for _, f := range [][2]int{{190, 128}, {318, 128}, {446, 128}, {702, 37}, {739, 37}, {776, 37}} {
			copy(d[f[0]:], text("", f[1]))
		}
		copy(d[574:], text("VIRT_MM", 128)) // application
		stamp := []byte(now.UTC().Format("20060102150405") + "00\x00")
		copy(d[813:], stamp)
		copy(d[830:], stamp)
		copy(d[847:], "0000000000000000\x00")
		copy(d[864:], "0000000000000000\x00")
		d[881] = 1
		return d
	}
	ascii := func(s string, n int) []byte { return []byte((s + strings.Repeat(" ", n))[:n]) }
	wide := func(s string, n int) []byte {
		b := ucs2(s)
		for len(b) < n {
			b = append(b, 0, ' ')
		}
		return b[:n]
	}
	put(pvdAt, descriptor(1, rootAt, 19, ascii))
	put(svdAt, descriptor(2, jolietRootAt, 21, wide))
	end := make([]byte, 7)
	end[0] = 255
	copy(end[1:], "CD001")
	end[6] = 1
	put(endAt, end)

	for _, p := range ps {
		put(p.at, p.Data)
	}
	return img
}

// isoName is name as ISO 9660 level 1 has it: NAME8.EXT;1 in capitals,
// digits and '_', unique among taken.
func isoName(name string, taken map[string]bool) string {
	clean := func(s string, n int) string {
		var b strings.Builder
		for _, r := range strings.ToUpper(s) {
			if b.Len() == n {
				break
			}
			if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	base, ext := name, ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		base, ext = name[:i], name[i+1:]
	}
	b, e := clean(base, 8), clean(ext, 3)
	n := b + "." + e + ";1"
	for i := 1; taken[n] && i < 10; i++ {
		n = (b + "________")[:7] + string(rune('0'+i)) + "." + e + ";1"
	}
	taken[n] = true
	return n
}

func ucs2(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u>>8), byte(u))
	}
	return b
}

// dirDate is t as a directory record has it.
func dirDate(t time.Time) []byte {
	t = t.UTC()
	return []byte{byte(t.Year() - 1900), byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second()), 0}
}

// both32 and both16 write v little-endian then big-endian, as ISO 9660
// writes its numbers.
func both32(b []byte, v uint32) {
	binary.LittleEndian.PutUint32(b, v)
	binary.BigEndian.PutUint32(b[4:], v)
}

func both16(b []byte, v uint16) {
	binary.LittleEndian.PutUint16(b, v)
	binary.BigEndian.PutUint16(b[2:], v)
}
