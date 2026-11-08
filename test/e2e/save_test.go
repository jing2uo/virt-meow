//go:build e2e

package e2e

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// lifecycle keeps a session's lifecycle events with their details.
type lifecycle struct {
	mu  sync.Mutex
	evs []libvirt.DomainEventLifecycleMsg
}

func (l *lifecycle) add(m libvirt.DomainEventLifecycleMsg) {
	l.mu.Lock()
	l.evs = append(l.evs, m)
	l.mu.Unlock()
}

// waitFor waits for the event ev with detail on the machine name.
func (l *lifecycle) waitFor(t *testing.T, name string, ev libvirt.DomainEventType, detail int32) {
	t.Helper()
	wait(t, "the event", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return slices.ContainsFunc(l.evs, func(m libvirt.DomainEventLifecycleMsg) bool {
			return m.Dom.Name == name && m.Event == int32(ev) && m.Detail == detail
		})
	})
	l.mu.Lock()
	l.evs = nil
	l.mu.Unlock()
}

func listed(t *testing.T, name string) hv.Domain {
	t.Helper()
	var doms []hv.Domain
	cli(t, &doms, false, "ls")
	i := slices.IndexFunc(doms, func(d hv.Domain) bool { return d.Name == name })
	if i < 0 {
		t.Fatalf("no %s in virt-mm ls: %+v", name, doms)
	}
	return doms[i]
}

// virt-mm reset, virt-mm save [--rm]: a reset the firmware shows on the
// serial console; a saved machine listed as such and resumed by start; a
// dropped save booting afresh.
func TestResetSave(t *testing.T) {
	var lc lifecycle
	tg, err := conn.Parse("qemu+unix:///system?socket=" + filepath.Join(env(t, "VIRT_MM_E2E_RUN"), "libvirt-sock"))
	if err != nil {
		t.Fatal(err)
	}
	s := conn.New(tg, conn.Options{OnLifecycle: lc.add})
	t.Cleanup(func() { s.Close() })
	const name = "e2e-save"
	scratchPool(t, s, name)
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: name, OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: name, DiskGiB: 1, NetKind: "none", Firmware: "uefi"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if msg := cli(t, nil, true, "save", name); !strings.Contains(msg, "not running") {
		t.Errorf("saving a machine that is off: %q", msg)
	}
	if msg := cli(t, nil, true, "reset", name); msg == "" {
		t.Error("reset a machine that is off")
	}

	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	var con screen
	openConsole(t, s, uuid, &con)
	con.waitFor(t, "BdsDxe: failed to load", 1)
	cli(t, nil, false, "reset", name)
	con.waitFor(t, "BdsDxe: failed to load", 2)

	// Saved: off, listed as saved, and resumed (not booted) by start.
	var res map[string]any
	cli(t, &res, false, "save", name)
	if res["saved"] != true {
		t.Errorf("save: %v", res)
	}
	lc.waitFor(t, name, libvirt.DomainEventStopped, int32(libvirt.DomainEventStoppedSaved))
	if d := listed(t, name); d.State != hv.ShutOff || !d.Saved {
		t.Fatalf("listed after save: %+v", d)
	}
	var in hv.Info
	cli(t, &in, false, "info", name)
	if !in.Saved {
		t.Errorf("info after save: %+v", in.Domain)
	}
	cli(t, nil, false, "start", name)
	lc.waitFor(t, name, libvirt.DomainEventStarted, int32(libvirt.DomainEventStartedRestored))
	if d := listed(t, name); d.State != hv.Running || d.Saved {
		t.Fatalf("listed after resuming: %+v", d)
	}

	// Dropped: the next start boots.
	cli(t, nil, false, "save", name)
	lc.waitFor(t, name, libvirt.DomainEventStopped, int32(libvirt.DomainEventStoppedSaved))
	res = nil
	cli(t, &res, false, "save", name, "--rm")
	if res["dropped"] != true || listed(t, name).Saved {
		t.Errorf("save --rm: %v", res)
	}
	res = nil
	cli(t, &res, false, "save", name, "--rm")
	if res["dropped"] != false {
		t.Errorf("save --rm with nothing saved: %v", res)
	}
	cli(t, nil, false, "start", name)
	lc.waitFor(t, name, libvirt.DomainEventStarted, int32(libvirt.DomainEventStartedBooted))
}
