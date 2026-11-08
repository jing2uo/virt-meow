//go:build e2e

package e2e

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// The clipboard shared with the console: device add|rm clipboard changes
// the qemu-vdagent channel now on a running machine, and QEMU's VNC server
// offers the extended clipboard noVNC speaks.
func TestClipboard(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-clip")
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-clip", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-clip", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })

	// The container's QEMU is 10.0, whose qemu-vdagent blocks saving and
	// snapshotting a running machine: new machines go without it (10.1
	// gives it to them, DomainXML's unit test says), it is added only when
	// asked for, with a warning, and what it blocks says how to undo it.
	h, err := hv.HostInfo(ctx(t), s)
	if err != nil {
		t.Fatal(err)
	}
	if h.Clipboard || devices(t, s, uuid).Clipboard {
		t.Fatalf("QEMU %s: new machines share the clipboard", h.Hypervisor)
	}
	var e hv.Edited
	cli(t, &e, false, "device", "add", "e2e-clip", "clipboard")
	if len(e.Changes) != 1 || len(e.Warnings) != 1 || !strings.Contains(e.Warnings[0], "older than 10.1") || !devices(t, s, uuid).Clipboard {
		t.Fatalf("added while off: %+v", e)
	}
	var ls struct{ Clipboard bool }
	cli(t, &ls, false, "device", "ls", "e2e-clip")
	if !ls.Clipboard {
		t.Fatal("device ls: no clipboard")
	}
	if msg := cli(t, nil, true, "device", "add", "e2e-clip", "clipboard"); !strings.Contains(msg, "already") {
		t.Errorf("added twice: %q", msg)
	}
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	if err := hv.Save(ctx(t), s, uuid); err == nil || !strings.Contains(err.Error(), "device rm e2e-clip clipboard") {
		t.Fatalf("saved with qemu-vdagent on QEMU 10.0: %v", err)
	}
	if _, err := hv.CreateSnapshot(ctx(t), s, uuid, "s1", ""); err == nil || !strings.Contains(err.Error(), "device rm e2e-clip clipboard") {
		t.Fatalf("snapshot with qemu-vdagent on QEMU 10.0: %v", err)
	}

	// Running: removed now and added now, in the running machine too.
	e = hv.Edited{}
	cli(t, &e, false, "device", "rm", "e2e-clip", "clipboard")
	if len(e.Changes) != 1 || !e.Changes[0].Live || liveClipboard(t, s, uuid) || devices(t, s, uuid).Clipboard {
		t.Fatalf("removed from a running machine: %+v", e)
	}
	if msg := cli(t, nil, true, "device", "rm", "e2e-clip", "clipboard"); !strings.Contains(msg, "doesn't share") {
		t.Errorf("removed twice: %q", msg)
	}
	e = hv.Edited{}
	cli(t, &e, false, "device", "add", "e2e-clip", "clipboard")
	if len(e.Changes) != 1 || !e.Changes[0].Live {
		t.Fatalf("added to a running machine: %+v", e)
	}
	if !liveClipboard(t, s, uuid) {
		t.Fatal("the running machine has no qemu-vdagent channel")
	}

	// noVNC's side: QEMU's VNC server, reached over SSH as the console is,
	// answers the extended clipboard's pseudo-encoding with its
	// capabilities and takes a client's text.
	sshHome(t)
	hk := &conn.HostKeys{Own: filepath.Join(t.TempDir(), "known_hosts")}
	tg, _ := conn.Parse("qemu+ssh://root@" + env(t, "VIRT_MM_E2E_SSH") + "/system")
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
	defer vc.Close()
	flags, err := extendedClipboard(vc)
	if err != nil {
		t.Fatal(err)
	}
	const caps, text = 1 << 24, 1 // the caps action, and the text format
	if flags&caps == 0 || flags&text == 0 {
		t.Fatalf("the extended clipboard's flags: %#x, want caps and text", flags)
	}

	// The page under serve adds and removes it.
	mustEdit(t)(hv.RemoveClipboard(ctx(t), s, uuid))
	sv := serve(t)
	sv.call(&e, "AddClipboard", sv.conn, uuid)
	if !e.Changes[0].Live || !liveClipboard(t, s, uuid) {
		t.Fatalf("AddClipboard: %+v", e)
	}
	var dev hv.Devices
	sv.call(&dev, "Devices", sv.conn, uuid)
	if !dev.Clipboard {
		t.Fatal("Devices: no clipboard")
	}
	sv.call(&e, "RemoveClipboard", sv.conn, uuid)
	if liveClipboard(t, s, uuid) {
		t.Fatal("RemoveClipboard: still in the running machine")
	}
}

// liveClipboard says whether the running machine has a qemu-vdagent channel.
func liveClipboard(t *testing.T, s *conn.Session, uuid string) bool {
	t.Helper()
	var x string
	u, _ := hv.ParseUUID(uuid)
	err := s.Do(ctx(t), func(lv *libvirt.Libvirt) error {
		d, err := lv.DomainLookupByUUID(u)
		if err == nil {
			x, err = lv.DomainGetXMLDesc(d, 0)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(x, "type='qemu-vdagent'")
}

// extendedClipboard logs in to a VNC server without a password, asks for
// the extended clipboard (as noVNC does), returns the flags of the
// capabilities the server answers with, then sends it a text.
func extendedClipboard(c net.Conn) (uint32, error) {
	c.SetDeadline(time.Now().Add(15 * time.Second))
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := io.ReadFull(c, b)
		return b, err
	}
	if _, err := read(12); err != nil {
		return 0, err
	}
	c.Write([]byte("RFB 003.008\n"))
	n, err := read(1)
	if err != nil || n[0] == 0 {
		return 0, fmt.Errorf("security types: %v %v", n, err)
	}
	types, _ := read(int(n[0]))
	if !strings.ContainsRune(string(types), 1) {
		return 0, fmt.Errorf("no security type None in %v", types)
	}
	c.Write([]byte{1})
	if r, err := read(4); err != nil || binary.BigEndian.Uint32(r) != 0 {
		return 0, fmt.Errorf("security result %v %v", r, err)
	}
	c.Write([]byte{1}) // shared
	init, err := read(24)
	if err != nil {
		return 0, err
	}
	if _, err := read(int(binary.BigEndian.Uint32(init[20:]))); err != nil {
		return 0, err
	}
	// SetEncodings: raw, and the extended clipboard's pseudo-encoding.
	enc := []byte{2, 0, 0, 2, 0, 0, 0, 0}
	enc = binary.BigEndian.AppendUint32(enc, 0xC0A1E5CE)
	c.Write(enc)
	for {
		h, err := read(1)
		if err != nil {
			return 0, fmt.Errorf("waiting for the clipboard's capabilities: %w", err)
		}
		if h[0] != 3 {
			return 0, fmt.Errorf("server message %d before the clipboard's capabilities", h[0])
		}
		b, err := read(7)
		if err != nil {
			return 0, err
		}
		length := int32(binary.BigEndian.Uint32(b[3:]))
		if length >= 0 { // plain ServerCutText
			read(int(length))
			continue
		}
		payload, err := read(int(-length))
		if err != nil || len(payload) < 4 {
			return 0, fmt.Errorf("extended ServerCutText: %v", err)
		}
		// ClientCutText, plain Latin-1: taken without complaint.
		msg := append([]byte{6, 0, 0, 0}, binary.BigEndian.AppendUint32(nil, 5)...)
		if _, err := c.Write(append(msg, "hello"...)); err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint32(payload), nil
	}
}
