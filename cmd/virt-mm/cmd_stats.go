package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// cmdStats shows the rates of the running machines between two samples a
// second apart, or with --watch every 2 s until interrupted.
func cmdStats(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: virt-mm stats [<machine>] [--watch]")
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	uuid := ""
	if len(args) == 1 {
		c, cancel := callCtx(ctx)
		d, err := hv.Lookup(c, s, args[0])
		cancel()
		if err != nil {
			return hint(err)
		}
		uuid = d.UUID
	}
	sample := func() (hv.Sample, error) {
		c, cancel := callCtx(ctx)
		defer cancel()
		return hv.GetSample(c, s, uuid)
	}
	sleep := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	prev, err := sample()
	if err != nil {
		return hint(err)
	}
	watch := o.flags["watch"] != ""
	every := time.Second
	if watch {
		every = 2 * time.Second
	}
	for sleep(every) {
		cur, err := sample()
		if err != nil {
			return err
		}
		st := hv.Rates(prev, cur)
		prev = cur
		switch {
		case !watch:
			out(o, st, func(w io.Writer) { printStats(w, st) })
			return nil
		case o.json:
			b, _ := json.Marshal(st)
			fmt.Printf("%s\n", b)
		default:
			fmt.Print("\033[H\033[2J") // home, clear
			fmt.Printf("%s  every %s, Ctrl+C to stop\n\n", st.Time.Format("15:04:05"), every)
			printStats(os.Stdout, st)
		}
	}
	return nil // interrupted: the end of --watch
}

func printStats(w io.Writer, st hv.Stats) {
	if len(st.Machines) == 0 {
		fmt.Fprintln(w, "no machine is running")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCPU\tMEMORY\tHOST RSS\tDISK READ\tDISK WRITE\tNET RX\tNET TX\t")
	for _, x := range st.Machines {
		cpu, mem := fmt.Sprintf("%.1f%%", x.CPUPercent), kib(x.MemoryKiB)
		if x.New {
			cpu = "-"
		}
		if x.GuestUsedKiB > 0 {
			mem = kib(x.GuestUsedKiB) + " / " + mem
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", x.Name, cpu, mem, kib(x.RSSKiB),
			rate(x.DiskRead), rate(x.DiskWrite), rate(x.NetRx), rate(x.NetTx))
	}
	tw.Flush()
}

// rate writes bytes per second.
func rate(bps float64) string {
	switch {
	case bps >= 1<<30:
		return fmt.Sprintf("%.1f GiB/s", bps/(1<<30))
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MiB/s", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.1f KiB/s", bps/(1<<10))
	}
	return fmt.Sprintf("%.0f B/s", bps)
}
