//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// liveXML is a running machine's definition as it runs now.
func liveXML(t *testing.T, s *conn.Session, uuid string) string {
	t.Helper()
	u, _ := hv.ParseUUID(uuid)
	var x string
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
	return x
}

func savedXML(t *testing.T, s *conn.Session, uuid string) string {
	t.Helper()
	def, err := hv.GetDefinition(ctx(t), s, uuid)
	if err != nil {
		t.Fatal(err)
	}
	return def.XML
}

// virt-mm device attach|detach: any device from its XML, into the
// definition, and into the running machine when it can take it.
func TestAttachDevice(t *testing.T) {
	s := qemuSession(t)
	const name = "e2e-attach"
	scratchPool(t, s, name)
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: name, OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: name, DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	uuid := c.Domain.UUID
	t.Cleanup(func() { hv.Delete(ctx(t), s, uuid, true) })
	dir := t.TempDir()
	file := func(x string) string {
		p := filepath.Join(dir, "dev.xml")
		if err := os.WriteFile(p, []byte(x), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const kbd = "<input type='keyboard' bus='usb'/>"
	const sound = "<sound model='ich6'/>"
	usbKbd := func(x string) bool { return strings.Contains(x, "<input type='keyboard' bus='usb'>") }

	// Off: into the definition only, from a file and from stdin.
	var e hv.Edited
	cli(t, &e, false, "device", "attach", name, file(kbd))
	if e.Active || len(e.Changes) != 1 || e.Changes[0].What != "input (keyboard)" || !usbKbd(savedXML(t, s, uuid)) {
		t.Fatalf("attached while off: %+v", e)
	}
	cmd := virtmm(t, "device", "detach", name, "-")
	cmd.Stdin = strings.NewReader(kbd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("detach from stdin: %v: %s", err, stdout.String())
	}
	if usbKbd(savedXML(t, s, uuid)) {
		t.Fatal("detached from stdin, still defined")
	}
	for what, x := range map[string]string{
		"two devices":           kbd + kbd,
		"not XML":               "keyboard",
		"a whole machine":       "<domain type='kvm'/>",
		"what libvirt can't do": "<input type='keyboard' bus='nope'/>",
	} {
		if msg := cli(t, nil, true, "device", "attach", name, file(x)); msg == "" {
			t.Errorf("%s: no error", what)
		}
	}
	if msg := cli(t, nil, true, "device", "detach", name, file(kbd)); msg == "" {
		t.Error("detached a device it doesn't have")
	}

	// Running: a USB keyboard comes and goes now; sound waits for the
	// next start, and says so.
	if err := hv.Power(ctx(t), s, uuid, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, uuid, hv.ForceOff) })
	e = hv.Edited{}
	cli(t, &e, false, "attach-device", name, file(kbd))
	if !e.Active || !e.Changes[0].Live || !usbKbd(liveXML(t, s, uuid)) || !usbKbd(savedXML(t, s, uuid)) {
		t.Fatalf("hot-plugged: %+v", e)
	}
	e = hv.Edited{}
	cli(t, &e, false, "device", "attach", name, file(sound))
	if e.Changes[0].Live || !strings.Contains(e.Changes[0].Note, "not supported") ||
		!strings.Contains(savedXML(t, s, uuid), "<sound model='ich6'") || strings.Contains(liveXML(t, s, uuid), "<sound model='ich6'") {
		t.Fatalf("sound while running: %+v", e)
	}
	e = hv.Edited{}
	cli(t, &e, false, "device", "detach", name, file(kbd))
	if !e.Changes[0].Live || usbKbd(liveXML(t, s, uuid)) || usbKbd(savedXML(t, s, uuid)) {
		t.Fatalf("unplugged: %+v", e)
	}
	cli(t, nil, false, "detach-device", name, file(sound))
	if strings.Contains(savedXML(t, s, uuid), "<sound model='ich6'") {
		t.Fatal("sound detached, still defined")
	}
}
