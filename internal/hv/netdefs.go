package hv

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// NetworkDefinition is a virtual network's persistent definition, as the
// XML editor shows it: what the network has when it next starts.
type NetworkDefinition struct {
	Name   string `json:"name"`
	UUID   string `json:"uuid"`
	Active bool   `json:"active"` // running: a saved change takes effect when it restarts
	XML    string `json:"xml"`
}

func networkDefinition(lv *libvirt.Libvirt, n libvirt.Network) (NetworkDefinition, error) {
	x, err := lv.NetworkGetXMLDesc(n, uint32(libvirt.NetworkXMLInactive))
	if err != nil {
		return NetworkDefinition{}, err
	}
	active, err := lv.NetworkIsActive(n)
	if err != nil {
		return NetworkDefinition{}, err
	}
	return NetworkDefinition{Name: n.Name, UUID: FormatUUID(n.UUID), Active: active == 1, XML: x}, nil
}

func lookupNetwork(lv *libvirt.Libvirt, name string) (libvirt.Network, error) {
	n, err := lv.NetworkLookupByName(name)
	var e libvirt.Error
	if errors.As(err, &e) && e.Code == uint32(libvirt.ErrNoNetwork) {
		return n, fmt.Errorf("no network %q (virt-mm nets lists them)", name)
	}
	return n, err
}

// GetNetworkDefinition reads a network's persistent definition (virsh
// net-dumpxml --inactive).
func GetNetworkDefinition(ctx context.Context, sess *conn.Session, name string) (NetworkDefinition, error) {
	var d NetworkDefinition
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		n, err := lookupNetwork(lv, name)
		if err != nil {
			return err
		}
		d, err = networkDefinition(lv, n)
		return err
	})
	return d, err
}

// RedefineNetwork saves x as the definition of the network name, and
// returns it as libvirt now has it. base is the XML the editor started
// from: if the definition changed since, nothing is saved and ErrChanged
// is returned. Its name and UUID can't change: libvirt can't rename a
// network, and another UUID is another network.
func RedefineNetwork(ctx context.Context, sess *conn.Session, name, base, x string) (NetworkDefinition, error) {
	var id struct {
		XMLName xml.Name `xml:"network"`
		Name    string   `xml:"name"`
		UUID    string   `xml:"uuid"`
	}
	if err := xml.Unmarshal([]byte(x), &id); err != nil {
		return NetworkDefinition{}, fmt.Errorf("not a network definition: %w", err)
	}
	var out NetworkDefinition
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		n, err := lookupNetwork(lv, name)
		if err != nil {
			return err
		}
		cur, err := networkDefinition(lv, n)
		if err != nil {
			return err
		}
		if base != "" && cur.XML != base {
			return ErrChanged
		}
		if strings.TrimSpace(id.Name) != name {
			return fmt.Errorf("keep <name>%s</name>: libvirt can't rename a network (add another, then remove this one)", name)
		}
		if u, err := ParseUUID(strings.TrimSpace(id.UUID)); err != nil || u != n.UUID {
			return fmt.Errorf("keep <uuid>%s</uuid>: another UUID is another network", cur.UUID)
		}
		if x == cur.XML {
			out = cur
			return nil
		}
		nn, err := lv.NetworkDefineXMLFlags(x, uint32(libvirt.NetworkDefineValidate))
		if err != nil {
			return fmt.Errorf("libvirt refused the definition: %w", err)
		}
		out, err = networkDefinition(lv, nn)
		return err
	})
	return out, err
}

// DHCPHost is a fixed address a network's DHCP server hands a MAC (virsh
// net-update ip-dhcp-host).
type DHCPHost struct {
	MAC  string `json:"mac"`
	IP   string `json:"ip"`
	Name string `json:"name,omitempty"` // the hostname it is given
	// Defined: in the definition, so the network has it after a restart.
	// Live: the running network hands it out now.
	Defined bool `json:"defined"`
	Live    bool `json:"live"`
}

// hostnameRE is what dnsmasq takes as a host's name.
var hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*$`)

// dhcpHosts reads the IPv4 DHCP hosts of a network definition, and the
// subnet they live in.
func dhcpHosts(x string) ([]libvirtxml.NetworkDHCPHost, netip.Prefix, error) {
	var def libvirtxml.Network
	if err := def.Unmarshal(x); err != nil {
		return nil, netip.Prefix{}, err
	}
	p, _ := ipv4(def)
	for _, ip := range def.IPs {
		if a, err := netip.ParseAddr(ip.Address); err == nil && a.Is4() && ip.DHCP != nil {
			return ip.DHCP.Hosts, p, nil
		}
	}
	return nil, p, nil
}

// DHCPHosts lists a network's fixed addresses, from its definition and,
// when it runs, from the running network.
func DHCPHosts(ctx context.Context, sess *conn.Session, name string) ([]DHCPHost, error) {
	var out []DHCPHost
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		n, err := lookupNetwork(lv, name)
		if err != nil {
			return err
		}
		def, err := networkDefinition(lv, n)
		if err != nil {
			return err
		}
		add := func(x string, live bool) error {
			hs, _, err := dhcpHosts(x)
			if err != nil {
				return err
			}
			for _, h := range hs {
				mac := strings.ToLower(h.MAC)
				i := slices.IndexFunc(out, func(d DHCPHost) bool { return d.MAC == mac && d.IP == h.IP && d.Name == h.Name })
				if i < 0 {
					out = append(out, DHCPHost{MAC: mac, IP: h.IP, Name: h.Name})
					i = len(out) - 1
				}
				if live {
					out[i].Live = true
				} else {
					out[i].Defined = true
				}
			}
			return nil
		}
		if err := add(def.XML, false); err != nil {
			return err
		}
		if def.Active {
			x, err := lv.NetworkGetXMLDesc(n, 0)
			if err != nil {
				return err
			}
			return add(x, true)
		}
		return nil
	})
	if out == nil {
		out = []DHCPHost{}
	}
	return out, err
}

// updateDHCPHost changes an ip-dhcp-host entry in the definition and, when
// the network runs, in the running network too, in one call.
func updateDHCPHost(lv *libvirt.Libvirt, n libvirt.Network, cmd libvirt.NetworkUpdateCommand, h libvirtxml.NetworkDHCPHost) (bool, error) {
	x, err := xml.Marshal(h)
	if err != nil {
		return false, err
	}
	active, err := lv.NetworkIsActive(n)
	if err != nil {
		return false, err
	}
	flags := libvirt.NetworkUpdateAffectConfig
	if active == 1 {
		flags |= libvirt.NetworkUpdateAffectLive
	}
	err = lv.NetworkUpdate(n, uint32(cmd), uint32(libvirt.NetworkSectionIPDhcpHost), -1, string(x), flags)
	return active == 1, err
}

// AddDHCPHost gives mac the fixed address ip (and the hostname name, if
// not "") on the network: in its definition and, when it runs, now. The
// address must be in the network's subnet; a machine that already has a
// lease keeps it until it renews.
func AddDHCPHost(ctx context.Context, sess *conn.Session, network, mac, ip, name string) (Edited, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return Edited{}, fmt.Errorf("%q is not a MAC address", mac)
	}
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() {
		return Edited{}, fmt.Errorf("%q is not an IPv4 address", ip)
	}
	if name != "" && !hostnameRE.MatchString(name) {
		return Edited{}, fmt.Errorf("hostname %q: letters, digits and '-', in dot-separated labels", name)
	}
	var out Edited
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		n, err := lookupNetwork(lv, network)
		if err != nil {
			return err
		}
		def, err := networkDefinition(lv, n)
		if err != nil {
			return err
		}
		hs, p, err := dhcpHosts(def.XML)
		if err != nil {
			return err
		}
		if !p.IsValid() {
			return fmt.Errorf("network %s has no addresses of its own to hand out (it joins a host bridge: its LAN's DHCP server does that)", network)
		}
		if !p.Contains(a) || a == p.Addr() || a == p.Masked().Addr() || a == broadcast(p) {
			return fmt.Errorf("%s is not a host address of %s's subnet %s", ip, network, p.Masked())
		}
		for _, h := range hs {
			if strings.EqualFold(h.MAC, hw.String()) || h.IP == ip {
				return fmt.Errorf("%s already has a fixed address for %s → %s: remove it first", network, h.MAC, h.IP)
			}
		}
		what := fmt.Sprintf("fixed address %s → %s", hw, ip)
		if name != "" {
			what += " (" + name + ")"
		}
		live, err := updateDHCPHost(lv, n, libvirt.NetworkUpdateCommandAddLast, libvirtxml.NetworkDHCPHost{MAC: hw.String(), IP: ip, Name: name})
		if err != nil {
			return fmt.Errorf("libvirt refused %s: %w", what, err)
		}
		out = Edited{UUID: def.UUID, Name: network, Active: live, Changes: []Change{{What: what, Live: live}}}
		return nil
	})
	return out, err
}

// RemoveDHCPHost drops the fixed address of mac (or of the address mac
// names, when it is an IP) from the network's definition and, when it
// runs, from the running network.
func RemoveDHCPHost(ctx context.Context, sess *conn.Session, network, mac string) (Edited, error) {
	var out Edited
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		n, err := lookupNetwork(lv, network)
		if err != nil {
			return err
		}
		def, err := networkDefinition(lv, n)
		if err != nil {
			return err
		}
		hs, _, err := dhcpHosts(def.XML)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(hs, func(h libvirtxml.NetworkDHCPHost) bool { return strings.EqualFold(h.MAC, mac) || h.IP == mac })
		if i < 0 {
			return fmt.Errorf("%s has no fixed address for %s", network, mac)
		}
		h := hs[i]
		what := fmt.Sprintf("fixed address %s → %s removed", h.MAC, h.IP)
		live, err := updateDHCPHost(lv, n, libvirt.NetworkUpdateCommandDelete, h)
		if err != nil {
			return fmt.Errorf("libvirt refused removing it: %w", err)
		}
		out = Edited{UUID: def.UUID, Name: network, Active: live, Changes: []Change{{What: what, Live: live}}}
		return nil
	})
	return out, err
}
