//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

type peripherals struct {
	Video    []hv.Video    `json:"video"`
	Graphics []hv.Graphics `json:"graphics"`
	TPMs     []hv.TPM      `json:"tpm"`
	Sound    []string      `json:"sound"`
	RNGs     []hv.RNG      `json:"rng"`
}

// virt-mm device: video, graphics, TPM, sound and RNG, on a machine that is
// off and on one that runs.
func TestPeripherals(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-per")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-per", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-per", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	ls := func() peripherals {
		t.Helper()
		var p peripherals
		cli(t, &p, false, "device", "ls", "e2e-per")
		return p
	}
	p := ls()
	if len(p.Video) != 1 || p.Video[0].Model != "virtio" || len(p.Graphics) != 1 || p.Graphics[0].Listen != "127.0.0.1" ||
		len(p.TPMs) != 0 || len(p.Sound) != 0 || len(p.RNGs) != 1 {
		t.Fatalf("as created: %+v", p)
	}

	// Off.
	for _, m := range []string{"vga", "bochs", "ramfb", "none", "virtio"} {
		var e hv.Edited
		cli(t, &e, false, "device", "set", "e2e-per", "video", m)
		if len(e.Changes) != 1 || !strings.HasSuffix(e.Changes[0].What, "→ "+m) {
			t.Fatalf("video %s: %+v", m, e.Changes)
		}
		if v := ls().Video; len(v) != 1 || v[0].Model != m {
			t.Fatalf("video %s: %+v", m, v)
		}
	}
	mustEdit(t)(hv.SetSound(ctx(t), s, uuid, "ich9"))
	mustEdit(t)(hv.AddTPM(ctx(t), s, uuid, ""))
	mustEdit(t)(hv.RemoveGraphics(ctx(t), s, uuid, "vnc"))
	var e hv.Edited
	cli(t, &e, false, "device", "add", "e2e-per", "graphics", "vnc")
	if len(e.Warnings) != 0 {
		t.Errorf("VNC on the loopback warned: %+v", e.Warnings)
	}
	cli(t, &e, false, "device", "set", "e2e-per", "graphics", "vnc", "--listen", "0.0.0.0")
	if len(e.Warnings) != 1 || !strings.Contains(e.Warnings[0], "unencrypted") {
		t.Errorf("VNC on 0.0.0.0: no warning: %+v", e)
	}
	p = ls()
	if !slices.Equal(p.Sound, []string{"ich9"}) || len(p.TPMs) != 1 || p.TPMs[0].Model != "tpm-crb" || p.TPMs[0].Version != "2.0" ||
		len(p.Graphics) != 1 || p.Graphics[0].Listen != "0.0.0.0" {
		t.Fatalf("after the changes: %+v", p)
	}
	mustEdit(t)(hv.SetGraphicsListen(ctx(t), s, uuid, "vnc", "127.0.0.1"))
	h, _ := hv.HostInfo(ctx(t), s)
	for what, bad := range map[string]func() (hv.Edited, error){
		"a second TPM":          func() (hv.Edited, error) { return hv.AddTPM(ctx(t), s, uuid, "") },
		"a TPM model":           func() (hv.Edited, error) { return hv.AddTPM(ctx(t), s, uuid, "tpm-spapr") },
		"a second VNC":          func() (hv.Edited, error) { return hv.AddGraphics(ctx(t), s, uuid, "vnc", "") },
		"a host name":           func() (hv.Edited, error) { return hv.SetGraphicsListen(ctx(t), s, uuid, "vnc", "localhost") },
		"RDP":                   func() (hv.Edited, error) { return hv.AddGraphics(ctx(t), s, uuid, "rdp", "") },
		"a video model":         func() (hv.Edited, error) { return hv.SetVideo(ctx(t), s, uuid, "voodoo") },
		"a sound model":         func() (hv.Edited, error) { return hv.SetSound(ctx(t), s, uuid, "sb16") },
		"a second RNG":          func() (hv.Edited, error) { return hv.AddRNG(ctx(t), s, uuid) },
		"no SPICE to set":       func() (hv.Edited, error) { return hv.SetGraphicsListen(ctx(t), s, uuid, "spice", "127.0.0.1") },
		"no SPICE to remove":    func() (hv.Edited, error) { return hv.RemoveGraphics(ctx(t), s, uuid, "spice") },
		"an address that isn't": func() (hv.Edited, error) { return hv.SetGraphicsListen(ctx(t), s, uuid, "vnc", "::zz") },
	} {
		if _, err := bad(); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	// The container's QEMU is built without SPICE and QXL: refused with
	// what it has, rather than at the next start.
	if h.VirtType == "qemu" {
		if msg := cli(t, nil, true, "device", "add", "e2e-per", "graphics", "spice"); !strings.Contains(msg, "vnc") {
			t.Errorf("SPICE: %q", msg)
		}
	}

	// It starts with all of it.
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	if g := ls().Graphics; len(g) != 1 || g[0].Port < 5900 {
		t.Errorf("running, the VNC port: %+v", g)
	}

	// Running: the RNG comes and goes now, the rest waits.
	e = mustEdit(t)(hv.RemoveRNG(ctx(t), s, uuid))
	if !e.Changes[0].Live {
		t.Errorf("RNG removed live: %+v", e.Changes)
	}
	e = mustEdit(t)(hv.AddRNG(ctx(t), s, uuid))
	if !e.Changes[0].Live {
		t.Errorf("RNG added live: %+v", e.Changes)
	}
	for what, f := range map[string]func() (hv.Edited, error){
		"video": func() (hv.Edited, error) { return hv.SetVideo(ctx(t), s, uuid, "vga") },
		"sound": func() (hv.Edited, error) { return hv.SetSound(ctx(t), s, uuid, "none") },
		"TPM":   func() (hv.Edited, error) { return hv.RemoveTPM(ctx(t), s, uuid) },
		"VNC":   func() (hv.Edited, error) { return hv.RemoveGraphics(ctx(t), s, uuid, "vnc") },
	} {
		e := mustEdit(t)(f())
		if !e.Active || len(e.Changes) != 1 || e.Changes[0].Live {
			t.Errorf("%s while running: %+v", what, e)
		}
	}
	p = ls()
	if len(p.Video) != 1 || p.Video[0].Model != "vga" || len(p.Sound) != 0 || len(p.TPMs) != 0 || len(p.Graphics) != 0 || len(p.RNGs) != 1 {
		t.Errorf("for the next start: %+v", p)
	}
	if err := hv.Power(ctx(t), s, uuid, hv.ForceOff); err != nil {
		t.Fatal(err)
	}
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatalf("starting with what was saved: %v", err)
	}
}
