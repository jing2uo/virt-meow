package api

import (
	"context"
	"time"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// cloneTimeout bounds a clone, which copies the machine's disks in the
// call: minutes for a large disk over SSH.
const cloneTimeout = time.Hour

// MachineInfo is a machine's details page: virt-mm info, with the power
// actions that make sense for it now, reset among them.
type MachineInfo struct {
	hv.Info
	Actions []hv.Action `json:"actions"`
}

func (s *Service) Info(id, uuid string) (MachineInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	info, err := s.c.m.Info(ctx, id, uuid)
	if err != nil {
		return MachineInfo{}, err
	}
	out := MachineInfo{Info: info, Actions: []hv.Action{}}
	for _, a := range append(hv.Actions[:len(hv.Actions):len(hv.Actions)], hv.Reset) {
		if a.Allowed(info.State) {
			out.Actions = append(out.Actions, a)
		}
	}
	return out, nil
}

// Save stops a running machine with its memory saved on the host.
func (s *Service) Save(id, uuid string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*callTimeout)
	defer cancel()
	return s.c.m.Save(ctx, id, uuid)
}

// DropSave removes a machine's saved state, reporting whether it had one.
func (s *Service) DropSave(id, uuid string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.DropSave(ctx, id, uuid)
}

// Set changes a machine's settings; only those given change.
func (s *Service) Set(id, uuid string, settings hv.Settings) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.Set(ctx, id, uuid, settings)
}

// Rename renames a machine that is off.
func (s *Service) Rename(id, uuid, name string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.Rename(ctx, id, uuid, name)
}

// Clone copies a machine that is off, its disks with it.
func (s *Service) Clone(id, uuid, name string) (hv.Cloned, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
	defer cancel()
	return s.c.m.Clone(ctx, id, uuid, name)
}

// CPUModels lists the CPU modes and models a machine can be given.
func (s *Service) CPUModels(id, uuid string) (hv.CPUModels, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.CPUModels(ctx, id, uuid)
}

// SetBoot sets the boot order (disk targets and interface MACs, first to
// last) and, when menu is not nil, the boot menu.
func (s *Service) SetBoot(id, uuid string, devs []string, menu *bool) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.SetBoot(ctx, id, uuid, devs, menu)
}

// AddDisk adds a disk: a new volume, an existing one, or a path.
func (s *Service) AddDisk(id, uuid string, d hv.NewDisk) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	return s.c.m.AddDisk(ctx, id, uuid, d)
}

// RemoveDisk removes a disk or drive; deleteVolume (off only) deletes its
// volume too.
func (s *Service) RemoveDisk(id, uuid, target string, deleteVolume bool) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RemoveDisk(ctx, id, uuid, target, deleteVolume)
}

// ResizeDisk grows a disk to size bytes.
func (s *Service) ResizeDisk(id, uuid, target string, size uint64) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.ResizeDisk(ctx, id, uuid, target, size)
}

// InsertCD puts pool/volume in the CD/DVD drive target ("" for the first,
// or a new drive when it has none).
func (s *Service) InsertCD(id, uuid, target, pool, volume string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.InsertCD(ctx, id, uuid, target, pool, volume)
}

// EjectCD empties the CD/DVD drive target.
func (s *Service) EjectCD(id, uuid, target string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.EjectCD(ctx, id, uuid, target)
}

// AddNIC adds a network interface: on a bridge, a virtual network, or
// user-mode networking.
func (s *Service) AddNIC(id, uuid string, n hv.NewNIC) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.AddNIC(ctx, id, uuid, n)
}

// RemoveNIC removes the interface with the MAC address mac.
func (s *Service) RemoveNIC(id, uuid, mac string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RemoveNIC(ctx, id, uuid, mac)
}

// SetLink plugs an interface's cable in (up) or pulls it out.
func (s *Service) SetLink(id, uuid, mac string, up bool) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.SetLink(ctx, id, uuid, mac, up)
}
