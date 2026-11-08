package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Window is how the desktop window was when it last closed, for the next
// start: its size and position (when the desktop tells it: Wayland
// doesn't), whether it was maximised, and the view it showed (the page's
// URL fragment, without '#').
type Window struct {
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	X          int    `json:"x,omitempty"`
	Y          int    `json:"y,omitempty"`
	Positioned bool   `json:"positioned,omitempty"`
	Maximised  bool   `json:"maximised,omitempty"`
	View       string `json:"view,omitempty"`
}

func windowPath() string { return filepath.Join(Dir(), "window.json") }

// windowMu orders this process's changes to window.json: the page's view
// and the window's size are written from different goroutines.
var windowMu sync.Mutex

// LoadWindow reads the window's last state; none saved is the zero Window.
func LoadWindow() (Window, error) {
	var w Window
	data, err := os.ReadFile(windowPath())
	if errors.Is(err, fs.ErrNotExist) {
		return w, nil
	} else if err != nil {
		return w, err
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return Window{}, fmt.Errorf("%s: %w", windowPath(), err)
	}
	return w, nil
}

// UpdateWindow changes the window's saved state with fn. A file that can't
// be read is started over: it only saves where the window was.
func UpdateWindow(fn func(*Window)) error {
	windowMu.Lock()
	defer windowMu.Unlock()
	w, _ := LoadWindow()
	fn(&w)
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(windowPath(), append(data, '\n'))
}
