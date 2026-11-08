//go:build e2e

package e2e

import (
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// A VNC console with a password: set and cleared from the CLI (on the
// command line and from stdin) and the page, at once on a running
// machine, which QEMU's VNC server then asks for and checks.
func TestVNCPassword(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-vncpw")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-vncpw", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-vncpw", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	vnc := func() hv.Graphics {
		t.Helper()
		d := devices(t, s, uuid)
		i := slices.IndexFunc(d.Graphics, func(g hv.Graphics) bool { return g.Type == "vnc" })
		if i < 0 {
			t.Fatalf("no VNC: %+v", d.Graphics)
		}
		return d.Graphics[i]
	}

	// Off: set on the command line, with the warning; too long refused.
	var e hv.Edited
	cli(t, &e, false, "device", "set", "e2e-vncpw", "graphics", "vnc", "--password", "secret1")
	if !vnc().Password || len(e.Warnings) != 1 || !strings.Contains(e.Warnings[0], "doesn't encrypt") || e.Changes[0].Live {
		t.Fatalf("set while off: %+v", e)
	}
	if msg := cli(t, nil, true, "device", "set", "e2e-vncpw", "graphics", "vnc", "--password", "123456789"); !strings.Contains(msg, "at most 8") {
		t.Errorf("a 9-character VNC password: %q", msg)
	}

	// Running: QEMU asks for it, and takes only the right one.
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	ss := sshSession(t)
	login := func(pw string) error {
		t.Helper()
		vc, _, err := hv.DialGraphics(ctx(t), ss, uuid, "vnc")
		if err != nil {
			t.Fatal(err)
		}
		defer vc.Close()
		return vncLogin(vc, pw)
	}
	if err := login("secret1"); err != nil {
		t.Fatalf("the right password: %v", err)
	}
	if err := login("wrong"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a wrong password: %v", err)
	}

	// Changed from stdin (--password -): now, in the running console.
	cmd := virtmm(t, "device", "set", "e2e-vncpw", "graphics", "vnc", "--password", "-", "--password-stdin")
	cmd.Stdin = strings.NewReader("other\n")
	if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), `"live": true`) {
		t.Fatalf("--password - from stdin: %v %s", err, out)
	}
	if err := login("other"); err != nil {
		t.Fatalf("the changed password: %v", err)
	}
	if err := login("secret1"); err == nil {
		t.Fatal("the old password still works")
	}

	// The page sets and clears it.
	sv := serve(t)
	sv.call(&e, "SetGraphicsPassword", sv.conn, uuid, "vnc", "fromweb")
	if !e.Changes[0].Live || login("fromweb") != nil {
		t.Fatalf("SetGraphicsPassword: %+v", e)
	}
	if err := sv.try(nil, "SetGraphicsPassword", sv.conn, uuid, "vnc", "far too long"); err == nil {
		t.Fatal("SetGraphicsPassword took 12 characters")
	}
	// Cleared on a running machine: at its next start, saying so, since
	// QEMU would refuse everyone until then; the old password still works.
	e = hv.Edited{}
	sv.call(&e, "SetGraphicsPassword", sv.conn, uuid, "vnc", "")
	if vnc().Password || e.Changes[0].Live || len(e.Warnings) != 1 || !strings.Contains(e.Warnings[0], "next start") || login("fromweb") != nil {
		t.Fatalf("cleared while running: %+v, password %v", e, vnc().Password)
	}
	for _, a := range []hv.Action{hv.ForceOff, hv.Start} {
		if err := hv.Power(ctx(t), s, uuid, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := login(""); err != nil {
		t.Fatalf("after the next start, without a password: %v", err)
	}

	// Set on a machine that started without one: QEMU can't add it now,
	// and the change says so; it asks from the next start.
	e = hv.Edited{}
	cli(t, &e, false, "device", "set", "e2e-vncpw", "graphics", "vnc", "--password", "late")
	if len(e.Changes) != 1 || e.Changes[0].Live || !strings.Contains(e.Changes[0].Note, "started without a password") || !vnc().Password {
		t.Fatalf("set on a console that started without one: %+v", e)
	}
	if err := login(""); err != nil {
		t.Fatalf("that console until the next start: %v", err)
	}
}

// sshSession is a session to the container's qemu:///system over SSH, its
// host key trusted, for the consoles (on the container's loopback).
func sshSession(t *testing.T) *conn.Session {
	t.Helper()
	sshHome(t)
	hk := &conn.HostKeys{Own: filepath.Join(t.TempDir(), "known_hosts")}
	tg, _ := conn.Parse("qemu+ssh://root@" + env(t, "VIRT_MM_E2E_SSH") + "/system")
	ss := conn.New(tg, conn.Options{HostKeys: hk})
	t.Cleanup(func() { ss.Close() })
	var unknown *conn.UnknownHostKeyError
	if err := ss.Connect(ctx(t)); errors.As(err, &unknown) {
		hk.Trust(unknown.Addr, unknown.Key)
	} else if err != nil {
		t.Fatal(err)
	}
	return ss
}

// vncLogin logs in to a VNC server as a viewer does: with no
// authentication when it offers that and pw is "", else with VNC's
// challenge and pw. An error says why it didn't get in.
func vncLogin(c net.Conn, pw string) error {
	c.SetDeadline(time.Now().Add(15 * time.Second))
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := io.ReadFull(c, b)
		return b, err
	}
	if _, err := read(12); err != nil {
		return err
	}
	c.Write([]byte("RFB 003.008\n"))
	n, err := read(1)
	if err != nil || n[0] == 0 {
		return fmt.Errorf("security types: %v %v", n, err)
	}
	types, _ := read(int(n[0]))
	want := byte(1)
	if pw != "" || !slices.Contains(types, 1) {
		want = 2
	}
	if !slices.Contains(types, want) {
		return fmt.Errorf("security types %v, want %d", types, want)
	}
	c.Write([]byte{want})
	if want == 2 {
		challenge, err := read(16)
		if err != nil {
			return err
		}
		// VNC's DES: the password, NUL-padded to 8 bytes, each byte's
		// bits reversed, as the key.
		key := make([]byte, 8)
		copy(key, pw)
		for i := range key {
			key[i] = bits.Reverse8(key[i])
		}
		block, _ := des.NewCipher(key)
		resp := make([]byte, 16)
		block.Encrypt(resp[:8], challenge[:8])
		block.Encrypt(resp[8:], challenge[8:])
		c.Write(resp)
	}
	r, err := read(4)
	if err != nil {
		return err
	}
	if binary.BigEndian.Uint32(r) != 0 {
		return errors.New("the server refused the login")
	}
	return nil
}
