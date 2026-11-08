//go:build e2e

// Package e2e runs virt-meow's core against a real libvirtd: the container in
// test/libvirtd, started by test/e2e.sh (`make e2e`), which exports
//
//	VIRT_MM_E2E_RUN        the directory holding its libvirt-sock
//	VIRT_MM_E2E_SSH        its sshd, host:port
//	VIRT_MM_E2E_KEY        the private key sshd accepts for root
//	VIRT_MM_E2E_CONTAINER  its name, for podman exec
//
// The tests use libvirt's test driver. Its state lives as long as some
// connection to it is open, so each test works within one session.
package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"golang.org/x/crypto/ssh"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set: run make e2e", name)
	}
	watchDaemon(t)
	return v
}

// daemon watches the container's libvirtd around each test that uses it.
// One that stops answering (it can deadlock on itself: see hv.Console)
// fails the test it stopped in, with its log, and is restarted, so that
// the tests after it don't each wait out their timeouts on it.
var daemon struct {
	sync.Mutex
	seen map[string]bool
	dead string // set when a restart didn't bring it back
}

func watchDaemon(t *testing.T) {
	t.Helper()
	daemon.Lock()
	if daemon.seen == nil {
		daemon.seen = map[string]bool{}
	}
	dead, first := daemon.dead, !daemon.seen[t.Name()]
	daemon.seen[t.Name()] = true
	daemon.Unlock()
	if dead != "" {
		t.Fatal(dead)
	}
	if !first {
		return
	}
	checkDaemon(t, "before the test")
	t.Cleanup(func() { checkDaemon(t, "at the end of the test") })
}

// checkDaemon fails t if libvirtd doesn't answer a new connection, and
// restarts it.
func checkDaemon(t *testing.T, when string) {
	t.Helper()
	err := probeDaemon(t, 10*time.Second)
	if err == nil {
		return
	}
	name := env(t, "VIRT_MM_E2E_CONTAINER")
	ps, _ := podman("exec", name, "ps", "-o", "pid,stat,wchan:32,args", "-C", "libvirtd").CombinedOutput()
	logs, _ := podman("logs", "--tail", "40", name).CombinedOutput()
	t.Errorf("libvirtd stopped answering %s: %v\n%s--- podman logs --tail 40 %s ---\n%s", when, err, ps, name, logs)
	podman("exec", name, "pkill", "-9", "-x", "libvirtd").Run()
	if err := probeDaemon(t, 20*time.Second); err != nil {
		daemon.Lock()
		daemon.dead = fmt.Sprintf("libvirtd stopped answering in %s and a restart didn't bring it back: %v", t.Name(), err)
		daemon.Unlock()
		t.Error(daemon.dead)
	}
}

// probeDaemon opens new connections to libvirtd until one answers.
func probeDaemon(t *testing.T, within time.Duration) error {
	c, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	tg, err := conn.Parse("qemu+unix:///system?socket=" + filepath.Join(env(t, "VIRT_MM_E2E_RUN"), "libvirt-sock"))
	if err != nil {
		return err
	}
	for {
		s := conn.New(tg, conn.Options{})
		err = s.Do(c, func(lv *libvirt.Libvirt) error { _, err := lv.ConnectGetLibVersion(); return err })
		s.Close()
		if err == nil || c.Err() != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func localTarget(t *testing.T) conn.Target {
	tg, err := conn.Parse("test+unix:///default?socket=" + filepath.Join(env(t, "VIRT_MM_E2E_RUN"), "libvirt-sock"))
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

// sshHome makes a HOME whose ~/.ssh/id_ed25519 is the key sshd accepts, and
// no agent, so the key file is what logs in.
func sshHome(t *testing.T) {
	key, err := os.ReadFile(env(t, "VIRT_MM_E2E_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
}

// recorder keeps what a session reports.
type recorder struct {
	mu     sync.Mutex
	states []conn.Status
	events []hv.Event
}

func (r *recorder) opts(o conn.Options) conn.Options {
	o.OnState = func(s conn.State) { r.mu.Lock(); r.states = append(r.states, s.Status); r.mu.Unlock() }
	o.OnLifecycle = func(m libvirt.DomainEventLifecycleMsg) {
		r.mu.Lock()
		r.events = append(r.events, hv.EventOf(m))
		r.mu.Unlock()
	}
	return o
}

func (r *recorder) waitEvent(t *testing.T, k hv.EventKind) {
	t.Helper()
	wait(t, "event "+string(k), func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.ContainsFunc(r.events, func(e hv.Event) bool { return e.Kind == k })
	})
}

func wait(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func machine(t *testing.T, s *conn.Session, name string) hv.Domain {
	t.Helper()
	doms, err := hv.List(ctx(t), s)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(doms, func(d hv.Domain) bool { return d.Name == name })
	if i < 0 {
		t.Fatalf("no machine %q in %v", name, doms)
	}
	return doms[i]
}

func TestListPowerEvents(t *testing.T) {
	var r recorder
	s := conn.New(localTarget(t), r.opts(conn.Options{}))
	defer s.Close()

	d := machine(t, s, "test")
	if d.State != hv.Running || d.VCPUs == 0 || d.MaxMemKiB == 0 || !d.Persistent {
		t.Fatalf("test machine: %+v", d)
	}
	steps := []struct {
		a     hv.Action
		ev    hv.EventKind
		state hv.State
	}{
		{hv.Pause, hv.EvPaused, hv.Paused},
		{hv.Resume, hv.EvResumed, hv.Running},
		{hv.ForceOff, hv.EvStopped, hv.ShutOff},
		{hv.Start, hv.EvStarted, hv.Running},
	}
	for _, st := range steps {
		if !st.a.Allowed(d.State) {
			t.Fatalf("%s not allowed in %s", st.a, d.State)
		}
		if err := hv.Power(ctx(t), s, d.UUID, st.a); err != nil {
			t.Fatalf("%s: %v", st.a, err)
		}
		r.waitEvent(t, st.ev)
		if d = machine(t, s, "test"); d.State != st.state {
			t.Fatalf("after %s: %s, want %s", st.a, d.State, st.state)
		}
	}
}

func TestSSHHostKeys(t *testing.T) {
	sshHome(t)
	addr := env(t, "VIRT_MM_E2E_SSH")
	hk := &conn.HostKeys{Own: filepath.Join(t.TempDir(), "known_hosts")}

	tg, err := conn.Parse("test+ssh://root@" + addr + "/default")
	if err != nil {
		t.Fatal(err)
	}
	s := conn.New(tg, conn.Options{HostKeys: hk})
	defer s.Close()
	err = s.Connect(ctx(t))
	var unknown *conn.UnknownHostKeyError
	if !errors.As(err, &unknown) {
		t.Fatalf("first contact: %v, want an unknown host key", err)
	}
	if err := hk.Trust(unknown.Addr, unknown.Key); err != nil {
		t.Fatal(err)
	}
	machine(t, s, "test")

	// The same host through virt-ssh-helper, on the same SSH client.
	helper, _ := conn.Parse("test+ssh://root@" + addr + "/default?proxy=helper")
	hs := conn.New(helper, conn.Options{HostKeys: hk})
	defer hs.Close()
	machine(t, hs, "test")

	// A different key on record: refused, and not retried on its own.
	bad := &conn.HostKeys{Own: filepath.Join(t.TempDir(), "known_hosts")}
	other := conn.New(tg, conn.Options{HostKeys: bad, RetryMin: 50 * time.Millisecond})
	defer other.Close()
	if err := bad.Trust(unknown.Addr, otherKey(t)); err != nil {
		t.Fatal(err)
	}
	err = other.Connect(ctx(t))
	var changed *conn.HostKeyChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("with another key on record: %v, want a changed host key", err)
	}
}

func TestReconnectAfterDaemonRestart(t *testing.T) {
	var r recorder
	s := conn.New(localTarget(t), r.opts(conn.Options{RetryMin: 200 * time.Millisecond}))
	defer s.Close()
	machine(t, s, "test")

	out, err := podman("exec", env(t, "VIRT_MM_E2E_CONTAINER"), "pkill", "-x", "libvirtd").CombinedOutput()
	if err != nil {
		t.Fatalf("killing libvirtd: %v: %s", err, out)
	}
	wait(t, "disconnected", func() bool { return s.State().Status == conn.Disconnected })
	wait(t, "connected again", func() bool { return s.State().Status == conn.Connected })

	// Events are subscribed again on the new connection.
	d := machine(t, s, "test")
	if err := hv.Power(ctx(t), s, d.UUID, hv.Pause); err != nil {
		t.Fatal(err)
	}
	r.waitEvent(t, hv.EvPaused)
}

func TestCallTimeoutDropsConnection(t *testing.T) {
	s := conn.New(localTarget(t), conn.Options{})
	defer s.Close()
	machine(t, s, "test")

	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	block := make(chan struct{})
	defer close(block)
	err := s.Do(short, func(*libvirt.Libvirt) error { <-block; return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked call: %v, want deadline exceeded", err)
	}
	if st := s.State().Status; st != conn.Disconnected {
		t.Fatalf("after a timed-out call: %s, want disconnected", st)
	}
	machine(t, s, "test") // the next call reconnects
}

func otherKey(t *testing.T) ssh.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The GUI's path: the Manager holds the unknown key until the user answers
// with the fingerprint they were shown.
func TestManagerTrustHostKey(t *testing.T) {
	sshHome(t)
	t.Setenv("VIRT_MM_CONFIG", t.TempDir())
	var mu sync.Mutex
	var seen []app.ConnInfo
	m, err := app.NewManager(app.Callbacks{OnConnection: func(c app.ConnInfo) {
		mu.Lock()
		seen = append(seen, c)
		mu.Unlock()
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	c, err := m.Add("", "test+ssh://root@"+env(t, "VIRT_MM_E2E_SSH")+"/default", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Connect(ctx(t), c.ID); err == nil {
		t.Fatal("connected to a host whose key nobody trusted")
	}
	var prompt *app.HostKeyPrompt
	wait(t, "the host key prompt", func() bool {
		for _, ci := range m.Connections() {
			if ci.ID == c.ID && ci.HostKey != nil {
				prompt = ci.HostKey
				return true
			}
		}
		return false
	})
	if err := m.TrustHostKey(ctx(t), c.ID, "SHA256:not-what-was-shown"); err == nil {
		t.Fatal("trusted a fingerprint the host didn't offer")
	}
	if err := m.TrustHostKey(ctx(t), c.ID, prompt.Fingerprint); err != nil {
		t.Fatal(err)
	}
	doms, err := m.Domains(ctx(t), c.ID)
	if err != nil || len(doms) == 0 {
		t.Fatalf("Domains after trusting: %v, %v", doms, err)
	}
	wait(t, "a connected event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.ContainsFunc(seen, func(ci app.ConnInfo) bool { return ci.Status == "connected" && ci.HostKey == nil })
	})
}
