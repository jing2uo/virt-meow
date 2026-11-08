//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/api"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm fs: a folder of the host (the container's) shared with the agent
// guest as virtiofs, the shared memory set up for it, and the guest
// (booted with virt-mm.virtiofs=<tag>) mounting it and finding the file
// put there; then removed.
func TestFilesystemCLI(t *testing.T) {
	s := qemuSession(t)
	podmanExec(t, "sh", "-c", "mkdir -p /srv/e2e-share && echo hi > /srv/e2e-share/hello-virtiofs")
	t.Cleanup(func() { podmanExec(t, "rm", "-rf", "/srv/e2e-share") })
	const name = "e2e-fs"
	d, err := hv.Define(ctx(t), s, `<domain type="qemu"><name>`+name+`</name><memory unit="MiB">256</memory><vcpu>1</vcpu>
<os><type arch="x86_64" machine="q35">hvm</type><kernel>/usr/share/virt-mm-e2e/vmlinuz</kernel>
<initrd>/usr/share/virt-mm-e2e/initrd.gz</initrd><cmdline>console=ttyS0 quiet virt-mm.virtiofs=e2eshare</cmdline></os>
<devices><serial type="pty"/><console type="pty"/></devices></domain>`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, d.UUID, false) })

	var ed hv.Edited
	cli(t, &ed, false, "fs", "add", name, "/srv/e2e-share/", "e2eshare")
	if len(ed.Changes) != 2 || !strings.Contains(ed.Changes[0].What, "memory shared") || !strings.Contains(ed.Changes[1].What, "/srv/e2e-share → e2eshare") {
		t.Fatalf("fs add: %+v", ed)
	}
	var fs []hv.Filesystem
	cli(t, &fs, false, "fs", "ls", name)
	if len(fs) != 1 || fs[0] != (hv.Filesystem{Tag: "e2eshare", Source: "/srv/e2e-share", Driver: "virtiofs"}) {
		t.Fatalf("fs ls: %+v", fs)
	}
	if !strings.Contains(savedXML(t, s, d.UUID), `<access mode='shared'/>`) {
		t.Errorf("no shared memory:\n%s", savedXML(t, s, d.UUID))
	}
	if msg := cli(t, nil, true, "fs", "add", name, "/srv", "e2eshare"); !strings.Contains(msg, "already has") {
		t.Errorf("a tag twice: %s", msg)
	}
	if msg := cli(t, nil, true, "fs", "add", name, "srv/x", "t2"); !strings.Contains(msg, "absolute") {
		t.Errorf("a relative path: %s", msg)
	}

	if err := hv.Power(ctx(t), s, d.UUID, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, d.UUID, hv.ForceOff) })
	var con screen
	openConsole(t, s, d.UUID, &con)
	con.waitFor(t, "virt-mm-e2e: virtiofs e2eshare", 1)
	if !strings.Contains(con.String(), "virt-mm-e2e: virtiofs e2eshare: hello-virtiofs") {
		t.Errorf("the guest's mount: %s", con.String())
	}

	// Removed while it runs: from the next start.
	cli(t, &ed, false, "fs", "rm", name, "e2eshare")
	if len(ed.Changes) != 1 || ed.Changes[0].Live || !ed.Active {
		t.Errorf("fs rm on a running machine: %+v", ed)
	}
	cli(t, &fs, false, "fs", "ls", name)
	if len(fs) != 0 {
		t.Errorf("after fs rm: %+v", fs)
	}
}

// Shared folders on the details page, over virt-mm serve.
func TestServeFilesystems(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-fs-page")
	sv := serve(t)
	c, err := hv.Create(ctx(t), s, hv.Spec{Name: "e2e-fs-page", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-fs-page", DiskGiB: 1, NetKind: "none", Firmware: "bios"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, true) })
	uuid := c.Domain.UUID

	var ed hv.Edited
	sv.call(&ed, "AddFilesystem", sv.conn, uuid, "/srv/data", "data")
	sv.call(&ed, "AddFilesystem", sv.conn, uuid, "/srv/media", "media")
	if len(ed.Changes) != 1 { // the memory is already shared
		t.Errorf("a second folder: %+v", ed)
	}
	var in api.MachineInfo
	sv.call(&in, "Info", sv.conn, uuid)
	if tags := slices.Collect(func(yield func(string) bool) {
		for _, f := range in.Devices.Filesystems {
			yield(f.Tag + "=" + f.Source)
		}
	}); strings.Join(tags, " ") != "data=/srv/data media=/srv/media" {
		t.Errorf("Info's folders: %v", tags)
	}
	sv.call(&ed, "RemoveFilesystem", sv.conn, uuid, "data")
	if err := sv.try(nil, "RemoveFilesystem", sv.conn, uuid, "data"); err == nil {
		t.Error("removed a folder twice")
	}
	if err := sv.try(nil, "AddFilesystem", sv.conn, uuid, "/srv/x", strings.Repeat("t", 37)); err == nil {
		t.Error("a 37-character tag")
	}
	sv.call(&in, "Info", sv.conn, uuid)
	if len(in.Devices.Filesystems) != 1 || in.Devices.Filesystems[0].Tag != "media" {
		t.Errorf("after RemoveFilesystem: %+v", in.Devices.Filesystems)
	}
}
