package hv

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Filesystem is a folder of the host a machine sees: virtiofs (or
// another driver virt-meow only shows), by its tag.
type Filesystem struct {
	Tag      string `json:"tag"`    // what the guest mounts: mount -t virtiofs <tag> /mnt
	Source   string `json:"source"` // the host's directory
	Driver   string `json:"driver"` // virtiofs, path (9p), …
	ReadOnly bool   `json:"readonly,omitempty"`
}

// maxTag is the longest virtiofs tag the device takes.
const maxTag = 36

func filesystems(d *libvirtxml.Domain) []Filesystem {
	out := []Filesystem{}
	if d.Devices == nil {
		return out
	}
	for _, f := range d.Devices.Filesystems {
		fs := Filesystem{Driver: "path", ReadOnly: f.ReadOnly != nil}
		if f.Driver != nil && f.Driver.Type != "" {
			fs.Driver = f.Driver.Type
		}
		if f.Target != nil {
			fs.Tag = f.Target.Dir
		}
		if f.Source != nil && f.Source.Mount != nil {
			fs.Source = f.Source.Mount.Dir
		}
		out = append(out, fs)
	}
	return out
}

// sharedMemory says whether the machine's memory is shared with other
// processes, which virtiofsd needs to reach the guest's buffers.
func sharedMemory(d *libvirtxml.Domain) bool {
	mb := d.MemoryBacking
	return mb != nil && mb.MemoryAccess != nil && mb.MemoryAccess.Mode == "shared"
}

// AddFilesystem shares the host's directory dir with the machine as a
// virtiofs folder named tag, from its next start (virtiofs can't be
// hot-plugged). virtiofsd needs the guest's memory shared: when it isn't,
// it is set up too (memfd, keeping hugepages if the machine has them),
// and the outcome says so. The directory must exist on the host when the
// machine starts; the guest mounts it with mount -t virtiofs <tag> <dir>.
func AddFilesystem(ctx context.Context, sess *conn.Session, uuid, dir, tag string) (Edited, error) {
	dir = strings.TrimSpace(dir)
	switch {
	case !path.IsAbs(dir) || strings.ContainsAny(dir, "\x00\n"):
		return Edited{}, fmt.Errorf("host directory %q: an absolute path on the host", dir)
	case tag == "" || len(tag) > maxTag || strings.ContainsAny(tag, " \t\n\x00,/"):
		return Edited{}, fmt.Errorf("tag %q: up to %d characters, no spaces, ',' or '/'", tag, maxTag)
	}
	dir = path.Clean(dir)
	return modify(ctx, sess, uuid, func(e *edit) error {
		for _, f := range filesystems(e.def) {
			if f.Tag == tag {
				return fmt.Errorf("%s already has a shared folder tagged %s (%s)", e.def.Name, tag, f.Source)
			}
		}
		dc, err := domainCaps(e.lv, e.def)
		if err != nil {
			return err
		}
		if fs := dc.Devices; fs == nil || fs.FileSystem == nil || fs.FileSystem.Supported != "yes" ||
			!slices.Contains(enum(fs.FileSystem.Enums, "driverType"), "virtiofs") {
			return fmt.Errorf("the host's QEMU has no virtiofs for %s", e.def.Name)
		}
		if !sharedMemory(e.def) {
			mb := e.def.MemoryBacking
			if mb == nil {
				mb = &libvirtxml.DomainMemoryBacking{}
				e.def.MemoryBacking = mb
			}
			if mb.MemoryHugePages == nil && mb.MemorySource == nil {
				mb.MemorySource = &libvirtxml.DomainMemorySource{Type: "memfd"}
			}
			mb.MemoryAccess = &libvirtxml.DomainMemoryAccess{Mode: "shared"}
			e.changed("memory shared with the host's virtiofsd (memfd)", nil)
		}
		if e.def.Devices == nil {
			e.def.Devices = &libvirtxml.DomainDeviceList{}
		}
		e.def.Devices.Filesystems = append(e.def.Devices.Filesystems, libvirtxml.DomainFilesystem{
			AccessMode: "passthrough",
			Driver:     &libvirtxml.DomainFilesystemDriver{Type: "virtiofs"},
			Source:     &libvirtxml.DomainFilesystemSource{Mount: &libvirtxml.DomainFilesystemSourceMount{Dir: dir}},
			Target:     &libvirtxml.DomainFilesystemTarget{Dir: tag},
		})
		e.changed(fmt.Sprintf("shared folder %s → %s (virtiofs)", dir, tag), nil)
		return nil
	})
}

// RemoveFilesystem stops sharing the folder tagged tag, from the
// machine's next start. The shared memory stays: other devices (or the
// user) may want it, and it costs nothing.
func RemoveFilesystem(ctx context.Context, sess *conn.Session, uuid, tag string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		if e.def.Devices != nil {
			for i, f := range filesystems(e.def) {
				if f.Tag == tag {
					e.def.Devices.Filesystems = slices.Delete(e.def.Devices.Filesystems, i, i+1)
					e.changed(fmt.Sprintf("shared folder %s → %s removed", f.Source, tag), nil)
					return nil
				}
			}
		}
		return fmt.Errorf("%s has no shared folder tagged %s", e.def.Name, tag)
	})
}
