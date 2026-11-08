package conn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
)

// localSockets lists where libvirtd may listen for t on this computer, in
// the order to try them.
func localSockets(t Target) []string {
	if t.Socket != "" {
		return []string{t.Socket}
	}
	modular := "virt" + t.Driver + "d-sock" // virtqemud-sock, …
	if t.Path == "/session" {
		dir := os.Getenv("XDG_RUNTIME_DIR")
		if dir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				dir = filepath.Join(home, ".cache")
			}
		}
		return []string{filepath.Join(dir, "libvirt", modular), filepath.Join(dir, "libvirt", "libvirt-sock")}
	}
	var socks []string
	for _, dir := range []string{"/run/libvirt", "/var/run/libvirt", "/opt/homebrew/var/run/libvirt", "/usr/local/var/run/libvirt"} {
		socks = append(socks, filepath.Join(dir, "libvirt-sock"), filepath.Join(dir, modular))
	}
	return socks
}

// dialLocal connects to the first of t's sockets that exists.
func dialLocal(ctx context.Context, t Target) (net.Conn, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("there is no local libvirt on Windows: connect to a host over SSH (qemu+ssh://user@host/system)")
	}
	var d net.Dialer
	var tried []string
	for _, sock := range localSockets(t) {
		if _, err := os.Stat(sock); err != nil {
			tried = append(tried, sock)
			continue
		}
		return d.DialContext(ctx, "unix", sock)
	}
	if t.Path == "/session" && t.Socket == "" {
		// None runs: start one, as libvirt's client does (ADR-0002).
		c, err := spawnSession(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("no libvirt session daemon is running (looked for %v), and starting one failed: %w", tried, err)
		}
		return c, nil
	}
	return nil, fmt.Errorf("libvirtd is not running on this computer (looked for %v)", tried)
}
