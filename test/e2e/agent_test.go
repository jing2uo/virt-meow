//go:build e2e

package e2e

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// agentGuest defines a machine booting the container's agent guest
// (test/libvirtd/agent-guest.sh): a kernel and an initramfs with
// qemu-guest-agent, and a shell on the serial console. Without channel it
// has no agent channel; with init it runs that instead of the guest's /init.
func agentGuest(t *testing.T, s *conn.Session, name string, channel bool, init string) string {
	t.Helper()
	cmdline := "console=ttyS0 quiet"
	if init != "" {
		cmdline += " rdinit=" + init
	}
	x := `<domain type="qemu"><name>` + name + `</name><memory unit="MiB">256</memory><vcpu>1</vcpu>
<os><type arch="x86_64" machine="q35">hvm</type><kernel>/usr/share/virt-mm-e2e/vmlinuz</kernel>
<initrd>/usr/share/virt-mm-e2e/initrd.gz</initrd><cmdline>` + cmdline + `</cmdline></os>
<devices><serial type="pty"/><console type="pty"/>`
	if channel {
		x += `<channel type="unix"><target type="virtio" name="org.qemu.guest_agent.0"/></channel>`
	}
	x += `</devices></domain>`
	d, err := hv.Define(ctx(t), s, x)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, d.UUID, false) })
	if err := hv.Power(ctx(t), s, d.UUID, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, d.UUID, hv.ForceOff) })
	return d.UUID
}

func pubKey(t *testing.T, comment string) string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " " + comment
}

// virt-mm agent password, agent ssh-key, and the guest in virt-mm info,
// against a real qemu-guest-agent; and the errors without one.
func TestGuestAgent(t *testing.T) {
	s := qemuSession(t)
	const name = "e2e-agent"
	uuid := agentGuest(t, s, name, true, "")
	var con screen
	in, _ := openConsole(t, s, uuid, &con)
	con.waitFor(t, "virt-mm-e2e: qemu-ga on", 1)
	// type runs a command in the guest's shell; its output is to show
	// something the command line itself doesn't.
	typ := func(cmd string) { fmt.Fprintf(in, "%s\n", cmd) }

	var info hv.Info
	for deadline := time.Now().Add(60 * time.Second); info.Guest == nil; time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("no guest info from the agent in 60 s: %s", info.GuestNote)
		}
		cli(t, &info, false, "info", name)
	}
	if g := info.Guest; g.Hostname != "virt-mm-agent-guest" || !strings.HasPrefix(g.OS, "Debian GNU/Linux") || g.OSID != "debian" ||
		g.Kernel == "" || g.Arch != "x86_64" || info.GuestNote != "" {
		t.Fatalf("guest: %+v (%s)", g, info.GuestNote)
	}

	// SSH keys: added (as e2e's own), listed, removed.
	dir := t.TempDir()
	a, b := pubKey(t, "virt-mm-e2e-a"), pubKey(t, "virt-mm-e2e-b")
	file := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var keys []string
	cli(t, &keys, false, "agent", "ssh-key", "ls", name, "e2e")
	if len(keys) != 0 {
		t.Fatalf("keys before: %q", keys)
	}
	cli(t, &keys, false, "agent", "ssh-key", "add", name, "e2e", file("both.pub", "# two\n"+a+"\n"+b+"\n"))
	if slices.Sort(keys); len(keys) != 2 || !slices.Contains(keys, a) || !slices.Contains(keys, b) {
		t.Fatalf("after add: %q", keys)
	}
	cli(t, &keys, false, "agent", "ssh-key", "rm", name, "e2e", file("a.pub", a))
	if len(keys) != 1 || keys[0] != b {
		t.Fatalf("after rm: %q", keys)
	}
	typ(`stat -c "owner=%U" /home/e2e/.ssh/authorized_keys; cat /home/e2e/.ssh/authorized_keys`)
	con.waitFor(t, "owner=e2e", 1)
	con.waitFor(t, "virt-mm-e2e-b", 1)
	if con.count("virt-mm-e2e-a") != 0 {
		t.Error("the removed key is still in authorized_keys")
	}
	if msg := cli(t, nil, true, "agent", "ssh-key", "add", name, "e2e", file("bad.pub", "ssh-ed25519 nope")); !strings.Contains(msg, "not an SSH public key") {
		t.Errorf("a bad key: %q", msg)
	}
	if msg := cli(t, nil, true, "agent", "ssh-key", "ls", name, "nobody"); msg == "" {
		t.Error("keys of a user the guest doesn't have")
	}

	// A password: the hash in /etc/shadow is of what was given.
	cmd := virtmm(t, "agent", "password", name, "root", "--password-stdin")
	cmd.Stdin = strings.NewReader("s3cret pass\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("agent password: %v: %s", err, out)
	}
	typ(`h=$(grep ^root: /etc/shadow | cut -d: -f2); [ "$(mkpasswd -m sha512 "s3cret pass" "$(echo "$h" | cut -d'$' -f3)")" = "$h" ] && echo "pw=$((40+2))"`)
	con.waitFor(t, "pw=42", 1)
	cmd = virtmm(t, "agent", "password", name, "nobody", "--password-stdin")
	cmd.Stdin = strings.NewReader("x\n")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err == nil || !strings.Contains(stdout.String(), "no user nobody") {
		t.Errorf("a user the guest doesn't have: %v %s", err, stdout.String())
	}
	if msg := cli(t, nil, true, "agent", "password", name, "root"); !strings.Contains(msg, "--password-stdin") {
		t.Errorf("no terminal and no --password-stdin: %q", msg)
	}

	// Without an agent: a channel nobody answers on, no channel, off.
	mute := agentGuest(t, s, "e2e-agent-mute", true, "/bin/sh")
	agentGuest(t, s, "e2e-agent-none", false, "/bin/sh")
	if msg := cli(t, nil, true, "agent", "ssh-key", "ls", "e2e-agent-mute", "root"); !strings.Contains(msg, "doesn't answer") {
		t.Errorf("an agent that doesn't answer: %q", msg)
	}
	if msg := cli(t, nil, true, "agent", "ssh-key", "ls", "e2e-agent-none", "root"); !strings.Contains(msg, "no channel for a guest agent") {
		t.Errorf("no agent channel: %q", msg)
	}
	info = hv.Info{}
	cli(t, &info, false, "info", "e2e-agent-mute")
	if info.Guest != nil || !strings.Contains(info.GuestNote, "doesn't answer") {
		t.Errorf("info without an agent: %+v %q", info.Guest, info.GuestNote)
	}
	hv.Power(ctx(t), s, mute, hv.ForceOff)
	if msg := cli(t, nil, true, "agent", "ssh-key", "ls", "e2e-agent-mute", "root"); !strings.Contains(msg, "not running") {
		t.Errorf("a machine that is off: %q", msg)
	}
}
