package hv

import (
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

// A virtiofs folder as AddFilesystem writes it, read back.
func TestFilesystems(t *testing.T) {
	d := libvirtxml.Domain{Devices: &libvirtxml.DomainDeviceList{Filesystems: []libvirtxml.DomainFilesystem{{
		AccessMode: "passthrough",
		Driver:     &libvirtxml.DomainFilesystemDriver{Type: "virtiofs"},
		Source:     &libvirtxml.DomainFilesystemSource{Mount: &libvirtxml.DomainFilesystemSourceMount{Dir: "/srv/share"}},
		Target:     &libvirtxml.DomainFilesystemTarget{Dir: "share"},
	}}}}
	x, err := d.Devices.Filesystems[0].Marshal()
	if err != nil || !strings.Contains(x, `<filesystem type="mount" accessmode="passthrough">`) || !strings.Contains(x, `<source dir="/srv/share">`) {
		t.Fatalf("%s, %v", x, err)
	}
	fs := filesystems(&d)
	if len(fs) != 1 || fs[0] != (Filesystem{Tag: "share", Source: "/srv/share", Driver: "virtiofs"}) {
		t.Errorf("%+v", fs)
	}
	if sharedMemory(&d) {
		t.Error("shared memory without <access mode='shared'/>")
	}
	d.MemoryBacking = &libvirtxml.DomainMemoryBacking{MemoryAccess: &libvirtxml.DomainMemoryAccess{Mode: "shared"}}
	if !sharedMemory(&d) {
		t.Error("shared memory not seen")
	}
}
