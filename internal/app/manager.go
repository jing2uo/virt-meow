// Package app holds the open connections of one virt-mm process: the GUI's
// many, or the CLI's one. It is the service API's implementation
// (docs/adr/0001): plain methods with JSON types, which the Wails binding
// wraps and a server mode could serve.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	libvirt "github.com/digitalocean/go-libvirt"

	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
	"github.com/jing2uo/virt-meow/internal/store"
)

// ConnInfo is a connection as the front end shows it.
type ConnInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URI         string `json:"uri"`
	Remote      bool   `json:"remote"`
	Autoconnect bool   `json:"autoconnect"`
	Status      string `json:"status"` // conn.Status, or "idle" when never opened
	Error       string `json:"error,omitempty"`
	// HostKey is set while the host's key waits for the user's answer.
	HostKey *HostKeyPrompt `json:"host_key,omitempty"`
	// HostKeyChanged is set when the host offered a different key than the
	// one on record; it can't be accepted from here.
	HostKeyChanged bool `json:"host_key_changed,omitempty"`
}

// HostKeyPrompt is an unknown host key to show the user.
type HostKeyPrompt struct {
	Addr        string `json:"addr"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
}

// Callbacks tell the front end what changed. Each may be nil.
type Callbacks struct {
	OnConnection func(ConnInfo)               // a connection's state changed
	OnEvent      func(id string, ev hv.Event) // a machine's state changed
}

// Manager holds the saved connections and the sessions opened on them.
type Manager struct {
	cb       Callbacks
	hostKeys *conn.HostKeys
	prompter conn.Prompter
	secrets  conn.Secrets

	mu        sync.Mutex
	saved     []store.Connection
	open      map[string]*entry
	listeners map[string]map[*listener]bool // Events' callers, by connection id

	stats   *shared[hv.Sample, hv.Stats]
	hostUse *shared[hv.HostSample, hv.HostUsage]
	osdbs   map[*conn.Session]*hv.OSDB // each session's osinfo-db, read once
}

type listener struct{ fn func(hv.Event) }

type entry struct {
	sess    *conn.Session
	pending *conn.UnknownHostKeyError // host key waiting for an answer
}

// NewManager loads the saved connections. prompter may be nil.
func NewManager(cb Callbacks, prompter conn.Prompter) (*Manager, error) {
	saved, err := store.Connections()
	if err != nil {
		return nil, err
	}
	m := &Manager{
		cb:        cb,
		hostKeys:  &conn.HostKeys{Own: store.KnownHosts(), Extra: store.SystemKnownHosts()},
		prompter:  prompter,
		secrets:   conn.SystemSecrets(),
		saved:     saved,
		open:      map[string]*entry{},
		listeners: map[string]map[*listener]bool{},
		osdbs:     map[*conn.Session]*hv.OSDB{},
	}
	m.stats = newShared(func(ctx context.Context, id string) (hv.Sample, error) { return m.Sample(ctx, id, "") }, hv.Rates)
	m.hostUse = newShared(m.HostSample, hv.HostRates)
	return m, nil
}

// HostKeys is the host-key store sessions use.
func (m *Manager) HostKeys() *conn.HostKeys { return m.hostKeys }

// Connections lists the saved connections with their state.
func (m *Manager) Connections() []ConnInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ConnInfo, 0, len(m.saved))
	for _, c := range m.saved {
		out = append(out, m.infoLocked(c))
	}
	return out
}

// Add saves a new connection after checking its URI. name may be empty.
func (m *Manager) Add(name, uri string, autoconnect bool) (ConnInfo, error) {
	t, err := conn.Parse(uri)
	if err != nil {
		return ConnInfo{}, err
	}
	if name = strings.TrimSpace(name); name == "" {
		name = t.String()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.saved {
		if c.URI == uri {
			return ConnInfo{}, fmt.Errorf("%s is already saved as %q", uri, c.Name)
		}
	}
	c := store.Connection{ID: store.NewID(), Name: name, URI: uri, Autoconnect: autoconnect}
	saved := append(append([]store.Connection{}, m.saved...), c)
	if err := store.SaveConnections(saved); err != nil {
		return ConnInfo{}, err
	}
	m.saved = saved
	return m.infoLocked(c), nil
}

// Remove closes and forgets a connection.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	var saved []store.Connection
	found := false
	for _, c := range m.saved {
		if c.ID == id {
			found = true
			continue
		}
		saved = append(saved, c)
	}
	if !found {
		m.mu.Unlock()
		return fmt.Errorf("no connection %q", id)
	}
	if saved == nil {
		saved = []store.Connection{}
	}
	if err := store.SaveConnections(saved); err != nil {
		m.mu.Unlock()
		return err
	}
	m.saved = saved
	e := m.open[id]
	delete(m.open, id)
	m.mu.Unlock()
	m.stats.forget(id)
	m.hostUse.forget(id)
	if e != nil {
		e.sess.Close()
	}
	return nil
}

// Find returns a saved connection by id, name or URI.
func (m *Manager) Find(ref string) (store.Connection, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return store.Find(m.saved, ref)
}

// SSHRoute is how a connection (saved, by id, name or URI; or a URI) is
// reached over SSH after ~/.ssh/config and the system's ssh_config: its
// jump hosts, then the host. Empty for a local one.
func (m *Manager) SSHRoute(ref string) ([]conn.Hop, error) {
	uri := ref
	if c, ok := m.Find(ref); ok {
		uri = c.URI
	}
	t, err := conn.Parse(uri)
	if err != nil {
		return nil, err
	}
	r, err := conn.Route(t)
	if r == nil {
		r = []conn.Hop{}
	}
	return r, err
}

// ForgetSecrets forgets what the keychain remembers for reaching a
// connection (saved, or a URI): the password of each SSH hop, and the
// passphrase of each of their keys (shared with other connections using
// the same key). It returns what was forgotten.
func (m *Manager) ForgetSecrets(ctx context.Context, ref string) ([]conn.Secret, error) {
	uri := ref
	if c, ok := m.Find(ref); ok {
		uri = c.URI
	}
	t, err := conn.Parse(uri)
	if err != nil {
		return nil, err
	}
	return conn.Forget(ctx, m.secrets, t)
}

// Session returns the session of a saved connection, creating it (not yet
// connected) on first use.
func (m *Manager) Session(id string) (*conn.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.open[id]; ok {
		return e.sess, nil
	}
	c, ok := store.Find(m.saved, id)
	if !ok {
		return nil, fmt.Errorf("no connection %q", id)
	}
	return m.sessionLocked(c)
}

// SessionFor opens a session on a URI that isn't saved (the CLI's -c).
func (m *Manager) SessionFor(uri string) (*conn.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := store.Find(m.saved, uri); ok {
		return m.sessionLocked(c)
	}
	return m.sessionLocked(store.Connection{ID: "uri:" + uri, Name: uri, URI: uri})
}

func (m *Manager) sessionLocked(c store.Connection) (*conn.Session, error) {
	if e, ok := m.open[c.ID]; ok {
		return e.sess, nil
	}
	t, err := conn.Parse(c.URI)
	if err != nil {
		return nil, err
	}
	id := c.ID
	e := &entry{}
	e.sess = conn.New(t, conn.Options{
		HostKeys: m.hostKeys,
		Prompter: m.prompter,
		Secrets:  m.secrets,
		OnState:  func(st conn.State) { m.stateChanged(id, st) },
		OnLifecycle: func(ev libvirt.DomainEventLifecycleMsg) {
			e := hv.EventOf(ev)
			if m.cb.OnEvent != nil {
				m.cb.OnEvent(id, e)
			}
			m.mu.Lock()
			ls := make([]*listener, 0, len(m.listeners[id]))
			for l := range m.listeners[id] {
				ls = append(ls, l)
			}
			m.mu.Unlock()
			for _, l := range ls {
				l.fn(e)
			}
		},
	})
	m.open[id] = e
	return e.sess, nil
}

func (m *Manager) stateChanged(id string, st conn.State) {
	m.mu.Lock()
	e := m.open[id]
	if e != nil {
		var unknown *conn.UnknownHostKeyError
		if errors.As(st.Err, &unknown) {
			e.pending = unknown
		} else if st.Status == conn.Connected {
			e.pending = nil
		}
	}
	c, ok := store.Find(m.saved, id)
	var info ConnInfo
	if ok {
		info = m.infoLocked(c)
	}
	m.mu.Unlock()
	if ok && m.cb.OnConnection != nil {
		m.cb.OnConnection(info)
	}
}

// Connect opens a connection now.
func (m *Manager) Connect(ctx context.Context, id string) error {
	s, err := m.Session(id)
	if err != nil {
		return err
	}
	return s.Connect(ctx)
}

// TrustHostKey records the key the host offered, if it is still the one with
// the given fingerprint (what the user saw), then connects.
func (m *Manager) TrustHostKey(ctx context.Context, id, fingerprint string) error {
	m.mu.Lock()
	e := m.open[id]
	var p *conn.UnknownHostKeyError
	if e != nil {
		p = e.pending
	}
	m.mu.Unlock()
	if p == nil {
		return fmt.Errorf("no host key is waiting for an answer on this connection")
	}
	if p.Fingerprint != fingerprint {
		return fmt.Errorf("the host key changed while waiting (now %s): connect again to see it", p.Fingerprint)
	}
	if err := m.hostKeys.Trust(p.Addr, p.Key); err != nil {
		return err
	}
	m.mu.Lock()
	e.pending = nil
	m.mu.Unlock()
	return e.sess.Connect(ctx)
}

// Events calls fn with each lifecycle event of the machines on connection
// id, from when it returns until stop is called. It connects first, so
// that nothing after it is missed; a connection lost later is made again,
// and events while it was lost are missed.
func (m *Manager) Events(ctx context.Context, id string, fn func(hv.Event)) (stop func(), err error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	l := &listener{fn: fn}
	m.mu.Lock()
	if m.listeners[id] == nil {
		m.listeners[id] = map[*listener]bool{}
	}
	m.listeners[id][l] = true
	m.mu.Unlock()
	stop = func() {
		m.mu.Lock()
		delete(m.listeners[id], l)
		m.mu.Unlock()
	}
	if err := s.Connect(ctx); err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

// Domains lists the machines of a connection.
func (m *Manager) Domains(ctx context.Context, id string) ([]hv.Domain, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	doms, err := hv.List(ctx, s)
	if doms == nil && err == nil {
		doms = []hv.Domain{}
	}
	return doms, err
}

// Power applies a power action to a machine.
func (m *Manager) Power(ctx context.Context, id, uuid, action string) error {
	a, err := hv.ParseAction(action)
	if err != nil {
		return err
	}
	s, err := m.Session(id)
	if err != nil {
		return err
	}
	return hv.Power(ctx, s, uuid, a)
}

// Save stops a running machine with its memory saved on the host; the
// next start resumes it.
func (m *Manager) Save(ctx context.Context, id, uuid string) error {
	s, err := m.Session(id)
	if err != nil {
		return err
	}
	return hv.Save(ctx, s, uuid)
}

// DropSave removes a machine's saved state, reporting whether it had one.
func (m *Manager) DropSave(ctx context.Context, id, uuid string) (bool, error) {
	s, err := m.Session(id)
	if err != nil {
		return false, err
	}
	return hv.DropSave(ctx, s, uuid)
}

// AddFilesystem shares a folder of the host with a machine (virtiofs).
func (m *Manager) AddFilesystem(ctx context.Context, id, uuid, dir, tag string) (hv.Edited, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Edited{}, err
	}
	return hv.AddFilesystem(ctx, s, uuid, dir, tag)
}

// RemoveFilesystem stops sharing a folder with a machine.
func (m *Manager) RemoveFilesystem(ctx context.Context, id, uuid, tag string) (hv.Edited, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Edited{}, err
	}
	return hv.RemoveFilesystem(ctx, s, uuid, tag)
}

// AttachHostDevice gives a machine a USB or PCI device of the host.
func (m *Manager) AttachHostDevice(ctx context.Context, id, uuid, ref string) (hv.Edited, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Edited{}, err
	}
	return hv.AttachHostDevice(ctx, s, uuid, ref)
}

// DetachHostDevice takes a host device from a machine.
func (m *Manager) DetachHostDevice(ctx context.Context, id, uuid, ref string) (hv.Edited, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Edited{}, err
	}
	return hv.DetachHostDevice(ctx, s, uuid, ref)
}

// HostDevices lists the host's PCI and USB devices and the machines that
// have each.
func (m *Manager) HostDevices(ctx context.Context, id string) ([]hv.HostDevice, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	return hv.HostDevices(ctx, s)
}

// HostDeviceXML is one host device's description as libvirt has it.
func (m *Manager) HostDeviceXML(ctx context.Context, id, name string) (string, error) {
	s, err := m.Session(id)
	if err != nil {
		return "", err
	}
	return hv.HostDeviceXML(ctx, s, name)
}

// Close closes every open session.
func (m *Manager) Close() {
	m.mu.Lock()
	open := m.open
	m.open = map[string]*entry{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range open {
		wg.Add(1)
		go func() { defer wg.Done(); e.sess.Close() }()
	}
	wg.Wait()
}

func (m *Manager) infoLocked(c store.Connection) ConnInfo {
	info := ConnInfo{ID: c.ID, Name: c.Name, URI: c.URI, Autoconnect: c.Autoconnect, Status: "idle"}
	if t, err := conn.Parse(c.URI); err == nil {
		info.Remote = t.Remote()
	}
	e, ok := m.open[c.ID]
	if !ok {
		return info
	}
	st := e.sess.State()
	info.Status = string(st.Status)
	if st.Err != nil {
		info.Error = st.Err.Error()
	}
	var changed *conn.HostKeyChangedError
	if errors.As(st.Err, &changed) {
		info.HostKeyChanged = true
	}
	if e.pending != nil {
		info.HostKey = &HostKeyPrompt{Addr: e.pending.Addr, Type: e.pending.Type, Fingerprint: e.pending.Fingerprint}
	}
	return info
}

// CreateOptions is what the creation form offers on a connection.
type CreateOptions struct {
	Host     hv.Host      `json:"host"`
	Pools    []hv.Pool    `json:"pools"`
	ISOs     []hv.Volume  `json:"isos"`  // installation media in every active pool
	Disks    []hv.Volume  `json:"disks"` // other volumes, to boot an existing disk
	Networks []hv.Network `json:"networks"`
	Bridges  []string     `json:"bridges"`
	// Defaults are the user's for new machines (the settings).
	Defaults store.NewMachine `json:"defaults"`
}

// CreateOptions gathers what the creation form needs, in one call.
func (m *Manager) CreateOptions(ctx context.Context, id string) (CreateOptions, error) {
	s, err := m.Session(id)
	if err != nil {
		return CreateOptions{}, err
	}
	var o CreateOptions
	if o.Host, err = hv.HostInfo(ctx, s); err != nil {
		return o, err
	}
	if o.Pools, err = hv.Pools(ctx, s); err != nil {
		return o, err
	}
	o.ISOs, o.Disks = []hv.Volume{}, []hv.Volume{}
	for _, p := range o.Pools {
		if !p.Active {
			continue
		}
		vols, err := hv.Volumes(ctx, s, p.Name)
		if err != nil {
			return o, err
		}
		for _, v := range vols {
			switch {
			case v.IsISO():
				o.ISOs = append(o.ISOs, v)
			case v.Type == "file" || v.Type == "block":
				o.Disks = append(o.Disks, v)
			}
		}
	}
	if o.Networks, err = hv.Networks(ctx, s); err != nil {
		return o, err
	}
	if o.Bridges, err = hv.Bridges(ctx, s); err != nil {
		return o, err
	}
	if o.Bridges == nil {
		o.Bridges = []string{}
	}
	if st, err := store.LoadSettings(); err == nil {
		o.Defaults = st.NewMachine
	}
	return o, nil
}

// OSList is what osinfo-db knows, for the creation form.
type OSList struct {
	Source   string         `json:"source"` // host: the host's own osinfo-db; virt-mm: the copy it ships
	Version  string         `json:"version"`
	Note     string         `json:"note,omitempty"`
	Variants []hv.OSVariant `json:"variants"`
}

// osdb is a connection's osinfo-db, read the first time it is asked for.
func (m *Manager) osdb(ctx context.Context, id string) (*conn.Session, *hv.OSDB, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	db := m.osdbs[s]
	m.mu.Unlock()
	if db != nil {
		return s, db, nil
	}
	if db, err = hv.LoadOSDB(ctx, s); err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	m.osdbs[s] = db
	m.mu.Unlock()
	return s, db, nil
}

// OSVariants lists the operating systems osinfo-db knows, each with what
// a new machine running it on this host should get.
func (m *Manager) OSVariants(ctx context.Context, id string) (OSList, error) {
	s, db, err := m.osdb(ctx, id)
	if err != nil {
		return OSList{}, err
	}
	h, err := hv.HostInfo(ctx, s)
	if err != nil {
		return OSList{}, err
	}
	return OSList{Source: db.Source, Version: db.Version, Note: db.Note, Variants: db.Variants(h)}, nil
}

// OSVariant finds one operating system by its short ID or ID, with its
// proposal for this host.
func (m *Manager) OSVariant(ctx context.Context, id, name string) (hv.OSVariant, error) {
	s, db, err := m.osdb(ctx, id)
	if err != nil {
		return hv.OSVariant{}, err
	}
	v := db.Lookup(name)
	if v == nil {
		return hv.OSVariant{}, fmt.Errorf("osinfo-db (%s %s) has no OS %q: virt-mm osinfo lists them", db.Source, db.Version, name)
	}
	h, err := hv.HostInfo(ctx, s)
	if err != nil {
		return hv.OSVariant{}, err
	}
	return v.For(h), nil
}

// DetectOS recognises the system on an ISO (its volume ID) or a disk
// image (its name).
func (m *Manager) DetectOS(ctx context.Context, id, pool, vol string) (hv.Detected, error) {
	s, db, err := m.osdb(ctx, id)
	if err != nil {
		return hv.Detected{}, err
	}
	h, err := hv.HostInfo(ctx, s)
	if err != nil {
		return hv.Detected{}, err
	}
	return hv.DetectOS(ctx, s, db, h, pool, vol)
}

// Preview is the definition a new machine would get, to edit before
// creating it.
func (m *Manager) Preview(ctx context.Context, id string, spec hv.Spec) (string, error) {
	s, err := m.Session(id)
	if err != nil {
		return "", err
	}
	return hv.Preview(ctx, s, spec)
}

// Create makes a new machine; xml, when set, is its edited definition.
func (m *Manager) Create(ctx context.Context, id string, spec hv.Spec, xml string) (hv.Created, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Created{}, err
	}
	return hv.Create(ctx, s, spec, xml)
}

// Definition reads a machine's persistent definition.
func (m *Manager) Definition(ctx context.Context, id, uuid string) (hv.Definition, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Definition{}, err
	}
	return hv.GetDefinition(ctx, s, uuid)
}

// Set changes a machine's settings.
func (m *Manager) Set(ctx context.Context, id, uuid string, s hv.Settings) (hv.Edited, error) {
	sess, err := m.Session(id)
	if err != nil {
		return hv.Edited{}, err
	}
	return hv.Set(ctx, sess, uuid, s)
}

// CPUModels lists the CPU models a machine can be given.
func (m *Manager) CPUModels(ctx context.Context, id, uuid string) (hv.CPUModels, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.CPUModels{}, err
	}
	return hv.GetCPUModels(ctx, s, uuid)
}

// Rename renames a machine that is off.
func (m *Manager) Rename(ctx context.Context, id, uuid, name string) (hv.Edited, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Edited{}, err
	}
	return hv.Rename(ctx, s, uuid, name)
}

// Clone copies a machine that is off, its disks and NVRAM with it, as a
// new machine named name. ctx should carry no short deadline: the disks are
// copied in the call.
func (m *Manager) Clone(ctx context.Context, id, uuid, name string) (hv.Cloned, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Cloned{}, err
	}
	return hv.Clone(ctx, s, uuid, name)
}

// Define makes a new machine from a definition.
func (m *Manager) Define(ctx context.Context, id, xml string) (hv.Definition, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Definition{}, err
	}
	return hv.Define(ctx, s, xml)
}

// Info reads a machine in detail, with its guest's addresses.
func (m *Manager) Info(ctx context.Context, id, uuid string) (hv.Info, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Info{}, err
	}
	return hv.GetInfo(ctx, s, uuid)
}

// Host is what the host is and what it can run.
func (m *Manager) Host(ctx context.Context, id string) (hv.Host, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Host{}, err
	}
	return hv.HostInfo(ctx, s)
}

// HostSample reads the host's CPU and memory counters and how many
// machines it runs; hv.HostRates makes its use of two.
func (m *Manager) HostSample(ctx context.Context, id string) (hv.HostSample, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.HostSample{}, err
	}
	return hv.GetHostSample(ctx, s)
}

// Sample reads the counters of every running machine, or of the machine
// uuid when it isn't "", in one call; hv.Rates makes rates of two.
func (m *Manager) Sample(ctx context.Context, id, uuid string) (hv.Sample, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Sample{}, err
	}
	return hv.GetSample(ctx, s, uuid)
}

// Console connects in and out to a running machine's serial console until
// in ends, the machine stops or ctx ends.
func (m *Manager) Console(ctx context.Context, id, uuid, dev string, force bool, in io.Reader, out io.Writer) error {
	s, err := m.Session(id)
	if err != nil {
		return err
	}
	return hv.Console(ctx, s, uuid, dev, force, in, out)
}

// FindGraphics reads where a running machine's console of kind (vnc or
// spice) listens on its host.
func (m *Manager) FindGraphics(ctx context.Context, id, uuid, kind string) (hv.GraphicsServer, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.GraphicsServer{}, err
	}
	return hv.FindGraphics(ctx, s, uuid, kind)
}

// DialGraphics connects to a running machine's VNC or SPICE server, over
// the host's SSH connection when it is remote.
func (m *Manager) DialGraphics(ctx context.Context, id, uuid, kind string) (net.Conn, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	c, _, err := hv.DialGraphics(ctx, s, uuid, kind)
	return c, err
}

// SendKeys presses each combination of keys ("ctrl+alt+delete") in turn on
// a running machine's keyboard.
func (m *Manager) SendKeys(ctx context.Context, id, uuid string, combos []string, holdMs uint32) error {
	s, err := m.Session(id)
	if err != nil {
		return err
	}
	return hv.SendKeys(ctx, s, uuid, combos, holdMs)
}

// Screenshot is what a running machine's display screen shows, as PNG.
func (m *Manager) Screenshot(ctx context.Context, id, uuid string, screen uint32) (hv.Screenshot, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Screenshot{}, err
	}
	return hv.TakeScreenshot(ctx, s, uuid, screen)
}

// Thumbnail is a running machine's first screen scaled down to width.
func (m *Manager) Thumbnail(ctx context.Context, id, uuid string, width int) (hv.Screenshot, error) {
	shot, err := m.Screenshot(ctx, id, uuid, 0)
	if err != nil {
		return hv.Screenshot{}, err
	}
	return hv.Thumbnail(shot, width)
}

// SetUserPassword sets a user's password in a running guest, through its
// agent.
func (m *Manager) SetUserPassword(ctx context.Context, id, uuid, user, password string) error {
	s, err := m.Session(id)
	if err != nil {
		return err
	}
	return hv.SetUserPassword(ctx, s, uuid, user, password)
}

// SSHKeys lists the SSH keys a user of a running guest accepts.
func (m *Manager) SSHKeys(ctx context.Context, id, uuid, user string) ([]string, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	return hv.SSHKeys(ctx, s, uuid, user)
}

// SetSSHKeys adds keys to, or with remove removes them from, those a user
// of a running guest accepts.
func (m *Manager) SetSSHKeys(ctx context.Context, id, uuid, user string, keys []string, remove bool) error {
	s, err := m.Session(id)
	if err != nil {
		return err
	}
	if remove {
		return hv.RemoveSSHKeys(ctx, s, uuid, user, keys)
	}
	return hv.AddSSHKeys(ctx, s, uuid, user, keys)
}

// Devices lists a machine's devices.
func (m *Manager) Devices(ctx context.Context, id, uuid string) (hv.Devices, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Devices{}, err
	}
	return hv.GetDevices(ctx, s, uuid)
}

// editOn runs an hv edit on connection id.
func (m *Manager) editOn(id string, f func(*conn.Session) (hv.Edited, error)) (hv.Edited, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Edited{}, err
	}
	return f(s)
}

func (m *Manager) AddDisk(ctx context.Context, id, uuid string, d hv.NewDisk) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.AddDisk(ctx, s, uuid, d) })
}

func (m *Manager) RemoveDisk(ctx context.Context, id, uuid, target string, deleteVolume bool) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.RemoveDisk(ctx, s, uuid, target, deleteVolume) })
}

func (m *Manager) ResizeDisk(ctx context.Context, id, uuid, target string, bytes uint64) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.ResizeDisk(ctx, s, uuid, target, bytes) })
}

func (m *Manager) InsertCD(ctx context.Context, id, uuid, target, pool, volume string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.InsertCD(ctx, s, uuid, target, pool, volume) })
}

func (m *Manager) EjectCD(ctx context.Context, id, uuid, target string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.EjectCD(ctx, s, uuid, target) })
}

func (m *Manager) AddNIC(ctx context.Context, id, uuid string, n hv.NewNIC) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.AddNIC(ctx, s, uuid, n) })
}

func (m *Manager) RemoveNIC(ctx context.Context, id, uuid, mac string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.RemoveNIC(ctx, s, uuid, mac) })
}

func (m *Manager) SetLink(ctx context.Context, id, uuid, mac string, up bool) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.SetLink(ctx, s, uuid, mac, up) })
}

func (m *Manager) SetBoot(ctx context.Context, id, uuid string, devs []string, menu *bool) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.SetBoot(ctx, s, uuid, devs, menu) })
}

func (m *Manager) SetVideo(ctx context.Context, id, uuid, model string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.SetVideo(ctx, s, uuid, model) })
}

func (m *Manager) SetSound(ctx context.Context, id, uuid, model string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.SetSound(ctx, s, uuid, model) })
}

func (m *Manager) AddGraphics(ctx context.Context, id, uuid, kind, listen string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.AddGraphics(ctx, s, uuid, kind, listen) })
}

func (m *Manager) SetGraphicsListen(ctx context.Context, id, uuid, kind, listen string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.SetGraphicsListen(ctx, s, uuid, kind, listen) })
}

func (m *Manager) AddClipboard(ctx context.Context, id, uuid string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.AddClipboard(ctx, s, uuid) })
}

func (m *Manager) RemoveClipboard(ctx context.Context, id, uuid string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.RemoveClipboard(ctx, s, uuid) })
}

func (m *Manager) SetGraphicsPassword(ctx context.Context, id, uuid, kind, password string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.SetGraphicsPassword(ctx, s, uuid, kind, password) })
}

func (m *Manager) RemoveGraphics(ctx context.Context, id, uuid, kind string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.RemoveGraphics(ctx, s, uuid, kind) })
}

func (m *Manager) AddTPM(ctx context.Context, id, uuid, model string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.AddTPM(ctx, s, uuid, model) })
}

func (m *Manager) RemoveTPM(ctx context.Context, id, uuid string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.RemoveTPM(ctx, s, uuid) })
}

func (m *Manager) AddRNG(ctx context.Context, id, uuid string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.AddRNG(ctx, s, uuid) })
}

func (m *Manager) RemoveRNG(ctx context.Context, id, uuid string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.RemoveRNG(ctx, s, uuid) })
}

// AttachDevice adds a device, given as its XML, to a machine.
func (m *Manager) AttachDevice(ctx context.Context, id, uuid, xml string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.AttachDevice(ctx, s, uuid, xml) })
}

// DetachDevice removes the device its XML describes from a machine.
func (m *Manager) DetachDevice(ctx context.Context, id, uuid, xml string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.DetachDevice(ctx, s, uuid, xml) })
}

// Snapshots lists a machine's snapshots as a tree, depth first.
func (m *Manager) Snapshots(ctx context.Context, id, uuid string) ([]hv.Snapshot, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	return hv.Snapshots(ctx, s, uuid)
}

func (m *Manager) CreateSnapshot(ctx context.Context, id, uuid, name, description string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.CreateSnapshot(ctx, s, uuid, name, description) })
}

func (m *Manager) RevertSnapshot(ctx context.Context, id, uuid, name string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.RevertSnapshot(ctx, s, uuid, name) })
}

func (m *Manager) DeleteSnapshot(ctx context.Context, id, uuid, name string, children bool) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.DeleteSnapshot(ctx, s, uuid, name, children) })
}

// Redefine saves an edited definition; base is the one the edit started from.
func (m *Manager) Redefine(ctx context.Context, id, uuid, base, xml string) (hv.Definition, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Definition{}, err
	}
	return hv.Redefine(ctx, s, uuid, base, xml)
}

// Delete removes a machine, and its disks if asked.
func (m *Manager) Delete(ctx context.Context, id, uuid string, disks bool) ([]string, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	return hv.Delete(ctx, s, uuid, disks)
}

func (m *Manager) AddPool(ctx context.Context, id string, p hv.NewPool) (hv.Pool, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Pool{}, err
	}
	return hv.AddPool(ctx, s, p)
}

func (m *Manager) SetPool(ctx context.Context, id, name string, a hv.PoolAction) (hv.Pool, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Pool{}, err
	}
	return hv.SetPool(ctx, s, name, a)
}

func (m *Manager) RemovePool(ctx context.Context, id, name string) error {
	s, err := m.Session(id)
	if err != nil {
		return err
	}
	return hv.RemovePool(ctx, s, name)
}

// Networks lists the virtual networks.
func (m *Manager) Networks(ctx context.Context, id string) ([]hv.Network, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	return hv.Networks(ctx, s)
}

// Bridges lists the host's bridges, for a network that joins one.
func (m *Manager) Bridges(ctx context.Context, id string) ([]string, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	return hv.Bridges(ctx, s)
}

func (m *Manager) AddNetwork(ctx context.Context, id string, n hv.NewNetwork) (hv.Network, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Network{}, err
	}
	return hv.AddNetwork(ctx, s, n)
}

// SetNetwork starts or stops a network, or sets its autostart; stopping one
// that running machines are on needs force.
func (m *Manager) SetNetwork(ctx context.Context, id, name string, a hv.NetworkAction, force bool) (hv.Network, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Network{}, err
	}
	return hv.SetNetwork(ctx, s, name, a, force)
}

func (m *Manager) RemoveNetwork(ctx context.Context, id, name string, force bool) error {
	s, err := m.Session(id)
	if err != nil {
		return err
	}
	return hv.RemoveNetwork(ctx, s, name, force)
}

// NetworkDefinition reads a network's persistent definition.
func (m *Manager) NetworkDefinition(ctx context.Context, id, name string) (hv.NetworkDefinition, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.NetworkDefinition{}, err
	}
	return hv.GetNetworkDefinition(ctx, s, name)
}

// RedefineNetwork saves an edited network definition; base is the one the
// edit started from.
func (m *Manager) RedefineNetwork(ctx context.Context, id, name, base, xml string) (hv.NetworkDefinition, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.NetworkDefinition{}, err
	}
	return hv.RedefineNetwork(ctx, s, name, base, xml)
}

// DHCPHosts lists a network's fixed addresses.
func (m *Manager) DHCPHosts(ctx context.Context, id, name string) ([]hv.DHCPHost, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	return hv.DHCPHosts(ctx, s, name)
}

func (m *Manager) AddDHCPHost(ctx context.Context, id, network, mac, ip, name string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.AddDHCPHost(ctx, s, network, mac, ip, name) })
}

func (m *Manager) RemoveDHCPHost(ctx context.Context, id, network, mac string) (hv.Edited, error) {
	return m.editOn(id, func(s *conn.Session) (hv.Edited, error) { return hv.RemoveDHCPHost(ctx, s, network, mac) })
}

func (m *Manager) Leases(ctx context.Context, id, name string) ([]hv.Lease, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	return hv.Leases(ctx, s, name)
}

// Pools lists the storage pools.
func (m *Manager) Pools(ctx context.Context, id string) ([]hv.Pool, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	return hv.Pools(ctx, s)
}

// Volumes lists a pool's volumes.
func (m *Manager) Volumes(ctx context.Context, id, pool string) ([]hv.Volume, error) {
	s, err := m.Session(id)
	if err != nil {
		return nil, err
	}
	return hv.Volumes(ctx, s, pool)
}

func (m *Manager) CreateVolume(ctx context.Context, id, pool, name string, size uint64, format string) (hv.Volume, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Volume{}, err
	}
	return hv.CreateVolume(ctx, s, pool, name, size, format)
}

func (m *Manager) ResizeVolume(ctx context.Context, id, pool, name string, size uint64) (hv.Volume, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Volume{}, err
	}
	return hv.ResizeVolume(ctx, s, pool, name, size)
}

func (m *Manager) DeleteVolume(ctx context.Context, id, pool, name string) error {
	s, err := m.Session(id)
	if err != nil {
		return err
	}
	return hv.DeleteVolume(ctx, s, pool, name)
}

// Download copies a volume into a new local file; a failed download
// removes the file.
func (m *Manager) Download(ctx context.Context, id, pool, name, path string, progress func(got, total int64)) (hv.Volume, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Volume{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return hv.Volume{}, err
	}
	v, err := hv.Download(ctx, s, pool, name, f, progress)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return v, err
}

// DownloadTo copies a volume's bytes into w: a browser's response under
// virt-mm serve.
func (m *Manager) DownloadTo(ctx context.Context, id, pool, name string, w io.Writer, progress func(got, total int64)) (hv.Volume, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Volume{}, err
	}
	return hv.Download(ctx, s, pool, name, w, progress)
}

// Upload copies a local file into a new volume of pool, named after the
// file unless name is set.
func (m *Manager) Upload(ctx context.Context, id, pool, path, name string, progress func(sent, total int64)) (hv.Volume, error) {
	f, err := os.Open(path)
	if err != nil {
		return hv.Volume{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return hv.Volume{}, err
	}
	if !st.Mode().IsRegular() {
		return hv.Volume{}, fmt.Errorf("%s is not a file", path)
	}
	if name == "" {
		name = filepath.Base(path)
	}
	return m.UploadFrom(ctx, id, pool, name, f, st.Size(), progress)
}

// UploadFrom copies size bytes from r into a new volume name of pool: a
// file a browser sends to virt-mm serve, which isn't on this computer's
// disk.
func (m *Manager) UploadFrom(ctx context.Context, id, pool, name string, r io.Reader, size int64, progress func(sent, total int64)) (hv.Volume, error) {
	s, err := m.Session(id)
	if err != nil {
		return hv.Volume{}, err
	}
	var report func(int64)
	if progress != nil {
		report = func(sent int64) { progress(sent, size) }
	}
	return hv.Upload(ctx, s, pool, name, r, size, report)
}
