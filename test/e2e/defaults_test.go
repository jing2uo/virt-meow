//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/hv"
	"github.com/jing2uo/virt-meow/internal/store"
)

// The defaults for new machines: set with virt-mm defaults, used by virt-mm
// create where its flags don't say, and by the page's form through the
// settings and CreateOptions under serve.
func TestNewMachineDefaults(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-def")
	cfg := t.TempDir()
	run := func(v any, wantErr bool, args ...string) string {
		t.Helper()
		cmd := virtmm(t, args...)
		cmd.Env = append(cmd.Env, "VIRT_MM_CONFIG="+cfg) // one config for all of them
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		err := cmd.Run()
		if (err != nil) != wantErr {
			t.Fatalf("virt-mm %v: %v %s", args, err, stdout.String())
		}
		if err != nil {
			var e struct{ Error string }
			json.Unmarshal(stdout.Bytes(), &e)
			return e.Error
		}
		if v != nil {
			if err := json.Unmarshal(stdout.Bytes(), v); err != nil {
				t.Fatalf("virt-mm %v: %v in %s", args, err, stdout.String())
			}
		}
		return ""
	}

	var nm store.NewMachine
	run(&nm, false, "defaults", "set", "--pool", "e2e-def", "--net", "none", "--disk-format", "raw", "--sound", "on",
		"--cpu-mode", "host-model", "--listen", "0.0.0.0", "--disk-bus", "sata")
	if nm.Pool != "e2e-def" || nm.DiskFormat != "raw" || !nm.Sound || nm.Listen != "0.0.0.0" || nm.DiskBus != "sata" {
		t.Fatalf("defaults set: %+v", nm)
	}
	for _, bad := range [][]string{{"--disk-bus", "floppy"}, {"--listen", "everywhere"}, {"--net", "wifi:x"}, {"--sound", "loud"}} {
		if msg := run(nil, true, append([]string{"defaults", "set"}, bad...)...); msg == "" {
			t.Errorf("defaults set %v: accepted", bad)
		}
	}

	// virt-mm create, its disk by size alone into the default pool.
	var c hv.Created
	run(&c, false, "create", "e2e-def", "--disk", "1G", "--firmware", "bios")
	t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, true) })
	if c.Disk != "e2e-def/e2e-def.img" {
		t.Fatalf("created: %+v", c)
	}
	// The container has no KVM: the CPU mode is said to be unused.
	if w := strings.Join(c.Warnings, "\n"); !strings.Contains(w, "0.0.0.0") || !strings.Contains(w, "no KVM") {
		t.Fatalf("warnings: %q", c.Warnings)
	}
	d := devices(t, s, c.Domain.UUID)
	if k := diskAt(t, d, "sda"); k.Format != "raw" || k.Bus != "sata" {
		t.Fatalf("disk: %+v", k)
	}
	if len(d.Sound) != 1 || d.Sound[0] != "ich9" || len(d.NICs) != 0 || d.Graphics[0].Listen != "0.0.0.0" {
		t.Fatalf("sound %v, NICs %v, graphics %+v", d.Sound, d.NICs, d.Graphics)
	}

	// The flags win over the defaults.
	var px struct{ XML string }
	run(&px, false, "create", "e2e-def2", "--disk", "1G", "--disk-format", "qcow2", "--sound", "off", "--listen", "127.0.0.1",
		"--disk-bus", "virtio", "--print-xml")
	if x := px.XML; !strings.Contains(x, "type='qcow2'") && !strings.Contains(x, `type="qcow2"`) || strings.Contains(x, "<sound") ||
		strings.Contains(x, "0.0.0.0") || !strings.Contains(x, "e2e-def2.qcow2") {
		t.Fatalf("flags over defaults:\n%s", x)
	}

	// The page: the settings dialog saves them, the form gets them with
	// the host's options, and the machine it asks for is made so.
	sv := serve(t)
	var st store.Settings
	sv.call(&st, "Settings")
	st.NewMachine = store.NewMachine{Pool: "e2e-def", Network: "none", DiskFormat: "raw", Sound: true}
	sv.call(&st, "SaveSettings", st)
	st.NewMachine.DiskFormat = "vmdk"
	if err := sv.try(nil, "SaveSettings", st); err == nil {
		t.Fatal("SaveSettings took disk format vmdk")
	}
	var o struct {
		Defaults store.NewMachine `json:"defaults"`
	}
	sv.call(&o, "CreateOptions", sv.conn)
	if o.Defaults.Pool != "e2e-def" || o.Defaults.DiskFormat != "raw" || !o.Defaults.Sound {
		t.Fatalf("CreateOptions' defaults: %+v", o.Defaults)
	}
	spec := hv.Spec{Name: "e2e-def3", OS: "linux", VCPUs: 1, MemoryMiB: 256, DiskPool: o.Defaults.Pool, DiskGiB: 1,
		NetKind: o.Defaults.Network, Firmware: "bios", DiskFormat: o.Defaults.DiskFormat, Sound: o.Defaults.Sound}
	var c3 hv.Created
	sv.call(&c3, "CreateMachine", sv.conn, spec, "")
	t.Cleanup(func() { hv.Delete(ctx(t), s, c3.Domain.UUID, true) })
	if d := devices(t, s, c3.Domain.UUID); c3.Disk != "e2e-def/e2e-def3.img" || diskAt(t, d, "vda").Format != "raw" || len(d.Sound) != 1 {
		t.Fatalf("from the page: %+v, %+v", c3, d)
	}
}
