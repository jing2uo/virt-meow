//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm pool add --type netfs: defined only. NFS can't be mounted in an
// unprivileged container (the kernel allows no NFS mount from a user
// namespace), so mounting an export is not tested: starting the pool
// fails here, and the test checks that it fails cleanly.
func TestNFSPool(t *testing.T) {
	s := qemuSession(t)
	t.Cleanup(func() {
		for _, n := range []string{"e2e-nfs", "e2e-nfs6", "e2e-nfs-up"} {
			hv.RemovePool(ctx(t), s, n)
		}
	})
	var p hv.Pool
	cli(t, &p, false, "pool", "add", "e2e-nfs", "--type", "netfs", "--source", "nas.invalid:/srv/vms/", "--no-start")
	if p.Type != "netfs" || p.Active || p.Source != "nas.invalid:/srv/vms" || p.Path != "/var/lib/libvirt/images/e2e-nfs" {
		t.Fatalf("added: %+v", p)
	}
	var x string
	s.Do(ctx(t), func(lv *libvirt.Libvirt) error {
		pl, err := lv.StoragePoolLookupByName("e2e-nfs")
		if err == nil {
			x, err = lv.StoragePoolGetXMLDesc(pl, libvirt.StorageXMLInactive)
		}
		return err
	})
	for _, want := range []string{"<host name='nas.invalid'/>", "<dir path='/srv/vms'/>", "<format type='nfs'/>", "<path>/var/lib/libvirt/images/e2e-nfs</path>"} {
		if !strings.Contains(x, want) {
			t.Errorf("no %s in\n%s", want, x)
		}
	}
	if out := podmanExec(t, "stat", "-c", "%F", "/var/lib/libvirt/images/e2e-nfs"); !strings.Contains(out, "directory") {
		t.Errorf("mount point: %s", out)
	}

	cli(t, &p, false, "pool", "add", "e2e-nfs6", "--type", "netfs", "--source", "[fd00::5]:/export", "--path", "/mnt/e2e-nfs6", "--no-start")
	if p.Source != "[fd00::5]:/export" || p.Path != "/mnt/e2e-nfs6" {
		t.Fatalf("IPv6, own mount point: %+v", p)
	}

	if msg := cli(t, nil, true, "pool", "start", "e2e-nfs"); msg == "" {
		t.Error("mounted NFS in the container")
	}
	// Started at once: refused, and not kept.
	if msg := cli(t, nil, true, "pool", "add", "e2e-nfs-up", "--type", "netfs", "--source", "nas.invalid:/srv"); !strings.Contains(msg, "starting the pool") {
		t.Errorf("an export that can't be mounted: %q", msg)
	}
	var ps []hv.Pool
	cli(t, &ps, false, "pools")
	if slices.ContainsFunc(ps, func(p hv.Pool) bool { return p.Name == "e2e-nfs-up" }) {
		t.Error("kept a pool that didn't start")
	}
	for _, src := range []string{"nas", "nas:relative", ":/x"} {
		if msg := cli(t, nil, true, "pool", "add", "e2e-nfs-bad", "--type", "netfs", "--source", src, "--no-start"); !strings.Contains(msg, "host:/export") && !strings.Contains(msg, "not a host") {
			t.Errorf("source %q: %q", src, msg)
		}
	}
	cli(t, nil, false, "pool", "rm", "e2e-nfs")
	cli(t, &ps, false, "pools")
	if slices.ContainsFunc(ps, func(p hv.Pool) bool { return p.Name == "e2e-nfs" }) {
		t.Error("not removed")
	}
}
