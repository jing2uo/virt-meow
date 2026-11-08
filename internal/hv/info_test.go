package hv

import (
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestConfigOf(t *testing.T) {
	var d libvirtxml.Domain
	err := d.Unmarshal(`<domain type='kvm'><name>a</name>
		<memory unit='GiB'>4</memory><currentMemory unit='MiB'>2048</currentMemory>
		<vcpu current='2'>8</vcpu><cpu><topology sockets='1' cores='4' threads='2'/></cpu></domain>`)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{VCPUs: 2, Sockets: 1, Cores: 4, Threads: 2, MemoryKiB: 2 << 20, MaxMemKiB: 4 << 20}
	if c := configOf(&d); c != want {
		t.Errorf("configOf: %+v, want %+v", c, want)
	}
	d = libvirtxml.Domain{Memory: &libvirtxml.DomainMemory{Value: 262144}, VCPU: &libvirtxml.DomainVCPU{Value: 1}}
	if c := configOf(&d); c != (Config{VCPUs: 1, MemoryKiB: 256 << 10, MaxMemKiB: 256 << 10}) {
		t.Errorf("no topology, no currentMemory: %+v", c)
	}
}
