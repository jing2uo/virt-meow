package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

func cmdSnap(ctx context.Context, o opts, m *app.Manager, args []string) error {
	const use = `usage: virt-mm snap ls <machine>
       virt-mm snap create <machine> [<name>] [--description D]
       virt-mm snap revert <machine> <name>
       virt-mm snap rm <machine> <name> [--children]`
	if len(args) < 2 {
		return errors.New(use)
	}
	sub, ref, rest := args[0], args[1], args[2:]
	switch sub {
	case "ls", "list":
		if len(rest) != 0 {
			return errors.New(use)
		}
		ctx, cancel := callCtx(ctx)
		defer cancel()
		s, d, err := machineOn(ctx, o, m, ref)
		if err != nil {
			return err
		}
		snaps, err := hv.Snapshots(ctx, s, d.UUID)
		if err != nil {
			return err
		}
		out(o, snaps, func(w io.Writer) { printSnapshots(w, d.Name, snaps) })
		return nil
	case "create":
		if len(rest) > 1 {
			return errors.New(use)
		}
		name := ""
		if len(rest) == 1 {
			name = rest[0]
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.CreateSnapshot(ctx, s, uuid, name, o.flags["description"])
		})
	case "revert":
		if len(rest) != 1 {
			return errors.New(use)
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.RevertSnapshot(ctx, s, uuid, rest[0])
		})
	case "rm", "delete":
		if len(rest) != 1 {
			return errors.New(use)
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.DeleteSnapshot(ctx, s, uuid, rest[0], o.flags["children"] != "")
		})
	}
	return errors.New(use)
}

// printSnapshots draws the snapshot tree, the current one marked.
func printSnapshots(w io.Writer, machine string, snaps []hv.Snapshot) {
	if len(snaps) == 0 {
		fmt.Fprintf(w, "%s has no snapshots\n", machine)
		return
	}
	for _, s := range snaps {
		mark := "  "
		if s.Current {
			mark = "* "
		}
		what := s.State
		if s.Memory {
			what += ", with memory"
		}
		line := fmt.Sprintf("%s%s%s  %s  (%s)", mark, strings.Repeat("  ", s.Depth), s.Name, s.Created.Local().Format("2006-01-02 15:04"), what)
		if s.Description != "" {
			line += "  " + s.Description
		}
		fmt.Fprintln(w, line)
	}
}
