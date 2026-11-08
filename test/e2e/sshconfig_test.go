//go:build e2e

package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// sshConfigHome makes a HOME whose ~/.ssh/config names the container's
// sshd e2e-jump, with its key under a name ssh wouldn't try by itself, and
// two ways to the sshd behind it (only on the container's loopback, 2223):
// ProxyJump, and ProxyCommand ssh -W. No agent. Its known_hosts is the
// HostKeys' own file, so the CLI and serve, with configs of their own,
// read what the test trusted.
func sshConfigHome(t *testing.T) (*conn.HostKeys, string) {
	t.Helper()
	key, err := os.ReadFile(env(t, "VIRT_MM_E2E_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(env(t, "VIRT_MM_E2E_SSH"))
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	os.MkdirAll(dir, 0o700)
	files := map[string]string{
		"e2e-key": string(key),
		"config": fmt.Sprintf(`Include behind.conf
Host e2e-jump
    HostName %s
    Port %s
    User root
    IdentityFile ~/.ssh/e2e-key
    IdentitiesOnly yes
Host e2e-agent e2e-agent-other
    HostName %[1]s
    Port %[2]s
    User root
    IdentitiesOnly yes
    IdentityAgent ~/agent.sock
Host e2e-agent
    IdentityFile ~/.ssh/agent-key
Host e2e-agent-other
    IdentityFile ~/.ssh/other-key
Host *
    User nobody
`, host, port),
		"behind.conf": `Host e2e-behind e2e-behind-cmd
    HostName 127.0.0.1
    Port 2223
    IdentityFile ~/.ssh/e2e-key
Host e2e-behind
    ProxyJump e2e-jump
Host e2e-behind-cmd
    ProxyCommand ssh -q -W %h:%p e2e-jump
Host e2e-*
    User root
`,
	}
	// The agent's key by its public half alone, and another key.
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	files["agent-key.pub"] = string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	files["other-key.pub"] = string(ssh.MarshalAuthorizedKey(otherSigner.PublicKey()))
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	return &conn.HostKeys{Own: filepath.Join(dir, "known_hosts")}, port
}

// connectTrusting connects s, trusting each unknown host key on the way
// (a jump host's, then the host's), and returns the addresses trusted.
func connectTrusting(t *testing.T, s *conn.Session, hk *conn.HostKeys) []string {
	t.Helper()
	var trusted []string
	for range 4 {
		err := s.Connect(ctx(t))
		var unknown *conn.UnknownHostKeyError
		if !errors.As(err, &unknown) {
			if err != nil {
				t.Fatalf("%s: %v", s.Target(), err)
			}
			return trusted
		}
		trusted = append(trusted, unknown.Addr)
		if err := hk.Trust(unknown.Addr, unknown.Key); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("%s: still an unknown host key after %v", s.Target(), trusted)
	return nil
}

// sshdPort is the port of the sshd s's commands run under, from the
// SSH_CONNECTION it sets: which of the two s really reached.
func sshdPort(t *testing.T, s *conn.Session) string {
	t.Helper()
	out, err := s.Run(ctx(t), "echo $SSH_CONNECTION")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(out))
	if len(f) != 4 {
		t.Fatalf("SSH_CONNECTION: %q", out)
	}
	return f[3]
}

func TestSSHConfig(t *testing.T) {
	hk, port := sshConfigHome(t)
	open := func(uri string) *conn.Session {
		return openWith(t, uri, hk)
	}

	// An alias: HostName, Port, User, and a key only IdentityFile names.
	jump := open("qemu+ssh://e2e-jump/system")
	if got := connectTrusting(t, jump, hk); len(got) != 1 || !strings.HasSuffix(got[0], ":"+port) {
		t.Fatalf("e2e-jump: trusted %v", got)
	}
	machines(t, jump)
	if p := sshdPort(t, jump); p != "2222" {
		t.Fatalf("e2e-jump reached the sshd on %s", p)
	}

	// Behind it, by ProxyJump: the jump host is known now, the host behind
	// isn't, under its own address as the jump host sees it.
	behind := open("qemu+ssh://e2e-behind/system")
	if got := connectTrusting(t, behind, hk); len(got) != 1 || got[0] != "127.0.0.1:2223" {
		t.Fatalf("e2e-behind: trusted %v", got)
	}
	machines(t, behind)
	if p := sshdPort(t, behind); p != "2223" {
		t.Fatalf("e2e-behind reached the sshd on %s", p)
	}

	// By ProxyCommand ssh -W %h:%p: the same way, and the same host key.
	cmd := open("qemu+ssh://e2e-behind-cmd/system")
	if got := connectTrusting(t, cmd, hk); len(got) != 0 {
		t.Fatalf("e2e-behind-cmd: trusted %v again", got)
	}
	if p := sshdPort(t, cmd); p != "2223" {
		t.Fatalf("e2e-behind-cmd reached the sshd on %s", p)
	}

	// The CLI: the route, and a list over it, as `virsh -c qemu+ssh://alias/system list`.
	var hops []conn.Hop
	cli(t, &hops, false, "conn", "route", "qemu+ssh://e2e-behind/system")
	if len(hops) != 2 || hops[0].Name != "e2e-jump" || hops[0].Port != port || !hops[0].IdentitiesOnly ||
		hops[1].String() != "root@127.0.0.1:2223" || !strings.HasSuffix(hops[1].IdentityFiles[0], "e2e-key") {
		t.Fatalf("conn route: %+v", hops)
	}
	cli(t, &[]any{}, false, "ls", "-c", "qemu+ssh://e2e-behind-cmd/system")

	// An agent holding the key, with IdentitiesOnly: offered for the host
	// whose IdentityFile is that key (its .pub, the private half isn't
	// there), not for the one whose IdentityFile is another key.
	key, _ := os.ReadFile(env(t, "VIRT_MM_E2E_KEY"))
	raw, err := ssh.ParseRawPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	kr := agent.NewKeyring()
	kr.Add(agent.AddedKey{PrivateKey: raw})
	ln, err := net.Listen("unix", filepath.Join(os.Getenv("HOME"), "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { agent.ServeAgent(kr, c); c.Close() }()
		}
	}()
	// SSH clients are shared per user@host:port and host-key store, and
	// these are the same host as e2e-jump: stores of their own dial anew.
	fresh := func() *conn.HostKeys { return &conn.HostKeys{Own: hk.Own} }
	if err := openWith(t, "qemu+ssh://e2e-agent/system", fresh()).Connect(ctx(t)); err != nil {
		t.Fatalf("e2e-agent: %v", err)
	}
	err = openWith(t, "qemu+ssh://e2e-agent-other/system", fresh()).Connect(ctx(t))
	var auth *conn.AuthError
	if !errors.As(err, &auth) {
		t.Fatalf("e2e-agent-other: %v, want a failed login", err)
	}

	// The page under serve: the route the form shows, and the alias added
	// and connected.
	where := startServe(t, virtmm(t, "serve", "--listen", "127.0.0.1:0"))
	p := page{t, "http://" + where.Listen}
	hops = nil
	p.call(&hops, "SSHRoute", "qemu+ssh://e2e-behind/system")
	if len(hops) != 2 || hops[1].Host != "127.0.0.1" || hops[0].User != "root" {
		t.Fatalf("SSHRoute: %+v", hops)
	}
	if err := p.try(nil, "SSHRoute", "qemu+ssh://bad host/system"); err == nil {
		t.Fatal("SSHRoute of a bad URI: no error")
	}
	ws, _, err := websocket.Dial(ctx(t), "ws://"+where.Listen+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	var c struct{ ID string }
	p.call(&c, "AddConnection", "", "qemu+ssh://e2e-behind/system", false)
	p.call(nil, "Connect", c.ID)
	await(t, ws, "connection", func(d json.RawMessage) bool {
		var ci struct{ ID, Status, Error string }
		json.Unmarshal(d, &ci)
		if ci.ID == c.ID && ci.Error != "" {
			t.Fatalf("connecting from the page: %s", ci.Error)
		}
		return ci.ID == c.ID && ci.Status == "connected"
	})
	var doms []any
	p.call(&doms, "Domains", c.ID)
}

func openWith(t *testing.T, uri string, hk *conn.HostKeys) *conn.Session {
	t.Helper()
	tg, err := conn.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	s := conn.New(tg, conn.Options{HostKeys: hk})
	t.Cleanup(func() { s.Close() })
	return s
}

// machines lists s's machines, failing t if it can't.
func machines(t *testing.T, s *conn.Session) {
	t.Helper()
	if _, err := hv.List(ctx(t), s); err != nil {
		t.Fatal(err)
	}
}
