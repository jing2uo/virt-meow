package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/hv"
)

func cmdPool(ctx context.Context, o opts, m *app.Manager, args []string) error {
	const use = `usage: virt-mm pool add <name> --type dir --path <dir> | --type logical [--source <vg>]
                | --type netfs --source <host>:/<export> [--path <mount point>]  [--autostart on|off] [--no-start]
       virt-mm pool start|stop <name>
       virt-mm pool autostart <name> on|off
       virt-mm pool rm <name>`
	if len(args) < 2 {
		return errors.New(use)
	}
	sub, name, rest := args[0], args[1], args[2:]
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	show := func(p hv.Pool, what string) {
		out(o, p, func(w io.Writer) {
			src := ""
			if p.Source != "" {
				src = " from " + p.Source
			}
			fmt.Fprintf(w, "pool %s %s: %s %s%s, %s, autostart %s\n", p.Name, what, p.Type, p.Path, src,
				map[bool]string{true: "active", false: "stopped"}[p.Active], map[bool]string{true: "on", false: "off"}[p.Autostart])
		})
	}
	var a hv.PoolAction
	switch sub {
	case "add":
		if len(rest) != 0 {
			return errors.New(use)
		}
		np := hv.NewPool{Name: name, Type: o.flags["type"], Path: o.flags["path"], Source: o.flags["source"], NoStart: o.flags["no-start"] != ""}
		if v := o.flags["autostart"]; v != "" {
			if np.Autostart, err = parseOnOff(v); err != nil {
				return fmt.Errorf("--autostart: %w", err)
			}
		}
		p, err := hv.AddPool(ctx, s, np)
		if err != nil {
			return hint(err)
		}
		show(p, "added")
		return nil
	case "rm", "remove":
		if len(rest) != 0 {
			return errors.New(use)
		}
		if err := hv.RemovePool(ctx, s, name); err != nil {
			return hint(err)
		}
		out(o, map[string]string{"removed": name}, func(w io.Writer) {
			fmt.Fprintf(w, "pool %s removed (what it holds stays on the host)\n", name)
		})
		return nil
	case "start", "stop":
		if len(rest) != 0 {
			return errors.New(use)
		}
		a = hv.PoolAction(sub)
	case "autostart":
		if len(rest) != 1 {
			return errors.New(use)
		}
		on, err := parseOnOff(rest[0])
		if err != nil {
			return err
		}
		a = map[bool]hv.PoolAction{true: hv.PoolAutostartOn, false: hv.PoolAutostartOff}[on]
	default:
		return errors.New(use)
	}
	p, err := hv.SetPool(ctx, s, name, a)
	if err != nil {
		return hint(err)
	}
	show(p, "now")
	return nil
}

func cmdVol(ctx context.Context, o opts, m *app.Manager, args []string) error {
	const use = `usage: virt-mm vol create <pool>/<name> <size> [--format qcow2|raw]
       virt-mm vol resize <pool>/<name> <size>|+<size>
       virt-mm vol rm <pool>/<name>
       virt-mm vol download <pool>/<name> <file>`
	if len(args) < 2 {
		return errors.New(use)
	}
	sub, ref, rest := args[0], args[1], args[2:]
	pool, name, _ := strings.Cut(ref, "/")
	if name == "" {
		return fmt.Errorf("%q: want <pool>/<volume>", ref)
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	show := func(v hv.Volume, what string) {
		out(o, v, func(w io.Writer) {
			fmt.Fprintf(w, "%s/%s %s: %s, %s (%s)\n", v.Pool, v.Name, what, v.Format, bytesOf(v.Capacity), v.Path)
		})
	}
	size := func(s string) (uint64, bool, error) {
		grow := strings.HasPrefix(s, "+")
		mib, err := parseSize(strings.TrimPrefix(s, "+"), "G")
		return uint64(mib) << 20, grow, err
	}
	switch sub {
	case "create":
		if len(rest) != 1 {
			return errors.New(use)
		}
		b, grow, err := size(rest[0])
		if err != nil || grow {
			return fmt.Errorf("size %q", rest[0])
		}
		c, cancel := callCtx(ctx)
		defer cancel()
		v, err := hv.CreateVolume(c, s, pool, name, b, o.flags["format"])
		if err != nil {
			return hint(err)
		}
		show(v, "created")
	case "resize":
		if len(rest) != 1 {
			return errors.New(use)
		}
		b, grow, err := size(rest[0])
		if err != nil {
			return fmt.Errorf("size: %w", err)
		}
		c, cancel := callCtx(ctx)
		defer cancel()
		if grow {
			vs, err := hv.Volumes(c, s, pool)
			if err != nil {
				return hint(err)
			}
			i := indexOf(vs, func(v hv.Volume) bool { return v.Name == name })
			if i < 0 {
				return fmt.Errorf("pool %s has no volume %s", pool, name)
			}
			b += vs[i].Capacity
		}
		v, err := hv.ResizeVolume(c, s, pool, name, b)
		if err != nil {
			return hint(err)
		}
		show(v, "is now")
	case "rm", "delete":
		if len(rest) != 0 {
			return errors.New(use)
		}
		c, cancel := callCtx(ctx)
		defer cancel()
		if err := hv.DeleteVolume(c, s, pool, name); err != nil {
			return hint(err)
		}
		out(o, map[string]string{"deleted": pool + "/" + name}, func(w io.Writer) { fmt.Fprintf(w, "deleted %s/%s\n", pool, name) })
	case "download":
		if len(rest) != 1 {
			return errors.New(use)
		}
		c, ok := m.Find(s.Target().URI)
		id := c.ID
		if !ok {
			id = "uri:" + s.Target().URI
		}
		last := time.Now()
		v, err := m.Download(ctx, id, pool, name, rest[0], func(got, total int64) {
			if time.Since(last) > time.Second {
				last = time.Now()
				fmt.Fprintf(os.Stderr, "\r%s / %s", bytesOf(uint64(got)), bytesOf(uint64(total)))
			}
		})
		fmt.Fprint(os.Stderr, "\r\033[K")
		if err != nil {
			return hint(err)
		}
		out(o, map[string]any{"volume": v, "file": rest[0]}, func(w io.Writer) {
			fmt.Fprintf(w, "downloaded %s/%s to %s\n", pool, name, rest[0])
		})
	default:
		return errors.New(use)
	}
	return nil
}

func bytesOf(b uint64) string { return kib(b >> 10) }
