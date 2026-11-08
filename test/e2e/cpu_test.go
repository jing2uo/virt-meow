//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm set --cpu-model, under TCG: host-model and named models the host
// offers; host-passthrough refused (no KVM).
func TestCPUModel(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-cpu")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-cpu", OS: "linux", VCPUs: 2, MemoryMiB: 256,
		DiskPool: "e2e-cpu", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	mustEdit(t)(hv.Set(ctx(t), s, uuid, hv.Settings{Sockets: ptr(1), Cores: ptr(2), Threads: ptr(1)}))

	var cm hv.CPUModels
	cli(t, &cm, false, "cpu-models", "e2e-cpu")
	if slices.Contains(cm.Modes, "host-passthrough") || !slices.Contains(cm.Modes, "host-model") {
		t.Fatalf("modes under TCG: %v", cm.Modes)
	}
	usable := func(name string) bool {
		return slices.ContainsFunc(cm.Models, func(m hv.CPUModel) bool { return m.Name == name && m.Usable == "yes" })
	}
	if !usable("Nehalem") {
		t.Fatalf("Nehalem not usable: %+v", cm.Models)
	}
	i := slices.IndexFunc(cm.Models, func(m hv.CPUModel) bool { return m.Usable == "no" })
	if i < 0 || len(cm.Models[i].Blockers) == 0 {
		t.Fatalf("no unusable model with its blockers under TCG: %+v", cm.Models)
	}
	unusable := cm.Models[i].Name

	var e hv.Edited
	cli(t, &e, false, "set", "e2e-cpu", "--cpu-model", "Nehalem")
	if len(e.Changes) != 1 || !strings.HasSuffix(e.Changes[0].What, "→ Nehalem") {
		t.Fatalf("set Nehalem: %+v", e)
	}
	def, _ := hv.GetDefinition(ctx(t), s, uuid)
	if !strings.Contains(def.XML, "<model fallback='forbid'>Nehalem</model>") || !strings.Contains(def.XML, "cores='2'") {
		t.Fatalf("definition after Nehalem (topology kept?):\n%s", def.XML)
	}
	if e := mustEdit(t)(hv.Set(ctx(t), s, uuid, hv.Settings{CPUModel: ptr("Nehalem")})); len(e.Changes) != 0 {
		t.Errorf("setting what it has: %+v", e.Changes)
	}
	for what, bad := range map[string]string{
		"host-passthrough under TCG": "host-passthrough",
		"a model that isn't":         "Pentium9000",
		"a model the host can't run": unusable,
		"the wrong case":             "nehalem",
		"empty":                      "",
	} {
		if _, err := hv.Set(ctx(t), s, uuid, hv.Settings{CPUModel: ptr(bad)}); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	if msg := cli(t, nil, true, "set", "e2e-cpu", "--cpu-model", "nehalem"); !strings.Contains(msg, "did you mean Nehalem") {
		t.Errorf("wrong case: %q", msg)
	}

	// It boots with it; a change while it runs waits for the next start.
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	e = mustEdit(t)(hv.Set(ctx(t), s, uuid, hv.Settings{CPUModel: ptr("host-model")}))
	if !e.Active || len(e.Changes) != 1 || e.Changes[0].Live {
		t.Fatalf("host-model while running: %+v", e)
	}
	if err := hv.Power(ctx(t), s, uuid, hv.ForceOff); err != nil {
		t.Fatal(err)
	}
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatalf("starting with host-model: %v", err)
	}
	cli(t, &cm, false, "cpu-models", "e2e-cpu")
	if cm.Current != "host-model" {
		t.Errorf("current: %q", cm.Current)
	}
}
