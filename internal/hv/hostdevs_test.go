package hv

import (
	"regexp"
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestHostDevice(t *testing.T) {
	var pci, usb libvirtxml.NodeDevice
	if err := pci.Unmarshal(`<device><name>pci_0000_00_02_0</name><driver><name>xe</name></driver>
<capability type='pci'><class>0x030000</class><domain>0</domain><bus>0</bus><slot>2</slot><function>0</function>
<product id='0x64a0'/><vendor id='0x8086'>Intel Corporation</vendor>
<iommuGroup number='3'><address domain='0x0000' bus='0x00' slot='0x02' function='0x0'/></iommuGroup></capability></device>`); err != nil {
		t.Fatal(err)
	}
	if err := usb.Unmarshal(`<device><name>usb_3_2</name><driver><name>usb</name></driver>
<capability type='usb_device'><bus>3</bus><device>2</device><product id='0x5411'>USB Hub</product><vendor id='0x0bda'/></capability></device>`); err != nil {
		t.Fatal(err)
	}
	p, u := hostDevice(pci), hostDevice(usb)
	if p.Kind != "pci" || p.Address != "0000:00:02.0" || p.VendorID != "8086" || p.Vendor != "Intel Corporation" ||
		p.ProductID != "64a0" || p.Driver != "xe" || p.IOMMUGroup == nil || *p.IOMMUGroup != 3 {
		t.Errorf("pci: %+v", p)
	}
	if u.Kind != "usb" || u.Address != "003:002" || u.VendorID != "0bda" || u.ProductID != "5411" || u.Product != "USB Hub" || u.IOMMUGroup != nil {
		t.Errorf("usb: %+v", u)
	}

	var d libvirtxml.Domain
	if err := d.Unmarshal(`<domain type='kvm'><name>m</name><devices>
<hostdev mode='subsystem' type='pci' managed='yes'><source><address domain='0x0000' bus='0x00' slot='0x02' function='0x0'/></source></hostdev>
<hostdev mode='subsystem' type='usb'><source><vendor id='0x0bda'/><product id='0x5411'/></source></hostdev>
</devices></domain>`); err != nil {
		t.Fatal(err)
	}
	users := hostdevsOf(d)
	if len(users) != 2 || !users[0].uses(p) || users[0].uses(u) || !users[1].uses(u) || users[1].uses(p) {
		t.Errorf("users: %+v", users)
	}
	other := u
	other.Address = "001:004"
	other.ProductID = "0001"
	if users[1].uses(other) {
		t.Error("a USB device of another product matched")
	}
}

// The <hostdev> for a device of the host, and reading it back from a
// definition as DetachHostDevice finds it.
func TestHostdevElement(t *testing.T) {
	pci := HostDevice{Name: "pci_0000_01_00_0", Kind: "pci", Address: "0000:01:00.0", VendorID: "10de", ProductID: "1b80"}
	mouse := HostDevice{Name: "usb_1_4", Kind: "usb", Address: "001:004", VendorID: "046d", ProductID: "c52b"}
	twin := HostDevice{Name: "usb_2_3", Kind: "usb", Address: "002:003", VendorID: "0781", ProductID: "5583"}
	twin2 := HostDevice{Name: "usb_2_5", Kind: "usb", Address: "002:005", VendorID: "0781", ProductID: "5583"}
	all := []HostDevice{pci, mouse, twin, twin2}
	want := map[string]string{
		pci.Name:   `<hostdev mode="subsystem" type="pci" managed="yes"><source><address domain="0x0000" bus="0x01" slot="0x00" function="0x0"></address></source></hostdev>`,
		mouse.Name: `<hostdev mode="subsystem" type="usb" managed="yes"><source><product id="0xc52b"></product><vendor id="0x046d"></vendor></source></hostdev>`,
		twin.Name:  `<hostdev mode="subsystem" type="usb" managed="yes"><source><address bus="2" device="3"></address></source></hostdev>`,
	}
	var dom libvirtxml.Domain
	dom.Devices = &libvirtxml.DomainDeviceList{}
	for _, d := range []HostDevice{pci, mouse, twin} {
		x, err := hostdevElement(d, all)
		if x = regexp.MustCompile(`>\s+<`).ReplaceAllString(x, "><"); err != nil || x != want[d.Name] {
			t.Errorf("%s: %s, %v", d.Name, x, err)
			continue
		}
		var h libvirtxml.DomainHostdev
		if err := h.Unmarshal(x); err != nil {
			t.Fatal(err)
		}
		dom.Devices.Hostdevs = append(dom.Devices.Hostdevs, h)
	}
	got := attachedHostDevices(&dom)
	if len(got) != 3 || got[0].Ref != "0000:01:00.0" || got[1].Ref != "046d:c52b" || got[1].Address != "" || got[2].Ref != "002:003" {
		t.Fatalf("attached: %+v", got)
	}
	for ref, i := range map[string]int{"0000:01:00.0": 0, "pci_0000_01_00_0": 0, "046d:c52b": 1, "002:003": 2} {
		if !hostdevRefers(got[i], ref) {
			t.Errorf("%s doesn't name %+v", ref, got[i])
		}
	}
	if hostdevRefers(got[0], "0000:01:00.1") || hostdevRefers(got[1], "usb_1_4") {
		t.Error("named the wrong device")
	}
}
