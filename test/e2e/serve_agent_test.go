//go:build e2e

package e2e

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/api"
)

// The guest agent on the details page under virt-mm serve: the guest in
// Info, a user's SSH keys added, listed and removed, a password set; and
// a guest whose agent doesn't answer.
func TestServeAgent(t *testing.T) {
	s := qemuSession(t)
	sv := serve(t)
	uuid := agentGuest(t, s, "e2e-agent-page", true, "")
	var con screen
	in, _ := openConsole(t, s, uuid, &con)
	con.waitFor(t, "virt-mm-e2e: qemu-ga on", 1)

	var info api.MachineInfo
	for deadline := time.Now().Add(60 * time.Second); info.Guest == nil; time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("no guest in Info in 60 s: %s", info.GuestNote)
		}
		sv.call(&info, "Info", sv.conn, uuid)
	}
	if g := info.Guest; g.Hostname != "virt-mm-agent-guest" || g.OSID != "debian" {
		t.Errorf("Info's guest: %+v", g)
	}

	a, b := pubKey(t, "virt-mm-page-a"), pubKey(t, "virt-mm-page-b")
	var keys []string
	sv.call(&keys, "SSHKeys", sv.conn, uuid, "e2e")
	if len(keys) != 0 {
		t.Fatalf("keys before: %q", keys)
	}
	sv.call(nil, "SetSSHKeys", sv.conn, uuid, "e2e", []string{a, b}, false)
	sv.call(&keys, "SSHKeys", sv.conn, uuid, "e2e")
	if len(keys) != 2 || !slices.Contains(keys, a) || !slices.Contains(keys, b) {
		t.Fatalf("after adding: %q", keys)
	}
	sv.call(nil, "SetSSHKeys", sv.conn, uuid, "e2e", []string{a}, true)
	sv.call(&keys, "SSHKeys", sv.conn, uuid, "e2e")
	if len(keys) != 1 || keys[0] != b {
		t.Errorf("after removing: %q", keys)
	}
	if err := sv.try(nil, "SetSSHKeys", sv.conn, uuid, "e2e", []string{"ssh-ed25519 nope"}, false); err == nil || !strings.Contains(err.Error(), "not an SSH public key") {
		t.Errorf("a bad key: %v", err)
	}

	sv.call(nil, "SetUserPassword", sv.conn, uuid, "root", "page pass")
	fmt.Fprintf(in, "%s\n", `h=$(grep ^root: /etc/shadow | cut -d: -f2); [ "$(mkpasswd -m sha512 "page pass" "$(echo "$h" | cut -d'$' -f3)")" = "$h" ] && echo "pw=$((40+3))"`)
	con.waitFor(t, "pw=43", 1)
	if err := sv.try(nil, "SetUserPassword", sv.conn, uuid, "nobody", "x"); err == nil || !strings.Contains(err.Error(), "no user nobody") {
		t.Errorf("a user the guest doesn't have: %v", err)
	}

	// An agent channel nobody answers on: said in Info, and the calls fail
	// with why.
	mute := agentGuest(t, s, "e2e-agent-page-mute", true, "/bin/sh")
	var mi api.MachineInfo
	sv.call(&mi, "Info", sv.conn, mute)
	if mi.Guest != nil || !strings.Contains(mi.GuestNote, "doesn't answer") {
		t.Errorf("Info without an agent: %+v %q", mi.Guest, mi.GuestNote)
	}
	if err := sv.try(nil, "SSHKeys", sv.conn, mute, "root"); err == nil || !strings.Contains(err.Error(), "doesn't answer") {
		t.Errorf("SSHKeys without an agent: %v", err)
	}
}
