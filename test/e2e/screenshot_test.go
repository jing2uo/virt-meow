//go:build e2e

package e2e

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm screenshot: the firmware's screen, as a PNG with something on it.
func TestScreenshot(t *testing.T) {
	s := qemuSession(t)
	const name = "e2e-shot"
	scratchPool(t, s, name)
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: name, OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: name, DiskGiB: 1, NetKind: "none", Firmware: "uefi"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	file := filepath.Join(t.TempDir(), "shot.png")
	if msg := cli(t, nil, true, "screenshot", name, file); !strings.Contains(msg, "not running") {
		t.Errorf("a machine that is off: %q", msg)
	}
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	var con screen
	openConsole(t, s, uuid, &con)
	con.waitFor(t, "Shell>", 1) // the UEFI shell: its prompt stays on the screen

	// The firmware clears the screen between its steps: a blank one is
	// taken again, for a while.
	var res struct {
		File          string
		Width, Height int
	}
	for deadline := time.Now().Add(60 * time.Second); ; {
		os.Remove(file)
		cli(t, &res, false, "screenshot", name, file)
		img := decodePNG(t, file)
		b := img.Bounds()
		if res.File != file || b.Dx() != res.Width || b.Dy() != res.Height || res.Width < 640 || res.Height < 480 {
			t.Fatalf("%+v, image %v", res, b)
		}
		if colours(img) > 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("only blank screens for 60 s")
		}
		time.Sleep(time.Second)
	}
	small := filepath.Join(t.TempDir(), "small.png")
	cli(t, &res, false, "screenshot", name, small, "--width", "160")
	if b := decodePNG(t, small).Bounds(); res.Width != 160 || b.Dx() != 160 || b.Dy() != res.Height || res.Height >= 160 {
		t.Errorf("--width 160: %+v, image %v", res, b)
	}
	if msg := cli(t, nil, true, "screenshot", name, file); !strings.Contains(msg, "exists") {
		t.Errorf("over an existing file: %q", msg)
	}
	if msg := cli(t, nil, true, "screenshot", name, filepath.Join(t.TempDir(), "x.png"), "--screen", "7"); msg == "" {
		t.Error("a screen it doesn't have")
	}
}

func decodePNG(t *testing.T, file string) image.Image {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return img
}

// colours counts the colours of every fourth pixel in each direction.
func colours(img image.Image) int {
	seen := map[[3]uint32]bool{}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			seen[[3]uint32{r, g, bl}] = true
		}
	}
	return len(seen)
}
