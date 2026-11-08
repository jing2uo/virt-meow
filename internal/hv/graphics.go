package hv

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// GraphicsServer is where a running machine's VNC or SPICE server listens,
// as the host sees it.
type GraphicsServer struct {
	Kind    string `json:"kind"`    // vnc or spice
	Network string `json:"network"` // tcp or unix
	Addr    string `json:"addr"`    // host:port, or a socket's path
}

// FindGraphics reads where the running machine uuid's console of kind (vnc
// or spice) listens. One on every address of the host (0.0.0.0, ::) is
// reached on its loopback.
func FindGraphics(ctx context.Context, sess *conn.Session, uuid, kind string) (GraphicsServer, error) {
	if err := graphicKind(kind); err != nil {
		return GraphicsServer{}, err
	}
	u, err := ParseUUID(uuid)
	if err != nil {
		return GraphicsServer{}, err
	}
	var out GraphicsServer
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		state, _, err := lv.DomainGetState(dom, 0)
		if err != nil {
			return err
		}
		if !stateOf(libvirt.DomainState(state)).Active() {
			return fmt.Errorf("%s is not running", dom.Name)
		}
		x, err := lv.DomainGetXMLDesc(dom, 0)
		if err != nil {
			return err
		}
		var d libvirtxml.Domain
		if err := d.Unmarshal(x); err != nil {
			return err
		}
		out, err = graphicsServer(&d, kind)
		return err
	})
	return out, err
}

func graphicsServer(d *libvirtxml.Domain, kind string) (GraphicsServer, error) {
	if d.Devices != nil {
		for _, g := range d.Devices.Graphics {
			var host, socket string
			var port, tlsPort int
			var ls []libvirtxml.DomainGraphicListener
			switch {
			case kind == "vnc" && g.VNC != nil:
				host, socket, port, ls = g.VNC.Listen, g.VNC.Socket, g.VNC.Port, g.VNC.Listeners
			case kind == "spice" && g.Spice != nil:
				host, port, tlsPort, ls = g.Spice.Listen, g.Spice.Port, g.Spice.TLSPort, g.Spice.Listeners
			default:
				continue
			}
			for _, l := range ls {
				switch {
				case l.Socket != nil && l.Socket.Socket != "":
					socket = l.Socket.Socket
				case l.Address != nil && l.Address.Address != "":
					host = l.Address.Address
				case l.Network != nil && l.Network.Address != "":
					host = l.Network.Address
				}
			}
			if socket != "" {
				return GraphicsServer{kind, "unix", socket}, nil
			}
			if port <= 0 {
				if tlsPort > 0 {
					return GraphicsServer{}, fmt.Errorf("%s's SPICE console takes TLS only, which virt-meow doesn't speak yet", d.Name)
				}
				return GraphicsServer{}, fmt.Errorf("%s's %s console has no port (it listens nowhere)", d.Name, strings.ToUpper(kind))
			}
			switch host {
			case "", "0.0.0.0":
				host = "127.0.0.1"
			case "::":
				host = "::1"
			}
			return GraphicsServer{kind, "tcp", net.JoinHostPort(host, strconv.Itoa(port))}, nil
		}
	}
	return GraphicsServer{}, fmt.Errorf("%s has no %s console (virt-mm device add %s graphics %s)", d.Name, strings.ToUpper(kind), d.Name, kind)
}

// DialGraphics connects to the running machine uuid's console of kind: over
// the session's SSH client when the host is another computer, so the server
// can stay on the host's loopback.
func DialGraphics(ctx context.Context, sess *conn.Session, uuid, kind string) (net.Conn, GraphicsServer, error) {
	g, err := FindGraphics(ctx, sess, uuid, kind)
	if err != nil {
		return nil, g, err
	}
	c, err := sess.DialHost(ctx, g.Network, g.Addr)
	return c, g, err
}
