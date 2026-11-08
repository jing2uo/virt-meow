//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// screen collects what a console prints.
type screen struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *screen) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *screen) count(text string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Count(s.buf.String(), text)
}

// waitFor waits until text shows n times; firmware under TCG is slow.
func (s *screen) waitFor(t *testing.T, text string, n int) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for s.count(text) < n {
		if time.Now().After(deadline) {
			s.mu.Lock()
			t.Fatalf("no %q (×%d) on the console after 90 s:\n%q", text, n, s.buf.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// openConsole runs hv.Console on s until the test ends, writing to out;
// what is written to the pipe goes to the guest. The console ends before
// the cleanups registered ahead of it (forcing the machine off, closing
// s): a connection closed under an open console can deadlock libvirtd
// (see hv.Console).
func openConsole(t *testing.T, s *conn.Session, uuid string, out io.Writer) (*io.PipeWriter, <-chan error) {
	in, w := io.Pipe()
	res := make(chan error, 1)
	go func() { res <- hv.Console(context.Background(), s, uuid, "", false, in, out) }()
	ended := make(chan error, 1)
	go func() { err := <-res; ended <- err; close(ended) }()
	t.Cleanup(func() {
		w.Close()
		select {
		case <-ended:
		case <-time.After(15 * time.Second):
			t.Error("the console didn't end within 15 s of its input")
		}
	})
	return w, ended
}

// consoleCLI starts virt-mm console; it is left (its stdin closed) before
// the cleanups registered ahead of it, and killed only if it doesn't go.
func consoleCLI(t *testing.T, out io.Writer, args ...string) (*exec.Cmd, io.WriteCloser) {
	cmd := virtmm(t, append([]string{"console"}, args...)...)
	cmd.Stdout = out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
			t.Error("virt-mm console didn't leave at the end of its stdin")
		}
	})
	return cmd, stdin
}

// virt-mm console: the serial console both ways, through the CLI on the
// local socket and through hv over SSH. The guest is OVMF, whose shell
// talks on the serial port when there is nothing to boot.
func TestConsole(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-con")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-con", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-con", DiskGiB: 1, NetKind: "none", Firmware: "uefi"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if msg := cli(t, nil, true, "console", "e2e-con"); !strings.Contains(msg, "not running") {
		t.Errorf("console of a machine that is off: %q", msg)
	}
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })

	// Locally, through the CLI: stdin is a pipe, not a terminal.
	var local screen
	cmd, stdin := consoleCLI(t, &local, "e2e-con")
	local.waitFor(t, "Shell>", 1)
	io.WriteString(stdin, "echo virt-mm-local\r")
	// The shell echoes each key with cursor moves; the line is echo's.
	local.waitFor(t, "\r\nvirt-mm-local\r\n", 1)

	// A second client is refused while the first has it.
	if msg := cli(t, nil, true, "console", "e2e-con"); !strings.Contains(msg, "force") {
		t.Errorf("a second console: %q", msg)
	}
	// Ctrl+] leaves.
	io.WriteString(stdin, "\x1d")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("leaving with Ctrl+]: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Ctrl+] did not end the console")
	}

	// Over SSH, through hv.
	sshHome(t)
	hk := &conn.HostKeys{Own: filepath.Join(t.TempDir(), "known_hosts")}
	tg, _ := conn.Parse("qemu+ssh://root@" + env(t, "VIRT_MM_E2E_SSH") + "/system")
	ss := conn.New(tg, conn.Options{HostKeys: hk})
	t.Cleanup(func() { ss.Close() })
	var unknown *conn.UnknownHostKeyError
	if err := ss.Connect(ctx(t)); errors.As(err, &unknown) {
		hk.Trust(unknown.Addr, unknown.Key)
	} else if err != nil {
		t.Fatal(err)
	}
	var remote screen
	w, res := openConsole(t, ss, uuid, &remote)
	// The shell is waiting at its prompt already: a new line shows it.
	time.Sleep(time.Second)
	io.WriteString(w, "\r")
	remote.waitFor(t, "Shell>", 1)
	io.WriteString(w, "echo virt-mm-ssh\r")
	remote.waitFor(t, "\r\nvirt-mm-ssh\r\n", 1)

	// --force takes it over: the SSH client's console ends.
	var taken screen
	cmd, stdin = consoleCLI(t, &taken, "e2e-con", "--force")
	select {
	case err := <-res:
		if err != nil {
			t.Logf("the SSH console ended with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("--force did not take the console over")
	}
	w.Close()
	time.Sleep(time.Second)
	io.WriteString(stdin, "\recho virt-mm-forced\r")
	taken.waitFor(t, "\r\nvirt-mm-forced\r\n", 1)
	stdin.Close() // the end of stdin leaves too
	if err := cmd.Wait(); err != nil {
		t.Fatalf("leaving at the end of stdin: %v", err)
	}

	// The end of its context finishes the stream and keeps the connection:
	// dropping it under the open stream can deadlock libvirtd.
	in, keep := io.Pipe()
	defer keep.Close()
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	var last screen
	go func() { ended <- hv.Console(cctx, s, uuid, "", false, in, &last) }()
	time.Sleep(time.Second)
	cancel()
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the console's context ended: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the console didn't end with its context")
	}
	if st := s.State().Status; st != conn.Connected {
		t.Errorf("after a console's context ended, the connection is %s", st)
	}
}
