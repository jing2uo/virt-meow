//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/console"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// rfb reads a VNC server's greeting from c.
func rfb(t *testing.T, c net.Conn) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	b := make([]byte, 12)
	if _, err := io.ReadFull(c, b); err != nil || !strings.HasPrefix(string(b), "RFB 003.") {
		t.Fatalf("VNC greeting: %q, %v", b, err)
	}
}

// virt-mm vnc: the machine's VNC console, on the host's loopback only,
// reached over SSH (direct-tcpip). (A local connection dials the same
// address on this computer; the container's loopback isn't this
// computer's, so only SSH can be tested here.)
func TestVNC(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-vnc")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-vnc", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-vnc", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	if msg := cli(t, nil, true, "vnc", "e2e-vnc"); !strings.Contains(msg, "not running") {
		t.Errorf("vnc of a machine that is off: %q", msg)
	}
	// On every address of the host: reached on its loopback.
	mustEdit(t)(hv.SetGraphicsListen(ctx(t), s, uuid, "vnc", "0.0.0.0"))
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	v, err := hv.FindGraphics(ctx(t), s, uuid, "vnc")
	if err != nil || v.Network != "tcp" || !strings.HasPrefix(v.Addr, "127.0.0.1:59") {
		t.Fatalf("VNC server: %+v, %v", v, err)
	}

	// Over SSH, through hv.
	sshHome(t)
	cfg := t.TempDir()
	hk := &conn.HostKeys{Own: filepath.Join(cfg, "known_hosts")}
	uri := "qemu+ssh://root@" + env(t, "VIRT_MM_E2E_SSH") + "/system"
	tg, _ := conn.Parse(uri)
	ss := conn.New(tg, conn.Options{HostKeys: hk})
	defer ss.Close()
	var unknown *conn.UnknownHostKeyError
	if err := ss.Connect(ctx(t)); errors.As(err, &unknown) {
		hk.Trust(unknown.Addr, unknown.Key)
	} else if err != nil {
		t.Fatal(err)
	}
	vc, _, err := hv.DialGraphics(ctx(t), ss, uuid, "vnc")
	if err != nil {
		t.Fatal(err)
	}
	rfb(t, vc)
	vc.Close()

	// As the page's noVNC reaches it: a WebSocket bridged over the same
	// SSH connection, once per token use.
	bridge := console.New()
	web := httptest.NewServer(bridge)
	defer web.Close()
	token := bridge.Open(func(c context.Context) (net.Conn, error) {
		nc, _, err := hv.DialGraphics(c, ss, uuid, "vnc")
		return nc, err
	})
	for range 2 {
		ws, _, err := websocket.Dial(ctx(t), "ws"+strings.TrimPrefix(web.URL, "http")+console.Path(token),
			&websocket.DialOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			t.Fatal(err)
		}
		rfb(t, websocket.NetConn(ctx(t), ws, websocket.MessageBinary))
		ws.CloseNow()
	}
	bridge.Close(token)

	// Through the CLI, to a port of our choosing, two viewers at once.
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()
	cmd := virtmm(t, "vnc", "e2e-vnc", "-c", uri, "--port", strconv.Itoa(port))
	cmd.Env = append(cmd.Env, "VIRT_MM_CONFIG="+cfg) // the trusted key
	pipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	line := bufio.NewScanner(pipe)
	if !line.Scan() {
		t.Fatalf("virt-mm vnc printed nothing: %v", line.Err())
	}
	var where struct{ Listen, VNC string }
	if err := json.Unmarshal(line.Bytes(), &where); err != nil || where.Listen != "127.0.0.1:"+strconv.Itoa(port) || where.VNC != v.Addr {
		t.Fatalf("virt-mm vnc: %s (%v)", line.Text(), err)
	}
	var viewers []net.Conn
	for range 2 {
		c, err := net.Dial("tcp", where.Listen)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		viewers = append(viewers, c)
	}
	for _, c := range viewers {
		rfb(t, c)
	}
	// Ctrl+C ends it.
	cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("after Ctrl+C: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Ctrl+C did not stop virt-mm vnc")
	}

	// A machine without VNC has nothing to forward.
	mustEdit(t)(hv.RemoveGraphics(ctx(t), s, uuid, "vnc"))
	hv.Power(ctx(t), s, uuid, hv.ForceOff)
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	if msg := cli(t, nil, true, "vnc", "e2e-vnc"); !strings.Contains(msg, "no VNC") {
		t.Errorf("without VNC: %q", msg)
	}
}
