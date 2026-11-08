//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

func network(t *testing.T, name string) (hv.Network, bool) {
	t.Helper()
	var ns struct {
		Networks []hv.Network `json:"networks"`
	}
	cli(t, &ns, false, "nets")
	i := slices.IndexFunc(ns.Networks, func(n hv.Network) bool { return n.Name == name })
	if i < 0 {
		return hv.Network{}, false
	}
	return ns.Networks[i], true
}

// netCLI runs a virt-mm net command that prints a network.
func netCLI(t *testing.T, args ...string) hv.Network {
	t.Helper()
	var n hv.Network
	cli(t, &n, false, append([]string{"net"}, args...)...)
	return n
}

// podmanHome is HOME as the tests started with it. Rootless podman finds its
// containers under $XDG_DATA_HOME, or $HOME/.local/share when that is unset
// (as on GitHub's runners), and sshHome changes HOME.
var podmanHome = os.Getenv("HOME")

// podman runs podman with the HOME the e2e container was started with.
func podman(args ...string) *exec.Cmd {
	c := exec.Command("podman", args...)
	c.Env = append(os.Environ(), "HOME="+podmanHome)
	return c
}

func podmanExec(t *testing.T, args ...string) string {
	t.Helper()
	out, err := podman(append([]string{"exec", env(t, "VIRT_MM_E2E_CONTAINER")}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman exec %v: %v: %s", args, err, out)
	}
	return string(out)
}

// netGuest defines a diskless machine with one NIC on network that boots
// from it: SeaBIOS's iPXE asks DHCP for an address, which is all a lease
// test needs, and nothing else to install.
func netGuest(t *testing.T, s *conn.Session, name, network string) hv.Domain {
	t.Helper()
	x := `<domain type="qemu"><name>` + name + `</name><memory unit="MiB">128</memory><vcpu>1</vcpu>
<os><type arch="x86_64" machine="pc">hvm</type><boot dev="network"/></os>
<devices><interface type="network"><source network="` + network + `"/><model type="virtio"/></interface></devices></domain>`
	f := filepath.Join(t.TempDir(), name+".xml")
	if err := os.WriteFile(f, []byte(x), 0o644); err != nil {
		t.Fatal(err)
	}
	cli(t, nil, false, "define", f)
	d := machine(t, s, name)
	t.Cleanup(func() {
		hv.Power(ctx(t), s, d.UUID, hv.ForceOff)
		hv.Delete(ctx(t), s, d.UUID, false)
	})
	return d
}

// virt-mm net: NAT with DHCP and a guest's lease, routed, isolated, and a
// bridge to one of the host's bridges; the container has the capabilities
// for bridges and nftables in its own network namespace (test/e2e.sh).
func TestNetworks(t *testing.T) {
	s := qemuSession(t)
	nets := []string{"e2e-nat", "e2e-route", "e2e-iso", "e2e-br", "e2e-clash"}
	t.Cleanup(func() {
		for _, n := range nets {
			hv.RemoveNetwork(ctx(t), s, n, true)
		}
	})

	// NAT, on the first free subnet, with DHCP over the rest of it.
	n := netCLI(t, "add", "e2e-nat", "--type", "nat", "--autostart", "on")
	if n.Forward != "nat" || !n.Active || !n.Autostart || !strings.HasSuffix(n.Address, ".1/24") || n.DHCP == "" || n.Bridge == "" {
		t.Fatalf("nat: %+v", n)
	}
	if !strings.Contains(podmanExec(t, "nft", "list", "ruleset"), "masquerade") {
		t.Error("no masquerading rule for the NAT network")
	}
	var ls []hv.Lease
	cli(t, &ls, false, "net", "leases", "e2e-nat")
	if len(ls) != 0 {
		t.Fatalf("leases before any guest: %+v", ls)
	}

	// The same subnet again: refused before libvirt sees it.
	if msg := cli(t, nil, true, "net", "add", "e2e-clash", "--type", "nat", "--address", n.Address); !strings.Contains(msg, "overlaps") {
		t.Errorf("an overlapping subnet: %q", msg)
	}
	// A bridge name the host already has: libvirt can't start it, and it
	// isn't kept.
	if msg := cli(t, nil, true, "net", "add", "e2e-clash", "--type", "isolated", "--bridge", n.Bridge, "--address", "10.78.0.1/24"); msg == "" {
		t.Error("a network on another network's bridge was accepted")
	}
	if _, ok := network(t, "e2e-clash"); ok {
		t.Error("the network that didn't start was kept")
	}
	for what, args := range map[string][]string{
		"the same name":     {"e2e-nat", "--type", "isolated"},
		"a bad type":        {"e2e-clash", "--type", "open"},
		"a bad DHCP range":  {"e2e-clash", "--type", "nat", "--address", "10.79.0.1/24", "--dhcp", "10.80.0.2-10.80.0.9"},
		"bridge, no bridge": {"e2e-clash", "--type", "bridge"},
	} {
		if msg := cli(t, nil, true, append([]string{"net", "add"}, args...)...); msg == "" {
			t.Errorf("%s: accepted", what)
		}
	}

	// A guest asks for an address and its lease shows, with its name.
	g := netGuest(t, s, "e2e-net-pxe", "e2e-nat")
	if err := hv.Power(ctx(t), s, g.UUID, hv.Start); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for len(ls) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no DHCP lease after 90 s")
		}
		time.Sleep(time.Second)
		cli(t, &ls, false, "net", "leases", "e2e-nat")
	}
	if l := ls[0]; l.Machine != "e2e-net-pxe" || l.Type != "ipv4" || l.MAC == "" || l.Expires == nil ||
		!strings.HasPrefix(l.IP, strings.TrimSuffix(n.Address, "1/24")) {
		t.Fatalf("lease: %+v (network %s)", l, n.Address)
	}

	// Stopping it under a running machine needs --force.
	if msg := cli(t, nil, true, "net", "stop", "e2e-nat"); !strings.Contains(msg, "e2e-net-pxe") || !strings.Contains(msg, "--force") {
		t.Errorf("stopping a network in use: %q", msg)
	}
	n = netCLI(t, "stop", "e2e-nat", "--force")
	if n.Active {
		t.Fatalf("stopped: %+v", n)
	}
	if msg := cli(t, nil, true, "net", "leases", "e2e-nat"); !strings.Contains(msg, "stopped") {
		t.Errorf("leases of a stopped network: %q", msg)
	}
	hv.Power(ctx(t), s, g.UUID, hv.ForceOff)
	n = netCLI(t, "start", "e2e-nat")
	n = netCLI(t, "autostart", "e2e-nat", "off")
	if !n.Active || n.Autostart {
		t.Fatalf("started, autostart off: %+v", n)
	}

	// Routed through one interface, and isolated without DHCP.
	n = netCLI(t, "add", "e2e-route", "--type", "route", "--address", "10.77.0.1/24", "--dev", "eth0", "--dhcp", "10.77.0.100-10.77.0.199")
	if n.Forward != "route" || n.Dev != "eth0" || n.Address != "10.77.0.1/24" || n.DHCP != "10.77.0.100-10.77.0.199" || !n.Active {
		t.Fatalf("route: %+v", n)
	}
	n = netCLI(t, "add", "e2e-iso", "--type", "isolated", "--dhcp", "off")
	if n.Forward != "" || n.DHCP != "" || n.Address == "" || !n.Active {
		t.Fatalf("isolated: %+v", n)
	}

	// A bridge the host has: a machine on the network is plugged into it.
	podmanExec(t, "ip", "link", "add", "e2e-br0", "type", "bridge")
	t.Cleanup(func() {
		podman("exec", env(t, "VIRT_MM_E2E_CONTAINER"), "ip", "link", "del", "e2e-br0").Run()
	})
	podmanExec(t, "ip", "link", "set", "e2e-br0", "up")
	n = netCLI(t, "add", "e2e-br", "--type", "bridge", "--bridge", "e2e-br0")
	if n.Forward != "bridge" || n.Bridge != "e2e-br0" || n.Address != "" || !n.Active {
		t.Fatalf("bridge: %+v", n)
	}
	b := netGuest(t, s, "e2e-net-br", "e2e-br")
	if err := hv.Power(ctx(t), s, b.UUID, hv.Start); err != nil {
		t.Fatal(err)
	}
	if ports := podmanExec(t, "ip", "-br", "link", "show", "master", "e2e-br0"); !strings.Contains(ports, "vnet") {
		t.Errorf("the machine's interface isn't on the host bridge: %q", ports)
	}
	if msg := cli(t, nil, true, "net", "rm", "e2e-br"); !strings.Contains(msg, "e2e-net-br") {
		t.Errorf("removing a network in use: %q", msg)
	}
	cli(t, nil, false, "net", "rm", "e2e-br", "--force")

	for _, name := range []string{"e2e-nat", "e2e-route", "e2e-iso"} {
		cli(t, nil, false, "net", "rm", name)
		if _, ok := network(t, name); ok {
			t.Errorf("%s still listed after rm", name)
		}
	}
}
