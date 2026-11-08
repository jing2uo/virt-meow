//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/api"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// A machine's details page under virt-mm serve: its info and power actions,
// reset and save, autostart, title and description, rename, clone.
func TestServeMachine(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-page")
	sv := serve(t)
	cr, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-page", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-page", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := cr.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if _, err := hv.AddNIC(ctx(t), s, uuid, hv.NewNIC{Kind: "user"}); err != nil {
		t.Fatal(err)
	}

	info := func() api.MachineInfo {
		t.Helper()
		var in api.MachineInfo
		sv.call(&in, "Info", sv.conn, uuid)
		return in
	}
	in := info()
	if in.Name != "e2e-page" || in.State != hv.ShutOff || in.Machine == "" || in.Firmware != "bios" ||
		len(in.Devices.Disks) != 1 || len(in.Devices.NICs) != 1 || len(in.Devices.Graphics) == 0 {
		t.Fatalf("Info: %+v", in)
	}
	if !slices.Equal(in.Actions, []hv.Action{hv.Start}) {
		t.Errorf("actions of a machine that is off: %v", in.Actions)
	}
	if err := sv.try(nil, "Save", sv.conn, uuid); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("saving a machine that is off: %v", err)
	}

	// Settings and the name, while it is off.
	var ed hv.Edited
	sv.call(&ed, "Set", sv.conn, uuid, map[string]any{"autostart": true, "title": "Web", "description": "from the page"})
	if len(ed.Changes) != 3 {
		t.Errorf("Set: %+v", ed)
	}
	sv.call(&ed, "Rename", sv.conn, uuid, "e2e-page-2")
	if in := info(); !in.Autostart || in.Title != "Web" || in.Description != "from the page" || in.Name != "e2e-page-2" {
		t.Errorf("after Set and Rename: %+v", in)
	}
	sv.call(nil, "Set", sv.conn, uuid, map[string]any{"autostart": false})

	// A clone: its own disk, in the same pool.
	var cl hv.Cloned
	sv.call(&cl, "Clone", sv.conn, uuid, "e2e-page-copy")
	t.Cleanup(func() { hv.Delete(ctx(t), s, cl.Domain.UUID, true) })
	if cl.Domain.Name != "e2e-page-copy" || len(cl.Disks) != 1 || !strings.HasPrefix(cl.Disks[0].To, "e2e-page/") {
		t.Errorf("Clone: %+v", cl)
	}

	// Running: reset offered and done, rename and clone refused, then saved
	// and its saved state dropped.
	sv.call(nil, "Power", sv.conn, uuid, "start")
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	if in := info(); in.State != hv.Running || !slices.Contains(in.Actions, hv.Reset) || slices.Contains(in.Actions, hv.Start) {
		t.Errorf("running: %s %v", in.State, in.Actions)
	}
	sv.call(nil, "Power", sv.conn, uuid, "reset")
	if err := sv.try(nil, "Rename", sv.conn, uuid, "e2e-page-3"); err == nil || !strings.Contains(err.Error(), "running") {
		t.Errorf("renaming a running machine: %v", err)
	}
	if err := sv.try(nil, "Clone", sv.conn, uuid, "e2e-page-3"); err == nil {
		t.Error("cloned a running machine")
	}
	sv.call(nil, "Save", sv.conn, uuid)
	if in := info(); in.State != hv.ShutOff || !in.Saved {
		t.Errorf("after Save: %s saved=%v", in.State, in.Saved)
	}
	var dropped bool
	sv.call(&dropped, "DropSave", sv.conn, uuid)
	if in := info(); !dropped || in.Saved {
		t.Errorf("DropSave: %v, saved=%v", dropped, in.Saved)
	}
	if err := sv.try(nil, "Info", sv.conn, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Error("Info of no machine")
	}
}

// The CPU, memory, firmware and boot section of the details page under
// virt-mm serve: Set, CPUModels and SetBoot, and what Info reads back.
func TestServeCPUMemoryBoot(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-cpu-page")
	sv := serve(t)
	cr, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-cpu-page", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-cpu-page", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := cr.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if _, err := hv.AddNIC(ctx(t), s, uuid, hv.NewNIC{Kind: "user"}); err != nil {
		t.Fatal(err)
	}
	info := func() api.MachineInfo {
		t.Helper()
		var in api.MachineInfo
		sv.call(&in, "Info", sv.conn, uuid)
		return in
	}

	var models hv.CPUModels
	sv.call(&models, "CPUModels", sv.conn, uuid)
	// A model a q35 machine starts with: the first usable one may be 486,
	// which has no APIC for its PCIe ports.
	model := ""
	for _, want := range []string{"Nehalem", "Westmere", "SandyBridge", "Penryn", "Conroe", "Opteron_G2"} {
		if slices.ContainsFunc(models.Models, func(m hv.CPUModel) bool { return m.Name == want && m.Usable == "yes" }) {
			model = want
			break
		}
	}
	if model == "" {
		t.Fatalf("CPUModels: none of the usual models usable in %+v", models)
	}

	var ed hv.Edited
	sv.call(&ed, "Set", sv.conn, uuid, map[string]any{"vcpus": 4, "sockets": 1, "cores": 2, "threads": 2,
		"cpu_model": model, "memory_mib": 384, "max_memory_mib": 512, "firmware": "uefi"})
	in := info()
	if c := in.Config; c.VCPUs != 4 || c.Sockets != 1 || c.Cores != 2 || c.Threads != 2 || c.MemoryKiB != 384<<10 || c.MaxMemKiB != 512<<10 {
		t.Errorf("Config after Set: %+v (%+v)", c, ed)
	}
	if in.CPU != model || in.Firmware != "uefi" {
		t.Errorf("after Set: cpu %q firmware %q", in.CPU, in.Firmware)
	}

	mac := in.Devices.NICs[0].MAC
	sv.call(&ed, "SetBoot", sv.conn, uuid, []string{mac, "vda"}, true)
	d := info().Devices
	if d.NICs[0].Boot != 1 || d.Disks[0].Boot != 2 || !d.BootMenu {
		t.Errorf("after SetBoot: nic %d disk %d menu %v", d.NICs[0].Boot, d.Disks[0].Boot, d.BootMenu)
	}
	sv.call(&ed, "SetBoot", sv.conn, uuid, []string{"vda"}, nil)
	if d := info().Devices; d.NICs[0].Boot != 0 || d.Disks[0].Boot != 1 || !d.BootMenu {
		t.Errorf("the interface left out: nic %d disk %d menu %v", d.NICs[0].Boot, d.Disks[0].Boot, d.BootMenu)
	}
	if err := sv.try(nil, "SetBoot", sv.conn, uuid, []string{"vdz"}, nil); err == nil || !strings.Contains(err.Error(), "vdz") {
		t.Errorf("booting from no such disk: %v", err)
	}

	// Back to one vCPU and BIOS before it runs: four vCPUs booting OVMF
	// under TCG keep QEMU's monitor so busy that a call through it can
	// take the whole call timeout in a loaded container.
	sv.call(&ed, "Set", sv.conn, uuid, map[string]any{"vcpus": 1, "sockets": 1, "cores": 1, "threads": 1, "firmware": "bios"})
	if c := info().Config; c.VCPUs != 1 || c.Cores != 1 || c.Threads != 1 {
		t.Errorf("back to one vCPU: %+v", c)
	}

	// Running: the firmware refused, the memory changed now.
	sv.call(nil, "Power", sv.conn, uuid, "start")
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	if err := sv.try(nil, "Set", sv.conn, uuid, map[string]any{"firmware": "bios"}); err == nil {
		t.Error("changed the firmware of a running machine")
	}
	sv.call(&ed, "Set", sv.conn, uuid, map[string]any{"memory_mib": 320})
	if !ed.Active || len(ed.Changes) != 1 {
		t.Errorf("memory of a running machine: %+v", ed)
	}
	if in := info(); in.Config.MemoryKiB != 320<<10 {
		t.Errorf("Config after a change while running: %+v", in.Config)
	}
}

// The disks and CD/DVD section of the details page under virt-mm serve:
// AddDisk, ResizeDisk, RemoveDisk, InsertCD and EjectCD, with the volumes
// CreateOptions offers.
func TestServeDisks(t *testing.T) {
	s := qemuSession(t)
	const pool = "e2e-disk-page"
	scratchPool(t, s, pool)
	iso := bytes.Repeat([]byte("virt-mm"), 100_000)
	if _, err := hv.Upload(ctx(t), s, pool, "page.iso", bytes.NewReader(iso), int64(len(iso)), nil); err != nil {
		t.Fatal(err)
	}
	sv := serve(t)
	cr, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-disk-page", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: pool, DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := cr.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	disks := func() []hv.Disk {
		t.Helper()
		var in api.MachineInfo
		sv.call(&in, "Info", sv.conn, uuid)
		return in.Devices.Disks
	}
	find := func(ds []hv.Disk, target string) hv.Disk {
		t.Helper()
		for _, d := range ds {
			if d.Target == target {
				return d
			}
		}
		t.Fatalf("no disk %s in %+v", target, ds)
		return hv.Disk{}
	}

	var opts struct {
		ISOs  []hv.Volume `json:"isos"`
		Disks []hv.Volume `json:"disks"`
	}
	sv.call(&opts, "CreateOptions", sv.conn)
	if !slices.ContainsFunc(opts.ISOs, func(v hv.Volume) bool { return v.Pool == pool && v.Name == "page.iso" }) {
		t.Fatalf("CreateOptions: no %s/page.iso in %+v", pool, opts.ISOs)
	}

	// Off: a new volume, grown, and removed with it; an ISO in a new drive,
	// ejected and inserted again.
	var ed hv.Edited
	sv.call(&ed, "AddDisk", sv.conn, uuid, hv.NewDisk{Pool: pool, SizeGiB: 1})
	if len(ed.Changes) != 1 {
		t.Errorf("AddDisk: %+v", ed)
	}
	if d := find(disks(), "vdb"); d.Source != pool+"/e2e-disk-page-vdb.qcow2" || d.Capacity != 1<<30 {
		t.Errorf("the new disk: %+v", d)
	}
	sv.call(&ed, "ResizeDisk", sv.conn, uuid, "vdb", uint64(2<<30))
	if d := find(disks(), "vdb"); d.Capacity != 2<<30 {
		t.Errorf("after ResizeDisk: %+v", d)
	}
	if err := sv.try(nil, "ResizeDisk", sv.conn, uuid, "vdb", uint64(1<<30)); err == nil || !strings.Contains(err.Error(), "shrink") {
		t.Errorf("shrinking a disk: %v", err)
	}
	sv.call(&ed, "InsertCD", sv.conn, uuid, "", pool, "page.iso")
	cd := find(disks(), "sda")
	if cd.Device != "cdrom" || cd.Source != pool+"/page.iso" {
		t.Errorf("after InsertCD: %+v", cd)
	}
	sv.call(&ed, "EjectCD", sv.conn, uuid, "sda")
	if d := find(disks(), "sda"); d.Source != "" {
		t.Errorf("after EjectCD: %+v", d)
	}
	sv.call(&ed, "InsertCD", sv.conn, uuid, "sda", pool, "page.iso")
	sv.call(&ed, "RemoveDisk", sv.conn, uuid, "vdb", true)
	if ds := disks(); slices.ContainsFunc(ds, func(d hv.Disk) bool { return d.Target == "vdb" }) {
		t.Errorf("after RemoveDisk: %+v", ds)
	}
	var left struct {
		Disks []hv.Volume `json:"disks"`
	}
	sv.call(&left, "CreateOptions", sv.conn)
	if slices.ContainsFunc(left.Disks, func(v hv.Volume) bool { return v.Name == "e2e-disk-page-vdb.qcow2" }) {
		t.Error("RemoveDisk with its volume left the volume")
	}

	// Running: a disk added now, its volume kept on removal; the CD/DVD
	// ejected live.
	sv.call(nil, "Power", sv.conn, uuid, "start")
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	sv.call(&ed, "AddDisk", sv.conn, uuid, hv.NewDisk{Pool: pool, SizeGiB: 1})
	if !ed.Active || len(ed.Changes) != 1 || !ed.Changes[0].Live {
		t.Errorf("AddDisk while running: %+v", ed)
	}
	if err := sv.try(nil, "RemoveDisk", sv.conn, uuid, "vdb", true); err == nil || !strings.Contains(err.Error(), "running") {
		t.Errorf("deleting a running machine's volume: %v", err)
	}
	sv.call(&ed, "EjectCD", sv.conn, uuid, "sda")
	if !ed.Active || len(ed.Changes) != 1 || !ed.Changes[0].Live {
		t.Errorf("EjectCD while running: %+v", ed)
	}
}

// The network interfaces section of the details page under virt-mm serve:
// AddNIC on a virtual network and user networking, SetLink, RemoveNIC, and
// the guest's address from the network's DHCP lease in Info.
func TestServeNICs(t *testing.T) {
	s := qemuSession(t)
	const net = "e2e-nic-page"
	t.Cleanup(func() { hv.RemoveNetwork(ctx(t), s, net, true) })
	netCLI(t, "add", net, "--type", "isolated", "--address", "10.231.88.1/24")
	g := netGuest(t, s, "e2e-nic-page", net)
	sv := serve(t)
	info := func() api.MachineInfo {
		t.Helper()
		var in api.MachineInfo
		sv.call(&in, "Info", sv.conn, g.UUID)
		return in
	}
	var opts struct {
		Networks []hv.Network `json:"networks"`
	}
	sv.call(&opts, "CreateOptions", sv.conn)
	if !slices.ContainsFunc(opts.Networks, func(n hv.Network) bool { return n.Name == net }) {
		t.Fatalf("CreateOptions: no network %s in %+v", net, opts.Networks)
	}

	// Running, on the network: an address from its lease.
	sv.call(nil, "Power", sv.conn, g.UUID, "start")
	mac := info().Devices.NICs[0].MAC
	deadline := time.Now().Add(90 * time.Second)
	for {
		in := info()
		if in.AddressSource == "lease" && slices.ContainsFunc(in.Addresses, func(a hv.Address) bool {
			return strings.EqualFold(a.MAC, mac) && strings.HasPrefix(a.Addr, "10.231.88.")
		}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no address from the lease: %+v %s", in.Addresses, in.AddressNote)
		}
		time.Sleep(2 * time.Second)
	}

	var ed hv.Edited
	sv.call(&ed, "SetLink", sv.conn, g.UUID, mac, false)
	if !ed.Active || len(ed.Changes) != 1 || !ed.Changes[0].Live {
		t.Errorf("SetLink down: %+v", ed)
	}
	if n := info().Devices.NICs[0]; n.Link != "down" {
		t.Errorf("after SetLink down: %+v", n)
	}
	sv.call(&ed, "SetLink", sv.conn, g.UUID, mac, true)
	if n := info().Devices.NICs[0]; n.Link != "up" {
		t.Errorf("after SetLink up: %+v", n)
	}

	// A second interface while it runs: user networking, e1000e; then
	// removed. This guest (pc, no ACPI) can't hot-plug, which the change
	// says.
	sv.call(&ed, "AddNIC", sv.conn, g.UUID, hv.NewNIC{Kind: "user", Model: "e1000e"})
	if !ed.Active || len(ed.Changes) != 1 || (!ed.Changes[0].Live && ed.Changes[0].Note == "") {
		t.Errorf("AddNIC while running: %+v", ed)
	}
	nics := info().Devices.NICs
	if len(nics) != 2 || nics[1].Kind != "user" || nics[1].Model != "e1000e" {
		t.Fatalf("after AddNIC: %+v", nics)
	}
	sv.call(&ed, "RemoveNIC", sv.conn, g.UUID, nics[1].MAC)
	if nics := info().Devices.NICs; len(nics) != 1 {
		t.Errorf("after RemoveNIC: %+v", nics)
	}
	if err := sv.try(nil, "AddNIC", sv.conn, g.UUID, hv.NewNIC{Kind: "network", Source: "e2e-no-such-net"}); err == nil || !strings.Contains(err.Error(), "e2e-no-such-net") {
		t.Errorf("AddNIC on no such network: %v", err)
	}
	if err := sv.try(nil, "RemoveNIC", sv.conn, g.UUID, "52:54:00:00:00:99"); err == nil {
		t.Error("RemoveNIC of no such interface")
	}
	// Off: on the network by name.
	sv.call(nil, "Power", sv.conn, g.UUID, "force-off")
	sv.call(&ed, "AddNIC", sv.conn, g.UUID, hv.NewNIC{Kind: "network", Source: net})
	if nics := info().Devices.NICs; len(nics) != 2 || nics[1].Kind != "network" || nics[1].Source != net || nics[1].Model != "virtio" {
		t.Errorf("AddNIC on %s: %+v", net, nics)
	}
}

// The other devices section of the details page under virt-mm serve: video,
// VNC and where it listens, TPM, sound, RNG, and a device from its XML.
func TestServeOtherDevices(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-dev-page")
	sv := serve(t)
	cr, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-dev-page", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-dev-page", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := cr.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	devs := func() hv.Devices {
		t.Helper()
		var in api.MachineInfo
		sv.call(&in, "Info", sv.conn, uuid)
		return in.Devices
	}

	// Off.
	var ed hv.Edited
	sv.call(&ed, "SetVideo", sv.conn, uuid, "vga")
	sv.call(&ed, "SetSound", sv.conn, uuid, "ich9")
	sv.call(&ed, "AddTPM", sv.conn, uuid, "")
	sv.call(&ed, "RemoveRNG", sv.conn, uuid)
	sv.call(&ed, "SetGraphicsListen", sv.conn, uuid, "vnc", "0.0.0.0")
	if len(ed.Warnings) != 1 || !strings.Contains(ed.Warnings[0], "unencrypted") {
		t.Errorf("VNC on 0.0.0.0, no warning: %+v", ed)
	}
	d := devs()
	if len(d.Video) != 1 || d.Video[0].Model != "vga" || !slices.Equal(d.Sound, []string{"ich9"}) || len(d.TPMs) != 1 ||
		len(d.RNGs) != 0 || len(d.Graphics) != 1 || d.Graphics[0].Listen != "0.0.0.0" {
		t.Fatalf("after the changes: %+v", d)
	}
	sv.call(&ed, "RemoveGraphics", sv.conn, uuid, "vnc")
	ed = hv.Edited{}
	sv.call(&ed, "AddGraphics", sv.conn, uuid, "vnc", "")
	if g := devs().Graphics; len(g) != 1 || g[0].Listen != "127.0.0.1" || len(ed.Warnings) != 0 {
		t.Errorf("VNC added again: %+v %+v", g, ed)
	}
	if err := sv.try(nil, "AddGraphics", sv.conn, uuid, "vnc", ""); err == nil {
		t.Error("a second VNC console")
	}
	if err := sv.try(nil, "SetVideo", sv.conn, uuid, "voodoo"); err == nil {
		t.Error("video voodoo")
	}
	sv.call(&ed, "RemoveTPM", sv.conn, uuid)
	sv.call(&ed, "SetSound", sv.conn, uuid, "none")
	const kbd = "<input type='keyboard' bus='usb'/>"
	sv.call(&ed, "AttachDevice", sv.conn, uuid, kbd)
	if ed.Active || len(ed.Changes) != 1 || ed.Changes[0].What != "input (keyboard)" {
		t.Errorf("AttachDevice: %+v", ed)
	}
	if err := sv.try(nil, "AttachDevice", sv.conn, uuid, "keyboard"); err == nil {
		t.Error("attached what isn't XML")
	}
	sv.call(&ed, "DetachDevice", sv.conn, uuid, kbd)
	if len(ed.Changes) != 1 || strings.Contains(savedXML(t, s, uuid), "<input type='keyboard' bus='usb'") {
		t.Errorf("DetachDevice: %+v", ed)
	}

	// Running: the RNG and a USB keyboard now; the video at the next start.
	sv.call(nil, "Power", sv.conn, uuid, "start")
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	sv.call(&ed, "AddRNG", sv.conn, uuid)
	if !ed.Active || !ed.Changes[0].Live {
		t.Errorf("AddRNG while running: %+v", ed)
	}
	sv.call(&ed, "AttachDevice", sv.conn, uuid, kbd)
	if !ed.Active || !ed.Changes[0].Live {
		t.Errorf("AttachDevice while running: %+v", ed)
	}
	sv.call(&ed, "SetVideo", sv.conn, uuid, "virtio")
	if !ed.Active || len(ed.Changes) != 1 || ed.Changes[0].Live {
		t.Errorf("SetVideo while running: %+v", ed)
	}
}

// The snapshots section of the details page under virt-mm serve: the tree,
// create, revert, delete with and without the snapshots taken from it.
func TestServeSnapshots(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-snap-page")
	sv := serve(t)
	cr, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-snap-page", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-snap-page", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := cr.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	tree := func() string {
		t.Helper()
		var ss []hv.Snapshot
		sv.call(&ss, "Snapshots", sv.conn, uuid)
		var b strings.Builder
		for _, x := range ss {
			fmt.Fprintf(&b, "%s%s", strings.Repeat(" ", x.Depth), x.Name)
			if x.Memory {
				b.WriteString("+mem")
			}
			if x.Current {
				b.WriteString("*")
			}
			b.WriteString(";")
		}
		return b.String()
	}
	if got := tree(); got != "" {
		t.Fatalf("a new machine's snapshots: %q", got)
	}

	var ed hv.Edited
	sv.call(&ed, "CreateSnapshot", sv.conn, uuid, "s1", "before")
	var ss []hv.Snapshot
	sv.call(&ss, "Snapshots", sv.conn, uuid)
	if len(ss) != 1 || ss[0].Description != "before" || ss[0].State != "shutoff" || ss[0].Created.IsZero() {
		t.Errorf("s1: %+v", ss)
	}
	sv.call(nil, "Power", sv.conn, uuid, "start")
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	sv.call(&ed, "CreateSnapshot", sv.conn, uuid, "s2", "")
	if got := tree(); got != "s1; s2+mem*;" {
		t.Errorf("after s2: %q", got)
	}
	if err := sv.try(nil, "CreateSnapshot", sv.conn, uuid, "a b", ""); err == nil {
		t.Error("a snapshot named \"a b\"")
	}

	// Back to s1: off; a sibling of s2 taken there.
	sv.call(&ed, "RevertSnapshot", sv.conn, uuid, "s1")
	var in api.MachineInfo
	sv.call(&in, "Info", sv.conn, uuid)
	if in.State != hv.ShutOff || ed.Active {
		t.Errorf("reverted to s1: %s %+v", in.State, ed)
	}
	sv.call(&ed, "CreateSnapshot", sv.conn, uuid, "s3", "")
	if got := tree(); got != "s1; s2+mem; s3*;" {
		t.Errorf("after s3: %q", got)
	}
	// Back to s2: running from its memory.
	sv.call(&ed, "RevertSnapshot", sv.conn, uuid, "s2")
	sv.call(&in, "Info", sv.conn, uuid)
	if in.State != hv.Running || !ed.Active {
		t.Errorf("reverted to s2: %s %+v", in.State, ed)
	}

	// s1 alone: its children move up; then s2 with nothing under it, and
	// s3 with its child s4.
	sv.call(&ed, "DeleteSnapshot", sv.conn, uuid, "s1", false)
	if got := tree(); got != "s2+mem*;s3;" {
		t.Errorf("s1 deleted alone: %q", got)
	}
	sv.call(&ed, "RevertSnapshot", sv.conn, uuid, "s3")
	sv.call(&ed, "CreateSnapshot", sv.conn, uuid, "s4", "")
	sv.call(&ed, "DeleteSnapshot", sv.conn, uuid, "s3", true)
	if len(ed.Changes) != 1 || !strings.Contains(ed.Changes[0].What, "the 1 taken from it") {
		t.Errorf("s3 with its children: %+v", ed)
	}
	if got := tree(); got != "s2+mem;" {
		t.Errorf("after s3 and its children: %q", got)
	}
	if err := sv.try(nil, "DeleteSnapshot", sv.conn, uuid, "s9", false); err == nil {
		t.Error("deleted no such snapshot")
	}
}
