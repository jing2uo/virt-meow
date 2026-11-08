package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/hv"
	"github.com/jing2uo/virt-meow/internal/store"
)

// downloadCore is a Core whose save dialog and volume copy are fakes: the
// dialog answers with pick(name), the copy writes data (or fails with
// fail) to the path it is given, as the Manager's does.
func downloadCore(t *testing.T, pick func(name string) (string, error), data string, fail error) (*Service, chan Download) {
	t.Helper()
	events := make(chan Download, 32)
	c := &Core{opts: Options{
		Mode: Desktop,
		Emit: func(name string, d any) {
			if name == EvDownload {
				events <- d.(Download)
			}
		},
		SaveFile: pick,
	}}
	c.ups.cancels = map[string]context.CancelFunc{}
	c.download = func(ctx context.Context, id, pool, name, path string, progress func(got, total int64)) (hv.Volume, error) {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			return hv.Volume{}, err
		}
		if fail != nil {
			os.Remove(path)
			return hv.Volume{}, fail
		}
		progress(int64(len(data)), int64(len(data)))
		return hv.Volume{Name: name}, nil
	}
	return &Service{c: c}, events
}

// last waits for a download's last event: done, failed or cancelled.
func last(t *testing.T, events chan Download) Download {
	t.Helper()
	for {
		select {
		case d := <-events:
			if d.State != "running" {
				return d
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the download never ended")
		}
	}
}

// The window's download: its save dialog is asked with the volume's name,
// the volume lands where it said, by way of a part file that doesn't stay.
func TestPickDownloadSaves(t *testing.T) {
	dir := t.TempDir()
	var asked string
	s, events := downloadCore(t, func(name string) (string, error) {
		asked = name
		return filepath.Join(dir, "mine.qcow2"), nil
	}, "the volume's bytes", nil)
	id, err := s.PickDownload("lab", "default", "web01.qcow2")
	if err != nil || id == "" {
		t.Fatalf("PickDownload: %q, %v", id, err)
	}
	d := last(t, events)
	if asked != "web01.qcow2" || d.State != "done" || d.Path != filepath.Join(dir, "mine.qcow2") {
		t.Fatalf("asked %q, ended %+v", asked, d)
	}
	if b, err := os.ReadFile(d.Path); err != nil || string(b) != "the volume's bytes" {
		t.Errorf("saved %q, %v", b, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

// Cancelling the dialog downloads nothing; a copy that fails leaves no
// file, and says why.
func TestPickDownloadCancelledOrFailed(t *testing.T) {
	dir := t.TempDir()
	s, _ := downloadCore(t, func(string) (string, error) { return "", nil }, "x", nil)
	if id, err := s.PickDownload("lab", "default", "web01.qcow2"); id != "" || err != nil {
		t.Errorf("a cancelled dialog: %q, %v", id, err)
	}
	s, events := downloadCore(t, func(string) (string, error) { return filepath.Join(dir, "web01.qcow2"), nil }, "x", errors.New("the host went away"))
	if _, err := s.PickDownload("lab", "default", "web01.qcow2"); err != nil {
		t.Fatal(err)
	}
	if d := last(t, events); d.State != "failed" || d.Error != "the host went away" {
		t.Errorf("ended %+v", d)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
	s.c.opts.SaveFile = nil
	if _, err := s.PickDownload("lab", "default", "web01.qcow2"); err == nil {
		t.Error("a browser's page opened a save dialog")
	}
}

// The settings: the defaults before any is saved, kept in the config dir,
// checked, and the language over the system's in Env.
func TestSettings(t *testing.T) {
	t.Setenv("VIRT_MM_CONFIG", t.TempDir())
	t.Setenv("LANG", "zh_CN.UTF-8")
	s := &Service{c: &Core{opts: Options{Mode: Desktop}}}
	s.c.once.Do(func() {}) // Env connects nothing here
	if st, err := s.Settings(); err != nil || st != store.DefaultSettings {
		t.Fatalf("before: %+v, %v", st, err)
	}
	want := store.Settings{Lang: "en", Notifications: false, Thumbnails: "off", Sidebar: "hidden"}
	if _, err := s.SaveSettings(want); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Settings(); st != want {
		t.Errorf("saved %+v, read %+v", want, st)
	}
	if e := s.Env(); e.Lang != "en" {
		t.Errorf("Env's language %q with the settings' en under a Chinese LANG", e.Lang)
	}
	if _, err := s.SaveSettings(store.Settings{Lang: "fr"}); err == nil {
		t.Error("saved a language the page doesn't have")
	}
}
