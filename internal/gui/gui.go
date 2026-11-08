//go:build !nogui

// Package gui is the Wails front end: a window showing the page
// (internal/ui), bound to api.Service. What only a window has lives here: a
// native file picker, more windows (a console of its own), its title, one
// instance, desktop notifications, the keyboard grab, and a loopback listener for the
// consoles' WebSockets, which Wails' asset server can't carry.
package gui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/jing2uo/virt-meow/internal/api"
	"github.com/jing2uo/virt-meow/internal/store"
	"github.com/jing2uo/virt-meow/internal/ui"
)

// AppID is the desktop/Flatpak application id.
const AppID = "io.github.jing2uo.VirtMeow"

// Run opens the window. view is the page's starting view: "create" opens
// the new-machine form once the first connection is up, "console/<machine>"
// that machine's console; "" the view it showed when it last closed, at
// the size and place it had. A virt-mm gui already running on the same
// config dir is given the view instead, and this one exits.
func Run(version, view string) error {
	// As in cellar: GTK's Vulkan renderer reads WebKit's dmabufs back to the
	// CPU on some GPUs; the GL renderer imports them directly.
	if runtime.GOOS == "linux" && os.Getenv("GSK_RENDERER") == "" {
		os.Setenv("GSK_RENDERER", "gl")
	}
	// The consoles' WebSockets, on a port of this computer's loopback. Only
	// a page holding a token virt-meow issued gets through.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()

	var a *application.App
	var main *application.WebviewWindow
	notes := notifications.New()
	core, err := api.New(api.Options{
		Version:      version,
		Mode:         api.Desktop,
		Emit:         func(name string, data any) { a.Event.Emit(name, data) },
		PickFile:     func() (string, error) { return pickFile(a) },
		SaveFile:     func(name string) (string, error) { return saveFile(a, name) },
		ConsoleBase:  "ws://" + ln.Addr().String(),
		OpenWindow:   func(url, title string) error { openWindow(a, url, title); return nil },
		SetTitle:     setTitle,
		Notify:       func(title, body string) error { return notify(a, notes, title, body) },
		GrabKeyboard: func(ctx context.Context, on bool) (bool, error) { return grabKeyboard(a, ctx, on) },
	})
	if err != nil {
		return err
	}
	defer core.Close()
	srv := &http.Server{Handler: core.Consoles(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	a = application.New(application.Options{
		Name:        "virt-meow",
		Description: "libvirt virtual machines, here or over SSH",
		Services:    []application.Service{application.NewService(core.Service()), application.NewService(notes)},
		Assets:      application.AssetOptions{Handler: application.BundledAssetFileServer(ui.Assets())},
		Linux:       application.LinuxOptions{ApplicationID: AppID},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID:       instanceID(),
			AdditionalData: map[string]string{"view": view},
			OnSecondInstanceLaunch: func(d application.SecondInstanceData) {
				if main == nil {
					return
				}
				main.Show()
				main.UnMinimise()
				main.Focus()
				if v := d.AdditionalData["view"]; v != "" {
					core.OpenView(v)
				}
			},
		},
	})
	last, _ := store.LoadWindow()
	if view == "" {
		view = last.View
	}
	opts := application.WebviewWindowOptions{
		Title:  "virt-meow",
		Width:  1180,
		Height: 760,
		URL:    "/#" + view,
	}
	if last.Width >= 400 && last.Height >= 300 {
		opts.Width, opts.Height = last.Width, last.Height
	}
	if last.Positioned {
		opts.InitialPosition, opts.X, opts.Y = application.WindowXY, last.X, last.Y
	}
	if last.Maximised {
		opts.StartState = application.WindowStateMaximised
	}
	main = a.Window.NewWithOptions(opts)
	main.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) { rememberWindow(main) })
	return a.Run()
}

// instanceID names the running virt-mm gui on one config dir: a second
// one on the same dir hands it its view; one on another dir (a scratch
// config) runs beside it. Under the app id, which a Flatpak may own on
// the session bus.
func instanceID() string {
	sum := sha256.Sum256([]byte(store.Dir()))
	return AppID + ".c" + hex.EncodeToString(sum[:4])
}

// setTitle titles the window whose page made the call.
func setTitle(ctx context.Context, title string) error {
	w, ok := ctx.Value(application.WindowKey).(application.Window)
	if !ok {
		return errors.New("no window made this call")
	}
	w.SetTitle(title)
	return nil
}

// notify sends a desktop notification unless one of virt-meow's windows has
// focus: then the page's own toast says it.
func notify(a *application.App, ns *notifications.NotificationService, title, body string) error {
	for _, w := range a.Window.GetAll() {
		if w.IsFocused() {
			return nil
		}
	}
	id := make([]byte, 6)
	rand.Read(id)
	return ns.SendNotification(notifications.NotificationOptions{ID: "virt-mm-" + hex.EncodeToString(id), Title: title, Body: body})
}

// rememberWindow keeps the window's size and place for its next start; a
// maximised window keeps the size it had before. A desktop that doesn't
// say where windows are (Wayland) gives 0, 0: no place is kept then.
func rememberWindow(w *application.WebviewWindow) {
	width, height := w.Size()
	x, y := w.Position()
	maxed := w.IsMaximised()
	store.UpdateWindow(func(s *store.Window) {
		s.Maximised = maxed
		if maxed {
			return
		}
		s.Width, s.Height = width, height
		s.X, s.Y, s.Positioned = x, y, x != 0 || y != 0
	})
}

// openWindow opens another window on the page: a console of its own.
func openWindow(a *application.App, url, title string) {
	a.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  title,
		Width:  1024,
		Height: 768,
		URL:    url,
	})
}

func pickFile(a *application.App) (string, error) {
	zh := strings.HasPrefix(strings.ToLower(os.Getenv("LANG")+os.Getenv("LC_ALL")), "zh")
	tr := func(z, e string) string {
		if zh {
			return z
		}
		return e
	}
	return a.Dialog.OpenFile().
		SetTitle(tr("选择要上传的 ISO 或磁盘镜像", "Choose an ISO or disk image to upload")).
		SetDirectory(downloads()).
		AddFilter(tr("ISO 和磁盘镜像", "ISOs and disk images"), "*.iso;*.ISO;*.img;*.qcow2;*.raw").
		AddFilter(tr("所有文件", "All files"), "*").
		PromptForSingleSelection()
}

func saveFile(a *application.App, name string) (string, error) {
	zh := strings.HasPrefix(strings.ToLower(os.Getenv("LANG")+os.Getenv("LC_ALL")), "zh")
	title := "Save the volume as"
	if zh {
		title = "把卷保存为"
	}
	return a.Dialog.SaveFile().
		SetMessage(title).
		SetDirectory(downloads()).
		SetFilename(name).
		CanCreateDirectories(true).
		PromptForSingleSelection()
}

func downloads() string {
	if runtime.GOOS == "linux" {
		if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil {
			if d := strings.TrimSpace(string(out)); d != "" {
				return d
			}
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}
