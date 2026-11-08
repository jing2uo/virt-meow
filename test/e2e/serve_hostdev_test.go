//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/api"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// Host devices on the details page under virt-mm serve: a PCI device of the
// host (the container sees the host's) given to a machine that is off and
// taken back, what the list then says, a second machine warned that the
// first has it; and the same from the CLI. The machines aren't started
// with it: nothing is taken from the host. Not tested: USB (the container
// lists none, as hostdevs_test.go says) and a running machine taking a
// device, which needs the host's IOMMU and vfio.
func TestServeHostDevices(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-hd-page")
	sv := serve(t)
	mk := func(name string) string {
		c, err := hv.Create(ctx(t), s, hv.Spec{Name: name, OS: "linux", VCPUs: 1, MemoryMiB: 256,
			DiskPool: "e2e-hd-page", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, true) })
		return c.Domain.UUID
	}
	a, b := mk("e2e-hd-a"), mk("e2e-hd-b")

	var ds []hv.HostDevice
	sv.call(&ds, "HostDevices", sv.conn)
	i := slices.IndexFunc(ds, func(d hv.HostDevice) bool { return d.Kind == "pci" && len(d.UsedBy) == 0 })
	if i < 0 {
		t.Fatalf("no free PCI device: %+v", ds)
	}
	dev := ds[i]

	var ed hv.Edited
	sv.call(&ed, "AttachHostDevice", sv.conn, a, dev.Name)
	if len(ed.Changes) != 1 || !strings.Contains(ed.Changes[0].What, dev.Address) {
		t.Errorf("AttachHostDevice: %+v", ed)
	}
	var in api.MachineInfo
	sv.call(&in, "Info", sv.conn, a)
	if hd := in.Devices.HostDevs; len(hd) != 1 || hd[0].Kind != "pci" || hd[0].Address != dev.Address || hd[0].Ref != dev.Address {
		t.Fatalf("Info's host devices: %+v", hd)
	}
	sv.call(&ds, "HostDevices", sv.conn)
	if d := ds[slices.IndexFunc(ds, func(d hv.HostDevice) bool { return d.Name == dev.Name })]; !slices.Equal(d.UsedBy, []string{"e2e-hd-a"}) {
		t.Errorf("used by %v", d.UsedBy)
	}
	if err := sv.try(nil, "AttachHostDevice", sv.conn, a, dev.Address); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Errorf("attached twice: %v", err)
	}
	sv.call(&ed, "AttachHostDevice", sv.conn, b, dev.Address)
	if !slices.ContainsFunc(ed.Warnings, func(w string) bool { return strings.Contains(w, "e2e-hd-a") }) {
		t.Errorf("no word of the other machine: %+v", ed)
	}

	sv.call(&ed, "DetachHostDevice", sv.conn, a, dev.Address)
	sv.call(&in, "Info", sv.conn, a)
	if len(ed.Changes) != 1 || len(in.Devices.HostDevs) != 0 {
		t.Errorf("after DetachHostDevice: %+v, %+v", ed, in.Devices.HostDevs)
	}
	if err := sv.try(nil, "DetachHostDevice", sv.conn, a, dev.Address); err == nil {
		t.Error("detached a device it hasn't")
	}
	if err := sv.try(nil, "AttachHostDevice", sv.conn, a, "pci_nope"); err == nil || !strings.Contains(err.Error(), "no device") {
		t.Errorf("an unknown device: %v", err)
	}

	// The CLI: device ls shows it, device rm hostdev by libvirt's name.
	var l struct{ HostDevs []hv.AttachedHostDevice }
	cli(t, &l, false, "device", "ls", "e2e-hd-b")
	if len(l.HostDevs) != 1 || l.HostDevs[0].Address != dev.Address {
		t.Errorf("device ls: %+v", l)
	}
	cli(t, &ed, false, "device", "rm", "e2e-hd-b", "hostdev", dev.Name)
	cli(t, &l, false, "device", "ls", "e2e-hd-b")
	if len(l.HostDevs) != 0 {
		t.Errorf("after device rm hostdev: %+v", l)
	}
	cli(t, &ed, false, "device", "add", "e2e-hd-b", "hostdev", dev.Address)
	if len(ed.Changes) != 1 {
		t.Errorf("device add hostdev: %+v", ed)
	}
}
