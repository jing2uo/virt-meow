// Package store keeps virt-meow's own files: the saved connections, the
// known_hosts virt-meow writes, serve's password and where the window was. They live in $VIRT_MM_CONFIG, or virt-mm/ in the
// platform's config directory ($XDG_CONFIG_HOME, ~/Library/Application
// Support, %AppData%).
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Dir is virt-meow's config directory.
func Dir() string {
	if d := os.Getenv("VIRT_MM_CONFIG"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "virt-mm")
}

// KnownHosts is the known_hosts virt-meow writes.
func KnownHosts() string { return filepath.Join(Dir(), "known_hosts") }

// SystemKnownHosts are the user's ssh known_hosts files, which virt-meow reads
// but never writes.
func SystemKnownHosts() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".ssh", "known_hosts")}
}

// Connection is a saved libvirt connection.
type Connection struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URI         string `json:"uri"`
	Autoconnect bool   `json:"autoconnect"`
}

type connectionsFile struct {
	Connections []Connection `json:"connections"`
}

func connectionsPath() string { return filepath.Join(Dir(), "connections.json") }

// Connections reads the saved connections; none saved is an empty list.
func Connections() ([]Connection, error) {
	data, err := os.ReadFile(connectionsPath())
	if errors.Is(err, os.ErrNotExist) {
		return []Connection{}, nil
	} else if err != nil {
		return nil, err
	}
	var f connectionsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", connectionsPath(), err)
	}
	if f.Connections == nil {
		f.Connections = []Connection{}
	}
	return f.Connections, nil
}

// SaveConnections replaces the saved connections.
func SaveConnections(cs []Connection) error {
	data, err := json.MarshalIndent(connectionsFile{Connections: cs}, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(connectionsPath(), append(data, '\n'))
}

// NewID is a short random id for a connection.
func NewID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Find returns the connection whose id, name or URI is ref.
func Find(cs []Connection, ref string) (Connection, bool) {
	i := slices.IndexFunc(cs, func(c Connection) bool {
		return c.ID == ref || strings.EqualFold(c.Name, ref) || c.URI == ref
	})
	if i < 0 {
		return Connection{}, false
	}
	return cs[i], true
}

// writeFile writes atomically: a crash leaves the old file or the new one.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func servePasswordPath() string { return filepath.Join(Dir(), "serve-password") }

// ServePassword is the hash (bcrypt) of virt-mm serve's login password, nil
// when none is set.
func ServePassword() ([]byte, error) {
	b, err := os.ReadFile(servePasswordPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return bytes.TrimSpace(b), err
}

// SetServePassword keeps the hash of virt-mm serve's login password, readable
// by this account only.
func SetServePassword(hash []byte) error {
	return writeFile(servePasswordPath(), append(bytes.Clone(hash), '\n'))
}
