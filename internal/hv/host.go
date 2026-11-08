package hv

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Host is what a connection's host is and what it can run: what the
// creation form offers is derived from it.
type Host struct {
	Hostname   string `json:"hostname"`
	Arch       string `json:"arch"`
	CPUs       int    `json:"cpus"`
	MemoryKiB  uint64 `json:"memory_kib"`
	FreeKiB    uint64 `json:"free_kib"`
	Libvirt    string `json:"libvirt_version"`
	Hypervisor string `json:"hypervisor_version"` // QEMU's, for the qemu driver

	// What new machines can have, from the domain capabilities of the
	// accelerated virt type (kvm) when the host has it, else of TCG (qemu).
	VirtType   string   `json:"virt_type"` // kvm, qemu, test
	Machines   []string `json:"machines"`  // canonical machine types: q35, pc, virt…
	MaxVCPUs   int      `json:"max_vcpus"`
	UEFI       bool     `json:"uefi"`
	SecureBoot bool     `json:"secure_boot"`
	TPM        bool     `json:"tpm"`
	// Clipboard says new machines share their clipboard with the console:
	// the host has qemu-vdagent (libvirt 8.4, QEMU 6.1) and it doesn't
	// block saving or snapshotting a running machine (QEMU 10.1).
	Clipboard bool `json:"clipboard"`
}

// HostInfo asks the host what it is and what it can run.
func HostInfo(ctx context.Context, s *conn.Session) (Host, error) {
	var h Host
	err := s.Do(ctx, func(lv *libvirt.Libvirt) error {
		var err error
		if h.Hostname, err = lv.ConnectGetHostname(); err != nil {
			return err
		}
		_, mem, cpus, _, _, _, _, _, err := lv.NodeGetInfo()
		if err != nil {
			return err
		}
		h.CPUs, h.MemoryKiB = int(cpus), mem
		if free, err := lv.NodeGetFreeMemory(); err == nil {
			h.FreeKiB = free / 1024
		}
		var lib, hyp uint64
		if v, err := lv.ConnectGetLibVersion(); err == nil {
			h.Libvirt, lib = version(v), v
		}
		if v, err := lv.ConnectGetVersion(); err == nil && v > 0 {
			h.Hypervisor, hyp = version(v), v
		}

		capsXML, err := lv.ConnectGetCapabilities()
		if err != nil {
			return err
		}
		var caps libvirtxml.Caps
		if err := caps.Unmarshal(capsXML); err != nil {
			return fmt.Errorf("host capabilities: %w", err)
		}
		if caps.Host.CPU != nil {
			h.Arch = caps.Host.CPU.Arch
		}
		h.VirtType, h.Machines = guestOf(caps, h.Arch)
		h.Clipboard = (h.VirtType == "kvm" || h.VirtType == "qemu") && clipboardSupported(lib, hyp) && clipboardMigrates(hyp)

		// For the machine type new machines get (q35 where there is one):
		// Secure Boot needs SMM, which i440fx (the default) lacks. The test
		// driver has no domain capabilities at all.
		var machine libvirt.OptString
		if slices.Contains(h.Machines, "q35") {
			machine = libvirt.OptString{"q35"}
		}
		dcXML, err := lv.ConnectGetDomainCapabilities(nil, libvirt.OptString{h.Arch}, machine, libvirt.OptString{h.VirtType}, 0)
		if err != nil {
			return nil
		}
		var dc libvirtxml.DomainCaps
		if err := dc.Unmarshal(dcXML); err != nil {
			return fmt.Errorf("domain capabilities: %w", err)
		}
		if dc.VCPU != nil {
			h.MaxVCPUs = int(dc.VCPU.Max)
		}
		if dc.OS != nil {
			h.UEFI = slices.Contains(enum(dc.OS.Enums, "firmware"), "efi")
			if ff := dc.OS.FirmwareFeatures; ff != nil && ff.Supported == "yes" {
				h.SecureBoot = slices.Contains(enum(ff.Enums, "secureBoot"), "yes")
			} else if dc.OS.Loader != nil {
				h.SecureBoot = slices.Contains(enum(dc.OS.Loader.Enums, "secure"), "yes")
			}
		}
		if dc.Devices != nil && dc.Devices.TPM != nil {
			h.TPM = dc.Devices.TPM.Supported == "yes" && len(enum(dc.Devices.TPM.Enums, "backendModel")) > 0
		}
		return nil
	})
	return h, err
}

// guestOf picks the best virt type the host offers for its own arch (kvm
// over qemu) and that guest's canonical machine types.
func guestOf(caps libvirtxml.Caps, arch string) (string, []string) {
	for _, g := range caps.Guests {
		if g.OSType != "hvm" || g.Arch.Name != arch {
			continue
		}
		virt := ""
		for _, d := range g.Arch.Domains {
			if d.Type == "kvm" || virt == "" {
				virt = d.Type
			}
		}
		// The aliases (q35, pc, virt), not the versions they point to
		// (pc-q35-9.2): a definition names the alias and libvirt pins it.
		var machines []string
		for _, m := range g.Arch.Machines {
			if !slices.Contains(machines, m.Name) && !strings.Contains(m.Name, "-") && m.Name != "none" {
				machines = append(machines, m.Name)
			}
		}
		return virt, machines
	}
	return "", nil
}

func enum(es []libvirtxml.DomainCapsEnum, name string) []string {
	for _, e := range es {
		if e.Name == name {
			return e.Values
		}
	}
	return nil
}

// version writes libvirt's packed version (major*1e6 + minor*1e3 + micro).
func version(v uint64) string {
	return fmt.Sprintf("%d.%d.%d", v/1000000, v/1000%1000, v%1000)
}

// HostSample is the host's counters at one moment; two make its use
// (HostRates).
type HostSample struct {
	Time time.Time `json:"time"`
	// CPU time of every CPU since the host booted, busy and in all; 0 when
	// the driver doesn't say (the test driver).
	CPUBusyNs  uint64 `json:"cpu_busy_ns"`
	CPUTotalNs uint64 `json:"cpu_total_ns"`
	MemoryKiB  uint64 `json:"memory_kib"`
	// UsedKiB is the memory in use, without the kernel's buffers and page
	// cache; 0 when the driver doesn't say.
	UsedKiB  uint64 `json:"used_kib"`
	Running  int    `json:"running"`  // machines
	Machines int    `json:"machines"` // running and defined
}

// HostUsage is what the host is using.
type HostUsage struct {
	CPUPercent *float64 `json:"cpu_percent"` // of all its CPUs; nil when unknown
	MemoryKiB  uint64   `json:"memory_kib"`
	UsedKiB    uint64   `json:"used_kib"` // 0 when unknown
	Running    int      `json:"running"`
	Machines   int      `json:"machines"`
}

// GetHostSample reads the host's CPU and memory counters and how many
// machines it runs.
func GetHostSample(ctx context.Context, s *conn.Session) (HostSample, error) {
	var h HostSample
	err := s.Do(ctx, func(lv *libvirt.Libvirt) error {
		n, err := lv.ConnectNumOfDomains()
		if err != nil {
			return err
		}
		defined, err := lv.ConnectNumOfDefinedDomains()
		if err != nil {
			return err
		}
		h.Running, h.Machines = int(n), int(n+defined)
		_, mem, _, _, _, _, _, _, err := lv.NodeGetInfo()
		if err != nil {
			return err
		}
		h.MemoryKiB = mem
		h.Time = time.Now()
		// -1: every CPU, every NUMA cell. A driver without these (the test
		// driver) leaves them unknown.
		const all = -1
		if _, np, err := lv.NodeGetCPUStats(all, 0, 0); err == nil && np > 0 {
			if ps, _, err := lv.NodeGetCPUStats(all, np, 0); err == nil {
				for _, p := range ps {
					switch p.Field {
					case "kernel", "user":
						h.CPUBusyNs += p.Value
						h.CPUTotalNs += p.Value
					case "idle", "iowait":
						h.CPUTotalNs += p.Value
					}
				}
			}
		}
		if _, np, err := lv.NodeGetMemoryStats(0, all, 0); err == nil && np > 0 {
			if ps, _, err := lv.NodeGetMemoryStats(np, all, 0); err == nil {
				m := map[string]uint64{}
				for _, p := range ps {
					m[p.Field] = p.Value
				}
				if t := m["total"]; t > 0 && t >= m["free"]+m["buffers"]+m["cached"] {
					h.UsedKiB = t - m["free"] - m["buffers"] - m["cached"]
				}
			}
		}
		return nil
	})
	return h, err
}

// HostRates is the host's use between two samples: CPU over that time,
// memory and machines as of cur.
func HostRates(prev, cur HostSample) HostUsage {
	u := HostUsage{MemoryKiB: cur.MemoryKiB, UsedKiB: cur.UsedKiB, Running: cur.Running, Machines: cur.Machines}
	if cur.CPUTotalNs > prev.CPUTotalNs && cur.CPUBusyNs >= prev.CPUBusyNs && prev.CPUTotalNs > 0 {
		p := float64(cur.CPUBusyNs-prev.CPUBusyNs) / float64(cur.CPUTotalNs-prev.CPUTotalNs) * 100
		u.CPUPercent = &p
	}
	return u
}
