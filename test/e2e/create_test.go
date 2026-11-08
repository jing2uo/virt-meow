//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// qemuSession connects to the container's QEMU driver (TCG: no /dev/kvm).
func qemuSession(t *testing.T) *conn.Session {
	tg, err := conn.Parse("qemu+unix:///system?socket=" + filepath.Join(env(t, "VIRT_MM_E2E_RUN"), "libvirt-sock"))
	if err != nil {
		t.Fatal(err)
	}
	s := conn.New(tg, conn.Options{})
	t.Cleanup(func() { s.Close() })
	return s
}

// scratchPool makes a transient dir pool for one test; it goes when the
// test ends, its volumes with it.
func scratchPool(t *testing.T, s *conn.Session, name string) {
	x, _ := (&libvirtxml.StoragePool{
		Type:   "dir",
		Name:   name,
		Target: &libvirtxml.StoragePoolTarget{Path: "/var/lib/libvirt/images/" + name},
	}).Marshal()
	err := s.Do(ctx(t), func(lv *libvirt.Libvirt) error {
		_, err := lv.StoragePoolCreateXML(x, libvirt.StoragePoolCreateWithBuild)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.Do(c, func(lv *libvirt.Libvirt) error {
			p, err := lv.StoragePoolLookupByName(name)
			if err != nil {
				return err
			}
			vols, _, _ := lv.StoragePoolListAllVolumes(p, 1, 0)
			for _, v := range vols {
				lv.StorageVolDelete(v, 0)
			}
			return lv.StoragePoolDestroy(p)
		})
	})
}

func TestCreateBootDelete(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e")

	h, err := hv.HostInfo(ctx(t), s)
	if err != nil {
		t.Fatal(err)
	}
	if h.VirtType == "" || !slices.Contains(h.Machines, "q35") || !h.UEFI || !h.TPM {
		t.Fatalf("host: %+v (want q35, UEFI and TPM from the container's QEMU, OVMF and swtpm)", h)
	}

	// An ISO goes in through libvirt's stream.
	iso := bytes.Repeat([]byte("virt-mm"), 200_000)
	var last int64
	vol, err := hv.Upload(ctx(t), s, "e2e", "install.iso", bytes.NewReader(iso), int64(len(iso)), func(n int64) { last = n })
	if err != nil {
		t.Fatal(err)
	}
	if !vol.IsISO() || vol.Capacity != uint64(len(iso)) || last != int64(len(iso)) {
		t.Fatalf("uploaded %+v, progress %d", vol, last)
	}
	if _, err := hv.Upload(ctx(t), s, "e2e", "install.iso", bytes.NewReader(iso), int64(len(iso)), nil); err == nil {
		t.Fatal("uploaded over an existing volume")
	}

	spec := hv.Spec{
		Name: "e2e-vm", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e", DiskGiB: 1, ISOPool: "e2e", ISOVolume: "install.iso",
		NetKind: "none", Firmware: "uefi", TPM: true, Start: true,
	}
	c, err := hv.Create(ctx(t), s, spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.StartErr != "" || c.Disk != "e2e/e2e-vm.qcow2" {
		t.Fatalf("created %+v", c)
	}
	d := machine(t, s, "e2e-vm")
	if d.State != hv.Running {
		t.Fatalf("e2e-vm is %s, want running", d.State)
	}

	// What libvirt made of it: UEFI chosen, the TPM, the CD first.
	var def libvirtxml.Domain
	s.Do(ctx(t), func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByName("e2e-vm")
		if err != nil {
			return err
		}
		x, err := lv.DomainGetXMLDesc(dom, 0)
		if err == nil {
			err = def.Unmarshal(x)
		}
		return err
	})
	if def.OS == nil || def.OS.Loader == nil || !strings.Contains(def.OS.Loader.Path, "OVMF") {
		t.Errorf("firmware: %+v", def.OS)
	}
	if len(def.Devices.TPMs) != 1 || len(def.Devices.Disks) != 2 || def.Devices.Disks[0].Device != "cdrom" {
		t.Errorf("devices: %d TPMs, disks %+v", len(def.Devices.TPMs), def.Devices.Disks)
	}
	if g := def.Devices.Graphics; len(g) != 1 || g[0].VNC == nil || g[0].VNC.Listeners[0].Address.Address != "127.0.0.1" {
		t.Errorf("graphics: %+v", g)
	}

	if _, err := hv.Create(ctx(t), s, spec, ""); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second create: %v", err)
	}

	removed, err := hv.Delete(ctx(t), s, d.UUID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{"e2e/e2e-vm.qcow2"}) {
		t.Fatalf("removed %v", removed)
	}
	vols, err := hv.Volumes(ctx(t), s, "e2e")
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 1 || vols[0].Name != "install.iso" {
		t.Fatalf("left in the pool: %+v (want the ISO only)", vols)
	}
}

func TestCreateRejectsWhatTheHostLacks(t *testing.T) {
	s := qemuSession(t)
	h, err := hv.HostInfo(ctx(t), s)
	if err != nil {
		t.Fatal(err)
	}
	bad := []hv.Spec{
		{Name: "bad name", OS: "linux", VCPUs: 1, MemoryMiB: 256, DiskPool: "x", DiskGiB: 1, NetKind: "none", Firmware: "bios"},
		{Name: "big", OS: "linux", VCPUs: 1, MemoryMiB: int(h.MemoryKiB>>10) + 1, DiskPool: "x", DiskGiB: 1, NetKind: "none", Firmware: "bios"},
		{Name: "noboot", OS: "linux", VCPUs: 1, MemoryMiB: 256, NetKind: "none", Firmware: "bios"},
		{Name: "nonet", OS: "linux", VCPUs: 1, MemoryMiB: 256, DiskPool: "x", DiskGiB: 1, NetKind: "bridge", Firmware: "bios"},
	}
	for _, sp := range bad {
		if err := sp.Check(h); err == nil {
			t.Errorf("Check accepted %+v", sp)
		}
	}
}

// Over SSH, the host's bridges come from its sysfs (the container has no
// interface driver either); the call must work even when there are none.
func TestBridgesOverSSH(t *testing.T) {
	sshHome(t)
	hk := &conn.HostKeys{Own: filepath.Join(t.TempDir(), "known_hosts")}
	tg, _ := conn.Parse("qemu+ssh://root@" + env(t, "VIRT_MM_E2E_SSH") + "/system")
	s := conn.New(tg, conn.Options{HostKeys: hk})
	defer s.Close()
	var unknown *conn.UnknownHostKeyError
	if err := s.Connect(ctx(t)); errors.As(err, &unknown) {
		hk.Trust(unknown.Addr, unknown.Key)
	} else if err != nil {
		t.Fatal(err)
	}
	if _, err := hv.Bridges(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	out, err := s.Run(ctx(t), "echo virt-mm")
	if err != nil || strings.TrimSpace(string(out)) != "virt-mm" {
		t.Fatalf("Run: %q, %v", out, err)
	}
}
