//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/api"
	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// The overview of every host under virt-mm serve: two connections (the
// container's QEMU and its test driver, both over SSH), each with its
// usage and machine counts, one list of the machines of both, and a power
// action done to several machines at once, one of which fails.
func TestServeOverview(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-all")
	sv := serve(t)
	mk := func(name string) string {
		c, err := hv.Create(ctx(t), s, hv.Spec{Name: name, OS: "linux", VCPUs: 1, MemoryMiB: 256,
			DiskPool: "e2e-all", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, true) })
		return c.Domain.UUID
	}
	a, b := mk("e2e-all-a"), mk("e2e-all-b")

	var tc struct{ ID string }
	sv.call(&tc, "AddConnection", "testdrv", "test+ssh://root@"+env(t, "VIRT_MM_E2E_SSH")+"/default", false)
	sv.call(nil, "Connect", tc.ID)
	await(t, sv.ws, "connection", func(d json.RawMessage) bool {
		var ci struct{ ID, Status string }
		json.Unmarshal(d, &ci)
		return ci.ID == tc.ID && ci.Status == "connected"
	})

	var o api.Overview
	sv.call(&o, "Overview")
	if len(o.Hosts) != 2 {
		t.Fatalf("hosts: %+v", o.Hosts)
	}
	for _, h := range o.Hosts {
		if h.Status != "connected" || (h.ID == sv.conn && (h.Usage == nil || h.Usage.Machines < 2 || h.Usage.MemoryKiB == 0)) {
			t.Errorf("host %s: %s, usage %+v, %s", h.Name, h.Status, h.Usage, h.ListError)
		}
	}
	on := func(conn, name string) *api.OverviewMachine {
		for i, m := range o.Machines {
			if m.Connection == conn && m.Name == name {
				return &o.Machines[i]
			}
		}
		return nil
	}
	if ma := on(sv.conn, "e2e-all-a"); ma == nil || ma.State != hv.ShutOff || !slices.Equal(ma.Actions, []hv.Action{hv.Start}) {
		t.Errorf("e2e-all-a in the overview: %+v", ma)
	}
	if on(tc.ID, "test") == nil { // the test driver's own machine
		t.Errorf("no test driver machine in %+v", o.Machines)
	}

	// Started together; then forced off together with a machine that
	// isn't there, which fails alone.
	var rs []app.PowerResult
	sv.call(&rs, "PowerMany", []app.MachineRef{{Connection: sv.conn, UUID: a}, {Connection: sv.conn, UUID: b}}, "start")
	t.Cleanup(func() { hv.Power(ctx(t), s, a, hv.ForceOff); hv.Power(ctx(t), s, b, hv.ForceOff) })
	if len(rs) != 2 || rs[0].Error != "" || rs[1].Error != "" {
		t.Fatalf("PowerMany start: %+v", rs)
	}
	sv.call(&o, "Overview")
	if on(sv.conn, "e2e-all-a").State != hv.Running || on(sv.conn, "e2e-all-b").State != hv.Running {
		t.Errorf("after starting both: %+v %+v", on(sv.conn, "e2e-all-a"), on(sv.conn, "e2e-all-b"))
	}
	sv.call(&rs, "PowerMany", []app.MachineRef{{Connection: sv.conn, UUID: a}, {Connection: sv.conn, UUID: "00000000-0000-0000-0000-000000000000"},
		{Connection: sv.conn, UUID: b}}, "force-off")
	if len(rs) != 3 || rs[0].Error != "" || rs[1].Error == "" || rs[2].Error != "" || rs[1].UUID != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("PowerMany force-off: %+v", rs)
	}
	if machine(t, s, "e2e-all-a").State != hv.ShutOff || machine(t, s, "e2e-all-b").State != hv.ShutOff {
		t.Error("not both off")
	}
	if err := sv.try(nil, "PowerMany", []app.MachineRef{{Connection: sv.conn, UUID: a}}, "explode"); err == nil {
		t.Error("an unknown action")
	}
}

// virt-mm ls --all over the saved connections, and a power action on
// several machines at once from the CLI.
func TestListAllCLI(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-all-cli")
	for _, n := range []string{"e2e-cli-a", "e2e-cli-b"} {
		c, err := hv.Create(ctx(t), s, hv.Spec{Name: n, OS: "linux", VCPUs: 1, MemoryMiB: 256,
			DiskPool: "e2e-all-cli", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { hv.Power(ctx(t), s, c.Domain.UUID, hv.ForceOff); hv.Delete(ctx(t), s, c.Domain.UUID, true) })
	}
	cfg := t.TempDir()
	run := func(v any, args ...string) {
		t.Helper()
		cmd := virtmm(t, args...)
		cmd.Env = append(cmd.Env, "VIRT_MM_CONFIG="+cfg) // one config for both runs
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("virt-mm %v: %v: %s", args, err, out.String())
		}
		if v != nil {
			json.Unmarshal(out.Bytes(), v)
		}
	}
	run(nil, "conn", "add", "qemu+unix:///system?socket="+filepath.Join(env(t, "VIRT_MM_E2E_RUN"), "libvirt-sock"), "--name", "box")
	var all app.AllHosts
	run(&all, "ls", "--all")
	if len(all.Hosts) != 1 || all.Hosts[0].Name != "box" || all.Hosts[0].Status != "connected" || all.Hosts[0].Usage == nil {
		t.Fatalf("ls --all hosts: %+v", all.Hosts)
	}
	n := 0
	for _, m := range all.Machines {
		if strings.HasPrefix(m.Name, "e2e-cli-") && m.Connection == all.Hosts[0].ID {
			n++
		}
	}
	if n != 2 {
		t.Errorf("ls --all machines: %+v", all.Machines)
	}

	var rs []struct{ Name, Action, Error string }
	cli(t, &rs, false, "start", "e2e-cli-a", "e2e-cli-b")
	if len(rs) != 2 || rs[0].Name != "e2e-cli-a" || rs[0].Error != "" || rs[1].Error != "" {
		t.Errorf("start a b: %+v", rs)
	}
	if machine(t, s, "e2e-cli-a").State != hv.Running || machine(t, s, "e2e-cli-b").State != hv.Running {
		t.Error("not both running")
	}
	cli(t, &rs, false, "force-off", "e2e-cli-a", "e2e-cli-b")
	if machine(t, s, "e2e-cli-a").State != hv.ShutOff {
		t.Error("e2e-cli-a still running")
	}
	if msg := cli(t, nil, true, "start", "e2e-cli-a", "nosuchmachine"); !strings.Contains(msg, "nosuchmachine") {
		t.Errorf("an unknown machine among them: %s", msg)
	}
}
