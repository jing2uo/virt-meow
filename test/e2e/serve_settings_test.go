//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// The settings dialog's calls under serve: the defaults, a change kept
// (in serve's config dir) and refused when it isn't one, and the language
// it sets over the browser's in Env. The window's own calls, its title
// (a method that takes the calling window's context first) and desktop
// notifications, say they are the window's.
func TestServeSettings(t *testing.T) {
	sv := serve(t)
	type settings struct {
		Lang          string `json:"lang"`
		Notifications bool   `json:"notifications"`
		Thumbnails    string `json:"thumbnails"`
		Sidebar       string `json:"sidebar"`
	}
	var st settings
	sv.call(&st, "Settings")
	if st != (settings{Notifications: true}) {
		t.Errorf("the defaults: %+v", st)
	}
	want := settings{Lang: "en", Thumbnails: "off", Sidebar: "hidden"}
	sv.call(nil, "SaveSettings", want)
	sv.call(&st, "Settings")
	if st != want {
		t.Errorf("saved %+v, read %+v", want, st)
	}
	var env struct{ Lang string }
	sv.call(&env, "Env")
	if env.Lang != "en" {
		t.Errorf("Env's language with the settings' en: %q", env.Lang)
	}
	if err := sv.try(nil, "SaveSettings", settings{Lang: "klingon"}); err == nil {
		t.Error("saved a language the page doesn't have")
	}
	if err := sv.try(nil, "SetTitle", "web01 — virt-meow"); err == nil || !strings.Contains(err.Error(), "only the desktop window has a title") {
		t.Errorf("SetTitle under serve: %v", err)
	}
	if err := sv.try(nil, "SetTitle", "a", "b"); err == nil || !strings.Contains(err.Error(), "takes 1 arguments, not 2") {
		t.Errorf("SetTitle with two arguments: %v", err)
	}
	if err := sv.try(nil, "Notify", "web01 crashed", "dev"); err == nil || !strings.Contains(err.Error(), "only the desktop window sends") {
		t.Errorf("Notify under serve: %v", err)
	}
}
