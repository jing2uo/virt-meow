//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// page is what a browser does with virt-mm serve: calls and events.
type page struct {
	t    *testing.T
	base string
}

// call runs a Service method and decodes its result into v (when not nil);
// a failed call fails the test.
func (p page) call(v any, method string, args ...any) {
	p.t.Helper()
	if err := p.try(v, method, args...); err != nil {
		p.t.Fatal(err)
	}
}

// try runs a Service method as call does, returning its error instead.
func (p page) try(v any, method string, args ...any) error {
	p.t.Helper()
	if args == nil {
		args = []any{}
	}
	b, _ := json.Marshal(args)
	r, err := http.Post(p.base+"/api/"+method, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		var e struct{ Error string }
		json.NewDecoder(r.Body).Decode(&e)
		return fmt.Errorf("%s: %d %s", method, r.StatusCode, e.Error)
	}
	if v != nil {
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			return fmt.Errorf("%s: %v", method, err)
		}
	}
	return nil
}

type event struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

// await reads events until one named name for which match is true.
func await(t *testing.T, ws *websocket.Conn, name string, match func(json.RawMessage) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for a %s event: %v", name, err)
		}
		var ev event
		json.Unmarshal(b, &ev)
		if ev.Name == name && match(ev.Data) {
			return
		}
	}
}

// virt-mm serve, the binary, on the container's libvirtd: a connection made
// and opened from the page, its machines, their events, and a file a
// browser uploads into a pool.
// served is virt-mm serve, the binary, on the container's libvirtd as a
// browser has it: the page's calls, its event stream, and a connection to
// the container over SSH, made, trusted and opened from the page.
type served struct {
	page
	listen string
	ws     *websocket.Conn
	conn   string // the connection's id
}

// startServe runs cmd, a virt-mm serve, and returns the first line it
// prints, decoded.
func startServe(t *testing.T, cmd *exec.Cmd) struct{ Listen, URL string } {
	t.Helper()
	pipe, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
			t.Error("Ctrl+C did not stop virt-mm serve")
		}
	})
	line := bufio.NewScanner(pipe)
	if !line.Scan() {
		t.Fatalf("virt-mm serve printed nothing: %v", line.Err())
	}
	var where struct{ Listen, URL string }
	if err := json.Unmarshal(line.Bytes(), &where); err != nil || !strings.HasPrefix(where.Listen, "127.0.0.1:") {
		t.Fatalf("virt-mm serve: %s", line.Text())
	}
	return where
}

func serve(t *testing.T) served {
	t.Helper()
	sshHome(t) // before virtmm(): serve reaches the host over SSH, as a remote one
	where := startServe(t, virtmm(t, "serve", "--listen", "127.0.0.1:0"))
	sv := served{page: page{t, "http://" + where.Listen}, listen: where.Listen}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+where.Listen+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	sv.ws = ws

	// Over SSH, the host's key shown to the page and trusted from it.
	var c struct{ ID string }
	sv.call(&c, "AddConnection", "e2e", "qemu+ssh://root@"+env(t, "VIRT_MM_E2E_SSH")+"/system", false)
	sv.conn = c.ID
	sv.call(nil, "Connect", c.ID)
	var fp string
	await(t, ws, "connection", func(d json.RawMessage) bool {
		var ci struct {
			ID      string
			HostKey *struct{ Fingerprint string } `json:"host_key"`
		}
		json.Unmarshal(d, &ci)
		if ci.ID == c.ID && ci.HostKey != nil {
			fp = ci.HostKey.Fingerprint
		}
		return fp != ""
	})
	sv.call(nil, "TrustHostKey", c.ID, fp)
	await(t, ws, "connection", func(d json.RawMessage) bool {
		var ci struct{ ID, Status string }
		json.Unmarshal(d, &ci)
		return ci.ID == c.ID && ci.Status == "connected"
	})
	return sv
}

// virt-mm serve, the binary, on the container's libvirtd: a connection made
// and opened from the page, its machines, their events, and a file a
// browser uploads into a pool.
func TestServe(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-web")
	sv := serve(t)
	p, ws, where := sv.page, sv.ws, struct{ Listen string }{sv.listen}
	c := struct{ ID string }{sv.conn}

	r, err := http.Get(p.base + "/")
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("the page: %v %v", r, err)
	}
	r.Body.Close()
	for _, f := range []string{"/app.js", "/rpc.js", "/vendor/novnc/core/rfb.js"} {
		if r, err := http.Get(p.base + f); err != nil || r.StatusCode != 200 {
			t.Fatalf("%s: %v %v", f, r, err)
		} else {
			r.Body.Close()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A machine made here shows in the page's list, and starting it from
	// the page is an event there.
	cr, err := hv.Create(ctx, s, hv.Spec{Name: "e2e-web", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-web", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx, s, cr.Domain.UUID, true) })
	var rows []struct{ Name, UUID, State string }
	p.call(&rows, "Domains", c.ID)
	found := false
	for _, d := range rows {
		found = found || d.Name == "e2e-web"
	}
	if !found {
		t.Fatalf("Domains: %+v", rows)
	}
	p.call(nil, "Power", c.ID, cr.Domain.UUID, "start")
	await(t, ws, "domain", func(d json.RawMessage) bool {
		var ev struct {
			Connection string
			Event      struct{ UUID string }
		}
		json.Unmarshal(d, &ev)
		return ev.Connection == c.ID && ev.Event.UUID == cr.Domain.UUID
	})
	p.call(nil, "Power", c.ID, cr.Domain.UUID, "force-off")

	// Its VNC console: a WebSocket the page reaches under the same address.
	var con struct{ URL, Token string }
	p.call(nil, "Power", c.ID, cr.Domain.UUID, "start")
	p.call(&con, "OpenConsole", c.ID, cr.Domain.UUID, "vnc")
	if !strings.HasPrefix(con.URL, "/console/") {
		t.Fatalf("console URL %q: want a path, relative to the page", con.URL)
	}
	cws, _, err := websocket.Dial(ctx, "ws://"+where.Listen+con.URL, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatal(err)
	}
	rfb(t, websocket.NetConn(ctx, cws, websocket.MessageBinary))
	cws.CloseNow()
	p.call(nil, "CloseConsole", con.Token)
	p.call(nil, "Power", c.ID, cr.Domain.UUID, "force-off")

	// A file from the browser's computer, into a pool.
	data := bytes.Repeat([]byte("virt-mm"), 400_000)
	req, _ := http.NewRequest(http.MethodPost, p.base+"/api/upload?connection="+c.ID+"&pool=e2e-web&name=web.iso", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("upload: %v %v", res, err)
	}
	res.Body.Close()
	await(t, ws, "upload", func(d json.RawMessage) bool {
		var u struct{ Name, State string }
		json.Unmarshal(d, &u)
		return u.Name == "web.iso" && u.State == "done"
	})
	vols, err := hv.Volumes(ctx, s, "e2e-web")
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, v := range vols {
		ok = ok || (v.Name == "web.iso" && v.Capacity == uint64(len(data)))
	}
	if !ok {
		t.Fatalf("after the upload: %+v", vols)
	}
}
