//go:build e2e

package e2e

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

func devices(t *testing.T, s *conn.Session, uuid string) hv.Devices {
	t.Helper()
	d, err := hv.GetDevices(ctx(t), s, uuid)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func diskAt(t *testing.T, d hv.Devices, target string) hv.Disk {
	t.Helper()
	i := slices.IndexFunc(d.Disks, func(k hv.Disk) bool { return k.Target == target })
	if i < 0 {
		t.Fatalf("no disk %s in %+v", target, d.Disks)
	}
	return d.Disks[i]
}

func volumes(t *testing.T, s *conn.Session, pool string) []string {
	t.Helper()
	vs, err := hv.Volumes(ctx(t), s, pool)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range vs {
		names = append(names, v.Name)
	}
	return names
}

// mustEdit(t)(hv.X(…)) is the edit's outcome; an error fails the test.
func mustEdit(t *testing.T) func(hv.Edited, error) hv.Edited {
	return func(e hv.Edited, err error) hv.Edited {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
}

// Disks, CD/DVD media, interfaces and the boot order, on a machine that is
// off and on one that runs.
func TestDevices(t *testing.T) {
	s := qemuSession(t)
	const pool = "e2e-dev"
	scratchPool(t, s, pool)
	iso := bytes.Repeat([]byte("virt-mm"), 100_000)
	for _, name := range []string{"a.iso", "b.iso"} {
		if _, err := hv.Upload(ctx(t), s, pool, name, bytes.NewReader(iso), int64(len(iso)), nil); err != nil {
			t.Fatal(err)
		}
	}
	vx, _ := (&libvirtxml.StorageVolume{Name: "data.qcow2",
		Capacity: &libvirtxml.StorageVolumeSize{Unit: "MiB", Value: 64},
		Target:   &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: "qcow2"}}}).Marshal()
	s.Do(ctx(t), func(lv *libvirt.Libvirt) error {
		p, err := lv.StoragePoolLookupByName(pool)
		if err == nil {
			_, err = lv.StorageVolCreateXML(p, vx, 0)
		}
		return err
	})
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-dev", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: pool, DiskGiB: 1, ISOPool: pool, ISOVolume: "a.iso", NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })

	d := devices(t, s, uuid)
	if cd, vda := diskAt(t, d, "sda"), diskAt(t, d, "vda"); cd.Device != "cdrom" || cd.Boot != 1 || vda.Boot != 2 || vda.Capacity != 1<<30 {
		t.Fatalf("as created: %+v", d.Disks)
	}

	// Off: disks added, refused, grown, removed.
	e := mustEdit(t)(hv.AddDisk(ctx(t), s, uuid, hv.NewDisk{Pool: pool, SizeGiB: 1}))
	if !strings.HasPrefix(e.Changes[0].What, "disk vdb on virtio") || !slices.Contains(volumes(t, s, pool), "e2e-dev-vdb.qcow2") {
		t.Fatalf("new disk: %+v, pool %v", e, volumes(t, s, pool))
	}
	mustEdit(t)(hv.AddDisk(ctx(t), s, uuid, hv.NewDisk{VolPool: pool, Volume: "data.qcow2"}))
	if k := diskAt(t, devices(t, s, uuid), "vdc"); k.Format != "qcow2" || k.Source != pool+"/data.qcow2" {
		t.Fatalf("existing volume: %+v", k)
	}
	e = mustEdit(t)(hv.AddDisk(ctx(t), s, uuid, hv.NewDisk{Pool: pool, SizeGiB: 1, Bus: "scsi"}))
	if len(e.Changes) != 2 || !strings.HasPrefix(e.Changes[0].What, "SCSI controller") || !strings.HasPrefix(e.Changes[1].What, "disk sdb on scsi") {
		t.Fatalf("SCSI disk: %+v", e.Changes)
	}
	for what, bad := range map[string]hv.NewDisk{
		"the same volume twice": {VolPool: pool, Volume: "data.qcow2"},
		"an ISO as a disk":      {VolPool: pool, Volume: "a.iso"},
		"two sources":           {Pool: pool, SizeGiB: 1, VolPool: pool, Volume: "data.qcow2"},
		"a path without format": {Path: "/var/lib/libvirt/images/x.img"},
		"a bus that isn't":      {Pool: pool, SizeGiB: 1, Bus: "floppy"},
		"no such pool":          {Pool: "nope", SizeGiB: 1},
	} {
		if _, err := hv.AddDisk(ctx(t), s, uuid, bad); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	if slices.Contains(volumes(t, s, pool), "e2e-dev-vdd.qcow2") {
		t.Error("a refused disk left its volume")
	}
	mustEdit(t)(hv.ResizeDisk(ctx(t), s, uuid, "vdb", 2<<30))
	if k := diskAt(t, devices(t, s, uuid), "vdb"); k.Capacity != 2<<30 {
		t.Fatalf("grown: %+v", k)
	}
	if _, err := hv.ResizeDisk(ctx(t), s, uuid, "vdb", 1<<30); err == nil {
		t.Error("shrank a disk")
	}
	mustEdit(t)(hv.RemoveDisk(ctx(t), s, uuid, "vdc", false))
	mustEdit(t)(hv.RemoveDisk(ctx(t), s, uuid, "sdb", true))
	if v := volumes(t, s, pool); !slices.Contains(v, "data.qcow2") || slices.Contains(v, "e2e-dev-sdb.qcow2") {
		t.Fatalf("after removing vdc (kept) and sdb (deleted): %v", v)
	}
	if _, err := hv.RemoveDisk(ctx(t), s, uuid, "sda", true); err == nil {
		t.Error("deleted a CD's ISO")
	}

	// Media.
	mustEdit(t)(hv.EjectCD(ctx(t), s, uuid, ""))
	if k := diskAt(t, devices(t, s, uuid), "sda"); k.Source != "" || k.Device != "cdrom" {
		t.Fatalf("ejected: %+v", k)
	}
	mustEdit(t)(hv.InsertCD(ctx(t), s, uuid, "", pool, "b.iso"))
	if k := diskAt(t, devices(t, s, uuid), "sda"); k.Source != pool+"/b.iso" {
		t.Fatalf("inserted: %+v", k)
	}

	// Interfaces.
	e = mustEdit(t)(hv.AddNIC(ctx(t), s, uuid, hv.NewNIC{Kind: "user"}))
	nics := devices(t, s, uuid).NICs
	if len(nics) != 1 || nics[0].Kind != "user" || nics[0].Model != "virtio" || !strings.HasPrefix(nics[0].MAC, "52:54:00:") {
		t.Fatalf("NIC: %+v", nics)
	}
	mac := nics[0].MAC
	if _, err := hv.AddNIC(ctx(t), s, uuid, hv.NewNIC{Kind: "user", MAC: strings.ToUpper(mac)}); err == nil {
		t.Error("added a second NIC with the same MAC")
	}
	if _, err := hv.AddNIC(ctx(t), s, uuid, hv.NewNIC{Kind: "bridge"}); err == nil {
		t.Error("added a bridged NIC without a bridge")
	}
	mustEdit(t)(hv.SetLink(ctx(t), s, uuid, mac, false))
	if n := devices(t, s, uuid).NICs[0]; n.Link != "down" {
		t.Fatalf("link: %+v", n)
	}

	// Boot order: named devices only, in that order.
	menu := true
	e = mustEdit(t)(hv.SetBoot(ctx(t), s, uuid, []string{"vda", strings.ToUpper(mac)}, &menu))
	d = devices(t, s, uuid)
	if diskAt(t, d, "vda").Boot != 1 || d.NICs[0].Boot != 2 || diskAt(t, d, "sda").Boot != 0 || !d.BootMenu {
		t.Fatalf("boot: %+v %+v", d.Disks, d.NICs)
	}
	if _, err := hv.SetBoot(ctx(t), s, uuid, []string{"vda", "vdz"}, nil); err == nil {
		t.Error("boot from a disk it doesn't have")
	}
	if diskAt(t, devices(t, s, uuid), "vda").Boot != 1 {
		t.Error("a refused boot order changed it")
	}

	// Running: hot-plugged into the spare root ports, media changed, a disk
	// grown through QEMU; the boot order waits for the next start.
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	e = mustEdit(t)(hv.AddDisk(ctx(t), s, uuid, hv.NewDisk{Pool: pool, SizeGiB: 1}))
	if !e.Active || !e.Changes[0].Live {
		t.Errorf("hot-plugged disk: %+v", e.Changes)
	}
	e = mustEdit(t)(hv.AddNIC(ctx(t), s, uuid, hv.NewNIC{Kind: "user"}))
	if !e.Changes[0].Live {
		t.Errorf("hot-plugged NIC: %+v", e.Changes)
	}
	if e = mustEdit(t)(hv.EjectCD(ctx(t), s, uuid, "sda")); !e.Changes[0].Live {
		t.Errorf("live eject: %+v", e.Changes)
	}
	if e = mustEdit(t)(hv.ResizeDisk(ctx(t), s, uuid, "vdb", 3<<30)); !e.Changes[0].Live {
		t.Errorf("live resize: %+v", e.Changes)
	}
	if k := diskAt(t, devices(t, s, uuid), "vdb"); k.Capacity != 3<<30 {
		t.Errorf("grown while running: %+v", k)
	}
	if e = mustEdit(t)(hv.SetBoot(ctx(t), s, uuid, []string{"vdb"}, nil)); e.Changes[0].Live {
		t.Errorf("boot order applied live: %+v", e.Changes)
	}
	if _, err := hv.RemoveDisk(ctx(t), s, uuid, "vdb", true); err == nil {
		t.Error("deleted the volume of a running machine's disk")
	}
}
