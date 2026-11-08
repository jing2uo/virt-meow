package hv

import (
	"strings"
	"testing"
	"time"

	"libvirt.org/go/libvirtxml"
)

func TestSnapshotTree(t *testing.T) {
	at := func(s int) time.Time { return time.Unix(1_800_000_000+int64(s), 0) }
	// As libvirt may list them: s3 and s2 taken in the same second, a
	// child before its parent, one whose parent is gone.
	all := []Snapshot{
		{Name: "s3", Parent: "s1", Created: at(5)},
		{Name: "s4", Parent: "s3", Created: at(6)},
		{Name: "s2", Parent: "s1", Created: at(5)},
		{Name: "s1", Created: at(1)},
		{Name: "orphan", Parent: "gone", Created: at(9)},
	}
	var got []string
	for _, s := range snapshotTree(all) {
		got = append(got, strings.Repeat(" ", s.Depth)+s.Name)
	}
	if want := "s1| s2| s3|  s4|orphan"; strings.Join(got, "|") != want {
		t.Errorf("tree %q, want %q", strings.Join(got, "|"), want)
	}
}

func TestSnapshottable(t *testing.T) {
	disk := func(dev, format string) libvirtxml.DomainDisk {
		return libvirtxml.DomainDisk{Device: "disk", Driver: &libvirtxml.DomainDiskDriver{Type: format},
			Source: &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: "/x/" + dev}},
			Target: &libvirtxml.DomainDiskTarget{Dev: dev}}
	}
	cd := libvirtxml.DomainDisk{Device: "cdrom", Target: &libvirtxml.DomainDiskTarget{Dev: "sda"}}
	for what, c := range map[string]struct {
		disks  []libvirtxml.DomainDisk
		active bool
		err    string
	}{
		"qcow2 and a CD/DVD":         {disks: []libvirtxml.DomainDisk{disk("vda", "qcow2"), cd}},
		"running, qcow2":             {disks: []libvirtxml.DomainDisk{disk("vda", "qcow2")}, active: true},
		"a raw disk":                 {disks: []libvirtxml.DomainDisk{disk("vda", "qcow2"), disk("vdb", "raw")}, err: "vdb (raw)"},
		"no disk":                    {err: "no writable qcow2 disk"},
		"running without a disk":     {active: true, err: "no writable qcow2 disk"},
		"running with a CD/DVD only": {disks: []libvirtxml.DomainDisk{cd}, active: true, err: "no writable qcow2 disk"},
	} {
		d := libvirtxml.Domain{Name: "m", Devices: &libvirtxml.DomainDeviceList{Disks: c.disks}}
		_, _, err := snapshottable(&d, c.active)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", what, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: %v, want %q", what, err, c.err)
		}
	}
}
