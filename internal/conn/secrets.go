package conn

import (
	"context"
	"fmt"
	"runtime"
)

// Secret names something remembered for a login: the password of a user
// on an SSH server, or the passphrase of a key file.
type Secret struct {
	Kind string `json:"kind"`           // password, passphrase
	User string `json:"user,omitempty"` // password: the user…
	Addr string `json:"addr,omitempty"` // …on this host:port
	Key  string `json:"key,omitempty"`  // passphrase: the key file
}

func (s Secret) String() string {
	if s.Kind == "passphrase" {
		return "the passphrase of " + s.Key
	}
	return "the password of " + s.User + "@" + s.Addr
}

// Secrets is the OS keychain, where an answer is kept when the user asks
// for it to be remembered (docs/adr/0001: never a file of virt-meow's).
type Secrets interface {
	// Available says whether the keychain can be used now: nil, or why not.
	Available(ctx context.Context) error
	// Get returns the remembered value; ok false when there is none.
	Get(ctx context.Context, s Secret) (value string, ok bool, err error)
	Set(ctx context.Context, s Secret, value string) error
	// Delete forgets s; ok false when there was nothing to forget.
	Delete(ctx context.Context, s Secret) (ok bool, err error)
}

// SystemSecrets is this computer's keychain: the Secret Service (GNOME
// Keyring, KWallet, KeePassXC) over the session's D-Bus on Linux and the
// BSDs. The macOS Keychain and Windows' Credential Manager are not done
// yet, and say so.
func SystemSecrets() Secrets {
	switch runtime.GOOS {
	case "darwin":
		return notYet("the macOS Keychain")
	case "windows":
		return notYet("the Windows Credential Manager")
	}
	return &SecretService{}
}

// notYet is a keychain virt-meow can't use yet: nothing is remembered, and
// asking to says why.
type notYet string

func (n notYet) err() error { return fmt.Errorf("remembering passwords in %s: not yet", string(n)) }

func (n notYet) Available(context.Context) error { return n.err() }
func (n notYet) Get(context.Context, Secret) (string, bool, error) {
	return "", false, nil
}
func (n notYet) Set(context.Context, Secret, string) error    { return n.err() }
func (n notYet) Delete(context.Context, Secret) (bool, error) { return false, nil }

// Forget forgets what is remembered for reaching t: the password of each
// hop of its route, and the passphrase of each of their key files (which
// other connections using the same key share). It returns what it forgot.
func Forget(ctx context.Context, sec Secrets, t Target) ([]Secret, error) {
	route, err := Route(t)
	if err != nil || len(route) == 0 {
		return []Secret{}, err
	}
	var candidates []Secret
	for _, h := range route {
		candidates = append(candidates, Secret{Kind: "password", User: h.User, Addr: h.Addr()})
		for _, f := range h.IdentityFiles {
			candidates = append(candidates, Secret{Kind: "passphrase", Key: f})
		}
	}
	forgot := []Secret{}
	for _, s := range candidates {
		ok, err := sec.Delete(ctx, s)
		if err != nil {
			return forgot, err
		}
		if ok {
			forgot = append(forgot, s)
		}
	}
	return forgot, nil
}
