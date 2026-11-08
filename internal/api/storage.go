package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// The storage tab: pools and their volumes, as virt-mm pool and virt-mm vol.

func (s *Service) Pools(id string) ([]hv.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	ps, err := s.c.m.Pools(ctx, id)
	if ps == nil {
		ps = []hv.Pool{}
	}
	return ps, err
}

func (s *Service) Volumes(id, pool string) ([]hv.Volume, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	vs, err := s.c.m.Volumes(ctx, id, pool)
	if vs == nil {
		vs = []hv.Volume{}
	}
	return vs, err
}

// AddPool defines a pool and starts it; an NFS pool mounts its export,
// which may take a while.
func (s *Service) AddPool(id string, p hv.NewPool) (hv.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	return s.c.m.AddPool(ctx, id, p)
}

// SetPool starts or stops a pool, or turns its autostart on or off.
func (s *Service) SetPool(id, name string, a hv.PoolAction) (hv.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	return s.c.m.SetPool(ctx, id, name, a)
}

// RemovePool stops and forgets a pool; what it holds stays on the host.
func (s *Service) RemovePool(id, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RemovePool(ctx, id, name)
}

// CreateVolume makes an empty volume of size bytes; format "" is the
// pool's default.
func (s *Service) CreateVolume(id, pool, name string, size uint64, format string) (hv.Volume, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	return s.c.m.CreateVolume(ctx, id, pool, name, size, format)
}

// ResizeVolume grows a volume to size bytes.
func (s *Service) ResizeVolume(id, pool, name string, size uint64) (hv.Volume, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.ResizeVolume(ctx, id, pool, name, size)
}

// DeleteVolume deletes a volume no machine has as a disk.
func (s *Service) DeleteVolume(id, pool, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.DeleteVolume(ctx, id, pool, name)
}

// Download is a volume being copied to this computer, by the window.
type Download struct {
	ID         string `json:"id"`
	Connection string `json:"connection"`
	Pool       string `json:"pool"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Got        int64  `json:"got"`
	Total      int64  `json:"total"`
	State      string `json:"state"` // running, done, failed, cancelled
	Error      string `json:"error,omitempty"`
}

// PickDownload asks where on this computer to save a volume and starts
// copying it there, returning the download's id ("" when no file was
// picked); progress comes as "download" events. Only the window can: a
// browser downloads from serve's GET /api/download instead. The copy goes
// to a file of its own beside the one picked, which it replaces only once
// complete, so a failed or cancelled download leaves that file as it was.
func (s *Service) PickDownload(id, pool, name string) (string, error) {
	c := s.c
	if c.opts.SaveFile == nil {
		return "", errors.New("no save dialog here: the browser downloads the volume itself")
	}
	path, err := c.opts.SaveFile(name)
	if err != nil || path == "" {
		return "", err
	}
	b := make([]byte, 6)
	rand.Read(b)
	d := Download{ID: hex.EncodeToString(b), Connection: id, Pool: pool, Name: name, Path: path, State: "running"}
	ctx, cancel := context.WithCancel(context.Background())
	c.ups.mu.Lock()
	c.ups.cancels[d.ID] = cancel
	c.ups.mu.Unlock()
	c.emit(EvDownload, d)
	go func() {
		defer func() {
			c.ups.mu.Lock()
			delete(c.ups.cancels, d.ID)
			c.ups.mu.Unlock()
			cancel()
		}()
		part := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+d.ID+".part")
		var last time.Time
		_, err := c.download(ctx, id, pool, name, part, func(got, total int64) {
			if time.Since(last) < 200*time.Millisecond {
				return
			}
			last = time.Now()
			p := d
			p.Got, p.Total = got, total
			c.emit(EvDownload, p)
		})
		if err == nil {
			if err = os.Rename(part, path); err != nil {
				os.Remove(part)
			}
		}
		switch {
		case err == nil:
			d.State = "done"
		case ctx.Err() != nil:
			d.State = "cancelled"
		default:
			d.State, d.Error = "failed", err.Error()
		}
		c.emit(EvDownload, d)
	}()
	return d.ID, nil
}

// CancelDownload stops a download; its partial file is removed.
func (s *Service) CancelDownload(downloadID string) { s.CancelUpload(downloadID) }

// DownloadTo copies a volume into w, a browser's response under serve.
func (c *Core) DownloadTo(ctx context.Context, id, pool, name string, w io.Writer) (hv.Volume, error) {
	return c.m.DownloadTo(ctx, id, pool, name, w, nil)
}
