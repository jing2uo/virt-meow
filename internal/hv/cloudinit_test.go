package hv

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// The test vector of Drepper's SHA-crypt specification.
func TestSHA512Crypt(t *testing.T) {
	got, err := sha512Crypt("Hello world!", "saltstring")
	want := "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"
	if err != nil || got != want {
		t.Fatalf("got %s, %v", got, err)
	}
	a, _ := sha512Crypt("pw", "")
	b, _ := sha512Crypt("pw", "")
	if a == b || !strings.HasPrefix(a, "$6$") || len(a) != 3+16+1+86 {
		t.Errorf("random salts: %s %s", a, b)
	}
}

// readISO reads back what makeISO wrote: the label and the root
// directory's files, by their Joliet names when Joliet says so.
func readISO(t *testing.T, img []byte, joliet bool) (string, map[string]string) {
	t.Helper()
	at := 16
	if joliet {
		at = 17
	}
	d := img[at*sector : (at+1)*sector]
	if string(d[1:6]) != "CD001" || (joliet && (d[0] != 2 || string(d[88:91]) != "%/E")) || (!joliet && d[0] != 1) {
		t.Fatalf("descriptor at %d: % x", at, d[:8])
	}
	name := func(b []byte) string {
		if !joliet {
			return string(b)
		}
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(b[2*i:])
		}
		return string(utf16.Decode(u))
	}
	label := strings.TrimRight(name(d[40:72]), " ")
	if binary.LittleEndian.Uint32(d[80:]) != uint32(len(img)/sector) {
		t.Errorf("volume size %d sectors, image %d", binary.LittleEndian.Uint32(d[80:]), len(img)/sector)
	}
	root := d[156:]
	lba, size := binary.LittleEndian.Uint32(root[2:]), binary.LittleEndian.Uint32(root[10:])
	dir := img[int(lba)*sector : int(lba)*sector+int(size)]
	files := map[string]string{}
	for i := 0; i < len(dir) && dir[i] != 0; i += int(dir[i]) {
		r := dir[i:]
		n := r[32]
		if r[25]&2 != 0 {
			continue // . and ..
		}
		at, l := binary.LittleEndian.Uint32(r[2:]), binary.LittleEndian.Uint32(r[10:])
		if binary.BigEndian.Uint32(r[6:]) != at || binary.BigEndian.Uint32(r[14:]) != l {
			t.Errorf("both-endian numbers disagree in %q", name(r[33:33+n]))
		}
		files[name(r[33:33+n])] = string(img[int(at)*sector : int(at)*sector+int(l)])
	}
	return label, files
}

func TestMakeISO(t *testing.T) {
	big := strings.Repeat("x", 5000) // over two sectors
	img := makeISO("cidata", []isoFile{{"user-data", []byte("#cloud-config\n")}, {"meta-data", []byte(big)}, {"network-config", nil}}, time.Now())
	if len(img)%sector != 0 {
		t.Fatalf("image of %d bytes", len(img))
	}
	label, files := readISO(t, img, true)
	if label != "cidata" || files["user-data"] != "#cloud-config\n" || files["meta-data"] != big || len(files) != 3 {
		t.Errorf("Joliet: %q %v", label, files)
	}
	label, files = readISO(t, img, false)
	if label != "cidata" || files["USER_DAT.;1"] != "#cloud-config\n" || files["META_DAT.;1"] != big {
		t.Errorf("ISO 9660: %q %v", label, files)
	}
}

func TestISOName(t *testing.T) {
	taken := map[string]bool{}
	for in, want := range map[string]string{"user-data": "USER_DAT.;1", "a.tar.gz": "A_TAR.GZ;1"} {
		if got := isoName(in, taken); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if got := isoName("user-database", taken); got != "USER_DA1.;1" {
		t.Errorf("a second USER_DAT: %s", got)
	}
}

func TestCloudInitSeed(t *testing.T) {
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk7Yb0HcTzS0QjvxLAyT3D0nXcPPt8tqTiXmnHqRbqL me@x"
	c := CloudInit{User: "alice", Password: "s3cret", SSHKeys: key + "\n"}
	files, err := c.seed("web_01")
	if err != nil {
		t.Fatal(err)
	}
	meta, user := string(files[0].Data), string(files[1].Data)
	if !strings.Contains(meta, `local-hostname: "web-01"`) || !strings.Contains(meta, `instance-id: "virt-mm-web_01-`) {
		t.Errorf("meta-data:\n%s", meta)
	}
	for _, want := range []string{"#cloud-config\n", `hostname: "web-01"`, `- name: "alice"`, `passwd: "$6$`, `- "` + key + `"`, "ssh_pwauth: true", "lock_passwd: false"} {
		if !strings.Contains(user, want) {
			t.Errorf("user-data lacks %q:\n%s", want, user)
		}
	}
	if strings.Contains(user, "s3cret") {
		t.Error("the password in clear")
	}
	// Keys alone go to the image's default user.
	files, _ = CloudInit{SSHKeys: key, Hostname: "h1"}.seed("m")
	if u := string(files[1].Data); !strings.HasPrefix(u, "#cloud-config\nhostname: \"h1\"\nssh_authorized_keys:\n  - ") || strings.Contains(u, "users:") {
		t.Errorf("keys for the default user:\n%s", u)
	}
	// User-data as given.
	files, _ = CloudInit{UserData: "#!/bin/sh\necho hi\n"}.seed("m")
	if !bytes.Equal(files[1].Data, []byte("#!/bin/sh\necho hi\n")) {
		t.Errorf("user-data: %q", files[1].Data)
	}
	for _, bad := range []CloudInit{{User: "Alice"}, {Password: "x"}, {SSHKeys: "not a key"}, {Hostname: "a_b"}, {UserData: "#cloud-config", User: "a"}} {
		if len(bad.check()) == 0 {
			t.Errorf("accepted %+v", bad)
		}
	}
}
