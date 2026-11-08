package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"

	"github.com/jing2uo/virt-meow/internal/console"
)

// Console is a machine's console as the page opens it.
type Console struct {
	Kind string `json:"kind"` // vnc, spice or serial
	// URL is the WebSocket to give the client: absolute in the window, a
	// path under serve, which the page resolves against its own address.
	URL   string `json:"url"`
	Token string `json:"token"` // for CloseConsole
}

// OpenConsole checks that the running machine has a console of kind (vnc
// or spice) and returns the WebSocket its client connects to.
func (s *Service) OpenConsole(id, uuid, kind string) (Console, error) {
	c := s.c
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if _, err := c.m.FindGraphics(ctx, id, uuid, kind); err != nil {
		return Console{}, err
	}
	token := c.bridge.Open(func(ctx context.Context) (net.Conn, error) {
		return c.m.DialGraphics(ctx, id, uuid, kind)
	})
	return Console{Kind: kind, URL: c.opts.ConsoleBase + console.Path(token), Token: token}, nil
}

// OpenConsoleWindow opens a machine's console in a window of its own, as
// virt-manager does: the page there shows only that console, the window
// titled with the machine's name. Only the desktop window can; a browser
// opens a tab instead.
func (s *Service) OpenConsoleWindow(id, machine, kind string) error {
	if s.c.opts.OpenWindow == nil {
		return errors.New("only the desktop window opens windows; a browser opens a tab instead")
	}
	u, err := SoloConsoleURL(id, machine, kind)
	if err != nil {
		return err
	}
	return s.c.opts.OpenWindow(u, machine)
}

// SoloConsoleURL is the page showing only a machine's console of kind
// (vnc, spice, serial, or "" for its first) on connection id.
func SoloConsoleURL(id, machine, kind string) (string, error) {
	switch kind {
	case "", "vnc", "spice", "serial":
	default:
		return "", fmt.Errorf("no console kind %q (want vnc, spice or serial)", kind)
	}
	if id == "" || machine == "" {
		return "", errors.New("a console window needs a connection and a machine")
	}
	u := "/?solo#/" + url.PathEscape(id) + "/console/" + url.PathEscape(machine)
	if kind != "" {
		u += "/" + kind
	}
	return u, nil
}

// CloseConsole ends a console the page opened, and its connections.
func (s *Service) CloseConsole(token string) { s.c.bridge.Close(token) }

// OpenSerial returns the WebSocket a terminal connects to for the running
// machine's serial console: its bytes both ways, raw. Each connection
// opens the console's stream (taking it over from another client when
// force); when the page goes, the stream gets the end of its input and
// finishes before anything closes the host's connection (docs/adr/0002).
// Why it couldn't open, or that it ended, is written to the terminal.
func (s *Service) OpenSerial(id, uuid string, force bool) (Console, error) {
	c := s.c
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	in, err := c.m.Info(ctx, id, uuid)
	if err != nil {
		return Console{}, err
	}
	if !in.State.Active() {
		return Console{}, fmt.Errorf("%s is not running", in.Name)
	}
	token := c.bridge.Open(func(context.Context) (net.Conn, error) {
		page, ours := net.Pipe()
		go func() {
			defer ours.Close()
			// No deadline: the stream ends with the page (ours reads EOF
			// when the bridge closes page) or with the machine.
			err := c.m.Console(context.Background(), id, uuid, "", force, ours, ours)
			if err != nil {
				fmt.Fprintf(ours, "\r\n[virt-mm: %v]\r\n", err)
			} else {
				fmt.Fprint(ours, "\r\n[virt-mm: the console ended]\r\n")
			}
		}()
		return page, nil
	})
	return Console{Kind: "serial", URL: c.opts.ConsoleBase + console.Path(token), Token: token}, nil
}

// GrabKeyboard has the window take the keys the desktop keeps for itself
// (Super, Alt+Tab, Ctrl+Alt+F…) for the console, or give them back. ok is
// false where the front end can't: in a browser, whose page uses the
// Keyboard Lock API instead, or in a window on a platform not done yet.
func (s *Service) GrabKeyboard(ctx context.Context, on bool) (bool, error) {
	if s.c.opts.GrabKeyboard == nil {
		return false, nil
	}
	return s.c.opts.GrabKeyboard(ctx, on)
}
