package hv

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Pool is a storage pool.
type Pool struct {
	Name       string `json:"name"`
	UUID       string `json:"uuid"`
	Type       string `json:"type"`             // dir, logical, netfs, …
	Path       string `json:"path"`             // target path
	Source     string `json:"source,omitempty"` // host:/export (netfs), the volume group (logical)
	Active     bool   `json:"active"`
	Autostart  bool   `json:"autostart"`
	Capacity   uint64 `json:"capacity"` // bytes
	Allocation uint64 `json:"allocation"`
	Available  uint64 `json:"available"`
}

// Volume is a storage volume: a disk image, an ISO, …
type Volume struct {
	Pool       string `json:"pool"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Type       string `json:"type"`   // file, block, dir, …
	Format     string `json:"format"` // qcow2, raw, iso, …
	Capacity   uint64 `json:"capacity"`
	Allocation uint64 `json:"allocation"`
}

// IsISO reports whether the volume is installation media.
func (v Volume) IsISO() bool {
	return v.Format == "iso" || strings.HasSuffix(strings.ToLower(v.Name), ".iso")
}

// Pools lists the storage pools, sorted by name.
func Pools(ctx context.Context, s *conn.Session) ([]Pool, error) {
	var out []Pool
	err := s.Do(ctx, func(lv *libvirt.Libvirt) error {
		pools, _, err := lv.ConnectListAllStoragePools(1, 0)
		if unsupported(err) {
			return nil // no storage driver on the host
		} else if err != nil {
			return err
		}
		out = make([]Pool, len(pools))
		return each(len(pools), func(i int) error {
			p := pools[i]
			out[i] = Pool{Name: p.Name, UUID: FormatUUID(p.UUID)}
			x, err := lv.StoragePoolGetXMLDesc(p, 0)
			if err != nil {
				return fmt.Errorf("pool %s: %w", p.Name, err)
			}
			var def libvirtxml.StoragePool
			if err := def.Unmarshal(x); err != nil {
				return fmt.Errorf("pool %s: %w", p.Name, err)
			}
			out[i].Type = def.Type
			if def.Target != nil {
				out[i].Path = def.Target.Path
			}
			out[i].Source = poolSource(def)
			state, capacity, alloc, avail, err := lv.StoragePoolGetInfo(p)
			if err != nil {
				return fmt.Errorf("pool %s: %w", p.Name, err)
			}
			out[i].Active = libvirt.StoragePoolState(state) == libvirt.StoragePoolRunning
			out[i].Capacity, out[i].Allocation, out[i].Available = capacity, alloc, avail
			if a, err := lv.StoragePoolGetAutostart(p); err == nil {
				out[i].Autostart = a != 0
			}
			return nil
		})
	})
	if out == nil {
		out = []Pool{}
	}
	slices.SortFunc(out, func(a, b Pool) int { return strings.Compare(a.Name, b.Name) })
	return out, err
}

// poolSource names where a pool's storage comes from, for the pools
// virt-meow adds: an NFS export, a volume group.
func poolSource(def libvirtxml.StoragePool) string {
	src := def.Source
	switch {
	case src == nil:
		return ""
	case def.Type == "netfs" && len(src.Host) > 0 && src.Dir != nil:
		host := src.Host[0].Name
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		return host + ":" + src.Dir.Path
	case def.Type == "logical":
		return src.Name
	}
	return ""
}

// Volumes lists a pool's volumes, sorted by name. The pool is refreshed
// first, so files copied into its directory by other means show up.
func Volumes(ctx context.Context, s *conn.Session, pool string) ([]Volume, error) {
	var out []Volume
	err := s.Do(ctx, func(lv *libvirt.Libvirt) error {
		p, err := lv.StoragePoolLookupByName(pool)
		if err != nil {
			return err
		}
		if active, err := lv.StoragePoolIsActive(p); err != nil {
			return err
		} else if active == 0 {
			return fmt.Errorf("storage pool %s is not active", pool)
		}
		lv.StoragePoolRefresh(p, 0) // best effort: a pool mid-job refuses
		vols, _, err := lv.StoragePoolListAllVolumes(p, 1, 0)
		if err != nil {
			return err
		}
		out = make([]Volume, len(vols))
		return each(len(vols), func(i int) error {
			v, err := volumeOf(lv, vols[i])
			out[i] = v
			return err
		})
	})
	slices.SortFunc(out, func(a, b Volume) int { return strings.Compare(a.Name, b.Name) })
	return out, err
}

func volumeOf(lv *libvirt.Libvirt, v libvirt.StorageVol) (Volume, error) {
	vol := Volume{Pool: v.Pool, Name: v.Name, Path: v.Key}
	x, err := lv.StorageVolGetXMLDesc(v, 0)
	if err != nil {
		return vol, fmt.Errorf("volume %s: %w", v.Name, err)
	}
	var def libvirtxml.StorageVolume
	if err := def.Unmarshal(x); err != nil {
		return vol, fmt.Errorf("volume %s: %w", v.Name, err)
	}
	vol.Type = def.Type
	if def.Target != nil {
		if def.Target.Path != "" {
			vol.Path = def.Target.Path
		}
		if def.Target.Format != nil {
			vol.Format = def.Target.Format.Type
		}
	}
	if def.Capacity != nil {
		vol.Capacity = unitBytes(def.Capacity.Value, def.Capacity.Unit)
	}
	if def.Allocation != nil {
		vol.Allocation = unitBytes(def.Allocation.Value, def.Allocation.Unit)
	}
	return vol, nil
}

// unitBytes converts a libvirt size with its unit to bytes.
func unitBytes(v uint64, unit string) uint64 {
	switch strings.ToLower(unit) {
	case "", "b", "bytes":
		return v
	case "k", "kib":
		return v << 10
	case "m", "mib":
		return v << 20
	case "g", "gib":
		return v << 30
	case "t", "tib":
		return v << 40
	case "kb":
		return v * 1e3
	case "mb":
		return v * 1e6
	case "gb":
		return v * 1e9
	case "tb":
		return v * 1e12
	}
	return v
}

// each runs fn for 0..n-1, eight at a time: go-libvirt multiplexes calls on
// one connection, so a remote host costs a few round trips, not n.
func each(n int, fn func(i int) error) error {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = fn(i)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
