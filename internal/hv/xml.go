package hv

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Definition is a machine's persistent definition, as the XML editor shows
// it: what the machine boots with next, secrets included so that saving it
// back doesn't drop them.
type Definition struct {
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Active bool   `json:"active"` // running: a saved change takes effect at the next start
	XML    string `json:"xml"`
}

// ErrChanged is returned when the definition changed after the editor read
// it; the editor reloads rather than overwrite someone else's change.
var ErrChanged = errors.New("the definition changed since it was opened: reload it and edit again")

const defFlags = libvirt.DomainXMLInactive | libvirt.DomainXMLSecure

// GetDefinition reads a machine's persistent definition.
func GetDefinition(ctx context.Context, sess *conn.Session, uuid string) (Definition, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return Definition{}, err
	}
	var d Definition
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		d, err = definition(lv, dom)
		return err
	})
	return d, err
}

func definition(lv *libvirt.Libvirt, dom libvirt.Domain) (Definition, error) {
	x, err := lv.DomainGetXMLDesc(dom, defFlags)
	if err != nil {
		return Definition{}, err
	}
	state, _, err := lv.DomainGetState(dom, 0)
	if err != nil {
		return Definition{}, err
	}
	return Definition{UUID: FormatUUID(dom.UUID), Name: dom.Name, Active: stateOf(libvirt.DomainState(state)).Active(), XML: x}, nil
}

// identity is the part of a domain document the editor checks before
// handing it to libvirt.
type identity struct {
	XMLName xml.Name `xml:"domain"`
	Name    string   `xml:"name"`
	UUID    string   `xml:"uuid"`
}

func parseIdentity(x string) (identity, error) {
	var id identity
	if err := xml.Unmarshal([]byte(x), &id); err != nil {
		return id, fmt.Errorf("not a domain definition: %w", err)
	}
	id.Name, id.UUID = strings.TrimSpace(id.Name), strings.TrimSpace(id.UUID)
	if id.Name == "" {
		return id, errors.New("the definition has no <name>")
	}
	return id, nil
}

// Redefine saves x as the definition of the machine uuid, and returns the
// definition as libvirt now has it. base is the XML the editor started from:
// if the definition changed since, nothing is saved and ErrChanged returned.
//
// The UUID can't change (that is another machine: clone it instead). The
// name can, on a machine that is off: it is renamed first, and renamed back
// if libvirt then refuses the definition.
func Redefine(ctx context.Context, sess *conn.Session, uuid, base, x string) (Definition, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return Definition{}, err
	}
	id, err := parseIdentity(x)
	if err != nil {
		return Definition{}, err
	}
	if id.UUID == "" {
		return Definition{}, fmt.Errorf("keep <uuid>%s</uuid>: without it libvirt makes a new machine", uuid)
	}
	if nu, err := ParseUUID(id.UUID); err != nil || nu != u {
		return Definition{}, fmt.Errorf("the <uuid> can't change (it is %s); to make another machine, clone this one", uuid)
	}
	var out Definition
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		cur, err := definition(lv, dom)
		if err != nil {
			return err
		}
		if base != "" && cur.XML != base {
			return ErrChanged
		}
		if x == cur.XML {
			out = cur
			return nil
		}
		renamed := false
		if id.Name != dom.Name {
			if cur.Active {
				return fmt.Errorf("a running machine can't be renamed: shut %s down first, or keep its <name>", dom.Name)
			}
			if !nameRE.MatchString(id.Name) {
				return fmt.Errorf("name %q: letters, digits, '.', '_' and '-', starting with a letter or digit", id.Name)
			}
			if _, err := lv.DomainRename(dom, libvirt.OptString{id.Name}, 0); err != nil {
				return fmt.Errorf("renaming to %s: %w", id.Name, err)
			}
			renamed = true
		}
		nd, err := lv.DomainDefineXMLFlags(x, libvirt.DomainDefineValidate)
		if err != nil {
			err = fmt.Errorf("libvirt refused the definition: %w", err)
			if renamed {
				if back, lerr := lv.DomainLookupByUUID(u); lerr == nil {
					if _, rerr := lv.DomainRename(back, libvirt.OptString{dom.Name}, 0); rerr != nil {
						err = fmt.Errorf("%w (and renaming it back to %s failed: %v)", err, dom.Name, rerr)
					}
				}
			}
			return err
		}
		out, err = definition(lv, nd)
		return err
	})
	return out, err
}
