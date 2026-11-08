package api

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jing2uo/virt-meow/internal/hv"
)

// EvActivity carries each new entry of the activity log.
const EvActivity = "activity" // Activity

// Activity is one thing that happened: an action taken from a page and its
// outcome, a machine's event on any host, an upload that ended.
type Activity struct {
	ID         int64     `json:"id"`
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"` // action, event, upload
	Connection string    `json:"connection,omitempty"`
	Machine    string    `json:"machine,omitempty"`
	// What is the Service method of an action, the event's kind (started,
	// stopped, …), or the upload's file.
	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// activityKeep is how many entries the log keeps, newest first.
const activityKeep = 500

// activityLog is the session's activity, shared by the pages of one virt-mm
// process (the window's, or every browser's under serve).
type activityLog struct {
	mu    sync.Mutex
	next  int64
	items []Activity // oldest first
}

func (l *activityLog) add(a Activity) Activity {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	a.ID = l.next
	if a.Time.IsZero() {
		a.Time = time.Now()
	}
	l.items = append(l.items, a)
	if n := len(l.items) - activityKeep; n > 0 {
		l.items = slices.Delete(l.items, 0, n)
	}
	return a
}

func (l *activityLog) list() []Activity {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := slices.Clone(l.items)
	slices.Reverse(out)
	return out
}

// record adds an entry to the log and tells every page.
func (c *Core) record(a Activity) Activity {
	a = c.activity.add(a)
	c.emit(EvActivity, a)
	return a
}

// recordEvent logs a machine's event.
func (c *Core) recordEvent(id string, ev hv.Event) {
	c.record(Activity{Kind: "event", Connection: id, Machine: ev.Name, What: string(ev.Kind), Detail: ev.Detail})
}

// recordUpload logs an upload that ended.
func (c *Core) recordUpload(u Upload) {
	if u.State == "running" {
		return
	}
	c.record(Activity{Kind: "upload", Connection: u.Connection, What: u.Name, Detail: u.State + " → " + u.Pool, Error: u.Error})
}

// Activity is the session's log, newest first: the actions the pages
// reported, machines' events on every host, uploads.
func (s *Service) Activity() []Activity { return s.c.activity.list() }

// RecordAction adds an action a page took, and its outcome, to the log
// every page shows; the entry as kept comes back (and to every page as an
// "activity" event).
func (s *Service) RecordAction(a Activity) (Activity, error) {
	if a.What == "" {
		return Activity{}, errors.New("an action names what it did")
	}
	clip := func(v string, n int) string {
		v = strings.TrimSpace(v)
		for len(v) > n {
			_, size := utf8.DecodeLastRuneInString(v)
			v = v[:len(v)-size]
		}
		return v
	}
	return s.c.record(Activity{Kind: "action", Connection: clip(a.Connection, 64), Machine: clip(a.Machine, 128),
		What: clip(a.What, 64), Detail: clip(a.Detail, 256), Error: clip(a.Error, 1024)}), nil
}
