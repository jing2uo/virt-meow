//go:build e2e

package e2e

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// isoWithLabel is the start of an ISO 9660 image whose volume ID is id:
// as much as recognising it reads.
func isoWithLabel(id string) []byte {
	b := make([]byte, 18*2048)
	d := b[16*2048:]
	d[0], d[6] = 1, 1
	copy(d[1:], "CD001")
	copy(d[40:72], id+strings.Repeat(" ", 32))
	b[17*2048] = 255
	copy(b[17*2048+1:], "CD001")
	return b
}

// osinfo over virt-mm serve: the host's own osinfo-db read over SSH (the
// e2e image has Debian's), an ISO recognised by its volume ID and a cloud
// image by its name, with what the form then proposes.
func TestServeOSInfo(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-os-web")
	sv := serve(t)

	var l app.OSList
	sv.call(&l, "OSVariants", sv.conn)
	var deb *hv.OSVariant
	for i, v := range l.Variants {
		if v.ShortID == "debian12" {
			deb = &l.Variants[i]
		}
	}
	if l.Source != "host" || l.Version == "" || l.Note != "" || len(l.Variants) < 500 || deb == nil {
		t.Fatalf("OSVariants: %s %s %q, %d OSes, debian12 %v", l.Source, l.Version, l.Note, len(l.Variants), deb)
	}
	// TCG and the container's OVMF: UEFI; Debian has virtio drivers.
	if p := deb.Propose; p.OS != "linux" || p.MemoryMiB != 2048 || p.DiskGiB != 20 || p.Firmware != "uefi" || p.DiskBus != "virtio" {
		t.Errorf("debian12 proposes %+v", p)
	}

	iso := isoWithLabel("Debian 12.5.0 amd64 n")
	if _, err := hv.Upload(ctx(t), s, "e2e-os-web", "netinst.iso", bytes.NewReader(iso), int64(len(iso)), nil); err != nil {
		t.Fatal(err)
	}
	var d hv.Detected
	sv.call(&d, "DetectOS", sv.conn, "e2e-os-web", "netinst.iso")
	if d.By != "volume-id" || d.VolumeID != "Debian 12.5.0 amd64 n" || d.Variant == nil || d.Variant.ShortID != "debian12" || d.Source != "host" {
		t.Errorf("DetectOS of the ISO: %+v", d)
	}
	if _, err := hv.CreateVolume(ctx(t), s, "e2e-os-web", "jammy-server-cloudimg-amd64.img", 16<<20, "qcow2"); err != nil {
		t.Fatal(err)
	}
	sv.call(&d, "DetectOS", sv.conn, "e2e-os-web", "jammy-server-cloudimg-amd64.img")
	if d.By != "name" || d.Variant == nil || d.Variant.ShortID != "ubuntu22.04" {
		t.Errorf("DetectOS of the image: %+v", d)
	}
	if _, err := hv.CreateVolume(ctx(t), s, "e2e-os-web", "mydisk.qcow2", 16<<20, "qcow2"); err != nil {
		t.Fatal(err)
	}
	sv.call(&d, "DetectOS", sv.conn, "e2e-os-web", "mydisk.qcow2")
	if d.Variant != nil {
		t.Errorf("a disk of no known OS: %+v", d.Variant)
	}
	if err := sv.try(nil, "DetectOS", sv.conn, "e2e-os-web", "nothing.iso"); err == nil {
		t.Error("DetectOS of no volume")
	}

	// The form sends the variant's ID; it ends up in the definition.
	spec := map[string]any{"name": "e2e-os-web", "os": "linux", "vcpus": 1, "memory_mib": 256,
		"disk_pool": "e2e-os-web", "disk_gib": 1, "iso_pool": "e2e-os-web", "iso_volume": "netinst.iso",
		"net_kind": "none", "firmware": "bios", "os_variant": deb.ID}
	var x string
	sv.call(&x, "PreviewMachine", sv.conn, spec)
	if !strings.Contains(x, `<libosinfo:os id="http://debian.org/debian/12"/>`) {
		t.Errorf("no osinfo metadata:\n%s", x)
	}
}

// virt-mm osinfo and virt-mm create --os-variant, from the CLI.
func TestOSInfoCLI(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-os")
	iso := isoWithLabel("CCCOMA_X64FRE_EN-US_DV9")
	if _, err := hv.Upload(ctx(t), s, "e2e-os", "win.iso", bytes.NewReader(iso), int64(len(iso)), nil); err != nil {
		t.Fatal(err)
	}
	var l app.OSList
	cli(t, &l, false, "osinfo", "ubuntu24")
	if len(l.Variants) == 0 || !strings.HasPrefix(l.Variants[0].ShortID, "ubuntu24") {
		t.Errorf("osinfo ubuntu24: %+v", l.Variants)
	}
	var d hv.Detected
	cli(t, &d, false, "osinfo", "--detect", "e2e-os/win.iso")
	if d.Variant == nil || d.Variant.ShortID != "win11" {
		t.Fatalf("--detect: %+v", d)
	}

	// Windows 11 from its ISO: what osinfo-db says, Secure Boot and a TPM.
	var px struct{ XML string }
	cli(t, &px, false, "create", "e2e-os", "--disk", "e2e-os", "--iso", "e2e-os/win.iso", "--os-variant", "auto", "--net", "none", "--print-xml")
	for _, want := range []string{`<libosinfo:os id="http://microsoft.com/win/11"/>`, `<memory unit="MiB">4096</memory>`,
		`firmware="efi"`, `<feature enabled="yes" name="secure-boot"/>`, `<tpm model="tpm-crb">`, `bus="sata"`, `hypervclock`} {
		if !strings.Contains(px.XML, want) {
			t.Errorf("--os-variant auto lacks %s:\n%s", want, px.XML)
		}
	}
	// What is given on the command line stays.
	cli(t, &px, false, "create", "e2e-os", "--disk", "e2e-os:2G", "--os-variant", "debian12", "--memory", "512M", "--firmware", "bios", "--net", "none", "--print-xml")
	if !strings.Contains(px.XML, `<memory unit="MiB">512</memory>`) || strings.Contains(px.XML, `firmware="efi"`) || !strings.Contains(px.XML, "debian.org/debian/12") {
		t.Errorf("--os-variant debian12 with --memory and --firmware:\n%s", px.XML)
	}
	if msg := cli(t, nil, true, "create", "e2e-os", "--disk", "e2e-os:2G", "--os-variant", "nosuchos", "--net", "none"); !strings.Contains(msg, "nosuchos") {
		t.Errorf("an unknown OS: %s", msg)
	}
}
