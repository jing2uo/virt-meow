//go:build e2e

package e2e

import (
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// liveOS is the <os> of the running machine: the firmware it booted.
func liveOS(t *testing.T, s *conn.Session, name string) *libvirtxml.DomainOS {
	t.Helper()
	var d libvirtxml.Domain
	err := s.Do(ctx(t), func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByName(name)
		if err != nil {
			return err
		}
		x, err := lv.DomainGetXMLDesc(dom, 0)
		if err == nil {
			err = d.Unmarshal(x)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return d.OS
}

// inContainer reports whether file exists in the e2e container.
func inContainer(t *testing.T, file string) bool {
	t.Helper()
	return podman("exec", env(t, "VIRT_MM_E2E_CONTAINER"), "test", "-e", file).Run() == nil
}

// virt-mm set --firmware: bios → uefi → uefi-secure → bios, the machine
// started after each switch, and no NVRAM carried from one firmware to the
// next.
func TestFirmware(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-fw")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-fw", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-fw", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	const nvram = "/var/lib/libvirt/qemu/nvram/e2e-fw_VARS.fd"

	for _, st := range []struct {
		fw     string
		loader string // in the path of the firmware it boots; "" for BIOS
		secure bool
	}{
		{"uefi", "OVMF_CODE", false},
		{"uefi-secure", "OVMF_CODE", true},
		{"bios", "", false},
	} {
		var e hv.Edited
		cli(t, &e, false, "set", "e2e-fw", "--firmware", st.fw)
		if len(e.Changes) == 0 || !strings.HasSuffix(e.Changes[0].What, "→ "+st.fw) {
			t.Fatalf("to %s: %+v", st.fw, e.Changes)
		}
		if inContainer(t, nvram) {
			t.Fatalf("to %s: the old NVRAM is still there (changes %+v)", st.fw, e.Changes)
		}
		if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
			t.Fatalf("starting with %s: %v", st.fw, err)
		}
		os := liveOS(t, s, "e2e-fw")
		if st.loader == "" {
			if os.Loader != nil || os.NVRam != nil {
				t.Errorf("BIOS boots %+v %+v", os.Loader, os.NVRam)
			}
		} else if os.Loader == nil || !strings.Contains(os.Loader.Path, st.loader) || (os.Loader.Secure == "yes") != st.secure || os.NVRam == nil || !inContainer(t, nvram) {
			t.Errorf("%s boots %+v, NVRAM %+v", st.fw, os.Loader, os.NVRam)
		}
		if _, err := hv.Set(ctx(t), s, uuid, hv.Settings{Firmware: ptr("bios")}); err == nil {
			t.Error("changed the firmware of a running machine")
		}
		if err := hv.Power(ctx(t), s, uuid, hv.ForceOff); err != nil {
			t.Fatal(err)
		}
	}
	if e := mustEdit(t)(hv.Set(ctx(t), s, uuid, hv.Settings{Firmware: ptr("bios")})); len(e.Changes) != 0 {
		t.Errorf("bios to bios: %+v", e.Changes)
	}
	if _, err := hv.Set(ctx(t), s, uuid, hv.Settings{Firmware: ptr("coreboot")}); err == nil {
		t.Error("firmware coreboot accepted")
	}

	// Secure Boot needs q35's SMM: refused on i440fx, which still gets UEFI.
	pc, err := hv.Define(ctx(t), s, `<domain type='qemu'><name>e2e-fw-pc</name><memory unit='MiB'>128</memory>
		<os><type arch='x86_64' machine='pc'>hvm</type></os></domain>`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, pc.UUID, false) })
	if msg := cli(t, nil, true, "set", "e2e-fw-pc", "--firmware", "uefi-secure"); !strings.Contains(msg, "q35") {
		t.Errorf("Secure Boot on i440fx: %q", msg)
	}
	e := mustEdit(t)(hv.Set(ctx(t), s, pc.UUID, hv.Settings{Firmware: ptr("uefi")}))
	changed(t, e, "ACPI on")
}
