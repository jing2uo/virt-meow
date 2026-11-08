package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// cmdEvents is virsh event --all --loop for lifecycle events: a line per
// event until interrupted, of every machine or of the one named (by name
// or UUID, so a machine defined meanwhile is followed too).
func cmdEvents(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: virt-mm events [<machine>]")
	}
	id, err := connID(o, m)
	if err != nil {
		return err
	}
	var mu sync.Mutex // events come from the connection's goroutine
	enc := json.NewEncoder(os.Stdout)
	print := func(e hv.Event) {
		if len(args) == 1 && e.Name != args[0] && e.UUID != args[0] {
			return
		}
		now := time.Now()
		mu.Lock()
		defer mu.Unlock()
		if o.json {
			enc.Encode(struct {
				Time time.Time `json:"time"`
				hv.Event
			}{now, e})
			return
		}
		what := string(e.Kind)
		if e.Detail != "" {
			what += " (" + e.Detail + ")"
		}
		fmt.Printf("%s  %s  %s\n", now.Format("2006-01-02 15:04:05"), e.Name, what)
	}
	c, cancel := callCtx(ctx)
	stop, err := m.Events(c, id, print)
	cancel()
	if err != nil {
		return hint(err)
	}
	defer stop()
	// On stderr even with --json: a script can wait for it before it acts.
	fmt.Fprintln(os.Stderr, "following events; Ctrl+C stops")
	<-ctx.Done()
	return nil
}
