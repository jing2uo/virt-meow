package conn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
	"golang.org/x/crypto/ssh"
)

// Status is where a Session is in its life.
type Status string

const (
	Connecting   Status = "connecting"
	Connected    Status = "connected"
	Disconnected Status = "disconnected" // Err says why; it may come back on its own
	Closed       Status = "closed"
)

// State is a Session's status, why it is so, and since when.
type State struct {
	Status Status
	Err    error // the last failure, for Disconnected
	Since  time.Time
}

// ErrClosed is returned by a Session after Close.
var ErrClosed = errors.New("connection closed")

// Options tune a Session. The zero value works for a connection without SSH.
type Options struct {
	HostKeys *HostKeys
	Prompter Prompter
	// Secrets is the keychain answers are remembered in; nil for none.
	Secrets Secrets

	ConnectTimeout    time.Duration // one dial and libvirt handshake; default 20 s
	KeepaliveInterval time.Duration // probe an idle connection this often; default 30 s
	ProbeTimeout      time.Duration // one probe; default 10 s
	RetryMin          time.Duration // first reconnect delay; default 2 s
	RetryMax          time.Duration // longest reconnect delay; default 60 s

	// OnState is called, in order and never concurrently, on each change.
	OnState func(State)
	// OnLifecycle is called for each domain lifecycle event, from the
	// connection's event goroutine. After a reconnect, events missed while
	// disconnected are not replayed: OnState(Connected) is the cue to reload.
	OnLifecycle func(libvirt.DomainEventLifecycleMsg)
}

func (o *Options) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&o.ConnectTimeout, 20*time.Second)
	set(&o.KeepaliveInterval, 30*time.Second)
	set(&o.ProbeTimeout, 10*time.Second)
	set(&o.RetryMin, 2*time.Second)
	set(&o.RetryMax, 60*time.Second)
}

// Session owns one libvirt connection and is the only thing that touches its
// go-libvirt client (docs/adr/0002, "Session upkeep"):
//
//   - calls take a context, and one that ends closes the transport;
//   - one connection attempt at a time, waited out before the next;
//   - liveness is the transport's own read and write errors, never
//     go-libvirt's racy Disconnected/IsConnected;
//   - keepalive probes, and reconnection with backoff once it has worked.
type Session struct {
	target Target
	opts   Options
	pool   *sshPool

	mu         sync.Mutex
	live       *link         // nil when not connected
	connecting chan struct{} // closed when the attempt in progress ends
	state      State
	everUp     bool // has been connected once: reconnect on its own
	closed     bool

	stateMu     sync.Mutex // guards sending on stateCh against closing it
	stateCh     chan State
	stateClosed bool
	stop        chan struct{}
	done        sync.WaitGroup
}

// link is one established connection: the client and what it runs on.
type link struct {
	lv      *libvirt.Libvirt
	raw     *watchedConn
	ssh     *ssh.Client        // the host's SSH client; nil for a local connection
	release func()             // the SSH client's pool reference, or a no-op
	events  context.Context    // lives as long as the link
	cancel  context.CancelFunc // ends events
	lastUse atomic.Int64       // unix nanos of the last call that worked
}

// New returns a Session for target. It doesn't connect: the first call does,
// or Connect.
func New(target Target, opts Options) *Session {
	opts.defaults()
	s := &Session{
		target:  target,
		opts:    opts,
		pool:    defaultPool,
		state:   State{Status: Disconnected, Since: time.Now()},
		stateCh: make(chan State, 16),
		stop:    make(chan struct{}),
	}
	s.done.Add(2)
	go s.deliverStates()
	go s.upkeep()
	return s
}

// Target is what the session connects to.
func (s *Session) Target() Target { return s.target }

// State is the session's current state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Connect connects now if not connected.
func (s *Session) Connect(ctx context.Context) error {
	_, err := s.client(ctx)
	return err
}

// Do runs fn against the live client, connecting first if needed. If ctx
// ends before fn returns, the connection is closed (which ends fn too, and
// any other call on it) and ctx's error returned; the next call reconnects.
func (s *Session) Do(ctx context.Context, fn func(*libvirt.Libvirt) error) error {
	l, err := s.client(ctx)
	if err != nil {
		return err
	}
	res := make(chan error, 1)
	go func() { res <- fn(l.lv) }()
	select {
	case err := <-res:
		if l.raw.dead() {
			s.evict(l, fmt.Errorf("connection lost: %w", l.raw.cause()))
			if err != nil {
				return fmt.Errorf("%s: %w", s.target, err)
			}
			return nil
		}
		if err == nil {
			l.lastUse.Store(time.Now().UnixNano())
		}
		return err
	case <-ctx.Done():
		s.evict(l, fmt.Errorf("a call took too long: %w", ctx.Err()))
		return ctx.Err()
	}
}

// Close ends the session for good.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	l := s.live
	s.live = nil
	wait := s.connecting
	s.mu.Unlock()

	if wait != nil {
		<-wait // the attempt sees closed and tears itself down
	}
	if l != nil {
		l.cancel()
		// Say goodbye to libvirtd if it answers quickly; close regardless.
		bye := make(chan struct{})
		go func() { l.lv.Disconnect(); close(bye) }()
		select {
		case <-bye:
		case <-time.After(2 * time.Second):
		}
		l.raw.Close()
		l.release()
	}
	s.setState(Closed, nil)
	s.stateMu.Lock()
	s.stateClosed = true
	close(s.stateCh)
	s.stateMu.Unlock()
	s.done.Wait()
	return nil
}

// client returns the live link, connecting if there is none.
func (s *Session) client(ctx context.Context) (*link, error) {
	s.mu.Lock()
	for {
		if s.closed {
			s.mu.Unlock()
			return nil, ErrClosed
		}
		if s.live != nil && !s.live.raw.dead() {
			l := s.live
			s.mu.Unlock()
			return l, nil
		}
		if s.connecting != nil {
			wait := s.connecting
			s.mu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			s.mu.Lock()
			if s.live == nil && s.state.Err != nil && !s.closed {
				// The attempt we waited for failed: report it rather than
				// starting another straight away.
				err := s.state.Err
				s.mu.Unlock()
				return nil, err
			}
			continue
		}
		if s.live != nil { // dead
			l := s.live
			s.live = nil
			s.mu.Unlock()
			s.teardown(l)
			s.mu.Lock()
			continue
		}
		s.connecting = make(chan struct{})
		s.mu.Unlock()

		s.setState(Connecting, nil)
		l, err := s.connect(ctx)

		s.mu.Lock()
		close(s.connecting)
		s.connecting = nil
		if err == nil && s.closed {
			s.mu.Unlock()
			s.teardown(l)
			return nil, ErrClosed
		}
		if err != nil {
			s.mu.Unlock()
			s.setState(Disconnected, err)
			return nil, err
		}
		s.live = l
		s.everUp = true
		s.mu.Unlock()
		s.setState(Connected, nil)
		s.startEvents(ctx, l)
		return l, nil
	}
}

// connect dials and performs libvirt's handshake, bounded by ctx and the
// connect timeout. It never leaves an attempt running: one that times out
// has its transport closed and is waited for.
func (s *Session) connect(ctx context.Context) (*link, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.ConnectTimeout)
	defer cancel()

	var raw net.Conn
	var sshc *ssh.Client
	release := func() {}
	var err error
	switch s.target.Transport {
	case Local:
		raw, err = dialLocal(ctx, s.target)
	case SSH:
		hk := s.opts.HostKeys
		if hk == nil {
			hk = &HostKeys{}
		}
		var c *ssh.Client
		c, release, err = s.pool.get(ctx, s.target, hk, &asker{prompt: s.opts.Prompter, secrets: s.opts.Secrets})
		sshc = c
		if err == nil {
			raw, err = dialLibvirtOverSSH(c, s.target)
			if err != nil {
				release()
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.target, err)
	}

	w := &watchedConn{Conn: raw}
	lv := libvirt.NewWithDialer(dialers.NewAlreadyConnected(w))
	res := make(chan error, 1)
	go func() { res <- lv.ConnectToURI(libvirt.ConnectURI(s.target.DriverURI())) }()
	select {
	case err = <-res:
	case <-ctx.Done():
		w.Close()
		<-res // closed transport: returns promptly
		err = ctx.Err()
	}
	if err != nil {
		w.Close()
		release()
		if w.dead() && w.cause() != nil && ctx.Err() == nil {
			err = w.cause()
		}
		return nil, fmt.Errorf("%s: opening %s: %w", s.target, s.target.DriverURI(), err)
	}
	l := &link{lv: lv, raw: w, ssh: sshc, release: release}
	l.events, l.cancel = context.WithCancel(context.Background())
	l.lastUse.Store(time.Now().UnixNano())
	return l, nil
}

// ErrNotSSH is returned by Run on a connection that isn't over SSH.
var ErrNotSSH = errors.New("not an SSH connection")

// Run runs a command on the host over the connection's SSH client and
// returns its standard output. It is for the few read-only facts libvirt has
// no API for (the host's bridges); docs/adr/0002, "Host commands".
func (s *Session) Run(ctx context.Context, command string) ([]byte, error) {
	l, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	if l.ssh == nil {
		return nil, ErrNotSSH
	}
	sess, err := l.ssh.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	defer stop()
	out, err := sess.Output(command)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, err
}

// DialHost opens a connection to an address on the host: network is "tcp"
// (host:port, as the host sees it) or "unix" (a socket's path). Over SSH it
// is a channel on the connection's SSH client (direct-tcpip or
// direct-streamlocal), so a service listening only on the host's loopback,
// as its VNC consoles do, is reached without opening it to the network; on
// a local connection it is a plain dial on this computer.
func (s *Session) DialHost(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" && network != "unix" {
		return nil, fmt.Errorf("network %q: tcp or unix", network)
	}
	l, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	if l.ssh == nil {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	c, err := l.ssh.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("%s: reaching %s on the host: %w", s.target, addr, err)
	}
	return c, nil
}

// evict drops l if it is still the live link and records why.
func (s *Session) evict(l *link, why error) {
	s.mu.Lock()
	if s.live != l {
		s.mu.Unlock()
		return
	}
	s.live = nil
	closed := s.closed
	s.mu.Unlock()
	s.teardown(l)
	if !closed {
		s.setState(Disconnected, why)
	}
}

func (s *Session) teardown(l *link) {
	l.cancel()
	l.raw.Close()
	l.release()
}

// startEvents subscribes to lifecycle events on l for as long as l lives.
// It returns once libvirt has registered the subscription, so that the
// call that connected sees every event after it (virt-mm events), or when
// ctx or the connect timeout ends: a host that doesn't answer doesn't hold
// up the call, which then finds the connection dead on its own.
func (s *Session) startEvents(ctx context.Context, l *link) {
	if s.opts.OnLifecycle == nil {
		return
	}
	registered := make(chan struct{})
	go func() {
		ch, err := l.lv.LifecycleEvents(l.events)
		close(registered)
		if err != nil {
			return // a dead connection; upkeep notices
		}
		for ev := range ch {
			s.opts.OnLifecycle(ev)
		}
	}()
	t := time.NewTimer(s.opts.ConnectTimeout)
	defer t.Stop()
	select {
	case <-registered:
	case <-ctx.Done():
	case <-t.C:
	}
}

// upkeep probes an idle connection and brings a lost one back.
func (s *Session) upkeep() {
	defer s.done.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	retry := s.opts.RetryMin
	var nextRetry time.Time
	for {
		select {
		case <-s.stop:
			return
		case now := <-tick.C:
			s.mu.Lock()
			l, everUp, busy, lastErr := s.live, s.everUp, s.connecting != nil, s.state.Err
			s.mu.Unlock()
			switch {
			case busy:
			case l != nil && l.raw.dead():
				s.evict(l, fmt.Errorf("connection lost: %w", l.raw.cause()))
			case l != nil:
				retry = s.opts.RetryMin
				if now.Sub(time.Unix(0, l.lastUse.Load())) >= s.opts.KeepaliveInterval {
					ctx, cancel := context.WithTimeout(context.Background(), s.opts.ProbeTimeout)
					s.Do(ctx, func(lv *libvirt.Libvirt) error { _, err := lv.ConnectGetLibVersion(); return err })
					cancel()
				}
			case everUp && !permanent(lastErr):
				if nextRetry.IsZero() {
					nextRetry = now.Add(retry)
				}
				if now.Before(nextRetry) {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), s.opts.ConnectTimeout)
				if s.Connect(ctx) != nil {
					retry = min(retry*2, s.opts.RetryMax)
				} else {
					retry = s.opts.RetryMin
				}
				cancel()
				nextRetry = time.Time{}
			}
		}
	}
}

// permanent errors need the user, not another try.
func permanent(err error) bool {
	var unknown *UnknownHostKeyError
	var changed *HostKeyChangedError
	var auth *AuthError
	return errors.As(err, &unknown) || errors.As(err, &changed) || errors.As(err, &auth)
}

func (s *Session) setState(st Status, err error) {
	s.mu.Lock()
	if s.state.Status == st && st != Disconnected {
		s.mu.Unlock()
		return
	}
	s.state = State{Status: st, Err: err, Since: time.Now()}
	state := s.state
	s.mu.Unlock()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if !s.stateClosed {
		s.stateCh <- state
	}
}

func (s *Session) deliverStates() {
	defer s.done.Done()
	for st := range s.stateCh {
		if s.opts.OnState != nil {
			s.opts.OnState(st)
		}
	}
}

// watchedConn records the first read or write error on a transport: from
// then on the connection is dead.
type watchedConn struct {
	net.Conn
	once sync.Once
	err  atomic.Pointer[error]
}

func (w *watchedConn) fail(err error) {
	w.once.Do(func() { w.err.Store(&err) })
}

func (w *watchedConn) Read(p []byte) (int, error) {
	n, err := w.Conn.Read(p)
	if err != nil {
		w.fail(err)
	}
	return n, err
}

func (w *watchedConn) Write(p []byte) (int, error) {
	n, err := w.Conn.Write(p)
	if err != nil {
		w.fail(err)
	}
	return n, err
}

func (w *watchedConn) Close() error {
	w.fail(net.ErrClosed)
	return w.Conn.Close()
}

func (w *watchedConn) dead() bool { return w.err.Load() != nil }

func (w *watchedConn) cause() error {
	if p := w.err.Load(); p != nil {
		return *p
	}
	return nil
}
