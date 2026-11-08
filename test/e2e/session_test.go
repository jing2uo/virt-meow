//go:build e2e

package e2e

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// qemu:///session without a daemon running: virt-meow starts one, as
// libvirt's client does. In the container, as a user with no session
// daemon (Debian ships libvirtd, not virtqemud): two virt-mm processes at once start
// one daemon, with --timeout=120, and both list its machines.
func TestSessionDaemon(t *testing.T) {
	name := env(t, "VIRT_MM_E2E_CONTAINER")
	virtmm(t, "version") // builds it
	if out, err := exec.Command("podman", "cp", binPath, name+":/tmp/virt-mm").CombinedOutput(); err != nil {
		t.Fatalf("podman cp: %v %s", err, out)
	}
	inside := func(script string) string {
		t.Helper()
		out, err := exec.Command("podman", "exec", "--user", "e2e", "-e", "HOME=/tmp/e2e-home", "-e", "XDG_RUNTIME_DIR=/tmp/e2e-run",
			"-e", "VIRT_MM_CONFIG=/tmp/e2e-home/virt-mm", name, "sh", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return string(out)
	}
	t.Cleanup(func() {
		exec.Command("podman", "exec", name, "sh", "-c", "pkill -u e2e -x libvirtd; rm -rf /tmp/e2e-home /tmp/e2e-run /tmp/virt-mm").Run()
	})
	inside("mkdir -p /tmp/e2e-home && mkdir -m 700 -p /tmp/e2e-run")
	if out := inside("pgrep -u e2e -x libvirtd || true"); strings.TrimSpace(out) != "" {
		t.Fatalf("a session daemon runs already: %s", out)
	}
	out := inside(`/tmp/virt-mm ls -c qemu:///session --json > /tmp/e2e-home/a & /tmp/virt-mm ls -c qemu:///session --json > /tmp/e2e-home/b & wait; cat /tmp/e2e-home/a; echo ---; cat /tmp/e2e-home/b`)
	for _, part := range strings.Split(out, "---") {
		var doms []any
		if err := json.Unmarshal([]byte(part), &doms); err != nil {
			t.Fatalf("virt-mm ls on qemu:///session: %v in %q", err, out)
		}
	}
	ps := strings.Fields(inside("pgrep -u e2e -a -x libvirtd"))
	if len(ps) != 3 || ps[2] != "--timeout=120" {
		t.Fatalf("session daemons: %q, want one libvirtd --timeout=120", ps)
	}
	// It is used, not started again.
	inside("/tmp/virt-mm ls -c qemu:///session --json >/dev/null")
	if n := len(strings.Fields(inside("pgrep -u e2e -x libvirtd"))); n != 1 {
		t.Fatalf("%d session daemons after a third virt-mm", n)
	}
}
