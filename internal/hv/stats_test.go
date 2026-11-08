package hv

import (
	"testing"
	"time"
)

func TestRates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	m := func(id int32, cpu, rd, rx uint64) Counters {
		return Counters{UUID: "u", Name: "a", ID: id, State: Running, VCPUs: 2, CPUTimeNs: cpu, MemoryKiB: 1024,
			Disks: []DiskCounters{{Name: "vda", ReadBytes: rd}}, NICs: []NICCounters{{Name: "vnet0", RxBytes: rx}}}
	}
	prev := Sample{Time: t0, Machines: []Counters{m(1, 0, 0, 100)}}
	cur := Sample{Time: t0.Add(2 * time.Second), Machines: []Counters{m(1, 2e9, 4096, 300), {UUID: "v", Name: "b", ID: 2, VCPUs: 1}}}
	s := Rates(prev, cur)
	if s.Seconds != 2 || len(s.Machines) != 2 {
		t.Fatalf("%+v", s)
	}
	a := s.Machines[0]
	// 2 s of CPU time in 2 s on 2 vCPUs: half of them.
	if a.CPUPercent != 50 || a.DiskRead != 2048 || a.Disks[0].Read != 2048 || a.NetRx != 100 || a.New {
		t.Errorf("a: %+v", a)
	}
	if b := s.Machines[1]; !b.New || b.CPUPercent != 0 {
		t.Errorf("b, new: %+v", b)
	}
	// Restarted between the samples: its counters start again.
	cur.Machines[0] = m(3, 1e9, 10, 0)
	if a := Rates(prev, cur).Machines[0]; !a.New || a.CPUPercent != 0 {
		t.Errorf("restarted: %+v", a)
	}
}

func TestHostRates(t *testing.T) {
	a := HostSample{CPUBusyNs: 100, CPUTotalNs: 1000, MemoryKiB: 8 << 20, UsedKiB: 1 << 20, Running: 1}
	b := HostSample{CPUBusyNs: 400, CPUTotalNs: 2000, MemoryKiB: 8 << 20, UsedKiB: 2 << 20, Running: 2, Machines: 3}
	u := HostRates(a, b)
	if u.CPUPercent == nil || *u.CPUPercent != 30 || u.UsedKiB != 2<<20 || u.Running != 2 || u.Machines != 3 {
		t.Errorf("%+v", u)
	}
	if u := HostRates(HostSample{}, HostSample{Running: 1}); u.CPUPercent != nil {
		t.Errorf("CPU without counters: %v", *u.CPUPercent)
	}
}
