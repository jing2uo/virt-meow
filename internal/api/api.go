// Package api is what the page calls, the same in the Wails window and
// under virt-mm serve (docs/adr/0003): Service's exported methods, with
// JSON-serialisable arguments and results, and the events Core emits. The
// window binds Service through Wails; internal/web serves it over HTTP and a
// WebSocket. Neither adds logic of its own: what differs between them
// (picking a local file, where a console's WebSocket is served) is given to
// New as options.
package api

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/console"
	"github.com/jing2uo/virt-meow/internal/hv"
	"github.com/jing2uo/virt-meow/internal/store"
)

// Events the page listens to.
const (
	EvConnection = "connection" // app.ConnInfo
	EvDomain     = "domain"     // DomainEvent
	EvPrompt     = "prompt"     // Prompt
	// EvNotRemembered: NotRemembered, an answer the keychain didn't take.
	EvNotRemembered = "not-remembered"
	EvUpload        = "upload"   // Upload
	EvDownload      = "download" // Download
	// EvView (desktop.go): a view to show.
	// EvKeyboardGrab: KeyboardGrab, to the window's own page only.
	EvKeyboardGrab = "keyboard-grab"
)

// KeyboardGrab is whether the desktop's keys go to the window now.
type KeyboardGrab struct {
	On bool `json:"on"`
}

// callTimeout bounds one call from the page; a hung host shows as an error
// and a dropped connection rather than a frozen button.
const callTimeout = 30 * time.Second

// Mode is the front end the page runs in.
type Mode string

const (
	Desktop Mode = "desktop" // the Wails window
	Web     Mode = "web"     // a browser, under virt-mm serve
)

// Options are what the front end gives the core.
type Options struct {
	Version string
	Mode    Mode
	// Emit sends an event to the page (to every page, under serve).
	Emit func(name string, data any)
	// PickFile asks for a file on this computer, "" when none was picked.
	// nil when there is no such computer: a browser uploads instead.
	PickFile func() (string, error)
	// SaveFile asks where on this computer to save a file named name, ""
	// when none was picked. nil in a browser, which downloads instead.
	SaveFile func(name string) (string, error)
	// ConsoleBase is put before a console's path to make the URL its client
	// opens: "ws://127.0.0.1:port" for the window's own listener, "" under
	// serve, whose page makes it relative to its own address.
	ConsoleBase string
	// OpenWindow opens another window showing the page at url (a path and
	// fragment of the page's own), titled title. nil in a browser, which
	// opens a tab itself.
	OpenWindow func(url, title string) error
	// SetTitle titles the window whose page made the call (ctx says which).
	// nil in a browser.
	SetTitle func(ctx context.Context, title string) error
	// Notify sends a desktop notification unless a virt-meow window has
	// focus. nil in a browser.
	Notify func(title, body string) error
	// GrabKeyboard has the window whose page made the call (ctx says which)
	// take the keys the desktop keeps for itself, or give them back; ok
	// false where it can't. It tells that page when the desktop takes the
	// grab away or refuses it (EvKeyboardGrab). nil in a browser, whose
	// page uses the Keyboard Lock API itself.
	GrabKeyboard func(ctx context.Context, on bool) (ok bool, err error)
}

// Core holds the open connections and everything the page's calls share.
// Its exported methods are for the front ends' Go code, not the page.
type Core struct {
	opts    Options
	m       *app.Manager
	prompts *prompter
	ups     uploads
	bridge  *console.Bridge
	svc     *Service
	// activity is what happened this session, for every page.
	activity activityLog
	once     sync.Once
	// download copies a volume to a file: the Manager's, a fake in tests.
	download func(ctx context.Context, id, pool, name, path string, progress func(got, total int64)) (hv.Volume, error)
}

// New loads the saved connections. Nothing is connected until the page
// asks for its Env.
func New(opts Options) (*Core, error) {
	c := &Core{opts: opts, bridge: console.New()}
	c.prompts = &prompter{emit: c.emit, waiting: map[string]chan answer{}}
	m, err := app.NewManager(app.Callbacks{
		OnConnection: func(ci app.ConnInfo) { c.emit(EvConnection, ci) },
		OnEvent: func(id string, ev hv.Event) {
			c.emit(EvDomain, DomainEvent{Connection: id, Event: ev})
			c.recordEvent(id, ev)
		},
	}, c.prompts)
	if err != nil {
		return nil, err
	}
	c.m = m
	c.download = m.Download
	c.ups.cancels = map[string]context.CancelFunc{}
	c.svc = &Service{c: c}
	return c, nil
}

func (c *Core) emit(name string, data any) {
	if c.opts.Emit != nil {
		c.opts.Emit(name, data)
	}
}

// Service is what the page calls.
func (c *Core) Service() *Service { return c.svc }

// Consoles is the WebSocket bridge the consoles' clients connect to, at
// console.Path(token); the front end serves it.
func (c *Core) Consoles() *console.Bridge { return c.bridge }

// Manager is the connections, for a front end's own Go code.
func (c *Core) Manager() *app.Manager { return c.m }

// Close closes every connection.
func (c *Core) Close() { c.m.Close() }

// Service is what the page calls: every exported method is reachable from
// it, so nothing else goes here.
type Service struct{ c *Core }

// DomainEvent is a machine's lifecycle event on one connection.
type DomainEvent struct {
	Connection string   `json:"connection"`
	Event      hv.Event `json:"event"`
}

// Env is what the page needs to start.
type Env struct {
	Version string `json:"version"`
	Mode    Mode   `json:"mode"`
	OS      string `json:"os"`   // of the computer virt-meow runs on
	Lang    string `json:"lang"` // zh, en, or "" for the browser's own
}

// lang follows the locale, as the rest of the desktop does; the webview's
// navigator.language doesn't (WebKitGTK reports en-US under a Chinese LANG).
// A browser under serve knows its user's language better than the server.
func lang() string {
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if l := os.Getenv(v); l != "" {
			if strings.HasPrefix(strings.ToLower(l), "zh") {
				return "zh"
			}
			return "en"
		}
	}
	return "en"
}

// Env also connects the saved connections marked autoconnect, once.
func (s *Service) Env() Env {
	c := s.c
	c.once.Do(func() {
		for _, ci := range c.m.Connections() {
			if ci.Autoconnect {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
					defer cancel()
					c.m.Connect(ctx, ci.ID)
				}()
			}
		}
	})
	e := Env{Version: c.opts.Version, Mode: c.opts.Mode, OS: runtime.GOOS}
	if c.opts.Mode == Desktop {
		e.Lang = lang()
	}
	if st, err := store.LoadSettings(); err == nil && st.Lang != "" {
		e.Lang = st.Lang // the settings' over the system's
	}
	return e
}

func (s *Service) Connections() []app.ConnInfo { return s.c.m.Connections() }

func (s *Service) AddConnection(name, uri string, autoconnect bool) (app.ConnInfo, error) {
	return s.c.m.Add(name, uri, autoconnect)
}

// SSHRoute is how uri is reached after the SSH configuration of the
// account virt-meow runs as: the add-connection form shows it for an alias.
func (s *Service) SSHRoute(uri string) ([]conn.Hop, error) { return s.c.m.SSHRoute(uri) }

func (s *Service) RemoveConnection(id string) error { return s.c.m.Remove(id) }

// Connect opens a connection. A host key waiting for an answer is not an
// error here: the connection event carries it to the page.
func (s *Service) Connect(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	err := s.c.m.Connect(ctx, id)
	// By the error, not by the connection's state: the state's change may
	// not have been recorded yet when Connect returns.
	var unknown *conn.UnknownHostKeyError
	if errors.As(err, &unknown) {
		return nil
	}
	return err
}

func (s *Service) TrustHostKey(id, fingerprint string) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.TrustHostKey(ctx, id, fingerprint)
}

// DomainRow is a machine with the power actions that make sense for it now.
type DomainRow struct {
	hv.Domain
	Actions []hv.Action `json:"actions"`
}

func (s *Service) Domains(id string) ([]DomainRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	doms, err := s.c.m.Domains(ctx, id)
	if err != nil {
		return nil, err
	}
	rows := make([]DomainRow, len(doms))
	for i, d := range doms {
		rows[i] = DomainRow{Domain: d, Actions: actionsFor(d.State)}
	}
	return rows, nil
}

// actionsFor are the power actions a machine in state st can take.
func actionsFor(st hv.State) []hv.Action {
	out := []hv.Action{}
	for _, a := range hv.Actions {
		if a.Allowed(st) {
			out = append(out, a)
		}
	}
	return out
}

// OverviewMachine is a machine in the overview of every host, with its
// connection and the power actions that make sense for it now.
type OverviewMachine struct {
	app.HostMachine
	Actions []hv.Action `json:"actions"`
}

// Overview is every connection (its usage and machine counts when it is
// connected) and one list of every machine on them.
type Overview struct {
	Hosts    []app.HostSummary `json:"hosts"`
	Machines []OverviewMachine `json:"machines"`
}

// Overview reads every connected host at once: one list and one usage
// sample each.
func (s *Service) Overview() (Overview, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	all := s.c.m.AllHosts(ctx, false)
	out := Overview{Hosts: all.Hosts, Machines: make([]OverviewMachine, len(all.Machines))}
	for i, m := range all.Machines {
		out.Machines[i] = OverviewMachine{HostMachine: m, Actions: actionsFor(m.State)}
	}
	return out, nil
}

// PowerMany does one power action to several machines, on any of the
// hosts; each one's outcome is in the result.
func (s *Service) PowerMany(refs []app.MachineRef, action string) ([]app.PowerResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	return s.c.m.PowerMany(ctx, refs, action)
}

func (s *Service) Power(id, uuid, action string) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.Power(ctx, id, uuid, action)
}

// Answer replies to a prompt; ok false means the user cancelled, remember
// asks for the answer to be kept in the OS keychain once it has worked.
func (s *Service) Answer(promptID, value string, ok, remember bool) {
	s.c.prompts.answer(promptID, value, ok, remember)
}

// ForgetSecrets forgets the passwords and key passphrases remembered for
// connection id, and says which they were.
func (s *Service) ForgetSecrets(id string) ([]conn.Secret, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.ForgetSecrets(ctx, id)
}

// CreateOptions is what the creation form offers; it reads every pool, so
// it gets more time than a plain call.
func (s *Service) CreateOptions(id string) (app.CreateOptions, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	return s.c.m.CreateOptions(ctx, id)
}

// OSVariants is osinfo-db for the form; reading a host's over SSH the first
// time takes more than a plain call.
func (s *Service) OSVariants(id string) (app.OSList, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	return s.c.m.OSVariants(ctx, id)
}

// DetectOS recognises the system on an ISO or a disk image in a pool.
func (s *Service) DetectOS(id, pool, vol string) (hv.Detected, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	return s.c.m.DetectOS(ctx, id, pool, vol)
}

func (s *Service) PreviewMachine(id string, spec hv.Spec) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.Preview(ctx, id, spec)
}

// CreateMachine makes a new machine; xml is its edited definition, or "".
// A disk copied from an image takes as long as a clone's.
func (s *Service) CreateMachine(id string, spec hv.Spec, xml string) (hv.Created, error) {
	timeout := 4 * callTimeout
	if spec.ImageVolume != "" {
		timeout = cloneTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.c.m.Create(ctx, id, spec, xml)
}

func (s *Service) Definition(id, uuid string) (hv.Definition, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.Definition(ctx, id, uuid)
}

func (s *Service) Redefine(id, uuid, base, xml string) (hv.Definition, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.Redefine(ctx, id, uuid, base, xml)
}

func (s *Service) DeleteMachine(id, uuid string, disks bool) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*callTimeout)
	defer cancel()
	removed, err := s.c.m.Delete(ctx, id, uuid, disks)
	if removed == nil {
		removed = []string{}
	}
	return removed, err
}

// Devices are a machine's disks, interfaces and consoles.
func (s *Service) Devices(id, uuid string) (hv.Devices, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return s.c.m.Devices(ctx, id, uuid)
}
