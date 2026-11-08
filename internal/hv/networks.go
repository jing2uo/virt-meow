package hv

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// NewNetwork is a virtual network to add. Type is one of
//
//	nat       guests reach out through the host's routes, masqueraded
//	route     the same, unmasqueraded: the LAN must route the subnet back
//	isolated  guests reach each other and the host only
//	bridge    guests join Bridge, a bridge that exists on the host
//
// The first three make a bridge of their own (Bridge, or libvirt's next
// virbrN) with Address on it, a CIDR such as 192.168.100.1/24 (the first
// free 192.168.N.1/24 when empty), and hand out DHCP leases unless NoDHCP:
// in DHCP ("start-end"), or above Address to the end of the subnet. Dev
// limits nat and route to one host interface.
type NewNetwork struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Bridge    string `json:"bridge,omitempty"`
	Address   string `json:"address,omitempty"`
	DHCP      string `json:"dhcp,omitempty"`
	NoDHCP    bool   `json:"no_dhcp,omitempty"`
	Dev       string `json:"dev,omitempty"`
	Autostart bool   `json:"autostart"`
}

// ifaceRE is what Linux takes as an interface name.
var ifaceRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)

// networkDef turns nn into libvirt's definition; used are the subnets
// other networks take, for picking a free one.
func networkDef(nn NewNetwork, used []netip.Prefix) (libvirtxml.Network, error) {
	def := libvirtxml.Network{Name: nn.Name}
	if !nameRE.MatchString(nn.Name) {
		return def, fmt.Errorf("network name %q: letters, digits, '.', '_' and '-', starting with a letter or digit", nn.Name)
	}
	if nn.Bridge != "" && !ifaceRE.MatchString(nn.Bridge) {
		return def, fmt.Errorf("bridge name %q: up to 15 letters, digits, '.', '_' and '-'", nn.Bridge)
	}
	if nn.Dev != "" && !ifaceRE.MatchString(nn.Dev) {
		return def, fmt.Errorf("interface name %q", nn.Dev)
	}
	switch nn.Type {
	case "bridge":
		if nn.Bridge == "" {
			return def, errors.New("a bridge network needs the host bridge it joins (--bridge)")
		}
		if nn.Address != "" || nn.DHCP != "" || nn.Dev != "" {
			return def, errors.New("a bridge network takes no address, DHCP or interface: the host's LAN provides them")
		}
		def.Forward = &libvirtxml.NetworkForward{Mode: "bridge"}
		def.Bridge = &libvirtxml.NetworkBridge{Name: nn.Bridge}
		return def, nil
	case "nat", "route":
		def.Forward = &libvirtxml.NetworkForward{Mode: nn.Type, Dev: nn.Dev}
	case "isolated":
		if nn.Dev != "" {
			return def, errors.New("an isolated network forwards to no interface")
		}
	default:
		return def, fmt.Errorf("network type %q: nat, route, isolated or bridge", nn.Type)
	}
	if nn.Bridge != "" {
		def.Bridge = &libvirtxml.NetworkBridge{Name: nn.Bridge, STP: "on", Delay: "0"}
	}

	var addr netip.Addr
	var pfx netip.Prefix
	if nn.Address == "" {
		for n := 100; n < 255 && !addr.IsValid(); n++ {
			p := netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 168, byte(n), 0}), 24)
			if !slices.ContainsFunc(used, p.Overlaps) {
				addr, pfx = netip.AddrFrom4([4]byte{192, 168, byte(n), 1}), p
			}
		}
		if !addr.IsValid() {
			return def, errors.New("no 192.168.N.0/24 is free for the network: give one with --address")
		}
	} else {
		p, err := netip.ParsePrefix(nn.Address)
		if err != nil || !p.Addr().Is4() || p.Bits() < 8 || p.Bits() > 30 {
			return def, fmt.Errorf("address %q: the host's IPv4 address on the network with its prefix, such as 192.168.100.1/24", nn.Address)
		}
		addr, pfx = p.Addr(), p.Masked()
		if addr == pfx.Addr() || addr == broadcast(pfx) {
			return def, fmt.Errorf("address %s is the subnet's own or its broadcast address: give the host's address in it, such as %s/%d", addr, pfx.Addr().Next(), pfx.Bits())
		}
		if i := slices.IndexFunc(used, pfx.Overlaps); i >= 0 {
			return def, fmt.Errorf("%s overlaps %s, which another network has", pfx, used[i])
		}
	}
	ip := libvirtxml.NetworkIP{Address: addr.String(), Prefix: uint(pfx.Bits())}
	if !nn.NoDHCP {
		start, end := addr.Next(), broadcast(pfx).Prev()
		if nn.DHCP != "" {
			s, e, ok := strings.Cut(nn.DHCP, "-")
			var err1, err2 error
			start, err1 = netip.ParseAddr(strings.TrimSpace(s))
			end, err2 = netip.ParseAddr(strings.TrimSpace(e))
			if !ok || err1 != nil || err2 != nil {
				return def, fmt.Errorf("DHCP range %q: <first>-<last>, such as 192.168.100.10-192.168.100.99", nn.DHCP)
			}
			if !pfx.Contains(start) || !pfx.Contains(end) || start == pfx.Addr() || end == broadcast(pfx) {
				return def, fmt.Errorf("DHCP range %s: both ends within %s, neither its own nor its broadcast address", nn.DHCP, pfx)
			}
			if start.Compare(addr) <= 0 && end.Compare(addr) >= 0 {
				return def, fmt.Errorf("DHCP range %s holds the host's own address %s", nn.DHCP, addr)
			}
		}
		if end.Compare(start) < 0 {
			return def, fmt.Errorf("no addresses left for DHCP between %s and the end of %s: give a range, or turn DHCP off", addr, pfx)
		}
		ip.DHCP = &libvirtxml.NetworkDHCP{Ranges: []libvirtxml.NetworkDHCPRange{{Start: start.String(), End: end.String()}}}
	} else if nn.DHCP != "" {
		return def, errors.New("a DHCP range with DHCP off")
	}
	def.IPs = []libvirtxml.NetworkIP{ip}
	return def, nil
}

func broadcast(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	n := binary.BigEndian.Uint32(a[:]) | (1<<(32-p.Bits()) - 1)
	binary.BigEndian.PutUint32(a[:], n)
	return netip.AddrFrom4(a)
}

// ipv4 is a network's first IPv4 address as a CIDR, with its DHCP range.
func ipv4(def libvirtxml.Network) (netip.Prefix, string) {
	for _, ip := range def.IPs {
		if ip.Family != "" && ip.Family != "ipv4" {
			continue
		}
		a, err := netip.ParseAddr(ip.Address)
		if err != nil || !a.Is4() {
			continue
		}
		bits := int(ip.Prefix)
		if ip.Netmask != "" {
			if m, err := netip.ParseAddr(ip.Netmask); err == nil && m.Is4() {
				b := m.As4()
				bits = 0
				for v := binary.BigEndian.Uint32(b[:]); v&(1<<31) != 0; v <<= 1 {
					bits++
				}
			}
		}
		if bits == 0 {
			bits = 24 // libvirt's default for a class C address without either
		}
		var dhcp string
		if ip.DHCP != nil && len(ip.DHCP.Ranges) > 0 {
			dhcp = ip.DHCP.Ranges[0].Start + "-" + ip.DHCP.Ranges[0].End
		}
		return netip.PrefixFrom(a, bits), dhcp
	}
	return netip.Prefix{}, ""
}

// usedSubnets are the IPv4 subnets the defined networks take.
func usedSubnets(lv *libvirt.Libvirt) ([]netip.Prefix, error) {
	nets, _, err := lv.ConnectListAllNetworks(1, 0)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Prefix, len(nets))
	err = each(len(nets), func(i int) error {
		x, err := lv.NetworkGetXMLDesc(nets[i], 0)
		if err != nil {
			return nil // gone meanwhile
		}
		var def libvirtxml.Network
		if def.Unmarshal(x) == nil {
			if p, _ := ipv4(def); p.IsValid() {
				out[i] = p.Masked()
			}
		}
		return nil
	})
	return slices.DeleteFunc(out, func(p netip.Prefix) bool { return !p.IsValid() }), err
}

// AddNetwork defines a virtual network and starts it; one that doesn't
// start (its subnet or bridge is taken on the host, say) is not kept.
func AddNetwork(ctx context.Context, sess *conn.Session, nn NewNetwork) (Network, error) {
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		if _, err := lv.NetworkLookupByName(nn.Name); err == nil {
			return fmt.Errorf("a network named %q already exists", nn.Name)
		}
		used, err := usedSubnets(lv)
		if err != nil {
			return err
		}
		def, err := networkDef(nn, used)
		if err != nil {
			return err
		}
		x, err := def.Marshal()
		if err != nil {
			return err
		}
		n, err := lv.NetworkDefineXML(x)
		if err != nil {
			return fmt.Errorf("libvirt refused the network: %w", err)
		}
		if err := lv.NetworkCreate(n); err != nil {
			cause := fmt.Errorf("starting the network: %w", err)
			if uerr := lv.NetworkUndefine(n); uerr != nil {
				return fmt.Errorf("%w (and removing its definition failed: %v)", cause, uerr)
			}
			return cause
		}
		if nn.Autostart {
			if err := lv.NetworkSetAutostart(n, 1); err != nil {
				return fmt.Errorf("autostart: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Network{}, err
	}
	return networkNamed(ctx, sess, nn.Name)
}

func networkNamed(ctx context.Context, sess *conn.Session, name string) (Network, error) {
	ns, err := Networks(ctx, sess)
	if err != nil {
		return Network{}, err
	}
	for _, n := range ns {
		if n.Name == name {
			return n, nil
		}
	}
	return Network{}, fmt.Errorf("no network %s", name)
}

// NetworkAction is what SetNetwork does to a network.
type NetworkAction string

const (
	NetworkStart        NetworkAction = "start"
	NetworkStop         NetworkAction = "stop"
	NetworkAutostartOn  NetworkAction = "autostart-on"
	NetworkAutostartOff NetworkAction = "autostart-off"
)

// networkUsers names the running machines with an interface on n, from
// the network's ports: one each, whatever the network's mode (a machine on
// a bridge network shows type='bridge' in its live definition).
func networkUsers(lv *libvirt.Libvirt, n libvirt.Network) ([]string, error) {
	ports, _, err := lv.NetworkListAllPorts(n, 1, 0)
	if err != nil {
		return nil, err
	}
	users := make([]string, len(ports))
	err = each(len(ports), func(i int) error {
		x, err := lv.NetworkPortGetXMLDesc(ports[i], 0)
		if err != nil {
			return nil // gone meanwhile
		}
		var p libvirtxml.NetworkPort
		if p.Unmarshal(x) == nil && p.Owner != nil {
			users[i] = p.Owner.Name
		}
		return nil
	})
	users = slices.DeleteFunc(users, func(u string) bool { return u == "" })
	slices.Sort(users)
	return slices.Compact(users), err
}

// ErrNetworkInUse is returned when stopping a network running machines
// are on, without force.
var ErrNetworkInUse = errors.New("network in use")

// stopNetwork stops an active network. Running machines on it keep their
// interfaces, cut off until they are restarted, so it is refused while
// there are any unless force.
func stopNetwork(lv *libvirt.Libvirt, n libvirt.Network, force bool) error {
	if active, err := lv.NetworkIsActive(n); err != nil || active == 0 {
		return err
	}
	if !force {
		users, err := networkUsers(lv, n)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("%w: %s running on %s would lose its network until restarted (--force to stop it anyway)",
				ErrNetworkInUse, strings.Join(users, ", "), n.Name)
		}
	}
	return lv.NetworkDestroy(n)
}

// SetNetwork starts or stops a network, or turns its autostart on or off.
func SetNetwork(ctx context.Context, sess *conn.Session, name string, a NetworkAction, force bool) (Network, error) {
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		n, err := lv.NetworkLookupByName(name)
		if err != nil {
			return fmt.Errorf("no network %s", name)
		}
		switch a {
		case NetworkStart:
			if active, err := lv.NetworkIsActive(n); err != nil || active != 0 {
				return err
			}
			return lv.NetworkCreate(n)
		case NetworkStop:
			return stopNetwork(lv, n, force)
		case NetworkAutostartOn:
			return lv.NetworkSetAutostart(n, 1)
		case NetworkAutostartOff:
			return lv.NetworkSetAutostart(n, 0)
		}
		return fmt.Errorf("network action %q", a)
	})
	if err != nil {
		return Network{}, err
	}
	return networkNamed(ctx, sess, name)
}

// RemoveNetwork stops a network and forgets it. Machines defined with an
// interface on it won't start until it is back, or the interface changed.
func RemoveNetwork(ctx context.Context, sess *conn.Session, name string, force bool) error {
	return sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		n, err := lv.NetworkLookupByName(name)
		if err != nil {
			return fmt.Errorf("no network %s", name)
		}
		if err := stopNetwork(lv, n, force); err != nil {
			return err
		}
		if persistent, err := lv.NetworkIsPersistent(n); err == nil && persistent == 0 {
			return nil // transient: stopping it was the end of it
		}
		return lv.NetworkUndefine(n)
	})
}

// Lease is an address a network's DHCP server handed out.
type Lease struct {
	MAC      string     `json:"mac"`
	IP       string     `json:"ip"` // with its prefix, 192.168.100.17/24
	Type     string     `json:"type"`
	Hostname string     `json:"hostname,omitempty"`
	ClientID string     `json:"client_id,omitempty"`
	Expires  *time.Time `json:"expires,omitempty"` // nil: never
	Machine  string     `json:"machine,omitempty"` // the machine with that MAC, if any
}

// Leases lists the DHCP leases of an active network, with the machine each
// went to.
func Leases(ctx context.Context, sess *conn.Session, name string) ([]Lease, error) {
	out := []Lease{}
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		n, err := lv.NetworkLookupByName(name)
		if err != nil {
			return fmt.Errorf("no network %s", name)
		}
		if active, err := lv.NetworkIsActive(n); err != nil {
			return err
		} else if active == 0 {
			return fmt.Errorf("network %s is stopped: it has no leases", name)
		}
		ls, _, err := lv.NetworkGetDhcpLeases(n, nil, 1, 0)
		if err != nil {
			return err
		}
		if len(ls) == 0 {
			return nil
		}
		macs, err := machineMACs(lv)
		if err != nil {
			return err
		}
		for _, l := range ls {
			le := Lease{IP: fmt.Sprintf("%s/%d", l.Ipaddr, l.Prefix), Type: "ipv4"}
			if l.Type == int32(libvirt.IPAddrTypeIpv6) {
				le.Type = "ipv6"
			}
			if len(l.Mac) > 0 {
				le.MAC = strings.ToLower(l.Mac[0])
				le.Machine = macs[le.MAC]
			}
			if len(l.Hostname) > 0 {
				le.Hostname = l.Hostname[0]
			}
			if len(l.Clientid) > 0 {
				le.ClientID = l.Clientid[0]
			}
			if l.Expirytime > 0 {
				t := time.Unix(l.Expirytime, 0).UTC()
				le.Expires = &t
			}
			out = append(out, le)
		}
		return nil
	})
	slices.SortFunc(out, func(a, b Lease) int { return strings.Compare(a.IP, b.IP) })
	return out, err
}

// machineMACs maps every machine's interface MACs to its name.
func machineMACs(lv *libvirt.Libvirt) (map[string]string, error) {
	doms, _, err := lv.ConnectListAllDomains(1, 0)
	if err != nil {
		return nil, err
	}
	per := make([][]string, len(doms))
	err = each(len(doms), func(i int) error {
		x, err := lv.DomainGetXMLDesc(doms[i], 0)
		if err != nil {
			return nil // gone meanwhile
		}
		var d libvirtxml.Domain
		if d.Unmarshal(x) != nil || d.Devices == nil {
			return nil
		}
		for _, nic := range d.Devices.Interfaces {
			if nic.MAC != nil {
				per[i] = append(per[i], strings.ToLower(nic.MAC.Address))
			}
		}
		return nil
	})
	m := map[string]string{}
	for i, macs := range per {
		for _, mac := range macs {
			m[mac] = doms[i].Name
		}
	}
	return m, err
}
