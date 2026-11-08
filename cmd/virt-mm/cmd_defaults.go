package main

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/jing2uo/virt-meow/internal/store"
)

// cmdDefaults shows or sets what new machines get (the settings the
// page's settings dialog shares): virt-mm defaults [set --flag value…].
func cmdDefaults(o opts, args []string) error {
	st, err := store.LoadSettings()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "set":
		n := &st.NewMachine
		f := o.flags
		set := func(flag string, to *string) {
			if v, ok := f[flag]; ok {
				*to = v
			}
		}
		set("listen", &n.Listen)
		set("cpu-mode", &n.CPUMode)
		set("disk-format", &n.DiskFormat)
		set("disk-bus", &n.DiskBus)
		set("nic-model", &n.NICModel)
		set("pool", &n.Pool)
		set("net", &n.Network)
		if v, ok := f["sound"]; ok {
			if n.Sound, err = parseOnOff(v); err != nil {
				return fmt.Errorf("--sound: %w", err)
			}
		}
		if err := store.SaveSettings(st); err != nil {
			return err
		}
	default:
		return errors.New("usage: virt-mm defaults [set [--listen A] [--cpu-mode M] [--disk-format F] [--disk-bus B] [--nic-model N] [--sound on|off] [--pool P] [--net K:N|none]]")
	}
	n := st.NewMachine
	out(o, n, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		row := func(k, v, auto string) {
			if v == "" {
				v = auto
			}
			fmt.Fprintf(tw, "%s\t%s\n", k, v)
		}
		row("listen", n.Listen, "127.0.0.1 (virt-meow's)")
		row("cpu-mode", n.CPUMode, "host-passthrough on KVM (virt-meow's)")
		row("disk-format", n.DiskFormat, "qcow2 (virt-meow's)")
		row("disk-bus", n.DiskBus, "by the system (virtio, SATA for Windows)")
		row("nic-model", n.NICModel, "by the system (virtio, e1000e for Windows)")
		row("sound", map[bool]string{true: "on", false: "off"}[n.Sound], "")
		row("pool", n.Pool, "the one with the most room")
		row("net", n.Network, "the host's first bridge, else its first network")
		tw.Flush()
	})
	return nil
}
