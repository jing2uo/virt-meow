# ADR-0001: Architecture

- Status: accepted
- Date: 2026-10-27

## Context

virt-meow replaces virt-manager: it creates, edits and runs libvirt virtual
machines, with a better interface and more of libvirt within reach. Three
requirements shape everything below:

1. **Every desktop platform.** Linux, macOS and Windows. On macOS and Windows
   there is almost never a local libvirt, so the machines virt-meow manages are
   usually on another computer.
2. **Remote first.** A connection to a host over SSH is the ordinary case,
   several connections are open at once, and a local `qemu:///system` is one
   connection among them.
3. **A cluster later.** The long-term goal is managing a pool of libvirt hosts.
   [virtrigaud](https://github.com/projectbeskar/virtrigaud) already builds
   the cluster layer libvirt lacks (its ADR-0007, the clustered libvirt
   provider, experimental since v0.4.0), and is one possible back end for
   it; [ADR-0003](0003-front-ends.md) leaves the choice to cluster mode's
   own design.

virt-manager can't be moved there: it is Python and GTK 3, bound to
libvirt's C library, spice-gtk and gtk-vnc, which are Linux-first and GTK 3
only (virt-manager#724). sachesi/machines, the closest modern attempt, is
GTK 4 but Linux-only for the same reasons (libvirt.so, gvnc, spice-glib,
Wayland protocols bound directly).

## Decision

### Layers

```
 front end (web: HTML + ES modules)
   ├─ desktop: Wails v3 window          (internal/gui)
   └─ server: the same pages over HTTP  (internal/web, ADR-0003; cluster mode later)
                    │  internal/api: what the page calls, for both
                    │  service API: plain Go methods, JSON types
 core (pure Go, CGO_ENABLED=0 builds)
   ├─ internal/conn   connections: transports, SSH, host keys, session upkeep
   ├─ internal/hv     what virt-meow does with libvirt: domains, power, events…
   └─ internal/store  saved connections, known hosts, preferences
 back ends
   ├─ libvirt, directly: go-libvirt over a Unix socket or SSH   (now)
   └─ virtrigaud: client-go against its CRDs           (perhaps, cluster mode)
```

- **The core is pure Go.** The CLI is a static binary built with
  `CGO_ENABLED=0 -tags nogui`; only the Wails GUI needs cgo. Everything the
  GUI does, the CLI can do, which keeps the core testable without a window
  and scriptable.
- **The service API is the seam.** The GUI calls Go methods whose arguments
  and results are plain JSON-serialisable types. Wails binds them now; a
  server mode can expose the same methods over HTTP and a WebSocket later
  without changing the pages.
- **No premature back-end interface.** Until the virtrigaud back end exists,
  `internal/hv` speaks libvirt directly. Its result types (a domain's state,
  sizes, devices) are written so they don't leak libvirt's RPC types, which
  is what a second back end would need. The interface is extracted when the
  second implementation is written, not before.

### libvirt: go-libvirt, not libvirt.so

[go-libvirt](https://github.com/digitalocean/go-libvirt) speaks libvirt's
RPC protocol in Go. It builds everywhere, needs no libvirt client library on
the machine running virt-meow (none to bundle in the Flatpak, none to install on
macOS or Windows) and is what virtrigaud chose too (its ADR-0008). Its gaps
are known from that work and are handled in `internal/conn`
([ADR-0002](0002-connections-and-ssh.md)): no `context.Context`, no
keepalive, no reconnect, and a data race inside `ConnectToURI`. It has no
tagged release; it is pinned by pseudo-version in `go.mod`.

Domain, network and storage definitions are edited as typed structs from
[`libvirt.org/go/libvirtxml`](https://pkg.go.dev/libvirt.org/go/libvirtxml)
(pure Go, from the libvirt project), with the raw XML editable as a fallback
for whatever the forms don't cover. libvirt validates every definition before
it takes it.

### GUI: Wails v3

Wails v3 (WebKitGTK 6 on Linux, WKWebView on macOS, WebView2 on Windows),
the toolkit cellar already uses. A web front end is what makes the server
mode possible with the same pages, and what lets the consoles be existing,
mature JavaScript: noVNC for graphics, xterm.js for the serial console.

The pages are plain HTML and ES modules without a bundler, as in cellar;
third-party modules (noVNC, xterm.js) are vendored with their licences.

MyGo (pure Go, GPU-drawn native UI) was considered: lighter and with a
shorter path for a framebuffer, but a month old at the time, without pointer
lock, keyboard grab or custom cursors, and with no way to reuse its UI as a
web console.

### Consoles

- **VNC** through noVNC, in the page. Its WebSocket ends in virt-meow, which
  forwards it to the guest's VNC server: over the host's SSH connection
  (`direct-tcpip` to the listen address on the host, `direct-streamlocal`
  to a VNC socket) for a remote host, or straight to that address or
  socket on a local one. (`virDomainOpenGraphicsFD` was the plan for
  local connections, but it hands back a file descriptor over the Unix
  socket, which go-libvirt can't receive.) Machines virt-meow creates get VNC
  by default, on the host's loopback; `virt-mm vnc` forwards the same
  connection to a local port for any viewer. The clipboard is shared over
  VNC too: QEMU's `qemu-vdagent` chardev (clipboard on) talks to
  spice-vdagent in the guest and hands the clipboard to QEMU's VNC server,
  which speaks the extended clipboard noVNC does. New machines get the
  channel where QEMU is 10.1 or later; before that (from libvirt 8.4 and
  QEMU 6.1) it blocks migration, and so saving a running machine and
  snapshotting its memory, and is added only when asked for, saying so. A
  console with a password asks for it (noVNC's VNC authentication); a
  password keeps out who doesn't know it, not who can read the traffic,
  so virt-meow keeps the console on the host's loopback all the same.
- **The WebSocket** is served by `internal/console`, a bridge that hands
  out a token per console and carries each WebSocket opened with it to
  the machine's server, as websockify does. The desktop window serves it
  on a port of this computer's loopback (Wails' asset server can't carry a
  WebSocket); `virt-mm serve` mounts the same handler. The token, not the
  page's origin, authorises a connection; it lives while the console is
  open, since a SPICE client opens one connection per channel.
- **Serial console** through xterm.js, on libvirt's console stream
  (`virDomainOpenConsole`).
- **SPICE**, for now, through spice-html5 in the page, over the same
  bridge (`virt-mm spice` forwards it to a local port for remote-viewer).
  It is old and limited: no scaling (it reads the pointer in canvas
  pixels), no GLZ, no USB redirection or clipboard. That is as far as
  SPICE goes: VNC is the console virt-meow builds on (new machines get it),
  and no better SPICE client is planned. What SPICE does better (USB
  redirection, several monitors, sound, 3D) needs a native client that a
  page can't be, and upstream's own use of SPICE is shrinking (RHEL moved
  to VNC and Cockpit); spice-gtk is GTK 3 and C, not something to bundle
  on macOS and Windows. Someone who wants it points remote-viewer at
  `virt-mm spice`.

### Cross-platform rules

- **Files live on the host.** An ISO or disk image is picked from the host's
  storage pools, or uploaded to one (`virStorageVolUpload`); a path on the
  computer running virt-meow means nothing to a remote host.
- **Local-only features are separate.** USB redirection from this computer,
  Looking Glass, preparing a host for PCI passthrough, libvirt hook scripts:
  each is offered only on a local Linux connection, and none is in the core.
- **Credentials go to the OS keychain** (Keychain, Credential Manager,
  Secret Service), never to a plain file.
- **Pointer lock and keyboard grab** use the web platform's APIs (Pointer
  Lock, Keyboard Lock); where a webview lacks one, the platform code fills
  it in through Wails' native window handle.

## Consequences

- One code base on three platforms, and no libvirt C library anywhere.
- Each go-libvirt gap is ours to cover, in one place (`internal/conn`).
- The GUI costs a webview (memory, and WebKitGTK's rendering on Linux, which
  the VNC console has to be measured against early).
- Builds: the GUI needs cgo on each platform, so CI needs Linux, macOS and
  Windows runners; macOS needs signing and notarisation.
- Cluster mode adds a back end and a server; it does not change the core's
  libvirt path.
