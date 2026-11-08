package conn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// spawnTimeout is how long a session daemon started here lives without a
// client: libvirt's own client starts it with the same.
const spawnTimeout = "--timeout=120"

// sessionDaemon finds the daemon that serves t's driver for one user: the
// modular one (virtqemud for qemu) where it is installed, else libvirtd,
// as libvirt's client chooses; $LIBVIRTD_PATH overrides, as there. Its
// socket is the one it will listen on.
func sessionDaemon(t Target) (path, socket string, err error) {
	socks := localSockets(t)
	if _, err := os.Stat("/.flatpak-info"); err == nil {
		return "", "", fmt.Errorf("virt-meow runs in a Flatpak sandbox, which can't start this computer's virt%sd: start it with `virt%[1]sd --timeout=120 &` (or libvirtd) outside, or connect to qemu:///system", t.Driver)
	}
	if p := os.Getenv("LIBVIRTD_PATH"); p != "" {
		return p, socks[1], nil
	}
	look := func(name string) string {
		for _, dir := range []string{"/usr/sbin", "/usr/local/sbin", "/usr/libexec", "/sbin"} {
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
				return p
			}
		}
		p, _ := exec.LookPath(name)
		return p
	}
	if p := look("virt" + t.Driver + "d"); p != "" {
		return p, socks[0], nil
	}
	if p := look("libvirtd"); p != "" {
		return p, socks[1], nil
	}
	return "", "", fmt.Errorf("no virt%sd or libvirtd on this computer to start a session daemon with", t.Driver)
}

// spawnSession starts t's session daemon when none listens, as libvirt's
// client does: under a lock on its socket's path (two virt-mm processes starting
// at once start one daemon), detached from virt-mm so that it outlives it
// and ends on its own once unused, then waits for its socket within ctx.
// It returns the connection to it.
func spawnSession(ctx context.Context, t Target) (net.Conn, error) {
	path, sock, err := sessionDaemon(t)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(sock+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	locked := make(chan error, 1)
	go func() { locked <- syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) }()
	select {
	case err := <-locked:
		if err != nil {
			return nil, fmt.Errorf("locking %s: %w", lock.Name(), err)
		}
	case <-ctx.Done():
		return nil, ctx.Err() // the lock goes with the file
	}

	var d net.Dialer
	// Another virt-mm (or virsh) may have started it while we waited.
	if c, err := d.DialContext(ctx, "unix", sock); err == nil {
		return c, nil
	}
	cmd := exec.Command(path, spawnTimeout)
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", path, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }() // reaped if it ends while virt-mm runs
	for wait := 20 * time.Millisecond; ; wait = min(wait*2, 500*time.Millisecond) {
		if c, err := d.DialContext(ctx, "unix", sock); err == nil {
			return c, nil
		}
		select {
		case err := <-exited:
			if err == nil {
				err = errors.New("it exited")
			}
			return nil, fmt.Errorf("%s %s didn't start: %w", path, spawnTimeout, err)
		case <-ctx.Done():
			return nil, fmt.Errorf("%s %s started but %s didn't appear: %w", path, spawnTimeout, sock, ctx.Err())
		case <-time.After(wait):
		}
	}
}
