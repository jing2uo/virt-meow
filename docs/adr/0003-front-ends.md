# ADR-0003: Three front ends, the command line first

- Status: accepted
- Date: 2026-10-27

## Context

virt-meow has one core (`internal/conn`, `internal/hv`) and one service API
(`internal/app.Manager`), and so far two ways in: the CLI and the Wails
window ([ADR-0001](0001-architecture.md)). Three audiences want it:

- **virsh's users**: scripts, a terminal on a host, automation. They need
  every operation, `--json`, and nothing interactive that can't be skipped.
- **virt-manager's users**: a desktop window, several hosts, forms, consoles.
- **a management platform**: a browser, from anywhere, on many hosts at
  once: create, delete, monitor, change machines. Later a cluster of hosts
  managed as one.

cellar already runs the same Go program as a CLI, a GUI and a web server.
Building features GUI first has also been slow to verify: the window can't
be driven by tests, so every change is checked by screenshots.

## Decision

### One program, three front ends

```
virt-mm <command> …      CLI: virsh's operations                  (now)
virt-mm gui              Wails window: virt-manager's             (now)
virt-mm serve            HTTP: the same pages in a browser,       (now)
                        a management platform for many hosts
```

All three call the same `Manager` methods with the same JSON types. The
page (`internal/ui`) is shared between the window and the browser. What it
calls is `internal/api`: `api.Service`'s exported methods (and nothing else
is exported on it) with the call timeouts, prompts, uploads and consoles,
and the events `api.Core` emits. `internal/gui` binds `api.Service` through
Wails; `internal/web` serves it as `POST /api/<Method>` (a JSON array of
arguments) and a WebSocket at `/api/events`. The page reaches either
through `rpc.js`, its one module that knows which it runs in. What differs
is given to `api.New` as options: the window picks local files, serves
the consoles' WebSockets on a loopback port of its own, opens a console
in a window of its own (`OpenConsoleWindow`), titles itself after the
view (`SetTitle`), sends desktop notifications (`Notify`), takes the
keys the desktop keeps for a console (`GrabKeyboard`: GTK's system
shortcuts inhibition on Linux) and remembers its view; under serve the browser uploads its file (`POST /api/upload`),
the consoles are under the server's own address, a console of its own is
a tab the page opens itself, the keyboard grab is the browser's Keyboard
Lock (in full screen, the only place it holds), and the page's title and
URL are the browser's. A method that takes a `context.Context` first gets the calling
window's under Wails and the request's under serve; the page passes only
the rest. The page's settings (language, notifications, the list's
pictures, the sidebar, new machines' defaults) are kept in the config
dir, so the window and every page under serve share them; the CLI reads
and sets the defaults for new machines too (`virt-mm defaults`, which
`virt-mm create` uses under its flags).

### The command line first

Every capability is built in this order:

1. `internal/hv` (and `Manager`), against libvirt;
2. a CLI command, with `--json`;
3. an e2e test against libvirtd in the container (`make e2e`);
4. then the page, which only calls what the CLI already exercises.

A capability is done in the core when the CLI and its e2e test are; the
GUI and the web follow. [The virsh table](../spec/virsh.md) is the list of
what the CLI covers and what is still to do.

Some things don't fit a terminal (the VNC console, the forms' live
defaults). The CLI still gets the part that does: the serial console in the
terminal, VNC forwarded to a local port for any viewer, `--print-xml` for
what a form would make.

### The web server

- `virt-mm serve` holds the connections and known hosts, and uses the SSH
  keys, of the account it runs as: the server is where they live, not the
  browser. Its config is the CLI's (`$VIRT_MM_CONFIG`, or `virt-mm/` in the
  user's config directory), since both are that account's; a server for
  others runs as an account of its own. It needs no cgo: the CLI build
  serves.
- **A login, one user.** It listens on `127.0.0.1` by default; another
  address only when given (`--listen`). Its password comes from
  `--password-file`, or is set once (`virt-mm serve --set-password`, or asked
  for on the first start that listens beyond loopback) and kept as a
  bcrypt hash in the config, readable by the account only. With a
  password, every request but the login page's needs a session: the page,
  every call, uploads and downloads, and the event and console WebSockets
  (the console's token on top). A session is a random token in a cookie
  that is HttpOnly, SameSite=Strict and, under TLS, Secure; it lives in
  memory, so a restart logs everyone out, and lapses after a week unused.
  Logins are rate-limited per address (five failures a minute, then
  wait); the bcrypt comparison is slow by design.
- **Beyond loopback** serve needs a password, and refuses to start
  without one where nobody can be asked, unless `--no-login` says a
  reverse proxy that authenticates is in front (then the old warning:
  anyone who reaches it controls every host it is connected to). With a
  login it only says to use TLS: `--tls-cert/--tls-key` serve HTTPS
  itself, or a TLS proxy goes in front. On loopback without a password it
  runs without a login, as before, and says how to add one.
- **Only its own pages may use it**, login or not. Any page the user's
  browser opens could otherwise drive it: a request must name the server
  as its Host (on loopback: no DNS rebinding), a page's request must come
  from the server's own origin, calls must be `application/json` and
  uploads `application/octet-stream`, which no other site can send without
  the browser asking first (and it never answers yes). A console's
  WebSocket also needs its token.
- The host key rules don't change: an unknown key is trusted only on the
  user's word, a changed one never from a page.
- The activity log (what was done from the pages and how it went, the
  machines' events on every host, uploads) is the process's, kept for its
  session: under serve every page sees the same one. The pages report
  their own actions (`RecordAction`, from `rpc.js`), since the window
  calls the service without passing through anything virt-meow could hook.
- Monitoring is the live statistics of M2 (`ConnectGetAllDomainStats`, one
  call per host). The page asks for them (`Stats`, `HostStats`) every 2 s
  while it shows them, and `Manager` samples each host at most once per
  interval however many pages ask, making the rates of its last two
  samples. Pushing them over the event stream instead would need a sampler
  per host kept alive by the pages (a lease each renews, since a closed
  browser tab says nothing), for the same calls on the host.

### Cluster mode, later

Many hosts managed as one (placement, a shared inventory, moving machines
between hosts) grows out of `serve`. virtrigaud is one possible back end for
it, not a requirement; whether virt-meow schedules itself or delegates is
decided when cluster mode is designed, in its own ADR.

## Consequences

- The core is tested through the CLI and e2e before any page uses it, and
  the CLI is complete rather than a by-product of the GUI.
- GUI work lags the core by design; a feature can be usable from the
  terminal days before it has a form.
- The page must not call Wails directly outside `call()` and the event
  subscription, or it won't run in a browser.
- A server with a login can be reached from a network, over TLS. It has
  one user: users and permissions are still to come before `serve` is
  offered to several people with different rights.
