//go:build e2e

package e2e

import "testing"

// Under serve the window's keyboard grab isn't there: GrabKeyboard says
// so, over HTTP and with the request's context, and the page takes the
// keys with the browser's Keyboard Lock instead (release-check presses
// them in headless Chrome).
func TestServeGrabKeyboard(t *testing.T) {
	where := startServe(t, virtmm(t, "serve", "--listen", "127.0.0.1:0"))
	p := page{t, "http://" + where.Listen}
	for _, on := range []bool{true, false} {
		var ok bool = true
		p.call(&ok, "GrabKeyboard", on)
		if ok {
			t.Fatalf("GrabKeyboard(%v) under serve: true, want false", on)
		}
	}
	if err := p.try(nil, "GrabKeyboard"); err == nil {
		t.Fatal("GrabKeyboard without its argument: no error")
	}
}
