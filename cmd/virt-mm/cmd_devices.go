package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// machineOn resolves the machine ref on the connection -c picks.
func machineOn(ctx context.Context, o opts, m *app.Manager, ref string) (*conn.Session, hv.Domain, error) {
	s, err := session(o, m)
	if err != nil {
		return nil, hv.Domain{}, err
	}
	d, err := hv.Lookup(ctx, s, ref)
	if err != nil {
		return nil, hv.Domain{}, hint(err)
	}
	return s, d, nil
}

// edited runs an edit on the machine args[0] names and prints its outcome.
func edited(ctx context.Context, o opts, m *app.Manager, ref string, f func(context.Context, *conn.Session, string) (hv.Edited, error)) error {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	s, d, err := machineOn(ctx, o, m, ref)
	if err != nil {
		return err
	}
	e, err := f(ctx, s, d.UUID)
	if err != nil {
		return err
	}
	out(o, e, func(w io.Writer) { printEdited(w, e) })
	return nil
}

func bootText(n uint) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprint(n)
}

func cmdDisk(ctx context.Context, o opts, m *app.Manager, args []string) error {
	const use = `usage: virt-mm disk ls <machine>
       virt-mm disk add <machine> --new <pool>:<size>G | --vol <pool>/<volume> | --path <file> --format raw|qcow2 [--bus virtio|sata|scsi|usb]
       virt-mm disk rm <machine> <target> [--delete-volume]
       virt-mm disk resize <machine> <target> <size>|+<size>`
	if len(args) < 2 {
		return errors.New(use)
	}
	sub, ref, rest := args[0], args[1], args[2:]
	f := o.flags
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
		dev, err := hv.GetDevices(ctx, s, d.UUID)
		if err != nil {
			return err
		}
		out(o, dev.Disks, func(w io.Writer) {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "TARGET\tDEVICE\tBUS\tSIZE\tFORMAT\tBOOT\tSOURCE")
			for _, k := range dev.Disks {
				src := k.Source
				if src == "" {
					src = "(empty)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.Target, k.Device, k.Bus, kib(k.Capacity>>10), k.Format, bootText(k.Boot), src)
			}
			tw.Flush()
		})
		return nil
	case "add":
		if len(rest) != 0 {
			return errors.New(use)
		}
		nd := hv.NewDisk{Path: f["path"], Format: f["format"], Bus: f["bus"]}
		if v := f["new"]; v != "" {
			pool, size, ok := strings.Cut(v, ":")
			mib, err := parseSize(size, "G")
			if !ok || err != nil {
				return fmt.Errorf("--new %q: want <pool>:<size>G", v)
			}
			nd.Pool, nd.SizeGiB = pool, int(mib>>10)
		}
		if v := f["vol"]; v != "" {
			if nd.VolPool, nd.Volume, _ = strings.Cut(v, "/"); nd.Volume == "" {
				return fmt.Errorf("--vol %q: want <pool>/<volume>", v)
			}
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.AddDisk(ctx, s, uuid, nd)
		})
	case "rm", "remove":
		if len(rest) != 1 {
			return errors.New(use)
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.RemoveDisk(ctx, s, uuid, rest[0], f["delete-volume"] != "")
		})
	case "resize":
		if len(rest) != 2 {
			return errors.New(use)
		}
		target, size := rest[0], rest[1]
		grow := strings.HasPrefix(size, "+")
		mib, err := parseSize(strings.TrimPrefix(size, "+"), "G")
		if err != nil {
			return fmt.Errorf("size: %w", err)
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			bytes := uint64(mib) << 20
			if grow {
				dev, err := hv.GetDevices(ctx, s, uuid)
				if err != nil {
					return hv.Edited{}, err
				}
				i := indexOf(dev.Disks, func(k hv.Disk) bool { return k.Target == target })
				if i < 0 || dev.Disks[i].Capacity == 0 {
					return hv.Edited{}, fmt.Errorf("no size known for %s: give the new size, not +", target)
				}
				bytes += dev.Disks[i].Capacity
			}
			return hv.ResizeDisk(ctx, s, uuid, target, bytes)
		})
	}
	return errors.New(use)
}

func indexOf[T any](s []T, f func(T) bool) int {
	for i, v := range s {
		if f(v) {
			return i
		}
	}
	return -1
}

func cmdCDROM(ctx context.Context, o opts, m *app.Manager, args []string) error {
	const use = `usage: virt-mm cdrom insert <machine> <pool>/<volume> [--target sdb]
       virt-mm cdrom eject <machine> [--target sdb]`
	if len(args) < 2 {
		return errors.New(use)
	}
	sub, ref, rest := args[0], args[1], args[2:]
	target := o.flags["target"]
	switch sub {
	case "insert":
		if len(rest) != 1 {
			return errors.New(use)
		}
		pool, vol, _ := strings.Cut(rest[0], "/")
		if vol == "" {
			return fmt.Errorf("%q: want <pool>/<volume>", rest[0])
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.InsertCD(ctx, s, uuid, target, pool, vol)
		})
	case "eject":
		if len(rest) != 0 {
			return errors.New(use)
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.EjectCD(ctx, s, uuid, target)
		})
	}
	return errors.New(use)
}

func cmdNIC(ctx context.Context, o opts, m *app.Manager, args []string) error {
	const use = `usage: virt-mm nic ls <machine>
       virt-mm nic add <machine> --net bridge:<br>|network:<name>|user [--model virtio|e1000e|…] [--mac 52:54:00:…]
       virt-mm nic rm <machine> <mac>
       virt-mm nic link <machine> <mac> up|down`
	if len(args) < 2 {
		return errors.New(use)
	}
	sub, ref, rest := args[0], args[1], args[2:]
	f := o.flags
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
		dev, err := hv.GetDevices(ctx, s, d.UUID)
		if err != nil {
			return err
		}
		out(o, dev.NICs, func(w io.Writer) {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "MAC\tKIND\tSOURCE\tMODEL\tLINK\tBOOT")
			for _, n := range dev.NICs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", n.MAC, n.Kind, n.Source, n.Model, n.Link, bootText(n.Boot))
			}
			tw.Flush()
		})
		return nil
	case "add":
		if len(rest) != 0 || f["net"] == "" {
			return errors.New(use)
		}
		kind, src, _ := strings.Cut(f["net"], ":")
		n := hv.NewNIC{Kind: kind, Source: src, Model: f["model"], MAC: f["mac"]}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.AddNIC(ctx, s, uuid, n)
		})
	case "rm", "remove":
		if len(rest) != 1 {
			return errors.New(use)
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.RemoveNIC(ctx, s, uuid, rest[0])
		})
	case "link":
		if len(rest) != 2 || (rest[1] != "up" && rest[1] != "down") {
			return errors.New(use)
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.SetLink(ctx, s, uuid, rest[0], rest[1] == "up")
		})
	}
	return errors.New(use)
}

func cmdBoot(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: virt-mm boot <machine> [<target>|<mac> …] [--menu on|off]")
	}
	ref, devs := args[0], args[1:]
	var menu *bool
	if v := o.flags["menu"]; v != "" {
		on, err := parseOnOff(v)
		if err != nil {
			return fmt.Errorf("--menu: %w", err)
		}
		menu = &on
	}
	if len(devs) > 0 || menu != nil {
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.SetBoot(ctx, s, uuid, devs, menu)
		})
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	s, d, err := machineOn(ctx, o, m, ref)
	if err != nil {
		return err
	}
	dev, err := hv.GetDevices(ctx, s, d.UUID)
	if err != nil {
		return err
	}
	type entry struct {
		Order  uint   `json:"order"`
		Device string `json:"device"`
		What   string `json:"what"`
	}
	order := []entry{}
	for _, k := range dev.Disks {
		if k.Boot > 0 {
			order = append(order, entry{k.Boot, k.Target, k.Device + " " + k.Source})
		}
	}
	for _, n := range dev.NICs {
		if n.Boot > 0 {
			order = append(order, entry{n.Boot, n.MAC, strings.TrimSpace("network " + n.Kind + " " + n.Source)})
		}
	}
	for i := range order {
		for j := i + 1; j < len(order); j++ {
			if order[j].Order < order[i].Order {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	res := map[string]any{"order": order, "menu": dev.BootMenu, "boot_devs": dev.BootDevs}
	out(o, res, func(w io.Writer) {
		switch {
		case len(dev.BootDevs) > 0:
			fmt.Fprintf(w, "by device type: %s\n", strings.Join(dev.BootDevs, ", "))
		case len(order) == 0:
			fmt.Fprintln(w, "no boot order: the firmware decides")
		}
		for _, e := range order {
			fmt.Fprintf(w, "%d  %s  %s\n", e.Order, e.Device, e.What)
		}
		fmt.Fprintf(w, "boot menu: %s\n", map[bool]string{true: "on", false: "off"}[dev.BootMenu])
	})
	return nil
}

func cmdDevice(ctx context.Context, o opts, m *app.Manager, args []string) error {
	const use = `usage: virt-mm device ls <machine>
       virt-mm device set <machine> video virtio|vga|qxl|bochs|ramfb|none
       virt-mm device set <machine> sound ich9|ich6|ich7|ac97|usb|virtio|none
       virt-mm device add|set <machine> graphics vnc|spice [--listen 127.0.0.1]
       virt-mm device set <machine> graphics vnc|spice --password <pw>|-|""
       virt-mm device rm <machine> graphics vnc|spice
       virt-mm device add <machine> tpm [--model crb|tis] | rng | clipboard
       virt-mm device rm <machine> tpm | rng | clipboard
       virt-mm device add <machine> hostdev <device>
       virt-mm device rm <machine> hostdev <address|vendor:product|device>
       virt-mm device attach|detach <machine> <file|->`
	if len(args) < 2 {
		return errors.New(use)
	}
	sub, ref, rest := args[0], args[1], args[2:]
	f := o.flags
	if sub == "attach" || sub == "detach" {
		if len(rest) != 1 {
			return errors.New(use)
		}
		b, err := readFile(rest[0])
		if err != nil {
			return err
		}
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			if sub == "attach" {
				return hv.AttachDevice(ctx, s, uuid, string(b))
			}
			return hv.DetachDevice(ctx, s, uuid, string(b))
		})
	}
	if sub == "ls" || sub == "list" {
		if len(rest) != 0 {
			return errors.New(use)
		}
		ctx, cancel := callCtx(ctx)
		defer cancel()
		s, d, err := machineOn(ctx, o, m, ref)
		if err != nil {
			return err
		}
		dev, err := hv.GetDevices(ctx, s, d.UUID)
		if err != nil {
			return err
		}
		type devs struct {
			Video     []hv.Video              `json:"video"`
			Graphics  []hv.Graphics           `json:"graphics"`
			TPMs      []hv.TPM                `json:"tpm"`
			Sound     []string                `json:"sound"`
			RNGs      []hv.RNG                `json:"rng"`
			HostDevs  []hv.AttachedHostDevice `json:"hostdevs"`
			Clipboard bool                    `json:"clipboard"`
		}
		out(o, devs{dev.Video, dev.Graphics, dev.TPMs, dev.Sound, dev.RNGs, dev.HostDevs, dev.Clipboard}, func(w io.Writer) { printPeripherals(w, dev) })
		return nil
	}
	if len(rest) < 1 {
		return errors.New(use)
	}
	kind, rest := rest[0], rest[1:]
	arg := func() (string, error) {
		if len(rest) != 1 {
			return "", errors.New(use)
		}
		return rest[0], nil
	}
	none := func() error {
		if len(rest) != 0 {
			return errors.New(use)
		}
		return nil
	}
	var do func(context.Context, *conn.Session, string) (hv.Edited, error)
	switch sub + " " + kind {
	case "set video", "set sound":
		model, err := arg()
		if err != nil {
			return err
		}
		do = func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			if kind == "video" {
				return hv.SetVideo(ctx, s, uuid, model)
			}
			return hv.SetSound(ctx, s, uuid, model)
		}
	case "add graphics", "set graphics", "rm graphics", "remove graphics":
		g, err := arg()
		if err != nil {
			return err
		}
		// --password - asks for it (or reads it from stdin with
		// --password-stdin), so that it isn't on the command line.
		if f["password"] == "-" {
			if f["password"], err = newPassword(o, "the "+strings.ToUpper(g)+" console"); err != nil {
				return err
			}
		}
		do = func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			switch sub {
			case "add":
				return hv.AddGraphics(ctx, s, uuid, g, f["listen"])
			case "set":
				if pw, ok := f["password"]; ok {
					if f["listen"] != "" {
						return hv.Edited{}, errors.New("set graphics: --listen or --password, one at a time")
					}
					return hv.SetGraphicsPassword(ctx, s, uuid, g, pw)
				}
				if f["listen"] == "" {
					return hv.Edited{}, errors.New("set graphics: --listen <address> or --password <pw>|-|\"\"")
				}
				return hv.SetGraphicsListen(ctx, s, uuid, g, f["listen"])
			}
			return hv.RemoveGraphics(ctx, s, uuid, g)
		}
	case "add hostdev", "rm hostdev", "remove hostdev":
		dev, err := arg()
		if err != nil {
			return err
		}
		do = func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			if sub == "add" {
				return hv.AttachHostDevice(ctx, s, uuid, dev)
			}
			return hv.DetachHostDevice(ctx, s, uuid, dev)
		}
	case "add tpm", "rm tpm", "remove tpm":
		if err := none(); err != nil {
			return err
		}
		do = func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			if sub == "add" {
				return hv.AddTPM(ctx, s, uuid, f["model"])
			}
			return hv.RemoveTPM(ctx, s, uuid)
		}
	case "add clipboard", "rm clipboard", "remove clipboard":
		if err := none(); err != nil {
			return err
		}
		do = func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			if sub == "add" {
				return hv.AddClipboard(ctx, s, uuid)
			}
			return hv.RemoveClipboard(ctx, s, uuid)
		}
	case "add rng", "rm rng", "remove rng":
		if err := none(); err != nil {
			return err
		}
		do = func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			if sub == "add" {
				return hv.AddRNG(ctx, s, uuid)
			}
			return hv.RemoveRNG(ctx, s, uuid)
		}
	default:
		return errors.New(use)
	}
	return edited(ctx, o, m, ref, do)
}

// printPeripherals lists a machine's video, graphics, TPM, sound and RNG.
func printPeripherals(w io.Writer, dev hv.Devices) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	peripheralRows(dev, func(k, v string) { fmt.Fprintf(tw, "%s\t%s\n", k, v) })
	tw.Flush()
}

func peripheralRows(dev hv.Devices, line func(kind, what string)) {
	if len(dev.Video) == 0 {
		line("video", "none")
	}
	for _, v := range dev.Video {
		line("video", v.Model)
	}
	if len(dev.Graphics) == 0 {
		line("graphics", "none")
	}
	for _, g := range dev.Graphics {
		what := g.Type
		if g.Listen != "" {
			what += " on " + g.Listen
		}
		if g.Port > 0 {
			what += fmt.Sprintf(":%d", g.Port)
		}
		line("graphics", what)
	}
	if len(dev.TPMs) == 0 {
		line("tpm", "none")
	}
	for _, t := range dev.TPMs {
		line("tpm", strings.TrimSpace(fmt.Sprintf("%s, %s %s", t.Model, t.Backend, t.Version)))
	}
	if len(dev.Sound) == 0 {
		line("sound", "none")
	}
	for _, s := range dev.Sound {
		line("sound", s)
	}
	if len(dev.RNGs) == 0 {
		line("rng", "none")
	}
	for _, r := range dev.RNGs {
		line("rng", r.Model+", "+r.Backend)
	}
	if dev.Clipboard {
		line("clipboard", "shared with the console (qemu-vdagent; spice-vdagent in the guest)")
	} else {
		line("clipboard", "none")
	}
	for _, h := range dev.HostDevs {
		line("hostdev", h.Kind+" "+h.Ref)
	}
}

func cmdInfo(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: virt-mm info <machine>")
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	s, d, err := machineOn(ctx, o, m, args[0])
	if err != nil {
		return err
	}
	in, err := hv.GetInfo(ctx, s, d.UUID)
	if err != nil {
		return err
	}
	out(o, in, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		row := func(k, v string) { fmt.Fprintf(tw, "%s\t%s\n", k, v) }
		row("name", in.Name)
		if in.Title != "" {
			row("title", in.Title)
		}
		row("uuid", in.UUID)
		state := stateText(in.Domain)
		if in.Saved {
			state += ": the next start resumes it"
		}
		if in.ID >= 0 {
			state += fmt.Sprintf(" (id %d)", in.ID)
		}
		row("state", state)
		row("vcpus", fmt.Sprint(in.VCPUs))
		mem := kib(in.MemoryKiB)
		if in.MaxMemKiB != in.MemoryKiB {
			mem += " (up to " + kib(in.MaxMemKiB) + ")"
		}
		row("memory", mem)
		if c := in.Config; c.Sockets > 0 {
			row("topology", fmt.Sprintf("sockets=%d cores=%d threads=%d", c.Sockets, c.Cores, c.Threads))
		}
		if c := in.Config; in.State.Active() && (c.VCPUs != in.VCPUs || c.MemoryKiB != in.MemoryKiB || c.MaxMemKiB != in.MaxMemKiB) {
			row("next start", fmt.Sprintf("%d vcpus, memory %s (up to %s)", c.VCPUs, kib(c.MemoryKiB), kib(c.MaxMemKiB)))
		}
		row("cpu", in.CPU)
		row("machine", strings.Join(slices.DeleteFunc([]string{in.VirtType, in.Arch, in.Machine, in.OSType}, func(s string) bool { return s == "" }), " "))
		row("firmware", in.Firmware)
		row("autostart", map[bool]string{true: "on", false: "off"}[in.Autostart])
		if !in.Persistent {
			row("persistent", "no (transient: gone when it stops)")
		}
		for _, k := range in.Devices.Disks {
			src := k.Source
			if src == "" {
				src = "(empty)"
			}
			size := ""
			if k.Capacity > 0 {
				size = ", " + kib(k.Capacity>>10)
			}
			row("disk", fmt.Sprintf("%s %s on %s%s: %s", k.Target, k.Device, k.Bus, size, src))
		}
		for _, n := range in.Devices.NICs {
			row("nic", strings.TrimSpace(fmt.Sprintf("%s %s %s %s, link %s", n.MAC, n.Model, n.Kind, n.Source, n.Link)))
		}
		for _, a := range in.Addresses {
			row("address", fmt.Sprintf("%s/%d  %s %s (%s)", a.Addr, a.Prefix, a.Interface, a.MAC, in.AddressSource))
		}
		if in.State.Active() && len(in.Addresses) == 0 {
			row("address", "none known: "+in.AddressNote)
		}
		if g := in.Guest; g != nil {
			if g.Hostname != "" {
				row("hostname", g.Hostname+" (from the guest agent)")
			}
			if g.OS != "" || g.Kernel != "" {
				row("guest os", strings.TrimSuffix(strings.TrimSpace(g.OS+", kernel "+g.Kernel), ", kernel"))
			}
		} else if in.GuestNote != "" {
			row("guest agent", in.GuestNote)
		}
		peripheralRows(in.Devices, row)
		tw.Flush()
	})
	return nil
}

// cmdFS lists, adds and removes a machine's shared folders (virtiofs).
func cmdFS(ctx context.Context, o opts, m *app.Manager, args []string) error {
	const use = `usage: virt-mm fs ls <machine>
       virt-mm fs add <machine> <host-dir> <tag>
       virt-mm fs rm <machine> <tag>`
	if len(args) < 2 {
		return errors.New(use)
	}
	sub, ref, rest := args[0], args[1], args[2:]
	switch {
	case (sub == "ls" || sub == "list") && len(rest) == 0:
		ctx, cancel := callCtx(ctx)
		defer cancel()
		s, d, err := machineOn(ctx, o, m, ref)
		if err != nil {
			return err
		}
		dev, err := hv.GetDevices(ctx, s, d.UUID)
		if err != nil {
			return err
		}
		out(o, dev.Filesystems, func(w io.Writer) {
			if len(dev.Filesystems) == 0 {
				fmt.Fprintf(w, "%s has no shared folders\n", d.Name)
				return
			}
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "TAG\tHOST DIRECTORY\tDRIVER")
			for _, f := range dev.Filesystems {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", f.Tag, f.Source, f.Driver)
			}
			tw.Flush()
		})
		return nil
	case sub == "add" && len(rest) == 2:
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.AddFilesystem(ctx, s, uuid, rest[0], rest[1])
		})
	case (sub == "rm" || sub == "remove") && len(rest) == 1:
		return edited(ctx, o, m, ref, func(ctx context.Context, s *conn.Session, uuid string) (hv.Edited, error) {
			return hv.RemoveFilesystem(ctx, s, uuid, rest[0])
		})
	}
	return errors.New(use)
}
