package hv

import (
	"context"
	"errors"
	"fmt"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Settings are a machine's simple settings, as virt-mm set and the details
// page change them. A nil field is left as it is.
type Settings struct {
	VCPUs        *int    `json:"vcpus,omitempty"`
	Sockets      *int    `json:"sockets,omitempty"`
	Cores        *int    `json:"cores,omitempty"`
	Threads      *int    `json:"threads,omitempty"`
	CPUModel     *string `json:"cpu_model,omitempty"`      // host-passthrough, host-model or a model's name
	Firmware     *string `json:"firmware,omitempty"`       // bios, uefi, uefi-secure; only when it is off
	MemoryMiB    *int    `json:"memory_mib,omitempty"`     // what it has
	MaxMemoryMiB *int    `json:"max_memory_mib,omitempty"` // what it may balloon up to
	Autostart    *bool   `json:"autostart,omitempty"`
	Title        *string `json:"title,omitempty"`
	Description  *string `json:"description,omitempty"`
}

func (s Settings) cpu() bool {
	return s.VCPUs != nil || s.Sockets != nil || s.Cores != nil || s.Threads != nil
}

// Set changes a machine's settings. What a running machine can take now it
// gets now (vCPUs up to its maximum, memory up to its maximum, title and
// description); the rest (the CPU model, the topology) waits for its next
// start, and the result says which is which. The firmware changes only on a
// machine that is off.
func Set(ctx context.Context, sess *conn.Session, uuid string, s Settings) (Edited, error) {
	if !s.cpu() && s.CPUModel == nil && s.Firmware == nil && s.MemoryMiB == nil && s.MaxMemoryMiB == nil && s.Autostart == nil && s.Title == nil && s.Description == nil {
		return Edited{}, errors.New("nothing to change")
	}
	return modify(ctx, sess, uuid, func(e *edit) error {
		_, hostMem, hostCPUs, _, _, _, _, _, err := e.lv.NodeGetInfo()
		if err != nil {
			return err
		}
		if err := e.setCPUs(s, int(hostCPUs)); err != nil {
			return err
		}
		if s.CPUModel != nil {
			if err := e.setCPUModel(*s.CPUModel); err != nil {
				return err
			}
		}
		if s.Firmware != nil {
			if err := e.setFirmware(*s.Firmware); err != nil {
				return err
			}
		}
		if err := e.setMemory(s, hostMem); err != nil {
			return err
		}
		e.setText("title", &e.def.Title, s.Title, libvirt.DomainMetadataTitle)
		e.setText("description", &e.def.Description, s.Description, libvirt.DomainMetadataDescription)
		if s.Autostart != nil {
			on := *s.Autostart
			cur, err := e.lv.DomainGetAutostart(e.dom)
			if err != nil {
				return err
			}
			if (cur == 1) != on {
				what, v := "autostart off", int32(0)
				if on {
					what, v = "autostart on", 1
				}
				e.outside(what, func() error { return e.lv.DomainSetAutostart(e.dom, v) })
			}
		}
		return nil
	})
}

func (e *edit) setCPUs(s Settings, hostCPUs int) error {
	if !s.cpu() {
		return nil
	}
	d := e.def
	if d.VCPUs != nil && len(d.VCPUs.VCPU) > 0 {
		return errors.New("this machine lists its vCPUs one by one (<vcpus>): change them with virt-mm edit")
	}
	if d.VCPU == nil {
		d.VCPU = &libvirtxml.DomainVCPU{Placement: "static", Value: 1}
	}
	was := d.VCPU.Value
	if d.VCPU.Current != 0 {
		was = d.VCPU.Current
	}
	var old *libvirtxml.DomainCPUTopology
	if d.CPU != nil {
		old = d.CPU.Topology
	}
	one := func(n int) int { return max(n, 1) }
	n := 0
	if s.VCPUs != nil {
		n = *s.VCPUs
	}
	var topo *libvirtxml.DomainCPUTopology
	switch {
	case s.Sockets != nil || s.Cores != nil || s.Threads != nil:
		t := libvirtxml.DomainCPUTopology{Sockets: 1, Cores: 1, Threads: 1}
		if old != nil {
			t = *old
		}
		for _, f := range []struct {
			v   *int
			dst *int
			n   string
		}{{s.Sockets, &t.Sockets, "sockets"}, {s.Cores, &t.Cores, "cores"}, {s.Threads, &t.Threads, "threads"}} {
			if f.v == nil {
				continue
			}
			if *f.v < 1 {
				return fmt.Errorf("%s: at least 1", f.n)
			}
			*f.dst = *f.v
		}
		prod := one(t.Sockets) * one(t.Dies) * one(t.Clusters) * one(t.Cores) * one(t.Threads)
		if n == 0 {
			n = prod
		} else if n != prod {
			return fmt.Errorf("%d vCPUs, but sockets=%d cores=%d threads=%d make %d", n, one(t.Sockets), one(t.Cores), one(t.Threads), prod)
		}
		topo = &t
	case old != nil:
		// Keep the shape of each socket; more or fewer sockets.
		per := one(old.Dies) * one(old.Clusters) * one(old.Cores) * one(old.Threads)
		if n%per != 0 {
			return fmt.Errorf("this machine has %d vCPUs per socket: give --sockets, --cores and --threads with --cpus", per)
		}
		t := *old
		t.Sockets = n / per
		topo = &t
	}
	if n < 1 || (hostCPUs > 0 && n > 4*hostCPUs) {
		return fmt.Errorf("vCPUs: 1 to %d (4 times the host's %d)", 4*hostCPUs, hostCPUs)
	}
	topoChanged := topo != nil && (old == nil || *topo != *old)
	if uint(n) == was && d.VCPU.Current == 0 && !topoChanged {
		return nil
	}
	d.VCPU.Value, d.VCPU.Current = uint(n), 0
	if topoChanged {
		if d.CPU == nil {
			d.CPU = &libvirtxml.DomainCPU{}
		}
		d.CPU.Topology = topo
		e.changed(fmt.Sprintf("CPU topology sockets=%d cores=%d threads=%d", topo.Sockets, one(topo.Cores), one(topo.Threads)), nil)
	}
	if uint(n) == was {
		return nil
	}
	live := func() error { return errors.New("the CPU topology changes at its next start") }
	if !topoChanged {
		live = func() error {
			m, err := e.lv.DomainGetVcpusFlags(e.dom, uint32(libvirt.DomainVCPULive|libvirt.DomainVCPUMaximum))
			if err != nil {
				return err
			}
			if n > int(m) {
				return fmt.Errorf("the running machine can have at most %d vCPUs", m)
			}
			return e.lv.DomainSetVcpusFlags(e.dom, uint32(n), uint32(libvirt.DomainVCPULive))
		}
	}
	e.changed(fmt.Sprintf("vCPUs %d → %d", was, n), live)
	return nil
}

func (e *edit) setMemory(s Settings, hostKiB uint64) error {
	if s.MemoryMiB == nil && s.MaxMemoryMiB == nil {
		return nil
	}
	d := e.def
	if d.Memory == nil {
		return errors.New("this machine has no <memory>: set it with virt-mm edit")
	}
	maxK, err := kibOf(d.Memory.Value, d.Memory.Unit)
	if err != nil {
		return err
	}
	curK := maxK
	if d.CurrentMemory != nil {
		if curK, err = kibOf(d.CurrentMemory.Value, d.CurrentMemory.Unit); err != nil {
			return err
		}
	}
	newMax, newCur := maxK, curK
	if s.MaxMemoryMiB != nil {
		newMax = uint64(max(*s.MaxMemoryMiB, 0)) << 10
		newCur = min(newCur, newMax)
	}
	if s.MemoryMiB != nil {
		newCur = uint64(max(*s.MemoryMiB, 0)) << 10
		if newCur > newMax {
			if s.MaxMemoryMiB != nil {
				return fmt.Errorf("memory %s is more than the maximum %s", mib(newCur), mib(newMax))
			}
			newMax = newCur
		}
	}
	if newCur < 64<<10 {
		return errors.New("memory: at least 64 MiB")
	}
	if hostKiB > 0 && newMax > hostKiB {
		return fmt.Errorf("memory: at most the host's %s", mib(hostKiB))
	}
	d.Memory.Value, d.Memory.Unit = uint(newMax), "KiB"
	d.CurrentMemory = &libvirtxml.DomainCurrentMemory{Value: uint(newCur), Unit: "KiB"}
	if newMax != maxK {
		e.changed(fmt.Sprintf("maximum memory %s → %s", mib(maxK), mib(newMax)), nil)
	}
	if newCur != curK {
		e.changed(fmt.Sprintf("memory %s → %s", mib(curK), mib(newCur)), func() error {
			m, err := e.lv.DomainGetMaxMemory(e.dom)
			if err != nil {
				return err
			}
			if newCur > m {
				return fmt.Errorf("the running machine can have at most %s", mib(m))
			}
			// The balloon: the guest needs its driver, and a machine without
			// a balloon device can't change at all.
			return e.lv.DomainSetMemoryFlags(e.dom, newCur, uint32(libvirt.DomainMemLive))
		})
	}
	return nil
}

func (e *edit) setText(what string, dst, v *string, kind libvirt.DomainMetadataType) {
	if v == nil || *v == *dst {
		return
	}
	*dst = *v
	if *v == "" {
		what += " removed"
	} else {
		what = fmt.Sprintf("%s %q", what, *v)
	}
	e.changed(what, func() error {
		var val libvirt.OptString
		if *v != "" {
			val = libvirt.OptString{*v}
		}
		return e.lv.DomainSetMetadata(e.dom, int32(kind), val, nil, nil, libvirt.DomainAffectLive)
	})
}

// Rename renames a machine that is off.
func Rename(ctx context.Context, sess *conn.Session, uuid, name string) (Edited, error) {
	if !nameRE.MatchString(name) {
		return Edited{}, fmt.Errorf("name %q: letters, digits, '.', '_' and '-', starting with a letter or digit", name)
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
		out = Edited{UUID: uuid, Name: dom.Name, Changes: []Change{}}
		if dom.Name == name {
			return nil
		}
		state, _, err := lv.DomainGetState(dom, 0)
		if err != nil {
			return err
		}
		if stateOf(libvirt.DomainState(state)).Active() {
			return fmt.Errorf("a running machine can't be renamed: shut %s down first", dom.Name)
		}
		if _, err := lv.DomainLookupByName(name); err == nil {
			return fmt.Errorf("a machine named %q already exists", name)
		}
		if _, err := lv.DomainRename(dom, libvirt.OptString{name}, 0); err != nil {
			return err
		}
		out.Changes = append(out.Changes, Change{What: fmt.Sprintf("name %s → %s", dom.Name, name), Live: true})
		out.Name = name
		return nil
	})
	return out, err
}

// Define makes a new machine from a definition, as virsh define does, but
// never over an existing one: that is Redefine.
func Define(ctx context.Context, sess *conn.Session, x string) (Definition, error) {
	id, err := parseIdentity(x)
	if err != nil {
		return Definition{}, err
	}
	if !nameRE.MatchString(id.Name) {
		return Definition{}, fmt.Errorf("name %q: letters, digits, '.', '_' and '-', starting with a letter or digit", id.Name)
	}
	var out Definition
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		if _, err := lv.DomainLookupByName(id.Name); err == nil {
			return fmt.Errorf("a machine named %q already exists: to change it, virt-mm xml --set or virt-mm edit", id.Name)
		}
		if id.UUID != "" {
			u, err := ParseUUID(id.UUID)
			if err != nil {
				return fmt.Errorf("<uuid>: %w", err)
			}
			if d, err := lv.DomainLookupByUUID(u); err == nil {
				return fmt.Errorf("machine %s already has the UUID %s: drop the <uuid> to get a new one", d.Name, id.UUID)
			}
		}
		dom, err := lv.DomainDefineXMLFlags(x, libvirt.DomainDefineValidate)
		if err != nil {
			return fmt.Errorf("libvirt refused the definition: %w", err)
		}
		out, err = definition(lv, dom)
		return err
	})
	return out, err
}
