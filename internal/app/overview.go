package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// HostSummary is one connection in the overview of every host: its state,
// and when it is connected, its CPU and memory in use and its machines.
type HostSummary struct {
	ConnInfo
	Usage *hv.HostUsage `json:"usage,omitempty"`
	// ListError is why its machines or usage couldn't be read.
	ListError string `json:"list_error,omitempty"`
}

// HostMachine is a machine of the overview, with its connection.
type HostMachine struct {
	Connection string `json:"connection"`
	hv.Domain
}

// AllHosts is every connection and every machine on them.
type AllHosts struct {
	Hosts    []HostSummary `json:"hosts"`
	Machines []HostMachine `json:"machines"`
}

// AllHosts reads every connection's machines and usage, the hosts at once:
// one list (ConnectListAllDomains) and one usage sample per host. The page
// asks only what is connected (connecting is the user's to do there); the
// CLI, with connect, opens every saved connection first.
func (m *Manager) AllHosts(ctx context.Context, connect bool) AllHosts {
	conns := m.Connections()
	out := AllHosts{Hosts: make([]HostSummary, len(conns)), Machines: []HostMachine{}}
	machines := make([][]HostMachine, len(conns))
	var wg sync.WaitGroup
	for i, c := range conns {
		out.Hosts[i].ConnInfo = c
		if c.Status != "connected" && !connect {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := &out.Hosts[i]
			s, err := m.Session(c.ID)
			if err != nil {
				h.ListError = err.Error()
				return
			}
			doms, err := hv.List(ctx, s)
			if err != nil {
				h.ListError = err.Error()
				return
			}
			for _, d := range doms {
				machines[i] = append(machines[i], HostMachine{Connection: c.ID, Domain: d})
			}
			if u, err := m.HostUsage(ctx, c.ID); err == nil {
				h.Usage = &u
			} else {
				h.ListError = "usage: " + err.Error()
			}
		}()
	}
	wg.Wait()
	for i, c := range conns {
		if connect { // the state it is in now
			out.Hosts[i].ConnInfo = m.connInfo(c.ID, out.Hosts[i].ConnInfo)
		}
		out.Machines = append(out.Machines, machines[i]...)
	}
	return out
}

// connInfo is the connection id's current state, else old.
func (m *Manager) connInfo(id string, old ConnInfo) ConnInfo {
	for _, c := range m.Connections() {
		if c.ID == id {
			return c
		}
	}
	return old
}

// MachineRef is a machine on one of the connections.
type MachineRef struct {
	Connection string `json:"connection"`
	UUID       string `json:"uuid"`
}

// PowerResult is one machine's outcome of a power action on several.
type PowerResult struct {
	MachineRef
	Error string `json:"error,omitempty"`
}

// powerAtOnce bounds the machines a bulk action works on at a time.
const powerAtOnce = 8

// PowerMany does the same power action to several machines, on any of the
// connections, a few at a time; each one's outcome is in the result, in
// the order asked.
func (m *Manager) PowerMany(ctx context.Context, refs []MachineRef, action string) ([]PowerResult, error) {
	if _, err := hv.ParseAction(action); err != nil {
		return nil, err
	}
	out := make([]PowerResult, len(refs))
	sem := make(chan struct{}, powerAtOnce)
	var wg sync.WaitGroup
	for i, r := range refs {
		out[i].MachineRef = r
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := m.Power(ctx, r.Connection, r.UUID, action); err != nil {
				out[i].Error = err.Error()
			}
		}()
	}
	wg.Wait()
	return out, nil
}

// PowerErrors joins the failures of a bulk action into one error, naming
// the machines by name where names gives them; nil when all succeeded.
func PowerErrors(rs []PowerResult, names map[MachineRef]string) error {
	var errs []string
	for _, r := range rs {
		if r.Error != "" {
			errs = append(errs, fmt.Sprintf("%s: %s", cmp.Or(names[r.MachineRef], r.UUID), r.Error))
		}
	}
	slices.Sort(errs)
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}
