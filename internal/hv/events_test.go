package hv

import (
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
)

func TestEventOf(t *testing.T) {
	for _, c := range []struct {
		ev     libvirt.DomainEventType
		detail int32
		kind   EventKind
		want   string
	}{
		{libvirt.DomainEventStarted, int32(libvirt.DomainEventStartedRestored), EvStarted, "restored"},
		{libvirt.DomainEventStopped, int32(libvirt.DomainEventStoppedSaved), EvStopped, "saved"},
		{libvirt.DomainEventStopped, int32(libvirt.DomainEventStoppedRecreated), EvStopped, "recreated"},
		{libvirt.DomainEventSuspended, int32(libvirt.DomainEventSuspendedPostcopyFailed), EvPaused, "postcopy-failed"},
		{libvirt.DomainEventCrashed, int32(libvirt.DomainEventCrashedCrashloaded), EvCrashed, "crashloaded"},
		{libvirt.DomainEventStopped, 99, EvStopped, ""},
		{libvirt.DomainEventType(42), 0, EvOther, ""},
	} {
		e := EventOf(libvirt.DomainEventLifecycleMsg{Dom: libvirt.Domain{Name: "m"}, Event: int32(c.ev), Detail: c.detail})
		if e.Kind != c.kind || e.Detail != c.want || e.Name != "m" {
			t.Errorf("%d/%d: %+v, want %s %q", c.ev, c.detail, e, c.kind, c.want)
		}
	}
}
