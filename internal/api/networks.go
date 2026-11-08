package api

import (
	"context"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// The networks tab: virtual networks, as virt-mm net.

func (s *Service) Networks(id string) ([]hv.Network, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	ns, err := s.c.m.Networks(ctx, id)
	if ns == nil {
		ns = []hv.Network{}
	}
	return ns, err
}

// Bridges are the host's bridges, for a network that joins one.
func (s *Service) Bridges(id string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	bs, err := s.c.m.Bridges(ctx, id)
	if bs == nil {
		bs = []string{}
	}
	return bs, err
}

func (s *Service) AddNetwork(id string, n hv.NewNetwork) (hv.Network, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.AddNetwork(ctx, id, n)
}

// SetNetwork starts or stops a network or turns its autostart on or off.
// Stopping one running machines are on fails with hv.ErrNetworkInUse,
// whose text starts the error, unless force.
func (s *Service) SetNetwork(id, name string, a hv.NetworkAction, force bool) (hv.Network, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.SetNetwork(ctx, id, name, a, force)
}

// RemoveNetwork stops and forgets a network, with force as SetNetwork.
func (s *Service) RemoveNetwork(id, name string, force bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RemoveNetwork(ctx, id, name, force)
}

func (s *Service) NetworkDefinition(id, name string) (hv.NetworkDefinition, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.NetworkDefinition(ctx, id, name)
}

// RedefineNetwork saves xml as the network's definition, unless it changed
// since base was read.
func (s *Service) RedefineNetwork(id, name, base, xml string) (hv.NetworkDefinition, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RedefineNetwork(ctx, id, name, base, xml)
}

// DHCPHosts are the network's fixed addresses.
func (s *Service) DHCPHosts(id, name string) ([]hv.DHCPHost, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	hs, err := s.c.m.DHCPHosts(ctx, id, name)
	if hs == nil {
		hs = []hv.DHCPHost{}
	}
	return hs, err
}

// AddDHCPHost gives mac the address ip (and the hostname name, if not
// ""), live and in the definition.
func (s *Service) AddDHCPHost(id, network, mac, ip, name string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.AddDHCPHost(ctx, id, network, mac, ip, name)
}

func (s *Service) RemoveDHCPHost(id, network, mac string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RemoveDHCPHost(ctx, id, network, mac)
}

// Leases are a running network's DHCP leases.
func (s *Service) Leases(id, name string) ([]hv.Lease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	ls, err := s.c.m.Leases(ctx, id, name)
	if ls == nil {
		ls = []hv.Lease{}
	}
	return ls, err
}
