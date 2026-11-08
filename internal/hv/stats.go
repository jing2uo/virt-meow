package hv

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Sample is the cumulative counters of running machines at one moment;
// two samples make rates (Rates).
type Sample struct {
	Time     time.Time  `json:"time"`
	Machines []Counters `json:"machines"`
}

// Counters are one running machine's counters.
type Counters struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	ID        int32  `json:"id"` // a new ID between two samples: it restarted
	State     State  `json:"state"`
	VCPUs     int    `json:"vcpus"`
	CPUTimeNs uint64 `json:"cpu_time_ns"`
	MemoryKiB uint64 `json:"memory_kib"`     // what it has (the balloon's size)
	MaxMemKiB uint64 `json:"max_memory_kib"` // what it may grow to
	RSSKiB    uint64 `json:"rss_kib"`        // what the host gives its process
	// GuestUsedKiB is what the guest uses, when its balloon driver reports
	// it; 0 when unknown.
	GuestUsedKiB uint64         `json:"guest_used_kib,omitempty"`
	Disks        []DiskCounters `json:"disks"`
	NICs         []NICCounters  `json:"nics"`
}

// DiskCounters are one disk's bytes read and written since it started.
type DiskCounters struct {
	Name       string `json:"name"` // the target: vda, sdb…
	ReadBytes  uint64 `json:"read_bytes"`
	WriteBytes uint64 `json:"write_bytes"`
}

// NICCounters are one interface's bytes received and sent, as the guest
// sees them, since it started.
type NICCounters struct {
	Name    string `json:"name"` // the host side: vnet0, tap…
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

// Stats are rates between two samples.
type Stats struct {
	Time     time.Time      `json:"time"`
	Seconds  float64        `json:"seconds"` // between the samples
	Machines []MachineStats `json:"machines"`
}

// MachineStats are one machine's rates and sizes. A machine that started
// (or restarted) between the samples has its sizes and no rates.
type MachineStats struct {
	UUID  string `json:"uuid"`
	Name  string `json:"name"`
	State State  `json:"state"`
	VCPUs int    `json:"vcpus"`
	// CPUPercent is the share of its vCPUs it used: 100 is all of them
	// busy, whatever their number.
	CPUPercent   float64    `json:"cpu_percent"`
	MemoryKiB    uint64     `json:"memory_kib"`
	MaxMemKiB    uint64     `json:"max_memory_kib"`
	RSSKiB       uint64     `json:"rss_kib"`
	GuestUsedKiB uint64     `json:"guest_used_kib,omitempty"`
	DiskRead     float64    `json:"disk_read_bps"` // bytes per second, every disk
	DiskWrite    float64    `json:"disk_write_bps"`
	NetRx        float64    `json:"net_rx_bps"`
	NetTx        float64    `json:"net_tx_bps"`
	Disks        []DiskRate `json:"disks"`
	NICs         []NICRate  `json:"nics"`
	New          bool       `json:"new,omitempty"` // no earlier sample: sizes only
}

// DiskRate is one disk's rates, bytes per second.
type DiskRate struct {
	Name  string  `json:"name"`
	Read  float64 `json:"read_bps"`
	Write float64 `json:"write_bps"`
}

// NICRate is one interface's rates, bytes per second.
type NICRate struct {
	Name string  `json:"name"`
	Rx   float64 `json:"rx_bps"`
	Tx   float64 `json:"tx_bps"`
}

// GetSample reads the counters of every running machine, or of the
// machine uuid when it isn't "", in one call.
func GetSample(ctx context.Context, sess *conn.Session, uuid string) (Sample, error) {
	out := Sample{Machines: []Counters{}}
	err := sess.Do(ctx, func(lv *libvirt.Libvirt) error {
		var doms []libvirt.Domain
		var flags uint32
		if uuid != "" {
			u, err := ParseUUID(uuid)
			if err != nil {
				return err
			}
			dom, err := lv.DomainLookupByUUID(u)
			if err != nil {
				return err
			}
			doms = []libvirt.Domain{dom}
		} else {
			flags = uint32(libvirt.ConnectGetAllDomainsStatsActive)
		}
		recs, err := lv.ConnectGetAllDomainStats(doms, uint32(libvirt.DomainStatsState|libvirt.DomainStatsCPUTotal|
			libvirt.DomainStatsBalloon|libvirt.DomainStatsVCPU|libvirt.DomainStatsInterface|libvirt.DomainStatsBlock), flags)
		if err != nil {
			return err
		}
		out.Time = time.Now()
		for _, r := range recs {
			c := countersOf(r.Dom, r.Params)
			if !c.State.Active() {
				if uuid != "" {
					return fmt.Errorf("%s is not running", r.Dom.Name)
				}
				continue
			}
			out.Machines = append(out.Machines, c)
		}
		return nil
	})
	slices.SortFunc(out.Machines, func(a, b Counters) int { return strings.Compare(a.Name, b.Name) })
	return out, err
}

func countersOf(d libvirt.Domain, p []libvirt.TypedParam) Counters {
	c := Counters{UUID: FormatUUID(d.UUID), Name: d.Name, ID: d.ID, State: Unknown, Disks: []DiskCounters{}, NICs: []NICCounters{}}
	u := func(f string) uint64 { v, _ := paramUint(p, f); return v }
	if v, ok := paramUint(p, "state.state"); ok {
		c.State = stateOf(libvirt.DomainState(v))
	}
	c.VCPUs = int(u("vcpu.current"))
	c.CPUTimeNs = u("cpu.time")
	c.MemoryKiB, c.MaxMemKiB, c.RSSKiB = u("balloon.current"), u("balloon.maximum"), u("balloon.rss")
	avail, okA := paramUint(p, "balloon.available")
	unused, okU := paramUint(p, "balloon.unused")
	if okA && okU && avail >= unused {
		c.GuestUsedKiB = avail - unused
	}
	for i := range int(u("block.count")) {
		k := fmt.Sprintf("block.%d.", i)
		c.Disks = append(c.Disks, DiskCounters{Name: paramString(p, k+"name"), ReadBytes: u(k + "rd.bytes"), WriteBytes: u(k + "wr.bytes")})
	}
	for i := range int(u("net.count")) {
		k := fmt.Sprintf("net.%d.", i)
		c.NICs = append(c.NICs, NICCounters{Name: paramString(p, k+"name"), RxBytes: u(k + "rx.bytes"), TxBytes: u(k + "tx.bytes")})
	}
	return c
}

func paramString(ps []libvirt.TypedParam, field string) string {
	for _, p := range ps {
		if p.Field == field {
			if s, ok := p.Value.I.(string); ok {
				return s
			}
		}
	}
	return ""
}

// Rates turns two samples into rates: the machines of cur, with what they
// did since prev.
func Rates(prev, cur Sample) Stats {
	secs := cur.Time.Sub(prev.Time).Seconds()
	out := Stats{Time: cur.Time, Seconds: secs, Machines: []MachineStats{}}
	before := map[string]Counters{}
	for _, m := range prev.Machines {
		before[m.UUID] = m
	}
	per := func(a, b uint64) float64 {
		if b < a || secs <= 0 { // a counter reset: a device replaced
			return 0
		}
		return float64(b-a) / secs
	}
	for _, m := range cur.Machines {
		s := MachineStats{UUID: m.UUID, Name: m.Name, State: m.State, VCPUs: m.VCPUs, MemoryKiB: m.MemoryKiB,
			MaxMemKiB: m.MaxMemKiB, RSSKiB: m.RSSKiB, GuestUsedKiB: m.GuestUsedKiB, Disks: []DiskRate{}, NICs: []NICRate{}}
		p, ok := before[m.UUID]
		if !ok || p.ID != m.ID || secs <= 0 {
			s.New = true
			out.Machines = append(out.Machines, s)
			continue
		}
		if m.VCPUs > 0 {
			s.CPUPercent = min(per(p.CPUTimeNs, m.CPUTimeNs)/1e9/float64(m.VCPUs)*100, 100)
		}
		for _, d := range m.Disks {
			r := DiskRate{Name: d.Name}
			if i := slices.IndexFunc(p.Disks, func(x DiskCounters) bool { return x.Name == d.Name }); i >= 0 {
				r.Read, r.Write = per(p.Disks[i].ReadBytes, d.ReadBytes), per(p.Disks[i].WriteBytes, d.WriteBytes)
			}
			s.DiskRead, s.DiskWrite = s.DiskRead+r.Read, s.DiskWrite+r.Write
			s.Disks = append(s.Disks, r)
		}
		for _, n := range m.NICs {
			r := NICRate{Name: n.Name}
			if i := slices.IndexFunc(p.NICs, func(x NICCounters) bool { return x.Name == n.Name }); i >= 0 {
				r.Rx, r.Tx = per(p.NICs[i].RxBytes, n.RxBytes), per(p.NICs[i].TxBytes, n.TxBytes)
			}
			s.NetRx, s.NetTx = s.NetRx+r.Rx, s.NetTx+r.Tx
			s.NICs = append(s.NICs, r)
		}
		out.Machines = append(out.Machines, s)
	}
	return out
}
