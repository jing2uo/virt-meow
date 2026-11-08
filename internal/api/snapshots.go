package api

import (
	"context"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// Snapshots lists a machine's snapshots as a tree, depth first.
func (s *Service) Snapshots(id, uuid string) ([]hv.Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	ss, err := s.c.m.Snapshots(ctx, id, uuid)
	if ss == nil {
		ss = []hv.Snapshot{}
	}
	return ss, err
}

// CreateSnapshot takes a snapshot named name (the time when empty); with
// a running machine's memory it takes a while.
func (s *Service) CreateSnapshot(id, uuid, name, description string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*callTimeout)
	defer cancel()
	return s.c.m.CreateSnapshot(ctx, id, uuid, name, description)
}

// RevertSnapshot puts the machine back as it was at the snapshot.
func (s *Service) RevertSnapshot(id, uuid, name string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*callTimeout)
	defer cancel()
	return s.c.m.RevertSnapshot(ctx, id, uuid, name)
}

// DeleteSnapshot deletes a snapshot, and with children those taken from it.
func (s *Service) DeleteSnapshot(id, uuid, name string, children bool) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	return s.c.m.DeleteSnapshot(ctx, id, uuid, name, children)
}
