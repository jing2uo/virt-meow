package hv

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// keycodes are Linux input key codes (linux/input-event-codes.h), libvirt's
// VIR_KEYCODE_SET_LINUX, by the names virt-mm send-key takes.
var keycodes = func() map[string]uint32 {
	m := map[string]uint32{
		"esc": 1, "escape": 1, "minus": 12, "equal": 13, "backspace": 14, "tab": 15,
		"enter": 28, "return": 28, "ctrl": 29, "leftctrl": 29, "semicolon": 39, "apostrophe": 40,
		"grave": 41, "shift": 42, "leftshift": 42, "backslash": 43, "comma": 51, "dot": 52, "period": 52,
		"slash": 53, "rightshift": 54, "kpasterisk": 55, "alt": 56, "leftalt": 56, "space": 57,
		"capslock": 58, "numlock": 69, "scrolllock": 70, "rightctrl": 97, "sysrq": 99, "print": 99,
		"rightalt": 100, "altgr": 100, "home": 102, "up": 103, "pageup": 104, "pgup": 104, "left": 105,
		"right": 106, "end": 107, "down": 108, "pagedown": 109, "pgdn": 109, "insert": 110,
		"delete": 111, "del": 111, "pause": 119, "meta": 125, "super": 125, "win": 125, "leftmeta": 125,
		"rightmeta": 126, "menu": 127, "leftbrace": 26, "rightbrace": 27,
	}
	for i, k := range "1234567890" {
		m[string(k)] = uint32(2 + i)
	}
	for i, row := range []string{"qwertyuiop", "asdfghjkl", "zxcvbnm"} {
		first := []uint32{16, 30, 44}[i]
		for j, k := range row {
			m[string(k)] = first + uint32(j)
		}
	}
	for i := range 10 {
		m[fmt.Sprintf("f%d", i+1)] = 59 + uint32(i)
	}
	m["f11"], m["f12"] = 87, 88
	return m
}()

// ParseKeys reads one combination of keys pressed together, as
// "ctrl+alt+delete", "ctrl+alt+f2" or "a": names, or Linux key codes as
// numbers.
func ParseKeys(combo string) ([]uint32, error) {
	var codes []uint32
	for _, name := range strings.Split(strings.ToLower(strings.TrimSpace(combo)), "+") {
		name = strings.TrimPrefix(name, "key_")
		if c, ok := keycodes[name]; ok {
			codes = append(codes, c)
			continue
		}
		if n, err := strconv.ParseUint(name, 10, 16); err == nil && len(name) > 1 && n > 0 && n < 0x300 {
			codes = append(codes, uint32(n))
			continue
		}
		return nil, fmt.Errorf("key %q in %q: a name (ctrl, alt, shift, delete, f1…f12, a…z, 0…9, enter, esc, …) or a Linux key code", name, combo)
	}
	// libvirt takes up to 16 keys at once (VIR_DOMAIN_SEND_KEY_MAX_KEYS).
	if len(codes) > 16 {
		return nil, fmt.Errorf("%q: at most 16 keys at once", combo)
	}
	return codes, nil
}

// SendKeys presses each combination of keys in turn on the running
// machine uuid's keyboard, each held for holdMs (libvirt's default when 0)
// and released.
func SendKeys(ctx context.Context, sess *conn.Session, uuid string, combos []string, holdMs uint32) error {
	if len(combos) == 0 {
		return fmt.Errorf("no keys to send")
	}
	var all [][]uint32
	for _, c := range combos {
		codes, err := ParseKeys(c)
		if err != nil {
			return err
		}
		all = append(all, codes)
	}
	u, err := ParseUUID(uuid)
	if err != nil {
		return err
	}
	return sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		state, _, err := lv.DomainGetState(dom, 0)
		if err != nil {
			return err
		}
		if !stateOf(libvirt.DomainState(state)).Active() {
			return fmt.Errorf("%s is not running", dom.Name)
		}
		for i, codes := range all {
			if err := lv.DomainSendKey(dom, uint32(libvirt.KeycodeSetLinux), holdMs, codes, 0); err != nil {
				return fmt.Errorf("%s: %w", combos[i], err)
			}
		}
		return nil
	})
}
