package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/hv"
)

// cmdGraphics forwards a machine's VNC or SPICE console to a port on this
// computer's loopback until interrupted: each connection a viewer makes (a
// SPICE client makes one per channel) gets its own to the machine's
// server, over the host's SSH connection when it is remote. With --json,
// one line says where, once listening.
func cmdGraphics(ctx context.Context, o opts, m *app.Manager, kind string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: virt-mm %s <machine> [--port N]", kind)
	}
	port := 0
	if v := o.flags["port"]; v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("--port %q: 1 to 65535", v)
		}
		port = p
	}
	c, cancel := callCtx(ctx)
	s, d, err := machineOn(c, o, m, args[0])
	var v hv.GraphicsServer
	if err == nil {
		v, err = hv.FindGraphics(c, s, d.UUID, kind)
	}
	cancel()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer ln.Close()
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	addr := ln.Addr().String()
	if o.json {
		b, _ := json.Marshal(map[string]string{"name": d.Name, "listen": addr, kind: v.Addr})
		fmt.Printf("%s\n", b)
	} else {
		viewer := map[string]string{"vnc": "a VNC viewer", "spice": "remote-viewer spice://" + addr}[kind]
		fmt.Printf("%s's %s console (%s on %s) is at %s here: point %s at it. Ctrl+C stops.\n",
			d.Name, strings.ToUpper(kind), v.Addr, s.Target(), addr, viewer)
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		viewer, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // interrupted: the end
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer viewer.Close()
			c, cancel := callCtx(ctx)
			server, _, err := hv.DialGraphics(c, s, d.UUID, kind)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "virt-mm: %s: %v\n", viewer.RemoteAddr(), err)
				return
			}
			defer server.Close()
			stop := context.AfterFunc(ctx, func() { viewer.Close(); server.Close() })
			defer stop()
			pipe(viewer, server)
		}()
	}
}

// pipe copies both ways until either side ends, then ends the other.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}

func cmdScreenshot(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: virt-mm screenshot <machine> [<file.png>|-] [--screen N] [--width W]")
	}
	width := 0
	if v := o.flags["width"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("--width %q: pixels, scaled down to it", v)
		}
		width = n
	}
	var screen uint64
	if v := o.flags["screen"]; v != "" {
		var err error
		if screen, err = strconv.ParseUint(v, 10, 32); err != nil {
			return fmt.Errorf("--screen %q", v)
		}
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	s, d, err := machineOn(ctx, o, m, args[0])
	if err != nil {
		return err
	}
	file := d.Name + time.Now().Format("-20060102-150405") + ".png"
	if len(args) == 2 {
		file = args[1]
	}
	if file == "-" && o.json {
		return errors.New("--json writes the outcome to stdout: give the PNG a file")
	}
	shot, err := hv.TakeScreenshot(ctx, s, d.UUID, uint32(screen))
	if err != nil {
		return err
	}
	if width > 0 {
		if shot, err = hv.Thumbnail(shot, width); err != nil {
			return err
		}
	}
	if file == "-" {
		_, err := os.Stdout.Write(shot.PNG)
		return err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(shot.PNG)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(file)
		return err
	}
	res := map[string]any{"file": file, "width": shot.Width, "height": shot.Height, "bytes": len(shot.PNG)}
	out(o, res, func(w io.Writer) { fmt.Fprintf(w, "%s: %dx%d, %s\n", file, shot.Width, shot.Height, d.Name) })
	return nil
}
