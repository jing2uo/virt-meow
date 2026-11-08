// Package conn keeps libvirt connections open: it parses libvirt URIs, dials
// the local socket or an in-process SSH channel, checks host keys, and owns
// the go-libvirt client of each connection (docs/adr/0002).
package conn

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Transport is how a connection reaches libvirtd.
type Transport string

const (
	Local Transport = "local" // this computer's Unix socket
	SSH   Transport = "ssh"   // an in-process SSH channel
)

// Proxy is how an SSH connection reaches the remote libvirt socket.
type Proxy string

const (
	ProxyAuto        Proxy = ""            // streamlocal, then virt-ssh-helper
	ProxyStreamLocal Proxy = "streamlocal" // direct-streamlocal@openssh.com
	ProxyHelper      Proxy = "helper"      // virt-ssh-helper in an exec channel
)

// Target is a parsed libvirt URI.
type Target struct {
	URI       string // as given
	Driver    string // qemu, test, …
	Path      string // /system, /session, /default
	Transport Transport
	User      string // SSH user; "" for the current one
	Host      string // SSH host
	Port      string // SSH port; "" for 22
	Socket    string // explicit socket (?socket=); remote for SSH
	Proxy     Proxy
}

// Parse reads a libvirt URI: driver[+transport]://[user@][host[:port]]/path[?query].
func Parse(raw string) (Target, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Target{}, fmt.Errorf("libvirt URI %q: %w", raw, err)
	}
	if u.Scheme == "" {
		return Target{}, fmt.Errorf("libvirt URI %q: no driver (want e.g. qemu:///system)", raw)
	}
	driver, transport, _ := strings.Cut(u.Scheme, "+")
	t := Target{
		URI:    raw,
		Driver: driver,
		Path:   u.Path,
		Socket: u.Query().Get("socket"),
		Proxy:  Proxy(u.Query().Get("proxy")),
	}
	if t.Path == "" || t.Path == "/" {
		return Target{}, fmt.Errorf("libvirt URI %q: no path (want /system or /session)", raw)
	}
	switch transport {
	case "", "unix":
		if u.Host != "" && transport == "" {
			return Target{}, fmt.Errorf("libvirt URI %q: a remote host needs a transport (e.g. %s+ssh://)", raw, driver)
		}
		t.Transport = Local
	case "ssh", "libssh", "libssh2":
		if u.Hostname() == "" {
			return Target{}, fmt.Errorf("libvirt URI %q: no host", raw)
		}
		t.Transport = SSH
		t.Host = u.Hostname()
		t.Port = u.Port()
		if u.User != nil {
			t.User = u.User.Username()
		}
	default:
		return Target{}, fmt.Errorf("libvirt URI %q: transport %q is not supported yet", raw, transport)
	}
	switch t.Proxy {
	case ProxyAuto, ProxyStreamLocal, ProxyHelper:
	default:
		return Target{}, fmt.Errorf("libvirt URI %q: proxy %q (want streamlocal or helper)", raw, t.Proxy)
	}
	return t, nil
}

// DriverURI is what libvirtd is asked to open once the socket is connected:
// the driver and path alone, as libvirt's remote driver sends it.
func (t Target) DriverURI() string { return t.Driver + "://" + t.Path }

// Addr is the SSH server's host:port.
func (t Target) Addr() string {
	port := t.Port
	if port == "" {
		port = "22"
	}
	return net.JoinHostPort(t.Host, port)
}

// Remote reports whether the target is on another computer.
func (t Target) Remote() bool { return t.Transport != Local }

// String is a short name for the target: host/path, or the path for a local one.
func (t Target) String() string {
	if t.Transport == Local {
		return "localhost" + t.Path
	}
	h := t.Host
	if t.User != "" {
		h = t.User + "@" + h
	}
	return h + t.Path
}
