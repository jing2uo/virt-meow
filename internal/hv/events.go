package hv

import (
	libvirt "github.com/digitalocean/go-libvirt"
)

// Event is a change in a machine's life.
type Event struct {
	UUID string    `json:"uuid"`
	Name string    `json:"name"`
	Kind EventKind `json:"kind"`
	// Detail is why, as libvirt says: booted or restored for started,
	// destroyed or saved for stopped, … ("" when libvirt's is unknown).
	Detail string `json:"detail,omitempty"`
}

// eventDetails names libvirt's details of each kind of event, by number.
var eventDetails = map[libvirt.DomainEventType][]string{
	libvirt.DomainEventDefined:     {"added", "updated", "renamed", "from-snapshot"},
	libvirt.DomainEventUndefined:   {"removed", "renamed"},
	libvirt.DomainEventStarted:     {"booted", "migrated", "restored", "from-snapshot", "wakeup", "recreated"},
	libvirt.DomainEventSuspended:   {"paused", "migrated", "io-error", "watchdog", "restored", "from-snapshot", "api-error", "postcopy", "postcopy-failed"},
	libvirt.DomainEventResumed:     {"unpaused", "migrated", "from-snapshot", "postcopy", "postcopy-failed"},
	libvirt.DomainEventStopped:     {"shutdown", "destroyed", "crashed", "migrated", "saved", "failed", "from-snapshot", "recreated"},
	libvirt.DomainEventShutdown:    {"finished", "guest", "host"},
	libvirt.DomainEventPmsuspended: {"memory", "disk"},
	libvirt.DomainEventCrashed:     {"panicked", "crashloaded"},
}

// EventKind is what happened.
type EventKind string

const (
	EvDefined     EventKind = "defined"
	EvUndefined   EventKind = "undefined"
	EvStarted     EventKind = "started"
	EvPaused      EventKind = "paused"
	EvResumed     EventKind = "resumed"
	EvStopped     EventKind = "stopped"
	EvShutdown    EventKind = "shutdown" // the guest is shutting down
	EvPMSuspended EventKind = "pm-suspended"
	EvCrashed     EventKind = "crashed"
	EvOther       EventKind = "other"
)

// EventOf converts a libvirt lifecycle event.
func EventOf(m libvirt.DomainEventLifecycleMsg) Event {
	k := EvOther
	switch libvirt.DomainEventType(m.Event) {
	case libvirt.DomainEventDefined:
		k = EvDefined
	case libvirt.DomainEventUndefined:
		k = EvUndefined
	case libvirt.DomainEventStarted:
		k = EvStarted
	case libvirt.DomainEventSuspended:
		k = EvPaused
	case libvirt.DomainEventResumed:
		k = EvResumed
	case libvirt.DomainEventStopped:
		k = EvStopped
	case libvirt.DomainEventShutdown:
		k = EvShutdown
	case libvirt.DomainEventPmsuspended:
		k = EvPMSuspended
	case libvirt.DomainEventCrashed:
		k = EvCrashed
	}
	ev := Event{UUID: FormatUUID(m.Dom.UUID), Name: m.Dom.Name, Kind: k}
	if ds := eventDetails[libvirt.DomainEventType(m.Event)]; m.Detail >= 0 && int(m.Detail) < len(ds) {
		ev.Detail = ds[m.Detail]
	}
	return ev
}
