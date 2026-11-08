# ADR-0002: Connections, SSH and session upkeep

- Status: accepted
- Date: 2026-10-27

## Context

A connection is one libvirt URI virt-meow keeps open: `qemu:///system` on this
computer, or `qemu+ssh://root@host/system` on another. The GUI holds several
at once for as long as the window is open, through suspends, network changes
and libvirtd restarts, and has to show each one's state honestly.

go-libvirt gives a raw RPC client and nothing an unattended, long-lived
client needs (virtrigaud ADR-0008, Fact 5): no `context.Context` on any call,
no keepalive, no reconnect, and a race in `ConnectToURI` between the
watcher goroutine it starts and the `disconnected` channel it assigns
afterwards. Its own SSH dialer re-handshakes on every reconnect and reads
neither the system's SSH configuration nor an interactive answer.

## Decision

### URIs

libvirt's own URI syntax, so a URI copied from virsh or virt-manager works:

| URI | Transport |
|---|---|
| `qemu:///system`, `qemu:///session` | this computer's libvirt socket (Linux) |
| `qemu+ssh://[user@]host[:port]/system` | SSH, in process |
| `qemu+unix:///system?socket=/path` | an explicit socket |
| `test:///default`, `test+ssh://…` | libvirt's test driver (tests) |

`qemu+tcp` and `qemu+tls` are later. A `+libssh`/`+libssh2` URI is accepted
as `+ssh`: the transport is ours either way. Once the socket is open, the URI
virt-meow asks libvirt for is the driver part (`qemu:///system`), as libvirt's
own remote driver does.

### Local sockets

- `system`: `/run/libvirt/libvirt-sock`, then `/var/run/libvirt/libvirt-sock`.
  Modular daemons serve this socket through virtproxyd.
- `session`: `$XDG_RUNTIME_DIR/libvirt/virtqemud-sock`, then
  `…/libvirt-sock`. When neither answers, virt-meow starts the daemon as
  libvirt's C client does (go-libvirt doesn't), on Linux only: the
  modular one (`virtqemud`) where it is installed, else `libvirtd`
  (`$LIBVIRTD_PATH` overrides), with `--timeout=120` so it ends once
  unused, detached so that it outlives virt-meow, under a lock on
  `<socket>.lock` so that two clients starting at once start one, then
  waits for its socket within the connect timeout. Inside the Flatpak
  sandbox there is no daemon to start: the error says to start it
  outside.

### SSH, in process

`golang.org/x/crypto/ssh`, one SSH client per host. Every libvirt
connection to that host, and later every console, is a channel on it, so a
reconnect of libvirt is a new channel, not a new handshake. The system's
`ssh` binary is never run, so it behaves the same on Windows.

The libvirt socket is reached through, in order:

1. **`direct-streamlocal@openssh.com`** to the remote socket
   (`/var/run/libvirt/libvirt-sock`), for `system`. No program runs on the
   host. OpenSSH allows it unless `AllowStreamLocalForwarding no`.
2. **`virt-ssh-helper <uri>`** in an exec channel, what libvirt's own client
   runs (`mode=native`). It finds the right daemon itself, modular or not,
   and is the only way to reach a remote `session`.

`?proxy=streamlocal|helper` forces one; the default tries them in that
order.

**The SSH configuration** is read as OpenSSH reads it, so that a URI
working with virsh (`qemu+ssh://alias/system`) works here: the user's
`~/.ssh/config`, then the system's `ssh_config` (`/etc/ssh/`,
`%ProgramData%\ssh\` on Windows), the first value of each setting
winning. `Host` patterns (with `!`), `Include`, and the `Match` criteria
that need no program (`all`, `host`, `originalhost`, `user`, `localuser`;
a block with any other is skipped) select what applies; of the settings,
`HostName`, `User`, `Port`, `IdentityFile`, `IdentitiesOnly`,
`IdentityAgent` and `ProxyJump` are used, with their `%` tokens and `~`. A
user or port in the URI wins over the configuration's. **ProxyJump** dials
each jump host in turn, the next one through a `direct-tcpip` channel on
the one before (the first jump host through its own configured jump
hosts, as ssh does), each with its own login and host key check; the
pooled client is the last one, and its end closes the others. A
**ProxyCommand** of the form `ssh [-q -l user -p port] -W %h:%p host` is
the same as a jump host and read as one; any other command is refused
with an error saying to use ProxyJump, since running a program to
connect is what this design avoids. The files are read on every dial, so
an edit takes effect at the next connection; `virt-mm conn route <conn>`
(and the add-connection form) shows what they make of a host. Host keys
are checked under the address dialled (HostName and port), as ssh does
without `HostKeyAlias`, which isn't read.

**Authentication**, tried in order for each hop: the SSH agent
(`SSH_AUTH_SOCK` or `IdentityAgent`; with `IdentitiesOnly`, only its keys
that match an `IdentityFile`; the Windows agent pipe later), then the
`IdentityFile`s, or the default key files when none is configured
(`~/.ssh/id_ed25519`, `id_ecdsa`, `id_rsa`), then a password or passphrase
asked through the front end.

**Remembered secrets.** A password or key passphrase is asked through the
front end, which offers to remember it when the OS keychain is there: the
Secret Service over the session's D-Bus on Linux (pure Go, a private
connection per use, the "plain" session algorithm, since the bus is the
user's own; never a bus launched for it), the macOS Keychain and Windows'
Credential Manager not yet (they say so). Nothing is remembered without
the user ticking it, and never in a file. What is remembered is tried
before anyone is asked: a password is stored only once the login with it
worked, a passphrase once it opened the key; one that is refused later is
forgotten at once and the user asked again, told why. A connection's menu
(`virt-mm conn forget`) forgets its hops' passwords and its keys'
passphrases, which connections using the same key share. A locked
keychain unlocks through its own desktop prompt; one that can't be
unlocked remembers nothing, and the user is asked.

### Host commands

A few facts libvirt has no API for on most hosts: the host's bridges
(libvirt's interface driver needs netcf or NetworkManager, which most hosts
lack), and its osinfo-db (`tar -cz` of `/usr/share/osinfo`, which virt-meow
reads to recognise installation media; without one it uses its own copy).
Over SSH, `Session.Run` runs a fixed, read-only command on the same
SSH client (`ls`-like reads of sysfs, nothing that changes the host); on a
local connection virt-meow reads this computer's sysfs instead. Nothing a user
types is ever put in such a command, and nothing that changes the host goes
this way: changes go through libvirt, where its access control applies.

### Host keys

- virt-meow keeps its own `known_hosts` in its config directory and also
  **reads** `~/.ssh/known_hosts`, never writing it.
- **Unknown key:** the connection fails with an error that carries the host,
  key type and SHA-256 fingerprint. The front end shows them and, on the
  user's word, virt-meow appends the key to its own file and connects again.
  Nothing is trusted on first use without that answer.
- **Changed key:** the connection fails and stays failed. There is no
  "accept anyway"; the user removes the old key (`virt-mm hostkey forget`)
  after checking why it changed.

### Session upkeep

`conn.Session` owns one connection and is the only thing that touches a
`*libvirt.Libvirt`:

- **Calls take a context.** `Session.Do(ctx, fn)` runs `fn` against the live
  client. If `ctx` ends first, the session closes the transport, which
  unblocks the call, and returns the context's error. Cancellation is
  therefore per connection, not per call: other calls in flight fail too and
  the next call reconnects. go-libvirt offers nothing finer.
- **A stream ends before its connection.** Closing a connection while one
  of its streams is open and the far end still writes can deadlock libvirtd
  itself (9.2 to at least 11.x: `daemonStreamEvent` closes the client under
  a lock its close hook takes again; a fix was posted upstream in September
  2026). The host then answers nothing until libvirtd is restarted. So a
  stream call (the serial console) doesn't hand its context to `Do`: the
  context's end finishes the stream, and the connection is dropped only if
  libvirtd doesn't confirm within a few seconds. Whatever holds a console
  ends it before closing the session.
- **Connecting is serialised.** One attempt at a time, under the session's
  lock. An attempt that runs out of time has its transport closed and is
  waited for before anything else touches the client, so `ConnectToURI`'s
  race is never reached from two goroutines.
- **Liveness comes from the transport.** The session wraps the `net.Conn` it
  dials; the first read or write error on it marks the connection dead. This
  avoids go-libvirt's `Disconnected()`/`IsConnected()`, which read the
  racy field.
- **Keepalive.** Every 30 s an idle connection is probed with
  `ConnectGetLibVersion` under a 10 s deadline; a failed probe closes it.
- **Reconnect.** A dead connection is redialled on the next call, and by the
  keepalive loop with backoff (2 s doubling to 60 s), so the GUI recovers
  without a click. Event subscriptions are made again after each reconnect,
  and are in place by the time the call that connected returns.
- **State is observable.** `connecting`, `connected`, `disconnected` (with
  the last error), and `closed`, delivered to a callback, which the GUI turns
  into events for the page.

## Consequences

- One SSH handshake per host for the life of the window; consoles reuse it.
- A changed host key is never one click away from being accepted.
- A slow call that times out drops its siblings with it. The default
  deadline is generous (30 s) and listing is done in a constant number of
  RPCs, not one per domain (virtrigaud's N+1 lesson), so that stays rare.
- Aliases and jump hosts from the SSH configuration work as with virsh;
  under `virt-mm serve` they are those of the account it runs as. A
  configuration that connects through a program (a ProxyCommand other
  than `ssh -W`, `Match exec`) needs rewriting as ProxyJump.
