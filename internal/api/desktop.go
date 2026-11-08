package api

import (
	"context"
	"errors"

	"github.com/jing2uo/virt-meow/internal/store"
)

// EvView asks the page to show a view (its URL's fragment, without '#'):
// a second virt-mm gui hands its own to the window already open.
const EvView = "view"

// OpenView sends the page to view, for the window: what a second virt-mm
// gui was started to show.
func (c *Core) OpenView(view string) { c.emit(EvView, view) }

// Settings are the page's settings, from the config dir.
func (s *Service) Settings() (store.Settings, error) { return store.LoadSettings() }

// SaveSettings keeps the page's settings in the config dir.
func (s *Service) SaveSettings(st store.Settings) (store.Settings, error) {
	if err := store.SaveSettings(st); err != nil {
		return store.Settings{}, err
	}
	return st, nil
}

// SetTitle titles the window the page calling it is in, after its view.
// Only the window: a browser shows the page's own title.
func (s *Service) SetTitle(ctx context.Context, title string) error {
	if s.c.opts.SetTitle == nil {
		return errors.New("only the desktop window has a title to set; a browser shows the page's")
	}
	return s.c.opts.SetTitle(ctx, title)
}

// Notify tells the desktop that something the user may be waiting for
// happened, when no virt-meow window has focus. Only the window: a page under
// serve says it in its own toasts.
func (s *Service) Notify(title, body string) error {
	if s.c.opts.Notify == nil {
		return errors.New("only the desktop window sends desktop notifications")
	}
	return s.c.opts.Notify(title, body)
}
