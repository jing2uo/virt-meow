//go:build e2e

package e2e

import (
	"bytes"
	"image/png"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// The list's small pictures under virt-mm serve: a running machine's screen
// scaled to the width asked, as PNG; none of a machine that is off.
func TestServeThumbnail(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-thumb-page")
	sv := serve(t)
	cr, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-thumb-page", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-thumb-page", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := cr.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if err := sv.try(nil, "Thumbnail", sv.conn, uuid, 200); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("the picture of a machine that is off: %v", err)
	}
	sv.call(nil, "Power", sv.conn, uuid, "start")
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })

	var th hv.Screenshot
	sv.call(&th, "Thumbnail", sv.conn, uuid, 200)
	img, err := png.Decode(bytes.NewReader(th.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if th.Width != 200 || img.Bounds().Dx() != 200 || img.Bounds().Dy() != th.Height || th.Height < 100 || th.Height > 200 {
		t.Errorf("Thumbnail: %dx%d, image %v", th.Width, th.Height, img.Bounds())
	}
	if len(th.PNG) > 64<<10 {
		t.Errorf("a 200-pixel picture of %d bytes", len(th.PNG))
	}
	if err := sv.try(nil, "Thumbnail", sv.conn, uuid, 0); err == nil {
		t.Error("width 0")
	}
}
