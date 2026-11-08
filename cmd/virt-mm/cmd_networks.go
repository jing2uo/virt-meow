package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/hv"
)

func cmdNet(ctx context.Context, o opts, m *app.Manager, args []string) error {
	const use = `usage: virt-mm net add <name> --type nat|route|isolated [--address 192.168.100.1/24]
                [--dhcp on|off|<first>-<last>] [--bridge virbrN] [--dev <host interface>] [--autostart on|off]
       virt-mm net add <name> --type bridge --bridge <host bridge> [--autostart on|off]
       virt-mm net start <name>;  virt-mm net stop <name> [--force]
       virt-mm net autostart <name> on|off
       virt-mm net rm <name> [--force]
       virt-mm net leases <name>
       virt-mm net host ls <net>;  virt-mm net host add <net> <mac> <ip> [<hostname>];  virt-mm net host rm <net> <mac>|<ip>
       virt-mm net xml <net> [--set <file|->];  virt-mm net edit <net>`
	if len(args) < 2 {
		return errors.New(use)
	}
	sub, name, rest := args[0], args[1], args[2:]
	switch sub {
	case "host":
		return cmdNetHost(ctx, o, m, args[1:], use)
	case "xml", "dumpxml":
		if len(rest) != 0 {
			return errors.New(use)
		}
		return cmdNetXML(ctx, o, m, name)
	case "edit":
		if len(rest) != 0 {
			return errors.New(use)
		}
		return cmdNetEdit(ctx, o, m, name)
	}
	if len(rest) != 0 && sub != "autostart" {
		return errors.New(use)
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	force := o.flags["force"] != ""
	show := func(n hv.Network, what string) {
		out(o, n, func(w io.Writer) {
			kind := n.Forward
			if kind == "" {
				kind = "isolated"
			}
			fmt.Fprintf(w, "network %s %s: %s on %s", n.Name, what, kind, n.Bridge)
			if n.Address != "" {
				fmt.Fprintf(w, ", %s", n.Address)
			}
			if n.DHCP != "" {
				fmt.Fprintf(w, ", DHCP %s", n.DHCP)
			}
			fmt.Fprintf(w, ", %s, autostart %s\n",
				map[bool]string{true: "active", false: "stopped"}[n.Active], map[bool]string{true: "on", false: "off"}[n.Autostart])
		})
	}
	var a hv.NetworkAction
	switch sub {
	case "add":
		nn := hv.NewNetwork{Name: name, Type: o.flags["type"], Address: o.flags["address"], Bridge: o.flags["bridge"], Dev: o.flags["dev"]}
		switch d := o.flags["dhcp"]; d {
		case "", "on", "yes", "true", "1":
		case "off", "no", "false", "0":
			nn.NoDHCP = true
		default:
			nn.DHCP = d
		}
		if v := o.flags["autostart"]; v != "" {
			if nn.Autostart, err = parseOnOff(v); err != nil {
				return fmt.Errorf("--autostart: %w", err)
			}
		}
		n, err := hv.AddNetwork(ctx, s, nn)
		if err != nil {
			return hint(err)
		}
		show(n, "added")
		return nil
	case "rm", "remove":
		if err := hv.RemoveNetwork(ctx, s, name, force); err != nil {
			return hint(err)
		}
		out(o, map[string]string{"removed": name}, func(w io.Writer) { fmt.Fprintf(w, "network %s removed\n", name) })
		return nil
	case "leases":
		ls, err := hv.Leases(ctx, s, name)
		if err != nil {
			return hint(err)
		}
		out(o, ls, func(w io.Writer) {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "IP\tMAC\tMACHINE\tHOSTNAME\tEXPIRES")
			for _, l := range ls {
				exp := "never"
				if l.Expires != nil {
					exp = l.Expires.Local().Format(time.DateTime)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", l.IP, l.MAC, or(l.Machine, "-"), or(l.Hostname, "-"), exp)
			}
			tw.Flush()
		})
		return nil
	case "start", "stop":
		a = hv.NetworkAction(sub)
	case "autostart":
		if len(rest) != 1 {
			return errors.New(use)
		}
		on, err := parseOnOff(rest[0])
		if err != nil {
			return err
		}
		a = map[bool]hv.NetworkAction{true: hv.NetworkAutostartOn, false: hv.NetworkAutostartOff}[on]
	default:
		return errors.New(use)
	}
	n, err := hv.SetNetwork(ctx, s, name, a, force)
	if err != nil {
		return hint(err)
	}
	show(n, "now")
	return nil
}

func cmdNetHost(ctx context.Context, o opts, m *app.Manager, args []string, use string) error {
	if len(args) < 2 {
		return errors.New(use)
	}
	sub, network, rest := args[0], args[1], args[2:]
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	var e hv.Edited
	switch {
	case (sub == "ls" || sub == "list") && len(rest) == 0:
		hs, err := hv.DHCPHosts(ctx, s, network)
		if err != nil {
			return hint(err)
		}
		out(o, hs, func(w io.Writer) {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "MAC\tIP\tHOSTNAME\tWHERE")
			for _, h := range hs {
				where := "defined and live"
				switch {
				case !h.Live:
					where = "defined (from the network's next start)"
				case !h.Defined:
					where = "live only (gone when the network restarts)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.MAC, h.IP, or(h.Name, "-"), where)
			}
			tw.Flush()
		})
		return nil
	case sub == "add" && (len(rest) == 2 || len(rest) == 3):
		name := ""
		if len(rest) == 3 {
			name = rest[2]
		}
		e, err = hv.AddDHCPHost(ctx, s, network, rest[0], rest[1], name)
	case (sub == "rm" || sub == "remove") && len(rest) == 1:
		e, err = hv.RemoveDHCPHost(ctx, s, network, rest[0])
	default:
		return errors.New(use)
	}
	if err != nil {
		return hint(err)
	}
	out(o, e, func(w io.Writer) {
		for _, c := range e.Changes {
			when := "in the definition; the network takes it when it starts"
			if c.Live {
				when = "now, and in the definition"
			}
			fmt.Fprintf(w, "network %s: %s, %s\n", e.Name, c.What, when)
		}
	})
	return nil
}

func netNextStart(d hv.NetworkDefinition) string {
	if d.Active {
		return " (it is running: the change takes effect when it restarts, virt-mm net stop and start)"
	}
	return ""
}

func cmdNetXML(ctx context.Context, o opts, m *app.Manager, name string) error {
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	if v := o.flags["set"]; v != "" {
		b, err := readFile(v)
		if err != nil {
			return fmt.Errorf("--set: %w", err)
		}
		def, err := hv.RedefineNetwork(ctx, s, name, "", string(b))
		if err != nil {
			return hint(err)
		}
		out(o, def, func(w io.Writer) { fmt.Fprintf(w, "saved network %s%s\n", def.Name, netNextStart(def)) })
		return nil
	}
	def, err := hv.GetNetworkDefinition(ctx, s, name)
	if err != nil {
		return hint(err)
	}
	out(o, def, func(w io.Writer) { fmt.Fprint(w, def.XML) })
	return nil
}

func cmdNetEdit(ctx context.Context, o opts, m *app.Manager, name string) error {
	s, err := session(o, m)
	if err != nil {
		return err
	}
	c, cancel := callCtx(ctx)
	def, err := hv.GetNetworkDefinition(c, s, name)
	cancel()
	if err != nil {
		return hint(err)
	}
	return editLoop(ctx, "net-"+def.Name, def.XML, func(ctx context.Context, x string) error {
		saved, err := hv.RedefineNetwork(ctx, s, name, def.XML, x)
		if err == nil {
			out(o, saved, func(w io.Writer) { fmt.Fprintf(w, "saved network %s%s\n", saved.Name, netNextStart(saved)) })
		}
		return err
	})
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
