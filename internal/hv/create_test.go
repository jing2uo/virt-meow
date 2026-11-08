package hv

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

// The creation form sends exactly these fields (internal/ui/assets/app.js);
// a renamed json tag on either side fails here instead of in the GUI.
func TestSpecJSONFromPage(t *testing.T) {
	page := `{"name":"web01","os":"linux","vcpus":2,"memory_mib":4096,
		"disk_pool":"datapool","disk_gib":40,"import_pool":"","import_volume":"",
		"iso_pool":"default","iso_volume":"debian.iso","net_kind":"bridge","net_source":"br0",
		"firmware":"uefi-secure","tpm":true,"start":true,
		"image_pool":"","image_volume":"","disk_bus":"scsi","nic_model":"e1000e",
		"os_variant":"http://debian.org/debian/12","cloud_init":null}`
	dec := json.NewDecoder(strings.NewReader(page))
	dec.DisallowUnknownFields()
	var s Spec
	if err := dec.Decode(&s); err != nil {
		t.Fatal(err)
	}
	want := Spec{Name: "web01", OS: "linux", VCPUs: 2, MemoryMiB: 4096, DiskPool: "datapool", DiskGiB: 40,
		ISOPool: "default", ISOVolume: "debian.iso", NetKind: "bridge", NetSource: "br0",
		Firmware: "uefi-secure", TPM: true, Start: true, DiskBus: "scsi", NICModel: "e1000e", OSVariant: "http://debian.org/debian/12"}
	if s != want {
		t.Fatalf("decoded %+v", s)
	}
}

var kvmHost = Host{Arch: "x86_64", CPUs: 4, MemoryKiB: 16 << 20, VirtType: "kvm",
	Machines: []string{"pc", "q35"}, MaxVCPUs: 255, UEFI: true, SecureBoot: true, TPM: true}

func TestDomainXMLWindows(t *testing.T) {
	s := Spec{Name: "win11", OS: "windows", VCPUs: 4, MemoryMiB: 8192, DiskPool: "p", DiskGiB: 64,
		ISOPool: "p", ISOVolume: "win.iso", NetKind: "bridge", NetSource: "br0", Firmware: "uefi-secure", TPM: true}
	if err := s.Check(kvmHost); err != nil {
		t.Fatal(err)
	}
	x, err := s.DomainXML(kvmHost, "p", "win11.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	var d libvirtxml.Domain
	if err := d.Unmarshal(x); err != nil {
		t.Fatal(err)
	}
	dev := d.Devices
	switch {
	case d.Type != "kvm" || d.OS.Type.Machine != "q35" || d.OS.Firmware != "efi":
		t.Errorf("type %s machine %s firmware %s", d.Type, d.OS.Type.Machine, d.OS.Firmware)
	case d.Features.SMM == nil || d.Features.HyperV == nil || d.Clock.Offset != "localtime":
		t.Errorf("Windows needs SMM (Secure Boot), Hyper-V enlightenments and a local clock")
	case dev.Disks[0].Device != "cdrom" || dev.Disks[0].Boot.Order != 1 || dev.Disks[1].Target.Bus != "sata" || dev.Disks[1].Boot.Order != 2:
		t.Errorf("disks %+v", dev.Disks)
	case dev.Interfaces[0].Model.Type != "e1000e" || dev.Interfaces[0].Source.Bridge.Bridge != "br0":
		t.Errorf("NIC %+v", dev.Interfaces[0])
	case len(dev.TPMs) != 1 || dev.TPMs[0].Model != "tpm-crb":
		t.Errorf("TPM %+v", dev.TPMs)
	case len(dev.RNGs) != 0 || dev.MemBalloon.Model != "none":
		t.Errorf("no virtio devices without drivers: RNG %d, balloon %s", len(dev.RNGs), dev.MemBalloon.Model)
	}
	if fw := d.OS.FirmwareInfo; fw == nil || !slices.Contains(fw.Features, libvirtxml.DomainOSFirmwareFeature{Name: "secure-boot", Enabled: "yes"}) {
		t.Errorf("Secure Boot not asked for:\n%s", x)
	}
}

func TestDomainXMLLinuxBIOSNoISO(t *testing.T) {
	s := Spec{Name: "web01", OS: "linux", VCPUs: 2, MemoryMiB: 2048, DiskPool: "p", DiskGiB: 20, NetKind: "network", NetSource: "default", Firmware: "bios"}
	x, err := s.DomainXML(kvmHost, "p", "web01.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	var d libvirtxml.Domain
	if err := d.Unmarshal(x); err != nil {
		t.Fatal(err)
	}
	dev := d.Devices
	if d.OS.Firmware != "" || len(dev.Disks) != 1 || dev.Disks[0].Target.Bus != "virtio" || dev.Disks[0].Boot.Order != 1 {
		t.Errorf("firmware %q, disks %+v", d.OS.Firmware, dev.Disks)
	}
	if dev.Interfaces[0].Source.Network.Network != "default" || dev.Interfaces[0].Model.Type != "virtio" {
		t.Errorf("NIC %+v", dev.Interfaces[0])
	}
	if d.CPU == nil || d.CPU.Mode != "host-passthrough" {
		t.Errorf("KVM guests get the host's CPU: %+v", d.CPU)
	}
}

// An edited definition makes the new disk only while it still refers to it.
func TestUsesNewDisk(t *testing.T) {
	s := Spec{Name: "web01", OS: "linux", VCPUs: 1, MemoryMiB: 512, DiskPool: "p", DiskGiB: 8, NetKind: "none", Firmware: "bios"}
	x, err := s.DomainXML(kvmHost, "p", s.diskVolume())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.usesNewDisk(x); !ok || err != nil {
		t.Fatalf("generated definition: %v, %v", ok, err)
	}
	i, j := strings.Index(x, "<disk "), strings.Index(x, "</disk>")
	if ok, err := s.usesNewDisk(x[:i] + x[j+len("</disk>"):]); ok || err != nil {
		t.Fatalf("disk removed: %v, %v", ok, err)
	}
	if _, err := s.usesNewDisk("<network/>"); err == nil {
		t.Fatal("accepted a network as a domain")
	}
}

func TestParseIdentity(t *testing.T) {
	id, err := parseIdentity("<domain type='kvm'>\n <name> a </name><uuid>00000000-0000-0000-0000-000000000001</uuid></domain>")
	if err != nil || id.Name != "a" || id.UUID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("%+v, %v", id, err)
	}
	for _, bad := range []string{"", "<domain>", "<domain><uuid>x</uuid></domain>", "<pool><name>a</name></pool>"} {
		if _, err := parseIdentity(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSelfClose(t *testing.T) {
	in := `<a x="1"><acpi></acpi><b y="a&gt;b"></b><c>t</c><d></e></a>`
	want := `<a x="1"><acpi/><b y="a&gt;b"/><c>t</c><d></e></a>`
	if got := selfClose(in); got != want {
		t.Fatalf("got %s", got)
	}
}

// q35 machines get pcie-root at index 0 and spare root ports to hot-plug into.
func TestDomainXMLRootPorts(t *testing.T) {
	s := Spec{Name: "web01", OS: "linux", VCPUs: 1, MemoryMiB: 512, DiskPool: "p", DiskGiB: 8, NetKind: "none", Firmware: "bios"}
	x, err := s.DomainXML(kvmHost, "p", "web01.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	var d libvirtxml.Domain
	if err := d.Unmarshal(x); err != nil {
		t.Fatal(err)
	}
	ports, root := 0, false
	for _, c := range d.Devices.Controllers {
		switch {
		case c.Model == "pcie-root":
			root = c.Index != nil && *c.Index == 0
		case c.Model == "pcie-root-port":
			ports++
		}
	}
	if !root || ports != rootPorts {
		t.Errorf("pcie-root at 0: %v, %d root ports", root, ports)
	}
}

// The disk bus and NIC model asked for, over the OS's defaults; SCSI gets a
// virtio-scsi controller.
func TestDomainXMLBusAndModel(t *testing.T) {
	s := Spec{Name: "web01", OS: "linux", VCPUs: 1, MemoryMiB: 512, DiskPool: "p", DiskGiB: 8,
		ISOPool: "p", ISOVolume: "a.iso", NetKind: "network", NetSource: "default", Firmware: "bios",
		DiskBus: "scsi", NICModel: "rtl8139"}
	if err := s.Check(kvmHost); err != nil {
		t.Fatal(err)
	}
	x, err := s.DomainXML(kvmHost, "p", "web01.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	var d libvirtxml.Domain
	if err := d.Unmarshal(x); err != nil {
		t.Fatal(err)
	}
	dev := d.Devices
	if dev.Disks[1].Target.Bus != "scsi" || dev.Disks[1].Target.Dev != "sda" || dev.Disks[0].Target.Dev != "sdb" {
		t.Errorf("disks %+v %+v", dev.Disks[0].Target, dev.Disks[1].Target)
	}
	if dev.Interfaces[0].Model.Type != "rtl8139" {
		t.Errorf("NIC model %s", dev.Interfaces[0].Model.Type)
	}
	if !slices.ContainsFunc(dev.Controllers, func(c libvirtxml.DomainController) bool { return c.Type == "scsi" && c.Model == "virtio-scsi" }) {
		t.Errorf("no virtio-scsi controller:\n%s", x)
	}
	for _, bad := range []Spec{{DiskBus: "ide"}, {NICModel: "ne2k"}} {
		b := s
		b.DiskBus, b.NICModel = cmp.Or(bad.DiskBus, s.DiskBus), cmp.Or(bad.NICModel, s.NICModel)
		if b.Check(kvmHost) == nil {
			t.Errorf("accepted bus %q model %q", b.DiskBus, b.NICModel)
		}
	}
}

// A disk from an image needs the pool its copy goes into; a size of 0
// keeps the image's.
func TestCheckImage(t *testing.T) {
	s := Spec{Name: "c1", OS: "linux", VCPUs: 1, MemoryMiB: 512, NetKind: "none", Firmware: "bios",
		ImagePool: "img", ImageVolume: "debian.qcow2"}
	if s.Check(kvmHost) == nil {
		t.Error("an image without a pool for its copy")
	}
	s.DiskPool = "p"
	if err := s.Check(kvmHost); err != nil {
		t.Errorf("image, size 0: %v", err)
	}
	s.DiskGiB = 20
	if err := s.Check(kvmHost); err != nil {
		t.Errorf("image, 20 GiB: %v", err)
	}
	s.ImagePool = ""
	if s.Check(kvmHost) == nil {
		t.Error("an image without its pool")
	}
}

func TestWarnings(t *testing.T) {
	h := kvmHost
	h.FreeKiB = 2 << 20
	s := Spec{VCPUs: 2, MemoryMiB: 1024}
	if w := s.Warnings(h); len(w) != 0 {
		t.Errorf("within the host: %v", w)
	}
	s.VCPUs, s.MemoryMiB = 8, 4096
	if w := s.Warnings(h); len(w) != 2 || !strings.HasPrefix(w[0], "memory") || !strings.HasPrefix(w[1], "vCPUs") {
		t.Errorf("beyond free memory and CPUs: %v", w)
	}
}

// New machines share the clipboard with the console where the host has
// qemu-vdagent that doesn't block saving them, and don't elsewhere.
func TestDomainXMLClipboard(t *testing.T) {
	s := Spec{Name: "web01", OS: "linux", VCPUs: 1, MemoryMiB: 512, NetKind: "none", Firmware: "bios"}
	for _, has := range []bool{true, false} {
		h := kvmHost
		h.Clipboard = has
		x, err := s.DomainXML(h, "", "")
		if err != nil {
			t.Fatal(err)
		}
		var d libvirtxml.Domain
		if err := d.Unmarshal(x); err != nil {
			t.Fatal(err)
		}
		if hasClipboard(&d) != has {
			t.Errorf("host with clipboard %v: %s", has, x)
		}
	}
	for _, c := range []struct {
		lib, qemu uint64
		ok        bool
		moves     bool
	}{{11_003_000, 10_001_000, true, true}, {11_003_000, 10_000_013, true, false}, {8_004_000, 6_001_000, true, false},
		{8_003_000, 9_000_000, false, false}, {11_000_000, 6_000_000, false, false}} {
		if clipboardSupported(c.lib, c.qemu) != c.ok || clipboardMigrates(c.qemu) != c.moves {
			t.Errorf("libvirt %d, QEMU %d: supported %v, migrates %v", c.lib, c.qemu, !c.ok, clipboardMigrates(c.qemu))
		}
	}
}

// The user's defaults for new machines, in the definition: the console's
// address, the CPU mode, a raw disk, a sound card; on a host without KVM
// the CPU mode is warned about, not used.
func TestDomainXMLDefaults(t *testing.T) {
	s := Spec{Name: "web01", OS: "linux", VCPUs: 1, MemoryMiB: 512, DiskPool: "p", DiskGiB: 4, NetKind: "none", Firmware: "bios",
		Listen: "0.0.0.0", CPUMode: "host-model", DiskFormat: "raw", Sound: true}
	if err := s.Check(kvmHost); err != nil {
		t.Fatal(err)
	}
	x, err := s.DomainXML(kvmHost, "p", s.diskVolume())
	if err != nil {
		t.Fatal(err)
	}
	var d libvirtxml.Domain
	if err := d.Unmarshal(x); err != nil {
		t.Fatal(err)
	}
	dev := d.Devices
	if l := dev.Graphics[0].VNC.Listeners[0].Address.Address; l != "0.0.0.0" {
		t.Errorf("listen %s", l)
	}
	if d.CPU == nil || d.CPU.Mode != "host-model" {
		t.Errorf("CPU %+v", d.CPU)
	}
	if k := dev.Disks[0]; k.Driver.Type != "raw" || k.Source.Volume.Volume != "web01.img" {
		t.Errorf("disk %+v %+v", k.Driver, k.Source.Volume)
	}
	if len(dev.Sounds) != 1 || dev.Sounds[0].Model != "ich9" {
		t.Errorf("sound %+v", dev.Sounds)
	}
	if w := s.Warnings(kvmHost); len(w) != 1 || !strings.Contains(w[0], "0.0.0.0") {
		t.Errorf("warnings on KVM: %q", w)
	}
	tcg := kvmHost
	tcg.VirtType = "qemu"
	if w := s.Warnings(tcg); len(w) != 2 || !strings.Contains(w[1], "no KVM") {
		t.Errorf("warnings on TCG: %q", w)
	}
	for what, bad := range map[string]Spec{"CPU mode": {CPUMode: "custom"}, "disk format": {DiskFormat: "vmdk"}, "listen": {Listen: "everywhere"}} {
		b := s
		b.CPUMode, b.DiskFormat, b.Listen = cmpOr(bad.CPUMode, s.CPUMode), cmpOr(bad.DiskFormat, s.DiskFormat), cmpOr(bad.Listen, s.Listen)
		if err := b.Check(kvmHost); err == nil || !strings.Contains(err.Error(), what) {
			t.Errorf("%s: %v", what, err)
		}
	}
}
