package hv

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// The clipboard shared with the guest over VNC: QEMU's qemu-vdagent
// chardev, which speaks the SPICE agent's protocol to spice-vdagent in the
// guest on a virtio-serial port and gives QEMU's clipboard to its VNC
// server, which hands it to the console (noVNC) and takes the console's.

// spiceAgentPort is the virtio-serial port spice-vdagent in the guest
// opens; one channel can be on it.
const spiceAgentPort = "com.redhat.spice.0"

// clipboardChannel is the qemu-vdagent channel with the clipboard on, and
// the agent's absolute pointer.
func clipboardChannel() libvirtxml.DomainChannel {
	return libvirtxml.DomainChannel{
		Source: &libvirtxml.DomainChardevSource{QEMUVDAgent: &libvirtxml.DomainChardevSourceQEMUVDAgent{
			ClipBoard: &libvirtxml.DomainChardevSourceQEMUVDAgentClipBoard{CopyPaste: "yes"},
			Mouse:     &libvirtxml.DomainChardevSourceQEMUVDAgentMouse{Mode: "client"},
		}},
		Target: &libvirtxml.DomainChannelTarget{VirtIO: &libvirtxml.DomainChannelTargetVirtIO{Name: spiceAgentPort}},
	}
}

// hasClipboard says whether d has a qemu-vdagent channel sharing the
// clipboard.
func hasClipboard(d *libvirtxml.Domain) bool {
	if d.Devices == nil {
		return false
	}
	return slices.ContainsFunc(d.Devices.Channels, isClipboard)
}

func isClipboard(c libvirtxml.DomainChannel) bool {
	return c.Source != nil && c.Source.QEMUVDAgent != nil && c.Source.QEMUVDAgent.ClipBoard != nil &&
		c.Source.QEMUVDAgent.ClipBoard.CopyPaste == "yes"
}

// onSpicePort is a channel on the SPICE agent's port.
func onSpicePort(c libvirtxml.DomainChannel) bool {
	return c.Target != nil && c.Target.VirtIO != nil && c.Target.VirtIO.Name == spiceAgentPort
}

// clipboardSupported says whether a host of libvirt and QEMU versions lib
// and qemu (as libvirt numbers them: major*1000000+minor*1000+micro) has
// the qemu-vdagent channel: libvirt 8.4 and QEMU 6.1.
func clipboardSupported(lib, qemu uint64) bool {
	return lib >= 8_004_000 && qemu >= 6_001_000
}

// clipboardMigrates says whether QEMU of version qemu can save, snapshot
// with its memory or migrate a running machine with a qemu-vdagent
// channel: before 10.1 the channel blocks migration, which those are.
func clipboardMigrates(qemu uint64) bool { return qemu >= 10_001_000 }

// clipboardBlocks is the warning of a QEMU whose qemu-vdagent blocks
// migration.
func clipboardBlocks(name string) string {
	return fmt.Sprintf("this host's QEMU is older than 10.1, whose qemu-vdagent can't be migrated: while %s runs with the clipboard shared it can't be saved (virt-mm save), snapshotted with its memory, or migrated; virt-mm device rm %[1]s clipboard undoes it", name)
}

// vdagentHint adds what to do to libvirt's refusal of a save or snapshot
// that a qemu-vdagent channel blocked (QEMU before 10.1).
func vdagentHint(err error, name string) error {
	if err != nil && strings.Contains(err.Error(), "vdagent chardev doesn't yet support migration") {
		return fmt.Errorf("%w (the clipboard shared with the console blocks it on this host's QEMU, older than 10.1: virt-mm device rm %s clipboard, then try again)", err, name)
	}
	return err
}

// AddClipboard gives the machine the clipboard shared over VNC: a
// qemu-vdagent channel, which spice-vdagent in the guest uses. A running
// machine gets it now.
func AddClipboard(ctx context.Context, sess *conn.Session, uuid string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		dev := e.devices()
		if hasClipboard(e.def) {
			return fmt.Errorf("%s already shares its clipboard", e.def.Name)
		}
		if i := slices.IndexFunc(dev.Channels, onSpicePort); i >= 0 {
			what := "another channel"
			if s := dev.Channels[i].Source; s != nil && s.SpiceVMC != nil {
				what = "SPICE's agent channel (spicevmc), whose clipboard a SPICE client shares,"
			}
			return fmt.Errorf("%s has %s on %s already: remove it first (virt-mm edit) to share the clipboard over VNC instead", e.def.Name, what, spiceAgentPort)
		}
		lib, err := e.lv.ConnectGetLibVersion()
		if err != nil {
			return err
		}
		qemu, err := e.lv.ConnectGetVersion()
		if err != nil {
			return err
		}
		if !clipboardSupported(lib, qemu) {
			return fmt.Errorf("sharing the clipboard needs libvirt 8.4 and QEMU 6.1 on the host (it has libvirt %s, QEMU %s)", version(lib), version(qemu))
		}
		if !clipboardMigrates(qemu) {
			e.out.Warnings = append(e.out.Warnings, clipboardBlocks(e.def.Name))
		}
		ch := clipboardChannel()
		return e.attach("clipboard shared with the console (qemu-vdagent; spice-vdagent in the guest)", &ch)
	})
}

// RemoveClipboard removes the machine's qemu-vdagent clipboard channel; a
// running machine lets go of it now.
func RemoveClipboard(ctx context.Context, sess *conn.Session, uuid string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		for _, c := range e.devices().Channels {
			if isClipboard(c) {
				return e.detach("clipboard sharing", &c)
			}
		}
		return fmt.Errorf("%s doesn't share its clipboard", e.def.Name)
	})
}
