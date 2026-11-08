package hv

import (
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestNFSSource(t *testing.T) {
	for src, want := range map[string][2]string{
		"nas:/srv/vms":           {"nas", "/srv/vms"},
		"10.0.0.5:/export/":      {"10.0.0.5", "/export"},
		"[fd00::5]:/a/../b":      {"fd00::5", "/b"},
		"nas.lan:/":              {"nas.lan", "/"},
		"nas.example.com:/a b/c": {"nas.example.com", "/a b/c"},
	} {
		h, d, err := nfsSource(src)
		if err != nil || h != want[0] || d != want[1] {
			t.Errorf("%q: %q %q %v, want %q", src, h, d, err, want)
		}
	}
	for _, bad := range []string{"", "nas", "nas:", "nas:relative", ":/x", "[]:/x", "a b:/x", "/x:/y"} {
		if _, _, err := nfsSource(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
	nfs := libvirtxml.StoragePool{Type: "netfs", Source: &libvirtxml.StoragePoolSource{
		Host: []libvirtxml.StoragePoolSourceHost{{Name: "fd00::5"}}, Dir: &libvirtxml.StoragePoolSourceDir{Path: "/x"}}}
	if s := poolSource(nfs); s != "[fd00::5]:/x" {
		t.Errorf("poolSource: %q", s)
	}
}
