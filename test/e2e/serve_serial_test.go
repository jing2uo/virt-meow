//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jing2uo/virt-meow/internal/api"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// The serial console in the page under virt-mm serve: the OVMF shell over
// the console bridge's WebSocket, typed into; a second page refused, then
// taking it over with force; and the host still answering after the pages
// went while the guest wrote (docs/adr/0002).
func TestServeSerial(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-serial-page")
	sv := serve(t)
	cr, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-serial-page", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-serial-page", DiskGiB: 1, NetKind: "none", Firmware: "uefi"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := cr.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if err := sv.try(nil, "OpenSerial", sv.conn, uuid, false); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("the serial console of a machine that is off: %v", err)
	}
	sv.call(nil, "Power", sv.conn, uuid, "start")
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })

	// open opens a page's terminal: its WebSocket, read into a screen
	// until it closes.
	type term struct {
		ws     *websocket.Conn
		out    *screen
		closed chan struct{}
		token  string
	}
	open := func(force bool) term {
		t.Helper()
		var c api.Console
		sv.call(&c, "OpenSerial", sv.conn, uuid, force)
		if c.Kind != "serial" || !strings.HasPrefix(c.URL, "/console/") {
			t.Fatalf("OpenSerial: %+v", c)
		}
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ws, _, err := websocket.Dial(dctx, "ws://"+sv.listen+c.URL, &websocket.DialOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			t.Fatal(err)
		}
		tm := term{ws: ws, out: &screen{}, closed: make(chan struct{}), token: c.Token}
		go func() {
			defer close(tm.closed)
			for {
				_, b, err := ws.Read(context.Background())
				if err != nil {
					return
				}
				tm.out.Write(b)
			}
		}()
		t.Cleanup(func() { ws.CloseNow() })
		return tm
	}
	write := func(tm term, s string) {
		t.Helper()
		if err := tm.ws.Write(ctx(t), websocket.MessageBinary, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	gone := func(tm term, what string) {
		t.Helper()
		select {
		case <-tm.closed:
		case <-time.After(30 * time.Second):
			t.Fatalf("%s: still open", what)
		}
	}

	a := open(false)
	a.out.waitFor(t, "Shell>", 1)
	write(a, "echo virt-mm-web\r")
	a.out.waitFor(t, "\r\nvirt-mm-web\r\n", 1)

	// A second page: told, in its terminal, that another has it.
	b := open(false)
	gone(b, "a second console")
	if out := b.out.String(); !strings.Contains(out, "[virt-mm:") || !strings.Contains(out, "force") {
		t.Errorf("a second console: %q", out)
	}
	// With force it takes it over; the first ends.
	f := open(true)
	gone(a, "the console taken over")
	write(f, "echo virt-mm-forced\r")
	f.out.waitFor(t, "\r\nvirt-mm-forced\r\n", 1)

	// The page goes while the shell writes: the stream finishes before
	// anything else, and the host still answers.
	write(f, "help\r")
	f.ws.Close(websocket.StatusNormalClosure, "")
	sv.call(nil, "CloseConsole", f.token)
	sv.call(nil, "CloseConsole", a.token)
	sv.call(nil, "CloseConsole", b.token)
	var in api.MachineInfo
	sv.call(&in, "Info", sv.conn, uuid)
	if in.State != hv.Running {
		t.Errorf("after the console: %s", in.State)
	}
	// And it can be opened again.
	g := open(false)
	write(g, "\r") // a prompt once the shell is done with help
	g.out.waitFor(t, "Shell>", 1)
	write(g, "echo virt-mm-again\r")
	g.out.waitFor(t, "\r\nvirt-mm-again\r\n", 1)

	// A machine without a serial port: said so in the terminal.
	x := `<domain type="qemu"><name>e2e-serial-none</name><memory unit="MiB">64</memory><vcpu>1</vcpu>
<os><type arch="x86_64" machine="pc">hvm</type></os></domain>`
	f2 := filepath.Join(t.TempDir(), "none.xml")
	if err := os.WriteFile(f2, []byte(x), 0o644); err != nil {
		t.Fatal(err)
	}
	cli(t, nil, false, "define", f2)
	none := machine(t, s, "e2e-serial-none")
	t.Cleanup(func() {
		hv.Power(ctx(t), s, none.UUID, hv.ForceOff)
		hv.Delete(ctx(t), s, none.UUID, false)
	})
	if err := hv.Power(ctx(t), s, none.UUID, hv.Start); err != nil {
		t.Fatal(err)
	}
	uuid = none.UUID
	n := open(false)
	gone(n, "no serial port")
	if out := n.out.String(); !strings.Contains(out, "has no serial console") {
		t.Errorf("a machine without a serial port: %q", out)
	}
}
