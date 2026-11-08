//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

type hostOut struct {
	hv.Host
	Usage hv.HostUsage `json:"usage"`
}

// virt-mm host: what the host uses, and how many machines it runs.
func TestHostUsage(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-host")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-host", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-host", DiskGiB: 1, NetKind: "none", Firmware: "bios", Start: true}, "")
	if err != nil || c.StartErr != "" {
		t.Fatal(err, c.StartErr)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, true) })
	doms, err := hv.List(ctx(t), s)
	if err != nil {
		t.Fatal(err)
	}
	running := 0
	for _, d := range doms {
		if d.State.Active() {
			running++
		}
	}

	var h hostOut
	cli(t, &h, false, "host")
	u := h.Usage
	if h.CPUs == 0 || h.Libvirt == "" || u.CPUPercent == nil || *u.CPUPercent < 0 || *u.CPUPercent > 100 ||
		u.MemoryKiB != h.MemoryKiB || u.UsedKiB == 0 || u.UsedKiB > u.MemoryKiB || u.Running != running || u.Machines != len(doms) || running == 0 {
		t.Fatalf("qemu host: %+v, usage %+v (CPU %v); %d of %d running", h.Host, u, u.CPUPercent, running, len(doms))
	}

	// The test driver has no memory statistics: unknown, not an error.
	h = hostOut{}
	cli(t, &h, false, "host", "-c", "test+unix:///default?socket="+filepath.Join(env(t, "VIRT_MM_E2E_RUN"), "libvirt-sock"))
	if h.Usage.UsedKiB != 0 || h.Usage.Running != 1 || h.Usage.Machines != 1 {
		t.Fatalf("test host: %+v", h.Usage)
	}
}
