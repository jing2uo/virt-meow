package conn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// remoteSystemSocket is libvirtd's socket on a host reached over SSH.
const remoteSystemSocket = "/var/run/libvirt/libvirt-sock"

// Question is what a Prompter is asked: a password or a key's passphrase.
type Question struct {
	Secret
	// CanRemember says the answer can be kept in the OS keychain, if the
	// user asks for it.
	CanRemember bool
	// Refused says the remembered answer was refused, and is forgotten.
	Refused bool
}

// Answer is the user's: the secret, and whether to remember it.
type Answer struct {
	Value    string
	Remember bool
}

// Prompter asks the user for a secret. Ask may return ErrNoPrompt when
// nobody can answer (a CLI without a terminal).
type Prompter interface {
	Ask(Question) (Answer, error)
	// NotRemembered says an answer the user asked to remember couldn't be.
	NotRemembered(s Secret, err error)
}

// ErrNoPrompt means a secret was needed and nobody could be asked.
var ErrNoPrompt = errors.New("no one to ask for a password")

// AuthError is an SSH login that every method failed.
type AuthError struct {
	User, Addr string
	Err        error
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("logging in to %s as %s: %v", e.Addr, e.User, e.Err)
}
func (e *AuthError) Unwrap() error { return e.Err }

// sshPool shares one SSH client per route (user@host:port, after its jump
// hosts) among the sessions and consoles that reach that host. Sessions
// checking host keys against different stores don't share: a client is
// only as trusted as the check it passed.
type sshPool struct {
	mu      sync.Mutex
	clients map[string]*pooledSSH
}

type pooledSSH struct {
	client *ssh.Client
	close  func() // closes the client and those of the jump hosts under it
	refs   int
	dead   chan struct{} // closed when the client's connection ends
}

var defaultPool = &sshPool{clients: map[string]*pooledSSH{}}

// get returns a live SSH client for t and a release func to call when done
// with it. The last release closes the client.
func (p *sshPool) get(ctx context.Context, t Target, hk *HostKeys, ask *asker) (*ssh.Client, func(), error) {
	route, err := Route(t)
	if err != nil {
		return nil, nil, err
	}
	hops := make([]string, len(route))
	for i, h := range route {
		hops[i] = h.String()
	}
	key := fmt.Sprintf("%s %p", strings.Join(hops, ","), hk)

	p.mu.Lock()
	if e, ok := p.clients[key]; ok {
		select {
		case <-e.dead:
			delete(p.clients, key)
		default:
			e.refs++
			p.mu.Unlock()
			return e.client, p.releaser(key, e), nil
		}
	}
	p.mu.Unlock()

	// Dialled without the lock: a handshake can wait on a prompt. Two
	// sessions racing to the same new host both dial; the loser's client is
	// closed below.
	c, closeAll, err := dialRoute(ctx, route, hk, ask)
	if err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.clients[key]; ok {
		select {
		case <-e.dead:
		default:
			closeAll()
			e.refs++
			return e.client, p.releaser(key, e), nil
		}
	}
	e := &pooledSSH{client: c, close: closeAll, refs: 1, dead: make(chan struct{})}
	go func() { c.Wait(); closeAll(); close(e.dead) }()
	p.clients[key] = e
	return c, p.releaser(key, e), nil
}

func (p *sshPool) releaser(key string, e *pooledSSH) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			e.refs--
			if e.refs > 0 {
				return
			}
			e.close()
			if p.clients[key] == e {
				delete(p.clients, key)
			}
		})
	}
}

// dialRoute logs in to each hop of route in turn, each one reached through
// the one before it (direct-tcpip, as ssh's ProxyJump does), and returns the
// last one's client and a func closing them all.
func dialRoute(ctx context.Context, route []Hop, hk *HostKeys, ask *asker) (*ssh.Client, func(), error) {
	var clients []*ssh.Client
	closeAll := func() {
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].Close()
		}
	}
	for i, h := range route {
		var raw net.Conn
		var err error
		if i == 0 {
			var d net.Dialer
			raw, err = d.DialContext(ctx, "tcp", h.Addr())
		} else if raw, err = clients[i-1].DialContext(ctx, "tcp", h.Addr()); err != nil {
			err = fmt.Errorf("reaching %s through the jump host %s: %w", h.Addr(), route[i-1], err)
		}
		if err == nil {
			var c *ssh.Client
			if c, err = handshake(ctx, raw, h, hk, ask); err == nil {
				clients = append(clients, c)
				continue
			}
		}
		closeAll()
		return nil, nil, err
	}
	return clients[len(clients)-1], closeAll, nil
}

// handshake logs in to h over raw, which it closes on failure.
func handshake(ctx context.Context, raw net.Conn, h Hop, hk *HostKeys, ask *asker) (*ssh.Client, error) {
	addr := h.Addr()
	// The handshake has no context of its own: a deadline bounds it (where
	// the transport has deadlines: a jump host's channel hasn't), and
	// closing the transport ends it when ctx does.
	if dl, ok := ctx.Deadline(); ok {
		raw.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()

	l := &login{ctx: ctx, hop: h, ask: ask}
	auth, closeAgent := l.methods()
	defer closeAgent()
	cfg := &ssh.ClientConfig{
		User:              h.User,
		Auth:              auth,
		HostKeyCallback:   hk.Callback(),
		HostKeyAlgorithms: hk.Algorithms(addr),
	}
	sc, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		raw.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var unknown *UnknownHostKeyError
		var changed *HostKeyChangedError
		if errors.As(err, &unknown) || errors.As(err, &changed) {
			return nil, err
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, &AuthError{User: h.User, Addr: addr, Err: err}
		}
		return nil, err
	}
	raw.SetDeadline(time.Time{})
	l.loggedIn()
	return ssh.NewClient(sc, chans, reqs), nil
}

// asker asks the user for secrets, through the keychain first: what was
// remembered is tried before anyone is asked, and an answer the user wants
// remembered is kept once it has worked.
type asker struct {
	prompt  Prompter // nil: nobody to ask
	secrets Secrets  // nil: nothing remembered

	canOnce  sync.Once
	canStore bool
}

// canRemember says whether the keychain is there to remember an answer.
func (a *asker) canRemember(ctx context.Context) bool {
	a.canOnce.Do(func() { a.canStore = a.secrets != nil && a.secrets.Available(ctx) == nil })
	return a.canStore
}

// remembered is s's remembered value, if any; a keychain that can't be
// read (locked, gone) remembers nothing.
func (a *asker) remembered(ctx context.Context, s Secret) (string, bool) {
	if a.secrets == nil {
		return "", false
	}
	v, ok, err := a.secrets.Get(ctx, s)
	return v, ok && err == nil
}

func (a *asker) forget(ctx context.Context, s Secret) {
	if a.secrets != nil {
		a.secrets.Delete(ctx, s)
	}
}

func (a *asker) remember(ctx context.Context, s Secret, value string) {
	if err := a.secrets.Set(ctx, s, value); err != nil && a.prompt != nil {
		a.prompt.NotRemembered(s, err)
	}
}

func (a *asker) ask(ctx context.Context, s Secret, refused bool) (Answer, error) {
	if a.prompt == nil {
		return Answer{}, ErrNoPrompt
	}
	return a.prompt.Ask(Question{Secret: s, CanRemember: a.canRemember(ctx), Refused: refused})
}

// login is one handshake's authentication with a hop.
type login struct {
	ctx context.Context
	hop Hop
	ask *asker

	pwTries   int
	pwStored  bool   // the last password tried was the remembered one
	pwPending string // typed, to remember once the login worked
	pwKeep    bool
}

// methods are, in order: the SSH agent, the hop's key files (the default
// ones when the SSH configuration names none), a password. With
// IdentitiesOnly, only the agent's keys that are among the files are
// offered, as ssh does. The second return closes the agent's socket once
// the handshake is over.
func (l *login) methods() ([]ssh.AuthMethod, func()) {
	h := l.hop
	var methods []ssh.AuthMethod
	closeAgent := func() {}
	sock := h.Agent
	if sock == "" {
		sock = os.Getenv("SSH_AUTH_SOCK")
	}
	if sock != "" && sock != "none" && runtime.GOOS != "windows" {
		if c, err := net.Dial("unix", sock); err == nil {
			ag := agent.NewClient(c)
			signers := ag.Signers
			if h.IdentitiesOnly {
				signers = func() ([]ssh.Signer, error) {
					all, err := ag.Signers()
					want := publicKeys(h.IdentityFiles)
					return slices.DeleteFunc(all, func(s ssh.Signer) bool {
						return !want[string(s.PublicKey().Marshal())]
					}), err
				}
			}
			methods = append(methods, ssh.PublicKeysCallback(signers))
			closeAgent = func() { c.Close() }
		}
	}
	methods = append(methods, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		return l.keyFileSigners(), nil
	}))
	if l.ask.prompt != nil || l.ask.secrets != nil {
		// Tried again when refused: the remembered password, then what
		// the user types, twice more.
		methods = append(methods, ssh.RetryableAuthMethod(ssh.PasswordCallback(l.password), 3))
	}
	return methods, closeAgent
}

// password is the next password to try: the remembered one first; once it
// is refused, it is forgotten and the user asked.
func (l *login) password() (string, error) {
	s := Secret{Kind: "password", User: l.hop.User, Addr: l.hop.Addr()}
	l.pwTries++
	refused := l.pwStored
	if refused {
		l.ask.forget(l.ctx, s)
		l.pwStored = false
	}
	if l.pwTries == 1 {
		if v, ok := l.ask.remembered(l.ctx, s); ok {
			l.pwStored = true
			return v, nil
		}
	}
	a, err := l.ask.ask(l.ctx, s, refused)
	if err != nil {
		return "", err
	}
	l.pwPending, l.pwKeep = a.Value, a.Remember
	return a.Value, nil
}

// loggedIn remembers the password that worked, if the user asked to.
func (l *login) loggedIn() {
	if l.pwKeep && !l.pwStored {
		l.ask.remember(l.ctx, Secret{Kind: "password", User: l.hop.User, Addr: l.hop.Addr()}, l.pwPending)
	}
}

// keyFileSigners loads the hop's private keys. An encrypted one is opened
// with its remembered passphrase, else the one the user gives (remembered
// if they ask, once it opened the key); a remembered passphrase that no
// longer opens it is forgotten. A key that can't be read or opened is
// skipped.
func (l *login) keyFileSigners() []ssh.Signer {
	var signers []ssh.Signer
	for _, path := range l.hop.IdentityFiles {
		pem, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s, err := ssh.ParsePrivateKey(pem)
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			s, err = l.unlockKey(path, pem)
		}
		if err == nil {
			signers = append(signers, s)
		}
	}
	return signers
}

func (l *login) unlockKey(path string, pem []byte) (ssh.Signer, error) {
	sec := Secret{Kind: "passphrase", Key: path}
	refused := false
	if v, ok := l.ask.remembered(l.ctx, sec); ok {
		s, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(v))
		if err == nil {
			return s, nil
		}
		l.ask.forget(l.ctx, sec)
		refused = true
	}
	a, err := l.ask.ask(l.ctx, sec, refused)
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(a.Value))
	if err == nil && a.Remember {
		l.ask.remember(l.ctx, sec, a.Value)
	}
	return s, err
}

// publicKeys are the public halves of the key files, from each one's .pub
// or, for a key without a passphrase, from the key itself.
func publicKeys(files []string) map[string]bool {
	keys := map[string]bool{}
	for _, f := range files {
		if b, err := os.ReadFile(f + ".pub"); err == nil {
			if k, _, _, _, err := ssh.ParseAuthorizedKey(b); err == nil {
				keys[string(k.Marshal())] = true
				continue
			}
		}
		if b, err := os.ReadFile(f); err == nil {
			if s, err := ssh.ParsePrivateKey(b); err == nil {
				keys[string(s.PublicKey().Marshal())] = true
			}
		}
	}
	return keys
}

func currentUser() string {
	u, err := user.Current()
	if err != nil {
		return os.Getenv("USER")
	}
	name := u.Username
	if i := strings.LastIndexByte(name, '\\'); i >= 0 { // DOMAIN\user on Windows
		name = name[i+1:]
	}
	return name
}

// dialLibvirtOverSSH opens a channel on c to the libvirt daemon t names.
func dialLibvirtOverSSH(c *ssh.Client, t Target) (net.Conn, error) {
	proxy := t.Proxy
	if proxy == ProxyAuto && t.Path != "/system" && t.Socket == "" {
		proxy = ProxyHelper // only the helper knows where a session daemon listens
	}
	if proxy == ProxyAuto || proxy == ProxyStreamLocal {
		sock := t.Socket
		if sock == "" {
			sock = remoteSystemSocket
		}
		conn, err := c.Dial("unix", sock)
		if err == nil || proxy == ProxyStreamLocal {
			if err != nil {
				return nil, fmt.Errorf("opening %s on %s over SSH: %w", sock, t.Host, err)
			}
			return conn, nil
		}
	}
	return helperConn(c, t.DriverURI())
}

// helperConn runs `virt-ssh-helper uri` on the host and speaks libvirt's
// protocol over its stdin and stdout, as libvirt's own client does.
func helperConn(c *ssh.Client, uri string) (net.Conn, error) {
	s, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	stdin, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	stdout, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	hc := &execConn{s: s, r: stdout, w: stdin}
	s.Stderr = &hc.stderr // written by the session's own goroutine
	if err := s.Start("virt-ssh-helper " + shellQuote(uri)); err != nil {
		s.Close()
		return nil, fmt.Errorf("starting virt-ssh-helper: %w", err)
	}
	return hc, nil
}

// execConn is a net.Conn over a remote command's stdin and stdout.
type execConn struct {
	s      *ssh.Session
	r      io.Reader
	w      io.WriteCloser
	stderr lockedBuffer
}

func (c *execConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err == io.EOF {
		if msg := c.stderrText(); msg != "" {
			err = fmt.Errorf("virt-ssh-helper: %s", msg)
		}
	}
	return n, err
}
func (c *execConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *execConn) Close() error {
	c.w.Close()
	return c.s.Close()
}
func (c *execConn) stderrText() string               { return strings.TrimSpace(c.stderr.String()) }
func (c *execConn) LocalAddr() net.Addr              { return execAddr{} }
func (c *execConn) RemoteAddr() net.Addr             { return execAddr{} }
func (c *execConn) SetDeadline(time.Time) error      { return nil }
func (c *execConn) SetReadDeadline(time.Time) error  { return nil }
func (c *execConn) SetWriteDeadline(time.Time) error { return nil }

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type execAddr struct{}

func (execAddr) Network() string { return "ssh-exec" }
func (execAddr) String() string  { return "virt-ssh-helper" }

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
