//go:build !nogui && linux

package gui

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>

extern void virtmmGrabChanged(void *window, gboolean on);

static void virtmm_grab_notify(GObject *surface, GParamSpec *spec, gpointer window) {
	gboolean on = FALSE;
	g_object_get(surface, "shortcuts-inhibited", &on, NULL);
	virtmmGrabChanged(window, on);
}

// virtmm_grab asks the desktop for the keys it keeps (GTK's system
// shortcuts inhibition: the Wayland protocol, a grab on X11), or gives them
// back; 0 when the window has no surface yet.
static int virtmm_grab(void *window, int on) {
	GdkSurface *s = gtk_native_get_surface(GTK_NATIVE(window));
	if (s == NULL || !GDK_IS_TOPLEVEL(s))
		return 0;
	if (g_object_get_data(G_OBJECT(s), "virt-mm-grab") == NULL) {
		g_signal_connect(s, "notify::shortcuts-inhibited", G_CALLBACK(virtmm_grab_notify), window);
		g_object_set_data(G_OBJECT(s), "virt-mm-grab", GINT_TO_POINTER(1));
	}
	if (on)
		gdk_toplevel_inhibit_system_shortcuts(GDK_TOPLEVEL(s), NULL);
	else
		gdk_toplevel_restore_system_shortcuts(GDK_TOPLEVEL(s));
	return 1;
}
*/
import "C"

import (
	"context"
	"errors"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/jing2uo/virt-meow/internal/api"
)

// grabApp is the app whose windows virtmmGrabChanged looks through.
var grabApp *application.App

// grabKeyboard has the window that made the call take the keys the desktop
// keeps for itself, or give them back. The desktop may ask its user first
// (GNOME does, once per app), refuse, or end it when the window loses
// focus: the page hears each change.
func grabKeyboard(a *application.App, ctx context.Context, on bool) (bool, error) {
	grabApp = a
	w, ok := ctx.Value(application.WindowKey).(*application.WebviewWindow)
	if !ok {
		return false, errors.New("no window made this call")
	}
	var done C.int
	application.InvokeSync(func() {
		if p := w.NativeWindow(); p != nil {
			o := C.int(0)
			if on {
				o = 1
			}
			done = C.virtmm_grab(p, o)
		}
	})
	return done == 1, nil
}

//export virtmmGrabChanged
func virtmmGrabChanged(window unsafe.Pointer, on C.gboolean) {
	if grabApp == nil {
		return
	}
	for _, w := range grabApp.Window.GetAll() {
		if ww, ok := w.(*application.WebviewWindow); ok && ww.NativeWindow() == window {
			ww.DispatchWailsEvent(&application.CustomEvent{Name: api.EvKeyboardGrab, Data: api.KeyboardGrab{On: on != 0}})
		}
	}
}
