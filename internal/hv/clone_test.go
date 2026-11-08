package hv

import "testing"

func TestVolumeCopyName(t *testing.T) {
	for _, c := range [][4]string{
		{"web.qcow2", "web", "web2", "web2.qcow2"},
		{"web-data.qcow2", "web", "db", "db-data.qcow2"},
		{"disk0.img", "web", "db", "db-disk0.img"},
	} {
		if got := volumeCopyName(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("volumeCopyName(%q, %q, %q) = %q, want %q", c[0], c[1], c[2], got, c[3])
		}
	}
}
