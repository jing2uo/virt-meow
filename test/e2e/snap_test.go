//go:build e2e

package e2e

import (
	"bytes"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/hv"
)

func snaps(t *testing.T, machine string) []hv.Snapshot {
	t.Helper()
	var ss []hv.Snapshot
	cli(t, &ss, false, "snap", "ls", machine)
	return ss
}

// tree writes snapshots as name@depth, the current one starred.
func tree(ss []hv.Snapshot) string {
	var b strings.Builder
	for _, s := range ss {
		b.WriteString(strings.Repeat(">", s.Depth) + s.Name)
		if s.Current {
			b.WriteString("*")
		}
		b.WriteString(" ")
	}
	return strings.TrimSpace(b.String())
}

// virt-mm snap: internal snapshots of a machine that is off and of one that
// runs, the tree, revert and delete; and the machines that can't have them.
func TestSnapshots(t *testing.T) {
	s := qemuSession(t)
	const pool = "e2e-snap"
	scratchPool(t, s, pool)
	iso := bytes.Repeat([]byte("virt-mm"), 100_000)
	if _, err := hv.Upload(ctx(t), s, pool, "cd.iso", bytes.NewReader(iso), int64(len(iso)), nil); err != nil {
		t.Fatal(err)
	}
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-snap", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: pool, DiskGiB: 1, ISOPool: pool, ISOVolume: "cd.iso", NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if ss := snaps(t, "e2e-snap"); len(ss) != 0 {
		t.Fatalf("new machine: %+v", ss)
	}

	// Off: the disks (the CD/DVD left out).
	var e hv.Edited
	cli(t, &e, false, "snap", "create", "e2e-snap", "s1", "--description", "before")
	if len(e.Changes) != 1 || e.Changes[0].What != "snapshot s1 (disks)" || len(e.Warnings) != 0 {
		t.Fatalf("s1: %+v", e)
	}
	// Running: with its memory.
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	e = mustEdit(t)(hv.CreateSnapshot(ctx(t), s, uuid, "s2", ""))
	if e.Changes[0].What != "snapshot s2 (disks and memory)" {
		t.Fatalf("s2: %+v", e)
	}
	mustEdit(t)(hv.CreateSnapshot(ctx(t), s, uuid, "s3", ""))
	ss := snaps(t, "e2e-snap")
	if got := tree(ss); got != "s1 >s2 >>s3*" {
		t.Fatalf("tree: %s (%+v)", got, ss)
	}
	if ss[0].Description != "before" || ss[0].State != "shutoff" || ss[0].Memory || ss[1].State != "running" || !ss[1].Memory || ss[0].Created.IsZero() {
		t.Fatalf("snapshots: %+v", ss)
	}
	for what, f := range map[string]func() (hv.Edited, error){
		"the same name":  func() (hv.Edited, error) { return hv.CreateSnapshot(ctx(t), s, uuid, "s1", "") },
		"a bad name":     func() (hv.Edited, error) { return hv.CreateSnapshot(ctx(t), s, uuid, "a/b", "") },
		"revert to none": func() (hv.Edited, error) { return hv.RevertSnapshot(ctx(t), s, uuid, "s9") },
		"delete none":    func() (hv.Edited, error) { return hv.DeleteSnapshot(ctx(t), s, uuid, "s9", false) },
	} {
		if _, err := f(); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}

	// Reverting: to s1 the machine is off, to s2 it runs again.
	cli(t, &e, false, "snap", "revert", "e2e-snap", "s1")
	if e.Active || machine(t, s, "e2e-snap").State != hv.ShutOff {
		t.Fatalf("revert to s1: %+v", e)
	}
	e = mustEdit(t)(hv.RevertSnapshot(ctx(t), s, uuid, "s2"))
	if !e.Active || machine(t, s, "e2e-snap").State != hv.Running || !strings.HasSuffix(e.Changes[0].What, "running") {
		t.Fatalf("revert to s2: %+v", e)
	}
	// Deleting s2 (the current one) hands s3 to s1, which becomes current;
	// --children takes the rest.
	cli(t, &e, false, "snap", "rm", "e2e-snap", "s2")
	if got := tree(snaps(t, "e2e-snap")); got != "s1* >s3" {
		t.Fatalf("after deleting s2: %s", got)
	}
	cli(t, &e, false, "snap", "rm", "e2e-snap", "s1", "--children")
	if ss := snaps(t, "e2e-snap"); len(ss) != 0 || !strings.Contains(e.Changes[0].What, "the 1 taken from it") {
		t.Fatalf("after deleting s1 and its children: %+v, %+v", ss, e)
	}

	// A raw disk can't hold a snapshot.
	vx, _ := (&libvirtxml.StorageVolume{Name: "raw.img", Capacity: &libvirtxml.StorageVolumeSize{Unit: "MiB", Value: 16},
		Target: &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: "raw"}}}).Marshal()
	if err := s.Do(ctx(t), func(lv *libvirt.Libvirt) error {
		p, err := lv.StoragePoolLookupByName(pool)
		if err == nil {
			_, err = lv.StorageVolCreateXML(p, vx, 0)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mustEdit(t)(hv.AddDisk(ctx(t), s, uuid, hv.NewDisk{VolPool: pool, Volume: "raw.img"}))
	if msg := cli(t, nil, true, "snap", "create", "e2e-snap"); !strings.Contains(msg, "vdb (raw)") {
		t.Errorf("with a raw disk: %q", msg)
	}

	// UEFI with raw NVRAM: not while it runs; off, without the variables.
	u, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-snap-efi", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: pool, DiskGiB: 1, NetKind: "none", Firmware: "uefi", Start: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, u.Domain.UUID, false) })
	if msg := cli(t, nil, true, "snap", "create", "e2e-snap-efi"); !strings.Contains(msg, "NVRAM") {
		t.Errorf("running UEFI: %q", msg)
	}
	hv.Power(ctx(t), s, u.Domain.UUID, hv.ForceOff)
	cli(t, &e, false, "snap", "create", "e2e-snap-efi", "efi1")
	if len(e.Warnings) != 1 || !strings.Contains(e.Warnings[0], "UEFI variables") {
		t.Errorf("UEFI, off: %+v", e)
	}
	mustEdit(t)(hv.DeleteSnapshot(ctx(t), s, u.Domain.UUID, "efi1", false))
}
