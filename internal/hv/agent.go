package hv

import (
	"context"
	"errors"
	"fmt"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"golang.org/x/crypto/ssh"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// GuestInfo is what the guest agent says about the system it runs in.
type GuestInfo struct {
	Hostname  string `json:"hostname,omitempty"`
	OS        string `json:"os,omitempty"`    // its pretty name: Debian GNU/Linux 13 (trixie)
	OSID      string `json:"os_id,omitempty"` // debian, fedora, mswindows …
	OSVersion string `json:"os_version,omitempty"`
	Kernel    string `json:"kernel,omitempty"` // the kernel's release
	Arch      string `json:"arch,omitempty"`
}

// libvirt's virDomainAuthorizedSSHKeysSetFlags, which go-libvirt lacks.
const (
	sshKeysAppend = 1 << 0
	sshKeysRemove = 1 << 1
)

// agentErr turns libvirt's errors about the guest agent into what the user
// can do about them.
func agentErr(name string, err error) error {
	var e libvirt.Error
	if !errors.As(err, &e) {
		return err
	}
	switch {
	case e.Code == uint32(libvirt.ErrArgumentUnsupported) && strings.Contains(e.Message, "not configured"):
		return fmt.Errorf("%s has no channel for a guest agent: add <channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel> (virt-mm device attach), and qemu-guest-agent in the guest", name)
	case e.Code == uint32(libvirt.ErrAgentUnresponsive):
		return fmt.Errorf("the guest agent of %s doesn't answer: is qemu-guest-agent installed and running in the guest? (%s)", name, e.Message)
	case e.Code == uint32(libvirt.ErrAgentCommandTimeout):
		return fmt.Errorf("the guest agent of %s took too long to answer", name)
	}
	return err
}

// withAgent runs f on a running machine, with agentErr's errors.
func withAgent(ctx context.Context, sess *conn.Session, uuid string, f func(lv *libvirt.Libvirt, dom libvirt.Domain) error) error {
	u, err := ParseUUID(uuid)
	if err != nil {
		return err
	}
	return sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		state, _, err := lv.DomainGetState(dom, 0)
		if err != nil {
			return err
		}
		if !stateOf(libvirt.DomainState(state)).Active() {
			return fmt.Errorf("%s is not running: its guest agent runs in the guest", dom.Name)
		}
		return agentErr(dom.Name, f(lv, dom))
	})
}

// guestInfo asks a running machine's agent for its hostname and system.
func guestInfo(lv *libvirt.Libvirt, dom libvirt.Domain) (*GuestInfo, error) {
	ps, err := lv.DomainGetGuestInfo(dom, uint32(libvirt.DomainGuestInfoOs|libvirt.DomainGuestInfoHostname), 0)
	if err != nil {
		return nil, agentErr(dom.Name, err)
	}
	return &GuestInfo{
		Hostname:  paramString(ps, "hostname"),
		OS:        paramString(ps, "os.pretty-name"),
		OSID:      paramString(ps, "os.id"),
		OSVersion: paramString(ps, "os.version"),
		Kernel:    paramString(ps, "os.kernel-release"),
		Arch:      paramString(ps, "os.machine"),
	}, nil
}

// SetUserPassword sets the password of a user in a running guest, through
// its agent (virsh set-user-password).
func SetUserPassword(ctx context.Context, sess *conn.Session, uuid, user, password string) error {
	if user == "" {
		return errors.New("no user")
	}
	if password == "" {
		return errors.New("an empty password: give one")
	}
	return withAgent(ctx, sess, uuid, func(lv *libvirt.Libvirt, dom libvirt.Domain) error {
		return lv.DomainSetUserPassword(dom, libvirt.OptString{user}, libvirt.OptString{password}, 0)
	})
}

// SSHKeys lists the SSH keys a user of a running guest accepts (its
// ~/.ssh/authorized_keys), through its agent.
func SSHKeys(ctx context.Context, sess *conn.Session, uuid, user string) ([]string, error) {
	var keys []string
	err := withAgent(ctx, sess, uuid, func(lv *libvirt.Libvirt, dom libvirt.Domain) error {
		var err error
		keys, err = lv.DomainAuthorizedSshKeysGet(dom, user, 0)
		// qemu-ga fails on a user without an authorized_keys file: one
		// that accepts no keys.
		var e libvirt.Error
		if errors.As(err, &e) && e.Code == uint32(libvirt.ErrAgentCommandFailed) &&
			strings.Contains(e.Message, "authorized_keys") && strings.Contains(e.Message, "No such file") {
			return nil
		}
		return err
	})
	if keys == nil {
		keys = []string{}
	}
	return keys, err
}

// ParseSSHKeys reads public keys in authorized_keys form, one a line;
// blank lines and # comments are skipped.
func ParseSSHKeys(text string) ([]string, error) {
	var keys []string
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err != nil {
			return nil, fmt.Errorf("line %d is not an SSH public key: %w", i+1, err)
		}
		keys = append(keys, line)
	}
	if len(keys) == 0 {
		return nil, errors.New("no SSH public keys")
	}
	return keys, nil
}

// AddSSHKeys adds keys to those a user of a running guest accepts.
func AddSSHKeys(ctx context.Context, sess *conn.Session, uuid, user string, keys []string) error {
	return setSSHKeys(ctx, sess, uuid, user, keys, sshKeysAppend)
}

// RemoveSSHKeys removes keys from those a user of a running guest accepts.
func RemoveSSHKeys(ctx context.Context, sess *conn.Session, uuid, user string, keys []string) error {
	return setSSHKeys(ctx, sess, uuid, user, keys, sshKeysRemove)
}

func setSSHKeys(ctx context.Context, sess *conn.Session, uuid, user string, keys []string, flags uint32) error {
	if user == "" {
		return errors.New("no user")
	}
	if len(keys) == 0 {
		return errors.New("no SSH public keys")
	}
	// What goes in must be keys: the agent writes any line it is given.
	// A line being taken out may be one that isn't.
	if flags&sshKeysAppend != 0 {
		for _, k := range keys {
			if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k)); err != nil || strings.ContainsAny(k, "\r\n") {
				return fmt.Errorf("%.40q is not an SSH public key: %v", k, err)
			}
		}
	}
	return withAgent(ctx, sess, uuid, func(lv *libvirt.Libvirt, dom libvirt.Domain) error {
		return lv.DomainAuthorizedSshKeysSet(dom, user, keys, flags)
	})
}
