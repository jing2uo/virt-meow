package hv

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"

	"github.com/jing2uo/virt-meow/internal/conn"
)

// Video is one of a machine's video devices.
type Video struct {
	Model   string `json:"model"` // virtio, vga, qxl, bochs, ramfb, none, …
	Heads   uint   `json:"heads,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// Graphics is one of a machine's graphical consoles.
type Graphics struct {
	Type   string `json:"type"`   // vnc, spice, …
	Listen string `json:"listen"` // the address on the host, "" for its default
	// Port is the running machine's (chosen when it started, with
	// autoport); 0 when it is off or has none.
	Port     int  `json:"port,omitempty"`
	AutoPort bool `json:"autoport"`
	// Password says the console asks for one (never what it is).
	Password bool `json:"password,omitempty"`
}

// TPM is a machine's TPM.
type TPM struct {
	Model   string `json:"model"`   // tpm-crb, tpm-tis
	Backend string `json:"backend"` // emulator, passthrough, external
	Version string `json:"version,omitempty"`
}

// RNG is a machine's random number generator.
type RNG struct {
	Model   string `json:"model"`
	Backend string `json:"backend"` // /dev/urandom, builtin, egd
}

// peripherals reads the video, graphics, TPM, sound, RNG and clipboard of d
// into out; live, when not nil, is the running machine's definition, for
// the ports its consoles got.
func peripherals(d, live *libvirtxml.Domain, out *Devices) {
	out.Video, out.Graphics, out.TPMs, out.Sound, out.RNGs = []Video{}, []Graphics{}, []TPM{}, []string{}, []RNG{}
	if d.Devices == nil {
		return
	}
	out.Clipboard = hasClipboard(d)
	for _, v := range d.Devices.Videos {
		out.Video = append(out.Video, Video{Model: cmpOr(v.Model.Type, "default"), Heads: v.Model.Heads, Primary: v.Model.Primary == "yes"})
	}
	for i, g := range d.Devices.Graphics {
		x := graphicsOf(g)
		x.Port = 0
		if live != nil && live.Devices != nil && i < len(live.Devices.Graphics) {
			if l := graphicsOf(live.Devices.Graphics[i]); l.Type == x.Type {
				x.Port = l.Port
			}
		}
		out.Graphics = append(out.Graphics, x)
	}
	for _, t := range d.Devices.TPMs {
		x := TPM{Model: cmpOr(t.Model, "default")}
		if b := t.Backend; b != nil {
			switch {
			case b.Emulator != nil:
				x.Backend, x.Version = "emulator", b.Emulator.Version
			case b.Passthrough != nil:
				x.Backend = "passthrough"
			case b.External != nil:
				x.Backend = "external"
			}
		}
		out.TPMs = append(out.TPMs, x)
	}
	for _, s := range d.Devices.Sounds {
		out.Sound = append(out.Sound, s.Model)
	}
	for _, r := range d.Devices.RNGs {
		out.RNGs = append(out.RNGs, RNG{Model: r.Model, Backend: rngBackend(r)})
	}
}

func graphicsOf(g libvirtxml.DomainGraphic) Graphics {
	listen := func(attr string, ls []libvirtxml.DomainGraphicListener) string {
		for _, l := range ls {
			if l.Address != nil {
				return l.Address.Address
			}
		}
		return attr
	}
	switch {
	case g.VNC != nil:
		return Graphics{Type: "vnc", Listen: listen(g.VNC.Listen, g.VNC.Listeners), Port: max(g.VNC.Port, 0), AutoPort: g.VNC.AutoPort == "yes", Password: g.VNC.Passwd != ""}
	case g.Spice != nil:
		return Graphics{Type: "spice", Listen: listen(g.Spice.Listen, g.Spice.Listeners), Port: max(g.Spice.Port, 0), AutoPort: g.Spice.AutoPort == "yes", Password: g.Spice.Passwd != ""}
	case g.SDL != nil:
		return Graphics{Type: "sdl"}
	case g.RDP != nil:
		return Graphics{Type: "rdp", Port: max(g.RDP.Port, 0)}
	case g.Desktop != nil:
		return Graphics{Type: "desktop"}
	case g.EGLHeadless != nil:
		return Graphics{Type: "egl-headless"}
	case g.DBus != nil:
		return Graphics{Type: "dbus"}
	}
	return Graphics{Type: "other"}
}

func rngBackend(r libvirtxml.DomainRNG) string {
	b := r.Backend
	switch {
	case b == nil:
		return ""
	case b.Random != nil:
		return cmpOr(b.Random.Device, "/dev/random")
	case b.BuiltIn != nil:
		return "builtin"
	case b.EGD != nil:
		return "egd"
	}
	return "other"
}

// devCaps is what the machine's domain capabilities say of one kind of
// device; nil when libvirt doesn't say (an older libvirt, another driver).
func (e *edit) devCaps(kind func(*libvirtxml.DomainCapsDevices) *libvirtxml.DomainCapsDevice) (*libvirtxml.DomainCapsDevice, error) {
	dc, err := domainCaps(e.lv, e.def)
	if err != nil {
		return nil, err
	}
	if dc.Devices == nil {
		return nil, nil
	}
	return kind(dc.Devices), nil
}

// offers fails when the domain capabilities list the values of enum and
// value isn't one of them.
func offers(c *libvirtxml.DomainCapsDevice, enumName, value, what string) error {
	if c == nil {
		return nil
	}
	if c.Supported != "yes" {
		return fmt.Errorf("the host can't give this machine a %s", what)
	}
	if vs := enum(c.Enums, enumName); len(vs) > 0 && !slices.Contains(vs, value) {
		return fmt.Errorf("the host's hypervisor has no %s %s for this machine (it has %s)", what, value, strings.Join(vs, ", "))
	}
	return nil
}

func (e *edit) devices() *libvirtxml.DomainDeviceList {
	if e.def.Devices == nil {
		e.def.Devices = &libvirtxml.DomainDeviceList{}
	}
	return e.def.Devices
}

func (e *edit) q35() bool {
	return e.def.OS != nil && e.def.OS.Type != nil && strings.Contains(e.def.OS.Type.Machine, "q35")
}

// VideoModels are the video models virt-meow offers.
var VideoModels = []string{"virtio", "vga", "qxl", "bochs", "ramfb", "none"}

// SetVideo gives the machine the video model: its primary video device is
// replaced (the others go too when model is none, a machine without a
// display). It takes effect at the next start.
func SetVideo(ctx context.Context, sess *conn.Session, uuid, model string) (Edited, error) {
	if !slices.Contains(VideoModels, model) {
		return Edited{}, fmt.Errorf("video %q: %s", model, strings.Join(VideoModels, ", "))
	}
	return modify(ctx, sess, uuid, func(e *edit) error {
		dev := e.devices()
		was := "none"
		if len(dev.Videos) > 0 {
			was = cmpOr(dev.Videos[0].Model.Type, "default")
		}
		if was == model && (model != "none" || len(dev.Videos) <= 1) {
			return nil
		}
		c, err := e.devCaps(func(d *libvirtxml.DomainCapsDevices) *libvirtxml.DomainCapsDevice { return d.Video })
		if err != nil {
			return err
		}
		if err := offers(c, "modelType", model, "video model"); err != nil {
			return err
		}
		v := libvirtxml.DomainVideo{Model: libvirtxml.DomainVideoModel{Type: model}}
		switch {
		// none is a machine without a display; ramfb, a framebuffer in
		// guest memory rather than a PCI device, can't sit next to others.
		case model == "none", model == "ramfb", len(dev.Videos) == 0, dev.Videos[0].Model.Type == "none":
			dev.Videos = []libvirtxml.DomainVideo{v}
		default:
			if len(dev.Videos) > 1 {
				v.Model.Primary = "yes"
			}
			dev.Videos[0] = v
		}
		e.changed(fmt.Sprintf("video %s → %s", was, model), nil)
		return nil
	})
}

// SoundModels are the sound models virt-meow offers; libvirt checks them
// against the hypervisor.
var SoundModels = []string{"ich9", "ich6", "ich7", "ac97", "usb", "virtio", "none"}

// SetSound gives the machine one sound device of the model, or none. It
// takes effect at the next start.
func SetSound(ctx context.Context, sess *conn.Session, uuid, model string) (Edited, error) {
	if !slices.Contains(SoundModels, model) {
		return Edited{}, fmt.Errorf("sound %q: %s", model, strings.Join(SoundModels, ", "))
	}
	return modify(ctx, sess, uuid, func(e *edit) error {
		dev := e.devices()
		was := "none"
		if len(dev.Sounds) > 0 {
			was = dev.Sounds[0].Model
		}
		if was == model && len(dev.Sounds) <= 1 {
			return nil
		}
		dev.Sounds = nil
		if model != "none" {
			dev.Sounds = []libvirtxml.DomainSound{{Model: model}}
		}
		e.changed(fmt.Sprintf("sound %s → %s", was, model), nil)
		return nil
	})
}

// checkListen reads a listen address: an IP address, 127.0.0.1 when empty.
// One that isn't the host's loopback is warned about: the console is then
// reachable, unencrypted and without a password, by anyone on the network.
func (e *edit) checkListen(kind, addr string) (string, error) {
	if addr == "" {
		return "127.0.0.1", nil
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return "", fmt.Errorf("listen %q: an IP address of the host (127.0.0.1 keeps the console on the host)", addr)
	}
	if !ip.IsLoopback() {
		e.out.Warnings = append(e.out.Warnings, fmt.Sprintf("%s listens on %s: its console is then open on the network, unencrypted and without a password; 127.0.0.1 keeps it on the host, where virt-meow reaches it over SSH", strings.ToUpper(kind), addr))
	}
	return addr, nil
}

func graphicKind(kind string) error {
	if kind != "vnc" && kind != "spice" {
		return fmt.Errorf("graphics %q: vnc or spice", kind)
	}
	return nil
}

// AddGraphics gives the machine a VNC or SPICE console listening on listen
// (127.0.0.1 when empty), on a port libvirt picks when it starts. It takes
// effect at the next start.
func AddGraphics(ctx context.Context, sess *conn.Session, uuid, kind, listen string) (Edited, error) {
	if err := graphicKind(kind); err != nil {
		return Edited{}, err
	}
	return modify(ctx, sess, uuid, func(e *edit) error {
		dev := e.devices()
		for _, g := range dev.Graphics {
			if graphicsOf(g).Type == kind {
				return fmt.Errorf("%s already has a %s console: change where it listens with set", e.def.Name, strings.ToUpper(kind))
			}
		}
		c, err := e.devCaps(func(d *libvirtxml.DomainCapsDevices) *libvirtxml.DomainCapsDevice { return d.Graphics })
		if err != nil {
			return err
		}
		if err := offers(c, "type", kind, "graphics type"); err != nil {
			return err
		}
		addr, err := e.checkListen(kind, listen)
		if err != nil {
			return err
		}
		ls := []libvirtxml.DomainGraphicListener{{Address: &libvirtxml.DomainGraphicListenerAddress{Address: addr}}}
		g := libvirtxml.DomainGraphic{VNC: &libvirtxml.DomainGraphicVNC{Port: -1, AutoPort: "yes", Listeners: ls}}
		if kind == "spice" {
			g = libvirtxml.DomainGraphic{Spice: &libvirtxml.DomainGraphicSpice{Port: -1, AutoPort: "yes", Listeners: ls}}
		}
		dev.Graphics = append(dev.Graphics, g)
		e.changed(fmt.Sprintf("%s console on %s", strings.ToUpper(kind), addr), nil)
		return nil
	})
}

// SetGraphicsListen moves the machine's VNC or SPICE console to listen. It
// takes effect at the next start.
func SetGraphicsListen(ctx context.Context, sess *conn.Session, uuid, kind, listen string) (Edited, error) {
	if err := graphicKind(kind); err != nil {
		return Edited{}, err
	}
	return modify(ctx, sess, uuid, func(e *edit) error {
		dev := e.devices()
		i := slices.IndexFunc(dev.Graphics, func(g libvirtxml.DomainGraphic) bool { return graphicsOf(g).Type == kind })
		if i < 0 {
			return fmt.Errorf("%s has no %s console", e.def.Name, strings.ToUpper(kind))
		}
		was := graphicsOf(dev.Graphics[i]).Listen
		addr, err := e.checkListen(kind, listen)
		if err != nil {
			return err
		}
		if addr == was {
			e.out.Warnings = nil
			return nil
		}
		ls := []libvirtxml.DomainGraphicListener{{Address: &libvirtxml.DomainGraphicListenerAddress{Address: addr}}}
		if g := dev.Graphics[i].VNC; g != nil {
			g.Listen, g.Listeners = "", ls
		} else if g := dev.Graphics[i].Spice; g != nil {
			g.Listen, g.Listeners = "", ls
		}
		e.changed(fmt.Sprintf("%s console listens on %s → %s", strings.ToUpper(kind), cmpOr(was, "the host's default"), addr), nil)
		return nil
	})
}

// vncPasswordMax is as much of a password as VNC's own authentication
// uses (its DES key); QEMU drops the rest.
const vncPasswordMax = 8

// SetGraphicsPassword sets the password the machine's VNC or SPICE console
// asks for, or clears it (""). A running machine's console takes a new one
// now (libvirt updates QEMU's), and its definition keeps it; a cleared one
// waits for the next start. A password keeps out who doesn't know it; it
// doesn't encrypt the console, which the warning says.
func SetGraphicsPassword(ctx context.Context, sess *conn.Session, uuid, kind, password string) (Edited, error) {
	if err := graphicKind(kind); err != nil {
		return Edited{}, err
	}
	if kind == "vnc" && len(password) > vncPasswordMax {
		return Edited{}, fmt.Errorf("a VNC password is at most %d characters: VNC's own authentication ignores the rest", vncPasswordMax)
	}
	if strings.ContainsAny(password, "\x00\n") {
		return Edited{}, errors.New("a console password can't hold a newline or NUL")
	}
	return modify(ctx, sess, uuid, func(e *edit) error {
		dev := e.devices()
		i := slices.IndexFunc(dev.Graphics, func(g libvirtxml.DomainGraphic) bool { return graphicsOf(g).Type == kind })
		if i < 0 {
			return fmt.Errorf("%s has no %s console", e.def.Name, strings.ToUpper(kind))
		}
		g := &dev.Graphics[i]
		pw := &g.VNC.Passwd
		if g.Spice != nil {
			pw = &g.Spice.Passwd
		}
		had := *pw != ""
		if *pw == password {
			return nil
		}
		*pw = password
		what := strings.ToUpper(kind) + " password set"
		switch {
		case password == "":
			what = strings.ToUpper(kind) + " password cleared: the console asks for none"
		case had:
			what = strings.ToUpper(kind) + " password changed"
		}
		if password != "" {
			e.out.Warnings = append(e.out.Warnings, fmt.Sprintf("a password doesn't encrypt %s: who can reach its port sees the screen and keystrokes as they pass, and VNC's own authentication is weak; keep it on 127.0.0.1, where virt-meow reaches it over SSH", strings.ToUpper(kind)))
		}
		// QEMU picks the console's authentication when it starts: one that
		// started with a password, cleared now, would let nobody in until
		// the next start, so a cleared password waits for it.
		if password == "" {
			if e.active {
				e.out.Warnings = append(e.out.Warnings, fmt.Sprintf("until %s's next start its running console still asks for the old password: QEMU can't drop a console's password while it runs", e.def.Name))
			}
			e.changed(what, nil)
			return nil
		}
		x, err := g.Marshal()
		if err != nil {
			return err
		}
		e.changed(what, func() error {
			err := e.lv.DomainUpdateDeviceFlags(e.dom, x, libvirt.DomainDeviceModifyFlags(libvirt.DomainAffectLive))
			if err != nil && strings.Contains(err.Error(), "Could not set password") {
				// A console QEMU started without a password can't take one.
				return errors.New("its running console started without a password and asks for none until then")
			}
			return err
		})
		return nil
	})
}

// RemoveGraphics removes the machine's VNC or SPICE console. It takes
// effect at the next start.
func RemoveGraphics(ctx context.Context, sess *conn.Session, uuid, kind string) (Edited, error) {
	if err := graphicKind(kind); err != nil {
		return Edited{}, err
	}
	return modify(ctx, sess, uuid, func(e *edit) error {
		dev := e.devices()
		n := len(dev.Graphics)
		dev.Graphics = slices.DeleteFunc(dev.Graphics, func(g libvirtxml.DomainGraphic) bool { return graphicsOf(g).Type == kind })
		if len(dev.Graphics) == n {
			return fmt.Errorf("%s has no %s console", e.def.Name, strings.ToUpper(kind))
		}
		e.changed(strings.ToUpper(kind)+" console removed", nil)
		return nil
	})
}

// AddTPM gives the machine an emulated TPM 2.0 (swtpm on the host): CRB on
// q35, TIS otherwise, unless model says. It takes effect at the next start.
func AddTPM(ctx context.Context, sess *conn.Session, uuid, model string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		dev := e.devices()
		if len(dev.TPMs) > 0 {
			return fmt.Errorf("%s already has a TPM", e.def.Name)
		}
		switch model {
		case "":
			model = "tpm-tis"
			if e.q35() {
				model = "tpm-crb"
			}
		case "crb", "tis":
			model = "tpm-" + model
		case "tpm-crb", "tpm-tis":
		default:
			return fmt.Errorf("TPM model %q: crb or tis", model)
		}
		c, err := e.devCaps(func(d *libvirtxml.DomainCapsDevices) *libvirtxml.DomainCapsDevice { return d.TPM })
		if err != nil {
			return err
		}
		for _, chk := range [][3]string{{"model", model, "TPM model"}, {"backendModel", "emulator", "TPM emulator"}, {"backendVersion", "2.0", "TPM version"}} {
			if err := offers(c, chk[0], chk[1], chk[2]); err != nil {
				if chk[0] == "backendModel" {
					return fmt.Errorf("%w (is swtpm installed on the host?)", err)
				}
				return err
			}
		}
		dev.TPMs = append(dev.TPMs, libvirtxml.DomainTPM{Model: model,
			Backend: &libvirtxml.DomainTPMBackend{Emulator: &libvirtxml.DomainTPMBackendEmulator{Version: "2.0"}}})
		e.changed(fmt.Sprintf("TPM 2.0 (%s, emulated)", model), nil)
		return nil
	})
}

// RemoveTPM removes the machine's TPM. Its emulator's state stays on the
// host until the machine is deleted. It takes effect at the next start.
func RemoveTPM(ctx context.Context, sess *conn.Session, uuid string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		dev := e.devices()
		if len(dev.TPMs) == 0 {
			return fmt.Errorf("%s has no TPM", e.def.Name)
		}
		dev.TPMs = nil
		e.changed("TPM removed", nil)
		return nil
	})
}

// AddRNG gives the machine a virtio RNG fed from the host's /dev/urandom;
// a running machine gets it now.
func AddRNG(ctx context.Context, sess *conn.Session, uuid string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		if len(e.devices().RNGs) > 0 {
			return fmt.Errorf("%s already has an RNG", e.def.Name)
		}
		c, err := e.devCaps(func(d *libvirtxml.DomainCapsDevices) *libvirtxml.DomainCapsDevice { return d.RNG })
		if err != nil {
			return err
		}
		if err := offers(c, "backendModel", "random", "RNG backend"); err != nil {
			return err
		}
		r := libvirtxml.DomainRNG{Model: "virtio",
			Backend: &libvirtxml.DomainRNGBackend{Random: &libvirtxml.DomainRNGBackendRandom{Device: "/dev/urandom"}}}
		return e.attach("RNG (virtio, /dev/urandom)", &r)
	})
}

// RemoveRNG removes the machine's RNG devices; a running machine is asked
// to let go of them now.
func RemoveRNG(ctx context.Context, sess *conn.Session, uuid string) (Edited, error) {
	return modify(ctx, sess, uuid, func(e *edit) error {
		rs := e.devices().RNGs
		if len(rs) == 0 {
			return errors.New(e.def.Name + " has no RNG")
		}
		for _, r := range rs {
			if err := e.detach("RNG ("+r.Model+")", &r); err != nil {
				return err
			}
		}
		return nil
	})
}
