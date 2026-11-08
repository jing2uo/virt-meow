package hv

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestToPNG(t *testing.T) {
	ppm := append([]byte("P6\n# QEMU\n2 1\n255\n"), 255, 0, 0, 0, 0, 255)
	s, err := toPNG(ppm)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(s.PNG))
	if err != nil || s.Width != 2 || s.Height != 1 {
		t.Fatalf("%dx%d, %v", s.Width, s.Height, err)
	}
	if r, _, _, _ := img.At(0, 0).RGBA(); r != 0xffff {
		t.Errorf("first pixel %v", img.At(0, 0))
	}
	if c := color.RGBAModel.Convert(img.At(1, 0)).(color.RGBA); c != (color.RGBA{0, 0, 255, 255}) {
		t.Errorf("second pixel %v", c)
	}
	again, err := toPNG(s.PNG) // PNG passes through
	if err != nil || !bytes.Equal(again.PNG, s.PNG) || again.Width != 2 {
		t.Errorf("PNG in: %+v, %v", again, err)
	}
	for _, bad := range [][]byte{nil, []byte("P6\n2 1\n255\n\x00"), []byte("P3\n1 1\n255\n0 0 0"), []byte("P6 0 0 255 "), []byte("GIF89a")} {
		if _, err := toPNG(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}

func TestThumbnail(t *testing.T) {
	// 640x400: left half white, right half black.
	src := image.NewRGBA(image.Rect(0, 0, 640, 400))
	for y := range 400 {
		for x := range 320 {
			src.Set(x, y, color.White)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, src)
	big := Screenshot{Width: 640, Height: 400, PNG: b.Bytes()}

	th, err := Thumbnail(big, 160)
	if err != nil {
		t.Fatal(err)
	}
	if th.Width != 160 || th.Height != 100 || len(th.PNG) >= len(big.PNG) {
		t.Fatalf("thumbnail %dx%d, %d bytes (from %d)", th.Width, th.Height, len(th.PNG), len(big.PNG))
	}
	img, err := png.Decode(bytes.NewReader(th.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 160 || img.Bounds().Dy() != 100 {
		t.Fatalf("decoded %v", img.Bounds())
	}
	if r, _, _, _ := img.At(10, 50).RGBA(); r>>8 != 255 {
		t.Errorf("left: %d, want white", r>>8)
	}
	if r, _, _, _ := img.At(150, 50).RGBA(); r>>8 != 0 {
		t.Errorf("right: %d, want black", r>>8)
	}
	if same, _ := Thumbnail(big, 800); same.Width != 640 {
		t.Errorf("not wider than asked: %d", same.Width)
	}
	if _, err := Thumbnail(big, 0); err == nil {
		t.Error("width 0")
	}
}
