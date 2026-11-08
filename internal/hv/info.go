package hv

import (
	"context"
	"fmt"
	"net"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Info is one machine in detail, as virt-mm info and the details page show
// it: its state now, and its definition (what it boots with next).
type Info struct {
	Domain
	Title       string  `json:"title,omitempty"`
	Description string  `json:"description,omitempty"`
	VirtType    string  `json:"virt_type"` // kvm, qemu, test
	OSType      string  `json:"os_type"`   // hvm, linux (the test driver) …
	Arch        string  `json:"arch"`
	Machine     string  `json:"machine"` // pc-q35-10.0, …
	Firmware    string  `json:"firmware"`
	CPU         string  `json:"cpu"` // the CPU model or mode
	Config      Config  `json:"config"`
	Devices     Devices `json:"devices"`

	// The guest's addresses, while it runs: from the guest agent when it
	// answers, else from the DHCP leases of libvirt's networks, else from
	// the host's ARP table. AddressSource says which; AddressNote, when
	// there are none, why.
	Addresses     []Address `json:"addresses"`
	AddressSource string    `json:"address_source,omitempty"` // agent, lease, arp
	AddressNote   string    `json:"address_note,omitempty"`

	// Guest is what the guest agent says, while the machine runs and the
	// agent answers; GuestNote, when it doesn't, why.
	Guest     *GuestInfo `json:"guest,omitempty"`
	GuestNote string     `json:"guest_note,omitempty"`
}

// Config is what a machine's definition gives it from its next start: a
// running machine's Domain has what it has now, which a change that waits
// for the next start makes differ.
type Config struct {
	VCPUs int `json:"vcpus"`
	// The CPU topology; 0 when the definition has none (one socket per
	// vCPU).
	Sockets   int    `json:"sockets,omitempty"`
	Cores     int    `json:"cores,omitempty"`
	Threads   int    `json:"threads,omitempty"`
	MemoryKiB uint64 `json:"memory_kib"`
	MaxMemKiB uint64 `json:"max_memory_kib"`
}

// configOf reads Config from a definition.
func configOf(d *libvirtxml.Domain) Config {
	var c Config
	if d.VCPU != nil {
		c.VCPUs = int(d.VCPU.Value)
		if d.VCPU.Current != 0 {
			c.VCPUs = int(d.VCPU.Current)
		}
	}
	if d.CPU != nil && d.CPU.Topology != nil {
		t := d.CPU.Topology
		c.Sockets, c.Cores, c.Threads = max(t.Sockets, 1), max(t.Cores, 1), max(t.Threads, 1)
	}
	if d.Memory != nil {
		c.MaxMemKiB, _ = kibOf(d.Memory.Value, d.Memory.Unit)
		c.MemoryKiB = c.MaxMemKiB
	}
	if d.CurrentMemory != nil {
		c.MemoryKiB, _ = kibOf(d.CurrentMemory.Value, d.CurrentMemory.Unit)
	}
	return c
}

// Address is one of the guest's IP addresses.
type Address struct {
	Interface string `json:"interface"` // the guest's name for it (agent), else the host's
	MAC       string `json:"mac"`
	Type      string `json:"type"` // ipv4, ipv6
	Addr      string `json:"addr"`
	Prefix    uint32 `json:"prefix"`
}

// GetInfo reads a machine's state, definition, devices and, when it runs,
// its guest's addresses.
func GetInfo(ctx context.Context, sess *conn.Session, uuid string) (Info, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return Info{}, err
	}
	var out Info
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		cur, err := definition(lv, dom)
		if err != nil {
			return err
		}
		var d libvirtxml.Domain
		if err := d.Unmarshal(cur.XML); err != nil {
			return err
		}
		state, maxMem, mem, vcpus, _, err := lv.DomainGetInfo(dom)
		if err != nil {
			return err
		}
		out = Info{
			Domain: Domain{UUID: cur.UUID, Name: cur.Name, ID: dom.ID, State: stateOf(libvirt.DomainState(state)),
				VCPUs: int(vcpus), MemoryKiB: mem, MaxMemKiB: maxMem},
			Title: d.Title, Description: d.Description, VirtType: d.Type,
			Firmware: firmwareOf(&d), CPU: cpuModelText(d.CPU), Config: configOf(&d), Addresses: []Address{},
		}
		if !out.State.Active() {
			out.ID = -1
		}
		if p, err := lv.DomainIsPersistent(dom); err == nil {
			out.Persistent = p == 1
		}
		if a, err := lv.DomainGetAutostart(dom); err == nil {
			out.Autostart = a == 1
		}
		if !out.State.Active() {
			if h, err := lv.DomainHasManagedSaveImage(dom, 0); err == nil {
				out.Saved = h == 1
			}
		}
		if d.OS != nil && d.OS.Type != nil {
			out.OSType, out.Arch, out.Machine = d.OS.Type.Type, d.OS.Type.Arch, d.OS.Type.Machine
		}
		if out.Devices, err = readDevices(lv, dom, cur, &d); err != nil {
			return err
		}
		if out.State.Active() {
			out.Addresses, out.AddressSource, out.AddressNote = addresses(lv, dom)
			if out.Guest, err = guestInfo(lv, dom); err != nil {
				out.GuestNote = err.Error()
			}
		}
		return nil
	})
	return out, err
}

// addresses asks for the guest's addresses from each source in turn and
// keeps the first that has any. The guest's loopback is left out.
func addresses(lv *libvirt.Libvirt, dom libvirt.Domain) ([]Address, string, string) {
	var notes []string
	for _, src := range []struct {
		name string
		src  libvirt.DomainInterfaceAddressesSource
	}{
		{"agent", libvirt.DomainInterfaceAddressesSrcAgent},
		{"lease", libvirt.DomainInterfaceAddressesSrcLease},
		{"arp", libvirt.DomainInterfaceAddressesSrcArp},
	} {
		ifaces, err := lv.DomainInterfaceAddresses(dom, uint32(src.src), 0)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: %v", src.name, err))
			continue
		}
		var out []Address
		for _, i := range ifaces {
			mac := ""
			if len(i.Hwaddr) > 0 {
				mac = i.Hwaddr[0]
			}
			for _, a := range i.Addrs {
				if ip := net.ParseIP(a.Addr); ip == nil || ip.IsLoopback() {
					continue
				}
				t := "ipv4"
				if libvirt.IPAddrType(a.Type) == libvirt.IPAddrTypeIpv6 {
					t = "ipv6"
				}
				out = append(out, Address{Interface: i.Name, MAC: mac, Type: t, Addr: a.Addr, Prefix: a.Prefix})
			}
		}
		if len(out) > 0 {
			return out, src.name, ""
		}
		notes = append(notes, src.name+": none")
	}
	return []Address{}, "", strings.Join(notes, "; ")
}
