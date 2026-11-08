//go:build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm hostdevs: the PCI devices the container sees of the host, with
// their IOMMU groups, and the machine whose definition has one. The machine
// is never started, so no device is taken from the host.
//
// Not tested here: USB devices (libvirt reads them from udev's database,
// which an unprivileged container doesn't have, so it lists none) and
// vendor and product names (udev's hwdb too).
func TestHostDevs(t *testing.T) {
	var ds []hv.HostDevice
	cli(t, &ds, false, "hostdevs")
	pci := slices.DeleteFunc(slices.Clone(ds), func(d hv.HostDevice) bool { return d.Kind != "pci" })
	if len(pci) == 0 {
		t.Fatalf("no PCI devices listed: %+v", ds)
	}
	addr := regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
	id := regexp.MustCompile(`^[0-9a-f]{4}$`)
	for _, d := range pci {
		if !addr.MatchString(d.Address) || !id.MatchString(d.VendorID) || !id.MatchString(d.ProductID) ||
			!strings.HasPrefix(d.Name, "pci_") || d.UsedBy == nil || len(d.UsedBy) != 0 {
			t.Errorf("%+v", d)
		}
	}
	groups, _ := exec.Command("podman", "exec", env(t, "VIRT_MM_E2E_CONTAINER"), "ls", "/sys/kernel/iommu_groups").Output()
	if hasIOMMU := len(strings.Fields(string(groups))) > 0; hasIOMMU != slices.ContainsFunc(pci, func(d hv.HostDevice) bool { return d.IOMMUGroup != nil }) {
		t.Errorf("IOMMU groups on the host: %v; in the list: %+v", hasIOMMU, pci)
	}
	if !slices.ContainsFunc(pci, func(d hv.HostDevice) bool { return d.Driver != "" }) {
		t.Errorf("no PCI device with a driver: %+v", pci)
	}

	var x map[string]string
	cli(t, &x, false, "hostdevs", pci[0].Name)
	if !strings.Contains(x["xml"], "<name>"+pci[0].Name+"</name>") {
		t.Errorf("hostdevs %s: %v", pci[0].Name, x)
	}
	if msg := cli(t, nil, true, "hostdevs", "pci_nope"); !strings.Contains(msg, "no device") {
		t.Errorf("an unknown device: %q", msg)
	}

	// In a definition: listed as used by that machine.
	s := qemuSession(t)
	const name = "e2e-hostdev"
	scratchPool(t, s, name)
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: name, OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: name, DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, true) })
	var dom, bus, slot, fn uint
	fmt.Sscanf(pci[0].Address, "%x:%x:%x.%x", &dom, &bus, &slot, &fn)
	mustEdit(t)(hv.AttachDevice(ctx(t), s, c.Domain.UUID, fmt.Sprintf(
		"<hostdev mode='subsystem' type='pci' managed='yes'><source><address domain='%#x' bus='%#x' slot='%#x' function='%#x'/></source></hostdev>",
		dom, bus, slot, fn)))
	ds = nil
	cli(t, &ds, false, "hostdevs")
	for _, d := range ds {
		if used := slices.Equal(d.UsedBy, []string{name}); used != (d.Name == pci[0].Name) {
			t.Errorf("%s used by %v", d.Name, d.UsedBy)
		}
	}
}
