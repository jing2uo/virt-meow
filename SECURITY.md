# Security

## Reporting a vulnerability

Please report it privately, through GitHub: the repository's **Security**
tab → **Report a vulnerability**
([private vulnerability reporting](https://github.com/jing2uo/virt-meow/security/advisories/new)).
Do not open a public issue for it. Say what is affected, how to reproduce
it and which version or commit you saw it in; you will get an answer, and
credit in the advisory if you want it.

## Supported versions

Only the latest release (and `main`) gets fixes. virt-meow is alpha:
upgrade rather than expect a backport.

## What is sensitive

virt-meow controls virtual machines, so whoever controls it does too. The
areas where a bug matters most:

- **`virt-mm serve` on a network.** It serves the full management pages
  and consoles over HTTP and a WebSocket. On loopback it can run without a
  login; beyond loopback it demands a password, and should be given
  `--tls-cert`/`--tls-key` (or sit behind a TLS proxy). Bypassing the
  login, the session cookie, cross-site requests and the WebSocket's
  origin checks are all in scope.
- **SSH host key checking.** Remote hosts are reached by SSH in process.
  An unknown host key is trusted only after the user confirms its
  fingerprint, and a changed key is refused (never accepted from the GUI).
  Anything that gets a connection made to a host with a key the user did
  not accept is a vulnerability.
- **Passwords in the keychain.** SSH passwords and key passphrases the user
  asks to remember are stored in the system keychain (Secret Service on
  Linux), never in virt-meow's config files. Leaking them into files, logs,
  the page or `--json` output is in scope.
- **Console tokens.** VNC, SPICE and serial consoles are proxied to the page
  through random tokens, valid until the console is closed or left idle.
  Guessing, leaking or outliving one to reach a console is in scope, as are VNC passwords set on machines.

Out of scope: libvirt, QEMU and the guests themselves (report those
upstream), and what a user who is already allowed to manage a host can do
to it.
