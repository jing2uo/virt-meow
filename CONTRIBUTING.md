# Contributing

Thank you for helping. Issues and pull requests are welcome; for anything
bigger than a fix, open an issue first so the design can be agreed on.

## Design rules

The full list is in [AGENTS.md](AGENTS.md); the ones a change most often
meets:

- **ADRs first.** Decisions are in [docs/adr/](docs/adr/), scope and
  milestones in [docs/spec/p0.md](docs/spec/p0.md). A change that goes
  against an ADR updates or supersedes it in the same commit.
- **The CLI first** ([ADR-0003](docs/adr/0003-front-ends.md)): a capability
  goes into `internal/hv`, a CLI command and an e2e test before any page;
  [docs/spec/virsh.md](docs/spec/virsh.md) tracks it.
- **A pure Go core.** Everything but `internal/gui` builds with
  `CGO_ENABLED=0 -tags nogui`, and nothing Linux-only goes into it
  ([ADR-0001](docs/adr/0001-architecture.md)).
- **Constant RPC counts**: listing N machines must not cost N round trips.
- **Host keys are never accepted without the user.**

## Checks

```sh
gofmt -l .          # empty
make test           # vet + unit tests, -race
make e2e            # libvirtd + QEMU (TCG) in a podman container, then release-check
make image gui      # the Linux GUI compiles (in a container)
```

`make e2e` needs podman, and node with Chrome or Chromium for the page
checks. Working on the pages: `test/dev-serve.sh` runs `virt-mm serve`
against a libvirtd in a container, and `scripts/page-shot.sh` screenshots
a page in headless Chrome (see AGENTS.md).

## Commits

```
area: what changed

Why it changed: what was wrong or missing, and why this way.
```

`area` is the package or part touched (`hv`, `ui`, `conn`, `serve`,
`docs` …; several joined by commas). One logical change per commit.

## The page's strings

Every new string the page shows goes into
[`internal/ui/assets/i18n.js`](internal/ui/assets/i18n.js) in both English
and Chinese.

## Releasing

Versions are `vMAJOR.MINOR.PATCH` tags; `scripts/version.sh` says the
current one (`0.1.0`, `0.1.0-3-gabc1234` after it) and `scripts/version.sh
next minor` the next.

- **From GitHub:** Actions → **release** → **Run workflow**, pick the bump
  (patch, minor or major). The tag is made on the run's commit.
- **Or push a tag:** `git tag v0.2.0 && git push origin v0.2.0`.

Either way `.github/workflows/release.yml` builds every platform
(`build.yml`), publishes the GitHub release with all the files and
`SHA256SUMS` (published at once, not a draft nor a prerelease), and tells
FlatPark, which then packages the new `virt-meow-linux-*`. A commit already
released is refused.

The release files keep their names from one version to the next, with no
version in them (the table in the README lists them): `virt-meow-*` is the
app, window and command line in one binary; `virt-mm-*` the command line
alone; the arch is `x86_64` or `arm64`. FlatPark finds its files by name,
as can a `releases/latest/download/<name>` link: a rename goes to
FlatPark's package (`registry/io.github.jing2uo.VirtMeow/resolve-update.sh`)
first, or its next update fails.

## License

By contributing you agree that your contribution is licensed under the
[MIT License](LICENSE).
