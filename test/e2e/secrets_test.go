//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// asked is a Prompter giving its answers in turn (the last one again once
// they run out) and keeping the questions.
type asked struct {
	mu        sync.Mutex
	answers   []conn.Answer
	questions []conn.Question
	failed    []error
}

func (a *asked) Ask(q conn.Question) (conn.Answer, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.questions = append(a.questions, q)
	ans := a.answers[0]
	if len(a.answers) > 1 {
		a.answers = a.answers[1:]
	}
	return ans, nil
}

func (a *asked) NotRemembered(s conn.Secret, err error) {
	a.mu.Lock()
	a.failed = append(a.failed, err)
	a.mu.Unlock()
}

func (a *asked) reset(answers ...conn.Answer) {
	a.mu.Lock()
	a.answers, a.questions = answers, nil
	a.mu.Unlock()
}

// The container's user pw logs in with a password only; the keychain is
// GNOME Keyring on a bus of the tests' own (test/e2e.sh).
func TestRememberedSecrets(t *testing.T) {
	keychain := &conn.SecretService{Address: env(t, "VIRT_MM_E2E_DBUS")}
	addr := env(t, "VIRT_MM_E2E_SSH")
	sshHome(t)
	home := os.Getenv("HOME")
	known := filepath.Join(home, ".ssh", "known_hosts")
	hk := &conn.HostKeys{Own: known}
	connectTrusting(t, openWith(t, "qemu+ssh://root@"+addr+"/system", hk), hk)

	p := &asked{}
	// A store of its own for each session: no SSH client is shared, every
	// connection logs in.
	open := func(uri string) *conn.Session {
		tg, _ := conn.Parse(uri)
		s := conn.New(tg, conn.Options{HostKeys: &conn.HostKeys{Own: known}, Prompter: p, Secrets: keychain})
		t.Cleanup(func() { s.Close() })
		return s
	}
	connect := func(s *conn.Session) {
		t.Helper()
		if err := s.Connect(ctx(t)); err != nil {
			t.Fatal(err)
		}
		machines(t, s)
	}
	pwURI := "qemu+ssh://pw@" + addr + "/system"
	pwSecret := conn.Secret{Kind: "password", User: "pw", Addr: addr}
	remembered := func(s conn.Secret) string {
		t.Helper()
		v, ok, err := keychain.Get(ctx(t), s)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return "(none)"
		}
		return v
	}

	// Asked, with the offer to remember; a wrong answer first, which
	// isn't kept, then the right one, kept once it worked.
	p.reset(conn.Answer{Value: "wrong", Remember: true}, conn.Answer{Value: "e2e-password", Remember: true})
	connect(open(pwURI))
	if len(p.questions) != 2 || !p.questions[0].CanRemember || p.questions[0].Refused || p.questions[0].Kind != "password" {
		t.Fatalf("asked %+v", p.questions)
	}
	if v := remembered(pwSecret); v != "e2e-password" {
		t.Fatalf("remembered %q", v)
	}

	// Remembered: nobody is asked.
	p.reset(conn.Answer{Value: "unused"})
	connect(open(pwURI))
	if len(p.questions) != 0 {
		t.Fatalf("asked %+v with the password remembered", p.questions)
	}

	// A remembered password that no longer works: forgotten, and the user
	// asked, saying so.
	if err := keychain.Set(ctx(t), pwSecret, "changed"); err != nil {
		t.Fatal(err)
	}
	p.reset(conn.Answer{Value: "e2e-password"})
	connect(open(pwURI))
	if len(p.questions) != 1 || !p.questions[0].Refused {
		t.Fatalf("asked %+v after the remembered password was refused", p.questions)
	}
	if v := remembered(pwSecret); v != "(none)" {
		t.Fatalf("still remembered: %q", v)
	}

	// A key with a passphrase: asked, remembered, then not asked.
	key, _ := os.ReadFile(env(t, "VIRT_MM_E2E_KEY"))
	raw, err := ssh.ParseRawPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(raw, "", []byte("e2e-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(home, ".ssh", "id_ed25519")
	os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600)
	rootURI := "qemu+ssh://root@" + addr + "/system"
	p.reset(conn.Answer{Value: "e2e-passphrase", Remember: true})
	connect(open(rootURI))
	if len(p.questions) != 1 || p.questions[0].Kind != "passphrase" || p.questions[0].Key != keyFile {
		t.Fatalf("asked %+v for the key", p.questions)
	}
	p.reset(conn.Answer{Value: "unused"})
	connect(open(rootURI))
	if len(p.questions) != 0 {
		t.Fatalf("asked %+v with the passphrase remembered", p.questions)
	}

	// Forgotten from the CLI: the password and the key's passphrase.
	p.reset(conn.Answer{Value: "e2e-password", Remember: true})
	connect(open(pwURI))
	var forgot []conn.Secret
	cli(t, &forgot, false, "conn", "forget", pwURI)
	if len(forgot) != 2 || forgot[0] != pwSecret || forgot[1].Key != keyFile {
		t.Fatalf("forgot %+v", forgot)
	}
	if remembered(pwSecret) != "(none)" || remembered(forgot[1]) != "(none)" {
		t.Fatal("still remembered after conn forget")
	}
	if len(p.failed) != 0 {
		t.Fatalf("not remembered: %v", p.failed)
	}

	// The page under serve: the prompt offers to remember, the answer is
	// kept, and the connection's menu forgets it.
	os.WriteFile(keyFile, key, 0o600)
	cmd := virtmm(t, "serve", "--listen", "127.0.0.1:0")
	config := ""
	for _, e := range cmd.Env {
		if v, ok := strings.CutPrefix(e, "VIRT_MM_CONFIG="); ok {
			config = v
		}
	}
	where := startServe(t, cmd)
	pg := page{t, "http://" + where.Listen}
	ws, _, err := websocket.Dial(ctx(t), "ws://"+where.Listen+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	var c struct{ ID string }
	pg.call(&c, "AddConnection", "pw", pwURI, false)
	done := make(chan error, 1)
	go func() { done <- pg.try(nil, "Connect", c.ID) }()
	var q struct {
		ID          string
		Kind        string
		CanRemember bool `json:"can_remember"`
	}
	await(t, ws, "prompt", func(d json.RawMessage) bool { json.Unmarshal(d, &q); return q.ID != "" })
	if q.Kind != "password" || !q.CanRemember {
		t.Fatalf("prompt: %+v", q)
	}
	pg.call(nil, "Answer", q.ID, "e2e-password", true, true)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if v := remembered(pwSecret); v != "e2e-password" {
		t.Fatalf("remembered from the page: %q", v)
	}
	forgot = nil
	pg.call(&forgot, "ForgetSecrets", c.ID)
	if len(forgot) != 1 || forgot[0] != pwSecret {
		t.Fatalf("ForgetSecrets: %+v", forgot)
	}

	// Never in a file: not in HOME, not in virt-meow's config.
	for _, dir := range []string{home, config} {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if b, _ := os.ReadFile(path); bytes.Contains(b, []byte("e2e-password")) {
					t.Errorf("the password is in %s", path)
				}
			}
			return nil
		})
	}
}
