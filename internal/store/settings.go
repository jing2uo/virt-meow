package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Settings are the page's own, from its settings dialog: shared by the
// window and every page under serve, since all are this account's.
type Settings struct {
	// Lang is the page's language: "zh" or "en", or "" for the system's
	// (the browser's own under serve).
	Lang string `json:"lang"`
	// Notifications: the window tells the desktop when a long operation
	// ends or a machine stops by itself while no virt-meow window has focus.
	Notifications bool `json:"notifications"`
	// Thumbnails: "" leaves the list's pictures to each host's switch,
	// "off" shows none anywhere.
	Thumbnails string `json:"thumbnails"`
	// Sidebar: "" beside the page, "hidden" put away (a wide window; a
	// narrow one always has it as a drawer).
	Sidebar string `json:"sidebar"`
	// NewMachine is what new machines get, in the form and virt-mm create.
	NewMachine NewMachine `json:"new_machine"`
}

// NewMachine is what new machines get unless the form or virt-mm create
// says otherwise; "" or false leaves it to virt-meow (and to osinfo-db's
// proposal for the system, for the disk bus and NIC model).
type NewMachine struct {
	Listen     string `json:"listen,omitempty"`      // the VNC console's address on the host; "" 127.0.0.1
	CPUMode    string `json:"cpu_mode,omitempty"`    // host-passthrough, host-model (KVM hosts)
	DiskFormat string `json:"disk_format,omitempty"` // qcow2, raw
	DiskBus    string `json:"disk_bus,omitempty"`    // virtio, sata, scsi
	NICModel   string `json:"nic_model,omitempty"`   // virtio, e1000e, e1000, rtl8139
	Sound      bool   `json:"sound,omitempty"`
	// Pool and Network are proposed on a host that has them: a pool's
	// name, and network:<name>, bridge:<name> or none.
	Pool    string `json:"pool,omitempty"`
	Network string `json:"network,omitempty"`
}

// check refuses values hv's Spec would.
func (n NewMachine) check() error {
	one := func(what, v string, ok ...string) error {
		if v != "" && !slices.Contains(ok, v) {
			return fmt.Errorf("new machines' %s %q: %s, or none", what, v, strings.Join(ok, ", "))
		}
		return nil
	}
	kind, name, _ := strings.Cut(n.Network, ":")
	switch {
	case n.Listen != "" && net.ParseIP(n.Listen) == nil:
		return fmt.Errorf("new machines' listen %q: an IP address", n.Listen)
	case n.Network != "" && n.Network != "none" && (kind != "network" && kind != "bridge" || name == ""):
		return fmt.Errorf("new machines' network %q: network:<name>, bridge:<name> or none", n.Network)
	case strings.ContainsAny(n.Pool+n.Network, "/ \t\n"):
		return errors.New("new machines' pool and network: names without spaces or slashes")
	}
	return errors.Join(one("CPU mode", n.CPUMode, "host-passthrough", "host-model"), one("disk format", n.DiskFormat, "qcow2", "raw"),
		one("disk bus", n.DiskBus, "virtio", "sata", "scsi"), one("NIC model", n.NICModel, "virtio", "e1000e", "e1000", "rtl8139"))
}

// DefaultSettings are the settings before any is changed.
var DefaultSettings = Settings{Notifications: true}

func settingsPath() string { return filepath.Join(Dir(), "settings.json") }

// LoadSettings reads the settings; none saved are DefaultSettings.
func LoadSettings() (Settings, error) {
	s := DefaultSettings
	data, err := os.ReadFile(settingsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return DefaultSettings, fmt.Errorf("%s: %w", settingsPath(), err)
	}
	return s, nil
}

// SaveSettings checks s and keeps it.
func SaveSettings(s Settings) error {
	switch {
	case s.Lang != "" && s.Lang != "zh" && s.Lang != "en":
		return fmt.Errorf("no language %q (want zh, en, or none for the system's)", s.Lang)
	case s.Thumbnails != "" && s.Thumbnails != "off":
		return fmt.Errorf("thumbnails %q: want off, or none", s.Thumbnails)
	case s.Sidebar != "" && s.Sidebar != "hidden":
		return fmt.Errorf("sidebar %q: want hidden, or none", s.Sidebar)
	}
	if err := s.NewMachine.check(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(settingsPath(), append(data, '\n'))
}
