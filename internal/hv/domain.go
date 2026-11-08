// Package hv is what virt-meow does with a libvirt connection: list machines,
// change their power state, follow their events. Its types are virt-meow's own,
// not go-libvirt's, so that the front end and a later back end don't depend
// on libvirt's RPC structs (docs/adr/0001).
package hv

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Domain is one machine as the list shows it.
type Domain struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	ID         int32  `json:"id"` // -1 when not running
	State      State  `json:"state"`
	VCPUs      int    `json:"vcpus"`
	MemoryKiB  uint64 `json:"memory_kib"`     // what it has now
	MaxMemKiB  uint64 `json:"max_memory_kib"` // what it may grow to
	Persistent bool   `json:"persistent"`     // defined, not only running
	Autostart  bool   `json:"autostart"`
	// Saved: off with its memory saved on the host (virt-mm save); the next
	// start resumes it.
	Saved bool `json:"saved"`
}

// State is a machine's state.
type State string

const (
	Running      State = "running"
	Idle         State = "idle" // running, blocked on a resource
	Paused       State = "paused"
	ShuttingDown State = "shutting-down"
	ShutOff      State = "shutoff"
	Crashed      State = "crashed"
	Suspended    State = "suspended" // guest power management (S3)
	Unknown      State = "unknown"
)

func stateOf(s libvirt.DomainState) State {
	switch s {
	case libvirt.DomainRunning:
		return Running
	case libvirt.DomainBlocked:
		return Idle
	case libvirt.DomainPaused:
		return Paused
	case libvirt.DomainShutdown:
		return ShuttingDown
	case libvirt.DomainShutoff:
		return ShutOff
	case libvirt.DomainCrashed:
		return Crashed
	case libvirt.DomainPmsuspended:
		return Suspended
	}
	return Unknown
}

// Active reports whether the machine is running in some form.
func (s State) Active() bool { return s != ShutOff && s != Crashed && s != Unknown }

// List returns every machine on the connection, sorted by name, in a fixed
// number of calls plus one per machine whose sizes the stats call left out.
func List(ctx context.Context, s *conn.Session) ([]Domain, error) {
	var out []Domain
	err := s.Do(ctx, func(lv *libvirt.Libvirt) error {
		all, _, err := lv.ConnectListAllDomains(1, 0)
		if err != nil {
			return err
		}
		persistent, err := uuidSet(lv, libvirt.ConnectListDomainsPersistent)
		if err != nil {
			return err
		}
		autostart, err := uuidSet(lv, libvirt.ConnectListDomainsAutostart)
		if err != nil {
			return err
		}
		saved, err := uuidSet(lv, libvirt.ConnectListDomainsManagedsave)
		if err != nil {
			return err
		}
		stats := map[libvirt.UUID][]libvirt.TypedParam{}
		recs, err := lv.ConnectGetAllDomainStats(nil,
			uint32(libvirt.DomainStatsState|libvirt.DomainStatsBalloon|libvirt.DomainStatsVCPU), 0)
		if err == nil {
			for _, r := range recs {
				stats[r.Dom.UUID] = r.Params
			}
		}
		out = make([]Domain, len(all))
		var missing []int
		for i, d := range all {
			out[i] = Domain{
				UUID: FormatUUID(d.UUID), Name: d.Name, ID: d.ID, State: Unknown,
				Persistent: persistent[d.UUID], Autostart: autostart[d.UUID], Saved: saved[d.UUID],
			}
			p, ok := stats[d.UUID]
			if !ok {
				missing = append(missing, i)
				continue
			}
			if v, ok := paramUint(p, "state.state"); ok {
				out[i].State = stateOf(libvirt.DomainState(v))
			}
			if v, ok := paramUint(p, "vcpu.current"); ok {
				out[i].VCPUs = int(v)
			}
			out[i].MemoryKiB, _ = paramUint(p, "balloon.current")
			out[i].MaxMemKiB, _ = paramUint(p, "balloon.maximum")
			if out[i].State == Unknown || out[i].VCPUs == 0 || out[i].MaxMemKiB == 0 {
				missing = append(missing, i)
			}
		}
		return fillFromInfo(lv, all, out, missing)
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Domain) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// fillFromInfo asks DomainGetInfo for what the stats call didn't give, a few
// machines at a time: go-libvirt multiplexes calls on one connection.
func fillFromInfo(lv *libvirt.Libvirt, all []libvirt.Domain, out []Domain, idx []int) error {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	errs := make([]error, len(idx))
	for j, i := range idx {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			state, maxMem, mem, vcpus, _, err := lv.DomainGetInfo(all[i])
			if err != nil {
				errs[j] = fmt.Errorf("%s: %w", all[i].Name, err)
				return
			}
			out[i].State = stateOf(libvirt.DomainState(state))
			out[i].VCPUs = int(vcpus)
			out[i].MemoryKiB, out[i].MaxMemKiB = mem, maxMem
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func uuidSet(lv *libvirt.Libvirt, flags libvirt.ConnectListAllDomainsFlags) (map[libvirt.UUID]bool, error) {
	doms, _, err := lv.ConnectListAllDomains(1, flags)
	if err != nil {
		return nil, err
	}
	set := make(map[libvirt.UUID]bool, len(doms))
	for _, d := range doms {
		set[d.UUID] = true
	}
	return set, nil
}

func paramUint(ps []libvirt.TypedParam, field string) (uint64, bool) {
	for _, p := range ps {
		if p.Field != field {
			continue
		}
		switch v := p.Value.I.(type) {
		case int32:
			return uint64(v), true
		case uint32:
			return uint64(v), true
		case int64:
			return uint64(v), true
		case uint64:
			return v, true
		}
	}
	return 0, false
}

// unsupported reports whether libvirt answered that the host has no driver
// for a call: a modular install without virtnetworkd, say. A list that
// needs one is then empty, not an error.
func unsupported(err error) bool {
	var e libvirt.Error
	return errors.As(err, &e) && e.Code == uint32(libvirt.ErrNoSupport)
}

// FormatUUID writes a UUID as libvirt does: 8-4-4-4-12 hex digits.
func FormatUUID(u libvirt.UUID) string {
	h := hex.EncodeToString(u[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ParseUUID reads a UUID in libvirt's form (dashes optional).
func ParseUUID(s string) (libvirt.UUID, error) {
	var u libvirt.UUID
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != len(u) {
		return u, fmt.Errorf("%q is not a UUID", s)
	}
	copy(u[:], b)
	return u, nil
}

// Lookup finds a machine by UUID or by name.
func Lookup(ctx context.Context, s *conn.Session, ref string) (Domain, error) {
	doms, err := List(ctx, s)
	if err != nil {
		return Domain{}, err
	}
	for _, d := range doms {
		if d.UUID == ref || d.Name == ref {
			return d, nil
		}
	}
	return Domain{}, fmt.Errorf("no machine %q on %s", ref, s.Target())
}
