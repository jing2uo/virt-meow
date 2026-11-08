//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// Monitoring under virt-mm serve: a running machine's rates from two
// samples, shared between pages that ask within the interval; the host's
// use, what it is, and its devices.
func TestServeMonitor(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-mon-page")
	sv := serve(t)
	cr, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-mon-page", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-mon-page", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := cr.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })

	stats := func() (hv.Stats, hv.MachineStats) {
		t.Helper()
		var st hv.Stats
		sv.call(&st, "Stats", sv.conn)
		i := slices.IndexFunc(st.Machines, func(m hv.MachineStats) bool { return m.UUID == uuid })
		if i < 0 {
			t.Fatalf("Stats: no e2e-mon-page in %+v", st.Machines)
		}
		return st, st.Machines[i]
	}
	// The first sample this server takes has sizes only; a page asking
	// again at once gets the same sample.
	first, m := stats()
	if !m.New || m.MemoryKiB != 256<<10 || m.VCPUs != 1 {
		t.Errorf("first sample: %+v", m)
	}
	again, _ := stats()
	if !again.Time.Equal(first.Time) {
		t.Errorf("asked again at once: sampled at %v and %v", first.Time, again.Time)
	}
	time.Sleep(2 * time.Second)
	st, m := stats()
	if m.New || st.Seconds < 1.5 || m.CPUPercent <= 0 || m.CPUPercent > 100 || len(m.Disks) != 1 {
		t.Errorf("rates: %.1fs %+v", st.Seconds, m)
	}

	var u hv.HostUsage
	sv.call(&u, "HostStats", sv.conn)
	if u.Running < 1 || u.Machines < 1 || u.MemoryKiB == 0 {
		t.Errorf("HostStats: %+v", u)
	}
	time.Sleep(2 * time.Second)
	sv.call(&u, "HostStats", sv.conn)
	if u.CPUPercent == nil {
		t.Errorf("HostStats, the second: no CPU use: %+v", u)
	}
	var h hv.Host
	sv.call(&h, "Host", sv.conn)
	if h.Libvirt == "" || h.Hypervisor == "" || h.CPUs == 0 || h.MemoryKiB == 0 {
		t.Errorf("Host: %+v", h)
	}

	var ds []hv.HostDevice
	sv.call(&ds, "HostDevices", sv.conn)
	i := slices.IndexFunc(ds, func(d hv.HostDevice) bool { return d.Kind == "pci" })
	if i < 0 {
		t.Fatalf("HostDevices: no PCI device in %+v", ds)
	}
	var x string
	sv.call(&x, "HostDeviceXML", sv.conn, ds[i].Name)
	if !strings.Contains(x, "<device>") || !strings.Contains(x, ds[i].Name) {
		t.Errorf("HostDeviceXML %s: %s", ds[i].Name, x)
	}
	if err := sv.try(nil, "HostDeviceXML", sv.conn, "pci_no_such"); err == nil {
		t.Error("the XML of no such device")
	}
}
