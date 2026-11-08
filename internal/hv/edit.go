package hv

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Change is one thing an edit changed, and when it takes effect.
type Change struct {
	What string `json:"what"`
	// Live: the running machine has it now. Otherwise it is in the
	// definition only, and a running machine gets it at its next start.
	Live bool   `json:"live"`
	Note string `json:"note,omitempty"` // why it couldn't be applied live
}

// Edited is the outcome of an edit.
type Edited struct {
	UUID    string   `json:"uuid"`
	Name    string   `json:"name"`
	Active  bool     `json:"active"`
	Changes []Change `json:"changes"`
	// Warnings are about what the edit made possible, for the user to
	// read before relying on it (a console open on the network).
	Warnings []string `json:"warnings,omitempty"`
}

// edit is one change to a machine: its definition, read and parsed, is
// handed to change, which updates it and returns what it changed and what
// to do on the running machine once the definition is saved.
type edit struct {
	lv     *libvirt.Libvirt
	dom    libvirt.Domain
	def    *libvirtxml.Domain
	active bool
	out    *Edited
	dirty  bool           // the definition changed: save it
	live   []func()       // after saving: apply to the running machine
	after  []func() error // after saving: changes outside the definition
}

// changed records a change to the definition; apply, when not nil and the
// machine is running, makes it live too.
func (e *edit) changed(what string, apply func() error) {
	e.dirty = true
	e.saved(what, apply)
}

// saved records a change libvirt has already made to the definition (a
// device attached with DomainAffectConfig); apply is as for changed.
func (e *edit) saved(what string, apply func() error) {
	i := len(e.out.Changes)
	e.out.Changes = append(e.out.Changes, Change{What: what})
	if apply == nil || !e.active {
		return
	}
	e.live = append(e.live, func() {
		if err := apply(); err != nil {
			e.out.Changes[i].Note = err.Error()
		} else {
			e.out.Changes[i].Live = true
		}
	})
}

// outside records a change that isn't in the definition (autostart). It is
// made once the definition is saved, so that a refused edit changes nothing.
func (e *edit) outside(what string, apply func() error) {
	e.after = append(e.after, func() error {
		if err := apply(); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		e.out.Changes = append(e.out.Changes, Change{What: what, Live: true})
		return nil
	})
}

// modify runs change on the persistent definition of the machine uuid,
// saves the definition if it changed (libvirt validating it), then applies
// the live parts. A definition virt-meow can't carry through libvirtxml
// without losing something is not rewritten: the edit is refused. (A change
// that saves its device through libvirt, with saved, doesn't rewrite it.)
func modify(ctx context.Context, sess *conn.Session, uuid string, change func(*edit) error) (Edited, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return Edited{}, err
	}
	var out Edited
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		cur, err := definition(lv, dom)
		if err != nil {
			return err
		}
		out = Edited{UUID: cur.UUID, Name: cur.Name, Active: cur.Active, Changes: []Change{}}
		var def libvirtxml.Domain
		if err := def.Unmarshal(cur.XML); err != nil {
			return err
		}
		e := &edit{lv: lv, dom: dom, def: &def, active: cur.Active, out: &out}
		if err := change(e); err != nil {
			return err
		}
		if e.dirty {
			if err := lossless(cur.XML); err != nil {
				return err
			}
			x, err := def.Marshal()
			if err != nil {
				return err
			}
			if _, err := lv.DomainDefineXMLFlags(x, libvirt.DomainDefineValidate); err != nil {
				return fmt.Errorf("libvirt refused the change: %w", err)
			}
		}
		for _, f := range e.live {
			f()
		}
		var errs []error
		for _, f := range e.after {
			errs = append(errs, f())
		}
		return errors.Join(errs...)
	})
	return out, err
}

// lossless fails if writing the definition x back through libvirtxml would
// lose any of it: an element or attribute libvirtxml doesn't know yet.
func lossless(x string) error {
	var d libvirtxml.Domain
	if err := d.Unmarshal(x); err != nil {
		return err
	}
	back, err := d.Marshal()
	if err != nil {
		return err
	}
	a, err := parseTree(x)
	if err != nil {
		return err
	}
	b, err := parseTree(back)
	if err != nil {
		return err
	}
	if diff := a.diff(b, ""); diff != "" {
		return fmt.Errorf("virt-meow would lose %s from this machine's definition; change it with virt-mm edit (the XML editor) instead", diff)
	}
	return nil
}

// xnode is an XML element compared for meaning: attributes in any order,
// surrounding whitespace ignored, children in their order among siblings of
// the same name (libvirt's schema doesn't order different elements).
type xnode struct {
	name  string
	attrs map[string]string
	text  string
	kids  []*xnode
}

func xname(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

func parseTree(x string) (*xnode, error) {
	dec := xml.NewDecoder(strings.NewReader(x))
	var stack []*xnode
	var root *xnode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xnode{name: xname(t.Name), attrs: map[string]string{}}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue // namespaces are already resolved into the names
				}
				n.attrs[xname(a.Name)] = a.Value
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			n := stack[len(stack)-1]
			n.text = strings.TrimSpace(n.text)
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("empty document")
	}
	return root, nil
}

// sameValue compares two attribute values or texts; numbers by value, as
// libvirt writes some in hex (port='0x10') and libvirtxml in decimal.
func sameValue(a, b string) bool {
	if a == b {
		return true
	}
	x, errA := strconv.ParseUint(a, 0, 64)
	y, errB := strconv.ParseUint(b, 0, 64)
	return errA == nil && errB == nil && x == y
}

// diff names the first thing a has that b lacks or has differently, or "".
func (a *xnode) diff(b *xnode, path string) string {
	path += "/" + a.name
	for k, v := range a.attrs {
		if bv, ok := b.attrs[k]; !ok || !sameValue(bv, v) {
			return fmt.Sprintf("%s/@%s", path, k)
		}
	}
	for k := range b.attrs {
		if _, ok := a.attrs[k]; !ok {
			return fmt.Sprintf("%s/@%s (added)", path, k)
		}
	}
	if !sameValue(a.text, b.text) {
		return path + " (its text)"
	}
	byName := func(n *xnode) map[string][]*xnode {
		m := map[string][]*xnode{}
		for _, k := range n.kids {
			m[k.name] = append(m[k.name], k)
		}
		return m
	}
	am, bm := byName(a), byName(b)
	names := make([]string, 0, len(am))
	for k := range am {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		ak, bk := am[k], bm[k]
		if len(ak) != len(bk) {
			return fmt.Sprintf("%s/%s (%d of them, %d after)", path, k, len(ak), len(bk))
		}
		for i := range ak {
			if d := ak[i].diff(bk[i], path); d != "" {
				return d
			}
		}
	}
	for k := range bm {
		if _, ok := am[k]; !ok {
			return fmt.Sprintf("%s/%s (added)", path, k)
		}
	}
	return ""
}

// kibOf converts a libvirt size (value and unit, as in <memory unit='GiB'>)
// to KiB.
func kibOf(v uint, unit string) (uint64, error) {
	n := uint64(v)
	switch strings.ToLower(unit) {
	case "", "k", "kib":
		return n, nil
	case "b", "bytes":
		return n >> 10, nil
	case "kb":
		return n * 1000 >> 10, nil
	case "m", "mib":
		return n << 10, nil
	case "mb":
		return n * 1000 * 1000 >> 10, nil
	case "g", "gib":
		return n << 20, nil
	case "gb":
		return n * 1000 * 1000 * 1000 >> 10, nil
	case "t", "tib":
		return n << 30, nil
	case "tb":
		return n * 1000 * 1000 * 1000 * 1000 >> 10, nil
	}
	return 0, fmt.Errorf("unknown unit %q", unit)
}

func mib(kib uint64) string {
	if kib%(1<<20) == 0 {
		return fmt.Sprintf("%d GiB", kib>>20)
	}
	return fmt.Sprintf("%d MiB", kib>>10)
}
