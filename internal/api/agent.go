package api

import (
	"context"
)

// The guest agent, on the details page: Info has the guest's hostname and
// system when the agent answers (and why not in guest_note); these change
// a user's password and SSH keys, as virt-mm agent does.

// SetUserPassword sets a guest user's password through the agent.
func (s *Service) SetUserPassword(id, uuid, user, password string) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.SetUserPassword(ctx, id, uuid, user, password)
}

// SSHKeys are the keys a guest user accepts (authorized_keys).
func (s *Service) SSHKeys(id, uuid, user string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	ks, err := s.c.m.SSHKeys(ctx, id, uuid, user)
	if ks == nil {
		ks = []string{}
	}
	return ks, err
}

// SetSSHKeys adds keys to a guest user's, or with remove takes them out.
func (s *Service) SetSSHKeys(id, uuid, user string, keys []string, remove bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.SetSSHKeys(ctx, id, uuid, user, keys, remove)
}
