package app

import (
	"context"
	"sync"
	"time"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// The pages showing a host's statistics ask for them every 2 s while they
// show them (docs/adr/0003). However many pages ask, each host is sampled
// at most once per sampleEvery, and the rates are of its last two samples.
const (
	sampleEvery = 1500 * time.Millisecond
	// A sample older than sampleStale is not a rate's start: the pages
	// stopped asking for a while, and an average over it isn't "now".
	sampleStale = 15 * time.Second
)

// shared samples one kind of counter per connection and keeps the last,
// for the rates of the next.
type shared[S, R any] struct {
	sample func(ctx context.Context, id string) (S, error)
	rates  func(prev, cur S) R // prev is the zero S for the first
	now    func() time.Time

	mu    sync.Mutex
	hosts map[string]*sampled[S, R]
}

type sampled[S, R any] struct {
	mu   sync.Mutex // held while sampling: a second page waits for it
	at   time.Time  // of last; zero before the first
	last S
	out  R
}

func newShared[S, R any](sample func(context.Context, string) (S, error), rates func(prev, cur S) R) *shared[S, R] {
	return &shared[S, R]{sample: sample, rates: rates, now: time.Now, hosts: map[string]*sampled[S, R]{}}
}

func (s *shared[S, R]) get(ctx context.Context, id string) (R, error) {
	s.mu.Lock()
	h := s.hosts[id]
	if h == nil {
		h = &sampled[S, R]{}
		s.hosts[id] = h
	}
	s.mu.Unlock()

	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.at.IsZero() && s.now().Sub(h.at) < sampleEvery {
		return h.out, nil
	}
	cur, err := s.sample(ctx, id)
	if err != nil {
		var zero R
		return zero, err
	}
	prev := h.last
	if h.at.IsZero() || s.now().Sub(h.at) > sampleStale {
		var zero S
		prev = zero
	}
	h.out, h.last, h.at = s.rates(prev, cur), cur, s.now()
	return h.out, nil
}

// forget drops a connection's samples.
func (s *shared[S, R]) forget(id string) {
	s.mu.Lock()
	delete(s.hosts, id)
	s.mu.Unlock()
}

// Stats are the rates of every machine running on the host, from the last
// two samples any page asked for.
func (m *Manager) Stats(ctx context.Context, id string) (hv.Stats, error) {
	return m.stats.get(ctx, id)
}

// HostUsage is the host's CPU and memory in use and its machines, as Stats.
func (m *Manager) HostUsage(ctx context.Context, id string) (hv.HostUsage, error) {
	return m.hostUse.get(ctx, id)
}
