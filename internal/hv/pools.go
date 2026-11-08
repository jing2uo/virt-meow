package hv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// NewPool is a storage pool to add: a directory on the host (Type dir,
// Path), an LVM volume group that exists on it (Type logical, Source: its
// name, Name's when empty), or an NFS export the host mounts (Type netfs,
// Source host:/export, Path the mount point, by default
// /var/lib/libvirt/images/<Name>).
type NewPool struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Path      string `json:"path,omitempty"`
	Source    string `json:"source,omitempty"`
	Autostart bool   `json:"autostart"`
	// NoStart defines the pool without starting it (virsh pool-define-as):
	// for an NFS server that isn't up yet, say.
	NoStart bool `json:"no_start,omitempty"`
}

// nfsSource splits an NFS source, host:/export or [IPv6]:/export.
func nfsSource(src string) (host, dir string, err error) {
	i := strings.Index(src, ":/")
	if i < 1 {
		return "", "", fmt.Errorf("NFS source %q: want host:/export", src)
	}
	host, dir = src[:i], path.Clean(src[i+1:])
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" || strings.ContainsAny(host, " /[]\t") {
		return "", "", fmt.Errorf("NFS source %q: %q is not a host", src, host)
	}
	return host, dir, nil
}

// AddPool defines a storage pool, builds it (a directory or mount point is
// made when missing; a volume group is never made, as that would wipe the
// disks under it) and, unless NoStart, starts it (an NFS pool mounts its
// export). A pool that doesn't start is not kept.
func AddPool(ctx context.Context, sess *conn.Session, np NewPool) (Pool, error) {
	if !nameRE.MatchString(np.Name) {
		return Pool{}, fmt.Errorf("pool name %q: letters, digits, '.', '_' and '-', starting with a letter or digit", np.Name)
	}
	def := libvirtxml.StoragePool{Name: np.Name, Type: np.Type}
	switch np.Type {
	case "dir":
		if !path.IsAbs(np.Path) || path.Clean(np.Path) == "/" {
			return Pool{}, fmt.Errorf("a directory pool needs the absolute path of a directory on the host (--path)")
		}
		def.Target = &libvirtxml.StoragePoolTarget{Path: path.Clean(np.Path)}
	case "logical":
		vg := cmpOr(np.Source, np.Name)
		if !nameRE.MatchString(vg) {
			return Pool{}, fmt.Errorf("volume group %q", vg)
		}
		def.Source = &libvirtxml.StoragePoolSource{Name: vg}
		def.Target = &libvirtxml.StoragePoolTarget{Path: "/dev/" + vg}
	case "netfs":
		host, dir, err := nfsSource(np.Source)
		if err != nil {
			return Pool{}, err
		}
		target := cmpOr(np.Path, "/var/lib/libvirt/images/"+np.Name)
		if !path.IsAbs(target) || path.Clean(target) == "/" {
			return Pool{}, fmt.Errorf("an NFS pool's mount point is an absolute path on the host (--path)")
		}
		def.Source = &libvirtxml.StoragePoolSource{Host: []libvirtxml.StoragePoolSourceHost{{Name: host}},
			Dir: &libvirtxml.StoragePoolSourceDir{Path: dir}, Format: &libvirtxml.StoragePoolSourceFormat{Type: "nfs"}}
		def.Target = &libvirtxml.StoragePoolTarget{Path: path.Clean(target)}
	default:
		return Pool{}, fmt.Errorf("pool type %q: dir, logical or netfs", np.Type)
	}
	x, err := def.Marshal()
	if err != nil {
		return Pool{}, err
	}
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		if _, err := lv.StoragePoolLookupByName(np.Name); err == nil {
			return fmt.Errorf("a pool named %q already exists", np.Name)
		}
		p, err := lv.StoragePoolDefineXML(x, 0)
		if err != nil {
			return fmt.Errorf("libvirt refused the pool: %w", err)
		}
		undo := func(cause error) error {
			if err := lv.StoragePoolUndefine(p); err != nil {
				return fmt.Errorf("%w (and removing the pool's definition failed: %v)", cause, err)
			}
			return cause
		}
		if np.Type == "dir" || np.Type == "netfs" {
			// Makes the directory (the mount point) if it is missing; one
			// that exists is kept.
			if err := lv.StoragePoolBuild(p, libvirt.StoragePoolBuildNew); err != nil {
				return undo(fmt.Errorf("making %s: %w", def.Target.Path, err))
			}
		}
		if !np.NoStart {
			if err := lv.StoragePoolCreate(p, 0); err != nil {
				return undo(fmt.Errorf("starting the pool: %w", err))
			}
		}
		if np.Autostart {
			if err := lv.StoragePoolSetAutostart(p, 1); err != nil {
				return fmt.Errorf("autostart: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Pool{}, err
	}
	return poolNamed(ctx, sess, np.Name)
}

func poolNamed(ctx context.Context, sess *conn.Session, name string) (Pool, error) {
	ps, err := Pools(ctx, sess)
	if err != nil {
		return Pool{}, err
	}
	for _, p := range ps {
		if p.Name == name {
			return p, nil
		}
	}
	return Pool{}, fmt.Errorf("no pool %s", name)
}

// PoolAction is what SetPool does to a pool.
type PoolAction string

const (
	PoolStart        PoolAction = "start"
	PoolStop         PoolAction = "stop"
	PoolAutostartOn  PoolAction = "autostart-on"
	PoolAutostartOff PoolAction = "autostart-off"
)

// SetPool starts or stops a pool, or turns its autostart on or off. A
// stopped pool's volumes stay where they are; machines using them can't
// start until it runs again.
func SetPool(ctx context.Context, sess *conn.Session, name string, a PoolAction) (Pool, error) {
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		p, err := lv.StoragePoolLookupByName(name)
		if err != nil {
			return fmt.Errorf("no pool %s", name)
		}
		active, err := lv.StoragePoolIsActive(p)
		if err != nil {
			return err
		}
		switch a {
		case PoolStart:
			if active == 0 {
				return lv.StoragePoolCreate(p, 0)
			}
		case PoolStop:
			if active != 0 {
				return lv.StoragePoolDestroy(p)
			}
		case PoolAutostartOn:
			return lv.StoragePoolSetAutostart(p, 1)
		case PoolAutostartOff:
			return lv.StoragePoolSetAutostart(p, 0)
		default:
			return fmt.Errorf("pool action %q", a)
		}
		return nil
	})
	if err != nil {
		return Pool{}, err
	}
	return poolNamed(ctx, sess, name)
}

// RemovePool stops a pool and forgets it. What it holds stays on the host
// (the directory and its files, the volume group): adding the pool again
// finds it.
func RemovePool(ctx context.Context, sess *conn.Session, name string) error {
	return sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		p, err := lv.StoragePoolLookupByName(name)
		if err != nil {
			return fmt.Errorf("no pool %s", name)
		}
		if active, err := lv.StoragePoolIsActive(p); err != nil {
			return err
		} else if active != 0 {
			if err := lv.StoragePoolDestroy(p); err != nil {
				return fmt.Errorf("stopping it: %w", err)
			}
		}
		if persistent, err := lv.StoragePoolIsPersistent(p); err == nil && persistent == 0 {
			return nil // transient: stopping it was the end of it
		}
		return lv.StoragePoolUndefine(p)
	})
}

// volume finds a volume in an active pool.
func volume(lv *libvirt.Libvirt, pool, name string) (libvirt.StorageVol, error) {
	p, err := lv.StoragePoolLookupByName(pool)
	if err != nil {
		return libvirt.StorageVol{}, fmt.Errorf("no pool %s", pool)
	}
	v, err := lv.StorageVolLookupByName(p, name)
	if err != nil {
		return libvirt.StorageVol{}, fmt.Errorf("pool %s has no volume %s", pool, name)
	}
	return v, nil
}

// CreateVolume makes a new empty volume of size bytes: qcow2 by default in
// a directory pool, raw (a logical volume) in a volume group.
func CreateVolume(ctx context.Context, sess *conn.Session, pool, name string, size uint64, format string) (Volume, error) {
	if name == "" || strings.ContainsAny(name, "/\x00") || strings.HasPrefix(name, ".") {
		return Volume{}, fmt.Errorf("volume name %q", name)
	}
	if size < 1<<20 {
		return Volume{}, errors.New("volume size: at least 1 MiB")
	}
	var out Volume
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		p, err := lv.StoragePoolLookupByName(pool)
		if err != nil {
			return fmt.Errorf("no pool %s", pool)
		}
		x, err := lv.StoragePoolGetXMLDesc(p, 0)
		if err != nil {
			return err
		}
		var pd libvirtxml.StoragePool
		if err := pd.Unmarshal(x); err != nil {
			return err
		}
		vd := libvirtxml.StorageVolume{Name: name, Capacity: &libvirtxml.StorageVolumeSize{Unit: "B", Value: size}}
		var flags libvirt.StorageVolCreateFlags
		switch pd.Type {
		case "dir", "fs", "netfs":
			format = cmpOr(format, "qcow2")
			if format != "qcow2" && format != "raw" {
				return fmt.Errorf("format %q: qcow2 or raw", format)
			}
			vd.Target = &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: format}}
			if format == "qcow2" {
				flags = libvirt.StorageVolCreatePreallocMetadata
			} else {
				vd.Allocation = &libvirtxml.StorageVolumeSize{Unit: "B", Value: 0} // sparse
			}
		default:
			if format != "" && format != "raw" {
				return fmt.Errorf("a %s pool holds raw volumes only", pd.Type)
			}
		}
		if _, err := lv.StorageVolLookupByName(p, name); err == nil {
			return fmt.Errorf("pool %s already has a volume %s", pool, name)
		}
		vx, err := vd.Marshal()
		if err != nil {
			return err
		}
		v, err := lv.StorageVolCreateXML(p, vx, flags)
		if err != nil {
			return fmt.Errorf("creating %s: %w", name, err)
		}
		out, err = volumeOf(lv, v)
		return err
	})
	return out, err
}

// volumeUsers names the machines whose disks are the volume v: running
// ones as they run, the others as defined.
func volumeUsers(lv *libvirt.Libvirt, v libvirt.StorageVol, running bool) ([]string, error) {
	vol, err := volumeOf(lv, v)
	if err != nil {
		return nil, err
	}
	flags := libvirt.ConnectListAllDomainsFlags(0)
	if running {
		flags = libvirt.ConnectListDomainsActive
	}
	doms, _, err := lv.ConnectListAllDomains(1, flags)
	if err != nil {
		return nil, err
	}
	users := make([]string, len(doms))
	err = each(len(doms), func(i int) error {
		xf := libvirt.DomainXMLFlags(0)
		if !running {
			xf = libvirt.DomainXMLInactive
		}
		x, err := lv.DomainGetXMLDesc(doms[i], xf)
		if err != nil {
			return nil // gone meanwhile
		}
		var d libvirtxml.Domain
		if d.Unmarshal(x) != nil || d.Devices == nil {
			return nil
		}
		for _, dk := range d.Devices.Disks {
			if src := diskSource(dk); src == vol.Pool+"/"+vol.Name || src == vol.Path {
				users[i] = doms[i].Name
				return nil
			}
		}
		return nil
	})
	var out []string
	for _, u := range users {
		if u != "" {
			out = append(out, u)
		}
	}
	return out, err
}

// ResizeVolume grows a volume to size bytes. One a running machine has as a
// disk is grown through the machine instead (virt-mm disk resize), which
// tells the guest.
func ResizeVolume(ctx context.Context, sess *conn.Session, pool, name string, size uint64) (Volume, error) {
	var out Volume
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		v, err := volume(lv, pool, name)
		if err != nil {
			return err
		}
		_, have, _, err := lv.StorageVolGetInfo(v)
		if err != nil {
			return err
		}
		switch {
		case size < have:
			return fmt.Errorf("%s/%s is %s: shrinking loses what is past the new end, so virt-meow only grows volumes", pool, name, bytesText(have))
		case size > have:
			users, err := volumeUsers(lv, v, true)
			if err != nil {
				return err
			}
			if len(users) > 0 {
				return fmt.Errorf("%s is running with %s/%s as a disk: grow it with virt-mm disk resize, which tells the guest", users[0], pool, name)
			}
			if err := lv.StorageVolResize(v, size, 0); err != nil {
				return err
			}
		}
		out, err = volumeOf(lv, v)
		return err
	})
	return out, err
}

// DeleteVolume deletes a volume no machine has as a disk.
func DeleteVolume(ctx context.Context, sess *conn.Session, pool, name string) error {
	return sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		v, err := volume(lv, pool, name)
		if err != nil {
			return err
		}
		for _, running := range []bool{false, true} {
			users, err := volumeUsers(lv, v, running)
			if err != nil {
				return err
			}
			if len(users) > 0 {
				return fmt.Errorf("%s has %s/%s as a disk: remove it there first (virt-mm disk rm)", strings.Join(users, ", "), pool, name)
			}
		}
		return lv.StorageVolDelete(v, 0)
	})
}

// Download copies a volume's bytes into w through libvirt's stream, so it
// works over SSH. progress, if not nil, is called with the bytes so far and
// the volume's size. ctx should carry no short deadline.
func Download(ctx context.Context, sess *conn.Session, pool, name string, w io.Writer, progress func(got, total int64)) (Volume, error) {
	var out Volume
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		v, err := volume(lv, pool, name)
		if err != nil {
			return err
		}
		if out, err = volumeOf(lv, v); err != nil {
			return err
		}
		cw := &countingWriter{w: w}
		if progress != nil {
			total := int64(out.Allocation)
			if out.Format == "raw" || out.Format == "iso" || total == 0 {
				total = int64(out.Capacity)
			}
			cw.progress = func(n int64) { progress(n, total) }
		}
		if err := lv.StorageVolDownload(v, cw, 0, 0, 0); err != nil {
			return fmt.Errorf("downloading %s/%s: %w", pool, name, err)
		}
		return nil
	})
	return out, err
}

type countingWriter struct {
	w        io.Writer
	n        int64
	progress func(int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if c.progress != nil {
		c.progress(c.n)
	}
	return n, err
}
