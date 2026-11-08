package hv

import (
	"strings"
	"testing"
)

func shipped(t *testing.T) *OSDB {
	t.Helper()
	db, err := ShippedOSDB()
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestShippedOSDB(t *testing.T) {
	db := shipped(t)
	if db.Source != "virt-mm" || db.Version != shippedOSInfoVersion || len(db.list) < 500 {
		t.Fatalf("%s %s, %d OSes", db.Source, db.Version, len(db.list))
	}
	d := db.Lookup("debian12")
	if d == nil || d.ID != "http://debian.org/debian/12" || d.Name != "Debian 12" {
		t.Fatalf("debian12: %+v", d)
	}
	if db.Lookup("debianbookworm") != d || db.Lookup(d.ID) != d {
		t.Error("by another short ID, by ID")
	}
	// Ubuntu's devices are listed on older releases: inherited.
	if u := db.Lookup("ubuntu24.04"); u == nil || !u.has(virtioBlock) || !u.has(virtioNet) {
		t.Errorf("ubuntu24.04 devices: %+v", u)
	}
}

func TestMatchISO(t *testing.T) {
	db := shipped(t)
	for id, want := range map[string]string{
		"Debian 12.5.0 amd64 n":           "debian12",
		"CCCOMA_X64FRE_EN-US_DV9":         "win11",
		"Ubuntu-Server 24.04.1 LTS amd64": "ubuntu24.04",
		"Fedora-WS-Live-41-1-4":           "fedora41",
	} {
		v := db.MatchISO(id, "x86_64")
		if v == nil || v.ShortID != want {
			got := "nothing"
			if v != nil {
				got = v.ShortID
			}
			t.Errorf("%q: %s, want %s", id, got, want)
		}
	}
	if v := db.MatchISO("MY_OWN_DISC", "x86_64"); v != nil {
		t.Errorf("an unknown disc: %s", v.ShortID)
	}
}

func TestMatchImage(t *testing.T) {
	db := shipped(t)
	for name, want := range map[string]string{
		"debian-12-genericcloud-amd64.qcow2":            "debian12",
		"jammy-server-cloudimg-amd64.img":               "ubuntu22.04",
		"noble-server-cloudimg-amd64.img":               "ubuntu24.04",
		"Fedora-Cloud-Base-Generic-41-1.4.x86_64.qcow2": "fedora41",
		"AlmaLinux-9-GenericCloud-latest.x86_64.qcow2":  "almalinux9",
	} {
		v := db.MatchImage(name, "x86_64")
		if v == nil || v.ShortID != want {
			got := "nothing"
			if v != nil {
				got = v.ShortID
			}
			t.Errorf("%q: %s, want %s", name, got, want)
		}
	}
	if v := db.MatchImage("web01.qcow2", "x86_64"); v != nil {
		t.Errorf("a machine's disk: %s", v.ShortID)
	}
}

func TestPropose(t *testing.T) {
	db := shipped(t)
	p := db.Lookup("debian12").For(kvmHost).Propose
	if p != (Proposal{OS: "linux", VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20, Firmware: "uefi", DiskBus: "virtio", NICModel: "virtio"}) {
		t.Errorf("debian12: %+v", p)
	}
	p = db.Lookup("win11").For(kvmHost).Propose
	if p != (Proposal{OS: "windows", VCPUs: 2, MemoryMiB: 4096, DiskGiB: 64, Firmware: "uefi-secure", TPM: true, DiskBus: "sata", NICModel: "e1000e"}) {
		t.Errorf("win11: %+v", p)
	}
	if p := db.Lookup("win7").For(kvmHost).Propose; p.Firmware != "bios" || p.OS != "windows" {
		t.Errorf("win7: %+v", p)
	}
	if p := db.Lookup("debian6").For(kvmHost).Propose; p.Firmware != "bios" {
		t.Errorf("debian6 (2011): %+v", p)
	}
	noUEFI := kvmHost
	noUEFI.UEFI, noUEFI.SecureBoot = false, false
	if p := db.Lookup("debian12").For(noUEFI).Propose; p.Firmware != "bios" {
		t.Errorf("debian12 without UEFI on the host: %+v", p)
	}
}

func TestOSInfoMetadata(t *testing.T) {
	if m := osinfoMetadata(`http://x/"a"&b`); m != `<libosinfo:libosinfo xmlns:libosinfo="http://libosinfo.org/xmlns/libvirt/domain/1.0"><libosinfo:os id="http://x/&#34;a&#34;&amp;b"/></libosinfo:libosinfo>` {
		t.Error(m)
	}
}

func TestDomainXMLOSVariant(t *testing.T) {
	s := Spec{Name: "d", OS: "linux", VCPUs: 1, MemoryMiB: 512, DiskPool: "p", DiskGiB: 8, NetKind: "none", Firmware: "bios",
		OSVariant: "http://debian.org/debian/12"}
	if err := s.Check(kvmHost); err != nil {
		t.Fatal(err)
	}
	x, err := s.DomainXML(kvmHost, "p", "d.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(x, `<metadata><libosinfo:libosinfo xmlns:libosinfo="http://libosinfo.org/xmlns/libvirt/domain/1.0"><libosinfo:os id="http://debian.org/debian/12"/></libosinfo:libosinfo></metadata>`) {
		t.Errorf("no osinfo metadata:\n%s", x)
	}
	s.OSVariant = "debian12"
	if s.Check(kvmHost) == nil {
		t.Error("a short ID taken as an osinfo ID")
	}
}
