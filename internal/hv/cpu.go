package hv

import (
	"context"
	"fmt"
	"slices"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// domainCaps asks libvirt what a machine like d can have: the domain
// capabilities of its emulator, arch, machine type and virt type.
func domainCaps(lv *libvirt.Libvirt, d *libvirtxml.Domain) (libvirtxml.DomainCaps, error) {
	var emu, arch, machine, virt libvirt.OptString
	if d.Devices != nil && d.Devices.Emulator != "" {
		emu = libvirt.OptString{d.Devices.Emulator}
	}
	if d.OS != nil && d.OS.Type != nil {
		if d.OS.Type.Arch != "" {
			arch = libvirt.OptString{d.OS.Type.Arch}
		}
		if d.OS.Type.Machine != "" {
			machine = libvirt.OptString{d.OS.Type.Machine}
		}
	}
	if d.Type != "" {
		virt = libvirt.OptString{d.Type}
	}
	var dc libvirtxml.DomainCaps
	x, err := lv.ConnectGetDomainCapabilities(emu, arch, machine, virt, 0)
	if err != nil {
		return dc, fmt.Errorf("the host's domain capabilities: %w", err)
	}
	if err := dc.Unmarshal(x); err != nil {
		return dc, fmt.Errorf("domain capabilities: %w", err)
	}
	return dc, nil
}

func cpuMode(dc libvirtxml.DomainCaps, name string) *libvirtxml.DomainCapsCPUMode {
	if dc.CPU == nil {
		return nil
	}
	for i, m := range dc.CPU.Modes {
		if m.Name == name {
			return &dc.CPU.Modes[i]
		}
	}
	return nil
}

// CPUModel is a named CPU model a machine can be given.
type CPUModel struct {
	Name   string `json:"name"`
	Vendor string `json:"vendor,omitempty"`
	// Usable: yes, no (the host lacks some of its features: Blockers) or
	// unknown.
	Usable   string   `json:"usable"`
	Blockers []string `json:"blockers,omitempty"`
}

// CPUModels is what --cpu-model takes on one machine.
type CPUModels struct {
	Current string     `json:"current"` // as cpuModelText describes it
	Modes   []string   `json:"modes"`   // host-passthrough, host-model, where supported
	Models  []CPUModel `json:"models"`
}

// GetCPUModels lists the CPU models the machine uuid can be given, from
// the domain capabilities of its own emulator and machine type.
func GetCPUModels(ctx context.Context, sess *conn.Session, uuid string) (CPUModels, error) {
	u, err := ParseUUID(uuid)
	if err != nil {
		return CPUModels{}, err
	}
	var out CPUModels
	err = sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		dom, err := lv.DomainLookupByUUID(u)
		if err != nil {
			return err
		}
		cur, err := definition(lv, dom)
		if err != nil {
			return err
		}
		var d libvirtxml.Domain
		if err := d.Unmarshal(cur.XML); err != nil {
			return err
		}
		dc, err := domainCaps(lv, &d)
		if err != nil {
			return err
		}
		out = CPUModels{Current: cpuModelText(d.CPU), Modes: []string{}, Models: []CPUModel{}}
		for _, m := range []string{"host-passthrough", "host-model"} {
			if cpuModeOK(&d, dc, m) == nil {
				out.Modes = append(out.Modes, m)
			}
		}
		if c := cpuMode(dc, "custom"); c != nil && c.Supported == "yes" {
			for _, m := range c.Models {
				out.Models = append(out.Models, CPUModel{Name: m.Name, Vendor: m.Vendor, Usable: cmpOr(m.Usable, "unknown"), Blockers: blockers(c, m.Name)})
			}
		}
		return nil
	})
	return out, err
}

func blockers(c *libvirtxml.DomainCapsCPUMode, model string) []string {
	for _, b := range c.Blockers {
		if b.Model == model {
			var fs []string
			for _, f := range b.Features {
				fs = append(fs, f.Name)
			}
			return fs
		}
	}
	return nil
}

// cpuModeOK says whether the machine d can have the CPU mode host-passthrough
// or host-model.
func cpuModeOK(d *libvirtxml.Domain, dc libvirtxml.DomainCaps, mode string) error {
	// Passing the host's CPU through needs hardware virtualisation: under
	// TCG there is nothing to pass.
	if mode == "host-passthrough" && d.Type != "kvm" && d.Type != "hvf" {
		return fmt.Errorf("host-passthrough needs KVM, and this machine runs under %s", cmpOr(d.Type, "an unknown virt type"))
	}
	if m := cpuMode(dc, mode); m == nil || m.Supported != "yes" {
		return fmt.Errorf("the host can't give this machine %s", mode)
	}
	return nil
}

// cpuModelText describes a machine's CPU model.
func cpuModelText(c *libvirtxml.DomainCPU) string {
	switch {
	case c == nil || (c.Mode == "" && c.Model == nil):
		return "the hypervisor's default"
	case c.Mode == "" || c.Mode == "custom":
		if c.Model == nil || c.Model.Value == "" {
			return "the hypervisor's default"
		}
		return c.Model.Value
	}
	return c.Mode
}

// setCPUModel gives the machine the CPU model name: host-passthrough,
// host-model, or a named model the host offers for its machine type. The
// topology, cache and NUMA layout are kept; the features of the old model
// go with it. It takes effect at the next start.
func (e *edit) setCPUModel(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("CPU model: host-passthrough, host-model or a model's name (virt-mm cpu-models %s)", e.def.Name)
	}
	d := e.def
	before := cpuModelText(d.CPU)
	cpu := &libvirtxml.DomainCPU{}
	if d.CPU != nil {
		cpu.Topology, cpu.Cache, cpu.Numa, cpu.MaxPhysAddr = d.CPU.Topology, d.CPU.Cache, d.CPU.Numa, d.CPU.MaxPhysAddr
	}
	dc, err := domainCaps(e.lv, d)
	if err != nil {
		return err
	}
	switch name {
	case "host-passthrough", "host-model":
		if err := cpuModeOK(d, dc, name); err != nil {
			return err
		}
		cpu.Mode = name
		if name == "host-passthrough" {
			cpu.Check, cpu.Migratable = "none", "on"
		}
	default:
		c := cpuMode(dc, "custom")
		if c == nil || c.Supported != "yes" {
			return fmt.Errorf("the host offers no named CPU models for this machine")
		}
		i := slices.IndexFunc(c.Models, func(m libvirtxml.DomainCapsCPUModel) bool { return m.Name == name })
		if i < 0 {
			if j := slices.IndexFunc(c.Models, func(m libvirtxml.DomainCapsCPUModel) bool { return strings.EqualFold(m.Name, name) }); j >= 0 {
				return fmt.Errorf("no CPU model %q: did you mean %s?", name, c.Models[j].Name)
			}
			return fmt.Errorf("no CPU model %q for this machine (virt-mm cpu-models %s lists them)", name, d.Name)
		}
		if c.Models[i].Usable == "no" {
			return fmt.Errorf("the host's CPU can't run %s: it lacks %s", name, strings.Join(blockers(c, name), ", "))
		}
		cpu.Mode, cpu.Match, cpu.Check = "custom", "exact", "partial"
		cpu.Model = &libvirtxml.DomainCPUModel{Value: name, Fallback: "forbid"}
	}
	if after := cpuModelText(cpu); after == before && (d.CPU == nil || d.CPU.Mode == cpu.Mode) {
		return nil
	}
	d.CPU = cpu
	e.changed(fmt.Sprintf("CPU model %s → %s", before, cpuModelText(cpu)), nil)
	return nil
}
