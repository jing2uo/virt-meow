// Package console carries a machine's graphical console to a page: the
// WebSocket that the page's VNC or SPICE client (noVNC, spice-html5) opens
// is bridged to the machine's server, as websockify does, over whatever
// connection the dial function makes (the host's SSH connection for a
// remote host). The desktop window serves it on a loopback port of its
// own; virt-mm serve will mount the same handler (docs/adr/0003).
package console

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Dial connects to a console's server.
type Dial func(ctx context.Context) (net.Conn, error)

// Bridge hands out tokens for consoles and bridges the WebSockets opened
// with them. A token is the only thing that authorises a connection: it is
// unguessable, and the page gets it from virt-meow itself.
type Bridge struct {
	// Idle is how long a token lives without a connection open on it; a
	// page that went away without closing its console leaves nothing
	// behind for longer.
	Idle time.Duration

	mu      sync.Mutex
	tickets map[string]*ticket
}

type ticket struct {
	dial   Dial
	ctx    context.Context
	cancel context.CancelFunc
	open   int       // connections bridged now
	last   time.Time // when the last one ended, or the token was made
}

// New makes a bridge whose tokens live two minutes without a connection.
func New() *Bridge {
	return &Bridge{Idle: 2 * time.Minute, tickets: map[string]*ticket{}}
}

// Open issues a token for a console. It stays valid until Close, or until
// it has had no connection for Idle; a SPICE client opens one connection
// per channel with it, and a client may reconnect.
func (b *Bridge) Open(dial Dial) string {
	k := make([]byte, 16)
	rand.Read(k)
	token := hex.EncodeToString(k)
	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked()
	b.tickets[token] = &ticket{dial: dial, ctx: ctx, cancel: cancel, last: time.Now()}
	return token
}

// Close ends a console: its token and every connection made with it.
func (b *Bridge) Close(token string) {
	b.mu.Lock()
	t := b.tickets[token]
	delete(b.tickets, token)
	b.mu.Unlock()
	if t != nil {
		t.cancel()
	}
}

func (b *Bridge) sweepLocked() {
	for k, t := range b.tickets {
		if t.open == 0 && time.Since(t.last) > b.Idle {
			t.cancel()
			delete(b.tickets, k)
		}
	}
}

// Path is where a token's WebSocket is served.
func Path(token string) string { return "/console/" + token }

// ServeHTTP bridges a WebSocket at Path(token) to the token's console.
func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.URL.Path, "/console/")
	b.mu.Lock()
	b.sweepLocked()
	t := b.tickets[token]
	if ok && t != nil {
		t.open++
	}
	b.mu.Unlock()
	if !ok || t == nil {
		http.Error(w, "no such console", http.StatusNotFound)
		return
	}
	defer func() {
		b.mu.Lock()
		t.open--
		t.last = time.Now()
		b.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()

	dctx, dcancel := context.WithTimeout(ctx, 30*time.Second)
	server, err := t.dial(dctx)
	dcancel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer server.Close()

	// The page's origin (wails://…, http://wails.localhost) is never the
	// listener's own: the token, not the origin, is what is checked.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"binary"},
		InsecureSkipVerify: true,
	})
	if err != nil {
		return // Accept has answered the request
	}
	ws.SetReadLimit(4 << 20)
	page := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer page.Close()
	stopBoth := context.AfterFunc(ctx, func() { page.Close(); server.Close() })
	defer stopBoth()

	done := make(chan struct{}, 2)
	go func() { io.Copy(server, page); done <- struct{}{} }()
	go func() { io.Copy(page, server); done <- struct{}{} }()
	<-done
	cancel()
	<-done
}
