# virt-meow has two builds:
#   make cli   static, GUI-less binary (pure Go, CGO_ENABLED=0), any platform
#   make gui   the Wails GUI for Linux, built in a podman container holding the
#              GTK/WebKit -dev packages, so the host needs none of them
#   make gui-native  the GUI with this host's toolchain, for GOOS/GOARCH
#              (Linux: the -dev packages; macOS: Xcode's clang; Windows: none,
#              it needs no cgo); what CI builds with
#   make windows  the Windows GUI, amd64 and arm64, zipped in dist/release
#   make test  unit tests;  make e2e  against libvirtd in a container, then
#   make release-check  virt-mm built from git archive HEAD, its pages looked at
#   make dist  the CLI for linux/darwin/windows x amd64/arm64, archived, with SHA256SUMS
#
# The command is in cmd/virt-mm, so `go install …/cmd/virt-mm` names it virt-mm.
# The GUI and the CLI are the same binary: with no command it opens the window.

IMAGE    ?= virt-mm-build
GOROOT   := $(shell go env GOROOT)
MODCACHE := $(shell go env GOMODCACHE)
# 0.1.0 on a release tag, 0.1.0-3-gabc1234 after it: scripts/version.sh.
VERSION  ?= $(or $(shell scripts/version.sh 2>/dev/null),dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
PKG      := ./cmd/virt-mm

# --user keeps the output owned by us (with --userns=keep-id under rootless
# podman too, where uids are otherwise shifted). The host Go toolchain is mounted, so
# the image needs none; the module cache is shared, the build cache is the
# container's own (different cgo flags).
PODMAN_RUN = podman run --rm --userns=keep-id --user "$$(id -u):$$(id -g)" \
	-v "$(CURDIR)":/work -w /work \
	-v "$(GOROOT)":/opt/go:ro \
	-v "$(MODCACHE)":/go/pkg/mod \
	-v "$(CURDIR)/.cache/go-build":/go/cache \
	-e GOMODCACHE=/go/pkg/mod -e GOCACHE=/go/cache -e HOME=/tmp \
	$(IMAGE)

.PHONY: cli gui gui-native winres windows image test e2e release-check vet dist clean

cli:
	CGO_ENABLED=0 go build -tags nogui -trimpath -ldflags="$(LDFLAGS)" -o dist/virt-mm-cli $(PKG)

image:
	podman build -t $(IMAGE) -f scripts/Containerfile scripts

gui: | .cache/go-build
	$(PODMAN_RUN) go build -tags production -trimpath -ldflags="$(LDFLAGS)" -o dist/virt-mm $(PKG)

.cache/go-build:
	mkdir -p $@

# The GUI, by the host's go for its GOOS/GOARCH (from the environment), to
# GUI_OUT. Windows' is a GUI-subsystem program (no console window of its
# own); cmd/virt-mm/console_windows.go gives its commands the terminal's.
GOOS_    := $(shell go env GOOS)
GUI_OUT  ?= dist/virt-mm$(if $(filter windows,$(GOOS_)),.exe)
gui-native:
	go build -tags production -trimpath -ldflags="$(LDFLAGS)$(if $(filter windows,$(GOOS_)), -H windowsgui)" -o $(GUI_OUT) $(PKG)

# Windows' icon, version and manifest, as cmd/virt-mm/rsrc_windows_*.syso,
# which go build links into Windows builds only (by their names). Made per
# build, for the version; not in git.
WINRES := go run github.com/tc-hib/go-winres@v0.3.3
winres:
	$(WINRES) make --in packaging/windows/winres.json --out cmd/virt-mm/rsrc --arch amd64,arm64 \
		--file-version "$(VERSION)" --product-version "$(VERSION)"

# dist/release/virt-mm-windows-<arch>.zip: virt-mm.exe, LICENSE, README.md.
windows: winres
	for a in amd64 arm64; do \
		rm -rf dist/windows-$$a && mkdir -p dist/windows-$$a dist/release && \
		GOOS=windows GOARCH=$$a CGO_ENABLED=0 $(MAKE) -s gui-native GUI_OUT=dist/windows-$$a/virt-mm.exe && \
		cp LICENSE README.md dist/windows-$$a/ && \
		rm -f dist/release/virt-mm-windows-$$a.zip && \
		(cd dist/windows-$$a && python3 -I -m zipfile -c ../release/virt-mm-windows-$$a.zip virt-mm.exe LICENSE README.md) || exit 1; \
	done

vet:
	CGO_ENABLED=0 go vet -tags nogui ./...
	go vet -tags 'e2e nogui' ./test/e2e/
	$(PODMAN_RUN) go vet ./internal/gui/

test:
	CGO_ENABLED=0 go vet -tags nogui ./... && go test -race -tags nogui ./...

e2e:
	./test/e2e.sh -race
	./scripts/release-check.sh

# What a clone builds: the committed files only, served and looked at in
# headless Chrome (a JS error or a file missing from git fails it).
release-check:
	./scripts/release-check.sh

# Release archives in dist/release: see scripts/dist.sh.
dist:
	VERSION=$(VERSION) ./scripts/dist.sh

clean:
	rm -rf dist .cache cmd/virt-mm/rsrc_windows_*.syso

FLATPAK_ID := io.github.jing2uo.VirtMeow
MANIFEST   := packaging/flatpak/$(FLATPAK_ID).yml

.PHONY: flatpak
# Build and (re)install the local Flatpak, the way to run the GUI on Linux
# while developing it. Users get it from FlatPark, which packages the
# release's virt-mm-linux-* files.
flatpak: gui
	flatpak run org.flatpak.Builder --force-clean --disable-rofiles-fuse --repo=.cache/repo .cache/flatpak-build $(MANIFEST)
	flatpak build-bundle --runtime-repo=https://dl.flathub.org/repo/flathub.flatpakrepo .cache/repo .cache/virt-mm.flatpak $(FLATPAK_ID)
	flatpak install --user -y --noninteractive --reinstall --bundle .cache/virt-mm.flatpak
