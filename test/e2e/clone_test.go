//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm clone: a UEFI machine with a TPM, a qcow2 disk, a raw disk of
// known bytes, an ISO and a NIC is copied; the copy has its own disks, NVRAM
// and MACs, shares the ISO, starts, and goes without touching the original.
func TestClone(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-clone")
	data := bytes.Repeat([]byte("virt-mm-clone "), 1<<16)
	for _, name := range []string{"base.img", "media.iso"} {
		if _, err := hv.Upload(ctx(t), s, "e2e-clone", name, bytes.NewReader(data), int64(len(data)), nil); err != nil {
			t.Fatal(err)
		}
	}
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-clone-src", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-clone", DiskGiB: 1, ISOPool: "e2e-clone", ISOVolume: "media.iso",
		NetKind: "none", Firmware: "uefi", TPM: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	src := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, src, false) })
	cli(t, nil, false, "nic", "add", "e2e-clone-src", "--net", "user")
	cli(t, nil, false, "disk", "add", "e2e-clone-src", "--vol", "e2e-clone/base.img")

	// Running: refused. Started once, it has an NVRAM to copy.
	if err := hv.Power(ctx(t), s, src, hv.Start); err != nil {
		t.Fatal(err)
	}
	if msg := cli(t, nil, true, "clone", "e2e-clone-src", "e2e-clone-dst"); !strings.Contains(msg, "off") {
		t.Errorf("cloning a running machine: %q", msg)
	}
	if err := hv.Power(ctx(t), s, src, hv.ForceOff); err != nil {
		t.Fatal(err)
	}
	if msg := cli(t, nil, true, "clone", "e2e-clone-src", "e2e-clone-src"); !strings.Contains(msg, "already exists") {
		t.Errorf("cloning onto an existing name: %q", msg)
	}
	srcNVRAM := liveOS(t, s, "e2e-clone-src").NVRam.NVRam
	if !inContainer(t, srcNVRAM) {
		t.Fatalf("no NVRAM %s after a start", srcNVRAM)
	}

	var cl hv.Cloned
	cli(t, &cl, false, "clone", "e2e-clone-src", "e2e-clone-dst")
	dst := cl.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, dst, true) })
	if dst == "" || dst == src || cl.Domain.Name != "e2e-clone-dst" {
		t.Fatalf("clone: %+v", cl.Domain)
	}
	tos := map[string]string{}
	for _, d := range cl.Disks {
		tos[d.From] = d.To
	}
	if len(cl.Disks) != 2 || tos["e2e-clone/base.img"] != "e2e-clone/e2e-clone-dst-base.img" ||
		tos["e2e-clone/e2e-clone-src.qcow2"] != "e2e-clone/e2e-clone-dst.qcow2" {
		t.Errorf("copied disks: %+v", cl.Disks)
	}
	if !slices.Contains(cl.Shared, "e2e-clone/media.iso") {
		t.Errorf("shared: %v", cl.Shared)
	}
	if !slices.ContainsFunc(cl.Notes, func(n string) bool { return strings.Contains(n, "new TPM") }) {
		t.Errorf("notes: %v", cl.Notes)
	}
	if cl.NVRAM == "" || cl.NVRAM == srcNVRAM || !inContainer(t, cl.NVRAM) {
		t.Fatalf("NVRAM: %q (the original's %q)", cl.NVRAM, srcNVRAM)
	}
	sum := func(f string) string { return strings.Fields(podmanExec(t, "sha256sum", f))[0] }
	if sum(cl.NVRAM) != sum(srcNVRAM) {
		t.Error("the NVRAM copy differs from the original")
	}

	// Its own disks and MACs, the same ISO, a TPM.
	a, err := hv.GetDevices(ctx(t), s, src)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hv.GetDevices(ctx(t), s, dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.NICs) != len(a.NICs) || len(b.NICs) == 0 || b.NICs[0].MAC == a.NICs[0].MAC {
		t.Errorf("NICs: %+v, the original's %+v", b.NICs, a.NICs)
	}
	for _, d := range b.Disks {
		switch d.Device {
		case "cdrom":
			if d.Source != "e2e-clone/media.iso" {
				t.Errorf("the clone's CD/DVD: %+v", d)
			}
		default:
			if !strings.Contains(d.Source, "e2e-clone-dst") {
				t.Errorf("the clone's disk %s is %s", d.Target, d.Source)
			}
		}
	}
	if len(b.TPMs) != 1 {
		t.Errorf("TPM: %+v", b.TPMs)
	}
	file := filepath.Join(t.TempDir(), "copy.img")
	cli(t, nil, false, "vol", "download", "e2e-clone/e2e-clone-dst-base.img", file)
	if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the copied disk has %d bytes (%v), want the original's %d", len(got), err, len(data))
	}

	// It starts, and goes with its own disks and NVRAM only.
	if err := hv.Power(ctx(t), s, dst, hv.Start); err != nil {
		t.Fatalf("starting the clone: %v", err)
	}
	cli(t, nil, false, "rm", "e2e-clone-dst", "--disks", "--yes")
	names := volumes(t, s, "e2e-clone")
	if slices.ContainsFunc(names, func(n string) bool { return strings.Contains(n, "e2e-clone-dst") }) ||
		!slices.Contains(names, "base.img") || !slices.Contains(names, "e2e-clone-src.qcow2") || !slices.Contains(names, "media.iso") {
		t.Errorf("volumes after removing the clone: %v", names)
	}
	if inContainer(t, cl.NVRAM) || !inContainer(t, srcNVRAM) {
		t.Errorf("NVRAMs after removing the clone: %s there %v, %s there %v", cl.NVRAM, inContainer(t, cl.NVRAM), srcNVRAM, inContainer(t, srcNVRAM))
	}
}
