package hv

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// CloudInit is what a cloud image is told at its first boot, through a
// NoCloud seed: an ISO labelled cidata with user-data and meta-data, which
// cloud-init in the image reads from the CD drive.
type CloudInit struct {
	// User is made with sudo and the password and keys below; without
	// one, the keys go to the image's default user (debian, ubuntu,
	// fedora…), which takes no password from here.
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"` // stored in the seed only as a SHA-512 crypt hash
	SSHKeys  string `json:"ssh_keys,omitempty"` // authorized_keys lines
	Hostname string `json:"hostname,omitempty"` // default: the machine's name
	// UserData, when set, is the user-data as given (#cloud-config, a
	// script…), instead of the one made from the fields above.
	UserData string `json:"user_data,omitempty"`
}

var (
	userRE    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	labelRE   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	notHostRE = regexp.MustCompile(`[^A-Za-z0-9-]+`)
)

func (c CloudInit) check() []string {
	var errs []string
	if c.User != "" && !userRE.MatchString(c.User) {
		errs = append(errs, "cloud-init user: lowercase letters, digits, '_' and '-', up to 32")
	}
	if c.Password != "" && c.User == "" {
		errs = append(errs, "cloud-init password: for which user (the image's default user takes only SSH keys)")
	}
	if c.SSHKeys != "" {
		if _, err := ParseSSHKeys(c.SSHKeys); err != nil {
			errs = append(errs, "cloud-init SSH keys: "+err.Error())
		}
	}
	if c.Hostname != "" && !labelRE.MatchString(c.Hostname) {
		errs = append(errs, "cloud-init hostname: letters, digits and '-', up to 63, not starting or ending with '-'")
	}
	if c.UserData != "" && (c.User != "" || c.Password != "" || c.SSHKeys != "") {
		errs = append(errs, "cloud-init user-data is used as given: put the user, password and keys in it")
	}
	return errs
}

// hostname is the guest's hostname: the one asked for, else the machine's
// name as a hostname can have it.
func (c CloudInit) hostname(machine string) string {
	if c.Hostname != "" {
		return c.Hostname
	}
	h := strings.Trim(notHostRE.ReplaceAllString(machine, "-"), "-")
	if len(h) > 63 {
		h = strings.TrimRight(h[:63], "-")
	}
	return h
}

// quote writes s as a YAML scalar: a JSON string is a valid double-quoted one.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// seed is the NoCloud seed's files for machine: meta-data with a new
// instance ID (cloud-init runs its first boot once per instance ID) and the
// hostname, and user-data.
func (c CloudInit) seed(machine string) ([]isoFile, error) {
	id := make([]byte, 4)
	rand.Read(id)
	host := c.hostname(machine)
	meta := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", quote("virt-mm-"+machine+"-"+hex.EncodeToString(id)), quote(host))
	user := c.UserData
	if user == "" {
		var err error
		if user, err = c.userData(host); err != nil {
			return nil, err
		}
	}
	return []isoFile{{"meta-data", []byte(meta)}, {"user-data", []byte(user)}}, nil
}

func (c CloudInit) userData(host string) (string, error) {
	var keys []string
	if c.SSHKeys != "" {
		var err error
		if keys, err = ParseSSHKeys(c.SSHKeys); err != nil {
			return "", err
		}
	}
	list := func(indent string, items []string) string {
		var b strings.Builder
		for _, k := range items {
			fmt.Fprintf(&b, "%s- %s\n", indent, quote(k))
		}
		return b.String()
	}
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	fmt.Fprintf(&b, "hostname: %s\n", quote(host))
	if c.User != "" {
		b.WriteString("users:\n  - default\n")
		fmt.Fprintf(&b, "  - name: %s\n    sudo: \"ALL=(ALL) NOPASSWD:ALL\"\n    shell: /bin/bash\n", quote(c.User))
		if c.Password != "" {
			hash, err := sha512Crypt(c.Password, "")
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "    lock_passwd: false\n    passwd: %s\n", quote(hash))
		}
		if len(keys) > 0 {
			b.WriteString("    ssh_authorized_keys:\n" + list("      ", keys))
		}
	} else if len(keys) > 0 {
		b.WriteString("ssh_authorized_keys:\n" + list("  ", keys))
	}
	if c.Password != "" {
		b.WriteString("ssh_pwauth: true\n")
	}
	return b.String(), nil
}

// seedISO is the NoCloud seed for machine, as an ISO image.
func (c CloudInit) seedISO(machine string) ([]byte, error) {
	files, err := c.seed(machine)
	if err != nil {
		return nil, err
	}
	return makeISO("cidata", files, time.Now()), nil
}

const cryptAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// sha512Crypt hashes password as crypt(3)'s $6$ (SHA-512, 5000 rounds), the
// hash /etc/shadow holds; salt "" picks a random one.
func sha512Crypt(password, salt string) (string, error) {
	if salt == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		for i := range b {
			b[i] = cryptAlphabet[int(b[i])%len(cryptAlphabet)]
		}
		salt = string(b)
	}
	if len(salt) > 16 {
		salt = salt[:16]
	}
	if strings.ContainsAny(salt, "$:\n") {
		return "", errors.New("salt: no '$', ':' or newline")
	}
	p, s := []byte(password), []byte(salt)
	sum := func(parts ...[]byte) []byte {
		h := sha512.New()
		for _, x := range parts {
			h.Write(x)
		}
		return h.Sum(nil)
	}
	b := sum(p, s, p)
	a := sha512.New()
	a.Write(p)
	a.Write(s)
	for i := len(p); i > 0; i -= 64 {
		a.Write(b[:min(i, 64)])
	}
	for i := len(p); i > 0; i >>= 1 {
		if i&1 != 0 {
			a.Write(b)
		} else {
			a.Write(p)
		}
	}
	c := a.Sum(nil)
	h := sha512.New()
	for range len(p) {
		h.Write(p)
	}
	dp := h.Sum(nil)
	pp := make([]byte, 0, len(p))
	for len(pp) < len(p) {
		pp = append(pp, dp[:min(64, len(p)-len(pp))]...)
	}
	h = sha512.New()
	for range 16 + int(c[0]) {
		h.Write(s)
	}
	ss := h.Sum(nil)[:len(s)]
	for i := range 5000 {
		h := sha512.New()
		if i&1 != 0 {
			h.Write(pp)
		} else {
			h.Write(c)
		}
		if i%3 != 0 {
			h.Write(ss)
		}
		if i%7 != 0 {
			h.Write(pp)
		}
		if i&1 != 0 {
			h.Write(c)
		} else {
			h.Write(pp)
		}
		c = h.Sum(nil)
	}
	var out strings.Builder
	out.WriteString("$6$" + salt + "$")
	enc := func(b2, b1, b0 byte, n int) {
		w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
		for range n {
			out.WriteByte(cryptAlphabet[w&0x3f])
			w >>= 6
		}
	}
	for _, t := range [][3]int{{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
		{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13}, {56, 14, 35},
		{15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41}} {
		enc(c[t[0]], c[t[1]], c[t[2]], 4)
	}
	enc(0, 0, c[63], 2)
	return out.String(), nil
}
