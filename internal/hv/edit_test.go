package hv

import (
	"os"
	"strings"
	"testing"
)

// Definitions as libvirt writes them survive libvirtxml unchanged, so an
// edit through it loses nothing.
func TestLosslessKeepsLibvirtsOwn(t *testing.T) {
	for _, f := range []string{"testdata/web01.xml", "testdata/rich.xml"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := lossless(string(b)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// What libvirtxml doesn't know is caught, not dropped.
func TestLosslessRefusesLoss(t *testing.T) {
	for x, want := range map[string]string{
		`<domain type="kvm"><name>a</name><wormhole/></domain>`:                 "/domain/wormhole",
		`<domain type="kvm"><name>a</name><memory flux="9">1</memory></domain>`: "/domain/memory/@flux",
	} {
		err := lossless(x)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want it to name %s", x, err, want)
		}
	}
}

func TestKibOf(t *testing.T) {
	for _, c := range []struct {
		v    uint
		unit string
		want uint64
	}{{4, "GiB", 4 << 20}, {512, "MiB", 512 << 10}, {1, "", 1}, {2048, "bytes", 2}, {1, "G", 1 << 20}} {
		if got, err := kibOf(c.v, c.unit); err != nil || got != c.want {
			t.Errorf("%d %s: %d, %v", c.v, c.unit, got, err)
		}
	}
}

func TestNextTarget(t *testing.T) {
	used := map[string]bool{"vda": true, "vdb": true, "sda": true}
	if got := nextTarget("vd", used); got != "vdc" {
		t.Errorf("got %s", got)
	}
	for c := 'a'; c <= 'z'; c++ {
		used["vd"+string(c)] = true
	}
	if got := nextTarget("vd", used); got != "vdaa" {
		t.Errorf("after vdz: got %s", got)
	}
}
