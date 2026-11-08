//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// eventsCLI runs virt-mm events with args until the test ends; its events
// arrive on the channel once it says it follows them.
func eventsCLI(t *testing.T, args ...string) (*exec.Cmd, <-chan hv.Event) {
	t.Helper()
	cmd := virtmm(t, append([]string{"events"}, args...)...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		var all []string
		for sc.Scan() {
			all = append(all, sc.Text())
			if strings.Contains(sc.Text(), "following events") {
				ready <- ""
			}
		}
		ready <- strings.Join(all, "\n")
	}()
	select {
	case msg := <-ready:
		if msg != "" {
			t.Fatalf("virt-mm events ended: %s", msg)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("virt-mm events didn't start following in 30 s")
	}
	evs := make(chan hv.Event, 100)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			var e hv.Event
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				t.Errorf("not a JSON line: %q", sc.Text())
				continue
			}
			evs <- e
		}
		close(evs)
	}()
	return cmd, evs
}

// next is the next event on evs, failing the test after 30 s.
func next(t *testing.T, evs <-chan hv.Event) hv.Event {
	t.Helper()
	select {
	case e, ok := <-evs:
		if !ok {
			t.Fatal("virt-mm events stopped")
		}
		return e
	case <-time.After(30 * time.Second):
		t.Fatal("no event in 30 s")
	}
	return hv.Event{}
}

// virt-mm events: one JSON line per lifecycle event, with its detail, only
// the machine asked for, through a restart of libvirtd, until Ctrl+C.
func TestEvents(t *testing.T) {
	s := qemuSession(t)
	const name = "e2e-events"
	scratchPool(t, s, name)
	all, evs := eventsCLI(t)
	_, mine := eventsCLI(t, name)

	c, err := hv.Create(ctx(t), s, hv.Spec{Name: name, OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: name, DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	for _, a := range []hv.Action{hv.Start, hv.Pause, hv.Resume, hv.ForceOff} {
		if err := hv.Power(ctx(t), s, uuid, a); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"defined added", "started booted", "paused paused", "resumed unpaused", "stopped destroyed"}
	// Another machine: in the list of all, not in the one of e2e-events.
	other, err := hv.Define(ctx(t), s, "<domain type='qemu'><name>e2e-events-other</name><memory unit='MiB'>128</memory>"+
		"<os><type arch='x86_64'>hvm</type></os></domain>")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hv.Delete(ctx(t), s, other.UUID, false); err != nil {
		t.Fatal(err)
	}
	// In order; libvirt adds a few of its own (QEMU starts resumed, a
	// force-off is a host shutdown first).
	for _, ch := range []<-chan hv.Event{evs, mine} {
		var got []string
		for _, w := range want {
			for {
				e := next(t, ch)
				if e.Name != name || e.UUID != uuid {
					t.Fatalf("%+v among %s's events %v", e, name, got)
				}
				got = append(got, string(e.Kind)+" "+e.Detail)
				if got[len(got)-1] == w {
					break
				}
			}
		}
	}
	for _, w := range []string{"defined added", "undefined removed"} {
		if e := next(t, evs); e.Name != "e2e-events-other" || string(e.Kind)+" "+e.Detail != w {
			t.Fatalf("got %+v, want e2e-events-other %s", e, w)
		}
	}

	// libvirtd restarts: virt-mm events connects again and goes on.
	if out, err := exec.Command("podman", "exec", env(t, "VIRT_MM_E2E_CONTAINER"), "pkill", "-x", "libvirtd").CombinedOutput(); err != nil {
		t.Fatalf("killing libvirtd: %v: %s", err, out)
	}
	if err := probeDaemon(t, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	// Until virt-mm events has connected again, a start's event is lost:
	// start, and if nothing comes, stop and try again.
	started := func(d time.Duration) bool {
		end := time.After(d)
		for {
			select {
			case e, ok := <-mine:
				if !ok {
					t.Fatal("virt-mm events stopped")
				}
				if e.Kind == hv.EvStarted {
					return true
				}
			case <-end:
				return false
			}
		}
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
			t.Fatal(err)
		}
		if started(3 * time.Second) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no events within 30 s of libvirtd's restart")
		}
		if err := hv.Power(ctx(t), s, uuid, hv.ForceOff); err != nil {
			t.Fatal(err)
		}
		started(time.Second) // drains its stop, if virt-mm events was back by then
	}
	hv.Power(ctx(t), s, uuid, hv.ForceOff)

	// Ctrl+C ends it, without an error.
	all.Process.Signal(syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- all.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("after Ctrl+C: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("virt-mm events didn't end within 10 s of Ctrl+C")
	}
}
