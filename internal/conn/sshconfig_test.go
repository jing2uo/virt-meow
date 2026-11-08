package conn

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testConfig writes a user config (and the files it includes) into a
// scratch HOME, and a system one beside it.
func testConfig(t *testing.T, user, system string, more map[string]string) sshConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config"), []byte(user), 0o600)
	for name, body := range more {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700)
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	sys := t.TempDir()
	os.WriteFile(filepath.Join(sys, "ssh_config"), []byte(system), 0o644)
	return sshConfig{files: []configFile{{filepath.Join(dir, "config"), dir}, {filepath.Join(sys, "ssh_config"), sys}}}
}

func hops(t *testing.T, c sshConfig, name, user, port string) []Hop {
	t.Helper()
	r, err := c.route(name, user, port, 0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSSHConfigAlias(t *testing.T) {
	c := testConfig(t, `
# a comment
Host kvm1 kvm1.lan
    HostName 10.0.0.5
    User admin
    Port 2200
    IdentityFile ~/.ssh/kvm_%h
    IdentitiesOnly yes

Host kvm*
    User nobody
    IdentityFile=~/.ssh/other
Host *
    Port 2201
`, "Host *\n  User sysuser\n  IdentityAgent none\n", nil)
	home := os.Getenv("HOME")
	r := hops(t, c, "KVM1", "", "")
	want := Hop{Name: "KVM1", User: "admin", Host: "10.0.0.5", Port: "2200", IdentitiesOnly: true, Agent: "none", Configured: true,
		IdentityFiles: []string{filepath.Join(home, ".ssh", "kvm_10.0.0.5"), filepath.Join(home, ".ssh", "other")}}
	if len(r) != 1 || !reflect.DeepEqual(r[0], want) {
		t.Fatalf("kvm1:\n got %+v\nwant %+v", r, want)
	}
	// The URI's user and port win.
	if r := hops(t, c, "kvm1", "root", "22"); r[0].User != "root" || r[0].Port != "22" {
		t.Fatalf("with user and port: %+v", r[0])
	}
	// A host the config only gives defaults for: the default keys.
	r = hops(t, c, "other.example", "", "")
	if r[0].Host != "other.example" || r[0].User != "sysuser" || r[0].Port != "2201" || len(r[0].IdentityFiles) != 3 ||
		r[0].IdentityFiles[0] != filepath.Join(home, ".ssh", "id_ed25519") {
		t.Fatalf("other: %+v", r[0])
	}
}

func TestSSHConfigPatterns(t *testing.T) {
	c := testConfig(t, `
Host *.lan !secret.lan
    User lan
Host ?b
    User two
Host q
    IdentityFile "~/.ssh/with space"
`, "", nil)
	for host, user := range map[string]string{"a.lan": "lan", "secret.lan": "", "ab": "two", "abc": ""} {
		r := hops(t, c, host, "", "")
		if user == "" {
			user = currentUser()
		}
		if r[0].User != user {
			t.Errorf("%s: user %q, want %q", host, r[0].User, user)
		}
	}
	if f := hops(t, c, "q", "", "")[0].IdentityFiles; len(f) != 1 || filepath.Base(f[0]) != "with space" {
		t.Errorf("quoted: %v", f)
	}
}

func TestSSHConfigIncludeAndMatch(t *testing.T) {
	c := testConfig(t, `
Include conf.d/*.conf
Match host 10.0.0.*
    User matched
Match originalhost box exec "true"
    Port 9999
Match !host nothing
    IdentitiesOnly yes
`, "", map[string]string{
		"conf.d/a.conf": "Host box\n  HostName 10.0.0.9\n",
		"conf.d/b.conf": "Host box\n  HostName ignored\n  Port 2022\n",
	})
	r := hops(t, c, "box", "", "")
	if r[0].Host != "10.0.0.9" || r[0].User != "matched" || r[0].Port != "2022" || !r[0].IdentitiesOnly {
		t.Fatalf("box: %+v", r[0])
	}
}

func TestSSHConfigJumps(t *testing.T) {
	c := testConfig(t, `
Host inner
    HostName 192.168.1.10
    ProxyJump mid,ops@edge:2222
Host mid
    HostName mid.example
    ProxyJump bastion
Host bastion
    HostName 203.0.113.1
    User jump
Host edge
    User ignored-for-edge
Host viacmd
    ProxyCommand ssh -q -W %h:%p -l cmduser bastion
    ProxyJump ignored
Host direct
    ProxyJump none
Host bad
    ProxyCommand nc %h %p
Host loop
    ProxyJump loop
`, "", nil)
	got := func(name string) []string {
		var s []string
		for _, h := range hops(t, c, name, "", "") {
			s = append(s, h.Name+"="+h.String())
		}
		return s
	}
	me := currentUser()
	if g, w := got("inner"), []string{
		"bastion=jump@203.0.113.1:22", "mid=" + me + "@mid.example:22", "edge=ops@edge:2222", "inner=" + me + "@192.168.1.10:22",
	}; !reflect.DeepEqual(g, w) {
		t.Errorf("inner: %v, want %v", g, w)
	}
	if g, w := got("viacmd"), []string{"bastion=cmduser@203.0.113.1:22", "viacmd=" + me + "@viacmd:22"}; !reflect.DeepEqual(g, w) {
		t.Errorf("viacmd: %v, want %v", g, w)
	}
	if g := got("direct"); len(g) != 1 {
		t.Errorf("direct: %v", g)
	}
	if _, err := c.route("bad", "", "", 0); err == nil || !strings.Contains(err.Error(), "ProxyJump") {
		t.Errorf("bad: %v", err)
	}
	if _, err := c.route("loop", "", "", 0); err == nil {
		t.Error("loop: no error")
	}
}

func TestRouteNotSSH(t *testing.T) {
	tg, _ := Parse("qemu:///system")
	if r, err := Route(tg); r != nil || err != nil {
		t.Fatalf("local: %v %v", r, err)
	}
}
