package hv

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Snapshot is one of a machine's snapshots.
type Snapshot struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
	State       string    `json:"state"`  // the machine's when it was taken: running, shutoff…
	Memory      bool      `json:"memory"` // it has the running machine's memory: reverting resumes it
	Parent      string    `json:"parent,omitempty"`
	Depth       int       `json:"depth"` // in the tree: 0 for a root
	Current     bool      `json:"current"`
}

// Snapshots lists a machine's snapshots as a tree, depth first: each one
// follows its parent, children by age.
func Snapshots(ctx context.Context, sess *conn.Session, uuid string) ([]Snapshot, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		snaps, _, err := lv.DomainListAllSnapshots(dom, 1, 0)
		if err != nil {
			return err
		}
		current := ""
		if c, err := lv.DomainSnapshotCurrent(dom, 0); err == nil {
			current = c.Name
		}
		// One call per snapshot for its description, a few at a time.
		all := make([]Snapshot, len(snaps))
		errs := make([]error, len(snaps))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for i, sn := range snaps {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				x, err := lv.DomainSnapshotGetXMLDesc(sn, 0)
				if err != nil {
					errs[i] = fmt.Errorf("snapshot %s: %w", sn.Name, err)
					return
				}
				var d libvirtxml.DomainSnapshot
				if err := d.Unmarshal(x); err != nil {
					errs[i] = err
					return
				}
				s := Snapshot{Name: sn.Name, Description: d.Description, State: d.State, Current: sn.Name == current,
					Memory: d.Memory != nil && d.Memory.Snapshot != "" && d.Memory.Snapshot != "no"}
				if n, err := strconv.ParseInt(d.CreationTime, 10, 64); err == nil {
					s.Created = time.Unix(n, 0).UTC()
				}
				if d.Parent != nil {
					s.Parent = d.Parent.Name
				}
				all[i] = s
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
		out = snapshotTree(all)
		return nil
	})
	return out, err
}

// snapshotTree orders snapshots depth first from their roots, siblings by
// age; libvirt keeps the time in seconds, so those taken in the same one go
// by name rather than in whatever order libvirt listed them.
func snapshotTree(all []Snapshot) []Snapshot {
	slices.SortFunc(all, func(a, b Snapshot) int {
		return cmp.Or(a.Created.Compare(b.Created), strings.Compare(a.Name, b.Name))
	})
	names := map[string]bool{}
	for _, s := range all {
		names[s.Name] = true
	}
	out := make([]Snapshot, 0, len(all))
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, s := range all {
			p := s.Parent
			if !names[p] {
				p = ""
			}
			if p == parent {
				s.Depth = depth
				out = append(out, s)
				walk(s.Name, depth+1)
			}
		}
	}
	walk("", 0)
	return out
}

// snapshottable says why a machine's snapshot would fail, before libvirt
// says it less plainly: internal snapshots live inside qcow2 images, so
// every writable disk must be one; a running machine's memory goes with
// them, and with UEFI its NVRAM too, which libvirt can only snapshot in
// qcow2. Warnings are what the snapshot will lack.
func snapshottable(d *libvirtxml.Domain, active bool) (disks []libvirtxml.DomainSnapshotDisk, warnings []string, err error) {
	var raw []string
	if d.Devices != nil {
		for _, dk := range d.Devices.Disks {
			if dk.Target == nil {
				continue
			}
			if dk.Device == "cdrom" || dk.Device == "floppy" || dk.ReadOnly != nil || dk.Shareable != nil || dk.Source == nil {
				disks = append(disks, libvirtxml.DomainSnapshotDisk{Name: dk.Target.Dev, Snapshot: "no"})
				continue
			}
			if dk.Driver == nil || dk.Driver.Type != "qcow2" {
				format := "raw"
				if dk.Driver != nil && dk.Driver.Type != "" {
					format = dk.Driver.Type
				}
				raw = append(raw, fmt.Sprintf("%s (%s)", dk.Target.Dev, format))
				continue
			}
			disks = append(disks, libvirtxml.DomainSnapshotDisk{Name: dk.Target.Dev, Snapshot: "internal"})
		}
	}
	if len(raw) > 0 {
		return nil, nil, fmt.Errorf("%s can't have snapshots: they are kept inside qcow2 images, and %s isn't one", d.Name, strings.Join(raw, ", "))
	}
	// A running machine's memory is kept in a qcow2 image too: without one
	// there is nowhere to keep either.
	if !slices.ContainsFunc(disks, func(x libvirtxml.DomainSnapshotDisk) bool { return x.Snapshot == "internal" }) {
		return nil, nil, fmt.Errorf("%s has no writable qcow2 disk to keep a snapshot in", d.Name)
	}
	if o := d.OS; o != nil && firmwareOf(d) != "bios" && o.NVRam != nil {
		qcow2 := o.NVRam.Format == "qcow2"
		switch {
		case active && !qcow2:
			return nil, nil, fmt.Errorf("%s runs UEFI with its NVRAM in a raw file, and libvirt snapshots a running UEFI machine only with qcow2 NVRAM: shut it down to snapshot its disks (without its UEFI variables)", d.Name)
		case !qcow2:
			warnings = append(warnings, "its UEFI variables (NVRAM) aren't in the snapshot: libvirt keeps them only in qcow2 NVRAM, and this machine's is raw")
		}
	}
	return disks, warnings, nil
}

// CreateSnapshot takes an internal snapshot of the machine uuid: of its
// qcow2 disks, and of its memory when it runs (reverting then resumes it
// where it was). name, when empty, is the time.
func CreateSnapshot(ctx context.Context, sess *conn.Session, uuid, name, description string) (Edited, error) {
	if name == "" {
		name = time.Now().Format("snap-20060102-150405")
	}
	if !nameRE.MatchString(name) {
		return Edited{}, fmt.Errorf("snapshot name %q: letters, digits, '.', '_' and '-', starting with a letter or digit", name)
	}
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
		var d libvirtxml.Domain
		if err := d.Unmarshal(cur.XML); err != nil {
			return err
		}
		if _, err := lv.DomainSnapshotLookupByName(dom, name, 0); err == nil {
			return fmt.Errorf("%s already has a snapshot %s", cur.Name, name)
		}
		disks, warnings, err := snapshottable(&d, cur.Active)
		if err != nil {
			return err
		}
		sx := libvirtxml.DomainSnapshot{Name: name, Description: description, Disks: &libvirtxml.DomainSnapshotDisks{Disks: disks}}
		what := "disks"
		if cur.Active {
			sx.Memory = &libvirtxml.DomainSnapshotMemory{Snapshot: "internal"}
			what = "disks and memory"
		}
		x, err := sx.Marshal()
		if err != nil {
			return err
		}
		if _, err := lv.DomainSnapshotCreateXML(dom, x, 0); err != nil {
			return vdagentHint(fmt.Errorf("libvirt refused the snapshot: %w", err), dom.Name)
		}
		out.Changes = append(out.Changes, Change{What: fmt.Sprintf("snapshot %s (%s)", name, what), Live: true})
		out.Warnings = warnings
		return nil
	})
	return out, err
}

// RevertSnapshot puts the machine uuid back as it was at the snapshot:
// running, from its memory, if it was taken running; off otherwise.
// What changed since is lost.
func RevertSnapshot(ctx context.Context, sess *conn.Session, uuid, name string) (Edited, error) {
	return onSnapshot(ctx, sess, uuid, name, func(lv *libvirt.Libvirt, dom libvirt.Domain, sn libvirt.DomainSnapshot, out *Edited) error {
		if err := lv.DomainRevertToSnapshot(sn, 0); err != nil {
			return fmt.Errorf("libvirt refused to revert: %w", err)
		}
		state, _, err := lv.DomainGetState(dom, 0)
		if err != nil {
			return err
		}
		st := stateOf(libvirt.DomainState(state))
		out.Active = st.Active()
		out.Changes = append(out.Changes, Change{What: fmt.Sprintf("reverted to %s: %s", name, st), Live: true})
		return nil
	})
}

// DeleteSnapshot deletes a snapshot; with children, the snapshots taken
// from it too. Without, its children take its parent.
func DeleteSnapshot(ctx context.Context, sess *conn.Session, uuid, name string, children bool) (Edited, error) {
	return onSnapshot(ctx, sess, uuid, name, func(lv *libvirt.Libvirt, dom libvirt.Domain, sn libvirt.DomainSnapshot, out *Edited) error {
		var flags libvirt.DomainSnapshotDeleteFlags
		what := "snapshot " + name + " deleted"
		if children {
			flags = libvirt.DomainSnapshotDeleteChildren
			if kids, _, err := lv.DomainSnapshotListAllChildren(sn, 1, 0); err == nil && len(kids) > 0 {
				what += fmt.Sprintf(", and the %d taken from it", len(kids))
			}
		}
		if err := lv.DomainSnapshotDelete(sn, flags); err != nil {
			return fmt.Errorf("libvirt refused to delete it: %w", err)
		}
		out.Changes = append(out.Changes, Change{What: what, Live: true})
		return nil
	})
}

func onSnapshot(ctx context.Context, sess *conn.Session, uuid, name string, f func(*libvirt.Libvirt, libvirt.Domain, libvirt.DomainSnapshot, *Edited) error) (Edited, error) {
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
		state, _, err := lv.DomainGetState(dom, 0)
		if err != nil {
			return err
		}
		out = Edited{UUID: uuid, Name: dom.Name, Active: stateOf(libvirt.DomainState(state)).Active(), Changes: []Change{}}
		sn, err := lv.DomainSnapshotLookupByName(dom, name, 0)
		if err != nil {
			return fmt.Errorf("%s has no snapshot %s", dom.Name, name)
		}
		return f(lv, dom, sn, &out)
	})
	return out, err
}
