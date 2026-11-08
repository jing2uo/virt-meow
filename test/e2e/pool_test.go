//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

func pool(t *testing.T, name string) (hv.Pool, bool) {
	t.Helper()
	var ps []hv.Pool
	cli(t, &ps, false, "pools")
	i := slices.IndexFunc(ps, func(p hv.Pool) bool { return p.Name == name })
	if i < 0 {
		return hv.Pool{}, false
	}
	return ps[i], true
}

// virt-mm pool and virt-mm vol: a directory pool's life, and its volumes'.
func TestPoolsAndVolumes(t *testing.T) {
	s := qemuSession(t)
	const dir = "/var/lib/libvirt/images/e2e-pool"
	t.Cleanup(func() {
		hv.RemovePool(ctx(t), s, "e2e-pool")
		// Its files stay with it gone; add it back for a moment to empty it.
		if _, err := hv.AddPool(ctx(t), s, hv.NewPool{Name: "e2e-pool", Type: "dir", Path: dir}); err == nil {
			vs, _ := hv.Volumes(ctx(t), s, "e2e-pool")
			for _, v := range vs {
				hv.DeleteVolume(ctx(t), s, "e2e-pool", v.Name)
			}
			hv.RemovePool(ctx(t), s, "e2e-pool")
		}
	})

	var p hv.Pool
	cli(t, &p, false, "pool", "add", "e2e-pool", "--type", "dir", "--path", dir, "--autostart", "on")
	if p.Type != "dir" || p.Path != dir || !p.Active || !p.Autostart {
		t.Fatalf("added: %+v", p)
	}
	cli(t, &p, false, "pool", "stop", "e2e-pool")
	if p.Active {
		t.Fatalf("stopped: %+v", p)
	}
	cli(t, &p, false, "pool", "start", "e2e-pool")
	cli(t, &p, false, "pool", "autostart", "e2e-pool", "off")
	if !p.Active || p.Autostart {
		t.Fatalf("started, autostart off: %+v", p)
	}
	for what, bad := range map[string]hv.NewPool{
		"the same name":      {Name: "e2e-pool", Type: "dir", Path: dir + "-2"},
		"a relative path":    {Name: "e2e-pool-2", Type: "dir", Path: "images"},
		"the root":           {Name: "e2e-pool-2", Type: "dir", Path: "/"},
		"a type":             {Name: "e2e-pool-2", Type: "zfs"},
		"a bad volume group": {Name: "e2e-pool-2", Type: "logical", Source: "vg/0"},
	} {
		if _, err := hv.AddPool(ctx(t), s, bad); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	// A volume group that isn't there: refused by LVM, and nothing kept.
	if msg := cli(t, nil, true, "pool", "add", "e2e-lvm", "--type", "logical", "--source", "e2e-novg"); !strings.Contains(msg, "starting the pool") {
		t.Errorf("logical pool without its volume group: %q", msg)
	}
	if _, ok := pool(t, "e2e-lvm"); ok {
		t.Error("the refused logical pool was kept")
	}

	// Volumes.
	var v hv.Volume
	cli(t, &v, false, "vol", "create", "e2e-pool/a.qcow2", "64M")
	if v.Format != "qcow2" || v.Capacity != 64<<20 {
		t.Fatalf("qcow2: %+v", v)
	}
	cli(t, &v, false, "vol", "create", "e2e-pool/b.img", "16M", "--format", "raw")
	if v.Format != "raw" || v.Capacity != 16<<20 {
		t.Fatalf("raw: %+v", v)
	}
	cli(t, &v, false, "vol", "resize", "e2e-pool/a.qcow2", "+64M")
	if v.Capacity != 128<<20 {
		t.Fatalf("grown: %+v", v)
	}
	if msg := cli(t, nil, true, "vol", "resize", "e2e-pool/a.qcow2", "32M"); !strings.Contains(msg, "only grows") {
		t.Errorf("shrinking: %q", msg)
	}
	if _, err := hv.CreateVolume(ctx(t), s, "e2e-pool", "a.qcow2", 1<<20, ""); err == nil {
		t.Error("created over an existing volume")
	}

	// Download what was uploaded.
	data := bytes.Repeat([]byte("virt-mm-download "), 50_000)
	if _, err := hv.Upload(ctx(t), s, "e2e-pool", "up.img", bytes.NewReader(data), int64(len(data)), nil); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "down.img")
	cli(t, nil, false, "vol", "download", "e2e-pool/up.img", file)
	if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded %d bytes (%v), want %d", len(got), err, len(data))
	}
	if msg := cli(t, nil, true, "vol", "download", "e2e-pool/up.img", file); !strings.Contains(msg, "exists") {
		t.Errorf("downloading over a file: %q", msg)
	}

	// A machine's disk can't be deleted, or grown under it while it runs.
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-pool-vm", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		ImportPool: "e2e-pool", ImportVolume: "a.qcow2", NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, false) })
	if msg := cli(t, nil, true, "vol", "rm", "e2e-pool/a.qcow2"); !strings.Contains(msg, "e2e-pool-vm") {
		t.Errorf("deleting a machine's disk: %q", msg)
	}
	if err := hv.Power(ctx(t), s, c.Domain.UUID, hv.Start); err != nil {
		t.Fatal(err)
	}
	if msg := cli(t, nil, true, "vol", "resize", "e2e-pool/a.qcow2", "+1M"); !strings.Contains(msg, "disk resize") {
		t.Errorf("growing a running machine's disk: %q", msg)
	}
	hv.Power(ctx(t), s, c.Domain.UUID, hv.ForceOff)
	hv.Delete(ctx(t), s, c.Domain.UUID, false)
	cli(t, nil, false, "vol", "rm", "e2e-pool/a.qcow2")
	if names := volumes(t, s, "e2e-pool"); slices.Contains(names, "a.qcow2") || !slices.Contains(names, "b.img") {
		t.Fatalf("after rm: %v", names)
	}

	// Removing the pool forgets it; its files stay, and come back with it.
	cli(t, nil, false, "pool", "rm", "e2e-pool")
	if _, ok := pool(t, "e2e-pool"); ok {
		t.Fatal("removed pool still listed")
	}
	cli(t, &p, false, "pool", "add", "e2e-pool", "--type", "dir", "--path", dir)
	if names := volumes(t, s, "e2e-pool"); !slices.Contains(names, "b.img") || !slices.Contains(names, "up.img") {
		t.Fatalf("the files after adding the pool again: %v", names)
	}
}
