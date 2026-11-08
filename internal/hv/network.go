package hv

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Network is a libvirt virtual network.
type Network struct {
	Name      string `json:"name"`
	UUID      string `json:"uuid"`
	Active    bool   `json:"active"`
	Autostart bool   `json:"autostart"`
	Forward   string `json:"forward"`           // nat, route, bridge, open, "" for isolated
	Bridge    string `json:"bridge"`            // the host bridge it uses or makes
	Address   string `json:"address,omitempty"` // the host's IPv4 address on it, 192.168.100.1/24
	DHCP      string `json:"dhcp,omitempty"`    // its DHCP range, first-last
	Dev       string `json:"dev,omitempty"`     // the one host interface nat or route goes through
}

// Networks lists the virtual networks, sorted by name.
func Networks(ctx context.Context, s *conn.Session) ([]Network, error) {
	var out []Network
	err := s.Do(ctx, func(lv *libvirt.Libvirt) error {
		nets, _, err := lv.ConnectListAllNetworks(1, 0)
		if unsupported(err) {
			return nil // no network driver on the host
		} else if err != nil {
			return err
		}
		active, err := netSet(lv, libvirt.ConnectListNetworksActive)
		if err != nil {
			return err
		}
		auto, err := netSet(lv, libvirt.ConnectListNetworksAutostart)
		if err != nil {
			return err
		}
		out = make([]Network, len(nets))
		return each(len(nets), func(i int) error {
			n := nets[i]
			out[i] = Network{Name: n.Name, UUID: FormatUUID(n.UUID), Active: active[n.UUID], Autostart: auto[n.UUID]}
			x, err := lv.NetworkGetXMLDesc(n, 0)
			if err != nil {
				return fmt.Errorf("network %s: %w", n.Name, err)
			}
			var def libvirtxml.Network
			if err := def.Unmarshal(x); err != nil {
				return fmt.Errorf("network %s: %w", n.Name, err)
			}
			if def.Forward != nil {
				out[i].Forward = def.Forward.Mode
				if out[i].Forward == "" {
					out[i].Forward = "nat" // libvirt's default for <forward/>
				}
				out[i].Dev = def.Forward.Dev
			}
			if p, dhcp := ipv4(def); p.IsValid() {
				out[i].Address, out[i].DHCP = p.String(), dhcp
			}
			if def.Bridge != nil {
				out[i].Bridge = def.Bridge.Name
			}
			return nil
		})
	})
	if out == nil {
		out = []Network{}
	}
	slices.SortFunc(out, func(a, b Network) int { return strings.Compare(a.Name, b.Name) })
	return out, err
}

func netSet(lv *libvirt.Libvirt, flags libvirt.ConnectListAllNetworksFlags) (map[libvirt.UUID]bool, error) {
	nets, _, err := lv.ConnectListAllNetworks(1, flags)
	if err != nil {
		return nil, err
	}
	set := make(map[libvirt.UUID]bool, len(nets))
	for _, n := range nets {
		set[n.UUID] = true
	}
	return set, nil
}

// bridgesCmd lists the host's bridges from sysfs, one per line.
const bridgesCmd = `for d in /sys/class/net/*/bridge; do [ -d "$d" ] && basename "$(dirname "$d")"; done; true`

// Bridges lists the host's bridge interfaces, for a machine to join the
// host's own network directly, the likeliest first. libvirt's interface
// driver says, where the host has one; most don't (it needs netcf or
// NetworkManager), so over SSH the host's sysfs is read instead. A local
// connection without the driver reads this computer's sysfs.
func Bridges(ctx context.Context, s *conn.Session) ([]string, error) {
	var out []string
	err := s.Do(ctx, func(lv *libvirt.Libvirt) error {
		ifaces, _, err := lv.ConnectListAllInterfaces(1, libvirt.ConnectListInterfacesActive)
		if err != nil {
			return nil // no interface driver
		}
		for _, i := range ifaces {
			x, err := lv.InterfaceGetXMLDesc(i, 0)
			if err != nil {
				continue
			}
			var def libvirtxml.Interface
			if def.Unmarshal(x) == nil && def.Bridge != nil {
				out = append(out, i.Name)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		switch b, rerr := s.Run(ctx, bridgesCmd); {
		case rerr == nil:
			out = strings.Fields(string(b))
		case errors.Is(rerr, conn.ErrNotSSH) && runtime.GOOS == "linux":
			dirs, _ := filepath.Glob("/sys/class/net/*/bridge")
			for _, d := range dirs {
				out = append(out, filepath.Base(filepath.Dir(d)))
			}
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		if ra, rb := bridgeRank(a), bridgeRank(b); ra != rb {
			return ra - rb
		}
		return strings.Compare(a, b)
	})
	return out, nil
}

// bridgeRank puts a host's own bridges (br0) before those other software
// makes for its own use.
func bridgeRank(name string) int {
	for _, p := range []string{"virbr", "docker", "podman", "cni", "lxc", "incus", "lxd"} {
		if strings.HasPrefix(name, p) {
			return 1
		}
	}
	return 0
}
