//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
	binDir    string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// virtmm is the virt-mm binary, built once, set to run with --json on the
// container's QEMU driver and a scratch config.
func virtmm(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	buildOnce.Do(func() {
		_, here, _, _ := runtime.Caller(0)
		if binDir, buildErr = os.MkdirTemp("", "virt-mm-e2e-bin"); buildErr != nil {
			return
		}
		binPath = filepath.Join(binDir, "virt-mm")
		cmd := exec.Command("go", "build", "-tags", "nogui", "-o", binPath, "./cmd/virt-mm")
		cmd.Dir = filepath.Join(filepath.Dir(here), "..", "..")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = &buildError{err, out}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	cmd := exec.Command(binPath, append(args, "--json")...)
	cmd.Env = append(os.Environ(),
		"VIRT_MM_CONFIG="+t.TempDir(),
		"VIRT_MM_URI=qemu+unix:///system?socket="+filepath.Join(env(t, "VIRT_MM_E2E_RUN"), "libvirt-sock"))
	return cmd
}

// cli runs virt-mm and decodes what it prints into v (when v isn't nil). It
// fails the test when the command fails, unless wantErr; the error's
// message is returned.
func cli(t *testing.T, v any, wantErr bool, args ...string) string {
	t.Helper()
	cmd := virtmm(t, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(stdout.Bytes(), &e)
		if !wantErr {
			t.Fatalf("virt-mm %v: %v: %s%s", args, err, e.Error, stderr.String())
		}
		return e.Error
	}
	if wantErr {
		t.Fatalf("virt-mm %v: succeeded, want an error: %s", args, stdout.String())
	}
	if v != nil {
		if err := json.Unmarshal(stdout.Bytes(), v); err != nil {
			t.Fatalf("virt-mm %v: %v in %s", args, err, stdout.String())
		}
	}
	return ""
}

type buildError struct {
	err error
	out []byte
}

func (b *buildError) Error() string {
	return "building virt-mm: " + b.err.Error() + "\n" + string(b.out)
}
