//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// The networks tab under virt-mm serve: networks of each kind added,
// started, stopped (refused while a machine is on one, then forced),
// autostarted and removed; a guest's lease, a fixed address, and the
// definition edited.
func TestServeNetworks(t *testing.T) {
	s := qemuSession(t)
	nets := []string{"e2e-net-page", "e2e-net-route", "e2e-net-iso", "e2e-net-br"}
	t.Cleanup(func() {
		for _, n := range nets {
			hv.RemoveNetwork(ctx(t), s, n, true)
		}
	})
	sv := serve(t)

	var n hv.Network
	sv.call(&n, "AddNetwork", sv.conn, hv.NewNetwork{Name: "e2e-net-page", Type: "nat", Address: "10.231.99.1/24"})
	if n.Forward != "nat" || !n.Active || n.DHCP == "" {
		t.Fatalf("AddNetwork nat: %+v", n)
	}
	var ns []hv.Network
	sv.call(&ns, "Networks", sv.conn)
	if !slices.ContainsFunc(ns, func(x hv.Network) bool { return x.Name == "e2e-net-page" }) {
		t.Fatalf("Networks: %+v", ns)
	}

	// A guest on it: its lease, then a fixed address for it.
	g := netGuest(t, s, "e2e-net-page-pxe", "e2e-net-page")
	sv.call(nil, "Power", sv.conn, g.UUID, "start")
	mac := devices(t, s, g.UUID).NICs[0].MAC
	var ls []hv.Lease
	for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(2 * time.Second) {
		sv.call(&ls, "Leases", sv.conn, "e2e-net-page")
		if len(ls) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no lease")
		}
	}
	if !strings.EqualFold(ls[0].MAC, mac) || ls[0].Machine != "e2e-net-page-pxe" || !strings.HasPrefix(ls[0].IP, "10.231.99.") {
		t.Errorf("Leases: %+v", ls)
	}
	var ed hv.Edited
	sv.call(&ed, "AddDHCPHost", sv.conn, "e2e-net-page", mac, "10.231.99.50", "pxe")
	if !ed.Active || len(ed.Changes) != 1 || !ed.Changes[0].Live {
		t.Errorf("AddDHCPHost: %+v", ed)
	}
	var hs []hv.DHCPHost
	sv.call(&hs, "DHCPHosts", sv.conn, "e2e-net-page")
	if len(hs) != 1 || hs[0].IP != "10.231.99.50" || hs[0].Name != "pxe" || !hs[0].Live || !hs[0].Defined {
		t.Errorf("DHCPHosts: %+v", hs)
	}
	if err := sv.try(nil, "AddDHCPHost", sv.conn, "e2e-net-page", "52:54:00:00:00:01", "10.231.98.5", ""); err == nil {
		t.Error("a fixed address outside the subnet")
	}
	sv.call(&ed, "RemoveDHCPHost", sv.conn, "e2e-net-page", mac)
	sv.call(&hs, "DHCPHosts", sv.conn, "e2e-net-page")
	if len(hs) != 0 {
		t.Errorf("after RemoveDHCPHost: %+v", hs)
	}

	// The definition: edited; an edit from an older read refused.
	var def hv.NetworkDefinition
	sv.call(&def, "NetworkDefinition", sv.conn, "e2e-net-page")
	if !def.Active || !strings.Contains(def.XML, "<name>e2e-net-page</name>") {
		t.Fatalf("NetworkDefinition: %+v", def)
	}
	x := strings.Replace(def.XML, "</network>", "<domain name='page.lan'/></network>", 1)
	var saved hv.NetworkDefinition
	sv.call(&saved, "RedefineNetwork", sv.conn, "e2e-net-page", def.XML, x)
	if !strings.Contains(saved.XML, "page.lan") {
		t.Errorf("RedefineNetwork: %s", saved.XML)
	}
	if err := sv.try(nil, "RedefineNetwork", sv.conn, "e2e-net-page", def.XML, x); err == nil {
		t.Error("saved over a definition changed since it was read")
	}

	// Stopping it with the guest on it: refused with the reason, then
	// forced; started again, autostart on.
	err := sv.try(nil, "SetNetwork", sv.conn, "e2e-net-page", hv.NetworkStop, false)
	if err == nil || !strings.Contains(err.Error(), "network in use") || !strings.Contains(err.Error(), "e2e-net-page-pxe") {
		t.Errorf("stopping a network in use: %v", err)
	}
	if err := sv.try(nil, "RemoveNetwork", sv.conn, "e2e-net-page", false); err == nil || !strings.Contains(err.Error(), "network in use") {
		t.Errorf("removing a network in use: %v", err)
	}
	sv.call(&n, "SetNetwork", sv.conn, "e2e-net-page", hv.NetworkStop, true)
	if n.Active {
		t.Errorf("forced stop: %+v", n)
	}
	sv.call(&n, "SetNetwork", sv.conn, "e2e-net-page", hv.NetworkStart, false)
	sv.call(&n, "SetNetwork", sv.conn, "e2e-net-page", hv.NetworkAutostartOn, false)
	if !n.Active || !n.Autostart {
		t.Errorf("started, autostart on: %+v", n)
	}
	// Its guest lost its interface's link to the network with the stop:
	// nothing is on it now.
	sv.call(nil, "RemoveNetwork", sv.conn, "e2e-net-page", false)

	// The other kinds.
	sv.call(&n, "AddNetwork", sv.conn, hv.NewNetwork{Name: "e2e-net-route", Type: "route", Address: "10.231.97.1/24"})
	if n.Forward != "route" || !n.Active {
		t.Errorf("route: %+v", n)
	}
	n = hv.Network{} // what it hasn't is left out of the JSON
	sv.call(&n, "AddNetwork", sv.conn, hv.NewNetwork{Name: "e2e-net-iso", Type: "isolated", NoDHCP: true})
	if n.Forward != "" || n.DHCP != "" || !n.Active {
		t.Errorf("isolated: %+v", n)
	}
	podmanExec(t, "ip", "link", "add", "e2e-pbr0", "type", "bridge")
	t.Cleanup(func() {
		podman("exec", env(t, "VIRT_MM_E2E_CONTAINER"), "ip", "link", "del", "e2e-pbr0").Run()
	})
	podmanExec(t, "ip", "link", "set", "e2e-pbr0", "up")
	var brs []string
	sv.call(&brs, "Bridges", sv.conn)
	if !slices.Contains(brs, "e2e-pbr0") {
		t.Fatalf("Bridges: %v", brs)
	}
	n = hv.Network{}
	sv.call(&n, "AddNetwork", sv.conn, hv.NewNetwork{Name: "e2e-net-br", Type: "bridge", Bridge: "e2e-pbr0"})
	if n.Forward != "bridge" || n.Bridge != "e2e-pbr0" || !n.Active {
		t.Errorf("bridge: %+v", n)
	}
	if err := sv.try(nil, "AddNetwork", sv.conn, hv.NewNetwork{Name: "e2e-net-bad", Type: "bridge"}); err == nil {
		t.Error("a bridge network without its bridge")
	}
	for _, name := range nets[1:] {
		sv.call(nil, "RemoveNetwork", sv.conn, name, false)
	}
	sv.call(&ns, "Networks", sv.conn)
	if slices.ContainsFunc(ns, func(x hv.Network) bool { return strings.HasPrefix(x.Name, "e2e-net-") }) {
		t.Errorf("after RemoveNetwork: %+v", ns)
	}
}
