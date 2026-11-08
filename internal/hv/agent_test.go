package hv

import (
	"context"
	"strings"
	"testing"
)

func TestParseSSHKeys(t *testing.T) {
	const a = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl a@x"
	keys, err := ParseSSHKeys("# mine\n\n" + a + "\r\n  " + a + " \n")
	if err != nil || len(keys) != 2 || keys[0] != a || keys[1] != a {
		t.Fatalf("%q, %v", keys, err)
	}
	for _, bad := range []string{"", "# only a comment\n", "ssh-ed25519 notbase64", a + "\nhello"} {
		if _, err := ParseSSHKeys(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}

// Keys are checked before anything reaches the guest's agent: the
// session isn't touched (nil here) when one isn't a key.
func TestAddSSHKeysChecks(t *testing.T) {
	good := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl e2e"
	for what, keys := range map[string][]string{
		"not a key":        {"ssh-ed25519 nope"},
		"one good one bad": {good, "hello"},
		"two in one":       {good + "\n" + good},
	} {
		if err := AddSSHKeys(context.Background(), nil, "", "root", keys); err == nil || !strings.Contains(err.Error(), "not an SSH public key") {
			t.Errorf("%s: %v", what, err)
		}
	}
}
