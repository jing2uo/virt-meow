//go:build e2e

package e2e

import (
	"errors"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// The XML editor's two paths: a new machine's definition edited before it is
// created, and an existing machine's saved back, renamed, refused.
func TestEditDefinitions(t *testing.T) {
	s := qemuSession(t)
	scratchPool(t, s, "e2e-xml")
	spec := hv.Spec{Name: "e2e-xml", OS: "linux", VCPUs: 1, MemoryMiB: 256,
		DiskPool: "e2e-xml", DiskGiB: 1, NetKind: "none", Firmware: "bios"}

	x, err := hv.Preview(ctx(t), s, spec)
	if err != nil {
		t.Fatal(err)
	}
	if vols, _ := hv.Volumes(ctx(t), s, "e2e-xml"); len(vols) != 0 {
		t.Fatalf("Preview made %+v", vols)
	}
	edited := edit(t, x, "<vcpu placement=\"static\">1</vcpu>", "<vcpu placement=\"static\">2</vcpu>")
	c, err := hv.Create(ctx(t), s, spec, edited)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, c.Domain.UUID, true) })
	if c.Disk != "e2e-xml/e2e-xml.qcow2" || c.Domain.VCPUs != 2 {
		t.Fatalf("created %+v", c)
	}
	if _, err := hv.Preview(ctx(t), s, spec); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Preview of a taken name: %v", err)
	}

	// Without the disk in the definition, no disk is made.
	bare := spec
	bare.Name = "e2e-xml-bare"
	bx, err := hv.Preview(ctx(t), s, bare)
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(bx, "<disk "), strings.Index(bx, "</disk>")
	bc, err := hv.Create(ctx(t), s, bare, bx[:i]+bx[j+len("</disk>"):])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Delete(ctx(t), s, bc.Domain.UUID, true) })
	if vols, _ := hv.Volumes(ctx(t), s, "e2e-xml"); bc.Disk != "" || len(vols) != 1 {
		t.Fatalf("created %+v, pool has %+v", bc, vols)
	}

	def, err := hv.GetDefinition(ctx(t), s, c.Domain.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if def.Active || def.Name != "e2e-xml" || !strings.Contains(def.XML, c.Domain.UUID) {
		t.Fatalf("definition %+v", def)
	}
	withDesc := edit(t, def.XML, "</name>", "</name>\n  <description>edited</description>")
	saved, err := hv.Redefine(ctx(t), s, def.UUID, def.XML, withDesc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.XML, "<description>edited</description>") {
		t.Fatalf("saved:\n%s", saved.XML)
	}
	if _, err := hv.Redefine(ctx(t), s, def.UUID, def.XML, def.XML); !errors.Is(err, hv.ErrChanged) {
		t.Fatalf("stale base: %v", err)
	}
	bad := map[string]string{
		"uuid changed":  edit(t, saved.XML, c.Domain.UUID, "00000000-0000-0000-0000-0000000000e2"),
		"uuid removed":  edit(t, saved.XML, "<uuid>"+c.Domain.UUID+"</uuid>", ""),
		"schema":        edit(t, saved.XML, "<description>", "<descriptio>"),
		"not a domain":  "<network><name>x</name></network>",
		"not even XML":  "<domain",
		"invalid value": edit(t, saved.XML, "<vcpu placement='static'>2</vcpu>", "<vcpu placement='static'>two</vcpu>"),
	}
	for what, x := range bad {
		if _, err := hv.Redefine(ctx(t), s, def.UUID, saved.XML, x); err == nil {
			t.Errorf("%s: saved", what)
		}
	}

	// Renamed while off; refused while running, and nothing saved.
	renamed, err := hv.Redefine(ctx(t), s, def.UUID, saved.XML, edit(t, saved.XML, "<name>e2e-xml</name>", "<name>e2e-xml-2</name>"))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "e2e-xml-2" || machine(t, s, "e2e-xml-2").UUID != def.UUID {
		t.Fatalf("renamed %+v", renamed)
	}
	// A rename that libvirt then refuses is undone.
	refused := edit(t, edit(t, renamed.XML, "<name>e2e-xml-2</name>", "<name>e2e-xml-3</name>"), "<description>", "<descriptio>")
	if _, err := hv.Redefine(ctx(t), s, def.UUID, renamed.XML, refused); err == nil {
		t.Fatal("saved an invalid definition")
	}
	if d := machine(t, s, "e2e-xml-2"); d.UUID != def.UUID {
		t.Fatalf("after a refused rename: %+v", d)
	}
	if err := hv.Power(ctx(t), s, def.UUID, hv.Start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hv.Power(ctx(t), s, def.UUID, hv.ForceOff) })
	running, err := hv.GetDefinition(ctx(t), s, def.UUID)
	if err != nil || !running.Active {
		t.Fatalf("running: %+v, %v", running, err)
	}
	if _, err := hv.Redefine(ctx(t), s, def.UUID, running.XML, edit(t, running.XML, "<name>e2e-xml-2</name>", "<name>e2e-xml-4</name>")); err == nil {
		t.Fatal("renamed a running machine")
	}
	// A change to a running machine waits for its next start.
	more := edit(t, running.XML, "<description>edited</description>", "<description>next boot</description>")
	if _, err := hv.Redefine(ctx(t), s, def.UUID, running.XML, more); err != nil {
		t.Fatal(err)
	}
	var live string
	s.Do(ctx(t), func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByName("e2e-xml-2")
		if err == nil {
			live, err = lv.DomainGetXMLDesc(dom, 0)
		}
		return err
	})
	if !strings.Contains(live, "<description>edited</description>") {
		t.Fatalf("the running machine changed:\n%s", live)
	}

	// virt-mm edit, through an editor that changes the description.
	cmd := virtmm(t, "edit", "e2e-xml-2")
	cmd.Env = append(cmd.Env, "VISUAL=sed -i s/next/editor/")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("virt-mm edit: %v: %s", err, out)
	}
	if d, _ := hv.GetDefinition(ctx(t), s, def.UUID); !strings.Contains(d.XML, "<description>editor boot</description>") {
		t.Fatalf("after virt-mm edit:\n%s", d.XML)
	}
}

// edit replaces the first old in x with new, and fails the test if there is
// none: an edit that silently changes nothing would pass for a saved one.
func edit(t *testing.T, x, old, new string) string {
	t.Helper()
	if !strings.Contains(x, old) {
		t.Fatalf("no %q in\n%s", old, x)
	}
	return strings.Replace(x, old, new, 1)
}
