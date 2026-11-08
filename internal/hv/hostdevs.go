package hv

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// HostDevice is a USB or PCI device of the host, one a machine can be
// given (virsh nodedev-list, nodedev-dumpxml).
type HostDevice struct {
	Name      string `json:"name"`    // libvirt's: pci_0000_00_02_0, usb_3_2
	Kind      string `json:"kind"`    // pci, usb
	Address   string `json:"address"` // PCI 0000:00:02.0; USB bus:device, 003:002
	VendorID  string `json:"vendor_id"`
	Vendor    string `json:"vendor,omitempty"` // names come from the host's udev database
	ProductID string `json:"product_id"`
	Product   string `json:"product,omitempty"`
	Class     string `json:"class,omitempty"`  // PCI class, 0x030000
	Driver    string `json:"driver,omitempty"` // the host driver bound to it now (vfio-pci while a machine has it)
	// IOMMUGroup is the PCI device's group, nil without an IOMMU: the
	// devices of a group go to a machine together.
	IOMMUGroup *int `json:"iommu_group,omitempty"`
	// UsedBy names the machines whose definition (or running
	// configuration) has the device.
	UsedBy []string `json:"used_by"`
}

// HostDevices lists the host's PCI and USB devices: one call for the list,
// then each device's details and each machine's definition, a few at a time.
func HostDevices(ctx context.Context, sess *conn.Session) ([]HostDevice, error) {
	var out []HostDevice
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		nds, _, err := lv.ConnectListAllNodeDevices(1,
			uint32(libvirt.ConnectListNodeDevicesCapPciDev|libvirt.ConnectListNodeDevicesCapUsbDev))
		if unsupported(err) {
			return errors.New("this host's libvirt lists no devices: its node device driver (virtnodedevd) isn't running")
		}
		if err != nil {
			return err
		}
		out = make([]HostDevice, len(nds))
		err = each(len(nds), func(i int) error {
			x, err := lv.NodeDeviceGetXMLDesc(nds[i].Name, 0)
			if err != nil {
				if isNotFound(err) { // gone since the list
					return nil
				}
				return fmt.Errorf("%s: %w", nds[i].Name, err)
			}
			var nd libvirtxml.NodeDevice
			if err := nd.Unmarshal(x); err != nil {
				return fmt.Errorf("%s: %w", nds[i].Name, err)
			}
			out[i] = hostDevice(nd)
			return nil
		})
		if err != nil {
			return err
		}
		out = slices.DeleteFunc(out, func(d HostDevice) bool { return d.Kind == "" })
		users, err := hostdevUsers(lv)
		if err != nil {
			return err
		}
		for i := range out {
			out[i].UsedBy = []string{}
			for _, u := range users {
				if u.uses(out[i]) && !slices.Contains(out[i].UsedBy, u.machine) {
					out[i].UsedBy = append(out[i].UsedBy, u.machine)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b HostDevice) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Address, b.Address))
	})
	return out, nil
}

// hostDevice reads a node device's XML; one that is neither PCI nor a USB
// device has no Kind.
func hostDevice(nd libvirtxml.NodeDevice) HostDevice {
	d := HostDevice{Name: nd.Name}
	if nd.Driver != nil {
		d.Driver = nd.Driver.Name
	}
	switch c := nd.Capability; {
	case c.PCI != nil:
		p := c.PCI
		d.Kind, d.Class = "pci", p.Class
		d.Address = fmt.Sprintf("%04x:%02x:%02x.%x", deref(p.Domain), deref(p.Bus), deref(p.Slot), deref(p.Function))
		d.VendorID, d.Vendor = hexID(p.Vendor.ID), strings.TrimSpace(p.Vendor.Name)
		d.ProductID, d.Product = hexID(p.Product.ID), strings.TrimSpace(p.Product.Name)
		if p.IOMMUGroup != nil {
			g := p.IOMMUGroup.Number
			d.IOMMUGroup = &g
		}
	case c.USBDevice != nil:
		u := c.USBDevice
		d.Kind = "usb"
		d.Address = fmt.Sprintf("%03d:%03d", u.Bus, u.Device)
		d.VendorID, d.Vendor = hexID(u.Vendor.ID), strings.TrimSpace(u.Vendor.Name)
		d.ProductID, d.Product = hexID(u.Product.ID), strings.TrimSpace(u.Product.Name)
	default:
		return HostDevice{}
	}
	return d
}

func deref(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}

// hexID writes a vendor or product ID as lsusb and lspci do: 4 hex digits.
func hexID(s string) string {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 0, 16)
	if err != nil {
		return strings.ToLower(strings.TrimPrefix(s, "0x"))
	}
	return fmt.Sprintf("%04x", v)
}

// hostdevUser is one host device a machine's definition names.
type hostdevUser struct {
	machine string
	pci     *libvirtxml.DomainAddressPCI
	usb     *libvirtxml.DomainHostdevSubsysUSBSource
}

// uses reports whether d is the device u names: a PCI address, or a USB
// device by its bus and device numbers or else its vendor and product.
func (u hostdevUser) uses(d HostDevice) bool {
	switch {
	case u.pci != nil && d.Kind == "pci":
		a := u.pci
		return fmt.Sprintf("%04x:%02x:%02x.%x", deref(a.Domain), deref(a.Bus), deref(a.Slot), deref(a.Function)) == d.Address
	case u.usb != nil && d.Kind == "usb":
		if a := u.usb.Address; a != nil && a.Bus != nil && a.Device != nil {
			return fmt.Sprintf("%03d:%03d", *a.Bus, *a.Device) == d.Address
		}
		return u.usb.Vendor != nil && u.usb.Product != nil &&
			hexID(u.usb.Vendor.ID) == d.VendorID && hexID(u.usb.Product.ID) == d.ProductID
	}
	return false
}

// hostdevUsers reads the host devices in every machine's definition and,
// for a running one, in what it runs with now.
func hostdevUsers(lv *libvirt.Libvirt) ([]hostdevUser, error) {
	doms, _, err := lv.ConnectListAllDomains(1, 0)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var users []hostdevUser
	err = each(len(doms), func(i int) error {
		var found []hostdevUser
		for _, flags := range []libvirt.DomainXMLFlags{0, libvirt.DomainXMLInactive} {
			x, err := lv.DomainGetXMLDesc(doms[i], flags)
			if err != nil {
				if isNotFound(err) {
					return nil
				}
				return fmt.Errorf("%s: %w", doms[i].Name, err)
			}
			var d libvirtxml.Domain
			if err := d.Unmarshal(x); err != nil {
				return fmt.Errorf("%s: %w", doms[i].Name, err)
			}
			found = append(found, hostdevsOf(d)...)
			if d.ID == nil { // not running: the two are the same
				break
			}
		}
		mu.Lock()
		users = append(users, found...)
		mu.Unlock()
		return nil
	})
	return users, err
}

func hostdevsOf(d libvirtxml.Domain) []hostdevUser {
	var out []hostdevUser
	if d.Devices == nil {
		return nil
	}
	for _, h := range d.Devices.Hostdevs {
		switch {
		case h.SubsysPCI != nil && h.SubsysPCI.Source != nil && h.SubsysPCI.Source.Address != nil:
			out = append(out, hostdevUser{machine: d.Name, pci: h.SubsysPCI.Source.Address})
		case h.SubsysUSB != nil && h.SubsysUSB.Source != nil:
			out = append(out, hostdevUser{machine: d.Name, usb: h.SubsysUSB.Source})
		}
	}
	for _, i := range d.Devices.Interfaces {
		if i.Source != nil && i.Source.Hostdev != nil && i.Source.Hostdev.PCI != nil && i.Source.Hostdev.PCI.Address != nil {
			out = append(out, hostdevUser{machine: d.Name, pci: i.Source.Hostdev.PCI.Address})
		}
	}
	return out
}

// isNotFound reports whether libvirt answered that the object is gone.
func isNotFound(err error) bool {
	var e libvirt.Error
	return errors.As(err, &e) && (e.Code == uint32(libvirt.ErrNoDomain) || e.Code == uint32(libvirt.ErrNoNodeDevice))
}

// HostDeviceXML is a host device's description as libvirt has it (virsh
// nodedev-dumpxml).
func HostDeviceXML(ctx context.Context, sess *conn.Session, name string) (string, error) {
	var x string
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		var err error
		x, err = lv.NodeDeviceGetXMLDesc(name, 0)
		if isNotFound(err) {
			return fmt.Errorf("the host has no device %q (virt-mm hostdevs lists them)", name)
		}
		return err
	})
	return x, err
}

// AttachedHostDevice is a host device in a machine's definition.
type AttachedHostDevice struct {
	Kind      string `json:"kind"`              // pci, usb
	Address   string `json:"address,omitempty"` // PCI 0000:00:02.0; USB bus:device, 003:002
	VendorID  string `json:"vendor_id,omitempty"`
	ProductID string `json:"product_id,omitempty"` // a USB device named by vendor and product
	// Ref is what DetachHostDevice takes for it: its address, or for a
	// USB device named by its IDs, vendor:product.
	Ref string `json:"ref"`
}

// attachedHostDevices are the <hostdev> devices of a definition.
func attachedHostDevices(d *libvirtxml.Domain) []AttachedHostDevice {
	out := []AttachedHostDevice{}
	if d.Devices == nil {
		return out
	}
	for _, u := range hostdevsOf(*d) {
		var a AttachedHostDevice
		switch {
		case u.pci != nil:
			a.Kind = "pci"
			a.Address = fmt.Sprintf("%04x:%02x:%02x.%x", deref(u.pci.Domain), deref(u.pci.Bus), deref(u.pci.Slot), deref(u.pci.Function))
			a.Ref = a.Address
		case u.usb != nil:
			a.Kind = "usb"
			if ad := u.usb.Address; ad != nil && ad.Bus != nil && ad.Device != nil {
				a.Address = fmt.Sprintf("%03d:%03d", *ad.Bus, *ad.Device)
				a.Ref = a.Address
			}
			if u.usb.Vendor != nil && u.usb.Product != nil {
				a.VendorID, a.ProductID = hexID(u.usb.Vendor.ID), hexID(u.usb.Product.ID)
				if a.Ref == "" {
					a.Ref = a.VendorID + ":" + a.ProductID
				}
			}
		}
		out = append(out, a)
	}
	return out
}

// hostdevElement is the <hostdev> giving a machine the host device d,
// managed: libvirt takes it from its host driver at start and gives it
// back after. A USB device goes by its vendor and product, which survive
// replugging, unless another device of the host has the same: then by its
// bus and device numbers.
func hostdevElement(d HostDevice, all []HostDevice) (string, error) {
	h := libvirtxml.DomainHostdev{Managed: "yes"}
	switch d.Kind {
	case "pci":
		var dom, bus, slot, fn uint
		if _, err := fmt.Sscanf(d.Address, "%x:%x:%x.%x", &dom, &bus, &slot, &fn); err != nil {
			return "", fmt.Errorf("PCI address %q: %w", d.Address, err)
		}
		h.SubsysPCI = &libvirtxml.DomainHostdevSubsysPCI{Source: &libvirtxml.DomainHostdevSubsysPCISource{
			Address: &libvirtxml.DomainAddressPCI{Domain: &dom, Bus: &bus, Slot: &slot, Function: &fn}}}
	case "usb":
		src := &libvirtxml.DomainHostdevSubsysUSBSource{}
		twin := slices.ContainsFunc(all, func(o HostDevice) bool {
			return o.Kind == "usb" && o.Name != d.Name && o.VendorID == d.VendorID && o.ProductID == d.ProductID
		})
		if twin || d.VendorID == "" {
			var bus, dev uint
			if _, err := fmt.Sscanf(d.Address, "%d:%d", &bus, &dev); err != nil {
				return "", fmt.Errorf("USB address %q: %w", d.Address, err)
			}
			src.Address = &libvirtxml.DomainAddressUSB{Bus: &bus, Device: &dev}
		} else {
			src.Vendor = &libvirtxml.DomainHostDevProductVendorID{ID: "0x" + d.VendorID}
			src.Product = &libvirtxml.DomainHostDevProductVendorID{ID: "0x" + d.ProductID}
		}
		h.SubsysUSB = &libvirtxml.DomainHostdevSubsysUSB{Source: src}
	default:
		return "", fmt.Errorf("%s is neither PCI nor USB", d.Name)
	}
	return h.Marshal()
}

// findHostDevice is the host device named ref: libvirt's name
// (pci_0000_00_02_0) or its address.
func findHostDevice(all []HostDevice, ref string) (HostDevice, error) {
	for _, d := range all {
		if d.Name == ref || d.Address == ref {
			return d, nil
		}
	}
	return HostDevice{}, fmt.Errorf("the host has no device %q (virt-mm hostdevs lists them)", ref)
}

// AttachHostDevice gives a machine a USB or PCI device of the host, by
// libvirt's name for it or its address: into its definition, and to the
// running machine when it can take it now. Warnings say which other
// machines have the device too (one running machine can have it at a
// time) and what else is in its IOMMU group.
func AttachHostDevice(ctx context.Context, sess *conn.Session, uuid, ref string) (Edited, error) {
	all, err := HostDevices(ctx, sess)
	if err != nil {
		return Edited{}, err
	}
	d, err := findHostDevice(all, ref)
	if err != nil {
		return Edited{}, err
	}
	x, err := hostdevElement(d, all)
	if err != nil {
		return Edited{}, err
	}
	what := fmt.Sprintf("host device %s %s (%s:%s)", d.Kind, d.Address, d.VendorID, d.ProductID)
	var warnings []string
	ed, err := modify(ctx, sess, uuid, func(e *edit) error {
		for _, u := range hostdevsOf(*e.def) {
			if u.uses(d) {
				return fmt.Errorf("%s already has %s", e.def.Name, what)
			}
		}
		if others := slices.DeleteFunc(slices.Clone(d.UsedBy), func(n string) bool { return n == e.def.Name }); len(others) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s is also given to %s: one running machine can have it at a time", d.Address, strings.Join(others, ", ")))
		}
		return e.attach(what, rawDevice(x))
	})
	if err != nil {
		return ed, err
	}
	if d.IOMMUGroup != nil {
		var group []string
		for _, o := range all {
			if o.Name != d.Name && o.IOMMUGroup != nil && *o.IOMMUGroup == *d.IOMMUGroup {
				group = append(group, o.Address)
			}
		}
		if len(group) > 0 {
			warnings = append(warnings, fmt.Sprintf("IOMMU group %d also has %s: the host can't keep them while the machine has %s", *d.IOMMUGroup, strings.Join(group, ", "), d.Address))
		}
	}
	ed.Warnings = append(ed.Warnings, warnings...)
	return ed, nil
}

// DetachHostDevice takes a host device from a machine: ref is its
// address, a USB device's vendor:product, or libvirt's name for it.
func DetachHostDevice(ctx context.Context, sess *conn.Session, uuid, ref string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		if e.def.Devices != nil {
			for _, h := range e.def.Devices.Hostdevs {
				dom := libvirtxml.Domain{Name: e.def.Name, Devices: &libvirtxml.DomainDeviceList{Hostdevs: []libvirtxml.DomainHostdev{h}}}
				as := attachedHostDevices(&dom)
				if len(as) == 0 || !hostdevRefers(as[0], ref) {
					continue
				}
				x, err := h.Marshal()
				if err != nil {
					return err
				}
				return e.detach(fmt.Sprintf("host device %s %s", as[0].Kind, as[0].Ref), rawDevice(x))
			}
		}
		return fmt.Errorf("%s has no host device %s", e.def.Name, ref)
	})
}

// hostdevRefers says whether ref names the attached device a: its
// address or IDs, or libvirt's name for a PCI device (pci_0000_00_02_0).
func hostdevRefers(a AttachedHostDevice, ref string) bool {
	if ref == a.Ref || ref == a.Address && a.Address != "" {
		return true
	}
	if a.VendorID != "" && ref == a.VendorID+":"+a.ProductID {
		return true
	}
	if a.Kind == "pci" {
		return ref == "pci_"+strings.NewReplacer(":", "_", ".", "_").Replace(a.Address)
	}
	return false
}
