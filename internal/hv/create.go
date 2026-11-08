package hv

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Spec is a new machine, as the creation form fills it in.
type Spec struct {
	Name string `json:"name"`
	OS   string `json:"os"` // linux, windows, other: picks devices the guest has drivers for
	// OSVariant is osinfo-db's ID of the system (http://debian.org/debian/12),
	// kept in the definition's metadata as virt-manager does.
	OSVariant string `json:"os_variant,omitempty"`
	VCPUs     int    `json:"vcpus"`
	MemoryMiB int    `json:"memory_mib"`

	// The system disk: a new one (DiskPool + DiskGiB) or an existing
	// volume (ImportPool + ImportVolume), which the machine boots from.
	// With ImagePool + ImageVolume (a cloud image, a disk image) the new
	// disk is a copy of that volume, grown to DiskGiB; 0 keeps its size.
	DiskPool     string `json:"disk_pool"`
	DiskGiB      int    `json:"disk_gib"`
	ImportPool   string `json:"import_pool"`
	ImportVolume string `json:"import_volume"`
	ImagePool    string `json:"image_pool,omitempty"`
	ImageVolume  string `json:"image_volume,omitempty"`
	DiskBus      string `json:"disk_bus,omitempty"` // virtio, sata, scsi; "" picks by OS

	// Installation media, optional: an ISO volume, booted first.
	ISOPool   string `json:"iso_pool"`
	ISOVolume string `json:"iso_volume"`

	// Network: "bridge" (Source is a host bridge), "network" (a libvirt
	// network) or "none".
	NetKind   string `json:"net_kind"`
	NetSource string `json:"net_source"`
	NICModel  string `json:"nic_model,omitempty"` // virtio, e1000e, e1000, rtl8139; "" picks by OS

	Firmware string `json:"firmware"` // bios, uefi, uefi-secure
	TPM      bool   `json:"tpm"`
	Start    bool   `json:"start"`

	// What the user's defaults for new machines may set (virt-meow's
	// settings); "" or false is virt-meow's own choice.
	Listen     string `json:"listen,omitempty"`      // the VNC console's address on the host; "" 127.0.0.1
	CPUMode    string `json:"cpu_mode,omitempty"`    // host-passthrough, host-model (KVM); "" host-passthrough
	DiskFormat string `json:"disk_format,omitempty"` // a new disk's: qcow2, raw; "" qcow2
	Sound      bool   `json:"sound,omitempty"`       // a sound card: ich9 on q35, ich6 otherwise

	// CloudInit, for a cloud image (or an existing disk with cloud-init):
	// a NoCloud seed ISO is made next to the disk and attached as a CD.
	CloudInit *CloudInit `json:"cloud_init,omitempty"`
}

// rootPorts is how many PCIe root ports a new q35 machine gets: one for each
// of its PCI devices (about six), the rest free for hot-plugging.
const rootPorts = 16

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// Check rejects a spec the host can't take, with a message for the form.
func (s Spec) Check(h Host) error {
	var errs []string
	if !nameRE.MatchString(s.Name) {
		errs = append(errs, "name: letters, digits, '.', '_' and '-', starting with a letter or digit")
	}
	switch s.OS {
	case "linux", "windows", "other":
	default:
		errs = append(errs, "os: linux, windows or other")
	}
	if s.VCPUs < 1 || (h.MaxVCPUs > 0 && s.VCPUs > h.MaxVCPUs) || (h.CPUs > 0 && s.VCPUs > 4*h.CPUs) {
		errs = append(errs, fmt.Sprintf("vCPUs: 1 to %d", min(max(h.MaxVCPUs, 1), max(4*h.CPUs, 1))))
	}
	if s.MemoryMiB < 64 || (h.MemoryKiB > 0 && uint64(s.MemoryMiB)<<10 > h.MemoryKiB) {
		errs = append(errs, "memory: at least 64 MiB, at most the host's")
	}
	newDisk, imported, image := s.DiskPool != "", s.ImportVolume != "", s.ImageVolume != ""
	switch {
	case newDisk && imported:
		errs = append(errs, "disk: a new one or an existing one, not both")
	case image && !newDisk:
		errs = append(errs, "image: which pool its copy goes into")
	case image && s.ImagePool == "":
		errs = append(errs, "image: which pool it is in")
	case image && (s.DiskGiB < 0 || s.DiskGiB > 64<<10):
		errs = append(errs, "disk size: up to 64 TiB, or 0 for the image's own")
	case newDisk && !image && (s.DiskGiB < 1 || s.DiskGiB > 64<<10):
		errs = append(errs, "disk size: 1 GiB to 64 TiB")
	case imported && s.ImportPool == "":
		errs = append(errs, "existing disk: which pool")
	case !newDisk && !imported && s.ISOVolume == "":
		errs = append(errs, "nothing to boot: add a disk or an ISO")
	}
	if s.ISOVolume != "" && s.ISOPool == "" {
		errs = append(errs, "ISO: which pool")
	}
	if s.CloudInit != nil {
		switch {
		case s.ISOVolume != "":
			errs = append(errs, "cloud-init: for a cloud image, not an installation ISO")
		case !newDisk && !imported:
			errs = append(errs, "cloud-init: needs a disk (a cloud image's copy, or an existing one)")
		}
		errs = append(errs, s.CloudInit.check()...)
	}
	switch s.NetKind {
	case "none":
	case "bridge", "network":
		if s.NetSource == "" {
			errs = append(errs, "network: which "+s.NetKind)
		}
	default:
		errs = append(errs, "network: bridge, network or none")
	}
	if v := s.OSVariant; v != "" && (!strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") || len(v) > 200 || strings.ContainsAny(v, " <>\"'\n")) {
		errs = append(errs, "OS variant: an osinfo-db ID (http://…)")
	}
	switch s.DiskBus {
	case "", "virtio", "sata", "scsi":
	default:
		errs = append(errs, "disk bus: virtio, sata or scsi")
	}
	switch s.NICModel {
	case "", "virtio", "e1000e", "e1000", "rtl8139":
	default:
		errs = append(errs, "NIC model: virtio, e1000e, e1000 or rtl8139")
	}
	switch s.CPUMode {
	case "", "host-passthrough", "host-model":
	default:
		errs = append(errs, "CPU mode: host-passthrough or host-model")
	}
	switch s.DiskFormat {
	case "", "qcow2", "raw":
	default:
		errs = append(errs, "disk format: qcow2 or raw")
	}
	if s.Listen != "" && net.ParseIP(s.Listen) == nil {
		errs = append(errs, "listen: an IP address of the host (127.0.0.1 keeps the console on it)")
	}
	switch s.Firmware {
	case "bios":
	case "uefi":
		if !h.UEFI && h.VirtType != "test" {
			errs = append(errs, "UEFI: the host has no UEFI firmware for this machine type")
		}
	case "uefi-secure":
		if !h.SecureBoot {
			errs = append(errs, "Secure Boot: the host has no Secure Boot firmware")
		}
	default:
		errs = append(errs, "firmware: bios, uefi or uefi-secure")
	}
	if s.TPM && !h.TPM {
		errs = append(errs, "TPM: the host can't emulate one (swtpm missing?)")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// Warnings are what the host takes but may regret: more memory than it
// has free, more vCPUs than it has CPUs. Check has already refused what it
// can't take at all.
func (s Spec) Warnings(h Host) []string {
	var w []string
	if h.FreeKiB > 0 && uint64(s.MemoryMiB)<<10 > h.FreeKiB {
		w = append(w, fmt.Sprintf("memory: %d MiB asked, the host has %d MiB free now; it may not start, or swap", s.MemoryMiB, h.FreeKiB>>10))
	}
	if h.CPUs > 0 && s.VCPUs > h.CPUs {
		w = append(w, fmt.Sprintf("vCPUs: %d asked, the host has %d CPUs; they share them, slower than as many real ones", s.VCPUs, h.CPUs))
	}
	if ip := net.ParseIP(s.Listen); ip != nil && !ip.IsLoopback() {
		w = append(w, fmt.Sprintf("VNC listens on %s: its console is then open on the network, unencrypted and without a password; 127.0.0.1 keeps it on the host, where virt-meow reaches it over SSH", s.Listen))
	}
	if s.CPUMode != "" && h.VirtType != "kvm" {
		w = append(w, fmt.Sprintf("CPU mode %s: the host has no KVM, so the machine gets QEMU's emulated CPU", s.CPUMode))
	}
	return w
}

// diskVolume is the name a new system disk gets in its pool.
func (s Spec) diskVolume() string {
	if s.diskFormat() == "raw" {
		return s.Name + ".img"
	}
	return s.Name + ".qcow2"
}

func (s Spec) diskFormat() string { return cmp.Or(s.DiskFormat, "qcow2") }

// seedSuffix ends the name of a cloud-init seed ISO virt-meow made: Delete
// takes such a CD's volume with the machine's disks.
const seedSuffix = "-cidata.iso"

// seedPool and seedVolume are where the cloud-init seed goes: beside the
// system disk.
func (s Spec) seedPool() string   { return cmp.Or(s.DiskPool, s.ImportPool) }
func (s Spec) seedVolume() string { return s.Name + seedSuffix }

// DomainXML builds the definition of the machine s describes on host h,
// with the system disk at diskPool/diskVol.
func (s Spec) DomainXML(h Host, diskPool, diskVol string) (string, error) {
	windows := s.OS == "windows"
	virtio := !windows // Windows has no virtio drivers until they are installed
	machine := ""
	if slices.Contains(h.Machines, "q35") {
		machine = "q35"
	}
	d := &libvirtxml.Domain{
		Type:          h.VirtType,
		Name:          s.Name,
		Memory:        &libvirtxml.DomainMemory{Value: uint(s.MemoryMiB), Unit: "MiB"},
		CurrentMemory: &libvirtxml.DomainCurrentMemory{Value: uint(s.MemoryMiB), Unit: "MiB"},
		VCPU:          &libvirtxml.DomainVCPU{Placement: "static", Value: uint(s.VCPUs)},
		OS: &libvirtxml.DomainOS{
			Type: &libvirtxml.DomainOSType{Arch: h.Arch, Machine: machine, Type: "hvm"},
		},
		Features: &libvirtxml.DomainFeatureList{
			ACPI: &libvirtxml.DomainFeature{},
			APIC: &libvirtxml.DomainFeatureAPIC{},
		},
		Clock:      &libvirtxml.DomainClock{Offset: "utc"},
		OnPoweroff: "destroy",
		OnReboot:   "restart",
		OnCrash:    "destroy",
		PM: &libvirtxml.DomainPM{
			SuspendToMem:  &libvirtxml.DomainPMPolicy{Enabled: "no"},
			SuspendToDisk: &libvirtxml.DomainPMPolicy{Enabled: "no"},
		},
		Devices: &libvirtxml.DomainDeviceList{},
	}
	switch {
	case h.VirtType != "kvm":
	case s.CPUMode == "host-model":
		d.CPU = &libvirtxml.DomainCPU{Mode: "host-model", Check: "partial"}
	default:
		d.CPU = &libvirtxml.DomainCPU{Mode: "host-passthrough", Check: "none", Migratable: "on"}
	}
	if s.OSVariant != "" {
		d.Metadata = &libvirtxml.DomainMetadata{XML: osinfoMetadata(s.OSVariant)}
	}
	if s.Firmware != "bios" {
		d.OS.Firmware = "efi"
		secure := "no"
		if s.Firmware == "uefi-secure" {
			secure = "yes"
			d.Features.SMM = &libvirtxml.DomainFeatureSMM{State: "on"}
		}
		d.OS.FirmwareInfo = &libvirtxml.DomainOSFirmwareInfo{Features: []libvirtxml.DomainOSFirmwareFeature{
			{Name: "secure-boot", Enabled: secure},
			{Name: "enrolled-keys", Enabled: secure},
		}}
	}
	if windows {
		d.Clock.Offset = "localtime"
		d.Clock.Timer = []libvirtxml.DomainTimer{
			{Name: "rtc", TickPolicy: "catchup"},
			{Name: "pit", TickPolicy: "delay"},
			{Name: "hpet", Present: "no"},
			{Name: "hypervclock", Present: "yes"},
		}
		on := &libvirtxml.DomainFeatureState{State: "on"}
		d.Features.HyperV = &libvirtxml.DomainFeatureHyperV{
			Relaxed: on, VAPIC: on, VPIndex: on, Runtime: on, Synic: on, Reset: on, Frequencies: on,
			Spinlocks: &libvirtxml.DomainFeatureHyperVSpinlocks{DomainFeatureState: *on, Retries: 8191},
			STimer:    &libvirtxml.DomainFeatureHyperVSTimer{DomainFeatureState: *on},
		}
	}

	dev := d.Devices
	boot := uint(1)
	diskBus := s.DiskBus
	if diskBus == "" {
		diskBus = "sata"
		if virtio {
			diskBus = "virtio"
		}
	}
	diskDev, cdDev := "vda", "sda"
	if diskBus != "virtio" {
		diskDev, cdDev = "sda", "sdb"
	}
	// Installation media boots first; once the guest is installed and the
	// disk has a boot loader, an ejected or empty drive falls through.
	if s.ISOVolume != "" {
		dev.Disks = append(dev.Disks, libvirtxml.DomainDisk{
			Device:   "cdrom",
			Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
			Source:   &libvirtxml.DomainDiskSource{Volume: &libvirtxml.DomainDiskSourceVolume{Pool: s.ISOPool, Volume: s.ISOVolume}},
			Target:   &libvirtxml.DomainDiskTarget{Dev: cdDev, Bus: "sata"},
			ReadOnly: &libvirtxml.DomainDiskReadOnly{},
			Boot:     &libvirtxml.DomainDeviceBoot{Order: boot},
		})
		boot++
	}
	if diskVol != "" {
		// A new disk is in the format asked; an existing one as before.
		diskFormat := s.diskFormat()
		if s.ImportVolume != "" {
			diskFormat = "qcow2"
		}
		dev.Disks = append(dev.Disks, libvirtxml.DomainDisk{
			Device: "disk",
			Driver: &libvirtxml.DomainDiskDriver{Name: "qemu", Type: diskFormat, Discard: "unmap"},
			Source: &libvirtxml.DomainDiskSource{Volume: &libvirtxml.DomainDiskSourceVolume{Pool: diskPool, Volume: diskVol}},
			Target: &libvirtxml.DomainDiskTarget{Dev: diskDev, Bus: diskBus},
			Boot:   &libvirtxml.DomainDeviceBoot{Order: boot},
		})
	}
	// The cloud-init seed: a CD that isn't booted from, read by cloud-init.
	if s.CloudInit != nil {
		dev.Disks = append(dev.Disks, libvirtxml.DomainDisk{
			Device:   "cdrom",
			Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
			Source:   &libvirtxml.DomainDiskSource{Volume: &libvirtxml.DomainDiskSourceVolume{Pool: s.seedPool(), Volume: s.seedVolume()}},
			Target:   &libvirtxml.DomainDiskTarget{Dev: cdDev, Bus: "sata"},
			ReadOnly: &libvirtxml.DomainDiskReadOnly{},
		})
	}

	nicModel := s.NICModel
	if nicModel == "" {
		nicModel = "e1000e"
		if virtio {
			nicModel = "virtio"
		}
	}
	switch s.NetKind {
	case "bridge":
		dev.Interfaces = append(dev.Interfaces, libvirtxml.DomainInterface{
			Source: &libvirtxml.DomainInterfaceSource{Bridge: &libvirtxml.DomainInterfaceSourceBridge{Bridge: s.NetSource}},
			Model:  &libvirtxml.DomainInterfaceModel{Type: nicModel},
		})
	case "network":
		dev.Interfaces = append(dev.Interfaces, libvirtxml.DomainInterface{
			Source: &libvirtxml.DomainInterfaceSource{Network: &libvirtxml.DomainInterfaceSourceNetwork{Network: s.NetSource}},
			Model:  &libvirtxml.DomainInterfaceModel{Type: nicModel},
		})
	}

	dev.Controllers = append(dev.Controllers,
		libvirtxml.DomainController{Type: "usb", Model: "qemu-xhci"},
		libvirtxml.DomainController{Type: "virtio-serial"},
	)
	if diskBus == "scsi" {
		// libvirt would add an LSI controller, which no current guest drives.
		dev.Controllers = append(dev.Controllers, libvirtxml.DomainController{Type: "scsi", Model: "virtio-scsi"})
	}
	// On q35 every PCI device sits behind a PCIe root port, and libvirt adds
	// only the ports the devices take: none would be left to hot-plug a disk
	// or NIC into a running machine. Like virt-manager, add spares.
	if machine == "q35" {
		// pcie-root first, at its index 0: libvirt numbers controllers
		// without an index from 0, and the first port would take it.
		zero := uint(0)
		dev.Controllers = append(dev.Controllers, libvirtxml.DomainController{Type: "pci", Model: "pcie-root", Index: &zero})
		for range rootPorts {
			dev.Controllers = append(dev.Controllers, libvirtxml.DomainController{Type: "pci", Model: "pcie-root-port"})
		}
	}
	// A tablet gives the console an absolute pointer: no grab, no drift.
	dev.Inputs = append(dev.Inputs, libvirtxml.DomainInput{Type: "tablet", Bus: "usb"})
	// VNC on the host's loopback only: virt-meow reaches it through the SSH
	// connection (or locally), never over the network unencrypted.
	// (Unless the user's defaults say another address.)
	dev.Graphics = append(dev.Graphics, libvirtxml.DomainGraphic{VNC: &libvirtxml.DomainGraphicVNC{
		Port: -1, AutoPort: "yes",
		Listeners: []libvirtxml.DomainGraphicListener{{Address: &libvirtxml.DomainGraphicListenerAddress{Address: cmp.Or(s.Listen, "127.0.0.1")}}},
	}})
	if s.Sound {
		model := "ich6"
		if machine == "q35" {
			model = "ich9"
		}
		dev.Sounds = append(dev.Sounds, libvirtxml.DomainSound{Model: model})
	}
	video := "virtio"
	if !virtio {
		video = "vga"
	}
	dev.Videos = append(dev.Videos, libvirtxml.DomainVideo{Model: libvirtxml.DomainVideoModel{Type: video}})
	port := uint(0)
	dev.Serials = append(dev.Serials, libvirtxml.DomainSerial{
		Source: &libvirtxml.DomainChardevSource{Pty: &libvirtxml.DomainChardevSourcePty{}},
		Target: &libvirtxml.DomainSerialTarget{Port: &port},
	})
	dev.Consoles = append(dev.Consoles, libvirtxml.DomainConsole{
		Source: &libvirtxml.DomainChardevSource{Pty: &libvirtxml.DomainChardevSourcePty{}},
		Target: &libvirtxml.DomainConsoleTarget{Type: "serial", Port: &port},
	})
	dev.Channels = append(dev.Channels, libvirtxml.DomainChannel{
		Source: &libvirtxml.DomainChardevSource{UNIX: &libvirtxml.DomainChardevSourceUNIX{Mode: "bind"}},
		Target: &libvirtxml.DomainChannelTarget{VirtIO: &libvirtxml.DomainChannelTargetVirtIO{Name: "org.qemu.guest_agent.0"}},
	})
	// The clipboard shared with the console, for a guest with spice-vdagent.
	if h.Clipboard {
		dev.Channels = append(dev.Channels, clipboardChannel())
	}
	if virtio {
		dev.RNGs = append(dev.RNGs, libvirtxml.DomainRNG{
			Model:   "virtio",
			Backend: &libvirtxml.DomainRNGBackend{Random: &libvirtxml.DomainRNGBackendRandom{Device: "/dev/urandom"}},
		})
	}
	balloon := "virtio"
	if !virtio {
		balloon = "none"
	}
	dev.MemBalloon = &libvirtxml.DomainMemBalloon{Model: balloon}
	if s.TPM {
		model := "tpm-tis"
		if machine == "q35" {
			model = "tpm-crb"
		}
		dev.TPMs = append(dev.TPMs, libvirtxml.DomainTPM{
			Model:   model,
			Backend: &libvirtxml.DomainTPMBackend{Emulator: &libvirtxml.DomainTPMBackendEmulator{Version: "2.0"}},
		})
	}
	x, err := d.Marshal()
	return selfClose(x), err
}

var emptyRE = regexp.MustCompile(`<([A-Za-z][\w:.-]*)((?:\s[^<>]*)?)></([A-Za-z][\w:.-]*)>`)

// selfClose writes empty elements as libvirt does, <acpi/> for
// <acpi></acpi>: the same document, easier to read in the XML editor.
// (encoding/xml escapes '>' in attribute values, so [^<>] stays in a tag.)
func selfClose(x string) string {
	return emptyRE.ReplaceAllStringFunc(x, func(m string) string {
		g := emptyRE.FindStringSubmatch(m)
		if g[1] != g[3] {
			return m
		}
		return "<" + g[1] + g[2] + "/>"
	})
}

// Created is what Create made.
type Created struct {
	Domain   Domain   `json:"domain"`
	Disk     string   `json:"disk,omitempty"`        // pool/volume of a new disk
	Seed     string   `json:"seed,omitempty"`        // pool/volume of the cloud-init seed ISO
	StartErr string   `json:"start_error,omitempty"` // defined, but did not start
	Warnings []string `json:"warnings,omitempty"`    // Spec.Warnings: taken, but worth saying
}

// Preview is the definition Create would give the machine s describes, for
// the user to edit before creating it. Nothing is made on the host.
func Preview(ctx context.Context, sess *conn.Session, s Spec) (string, error) {
	h, err := HostInfo(ctx, sess)
	if err != nil {
		return "", err
	}
	if err := s.Check(h); err != nil {
		return "", err
	}
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error { return s.free(lv, s.Name) })
	if err != nil {
		return "", err
	}
	diskPool, diskVol := s.ImportPool, s.ImportVolume
	if s.DiskPool != "" {
		diskPool, diskVol = s.DiskPool, s.diskVolume()
	}
	return s.DomainXML(h, diskPool, diskVol)
}

// free checks that the names a new machine takes are free: its own, and
// its new disk's in its pool.
func (s Spec) free(lv *libvirt.Libvirt, name string) error {
	if _, err := lv.DomainLookupByName(name); err == nil {
		return fmt.Errorf("a machine named %q already exists", name)
	}
	if s.CloudInit != nil {
		p, err := lv.StoragePoolLookupByName(s.seedPool())
		if err != nil {
			return fmt.Errorf("pool %s: %w", s.seedPool(), err)
		}
		if _, err := lv.StorageVolLookupByName(p, s.seedVolume()); err == nil {
			return fmt.Errorf("pool %s already has a volume %s: delete it, or pick another name", s.seedPool(), s.seedVolume())
		}
	}
	if s.DiskPool == "" {
		return nil
	}
	p, err := lv.StoragePoolLookupByName(s.DiskPool)
	if err != nil {
		return fmt.Errorf("pool %s: %w", s.DiskPool, err)
	}
	if _, err := lv.StorageVolLookupByName(p, s.diskVolume()); err == nil {
		return fmt.Errorf("pool %s already has a volume %s: pick another name, or use it as an existing disk", s.DiskPool, s.diskVolume())
	}
	if s.ImageVolume != "" {
		if _, err := volume(lv, s.ImagePool, s.ImageVolume); err != nil {
			return fmt.Errorf("image: %w", err)
		}
	}
	return nil
}

// makeDisk makes the new system disk in pool p: an empty volume in its
// format (qcow2 unless asked), or a copy of the image converted to it (StorageVolCreateXMLFrom, so it
// works over SSH and the image stays as it was).
func (s Spec) makeDisk(lv *libvirt.Libvirt, p libvirt.StoragePool) (libvirt.StorageVol, error) {
	vd := libvirtxml.StorageVolume{
		Name:     s.diskVolume(),
		Capacity: &libvirtxml.StorageVolumeSize{Unit: "GiB", Value: uint64(s.DiskGiB)},
		Target:   &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: s.diskFormat()}},
	}
	if s.ImageVolume == "" {
		vx, err := vd.Marshal()
		if err != nil {
			return libvirt.StorageVol{}, err
		}
		var flags libvirt.StorageVolCreateFlags
		if s.diskFormat() == "qcow2" {
			flags = libvirt.StorageVolCreatePreallocMetadata
		}
		v, err := lv.StorageVolCreateXML(p, vx, flags)
		if err != nil {
			return v, fmt.Errorf("creating the disk: %w", err)
		}
		return v, nil
	}
	src, err := volume(lv, s.ImagePool, s.ImageVolume)
	if err != nil {
		return src, fmt.Errorf("image: %w", err)
	}
	img, err := volumeOf(lv, src)
	if err != nil {
		return src, err
	}
	if img.IsISO() {
		return src, fmt.Errorf("%s/%s is installation media, not a disk image: boot it as the ISO", s.ImagePool, s.ImageVolume)
	}
	// The copy starts at the image's own size; growCopy grows it after.
	vd.Capacity = &libvirtxml.StorageVolumeSize{Unit: "B", Value: img.Capacity}
	vx, err := vd.Marshal()
	if err != nil {
		return src, err
	}
	v, err := lv.StorageVolCreateXMLFrom(p, vx, src, 0)
	if err != nil {
		return v, fmt.Errorf("copying the image %s/%s: %w", s.ImagePool, s.ImageVolume, err)
	}
	return v, nil
}

// growCopy grows the copy of an image to the size asked, if that is more
// than the image's.
func (s Spec) growCopy(lv *libvirt.Libvirt, v libvirt.StorageVol) error {
	want := uint64(s.DiskGiB) << 30
	_, have, _, err := lv.StorageVolGetInfo(v)
	if err != nil {
		return err
	}
	if want <= have {
		return nil
	}
	if err := lv.StorageVolResize(v, want, 0); err != nil {
		return fmt.Errorf("growing the copy of the image to %d GiB: %w", s.DiskGiB, err)
	}
	return nil
}

// usesNewDisk reports whether the definition x still has the disk s would
// create; one the user removed from it isn't made.
func (s Spec) usesNewDisk(x string) (bool, error) { return usesVolume(x, s.DiskPool, s.diskVolume()) }

// usesVolume reports whether the definition x has pool/vol as a disk or CD.
func usesVolume(x, pool, vol string) (bool, error) {
	var d libvirtxml.Domain
	if err := d.Unmarshal(x); err != nil {
		return false, fmt.Errorf("not a domain definition: %w", err)
	}
	if d.Devices == nil {
		return false, nil
	}
	for _, disk := range d.Devices.Disks {
		if v := disk.Source; v != nil && v.Volume != nil && v.Volume.Pool == pool && v.Volume.Volume == vol {
			return true, nil
		}
	}
	return false, nil
}

// Create makes the machine s describes: its new disk, its definition, and
// starts it if asked. x, when set, is the definition to use instead of the
// one s gives (Preview's, as the user edited it); the new disk is made only
// if x still refers to it. A definition libvirt refuses leaves no disk
// behind; a machine that is defined but fails to start is kept, with the
// reason.
func Create(ctx context.Context, sess *conn.Session, s Spec, x string) (Created, error) {
	h, err := HostInfo(ctx, sess)
	if err != nil {
		return Created{}, err
	}
	if err := s.Check(h); err != nil {
		return Created{}, err
	}
	name, newDisk, seed := s.Name, s.DiskPool != "", s.CloudInit != nil
	if x != "" {
		id, err := parseIdentity(x)
		if err != nil {
			return Created{}, err
		}
		if !nameRE.MatchString(id.Name) {
			return Created{}, fmt.Errorf("name %q: letters, digits, '.', '_' and '-', starting with a letter or digit", id.Name)
		}
		name = id.Name
		if newDisk, err = s.usesNewDisk(x); err != nil {
			return Created{}, err
		}
		if seed {
			if seed, err = usesVolume(x, s.seedPool(), s.seedVolume()); err != nil {
				return Created{}, err
			}
		}
	}
	var seedISO []byte
	if seed {
		// The seed is made from the machine's own name, as in the definition.
		if seedISO, err = s.CloudInit.seedISO(s.Name); err != nil {
			return Created{}, err
		}
	}
	var out Created
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		if err := s.free(lv, name); err != nil {
			return err
		}
		diskPool, diskVol := s.ImportPool, s.ImportVolume
		var made *libvirt.StorageVol
		if newDisk {
			p, err := lv.StoragePoolLookupByName(s.DiskPool)
			if err != nil {
				return fmt.Errorf("pool %s: %w", s.DiskPool, err)
			}
			v, err := s.makeDisk(lv, p)
			if err != nil {
				return err
			}
			made = &v
			diskPool, diskVol = s.DiskPool, v.Name
			out.Disk = s.DiskPool + "/" + v.Name
		}
		var seedVol *libvirt.StorageVol
		undo := func(cause error) error {
			if seedVol != nil {
				if derr := lv.StorageVolDelete(*seedVol, 0); derr != nil {
					cause = fmt.Errorf("%w (and removing the cloud-init seed %s failed: %v)", cause, out.Seed, derr)
				}
				out.Seed = ""
			}
			if made != nil {
				if derr := lv.StorageVolDelete(*made, 0); derr != nil {
					return fmt.Errorf("%w (and removing the new disk %s failed: %v)", cause, out.Disk, derr)
				}
				out.Disk = ""
			}
			return cause
		}
		if made != nil && s.ImageVolume != "" {
			// A copy of a cloud image is a disk: grow it to the size asked.
			if err := s.growCopy(lv, *made); err != nil {
				return undo(err)
			}
		}
		if seedISO != nil {
			v, err := uploadBytes(lv, s.seedPool(), s.seedVolume(), seedISO)
			if err != nil {
				return undo(fmt.Errorf("the cloud-init seed: %w", err))
			}
			seedVol = &v
			out.Seed = s.seedPool() + "/" + v.Name
		}
		if x == "" {
			if x, err = s.DomainXML(h, diskPool, diskVol); err != nil {
				return undo(err)
			}
		}
		dom, err := lv.DomainDefineXMLFlags(x, libvirt.DomainDefineValidate)
		if err != nil {
			return undo(fmt.Errorf("libvirt refused the definition: %w", err))
		}
		out.Warnings = s.Warnings(h)
		out.Domain = Domain{UUID: FormatUUID(dom.UUID), Name: dom.Name, ID: -1, State: ShutOff,
			VCPUs: s.VCPUs, MemoryKiB: uint64(s.MemoryMiB) << 10, MaxMemKiB: uint64(s.MemoryMiB) << 10, Persistent: true}
		if _, maxMem, mem, vcpus, _, err := lv.DomainGetInfo(dom); err == nil {
			// What the definition says, edited or not.
			out.Domain.VCPUs, out.Domain.MemoryKiB, out.Domain.MaxMemKiB = int(vcpus), mem, maxMem
		}
		if s.Start {
			if err := lv.DomainCreate(dom); err != nil {
				out.StartErr = err.Error()
			} else {
				out.Domain.State = Running
			}
		}
		return nil
	})
	return out, err
}

// Delete removes a machine's definition, forcing it off first, with its
// NVRAM, TPM state, snapshots' metadata and managed save. With disks, the
// volumes of its disks (not its CD/DVD media) go too, and their names are
// returned; a disk that isn't a volume in a pool is left alone and named in
// the error.
func Delete(ctx context.Context, sess *conn.Session, uuid string, disks bool) ([]string, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return nil, err
	}
	var removed []string
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		x, err := lv.DomainGetXMLDesc(dom, libvirt.DomainXMLInactive)
		if err != nil {
			return err
		}
		var def libvirtxml.Domain
		if err := def.Unmarshal(x); err != nil {
			return err
		}
		if state, _, err := lv.DomainGetState(dom, 0); err == nil && stateOf(libvirt.DomainState(state)).Active() {
			if err := lv.DomainDestroy(dom); err != nil {
				return fmt.Errorf("forcing it off: %w", err)
			}
		}
		flags := libvirt.DomainUndefineManagedSave | libvirt.DomainUndefineSnapshotsMetadata |
			libvirt.DomainUndefineCheckpointsMetadata | libvirt.DomainUndefineNvram
		if err := lv.DomainUndefineFlags(dom, flags|libvirt.DomainUndefineTpm); err != nil {
			// libvirt before 8.9 has no TPM flag (and keeps the state anyway).
			if err := lv.DomainUndefineFlags(dom, flags); err != nil {
				return err
			}
		}
		if !disks || def.Devices == nil {
			return nil
		}
		var errs []error
		for _, d := range def.Devices.Disks {
			if d.Device == "cdrom" && d.Source != nil && d.Source.Volume != nil && strings.HasSuffix(d.Source.Volume.Volume, seedSuffix) {
				// The machine's cloud-init seed, which virt-meow made for it
				// (and which has its password's hash): it goes too.
				if v, err := volume(lv, d.Source.Volume.Pool, d.Source.Volume.Volume); err == nil && lv.StorageVolDelete(v, 0) == nil {
					removed = append(removed, d.Source.Volume.Pool+"/"+d.Source.Volume.Volume)
				}
				continue
			}
			if d.Device != "disk" || d.Source == nil || d.ReadOnly != nil || d.Shareable != nil {
				continue
			}
			var vol libvirt.StorageVol
			var name string
			switch {
			case d.Source.Volume != nil:
				p, err := lv.StoragePoolLookupByName(d.Source.Volume.Pool)
				if err == nil {
					vol, err = lv.StorageVolLookupByName(p, d.Source.Volume.Volume)
				}
				name = d.Source.Volume.Pool + "/" + d.Source.Volume.Volume
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
					continue
				}
			case d.Source.File != nil:
				name = d.Source.File.File
				vol, err = lv.StorageVolLookupByPath(name)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s is not in a storage pool, left alone", name))
					continue
				}
			case d.Source.Block != nil:
				errs = append(errs, fmt.Errorf("%s is a block device, left alone", d.Source.Block.Dev))
				continue
			default:
				continue
			}
			if err := lv.StorageVolDelete(vol, 0); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
			removed = append(removed, name)
		}
		return errors.Join(errs...)
	})
	return removed, err
}
