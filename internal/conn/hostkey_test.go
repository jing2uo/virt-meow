package conn

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func edKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func ecKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

var remote = &net.TCPAddr{IP: net.IPv4(192, 0, 2, 7), Port: 2200}

func TestHostKeysTrustAndCheck(t *testing.T) {
	dir := t.TempDir()
	h := &HostKeys{Own: filepath.Join(dir, "sub", "known_hosts"), Extra: []string{filepath.Join(dir, "missing")}}
	key := edKey(t)
	const addr = "kvm1:2200"

	err := h.Callback()(addr, remote, key)
	var unknown *UnknownHostKeyError
	if !errors.As(err, &unknown) {
		t.Fatalf("first contact: %v, want UnknownHostKeyError", err)
	}
	if unknown.Fingerprint != ssh.FingerprintSHA256(key) || unknown.Type != ssh.KeyAlgoED25519 {
		t.Errorf("prompt shows %s %s", unknown.Type, unknown.Fingerprint)
	}

	if err := h.Trust(addr, key); err != nil {
		t.Fatal(err)
	}
	if err := h.Callback()(addr, remote, key); err != nil {
		t.Fatalf("after Trust: %v", err)
	}

	err = h.Callback()(addr, remote, edKey(t))
	var changed *HostKeyChangedError
	if !errors.As(err, &changed) || len(changed.Known) != 1 {
		t.Fatalf("another key: %v, want HostKeyChangedError with one key on record", err)
	}

	if n, err := h.Forget(addr); err != nil || n != 1 {
		t.Fatalf("Forget = %d, %v", n, err)
	}
	if err := h.Callback()(addr, remote, key); !errors.As(err, &unknown) {
		t.Fatalf("after Forget: %v, want unknown again", err)
	}
}

func TestHostKeysExtraIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	extra := filepath.Join(dir, "ssh_known_hosts")
	key := edKey(t)
	line := knownhosts.Line([]string{knownhosts.Normalize("kvm1")}, key)
	if err := os.WriteFile(extra, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &HostKeys{Own: filepath.Join(dir, "own"), Extra: []string{extra}}
	if err := h.Callback()("kvm1:22", remote, key); err != nil {
		t.Fatalf("key from Extra: %v", err)
	}
	if n, _ := h.Forget("kvm1"); n != 0 {
		t.Errorf("Forget removed %d lines it doesn't own", n)
	}
	if data, _ := os.ReadFile(extra); string(data) != line+"\n" {
		t.Errorf("Extra was rewritten")
	}
}

func TestHostKeysAlgorithms(t *testing.T) {
	dir := t.TempDir()
	h := &HostKeys{Own: filepath.Join(dir, "own")}
	if a := h.Algorithms("kvm1:22"); a != nil {
		t.Errorf("no file: %v, want nil", a)
	}
	if err := h.Trust("kvm1:22", edKey(t)); err != nil {
		t.Fatal(err)
	}
	if err := h.Trust("kvm1:22", ecKey(t)); err != nil {
		t.Fatal(err)
	}
	got := h.Algorithms("kvm1:22")
	if !slices.Contains(got, ssh.KeyAlgoED25519) || !slices.Contains(got, ssh.KeyAlgoECDSA256) || len(got) != 2 {
		t.Errorf("Algorithms = %v", got)
	}
	if a := h.Algorithms("other:22"); a != nil {
		t.Errorf("unknown host: %v, want nil", a)
	}
}
