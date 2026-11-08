package hv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Screenshot is what a running machine's display shows, as PNG.
type Screenshot struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	PNG    []byte `json:"png"`
}

// screenshotTime bounds a screenshot's stream. It doesn't follow the
// caller's context: a connection closed while libvirtd still writes a
// stream can deadlock libvirtd (see Console), and a screen is a few MiB.
const screenshotTime = 2 * time.Minute

// TakeScreenshot reads the display screen (0: the first) of a running
// machine through libvirt's stream, the same over SSH as locally, and
// converts what QEMU gives (PPM, or PNG) to PNG.
func TakeScreenshot(ctx context.Context, sess *conn.Session, uuid string, screen uint32) (Screenshot, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return Screenshot{}, err
	}
	call, cancel := context.WithTimeout(context.WithoutCancel(ctx), screenshotTime)
	defer cancel()
	var buf bytes.Buffer
	err = sess.Do(call, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		state, _, err := lv.DomainGetState(dom, 0)
		if err != nil {
			return err
		}
		if !stateOf(libvirt.DomainState(state)).Active() {
			return fmt.Errorf("%s is not running: there is no screen to take", dom.Name)
		}
		_, err = lv.DomainScreenshot(dom, &buf, screen, 0)
		// go-libvirt decodes the reply (the MIME type) from the stream's
		// closing packet, which is empty, and fails with its XDR decoder's
		// error once the whole image is in. The image says what it is
		// instead, and a cut-short one fails to decode below.
		var le libvirt.Error
		if err != nil && buf.Len() > 0 && !errors.As(err, &le) && strings.HasPrefix(err.Error(), "xdr:") {
			err = nil
		}
		return err
	})
	if err != nil {
		return Screenshot{}, err
	}
	return toPNG(buf.Bytes())
}

// toPNG converts a PPM or PNG image to PNG.
func toPNG(b []byte) (Screenshot, error) {
	var img image.Image
	var err error
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG")):
		if img, err = png.Decode(bytes.NewReader(b)); err != nil {
			return Screenshot{}, err
		}
		r := img.Bounds()
		return Screenshot{Width: r.Dx(), Height: r.Dy(), PNG: b}, nil
	case bytes.HasPrefix(b, []byte("P6")):
		if img, err = decodePPM(b); err != nil {
			return Screenshot{}, err
		}
	default:
		return Screenshot{}, fmt.Errorf("the screenshot is neither PPM nor PNG (%d bytes)", len(b))
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&out, img); err != nil {
		return Screenshot{}, err
	}
	r := img.Bounds()
	return Screenshot{Width: r.Dx(), Height: r.Dy(), PNG: out.Bytes()}, nil
}

// decodePPM reads a binary PPM (P6) with 8-bit samples, what QEMU's
// screendump writes.
func decodePPM(b []byte) (image.Image, error) {
	r := bufio.NewReader(bytes.NewReader(b))
	var magic string
	var w, h, max int
	// The header is whitespace-separated, with # comments.
	fields := make([]string, 0, 4)
	for len(fields) < 4 {
		var tok []byte
		for {
			c, err := r.ReadByte()
			if err != nil {
				return nil, errors.New("PPM: header cut short")
			}
			if c == '#' && len(tok) == 0 {
				r.ReadString('\n')
				continue
			}
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				if len(tok) > 0 {
					break
				}
				continue
			}
			tok = append(tok, c)
		}
		fields = append(fields, string(tok))
	}
	magic = fields[0]
	if _, err := fmt.Sscan(fields[1]+" "+fields[2]+" "+fields[3], &w, &h, &max); err != nil || magic != "P6" {
		return nil, fmt.Errorf("PPM: bad header %q", fields)
	}
	if w <= 0 || h <= 0 || w > 1<<14 || h > 1<<14 || max <= 0 || max > 255 {
		return nil, fmt.Errorf("PPM: %dx%d, max %d: not a screen", w, h, max)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	px := make([]byte, 3*w)
	for y := range h {
		if _, err := io.ReadFull(r, px); err != nil {
			return nil, fmt.Errorf("PPM: row %d of %d: %w", y, h, err)
		}
		row := img.Pix[y*img.Stride:]
		for x := range w {
			for c := range 3 {
				row[4*x+c] = byte(int(px[3*x+c]) * 255 / max)
			}
			row[4*x+3] = 0xff
		}
	}
	return img, nil
}

// Thumbnail scales a screenshot down to width pixels wide (its height in
// proportion), averaging the pixels each one covers: the small picture of
// a machine in a list, a few KiB rather than the screen's MiB. One no
// wider than width is returned as it is.
func Thumbnail(s Screenshot, width int) (Screenshot, error) {
	if width < 1 {
		return Screenshot{}, fmt.Errorf("width %d", width)
	}
	if s.Width <= width {
		return s, nil
	}
	src, err := png.Decode(bytes.NewReader(s.PNG))
	if err != nil {
		return Screenshot{}, err
	}
	b := src.Bounds()
	w, h := width, max(1, b.Dy()*width/b.Dx())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		y0, y1 := b.Min.Y+y*b.Dy()/h, b.Min.Y+max((y+1)*b.Dy()/h, y*b.Dy()/h+1)
		for x := range w {
			x0, x1 := b.Min.X+x*b.Dx()/w, b.Min.X+max((x+1)*b.Dx()/w, x*b.Dx()/w+1)
			var r, g, bl, a, n uint32
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r, g, bl, a, n = r+cr, g+cg, bl+cb, a+ca, n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8), uint8(a/n>>8)
		}
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&out, dst); err != nil {
		return Screenshot{}, err
	}
	return Screenshot{Width: w, Height: h, PNG: out.Bytes()}, nil
}
