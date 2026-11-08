//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm info: state, definition, devices and the guest's addresses.
func TestInfo(t *testing.T) {
	// The test driver answers every address source; its guest "has" an
	// agent.
	var in hv.Info
	cli(t, &in, false, "info", "test", "-c", "test+unix:///default?socket="+filepath.Join(env(t, "VIRT_MM_E2E_RUN"), "libvirt-sock"))
	if in.State != hv.Running || in.ID < 0 || in.VCPUs == 0 || len(in.Devices.Disks) == 0 || len(in.Devices.NICs) == 0 {
		t.Fatalf("test: %+v", in)
	}
	if len(in.Addresses) == 0 || in.AddressSource != "agent" || in.Addresses[0].MAC != in.Devices.NICs[0].MAC || in.Addresses[0].Type != "ipv4" {
		t.Fatalf("test's addresses: %+v from %q (%s)", in.Addresses, in.AddressSource, in.AddressNote)
	}

	s := qemuSession(t)
	scratchPool(t, s, "e2e-info")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-info", OS: "linux", VCPUs: 2, MemoryMiB: 256,
		DiskPool: "e2e-info", DiskGiB: 1, NetKind: "none", Firmware: "uefi", TPM: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	mustEdit(t)(hv.Set(ctx(t), s, uuid, hv.Settings{Autostart: ptr(true), Title: ptr("info"), CPUModel: ptr("Nehalem")}))
	mustEdit(t)(hv.AddNIC(ctx(t), s, uuid, hv.NewNIC{Kind: "user"}))

	in = hv.Info{}
	cli(t, &in, false, "info", "e2e-info")
	if in.State != hv.ShutOff || in.ID != -1 || in.VCPUs != 2 || in.MaxMemKiB != 256<<10 || !in.Autostart || !in.Persistent ||
		in.Title != "info" || in.Firmware != "uefi" || in.CPU != "Nehalem" || !strings.Contains(in.Machine, "q35") ||
		in.VirtType != "qemu" || in.Arch != "x86_64" || in.OSType != "hvm" {
		t.Fatalf("off: %+v", in)
	}
	if d := in.Devices; len(d.Disks) != 1 || d.Disks[0].Capacity != 1<<30 || len(d.NICs) != 1 || len(d.TPMs) != 1 ||
		len(d.Graphics) != 1 || d.Graphics[0].Port != 0 {
		t.Fatalf("off, devices: %+v", d)
	}
	if len(in.Addresses) != 0 || in.AddressNote != "" {
		t.Errorf("off, addresses: %+v %q", in.Addresses, in.AddressNote)
	}

	// Running, with no guest to answer: every source tried, and why each
	// gave nothing.
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	in = hv.Info{}
	cli(t, &in, false, "info", "e2e-info")
	if in.State != hv.Running || in.ID < 1 || in.Devices.Graphics[0].Port < 5900 {
		t.Fatalf("running: %+v", in)
	}
	if len(in.Addresses) != 0 || in.AddressSource != "" ||
		!strings.Contains(in.AddressNote, "agent: ") || !strings.Contains(in.AddressNote, "lease: ") || !strings.Contains(in.AddressNote, "arp: ") {
		t.Errorf("running, addresses: %+v %q", in.Addresses, in.AddressNote)
	}
}
