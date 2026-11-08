package hv

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// ClonedDisk is a disk of a clone: From the original's volume, To its copy.
type ClonedDisk struct {
	Target string `json:"target"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// Cloned is the outcome of Clone.
type Cloned struct {
	Domain Domain       `json:"domain"`
	Disks  []ClonedDisk `json:"disks"`
	Shared []string     `json:"shared,omitempty"` // CD/DVD media and read-only or shareable disks, not copied
	NVRAM  string       `json:"nvram,omitempty"`  // the clone's copy of the UEFI variables
	Notes  []string     `json:"notes,omitempty"`
}

// Clone copies a machine that is off into a new one named name: each of
// its disks into a new volume next to the original (StorageVolCreateXMLFrom,
// so it works over SSH), with a new UUID and new MACs, and its UEFI
// variables (boot entries, enrolled keys) copied. Its TPM is a new one: a
// TPM's keys are its identity, which two machines must not share. Snapshots
// aren't copied, nor is autostart. Whatever fails undoes the copies made so
// far.
func Clone(ctx context.Context, sess *conn.Session, uuid, name string) (Cloned, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return Cloned{}, err
	}
	if !nameRE.MatchString(name) {
		return Cloned{}, fmt.Errorf("machine name %q: letters, digits, '.', '_' and '-', starting with a letter or digit", name)
	}
	out := Cloned{Disks: []ClonedDisk{}}
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		if state, _, err := lv.DomainGetState(dom, 0); err != nil {
			return err
		} else if st := stateOf(libvirt.DomainState(state)); st != ShutOff && st != Crashed {
			return fmt.Errorf("%s is %s: a machine is cloned only when it is off, so that its disks are still", dom.Name, st)
		}
		if saved, err := lv.DomainHasManagedSaveImage(dom, 0); err == nil && saved != 0 {
			return fmt.Errorf("%s has a saved state: start it and shut it down first, so that its disks hold all of it", dom.Name)
		}
		if _, err := lv.DomainLookupByName(name); err == nil {
			return fmt.Errorf("a machine named %q already exists", name)
		}
		x, err := lv.DomainGetXMLDesc(dom, defFlags)
		if err != nil {
			return err
		}
		if err := lossless(x); err != nil {
			return err
		}
		var def libvirtxml.Domain
		if err := def.Unmarshal(x); err != nil {
			return err
		}
		old := def.Name

		var undo []func()
		fail := func(err error) error {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
			return err
		}

		def.Name, def.UUID = name, ""
		if def.GenID != nil {
			def.GenID.Value = "" // libvirt makes a new one
		}
		for i := range def.SysInfo {
			if s := def.SysInfo[i].SMBIOS; s != nil && s.System != nil {
				// libvirt refuses an SMBIOS UUID other than the machine's.
				e := s.System.Entry[:0]
				for _, en := range s.System.Entry {
					if en.Name != "uuid" {
						e = append(e, en)
					}
				}
				s.System.Entry = e
			}
		}
		if def.Devices != nil {
			dev := def.Devices
			for i := range dev.Interfaces {
				dev.Interfaces[i].MAC = nil    // libvirt makes new ones
				dev.Interfaces[i].Target = nil // a fixed tap name would clash
			}
			for i := range dev.Graphics {
				if g := dev.Graphics[i].VNC; g != nil && g.Port > 0 && g.AutoPort != "yes" {
					out.Notes = append(out.Notes, fmt.Sprintf("VNC on any free port (%s has port %d)", old, g.Port))
					g.Port, g.AutoPort = 0, "yes"
				}
				if g := dev.Graphics[i].Spice; g != nil && g.Port > 0 && g.AutoPort != "yes" {
					g.Port, g.TLSPort, g.AutoPort = 0, 0, "yes"
					out.Notes = append(out.Notes, "SPICE on any free port")
				}
			}
			for i := range dev.TPMs {
				if b := dev.TPMs[i].Backend; b != nil && b.Emulator != nil {
					b.Emulator.Source = nil // its own state, in libvirt's place for the new UUID
					out.Notes = append(out.Notes, "a new TPM: the original's keys are its identity and aren't copied "+
						"(what was sealed to it, a BitLocker or LUKS key, needs its recovery key in the clone)")
				}
			}
			for i := range dev.Disks {
				d := &dev.Disks[i]
				target := ""
				if d.Target != nil {
					target = d.Target.Dev
				}
				src := diskSource(*d)
				if src == "" {
					continue
				}
				if d.Device == "cdrom" || d.Device == "floppy" || d.ReadOnly != nil || d.Shareable != nil {
					out.Shared = append(out.Shared, src)
					continue
				}
				vol, pool, release, err := cloneSource(lv, *d)
				if err != nil {
					return fail(fmt.Errorf("%s (%s): %w", target, src, err))
				}
				cv, err := cloneVolume(lv, pool, vol, volumeCopyName(vol.Name, old, name))
				release()
				if err != nil {
					return fail(fmt.Errorf("copying %s: %w", src, err))
				}
				undo = append(undo, func() { deleteFile(lv, cv.Path) })
				to := cv.Path
				switch {
				case d.Source.Volume != nil:
					d.Source.Volume.Volume = cv.Name
					to = cv.Pool + "/" + cv.Name
				case d.Source.File != nil:
					d.Source.File.File = cv.Path
				case d.Source.Block != nil:
					d.Source.Block.Dev = cv.Path
				}
				d.BackingStore = nil // the copy is whole
				out.Disks = append(out.Disks, ClonedDisk{Target: target, From: src, To: to})
			}
		}

		if o := def.OS; o != nil && o.NVRam != nil && o.NVRam.NVRam != "" {
			from := o.NVRam.NVRam
			to := path.Join(path.Dir(from), name+"_VARS"+path.Ext(from))
			vol, pool, release, err := fileVolume(lv, from)
			switch {
			case errors.Is(err, errNoFile):
				// Never started: libvirt makes it from the template.
				o.NVRam.NVRam = to
			case err != nil:
				return fail(fmt.Errorf("the NVRAM %s: %w", from, err))
			default:
				cv, err := cloneVolume(lv, pool, vol, path.Base(to))
				release()
				if err != nil {
					return fail(fmt.Errorf("copying the NVRAM %s: %w", from, err))
				}
				undo = append(undo, func() { deleteFile(lv, cv.Path) })
				o.NVRam.NVRam, out.NVRAM = cv.Path, cv.Path
			}
		}
		if snaps, _, err := lv.DomainListAllSnapshots(dom, 1, 0); err == nil && len(snaps) > 0 {
			out.Notes = append(out.Notes, fmt.Sprintf("%d snapshots not copied: the clone starts from %s's current state", len(snaps), old))
		}

		nx, err := def.Marshal()
		if err != nil {
			return fail(err)
		}
		nd, err := lv.DomainDefineXMLFlags(nx, libvirt.DomainDefineValidate)
		if err != nil {
			return fail(fmt.Errorf("libvirt refused the clone's definition: %w", err))
		}
		out.Domain = Domain{UUID: FormatUUID(nd.UUID), Name: nd.Name, ID: -1, State: ShutOff, Persistent: true}
		if _, maxMem, mem, vcpus, _, err := lv.DomainGetInfo(nd); err == nil {
			out.Domain.VCPUs, out.Domain.MemoryKiB, out.Domain.MaxMemKiB = int(vcpus), mem, maxMem
		}
		return nil
	})
	return out, err
}

// volumeCopyName names the copy of a volume of machine old for machine
// name: old's name in it replaced (old.qcow2 → name.qcow2), else name in
// front (data.qcow2 → name-data.qcow2).
func volumeCopyName(vol, old, name string) string {
	if strings.Contains(vol, old) {
		return strings.Replace(vol, old, name, 1)
	}
	return name + "-" + vol
}

// cloneVolume copies vol into a new volume of pool named like want, with a
// number added before its extension while that name is taken.
func cloneVolume(lv *libvirt.Libvirt, pool libvirt.StoragePool, vol libvirt.StorageVol, want string) (Volume, error) {
	src, err := volumeOf(lv, vol)
	if err != nil {
		return Volume{}, err
	}
	name := want
	ext := path.Ext(want)
	for n := 2; ; n++ {
		if _, err := lv.StorageVolLookupByName(pool, name); err != nil {
			break
		}
		if n > 99 {
			return Volume{}, fmt.Errorf("no free name like %s", want)
		}
		name = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(want, ext), n, ext)
	}
	vd := libvirtxml.StorageVolume{Name: name, Capacity: &libvirtxml.StorageVolumeSize{Unit: "B", Value: src.Capacity}}
	if src.Format != "" {
		vd.Target = &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: src.Format}}
	}
	x, err := vd.Marshal()
	if err != nil {
		return Volume{}, err
	}
	cp, err := lv.StorageVolCreateXMLFrom(pool, x, vol, 0)
	if err != nil {
		return Volume{}, err
	}
	return volumeOf(lv, cp)
}

// cloneSource is the storage volume a disk is, and its pool, to copy. release ends a
// pool made for the moment to reach it.
func cloneSource(lv *libvirt.Libvirt, d libvirtxml.DomainDisk) (libvirt.StorageVol, libvirt.StoragePool, func(), error) {
	s := d.Source
	switch {
	case s.Volume != nil:
		p, err := lv.StoragePoolLookupByName(s.Volume.Pool)
		if err != nil {
			return libvirt.StorageVol{}, p, nil, fmt.Errorf("no pool %s", s.Volume.Pool)
		}
		v, err := lv.StorageVolLookupByName(p, s.Volume.Volume)
		if err != nil {
			return v, p, nil, fmt.Errorf("pool %s has no volume %s", s.Volume.Pool, s.Volume.Volume)
		}
		return v, p, func() {}, nil
	case s.File != nil:
		v, p, release, err := fileVolume(lv, s.File.File)
		if errors.Is(err, errNoFile) {
			return v, p, nil, errors.New("the file isn't there")
		}
		return v, p, release, err
	case s.Block != nil:
		v, err := lv.StorageVolLookupByPath(s.Block.Dev)
		if err != nil {
			return v, libvirt.StoragePool{}, nil, errors.New("a block device in no storage pool: virt-meow can't copy it")
		}
		p, err := lv.StoragePoolLookupByVolume(v)
		return v, p, func() {}, err
	}
	return libvirt.StorageVol{}, libvirt.StoragePool{}, nil, errors.New("a network disk: virt-meow can't copy it")
}

var errNoFile = errors.New("no such file")

// fileVolume reaches a file on the host through libvirt's storage driver,
// so that it works the same over SSH and needs no shell: as a volume of the
// pool that holds it, or of a transient pool made on its directory, which
// release ends (the directory stays). errNoFile when it isn't there.
func fileVolume(lv *libvirt.Libvirt, file string) (libvirt.StorageVol, libvirt.StoragePool, func(), error) {
	if v, err := lv.StorageVolLookupByPath(file); err == nil {
		p, err := lv.StoragePoolLookupByVolume(v)
		return v, p, func() {}, err
	}
	b := make([]byte, 4)
	rand.Read(b)
	x, err := (&libvirtxml.StoragePool{
		Type:   "dir",
		Name:   fmt.Sprintf("virt-mm-tmp-%x", b),
		Target: &libvirtxml.StoragePoolTarget{Path: path.Dir(file)},
	}).Marshal()
	if err != nil {
		return libvirt.StorageVol{}, libvirt.StoragePool{}, nil, err
	}
	p, err := lv.StoragePoolCreateXML(x, 0)
	if err != nil {
		return libvirt.StorageVol{}, p, nil, fmt.Errorf("reaching %s through a storage pool: %w", path.Dir(file), err)
	}
	release := func() { lv.StoragePoolDestroy(p) }
	v, err := lv.StorageVolLookupByName(p, path.Base(file))
	if err != nil {
		release()
		return v, p, nil, errNoFile
	}
	return v, p, release, nil
}
