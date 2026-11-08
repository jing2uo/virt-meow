//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// virt-mm create --cloud-init: the disk a copy of the image, the NoCloud
// seed made in Go, uploaded beside it and attached as a CD; a guest that
// reads it (the agent guest booted with virt-mm.nocloud, which finds the
// cidata label, mounts the ISO and takes its hostname, as cloud-init's
// NoCloud source does) reports the hostname given; and the seed goes with
// the machine's disks. Users, passwords and keys are what cloud-init
// itself applies: their user-data is checked here, not applied (the agent
// guest isn't cloud-init).
func TestCloudInit(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-ci")
	if _, err := hv.CreateVolume(ctx(t), s, "e2e-ci", "cloud.qcow2", 32<<20, "qcow2"); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "id.pub")
	pub := pubKey(t, "virt-mm-e2e-ci")
	os.WriteFile(key, []byte(pub+"\n"), 0o644)

	cmd := virtmm(t, "create", "e2e-ci", "--image", "e2e-ci/cloud.qcow2", "--disk", "e2e-ci:1G",
		"--cloud-init", "user=e2e,password,ssh-key="+key+",hostname=ci-host-e2e", "--password-stdin",
		"--net", "none", "--firmware", "bios", "--memory", "256M", "--cpus", "1")
	cmd.Stdin = strings.NewReader("s3cret\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("create: %v: %s", err, out)
	}
	var c hv.Created
	if err := json.Unmarshal(out, &c); err != nil || c.Seed != "e2e-ci/e2e-ci-cidata.iso" || c.Disk != "e2e-ci/e2e-ci.qcow2" {
		t.Fatalf("create: %s", out)
	}
	d := c.Domain
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			hv.Delete(ctx(t), s, d.UUID, true)
		}
	})

	// The seed: an ISO with the hostname, the user, the key and a hash.
	var iso bytes.Buffer
	if _, err := hv.Download(ctx(t), s, "e2e-ci", "e2e-ci-cidata.iso", &iso, nil); err != nil {
		t.Fatal(err)
	}
	b := iso.Bytes()
	if len(b) < 17*2048 || string(b[16*2048+1:16*2048+6]) != "CD001" || !bytes.Contains(b, []byte("cidata")) {
		t.Fatalf("seed of %d bytes is no ISO", len(b))
	}
	for _, want := range []string{`local-hostname: "ci-host-e2e"`, `- name: "e2e"`, `passwd: "$6$`, pub} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("seed lacks %q", want)
		}
	}
	if bytes.Contains(b, []byte("s3cret")) {
		t.Error("the password in clear in the seed")
	}
	dv := devices(t, s, d.UUID)
	if len(dv.Disks) != 2 || dv.Disks[1].Device != "cdrom" || dv.Disks[1].Source != "e2e-ci/e2e-ci-cidata.iso" || dv.Disks[1].Boot != 0 {
		t.Fatalf("disks %+v", dv.Disks)
	}

	// Booted as the agent guest, which reads the seed from the CD.
	def, err := hv.GetDefinition(ctx(t), s, d.UUID)
	if err != nil {
		t.Fatal(err)
	}
	x := strings.Replace(def.XML, "</type>", `</type><kernel>/usr/share/virt-mm-e2e/vmlinuz</kernel>
<initrd>/usr/share/virt-mm-e2e/initrd.gz</initrd><cmdline>console=ttyS0 quiet virt-mm.nocloud</cmdline>`, 1)
	if _, err := hv.Redefine(ctx(t), s, d.UUID, def.XML, x); err != nil {
		t.Fatal(err)
	}
	if err := hv.Power(ctx(t), s, d.UUID, hv.Start); err != nil {
		t.Fatal(err)
	}
	var con screen
	openConsole(t, s, d.UUID, &con)
	con.waitFor(t, "virt-mm-e2e: cidata on", 1)
	if !strings.Contains(con.String(), "meta-data user-data hostname ci-host-e2e") {
		t.Errorf("the guest read the seed as: %s", con.String())
	}
	var info hv.Info
	for deadline := time.Now().Add(60 * time.Second); info.Guest == nil; time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("no guest info from the agent in 60 s: %s", info.GuestNote)
		}
		cli(t, &info, false, "info", "e2e-ci")
	}
	if info.Guest.Hostname != "ci-host-e2e" {
		t.Errorf("the guest's hostname: %q", info.Guest.Hostname)
	}

	// Deleted with its disks: the seed goes too, the image stays.
	removed, err := hv.Delete(ctx(t), s, d.UUID, true)
	deleted = true
	if slices.Sort(removed); err != nil || strings.Join(removed, " ") != "e2e-ci/e2e-ci-cidata.iso e2e-ci/e2e-ci.qcow2" {
		t.Errorf("deleted %v, %v", removed, err)
	}
	if v := volumes(t, s, "e2e-ci"); strings.Join(v, " ") != "cloud.qcow2" {
		t.Errorf("left in the pool: %v", v)
	}

	// What can't be: a password without a user, cloud-init with an ISO.
	if msg := cli(t, nil, true, "create", "e2e-ci2", "--image", "e2e-ci/cloud.qcow2", "--cloud-init", "password", "--net", "none"); !strings.Contains(msg, "user=") {
		t.Errorf("a password without a user: %s", msg)
	}
	if msg := cli(t, nil, true, "create", "e2e-ci2", "--disk", "e2e-ci:1G", "--iso", "e2e-ci/x.iso", "--cloud-init", "hostname=h", "--net", "none"); !strings.Contains(msg, "cloud-init") {
		t.Errorf("cloud-init with an ISO: %s", msg)
	}
}

// cloud-init from the page: CreateMachine with a cloud_init part over
// virt-mm serve, an edited definition without the seed making none, and a
// bad key refused before anything is made.
func TestServeCloudInit(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-ci-web")
	sv := serve(t)
	if _, err := hv.CreateVolume(ctx(t), s, "e2e-ci-web", "cloud.qcow2", 32<<20, "qcow2"); err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{"name": "e2e-ci-web", "os": "linux", "vcpus": 1, "memory_mib": 256,
		"disk_pool": "e2e-ci-web", "disk_gib": 1, "image_pool": "e2e-ci-web", "image_volume": "cloud.qcow2",
		"net_kind": "none", "firmware": "bios",
		"cloud_init": map[string]any{"user": "alice", "password": "pw", "ssh_keys": pubKey(t, "web"), "hostname": "web-ci"}}
	var x string
	sv.call(&x, "PreviewMachine", sv.conn, spec)
	if !strings.Contains(x, "e2e-ci-web-cidata.iso") {
		t.Fatalf("preview without the seed:\n%s", x)
	}
	var c hv.Created
	sv.call(&c, "CreateMachine", sv.conn, spec, "")
	t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, true) })
	if c.Seed != "e2e-ci-web/e2e-ci-web-cidata.iso" {
		t.Fatalf("CreateMachine: %+v", c)
	}

	// The seed's CD taken out of the definition: no seed made.
	spec["name"] = "e2e-ci-web2"
	sv.call(&x, "PreviewMachine", sv.conn, spec)
	i := strings.Index(x, `<disk type="volume" device="cdrom">`)
	j := strings.Index(x[i:], "</disk>") + i + len("</disk>")
	var c2 hv.Created
	sv.call(&c2, "CreateMachine", sv.conn, spec, x[:i]+x[j:])
	t.Cleanup(func() { hv.Delete(ctx(t), s, c2.Domain.UUID, true) })
	if c2.Seed != "" || strings.Contains(strings.Join(volumes(t, s, "e2e-ci-web"), " "), "e2e-ci-web2-cidata") {
		t.Errorf("a seed made for a definition without it: %+v", c2)
	}

	spec["name"] = "e2e-ci-web3"
	spec["cloud_init"] = map[string]any{"ssh_keys": "ssh-rsa nope"}
	if err := sv.try(nil, "CreateMachine", sv.conn, spec, ""); err == nil || !strings.Contains(err.Error(), "SSH") {
		t.Errorf("a bad key: %v", err)
	}
	if v := strings.Join(volumes(t, s, "e2e-ci-web"), " "); strings.Contains(v, "web3") {
		t.Errorf("left behind: %s", v)
	}
}
