//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

func ptr[T any](v T) *T { return &v }

// changed returns the edit's change starting with what, or fails.
func changed(t *testing.T, e hv.Edited, what string) hv.Change {
	t.Helper()
	for _, c := range e.Changes {
		if strings.HasPrefix(c.What, what) {
			return c
		}
	}
	t.Fatalf("no change %q in %+v", what, e.Changes)
	return hv.Change{}
}

// virt-mm set, rename and define, on a machine that is off and on one that
// runs.
func TestSetRenameDefine(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-set")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-set", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-set", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })

	// Off: everything goes into the definition.
	e, err := hv.Set(ctx(t), s, uuid, hv.Settings{VCPUs: ptr(2), MaxMemoryMiB: ptr(512), Autostart: ptr(true), Title: ptr("web")})
	if err != nil {
		t.Fatal(err)
	}
	if e.Active || len(e.Changes) != 4 {
		t.Fatalf("edited %+v", e)
	}
	d := machine(t, s, "e2e-set")
	if d.VCPUs != 2 || d.MaxMemKiB != 512<<10 || d.MemoryKiB != 256<<10 || !d.Autostart {
		t.Fatalf("after set: %+v", d)
	}
	if e, err := hv.Set(ctx(t), s, uuid, hv.Settings{VCPUs: ptr(2)}); err != nil || len(e.Changes) != 0 {
		t.Fatalf("setting what it has: %+v, %v", e, err)
	}

	// Topology: given whole, or kept per socket when only the count changes.
	if _, err := hv.Set(ctx(t), s, uuid, hv.Settings{Sockets: ptr(1), Cores: ptr(2), Threads: ptr(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := hv.Set(ctx(t), s, uuid, hv.Settings{VCPUs: ptr(4)}); err != nil {
		t.Fatal(err)
	}
	def, _ := hv.GetDefinition(ctx(t), s, uuid)
	if !strings.Contains(def.XML, "<topology sockets='2' dies='1' clusters='1' cores='2' threads='1'/>") &&
		!strings.Contains(def.XML, "<topology sockets='2' dies='1' cores='2' threads='1'/>") {
		t.Fatalf("topology:\n%s", def.XML)
	}
	for what, bad := range map[string]hv.Settings{
		"odd count for 2 per socket": {VCPUs: ptr(3)},
		"count against topology":     {VCPUs: ptr(5), Sockets: ptr(2)},
		"tiny memory":                {MemoryMiB: ptr(16)},
		"more than the host":         {MemoryMiB: ptr(1 << 30)},
		"memory over its maximum":    {MemoryMiB: ptr(1024), MaxMemoryMiB: ptr(512)},
		"nothing":                    {},
	} {
		if _, err := hv.Set(ctx(t), s, uuid, bad); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	// A refused edit changes nothing, autostart included.
	if _, err := hv.Set(ctx(t), s, uuid, hv.Settings{Autostart: ptr(false), MemoryMiB: ptr(16)}); err == nil || !machine(t, s, "e2e-set").Autostart {
		t.Fatalf("refused edit: %v, autostart %v", err, machine(t, s, "e2e-set").Autostart)
	}

	// Running: memory up to the maximum and the title now, more vCPUs than
	// it started with at the next start.
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	e, err = hv.Set(ctx(t), s, uuid, hv.Settings{MemoryMiB: ptr(384), VCPUs: ptr(8), Title: ptr("web, running")})
	if err != nil {
		t.Fatal(err)
	}
	if !e.Active || !changed(t, e, "memory").Live || !changed(t, e, "title").Live {
		t.Errorf("live changes: %+v", e.Changes)
	}
	if v := changed(t, e, "vCPUs"); v.Live || v.Note == "" {
		t.Errorf("vCPUs beyond the running maximum: %+v", v)
	}
	// (The balloon's target, not checked: with no guest to inflate it the
	// container's machine keeps all of its memory.)
	if d := machine(t, s, "e2e-set"); d.VCPUs != 4 {
		t.Errorf("running: %+v", d)
	}

	if _, err := hv.Rename(ctx(t), s, uuid, "e2e-set-2"); err == nil {
		t.Fatal("renamed a running machine")
	}
	if err := hv.Power(ctx(t), s, uuid, hv.ForceOff); err != nil {
		t.Fatal(err)
	}
	if d := machine(t, s, "e2e-set"); d.VCPUs != 8 {
		t.Errorf("after the run, vCPUs %d, want the 8 saved for the next start", d.VCPUs)
	}
	if _, err := hv.Rename(ctx(t), s, uuid, "e2e-set-2"); err != nil {
		t.Fatal(err)
	}
	if machine(t, s, "e2e-set-2").UUID != uuid {
		t.Fatal("renamed machine lost")
	}

	// Define: a copy without its UUID is a new machine; nothing overwrites.
	def, _ = hv.GetDefinition(ctx(t), s, uuid)
	copyX := edit(t, edit(t, def.XML, "<uuid>"+uuid+"</uuid>", ""), "<name>e2e-set-2</name>", "<name>e2e-set-copy</name>")
	nd, err := hv.Define(ctx(t), s, copyX)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, nd.UUID, false) })
	if nd.UUID == uuid || nd.Name != "e2e-set-copy" {
		t.Fatalf("defined %+v", nd)
	}
	if _, err := hv.Define(ctx(t), s, copyX); err == nil {
		t.Error("defined over an existing name")
	}
	if _, err := hv.Define(ctx(t), s, edit(t, def.XML, "<name>e2e-set-2</name>", "<name>e2e-set-3</name>")); err == nil {
		t.Error("defined over an existing UUID")
	}
}
