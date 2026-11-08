package api

import (
	"context"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// A machine's other devices, as virt-mm device changes them: video,
// graphics, TPM, sound, RNG, the clipboard, and any device from its XML.

// SetVideo gives the machine the video model (hv.VideoModels).
func (s *Service) SetVideo(id, uuid, model string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.SetVideo(ctx, id, uuid, model)
}

// SetSound gives the machine one sound device of the model, or none.
func (s *Service) SetSound(id, uuid, model string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.SetSound(ctx, id, uuid, model)
}

// AddGraphics gives the machine a console of kind (vnc) listening on
// listen, 127.0.0.1 when empty.
func (s *Service) AddGraphics(id, uuid, kind, listen string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.AddGraphics(ctx, id, uuid, kind, listen)
}

// SetGraphicsListen moves the machine's console of kind to listen.
func (s *Service) SetGraphicsListen(id, uuid, kind, listen string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.SetGraphicsListen(ctx, id, uuid, kind, listen)
}

// SetGraphicsPassword sets the password the machine's console of kind
// asks for, "" for none; a running machine's console takes it now.
func (s *Service) SetGraphicsPassword(id, uuid, kind, password string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.SetGraphicsPassword(ctx, id, uuid, kind, password)
}

// RemoveGraphics removes the machine's console of kind.
func (s *Service) RemoveGraphics(id, uuid, kind string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RemoveGraphics(ctx, id, uuid, kind)
}

// AddClipboard has the machine share its clipboard with the console (a
// qemu-vdagent channel, for spice-vdagent in the guest); now when it runs.
func (s *Service) AddClipboard(id, uuid string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.AddClipboard(ctx, id, uuid)
}

func (s *Service) RemoveClipboard(id, uuid string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RemoveClipboard(ctx, id, uuid)
}

// AddTPM gives the machine an emulated TPM 2.0; model "" picks crb on q35,
// tis otherwise.
func (s *Service) AddTPM(id, uuid, model string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.AddTPM(ctx, id, uuid, model)
}

func (s *Service) RemoveTPM(id, uuid string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RemoveTPM(ctx, id, uuid)
}

// AddRNG gives the machine a virtio RNG fed from the host's /dev/urandom.
func (s *Service) AddRNG(id, uuid string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.AddRNG(ctx, id, uuid)
}

func (s *Service) RemoveRNG(id, uuid string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RemoveRNG(ctx, id, uuid)
}

// AttachDevice adds a device given as its XML (one element of <devices>).
func (s *Service) AttachDevice(id, uuid, xml string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.AttachDevice(ctx, id, uuid, xml)
}

// DetachDevice removes the device its XML describes.
func (s *Service) DetachDevice(id, uuid, xml string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.DetachDevice(ctx, id, uuid, xml)
}

// AttachHostDevice gives a machine a USB or PCI device of the host, by its
// name or address (HostDevices lists them).
func (s *Service) AttachHostDevice(id, uuid, ref string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.AttachHostDevice(ctx, id, uuid, ref)
}

// DetachHostDevice takes a host device from a machine, by the ref its
// details list.
func (s *Service) DetachHostDevice(id, uuid, ref string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.DetachHostDevice(ctx, id, uuid, ref)
}

// AddFilesystem shares the host's directory dir with a machine as the
// virtiofs folder tag, from its next start.
func (s *Service) AddFilesystem(id, uuid, dir, tag string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.AddFilesystem(ctx, id, uuid, dir, tag)
}

// RemoveFilesystem stops sharing the folder tag, from the next start.
func (s *Service) RemoveFilesystem(id, uuid, tag string) (hv.Edited, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.RemoveFilesystem(ctx, id, uuid, tag)
}
