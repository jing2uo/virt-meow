//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// The storage tab under virt-mm serve: pools added (dir; logical and netfs
// as far as the container goes), stopped, started, autostarted and
// removed; volumes created, grown, uploaded, downloaded by the browser
// and deleted.
func TestServeStorage(t *testing.T) {
	s := qemuSession(t)
	sv := serve(t)
	const pool = "e2e-store-page"
	t.Cleanup(func() {
		for _, v := range []string{"blank.qcow2", "up.iso"} {
			hv.DeleteVolume(ctx(t), s, pool, v)
		}
		hv.RemovePool(ctx(t), s, pool)
		hv.RemovePool(ctx(t), s, "e2e-store-nfs")
	})

	var p hv.Pool
	sv.call(&p, "AddPool", sv.conn, hv.NewPool{Name: pool, Type: "dir", Path: "/var/lib/libvirt/images/" + pool})
	if !p.Active || p.Autostart || p.Type != "dir" {
		t.Fatalf("AddPool: %+v", p)
	}
	var ps []hv.Pool
	sv.call(&ps, "Pools", sv.conn)
	if !slices.ContainsFunc(ps, func(x hv.Pool) bool { return x.Name == pool }) {
		t.Fatalf("Pools: no %s in %+v", pool, ps)
	}
	sv.call(&p, "SetPool", sv.conn, pool, hv.PoolAutostartOn)
	sv.call(&p, "SetPool", sv.conn, pool, hv.PoolStop)
	if p.Active || !p.Autostart {
		t.Errorf("stopped, autostart on: %+v", p)
	}
	if err := sv.try(nil, "Volumes", sv.conn, pool); err == nil {
		t.Error("the volumes of a stopped pool")
	}
	sv.call(&p, "SetPool", sv.conn, pool, hv.PoolStart)
	if !p.Active {
		t.Errorf("started again: %+v", p)
	}

	// Volumes: a blank one, grown; one uploaded, downloaded as it went in.
	var v hv.Volume
	sv.call(&v, "CreateVolume", sv.conn, pool, "blank.qcow2", uint64(1<<30), "")
	if v.Format != "qcow2" || v.Capacity != 1<<30 {
		t.Errorf("CreateVolume: %+v", v)
	}
	sv.call(&v, "ResizeVolume", sv.conn, pool, "blank.qcow2", uint64(2<<30))
	if v.Capacity != 2<<30 {
		t.Errorf("ResizeVolume: %+v", v)
	}
	if err := sv.try(nil, "ResizeVolume", sv.conn, pool, "blank.qcow2", uint64(1<<30)); err == nil {
		t.Error("shrank a volume")
	}
	data := bytes.Repeat([]byte("virt-mm-download "), 64<<10)
	req, _ := http.NewRequest(http.MethodPost, sv.base+"/api/upload?connection="+sv.conn+"&pool="+pool+"&name=up.iso", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != 200 {
		t.Fatalf("upload: %v %v", r, err)
	} else {
		r.Body.Close()
	}
	var vs []hv.Volume
	sv.call(&vs, "Volumes", sv.conn, pool)
	if names := volNames(vs); !slices.Equal(names, []string{"blank.qcow2", "up.iso"}) {
		t.Errorf("Volumes: %v", names)
	}
	q := url.Values{"connection": {sv.conn}, "pool": {pool}, "name": {"up.iso"}}
	r, err := http.Get(sv.base + "/api/download?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !bytes.Equal(got, data) || !strings.Contains(r.Header.Get("Content-Disposition"), "up.iso") {
		t.Errorf("download: %d, %d bytes of %d, %q", r.StatusCode, len(got), len(data), r.Header.Get("Content-Disposition"))
	}
	q.Set("name", "nope.iso")
	if r, err := http.Get(sv.base + "/api/download?" + q.Encode()); err != nil || r.StatusCode != 500 {
		t.Errorf("downloading no such volume: %v %v", r, err)
	} else {
		var e struct{ Error string }
		json.NewDecoder(r.Body).Decode(&e)
		r.Body.Close()
		if !strings.Contains(e.Error, "nope.iso") {
			t.Errorf("downloading no such volume: %q", e.Error)
		}
	}
	// The window's download needs a save dialog serve hasn't got.
	if err := sv.try(nil, "PickDownload", sv.conn, pool, "up.iso"); err == nil || !strings.Contains(err.Error(), "browser") {
		t.Errorf("PickDownload under serve: %v", err)
	}
	sv.call(nil, "DeleteVolume", sv.conn, pool, "blank.qcow2")
	sv.call(&vs, "Volumes", sv.conn, pool)
	if names := volNames(vs); !slices.Equal(names, []string{"up.iso"}) {
		t.Errorf("after DeleteVolume: %v", names)
	}

	// netfs defined only (no NFS mount in the container); a logical pool
	// without its volume group refused and not kept.
	sv.call(&p, "AddPool", sv.conn, hv.NewPool{Name: "e2e-store-nfs", Type: "netfs", Source: "nas.invalid:/srv/vms", NoStart: true})
	if p.Type != "netfs" || p.Active || p.Source != "nas.invalid:/srv/vms" {
		t.Errorf("netfs: %+v", p)
	}
	if err := sv.try(nil, "AddPool", sv.conn, hv.NewPool{Name: "e2e-store-lvm", Type: "logical", Source: "e2e-novg"}); err == nil {
		t.Error("a logical pool without its volume group")
	}
	sv.call(nil, "RemovePool", sv.conn, "e2e-store-nfs")
	sv.call(nil, "RemovePool", sv.conn, pool)
	sv.call(&ps, "Pools", sv.conn)
	if slices.ContainsFunc(ps, func(x hv.Pool) bool { return strings.HasPrefix(x.Name, "e2e-store") }) {
		t.Errorf("after RemovePool: %+v", ps)
	}
}

func volNames(vs []hv.Volume) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Name)
	}
	slices.Sort(out)
	return out
}
