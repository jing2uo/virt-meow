package conn

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

// SecretService is the freedesktop.org Secret Service API over the
// session's D-Bus ($DBUS_SESSION_BUS_ADDRESS, else $XDG_RUNTIME_DIR/bus;
// never a bus launched for it), in pure Go. Items go into the default
// collection (the login keyring), labelled "virt-meow: …" and found by their
// attributes. The secret crosses the bus in a "plain" session: the bus is
// the user's own, as every client of the keychain on it is.
type SecretService struct {
	// Address is the bus to use; "" for the session's.
	Address string
}

const (
	ssName     = "org.freedesktop.secrets"
	ssPath     = dbus.ObjectPath("/org/freedesktop/secrets")
	ssService  = "org.freedesktop.Secret.Service"
	ssItem     = "org.freedesktop.Secret.Item"
	ssColl     = "org.freedesktop.Secret.Collection"
	ssPrompt   = "org.freedesktop.Secret.Prompt"
	ssSchema   = "io.github.jing2uo.VirtMeow.Secret"
	ssCallTime = 5 * time.Second
)

// ssSecret is the Secret Service's Secret struct, (oayays).
type ssSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

// ssConn is one use of the service: a private bus connection and a
// session on it, closed after.
type ssConn struct {
	bus     *dbus.Conn
	session dbus.ObjectPath
}

func (s *SecretService) open(ctx context.Context) (*ssConn, error) {
	var bus *dbus.Conn
	var err error
	if s.Address != "" {
		bus, err = dbus.Dial(s.Address)
	} else {
		bus, err = dbus.SessionBusPrivateNoAutoStartup()
	}
	if err != nil {
		return nil, fmt.Errorf("the Secret Service: no D-Bus session: %w", err)
	}
	if err = bus.Auth(nil); err == nil {
		err = bus.Hello()
	}
	if err != nil {
		bus.Close()
		return nil, fmt.Errorf("the Secret Service: D-Bus: %w", err)
	}
	c := &ssConn{bus: bus}
	var out dbus.Variant
	if err := c.call(ctx, ssPath, ssService+".OpenSession", []any{&out, &c.session}, "plain", dbus.MakeVariant("")); err != nil {
		bus.Close()
		return nil, fmt.Errorf("the Secret Service: %w", err)
	}
	return c, nil
}

func (c *ssConn) close() { c.bus.Close() }

// call calls method on the object at path, storing its results in out,
// within ssCallTime unless ctx ends first.
func (c *ssConn) call(ctx context.Context, path dbus.ObjectPath, method string, out []any, args ...any) error {
	ctx, cancel := context.WithTimeout(ctx, ssCallTime)
	defer cancel()
	call := c.bus.Object(ssName, path).CallWithContext(ctx, method, 0, args...)
	if call.Err != nil {
		return call.Err
	}
	if len(out) > 0 {
		return call.Store(out...)
	}
	return nil
}

// collection is the default collection, unlocked.
func (c *ssConn) collection(ctx context.Context) (dbus.ObjectPath, error) {
	var path dbus.ObjectPath
	if err := c.call(ctx, ssPath, ssService+".ReadAlias", []any{&path}, "default"); err != nil {
		return "", err
	}
	if path == "/" {
		return "", errors.New("the Secret Service has no default keyring")
	}
	return path, c.unlock(ctx, []dbus.ObjectPath{path})
}

// unlock unlocks objects, through the keychain's own prompt (a desktop
// dialog) when it needs the user, waiting for it as long as ctx lets.
func (c *ssConn) unlock(ctx context.Context, objects []dbus.ObjectPath) error {
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := c.call(ctx, ssPath, ssService+".Unlock", []any{&unlocked, &prompt}, objects); err != nil {
		return err
	}
	return c.prompt(ctx, prompt)
}

// prompt runs a prompt the service returned ("/" for none) and waits for
// it to complete.
func (c *ssConn) prompt(ctx context.Context, prompt dbus.ObjectPath) error {
	if prompt == "/" || prompt == "" {
		return nil
	}
	if err := c.bus.AddMatchSignalContext(ctx, dbus.WithMatchObjectPath(prompt), dbus.WithMatchInterface(ssPrompt), dbus.WithMatchMember("Completed")); err != nil {
		return err
	}
	signals := make(chan *dbus.Signal, 4)
	c.bus.Signal(signals)
	defer c.bus.RemoveSignal(signals)
	if err := c.call(ctx, prompt, ssPrompt+".Prompt", nil, ""); err != nil {
		return err
	}
	for {
		select {
		case sig := <-signals:
			if sig.Path != prompt || sig.Name != ssPrompt+".Completed" {
				continue
			}
			if len(sig.Body) > 0 {
				if dismissed, _ := sig.Body[0].(bool); dismissed {
					return errors.New("the keychain stayed locked")
				}
			}
			return nil
		case <-ctx.Done():
			c.call(context.Background(), prompt, ssPrompt+".Dismiss", nil)
			return ctx.Err()
		}
	}
}

func ssAttributes(s Secret) map[string]string {
	a := map[string]string{"xdg:schema": ssSchema, "kind": s.Kind}
	if s.Kind == "passphrase" {
		a["key"] = s.Key
	} else {
		a["user"], a["addr"] = s.User, s.Addr
	}
	return a
}

// search finds the items holding s, unlocked.
func (c *ssConn) search(ctx context.Context, s Secret) ([]dbus.ObjectPath, error) {
	var unlocked, locked []dbus.ObjectPath
	if err := c.call(ctx, ssPath, ssService+".SearchItems", []any{&unlocked, &locked}, ssAttributes(s)); err != nil {
		return nil, err
	}
	if len(locked) > 0 {
		if err := c.unlock(ctx, locked); err != nil {
			return unlocked, err
		}
		unlocked = append(unlocked, locked...)
	}
	return unlocked, nil
}

func (s *SecretService) Available(ctx context.Context) error {
	c, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer c.close()
	var path dbus.ObjectPath
	if err := c.call(ctx, ssPath, ssService+".ReadAlias", []any{&path}, "default"); err != nil {
		return fmt.Errorf("the Secret Service: %w", err)
	}
	if path == "/" {
		return errors.New("the Secret Service has no default keyring")
	}
	return nil
}

func (s *SecretService) Get(ctx context.Context, sec Secret) (string, bool, error) {
	c, err := s.open(ctx)
	if err != nil {
		return "", false, err
	}
	defer c.close()
	items, err := c.search(ctx, sec)
	if err != nil || len(items) == 0 {
		return "", false, err
	}
	var got map[dbus.ObjectPath]ssSecret
	if err := c.call(ctx, ssPath, ssService+".GetSecrets", []any{&got}, items[:1], c.session); err != nil {
		return "", false, fmt.Errorf("the Secret Service: %w", err)
	}
	v, ok := got[items[0]]
	return string(v.Value), ok, nil
}

func (s *SecretService) Set(ctx context.Context, sec Secret, value string) error {
	c, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer c.close()
	coll, err := c.collection(ctx)
	if err != nil {
		return fmt.Errorf("the Secret Service: %w", err)
	}
	props := map[string]dbus.Variant{
		ssItem + ".Label":      dbus.MakeVariant("virt-meow: " + sec.String()),
		ssItem + ".Attributes": dbus.MakeVariant(ssAttributes(sec)),
	}
	secret := ssSecret{Session: c.session, Parameters: []byte{}, Value: []byte(value), ContentType: "text/plain; charset=utf8"}
	var item, prompt dbus.ObjectPath
	if err := c.call(ctx, coll, ssColl+".CreateItem", []any{&item, &prompt}, props, secret, true); err != nil {
		return fmt.Errorf("the Secret Service: %w", err)
	}
	return c.prompt(ctx, prompt)
}

func (s *SecretService) Delete(ctx context.Context, sec Secret) (bool, error) {
	c, err := s.open(ctx)
	if err != nil {
		return false, err
	}
	defer c.close()
	items, err := c.search(ctx, sec)
	if err != nil {
		return false, err
	}
	for _, it := range items {
		var prompt dbus.ObjectPath
		if err := c.call(ctx, it, ssItem+".Delete", []any{&prompt}); err != nil {
			return false, fmt.Errorf("the Secret Service: %w", err)
		}
		if err := c.prompt(ctx, prompt); err != nil {
			return false, err
		}
	}
	return len(items) > 0, nil
}
