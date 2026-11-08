package api

import (
	"testing"

	"github.com/jing2uo/virt-meow/internal/store"
)

func TestRememberView(t *testing.T) {
	t.Setenv("VIRT_MM_CONFIG", t.TempDir())
	s := &Service{c: &Core{opts: Options{Mode: Desktop}}}
	if err := store.UpdateWindow(func(w *store.Window) { w.Width, w.Height = 900, 700 }); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberView("/0b0872ec/machine/web%2001/disks"); err != nil {
		t.Fatal(err)
	}
	w, err := store.LoadWindow()
	if err != nil || w.View != "/0b0872ec/machine/web%2001/disks" || w.Width != 900 {
		t.Errorf("saved %+v, %v", w, err)
	}
	for _, bad := range []string{"/x\"><script>", "javascript:alert(1)", "/a b"} {
		if err := s.RememberView(bad); err == nil {
			t.Errorf("remembered %q", bad)
		}
	}
	s.c.opts.Mode = Web
	if err := s.RememberView("/all"); err == nil {
		t.Error("serve remembered a view")
	}
}
