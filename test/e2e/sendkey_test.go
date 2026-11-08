//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm send-key: keys reach the guest's keyboard. OVMF's shell reads the
// keyboard and the serial port both, so what is typed shows on the console.
func TestSendKey(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-key")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-key", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-key", DiskGiB: 1, NetKind: "none", Firmware: "uefi"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if msg := cli(t, nil, true, "send-key", "e2e-key", "enter"); !strings.Contains(msg, "not running") {
		t.Errorf("keys to a machine that is off: %q", msg)
	}
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })

	var con screen
	openConsole(t, s, uuid, &con)
	con.waitFor(t, "Shell>", 1)

	keys := []string{"send-key", "e2e-key"}
	for _, k := range "echo virtmmkeys" {
		keys = append(keys, map[rune]string{' ': "space"}[k]+strings.TrimSpace(string(k)))
	}
	var res struct {
		Sent []string `json:"sent"`
	}
	cli(t, &res, false, append(keys, "enter")...)
	if len(res.Sent) != len(keys)-1 {
		t.Errorf("sent %v", res.Sent)
	}
	con.waitFor(t, "\r\nvirtmmkeys\r\n", 1)

	if msg := cli(t, nil, true, "send-key", "e2e-key", "ctrl+hyper"); !strings.Contains(msg, "hyper") {
		t.Errorf("an unknown key: %q", msg)
	}
	// Ctrl+Alt+Delete: the firmware resets, and boots again.
	cli(t, nil, false, "send-key", "e2e-key", "ctrl+alt+delete")
	con.waitFor(t, "BdsDxe: failed to load", 2)
}
