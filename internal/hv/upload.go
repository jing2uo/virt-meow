package hv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Upload copies size bytes from r into a new volume name in pool, through
// libvirt's stream (so it works over SSH and on any pool type). progress,
// if not nil, is called with the bytes sent so far. A failed or cancelled
// upload removes the volume it made. ctx should carry no short deadline:
// an ISO takes minutes.
func Upload(ctx context.Context, s *conn.Session, pool, name string, r io.Reader, size int64, progress func(sent int64)) (Volume, error) {
	if name == "" || strings.ContainsAny(name, "/\x00") {
		return Volume{}, fmt.Errorf("volume name %q", name)
	}
	// An ISO is raw bytes too: created as "iso", libvirt would want
	// qemu-img to make it. A pool refresh reports what the file really is.
	format := "raw"
	if strings.HasSuffix(strings.ToLower(name), ".qcow2") {
		format = "qcow2"
	}
	var vol Volume
	err := s.Do(ctx, func(lv *libvirt.Libvirt) error {
		p, err := lv.StoragePoolLookupByName(pool)
		if err != nil {
			return fmt.Errorf("pool %s: %w", pool, err)
		}
		if _, err := lv.StorageVolLookupByName(p, name); err == nil {
			return fmt.Errorf("pool %s already has a volume %s", pool, name)
		}
		vx, err := (&libvirtxml.StorageVolume{
			Name:       name,
			Capacity:   &libvirtxml.StorageVolumeSize{Unit: "B", Value: uint64(size)},
			Allocation: &libvirtxml.StorageVolumeSize{Unit: "B", Value: 0},
			Target:     &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: format}},
		}).Marshal()
		if err != nil {
			return err
		}
		v, err := lv.StorageVolCreateXML(p, vx, 0)
		if err != nil {
			return fmt.Errorf("creating %s: %w", name, err)
		}
		cr := &countingReader{r: r, progress: progress}
		if err := lv.StorageVolUpload(v, cr, 0, uint64(size), 0); err != nil {
			lv.StorageVolDelete(v, 0)
			return fmt.Errorf("uploading %s: %w", name, err)
		}
		if n := cr.n.Load(); n != size {
			lv.StorageVolDelete(v, 0)
			return fmt.Errorf("uploading %s: sent %d of %d bytes", name, n, size)
		}
		vol, err = volumeOf(lv, v)
		return err
	})
	return vol, err
}

type countingReader struct {
	r        io.Reader
	n        atomic.Int64
	progress func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		sent := c.n.Add(int64(n))
		if c.progress != nil {
			c.progress(sent)
		}
	}
	return n, err
}

// uploadBytes makes a new raw volume of pool named name holding b, as
// Upload does, on a connection already in hand.
func uploadBytes(lv *libvirt.Libvirt, pool, name string, b []byte) (libvirt.StorageVol, error) {
	p, err := lv.StoragePoolLookupByName(pool)
	if err != nil {
		return libvirt.StorageVol{}, fmt.Errorf("pool %s: %w", pool, err)
	}
	vx, err := (&libvirtxml.StorageVolume{
		Name:       name,
		Capacity:   &libvirtxml.StorageVolumeSize{Unit: "B", Value: uint64(len(b))},
		Allocation: &libvirtxml.StorageVolumeSize{Unit: "B", Value: 0},
		Target:     &libvirtxml.StorageVolumeTarget{Format: &libvirtxml.StorageVolumeTargetFormat{Type: "raw"}},
	}).Marshal()
	if err != nil {
		return libvirt.StorageVol{}, err
	}
	v, err := lv.StorageVolCreateXML(p, vx, 0)
	if err != nil {
		return v, fmt.Errorf("creating %s: %w", name, err)
	}
	if err := lv.StorageVolUpload(v, bytes.NewReader(b), 0, uint64(len(b)), 0); err != nil {
		lv.StorageVolDelete(v, 0)
		return v, fmt.Errorf("uploading %s: %w", name, err)
	}
	return v, nil
}
