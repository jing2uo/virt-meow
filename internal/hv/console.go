package hv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// consoleGrace is how long libvirtd has to confirm the end of a console's
// stream once ctx has ended, before the connection is dropped.
const consoleGrace = 5 * time.Second

// Console connects in and out to a running machine's serial console (its
// first, or the one named dev, as "serial0" or "console1") through
// libvirt's stream, the same over SSH as locally. It returns when in ends
// (io.EOF: the user left), when the machine stops, or when ctx ends. With
// force, a console someone else has open is taken over.
//
// The end of ctx ends the stream the way the end of in does, with a finish
// libvirtd confirms; only if it doesn't within consoleGrace is the
// connection closed, as for any call that outlives its context. Closing a
// connection while its console stream is open and the guest writes can
// deadlock libvirtd itself, from 9.2 to at least 11.x: daemonStreamEvent
// closes the client under priv->lock, which the close hook takes again
// (fix posted upstream in September 2026, "remote: fix a daemon
// self-deadlock on the stream error path"). The host then answers no RPC
// until libvirtd is restarted.
func Console(ctx context.Context, sess *conn.Session, uuid, dev string, force bool, in io.Reader, out io.Writer) error {
	u, err := ParseUUID(uuid)
	if err != nil {
		return err
	}
	// in goes through a pipe that the end of ctx closes: go-libvirt's
	// sender, blocked reading it, then sends the finish. in's own end
	// (EOF, or an error: an abort) passes through.
	pr, pw := io.Pipe()
	defer pr.Close()
	go func() {
		_, err := io.Copy(pw, in)
		pw.CloseWithError(err)
	}()
	call, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		pw.Close()
		time.AfterFunc(consoleGrace, cancel)
	})
	defer stop()
	err = sess.Do(call, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		state, _, err := lv.DomainGetState(dom, 0)
		if err != nil {
			return err
		}
		if !stateOf(libvirt.DomainState(state)).Active() {
			return fmt.Errorf("%s is not running", dom.Name)
		}
		// libvirt says "cannot find character device <null>" of a machine
		// without one.
		x, err := lv.DomainGetXMLDesc(dom, 0)
		if err != nil {
			return err
		}
		var d libvirtxml.Domain
		if err := d.Unmarshal(x); err != nil {
			return err
		}
		if d.Devices == nil || len(d.Devices.Serials)+len(d.Devices.Consoles) == 0 {
			return fmt.Errorf("%s has no serial console", dom.Name)
		}
		var name libvirt.OptString
		if dev != "" {
			name = libvirt.OptString{dev}
		}
		flags := uint32(libvirt.DomainConsoleSafe)
		if force {
			flags |= uint32(libvirt.DomainConsoleForce)
		}
		err = lv.DomainOpenConsoleBidirectional(dom, name, pr, out, flags)
		var le libvirt.Error
		if errors.As(err, &le) && le.Code == uint32(libvirt.ErrOperationFailed) {
			return fmt.Errorf("%w (another client may have it open: force takes it over)", err)
		}
		return err
	})
	if err == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
