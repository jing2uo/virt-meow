# Roadmap

What virt-meow still lacks to replace virt-manager for daily use, most
needed first. [docs/spec/p0.md](docs/spec/p0.md) has the milestones and
[docs/spec/virsh.md](docs/spec/virsh.md) tracks the CLI against virsh.

## Known gaps in what is there

- **Tested in a container, little on real hosts.** `make e2e` runs every
  capability against libvirtd and QEMU (TCG) in a container; KVM-only
  behaviour (`host-model`, `host-passthrough`) is unit-tested, not run.
- **The desktop window on macOS and Windows is experimental.** Releases
  build it (a universal `virt-meow.app`; `virt-mm.exe` for amd64 and arm64),
  but it is not signed nor notarized and hasn't been tried on a real Mac or
  PC. Remembered secrets are Linux-only (the Secret Service: GNOME Keyring,
  KWallet, KeePassXC); the macOS Keychain and Windows Credential Manager are
  stubs that say "not yet". The console's keyboard grab has no native
  implementation there: on Windows the page uses WebView2's Keyboard Lock
  in full screen; macOS' WebKit has none, so keys the desktop keeps are sent
  from the console's Keys menu.
  The native file dialogs take their language from `$LANG`, which a
  desktop app on either rarely has. On Windows, the window's `virt-mm.exe`
  runs commands in a terminal without the terminal waiting for it (the CLI
  archive's doesn't have that caveat).
- **The window is checked by hand on Linux.** Its keyboard grab, clipboard
  and keychain prompts are not covered by the tests.
- **Messages in the page's language.** Some errors from libvirt and from
  the core reach the Chinese page in English.
- **A rare dropped connection in e2e.** A test now and then loses its
  libvirt connection (`procedure interrupted`, `connection reset`); the
  cause, libvirtd restarting or closing clients, isn't found yet.

## virt-manager's details and dialogs

- Disk options: cache and IO mode, discard, detect-zeroes, serial,
  read-only, shareable; the bus of a disk on a machine that is off.
- Change a NIC: its source (network, bridge, macvtap, user), model and MAC.
- The rest of "Add hardware": input devices, watchdog, serial, parallel and
  console ports, channels, controllers, panic notifier, vsock.
- Direct kernel boot: kernel, initrd, DTB and command line.
- More ways to install: from a URL (as `virt-install --location`), network
  boot (PXE), an empty machine; other architectures and machine types.
- Delete and clone disk by disk: which disks go, are copied or shared.
- External snapshots, where internal ones can't be.
- More storage: fs, disk, iscsi, rbd and zfs pools, with secrets; volumes
  with a backing store; wipe.
- More networks: IPv6, DNS domain and hosts, static routes, macvtap pools.
- Preferences: stats interval, which graphs the list shows, console scaling.

## Later

- Live and offline migration between connections.
- Backups, full and incremental.
- Signed and notarized builds for macOS and Windows; Flathub.
- Cluster mode, with [virtrigaud](https://github.com/projectbeskar/virtrigaud)
  as one of its back ends.
