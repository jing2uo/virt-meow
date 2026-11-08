package hv

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// firmwareOf is a definition's firmware: bios, uefi or uefi-secure.
func firmwareOf(d *libvirtxml.Domain) string {
	o := d.OS
	if o == nil || (o.Firmware != "efi" && (o.Loader == nil || o.Loader.Type != "pflash")) {
		return "bios"
	}
	if o.Loader != nil && o.Loader.Secure == "yes" {
		return "uefi-secure"
	}
	if o.FirmwareInfo != nil {
		for _, f := range o.FirmwareInfo.Features {
			if f.Name == "secure-boot" && f.Enabled == "yes" {
				return "uefi-secure"
			}
		}
	}
	return "uefi"
}

// setFirmware switches a machine that is off to fw. The firmware libvirt
// resolved for the old choice (<loader>, <nvram>) is dropped so that it
// picks one again, and the old NVRAM file is deleted once the definition is
// saved: libvirt reuses a file that is there, and variables written by
// another firmware (or Secure Boot keys that are no longer wanted) must not
// carry over.
func (e *edit) setFirmware(fw string) error {
	d := e.def
	if e.active {
		return fmt.Errorf("%s is running: the firmware changes only on a machine that is off", d.Name)
	}
	switch fw {
	case "bios", "uefi", "uefi-secure":
	default:
		return fmt.Errorf("firmware %q: bios, uefi or uefi-secure", fw)
	}
	was := firmwareOf(d)
	if was == fw {
		return nil
	}
	if d.OS == nil {
		d.OS = &libvirtxml.DomainOS{}
	}
	if fw != "bios" {
		dc, err := domainCaps(e.lv, d)
		if err != nil {
			return err
		}
		if dc.OS == nil || dc.OS.Supported != "yes" || !slices.Contains(enum(dc.OS.Enums, "firmware"), "efi") {
			return errors.New("the host has no UEFI firmware for this machine's type")
		}
		if fw == "uefi-secure" {
			machine := ""
			if d.OS.Type != nil {
				machine = d.OS.Type.Machine
			}
			// Secure Boot keeps its variables safe from the guest through
			// SMM, which only q35 has.
			if !strings.Contains(machine, "q35") {
				return fmt.Errorf("Secure Boot needs the q35 machine type, and %s is %s", d.Name, cmpOr(machine, "of no known type"))
			}
			secure := dc.OS.Loader != nil && slices.Contains(enum(dc.OS.Loader.Enums, "secure"), "yes")
			if ff := dc.OS.FirmwareFeatures; ff != nil && ff.Supported == "yes" {
				secure = slices.Contains(enum(ff.Enums, "secureBoot"), "yes")
			}
			if !secure {
				return errors.New("the host has no Secure Boot firmware for this machine's type")
			}
		}
	}
	var oldNVRAM string
	if d.OS.NVRam != nil {
		oldNVRAM = d.OS.NVRam.NVRam
	}
	d.OS.Loader, d.OS.NVRam, d.OS.Firmware, d.OS.FirmwareInfo = nil, nil, "", nil
	if fw != "bios" {
		secure := map[bool]string{true: "yes", false: "no"}[fw == "uefi-secure"]
		d.OS.Firmware = "efi"
		d.OS.FirmwareInfo = &libvirtxml.DomainOSFirmwareInfo{Features: []libvirtxml.DomainOSFirmwareFeature{
			{Name: "enrolled-keys", Enabled: secure},
			{Name: "secure-boot", Enabled: secure},
		}}
		if d.Features == nil {
			d.Features = &libvirtxml.DomainFeatureList{}
		}
		if fw == "uefi-secure" {
			d.Features.SMM = &libvirtxml.DomainFeatureSMM{State: "on"}
		}
	}
	e.changed(fmt.Sprintf("firmware %s → %s", was, fw), nil)
	// libvirt refuses UEFI without ACPI on x86 (OVMF needs its tables).
	if fw != "bios" && d.Features.ACPI == nil {
		d.Features.ACPI = &libvirtxml.DomainFeature{}
		e.changed("ACPI on (UEFI needs it)", nil)
	}
	if oldNVRAM != "" {
		e.outside("old NVRAM "+oldNVRAM+" deleted", func() error { return deleteFile(e.lv, oldNVRAM) })
	}
	return nil
}

// deleteFile deletes a file on the host through libvirt's storage driver
// (fileVolume). A file that is already gone is fine.
func deleteFile(lv *libvirt.Libvirt, file string) error {
	v, _, release, err := fileVolume(lv, file)
	if errors.Is(err, errNoFile) {
		return nil // not there: nothing to reuse
	} else if err != nil {
		return err
	}
	defer release()
	return lv.StorageVolDelete(v, 0)
}
