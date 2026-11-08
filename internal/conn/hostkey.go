package conn

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeys checks SSH host keys against virt-meow's own known_hosts, which it
// writes, and any number of others (~/.ssh/known_hosts), which it only reads.
type HostKeys struct {
	Own   string   // virt-meow's known_hosts; created on the first Trust
	Extra []string // read-only; missing files are skipped

	mu sync.Mutex // serialises Trust and Forget on Own
}

// UnknownHostKeyError is a host whose key no known_hosts has. The front end
// shows Fingerprint and, if the user trusts it, calls HostKeys.Trust.
type UnknownHostKeyError struct {
	Addr        string // host:port as dialled
	Key         ssh.PublicKey
	Type        string // ssh-ed25519, ecdsa-sha2-nistp256, …
	Fingerprint string // SHA256:…
}

func (e *UnknownHostKeyError) Error() string {
	return fmt.Sprintf("the host key of %s is not known yet (%s %s)", e.Addr, e.Type, e.Fingerprint)
}

// HostKeyChangedError is a host whose key differs from the one on record.
// It is never accepted from the front end; see HostKeys.Forget.
type HostKeyChangedError struct {
	Addr        string
	Type        string
	Fingerprint string   // the key the host offered
	Known       []string // file:line of each key on record
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("the host key of %s has CHANGED (now %s %s; on record: %s): someone may be intercepting the connection, or the host was reinstalled",
		e.Addr, e.Type, e.Fingerprint, strings.Join(e.Known, ", "))
}

func (h *HostKeys) files() []string {
	var files []string
	for _, f := range append([]string{h.Own}, h.Extra...) {
		if f == "" {
			continue
		}
		if _, err := os.Stat(f); err == nil {
			files = append(files, f)
		}
	}
	return files
}

// Algorithms returns the host key algorithms to offer addr: those of the
// keys already on record for it, so a host known by its ed25519 key isn't
// asked for its ECDSA one and reported unknown (OpenSSH does the same).
// nil, for a host with no key on record, means the library's default order.
func (h *HostKeys) Algorithms(addr string) []string {
	files := h.files()
	if len(files) == 0 {
		return nil
	}
	cb, err := knownhosts.New(files...)
	if err != nil {
		return nil
	}
	// Ask about a key no host has: the mismatch lists every key on record.
	err = cb(addr, probeAddr{}, probeKey{})
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) {
		return nil
	}
	var algos []string
	add := func(as ...string) {
		for _, a := range as {
			if !slices.Contains(algos, a) {
				algos = append(algos, a)
			}
		}
	}
	for _, w := range ke.Want {
		if t := w.Key.Type(); t == ssh.KeyAlgoRSA {
			add(ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		} else {
			add(t)
		}
	}
	return algos
}

// probeKey is a public key no known_hosts line matches.
type probeKey struct{}

func (probeKey) Type() string                        { return "virt-mm-probe" }
func (probeKey) Marshal() []byte                     { return []byte("virt-mm-probe") }
func (probeKey) Verify([]byte, *ssh.Signature) error { return errors.New("probe key") }

type probeAddr struct{}

func (probeAddr) Network() string { return "tcp" }
func (probeAddr) String() string  { return "0.0.0.0:0" }

// Callback returns the ssh.HostKeyCallback for one dial.
func (h *HostKeys) Callback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		files := h.files()
		unknown := &UnknownHostKeyError{Addr: hostname, Key: key, Type: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)}
		if len(files) == 0 {
			return unknown
		}
		cb, err := knownhosts.New(files...)
		if err != nil {
			return fmt.Errorf("reading known_hosts: %w", err)
		}
		err = cb(hostname, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			if len(ke.Want) == 0 {
				return unknown
			}
			changed := &HostKeyChangedError{Addr: hostname, Type: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)}
			for _, w := range ke.Want {
				changed.Known = append(changed.Known, fmt.Sprintf("%s:%d", w.Filename, w.Line))
			}
			return changed
		}
		return err
	}
}

// Trust records key for addr (host:port) in Own.
func (h *HostKeys) Trust(addr string, key ssh.PublicKey) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(h.Own), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(h.Own, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(addr)}, key))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Forget removes every key Own has for addr (host or host:port) and reports
// how many lines went. Keys in Extra are not touched: those files belong to
// ssh (ssh-keygen -R).
func (h *HostKeys) Forget(addr string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := os.ReadFile(h.Own)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	want := knownhosts.Normalize(addr)
	var out bytes.Buffer
	removed := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if hostsMatch(line, want) {
			removed++
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, os.WriteFile(h.Own, out.Bytes(), 0o600)
}

// hostsMatch reports whether a known_hosts line (as Trust writes them: plain,
// unhashed) names host.
func hostsMatch(line, host string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 || strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], "@") {
		return false
	}
	for _, h := range strings.Split(fields[0], ",") {
		if h == host {
			return true
		}
	}
	return false
}
