# Agent instructions

virt-meow is a cross-platform libvirt VM manager: Go core, Wails v3 GUI,
go-libvirt over a Unix socket or in-process SSH.

- **Design first.** Decisions are in [`docs/adr/`](docs/adr/), scope and
  milestones in [`docs/spec/p0.md`](docs/spec/p0.md). A change that goes
  against an ADR updates or supersedes it in the same commit.
- **Layers** ([ADR-0001](docs/adr/0001-architecture.md)):
  - `internal/conn`: the only code that touches a `*libvirt.Libvirt`
    connection's lifecycle. Calls go through `Session.Do(ctx, …)`; never call
    go-libvirt's `Disconnected`/`IsConnected` or `ConnectToURI` anywhere else
    ([ADR-0002](docs/adr/0002-connections-and-ssh.md) says why).
  - `internal/hv`: what virt-meow does with libvirt; returns virt-meow's own types,
    not go-libvirt's.
  - `internal/app`: the service API (Manager), shared by the CLI and the GUI.
  - `internal/api`: what the page calls (`api.Service`, every exported
    method reachable from it) and the events; shared by both front ends.
  - `internal/ui`: the page (`assets/`, plain ES modules, no bundler); it
    reaches Go only through `rpc.js`.
  - `internal/gui`: the Wails window binding `api.Service`;
    `internal/web`: `virt-mm serve`, the same over HTTP and a WebSocket.
  - `cmd/virt-mm`: the command (`package main`): the CLI, and the window
    unless built with `-tags nogui`.
- **The CLI first** ([ADR-0003](docs/adr/0003-front-ends.md)): a capability
  goes into `internal/hv`, a CLI command and an e2e test before any page;
  [`docs/spec/virsh.md`](docs/spec/virsh.md) tracks it. The page talks to Go
  only through `call()` and the event subscription, so it can run in a
  browser under `virt-mm serve` too.
- **Keep the core pure Go.** Everything but `internal/gui` builds with
  `CGO_ENABLED=0 -tags nogui`. Nothing Linux-only goes into the core: guard
  it by `runtime.GOOS`, or keep it out ([ADR-0001](docs/adr/0001-architecture.md),
  "Cross-platform rules").
- **Constant RPC counts.** Listing N machines must not cost N round trips over
  SSH; use `ConnectListAllDomains` / `ConnectGetAllDomainStats` and fill gaps
  concurrently.
- **Host keys are never accepted without the user**, and a changed key never
  from the GUI.

## Checks

```sh
make test   # vet + unit tests, -race
make e2e    # libvirtd (test driver) + sshd in a container; covers SSH,
            # host keys, events, reconnect, call timeouts; then
            # release-check
make release-check  # virt-mm built from git archive HEAD under dev-serve,
            # its list, a machine and a VNC console in headless Chrome:
            # a JS error or a file missing from git fails it
make gui    # the GUI compiles (in the build container)
make dist   # the CLI's release archives and SHA256SUMS in dist/release
```

CI is in `.github/workflows`: build.yml builds every platform's GUI (Linux
on debian:sid, macOS universal, Windows cross-compiled) and the CLI;
ci.yml runs the checks and build.yml; release.yml (by hand with a bump, or
a `vX.Y.Z` tag) publishes a release and tells FlatPark. Its file names are
fixed (CONTRIBUTING.md, "Releasing"). Check them with
`go run github.com/rhysd/actionlint/cmd/actionlint@latest`. Anything
Linux-only in `internal/gui` goes in a `_linux.go` file with a stub for
the others; `make windows` and `GOOS=darwin go vet -tags nogui ./...` check
the rest builds.

Working on the pages: `test/dev-serve.sh` runs `virt-mm serve` on a
libvirtd in a container, and `scripts/page-shot.sh <url> <png>` takes a
screenshot of a page in headless Chrome, with its JS errors and failed
requests (`SHOT_SIZE`, `SHOT_SCHEME=dark`, `SHOT_LANG=en-US`, `SHOT_DO`
for a click, `SHOT_KEYS` for real key presses).

The GUI runs on Linux through the local Flatpak (`make flatpak`, then
`flatpak run io.github.jing2uo.VirtMeow`); the host has no WebKitGTK 6. Point
it at a scratch config with `--env=VIRT_MM_CONFIG=…` when testing.

Commit messages: `area: what changed`, then a body saying why.
