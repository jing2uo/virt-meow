package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jing2uo/virt-meow/internal/api"
	"github.com/jing2uo/virt-meow/internal/store"
	"github.com/jing2uo/virt-meow/internal/ui"
	"github.com/jing2uo/virt-meow/internal/web"
)

// cmdServe is virt-mm serve: the pages in a browser, until interrupted.
func cmdServe(o opts, args []string) error {
	f := o.flags
	if len(args) != 0 {
		return errors.New("usage: virt-mm serve [--listen 127.0.0.1:8080] [--password-file F | --no-login] [--tls-cert C --tls-key K]\n" +
			"       virt-mm serve --set-password [--password-stdin]")
	}
	if f["set-password"] != "" {
		return setServePassword(o)
	}
	if (f["tls-cert"] == "") != (f["tls-key"] == "") {
		return errors.New("--tls-cert and --tls-key go together")
	}
	tlsOn := f["tls-cert"] != ""
	if tlsOn {
		if _, err := tls.LoadX509KeyPair(f["tls-cert"], f["tls-key"]); err != nil {
			return fmt.Errorf("--tls-cert/--tls-key: %w", err)
		}
	}
	ln, err := net.Listen("tcp", cmp.Or(f["listen"], "127.0.0.1:8080"))
	if err != nil {
		return err
	}
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)
	login, err := serveLogin(o, addr, tlsOn)
	if err != nil {
		return err
	}

	var srv *web.Server
	core, err := api.New(api.Options{
		Version: version,
		Mode:    api.Web,
		Emit: func(name string, data any) {
			if srv != nil {
				srv.Emit(name, data)
			}
		},
	})
	if err != nil {
		return err
	}
	defer core.Close()
	var hosts []string
	switch {
	case addr.IP.IsLoopback():
		hosts = web.LoopbackHosts(addr.String())
	case login == nil:
		fmt.Fprintf(os.Stderr, "virt-mm: WARNING: %s is reachable from the network with --no-login:\n"+
			"anyone who reaches it controls every host it is connected to. Put an authenticating proxy\n"+
			"(and TLS) in front of it, or listen on 127.0.0.1 and use an SSH tunnel.\n", addr)
	case !tlsOn:
		fmt.Fprintf(os.Stderr, "virt-mm: note: %s is reachable from the network without TLS: the password and the\n"+
			"session cross it unencrypted. Give --tls-cert/--tls-key, or put a TLS proxy in front.\n", addr)
	}
	srv = web.New(core, ui.Assets(), hosts)
	if login != nil {
		srv.UseLogin(login)
	}
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	scheme := "http"
	if tlsOn {
		scheme = "https"
	}
	url := scheme + "://" + addr.String() + "/"
	if addr.IP.IsUnspecified() {
		url = fmt.Sprintf("%s://<this host>:%d/", scheme, addr.Port)
	}
	if o.json {
		// One line, as soon as it listens: what a script reads to find it.
		b, _ := json.Marshal(map[string]any{"listen": addr.String(), "url": url, "login": login != nil, "tls": tlsOn})
		fmt.Printf("%s\n", b)
	} else {
		how := "no login"
		if login != nil {
			how = "login"
		}
		fmt.Printf("virt-mm serve: open %s  (%s; Ctrl+C stops)\n", url, how)
	}
	if tlsOn {
		err = hs.ServeTLS(ln, f["tls-cert"], f["tls-key"])
	} else {
		err = hs.Serve(ln)
	}
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// serveLogin is the login serve asks for: the password of --password-file,
// else the one set before (virt-mm serve --set-password). Listening beyond
// loopback needs one: on a terminal it is asked for there (and kept), else
// serve refuses, unless --no-login says an authenticating proxy is in
// front. On loopback, no password means no login, as before there was one.
func serveLogin(o opts, addr *net.TCPAddr, tlsOn bool) (*web.Login, error) {
	f := o.flags
	if f["no-login"] != "" {
		if f["password-file"] != "" {
			return nil, errors.New("--no-login or --password-file, not both")
		}
		return nil, nil
	}
	var hash []byte
	if p := f["password-file"]; p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("--password-file: %w", err)
		}
		pw, _, _ := strings.Cut(string(b), "\n")
		if hash, err = web.HashPassword(strings.TrimRight(pw, "\r")); err != nil {
			return nil, fmt.Errorf("--password-file: %w", err)
		}
	} else {
		var err error
		if hash, err = store.ServePassword(); err != nil {
			return nil, err
		}
	}
	if hash == nil && !addr.IP.IsLoopback() {
		fmt.Fprintf(os.Stderr, "virt-mm serve listens on %s, beyond this computer: it needs a password for its login.\n", addr)
		pw, err := newPassword(o, "virt-mm serve")
		if err != nil {
			return nil, fmt.Errorf("%w; or set one first with virt-mm serve --set-password, or give --password-file "+
				"(--no-login only behind a proxy that authenticates)", err)
		}
		if hash, err = web.HashPassword(pw); err != nil {
			return nil, err
		}
		if err := store.SetServePassword(hash); err != nil {
			return nil, err
		}
	}
	if hash == nil {
		fmt.Fprintln(os.Stderr, "virt-mm: no login (this computer only); virt-mm serve --set-password adds one")
		return nil, nil
	}
	return web.NewLogin(hash, tlsOn)
}

// setServePassword is virt-mm serve --set-password: asked twice (or read
// from stdin), kept hashed in the config for the next serve.
func setServePassword(o opts) error {
	pw, err := newPassword(o, "virt-mm serve")
	if err != nil {
		return err
	}
	hash, err := web.HashPassword(pw)
	if err != nil {
		return err
	}
	if err := store.SetServePassword(hash); err != nil {
		return err
	}
	out(o, map[string]bool{"set": true}, func(w io.Writer) {
		fmt.Fprintln(w, "virt-mm serve: password set; the next serve asks for it")
	})
	return nil
}
