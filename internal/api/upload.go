package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Upload is a file being copied into a storage pool.
type Upload struct {
	ID         string `json:"id"`
	Connection string `json:"connection"`
	Pool       string `json:"pool"`
	Name       string `json:"name"`
	Sent       int64  `json:"sent"`
	Total      int64  `json:"total"`
	State      string `json:"state"` // running, done, failed, cancelled
	Error      string `json:"error,omitempty"`
}

type uploads struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// PickUpload asks for a file on this computer and starts copying it into
// pool, returning the upload's id ("" when no file was picked). Progress
// comes as "upload" events. Only the window can: a browser uploads its file
// to serve instead (internal/web).
func (s *Service) PickUpload(id, pool string) (string, error) {
	c := s.c
	if c.opts.PickFile == nil {
		return "", errors.New("no file picker here: the page uploads the file itself")
	}
	path, err := c.opts.PickFile()
	if err != nil || path == "" {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return "", errors.New(path + " is not a file")
	}
	u, ctx, done := c.startUpload(id, pool, filepath.Base(path), st.Size())
	go func() {
		defer f.Close()
		done(c.copyUpload(ctx, u, f))
	}()
	return u.ID, nil
}

// CancelUpload stops an upload; its partial volume is removed.
func (s *Service) CancelUpload(uploadID string) {
	s.c.ups.mu.Lock()
	cancel := s.c.ups.cancels[uploadID]
	s.c.ups.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// UploadFrom copies size bytes from r into a new volume name of pool, with
// the same "upload" events as PickUpload; it returns when the copy ends. r
// is a browser's request body under serve.
func (c *Core) UploadFrom(ctx context.Context, id, pool, name string, r io.Reader, size int64) error {
	u, uctx, done := c.startUpload(id, pool, name, size)
	stop := context.AfterFunc(ctx, func() { c.svc.CancelUpload(u.ID) })
	defer stop()
	err := c.copyUpload(uctx, u, r)
	done(err)
	return err
}

// startUpload records an upload and announces it; done ends it with the
// copy's outcome.
func (c *Core) startUpload(id, pool, name string, size int64) (Upload, context.Context, func(error)) {
	b := make([]byte, 6)
	rand.Read(b)
	u := Upload{ID: hex.EncodeToString(b), Connection: id, Pool: pool, Name: name, Total: size, State: "running"}
	ctx, cancel := context.WithCancel(context.Background())
	c.ups.mu.Lock()
	c.ups.cancels[u.ID] = cancel
	c.ups.mu.Unlock()
	c.emit(EvUpload, u)
	return u, ctx, func(err error) {
		c.ups.mu.Lock()
		delete(c.ups.cancels, u.ID)
		c.ups.mu.Unlock()
		cancel()
		switch {
		case err == nil:
			u.State, u.Sent = "done", u.Total
		case errors.Is(err, context.Canceled):
			u.State = "cancelled"
		default:
			u.State, u.Error = "failed", err.Error()
		}
		c.emit(EvUpload, u)
		c.recordUpload(u)
	}
}

func (c *Core) copyUpload(ctx context.Context, u Upload, r io.Reader) error {
	var last time.Time
	_, err := c.m.UploadFrom(ctx, u.Connection, u.Pool, u.Name, r, u.Total, func(sent, total int64) {
		if time.Since(last) < 200*time.Millisecond && sent != total {
			return
		}
		last = time.Now()
		p := u
		p.Sent, p.Total = sent, total
		c.emit(EvUpload, p)
	})
	if err != nil && ctx.Err() != nil {
		return context.Canceled
	}
	return err
}
