package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/hv"
)

func cmdAgent(ctx context.Context, o opts, m *app.Manager, args []string) error {
	const use = `usage: virt-mm agent password <machine> <user> [--password-stdin]
       virt-mm agent ssh-key ls <machine> <user>
       virt-mm agent ssh-key add|rm <machine> <user> <file|->`
	if len(args) < 1 {
		return errors.New(use)
	}
	switch args[0] {
	case "password":
		if len(args) != 3 {
			return errors.New(use)
		}
		pw, err := newPassword(o, args[2])
		if err != nil {
			return err
		}
		ctx, cancel := callCtx(ctx)
		defer cancel()
		s, d, err := machineOn(ctx, o, m, args[1])
		if err != nil {
			return err
		}
		if err := hv.SetUserPassword(ctx, s, d.UUID, args[2], pw); err != nil {
			return err
		}
		out(o, map[string]string{"name": d.Name, "user": args[2]}, func(w io.Writer) {
			fmt.Fprintf(w, "%s: password of %s set\n", d.Name, args[2])
		})
		return nil
	case "ssh-key", "ssh-keys":
		if len(args) < 4 {
			return errors.New(use)
		}
		sub, ref, user, rest := args[1], args[2], args[3], args[4:]
		var keys []string
		switch {
		case (sub == "ls" || sub == "list") && len(rest) == 0:
		case (sub == "add" || sub == "rm" || sub == "remove") && len(rest) == 1:
			b, err := readFile(rest[0])
			if err != nil {
				return err
			}
			if keys, err = hv.ParseSSHKeys(string(b)); err != nil {
				return fmt.Errorf("%s: %w", rest[0], err)
			}
		default:
			return errors.New(use)
		}
		ctx, cancel := callCtx(ctx)
		defer cancel()
		s, d, err := machineOn(ctx, o, m, ref)
		if err != nil {
			return err
		}
		switch sub {
		case "add":
			err = hv.AddSSHKeys(ctx, s, d.UUID, user, keys)
		case "rm", "remove":
			err = hv.RemoveSSHKeys(ctx, s, d.UUID, user, keys)
		}
		if err != nil {
			return err
		}
		all, err := hv.SSHKeys(ctx, s, d.UUID, user)
		if err != nil {
			return err
		}
		out(o, all, func(w io.Writer) {
			if len(all) == 0 {
				fmt.Fprintf(w, "%s: %s accepts no SSH keys\n", d.Name, user)
			}
			for _, k := range all {
				fmt.Fprintln(w, k)
			}
		})
		return nil
	}
	return errors.New(use)
}

// newPassword reads the password to set: one line of stdin with
// --password-stdin, else asked twice on the terminal without echo.
func newPassword(o opts, user string) (string, error) {
	if o.flags["password-stdin"] != "" {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && (err != io.EOF || line == "") {
			return "", fmt.Errorf("--password-stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	pw, err := ask(fmt.Sprintf("new password for %s: ", user))
	if err != nil {
		return "", fmt.Errorf("%w: give it with --password-stdin", err)
	}
	again, err := ask("again: ")
	if err != nil {
		return "", err
	}
	if pw != again {
		return "", errors.New("the two passwords differ")
	}
	return pw, nil
}
