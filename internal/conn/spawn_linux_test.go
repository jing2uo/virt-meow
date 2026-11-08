package conn

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain lets this test binary stand in for a session daemon: run with
// VIRT_MM_FAKE_DAEMON set, it listens on the session socket, notes its
// arguments in that file, and ends after a while.
func TestMain(m *testing.M) {
	if log := os.Getenv("VIRT_MM_FAKE_DAEMON"); log != "" {
		fakeDaemon(log)
		return
	}
	os.Exit(m.Run())
}

func fakeDaemon(log string) {
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	f.WriteString(strings.Join(os.Args[1:], " ") + "\n")
	f.Close()
	time.Sleep(200 * time.Millisecond) // slow to start, as a real one is
	ln, err := net.Listen("unix", filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "libvirt", "libvirt-sock"))
	if err != nil {
		os.Exit(1)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	time.Sleep(5 * time.Second)
}

// No session daemon: one is started (once, for two connections at once),
// with libvirt's client's --timeout=120, and both connect to it.
func TestSpawnSession(t *testing.T) {
	run := t.TempDir()
	log := filepath.Join(t.TempDir(), "started")
	t.Setenv("XDG_RUNTIME_DIR", run)
	t.Setenv("LIBVIRTD_PATH", os.Args[0])
	t.Setenv("VIRT_MM_FAKE_DAEMON", log)
	tg, _ := Parse("qemu:///session")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := dialLocal(ctx, tg)
			if err == nil {
				c.Close()
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(b)); got != "--timeout=120" {
		t.Fatalf("daemons started: %q, want one with --timeout=120", got)
	}

	// A daemon that won't start says so.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("LIBVIRTD_PATH", "/bin/false")
	if _, err := dialLocal(ctx, tg); err == nil || !strings.Contains(err.Error(), "didn't start") {
		t.Fatalf("a daemon that exits: %v", err)
	}
}
