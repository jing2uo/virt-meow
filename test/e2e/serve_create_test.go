//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// The new-machine form under virt-mm serve: what it offers, a machine whose
// disk is a copy of an image grown to the size asked, with the disk bus
// and NIC model chosen, made without starting ("customize before install")
// and started after; and a machine bigger than the host refused.
func TestServeCreateForm(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-form")
	sv := serve(t)
	img, err := hv.CreateVolume(ctx(t), s, "e2e-form", "tiny-cloud.qcow2", 64<<20, "qcow2")
	if err != nil {
		t.Fatal(err)
	}

	var o app.CreateOptions
	sv.call(&o, "CreateOptions", sv.conn)
	found := false
	for _, d := range o.Disks {
		found = found || d.Pool == "e2e-form" && d.Name == img.Name
	}
	if !found || o.Host.CPUs < 1 || o.Host.MemoryKiB == 0 || o.Host.FreeKiB == 0 {
		t.Fatalf("CreateOptions: image listed %v, host %+v", found, o.Host)
	}

	spec := map[string]any{"name": "e2e-form", "os": "linux", "vcpus": 1, "memory_mib": 256,
		"disk_pool": "e2e-form", "disk_gib": 1, "image_pool": "e2e-form", "image_volume": img.Name,
		"disk_bus": "sata", "net_kind": "network", "net_source": "e2e-form-net", "nic_model": "e1000",
		"firmware": "bios", "start": false}
	// The NIC's network doesn't exist: the preview (made by the form, not
	// checked against networks) still shows the model asked for.
	var x string
	sv.call(&x, "PreviewMachine", sv.conn, spec)
	if !strings.Contains(x, `bus="sata"`) || !strings.Contains(x, `<model type="e1000"/>`) || !strings.Contains(x, "e2e-form.qcow2") {
		t.Fatalf("preview:\n%s", x)
	}
	spec["net_kind"], spec["net_source"] = "none", ""

	var c hv.Created
	sv.call(&c, "CreateMachine", sv.conn, spec, "")
	t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, true) })
	if c.Disk != "e2e-form/e2e-form.qcow2" || c.Domain.State != hv.ShutOff || c.StartErr != "" {
		t.Fatalf("CreateMachine: %+v", c)
	}
	vols, err := hv.Volumes(ctx(t), s, "e2e-form")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vols {
		switch v.Name {
		case "e2e-form.qcow2":
			if v.Capacity != 1<<30 || v.Format != "qcow2" {
				t.Errorf("the copy: %+v, want 1 GiB of qcow2", v)
			}
		case img.Name:
			if v.Capacity != 64<<20 {
				t.Errorf("the image changed: %+v", v)
			}
		}
	}
	d := devices(t, s, c.Domain.UUID)
	if len(d.Disks) != 1 || d.Disks[0].Bus != "sata" {
		t.Errorf("disks %+v", d.Disks)
	}

	// Customized, then started from its details page.
	sv.call(nil, "Power", sv.conn, c.Domain.UUID, "start")
	t.Cleanup(func() { hv.Power(ctx(t), s, c.Domain.UUID, hv.ForceOff) })
	if m := machine(t, s, "e2e-form"); m.State != hv.Running {
		t.Errorf("after Start: %s", m.State)
	}

	// More memory than the host has is refused before anything is made.
	spec["name"], spec["memory_mib"] = "e2e-form-big", int(o.Host.MemoryKiB>>10)+1024
	if err := sv.try(nil, "CreateMachine", sv.conn, spec, ""); err == nil || !strings.Contains(err.Error(), "memory") {
		t.Errorf("a machine bigger than the host: %v", err)
	}
	if _, err := hv.Lookup(ctx(t), s, "e2e-form-big"); err == nil {
		t.Error("e2e-form-big was made")
	}
}

// virt-mm create --image, --disk-bus and --nic-model: the copy goes into
// the image's pool at the image's size unless --disk says otherwise.
func TestCreateFromImageCLI(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-img")
	if _, err := hv.CreateVolume(ctx(t), s, "e2e-img", "base.qcow2", 32<<20, "qcow2"); err != nil {
		t.Fatal(err)
	}
	var px struct{ XML string }
	cli(t, &px, false, "create", "e2e-img", "--image", "e2e-img/base.qcow2", "--disk-bus", "scsi",
		"--nic-model", "e1000e", "--net", "network:x", "--firmware", "bios", "--print-xml")
	if !strings.Contains(px.XML, `model="virtio-scsi"`) || !strings.Contains(px.XML, `bus="scsi"`) || !strings.Contains(px.XML, `<model type="e1000e"/>`) {
		t.Fatalf("--print-xml:\n%s", px.XML)
	}
	msg := cli(t, nil, true, "create", "e2e-img", "--image", "e2e-img/base.qcow2", "--disk-bus", "ide", "--net", "none")
	if !strings.Contains(msg, "disk bus") {
		t.Errorf("--disk-bus ide: %s", msg)
	}

	var c hv.Created
	cli(t, &c, false, "create", "e2e-img", "--image", "e2e-img/base.qcow2", "--net", "none", "--firmware", "bios", "--memory", "256M", "--cpus", "1")
	t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, true) })
	if c.Disk != "e2e-img/e2e-img.qcow2" {
		t.Fatalf("created %+v", c)
	}
	vols, _ := hv.Volumes(ctx(t), s, "e2e-img")
	for _, v := range vols {
		if v.Name == "e2e-img.qcow2" && v.Capacity != 32<<20 {
			t.Errorf("the copy at the image's size: %+v", v)
		}
	}
	if msg := cli(t, nil, true, "create", "e2e-img2", "--image", "e2e-img/nothing.qcow2", "--net", "none"); !strings.Contains(msg, "nothing.qcow2") {
		t.Errorf("a missing image: %s", msg)
	}
}
