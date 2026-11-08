package hv

import (
	"net/netip"
	"strings"
	"testing"
)

func TestNetworkDef(t *testing.T) {
	used := []netip.Prefix{netip.MustParsePrefix("192.168.100.0/24"), netip.MustParsePrefix("10.0.0.0/16")}
	for _, c := range []struct {
		nn                  NewNetwork
		forward, addr, dhcp string
		prefix              uint
		err                 string
	}{
		{nn: NewNetwork{Name: "n", Type: "nat"}, forward: "nat", addr: "192.168.101.1", prefix: 24, dhcp: "192.168.101.2-192.168.101.254"},
		{nn: NewNetwork{Name: "n", Type: "route", Address: "172.16.5.1/28", Dev: "eth0"}, forward: "route", addr: "172.16.5.1", prefix: 28, dhcp: "172.16.5.2-172.16.5.14"},
		{nn: NewNetwork{Name: "n", Type: "isolated", Address: "172.16.0.10/24", DHCP: "172.16.0.100-172.16.0.199"}, addr: "172.16.0.10", prefix: 24, dhcp: "172.16.0.100-172.16.0.199"},
		{nn: NewNetwork{Name: "n", Type: "isolated", Address: "172.16.0.1/24", NoDHCP: true}, addr: "172.16.0.1", prefix: 24},
		{nn: NewNetwork{Name: "n", Type: "bridge", Bridge: "br0"}, forward: "bridge"},
		{nn: NewNetwork{Name: "n", Type: "bridge"}, err: "--bridge"},
		{nn: NewNetwork{Name: "n", Type: "bridge", Bridge: "br0", Address: "10.1.0.1/24"}, err: "no address"},
		{nn: NewNetwork{Name: "n", Type: "nat", Address: "10.0.3.1/24"}, err: "overlaps 10.0.0.0/16"},
		{nn: NewNetwork{Name: "n", Type: "nat", Address: "172.16.0.0/24"}, err: "subnet's own"},
		{nn: NewNetwork{Name: "n", Type: "nat", Address: "172.16.0.1"}, err: "address"},
		{nn: NewNetwork{Name: "n", Type: "nat", Address: "172.16.0.254/24"}, err: "no addresses left"},
		{nn: NewNetwork{Name: "n", Type: "nat", Address: "172.16.0.50/24", DHCP: "172.16.0.10-172.16.0.99"}, err: "host's own address"},
		{nn: NewNetwork{Name: "n", Type: "nat", Address: "172.16.0.1/24", DHCP: "172.16.1.10-172.16.1.99"}, err: "within"},
		{nn: NewNetwork{Name: "n", Type: "isolated", Dev: "eth0"}, err: "no interface"},
		{nn: NewNetwork{Name: "n", Type: "open"}, err: "network type"},
		{nn: NewNetwork{Name: "a/b", Type: "nat"}, err: "network name"},
		{nn: NewNetwork{Name: "n", Type: "nat", Bridge: "a-bridge-name-too-long"}, err: "bridge name"},
	} {
		def, err := networkDef(c.nn, used)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%+v: %v, want %q", c.nn, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%+v: %v", c.nn, err)
			continue
		}
		forward := ""
		if def.Forward != nil {
			forward = def.Forward.Mode
		}
		var addr, dhcp string
		var prefix uint
		if len(def.IPs) > 0 {
			addr, prefix = def.IPs[0].Address, def.IPs[0].Prefix
			if d := def.IPs[0].DHCP; d != nil {
				dhcp = d.Ranges[0].Start + "-" + d.Ranges[0].End
			}
		}
		if forward != c.forward || addr != c.addr || prefix != c.prefix || dhcp != c.dhcp {
			t.Errorf("%+v: forward %q, %s/%d, DHCP %q", c.nn, forward, addr, prefix, dhcp)
		}
	}
}
