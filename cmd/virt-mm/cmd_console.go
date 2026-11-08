package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// ctrlBracket leaves the console, as in virsh.
const ctrlBracket = 0x1d

// cmdConsole is virsh console: the machine's serial console in the
// terminal, raw, until Ctrl+] (or the end of stdin when it isn't a
// terminal). The stream is the guest's bytes: --json changes only how an
// error is written.
func cmdConsole(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: virt-mm console <machine> [--dev serial0] [--force]")
	}
	c, cancel := callCtx(ctx)
	s, d, err := machineOn(c, o, m, args[0])
	cancel()
	if err != nil {
		return err
	}
	if !d.State.Active() {
		return fmt.Errorf("%s is not running", d.Name)
	}
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		old, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer term.Restore(fd, old)
		fmt.Fprintf(os.Stderr, "connected to %s; Ctrl+] leaves\r\n", d.Name)
		defer fmt.Fprint(os.Stderr, "\r\n")
	}
	return hv.Console(ctx, s, d.UUID, o.flags["dev"], o.flags["force"] != "", &escapeReader{r: os.Stdin}, os.Stdout)
}

// escapeReader reads r until the escape byte, then ends.
type escapeReader struct {
	r    io.Reader
	done bool
}

func (e *escapeReader) Read(p []byte) (int, error) {
	if e.done {
		return 0, io.EOF
	}
	n, err := e.r.Read(p)
	if i := bytes.IndexByte(p[:n], ctrlBracket); i >= 0 {
		e.done = true
		if i == 0 {
			return 0, io.EOF
		}
		return i, nil
	}
	return n, err
}

func cmdSendKey(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: virt-mm send-key <machine> <keys>… [--holdtime ms]   (keys: ctrl+alt+delete, ctrl+alt+f2, a, enter, …)")
	}
	var hold uint32
	if v := o.flags["holdtime"]; v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return fmt.Errorf("--holdtime %q: milliseconds", v)
		}
		hold = uint32(n)
	}
	for _, c := range args[1:] {
		if _, err := hv.ParseKeys(c); err != nil {
			return err
		}
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	s, d, err := machineOn(ctx, o, m, args[0])
	if err != nil {
		return err
	}
	if err := hv.SendKeys(ctx, s, d.UUID, args[1:], hold); err != nil {
		return err
	}
	out(o, map[string]any{"name": d.Name, "sent": args[1:]}, func(w io.Writer) {
		fmt.Fprintf(w, "%s: sent %s\n", d.Name, strings.Join(args[1:], " "))
	})
	return nil
}
