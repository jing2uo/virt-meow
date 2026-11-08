//go:build windows && !nogui

package main

import (
	"os"
	"syscall"
)

// The GUI build for Windows is a GUI-subsystem program (-H windowsgui), so
// that opening the window opens no console window beside it. Such a
// program gets no console from the terminal it is started in, and what a
// command prints would go nowhere. So a command (anything but the window)
// attaches to its parent's console, the terminal's, and writes there.
//
// The terminal doesn't wait for a GUI-subsystem program, so its prompt can
// come back before the output; piping it (virt-mm ls | more) makes it
// wait. The CLI archive's virt-mm.exe is a console program without these
// caveats, the one for scripts and prompts (README.md, "Windows").
//
// A stream already redirected (to a file, a pipe) keeps where it goes.
func init() {
	if len(os.Args) < 2 || os.Args[1] == "gui" {
		return
	}
	streams := []struct {
		std  int
		name string
		file **os.File
		flag int
	}{
		{syscall.STD_INPUT_HANDLE, "CONIN$", &os.Stdin, os.O_RDWR},
		{syscall.STD_OUTPUT_HANDLE, "CONOUT$", &os.Stdout, os.O_RDWR},
		{syscall.STD_ERROR_HANDLE, "CONOUT$", &os.Stderr, os.O_RDWR},
	}
	missing := false
	for _, s := range streams {
		if h, err := syscall.GetStdHandle(s.std); err != nil || h == 0 || h == syscall.InvalidHandle {
			missing = true
		}
	}
	if !missing {
		return
	}
	k := syscall.NewLazyDLL("kernel32.dll")
	// ATTACH_PARENT_PROCESS is (DWORD)-1. It fails when the parent has no
	// console (started from Explorer, say): nothing to write to then.
	if r, _, _ := k.NewProc("AttachConsole").Call(uintptr(0xFFFFFFFF)); r == 0 {
		return
	}
	setStd := k.NewProc("SetStdHandle")
	for _, s := range streams {
		if h, err := syscall.GetStdHandle(s.std); err == nil && h != 0 && h != syscall.InvalidHandle {
			continue
		}
		f, err := os.OpenFile(s.name, s.flag, 0)
		if err != nil {
			continue
		}
		*s.file = f
		// For the programs it starts ($EDITOR for virt-mm edit).
		// The id is a negative DWORD, -10 to -12.
		setStd.Call(uintptr(uint32(int32(s.std))), f.Fd())
	}
}
