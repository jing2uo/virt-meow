package api

import (
	"context"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// Stats are the rates of the host's running machines. The page asks every
// 2 s while it shows them; the host is sampled once however many pages ask
// (app.Manager.Stats).
func (s *Service) Stats(id string) (hv.Stats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.Stats(ctx, id)
}

// HostStats is the host's CPU and memory in use and its machines, as Stats.
func (s *Service) HostStats(id string) (hv.HostUsage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.HostUsage(ctx, id)
}

// Host is what the host is (versions, CPUs, memory) and what it can run.
func (s *Service) Host(id string) (hv.Host, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.Host(ctx, id)
}

// HostDevices are the host's PCI and USB devices and the machines using
// each; one call per device and machine, so more time.
func (s *Service) HostDevices(id string) ([]hv.HostDevice, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	ds, err := s.c.m.HostDevices(ctx, id)
	if ds == nil {
		ds = []hv.HostDevice{}
	}
	return ds, err
}

// HostDeviceXML is a host device's description, as nodedev-dumpxml gives it.
func (s *Service) HostDeviceXML(id, name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.HostDeviceXML(ctx, id, name)
}
