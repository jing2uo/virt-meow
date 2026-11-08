package hv

import (
	"context"
	"fmt"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Action changes a machine's power state.
type Action string

const (
	Start    Action = "start"
	Shutdown Action = "shutdown" // ask the guest (ACPI)
	Reboot   Action = "reboot"   // ask the guest
	ForceOff Action = "force-off"
	Pause    Action = "pause"
	Resume   Action = "resume"
	Reset    Action = "reset" // as the reset button: no word to the guest
)

// Actions lists the Actions a menu shows, in its order. Reset is not among
// them: it loses what the guest hasn't written, and is for the CLI.
var Actions = []Action{Start, Shutdown, Reboot, ForceOff, Pause, Resume}

// ParseAction reads an action's name; "destroy", "stop", "suspend" are
// accepted as virsh users type them.
func ParseAction(s string) (Action, error) {
	switch s {
	case "destroy":
		return ForceOff, nil
	case "stop":
		return Shutdown, nil
	case "suspend":
		return Pause, nil
	}
	for _, a := range append(Actions, Reset) {
		if string(a) == s {
			return a, nil
		}
	}
	return "", fmt.Errorf("no power action %q (want one of %v)", s, append(Actions, Reset))
}

// Allowed reports whether a makes sense for a machine in state st, for the
// front end to enable its buttons.
func (a Action) Allowed(st State) bool {
	switch a {
	case Start:
		return st == ShutOff || st == Crashed
	case Shutdown, Reboot:
		return st == Running || st == Idle
	case ForceOff:
		return st.Active()
	case Reset:
		return st == Running || st == Idle || st == Paused
	case Pause:
		return st == Running || st == Idle
	case Resume:
		return st == Paused
	}
	return false
}

// Power applies a to the machine with the given UUID.
func Power(ctx context.Context, s *conn.Session, uuid string, a Action) error {
	u, err := ParseUUID(uuid)
	if err != nil {
		return err
	}
	return s.Do(ctx, func(lv *libvirt.Libvirt) error {
		d, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		switch a {
		case Start:
			return lv.DomainCreate(d)
		case Shutdown:
			return lv.DomainShutdown(d)
		case Reboot:
			return lv.DomainReboot(d, libvirt.DomainRebootDefault)
		case ForceOff:
			return lv.DomainDestroy(d)
		case Pause:
			return lv.DomainSuspend(d)
		case Resume:
			return lv.DomainResume(d)
		case Reset:
			return lv.DomainReset(d, 0)
		}
		return fmt.Errorf("no power action %q", a)
	})
}
