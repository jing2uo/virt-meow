//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm stats: rates from two samples, once or with --watch.
func TestStats(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-stats")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-stats", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-stats", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if msg := cli(t, nil, true, "stats", "e2e-stats"); !strings.Contains(msg, "not running") {
		t.Errorf("stats of a machine that is off: %q", msg)
	}
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	mustEdit(t)(hv.AddNIC(ctx(t), s, uuid, hv.NewNIC{Kind: "user"}))

	check := func(what string, st hv.Stats) {
		t.Helper()
		i := slices.IndexFunc(st.Machines, func(m hv.MachineStats) bool { return m.UUID == uuid })
		if i < 0 {
			t.Fatalf("%s: e2e-stats missing from %+v", what, st)
		}
		m := st.Machines[i]
		if st.Seconds < 0.9 || m.New || m.VCPUs != 1 || m.MemoryKiB != 256<<10 || m.RSSKiB == 0 || m.CPUPercent < 0 || m.CPUPercent > 100 ||
			len(m.Disks) != 1 || m.Disks[0].Name != "vda" || len(m.NICs) != 1 {
			t.Fatalf("%s: %+v in %v s", what, m, st.Seconds)
		}
	}
	var st hv.Stats
	cli(t, &st, false, "stats", "e2e-stats")
	if len(st.Machines) != 1 {
		t.Fatalf("one machine: %+v", st.Machines)
	}
	check("one", st)
	st = hv.Stats{}
	cli(t, &st, false, "stats")
	check("all", st)

	// --watch --json: a line per sample, until stopped.
	cmd := virtmm(t, "stats", "e2e-stats", "--watch")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	lines := bufio.NewScanner(pipe)
	for i := range 2 {
		if !lines.Scan() {
			t.Fatalf("--watch: line %d: %v", i, lines.Err())
		}
		var st hv.Stats
		if err := json.Unmarshal(lines.Bytes(), &st); err != nil {
			t.Fatalf("--watch: line %d: %v: %s", i, err, lines.Text())
		}
		check("--watch", st)
	}
	cmd.Process.Kill()
	cmd.Wait()
}
