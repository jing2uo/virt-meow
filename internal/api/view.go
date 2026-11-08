package api

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/jing2uo/virt-meow/internal/store"
)

// viewRE is a view as the page writes it in its URL's fragment: its own
// routes, with names as encodeURIComponent leaves them.
var viewRE = regexp.MustCompile(`^[A-Za-z0-9/%._~!*'()-]{0,400}$`)

// RememberView keeps the view the window shows (the page's URL fragment,
// without '#') in the config dir, where the window opens the next time it
// starts without one asked for. Only the window: a browser has its URL.
func (s *Service) RememberView(view string) error {
	if s.c.opts.Mode != Desktop {
		return errors.New("only the desktop window remembers its view; a browser keeps it in its URL")
	}
	if !viewRE.MatchString(view) {
		return fmt.Errorf("not a view of the page: %q", view)
	}
	return store.UpdateWindow(func(w *store.Window) { w.View = view })
}
