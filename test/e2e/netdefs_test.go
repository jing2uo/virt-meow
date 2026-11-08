//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm net host add|rm|ls: a fixed address handed to a PXE guest by the
// running network, and one kept for the network's next start;
// virt-mm net xml [--set], net edit.
func TestNetHostsAndXML(t *testing.T) {
	s := qemuSession(t)
	const net = "e2e-fix"
	t.Cleanup(func() { hv.RemoveNetwork(ctx(t), s, net, true) })
	netCLI(t, "add", net, "--type", "isolated", "--address", "10.231.77.1/24")
	g := netGuest(t, s, "e2e-fix-pxe", net)
	mac := devices(t, s, g.UUID).NICs[0].MAC

	var e hv.Edited
	cli(t, &e, false, "net", "host", "add", net, strings.ToUpper(mac), "10.231.77.50", "fixed")
	if !e.Active || len(e.Changes) != 1 || !e.Changes[0].Live {
		t.Fatalf("added to a running network: %+v", e)
	}
	var hs []hv.DHCPHost
	cli(t, &hs, false, "net", "host", "ls", net)
	if len(hs) != 1 || hs[0] != (hv.DHCPHost{MAC: mac, IP: "10.231.77.50", Name: "fixed", Defined: true, Live: true}) {
		t.Fatalf("hosts: %+v", hs)
	}
	for what, args := range map[string][]string{
		"outside the subnet":   {mac, "10.231.78.50"},
		"the network's own":    {"52:54:00:00:00:01", "10.231.77.1"},
		"the subnet's":         {"52:54:00:00:00:01", "10.231.77.0"},
		"the same MAC again":   {mac, "10.231.77.51"},
		"the same IP again":    {"52:54:00:00:00:01", "10.231.77.50"},
		"not a MAC":            {"52:54:00:00:01", "10.231.77.51"},
		"IPv6":                 {"52:54:00:00:00:01", "fd00::5"},
		"not a hostname":       {"52:54:00:00:00:01", "10.231.77.51", "a b"},
		"a network that isn't": {"52:54:00:00:00:01", "10.231.77.51"},
	} {
		n := net
		if what == "a network that isn't" {
			n = "e2e-nope"
		}
		if msg := cli(t, nil, true, append([]string{"net", "host", "add", n}, args...)...); msg == "" {
			t.Errorf("%s: accepted", what)
		}
	}

	// The guest gets it.
	if err := hv.Power(ctx(t), s, g.UUID, hv.Start); err != nil {
		t.Fatal(err)
	}
	var ls []hv.Lease
	for deadline := time.Now().Add(90 * time.Second); len(ls) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("no DHCP lease after 90 s")
		}
		time.Sleep(time.Second)
		cli(t, &ls, false, "net", "leases", net)
	}
	if ls[0].IP != "10.231.77.50/24" || ls[0].MAC != mac || ls[0].Hostname != "fixed" {
		t.Fatalf("lease: %+v, want the fixed address", ls)
	}
	hv.Power(ctx(t), s, g.UUID, hv.ForceOff)

	e = hv.Edited{}
	cli(t, &e, false, "net", "host", "rm", net, "10.231.77.50")
	if !e.Changes[0].Live {
		t.Errorf("removed from a running network: %+v", e)
	}
	cli(t, &hs, false, "net", "host", "ls", net)
	if len(hs) != 0 {
		t.Fatalf("after rm: %+v", hs)
	}
	if msg := cli(t, nil, true, "net", "host", "rm", net, mac); !strings.Contains(msg, "no fixed address") {
		t.Errorf("removing one it doesn't have: %q", msg)
	}

	// Stopped: into the definition, handed out from its next start.
	netCLI(t, "stop", net)
	e = hv.Edited{}
	cli(t, &e, false, "net", "host", "add", net, mac, "10.231.77.60")
	if e.Active || e.Changes[0].Live {
		t.Errorf("added to a stopped network: %+v", e)
	}
	netCLI(t, "start", net)
	cli(t, &hs, false, "net", "host", "ls", net)
	if len(hs) != 1 || !hs[0].Defined || !hs[0].Live || hs[0].IP != "10.231.77.60" {
		t.Fatalf("after the network's start: %+v", hs)
	}

	// The XML: shown, saved, refused when it renames.
	var def hv.NetworkDefinition
	cli(t, &def, false, "net", "xml", net)
	if !def.Active || !strings.Contains(def.XML, "<host mac='"+mac+"' ip='10.231.77.60'/>") {
		t.Fatalf("net xml: %+v", def)
	}
	dir := t.TempDir()
	write := func(x string) string {
		p := filepath.Join(dir, "net.xml")
		if err := os.WriteFile(p, []byte(x), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if msg := cli(t, nil, true, "net", "xml", net, "--set", write(strings.Replace(def.XML, "<name>"+net+"</name>", "<name>e2e-other</name>", 1))); !strings.Contains(msg, "rename") {
		t.Errorf("a renamed network: %q", msg)
	}
	if msg := cli(t, nil, true, "net", "xml", net, "--set", write(strings.Replace(def.XML, "<uuid>", "<uuid>0", 1))); !strings.Contains(msg, "uuid") {
		t.Errorf("another UUID: %q", msg)
	}
	if msg := cli(t, nil, true, "net", "xml", net, "--set", write(strings.Replace(def.XML, "<ip ", "<ipx ", 1))); msg == "" {
		t.Error("an invalid definition saved")
	}
	var saved hv.NetworkDefinition
	cli(t, &saved, false, "net", "xml", net, "--set", write(strings.Replace(def.XML, "10.231.77.254", "10.231.77.200", 1)))
	if !saved.Active || !strings.Contains(saved.XML, "end='10.231.77.200'") {
		t.Fatalf("--set: %+v", saved)
	}

	// net edit, with an editor that changes the range's end.
	cmd := virtmm(t, "net", "edit", net)
	cmd.Env = append(cmd.Env, "VISUAL=sed -i s/10.231.77.200/10.231.77.150/")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("net edit: %v: %s%s", err, stdout.String(), stderr.String())
	}
	cli(t, &def, false, "net", "xml", net)
	if !strings.Contains(def.XML, "end='10.231.77.150'") {
		t.Fatalf("after net edit: %s", def.XML)
	}
	cmd = virtmm(t, "net", "edit", net)
	cmd.Env = append(cmd.Env, "VISUAL=true")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil || !strings.Contains(stderr.String(), "unchanged") {
		t.Errorf("net edit, unchanged: %v: %s", err, stderr.String())
	}
}
