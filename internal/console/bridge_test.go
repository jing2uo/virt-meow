package console

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// echo is a console server that sends a greeting, then echoes.
func echo(greeting string) Dial {
	return func(ctx context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			b.Write([]byte(greeting))
			buf := make([]byte, 1024)
			for {
				n, err := b.Read(buf)
				if err != nil {
					return
				}
				b.Write(buf[:n])
			}
		}()
		return a, nil
	}
}

func dial(t *testing.T, srv *httptest.Server, token string) (*websocket.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + Path(token)
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	return c, err
}

func read(t *testing.T, c *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("message type %v, want binary", typ)
	}
	return b
}

func TestBridge(t *testing.T) {
	b := New()
	srv := httptest.NewServer(b)
	defer srv.Close()
	token := b.Open(echo("RFB 003.008\n"))

	// Two connections on one token, as SPICE's channels make.
	for range 2 {
		c, err := dial(t, srv, token)
		if err != nil {
			t.Fatal(err)
		}
		if c.Subprotocol() != "binary" {
			t.Errorf("subprotocol %q", c.Subprotocol())
		}
		if got := read(t, c); string(got) != "RFB 003.008\n" {
			t.Fatalf("greeting %q", got)
		}
		c.Write(context.Background(), websocket.MessageBinary, []byte("ping"))
		if got := read(t, c); !bytes.Equal(got, []byte("ping")) {
			t.Fatalf("echo %q", got)
		}
		c.CloseNow()
	}

	if _, err := dial(t, srv, "nope"); err == nil {
		t.Error("connected with an unknown token")
	}

	// Close ends the connections made with the token, and the token.
	c, err := dial(t, srv, token)
	if err != nil {
		t.Fatal(err)
	}
	read(t, c)
	b.Close(token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("after Close, read: %v", err)
	}
	if _, err := dial(t, srv, token); err == nil {
		t.Error("connected with a closed token")
	}
}

func TestBridgeDialFails(t *testing.T) {
	b := New()
	srv := httptest.NewServer(b)
	defer srv.Close()
	token := b.Open(func(context.Context) (net.Conn, error) { return nil, errors.New("not running") })
	if _, err := dial(t, srv, token); err == nil {
		t.Fatal("connected to a console whose server can't be reached")
	}
}

func TestBridgeIdleTokensGo(t *testing.T) {
	b := New()
	b.Idle = time.Millisecond
	token := b.Open(echo(""))
	time.Sleep(5 * time.Millisecond)
	b.Open(echo("")) // sweeps
	b.mu.Lock()
	_, ok := b.tickets[token]
	b.mu.Unlock()
	if ok {
		t.Error("an idle token stayed")
	}
}
