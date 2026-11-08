#!/bin/sh
# The release archives of the CLI (nogui, CGO_ENABLED=0): linux, darwin and
# windows on amd64 and arm64, each with LICENSE and README.md, and their
# SHA256SUMS, in dist/release:
#
#   virt-mm_<version>_<os>_<arch>.tar.gz   (windows: .zip, with virt-mm.exe)
#
#   VERSION=0.1.0 scripts/dist.sh       (make dist VERSION=0.1.0)
#
# VERSION defaults to scripts/version.sh's; the binary says it as given, the
# file names without a leading "v". The GUI's release files are built by
# .github/workflows/build.yml.
# DIST_TARGETS='linux/amd64 …' builds fewer.
set -eu
cd "$(dirname "$0")/.."
version=${VERSION:-$(scripts/version.sh)}
v=${version#v}
targets=${DIST_TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}
out=dist/release
work=$(mktemp -d "${TMPDIR:-/tmp}/virt-mm-dist.XXXXXX")
trap 'rm -rf "$work"' EXIT

# Its own files only: make windows puts its zips here too.
mkdir -p "$out"
rm -f "$out"/virt-mm_* "$out/SHA256SUMS"
# The archives' entries get this time and owner, whoever builds them when.
mtime=$(git log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
for t in $targets; do
	os=${t%/*} arch=${t#*/}
	name=virt-mm_${v}_${os}_${arch}
	exe=virt-mm
	[ "$os" = windows ] && exe=virt-mm.exe
	dir=$work/$name
	mkdir -p "$dir"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -tags nogui -trimpath \
		-ldflags="-s -w -X main.version=$version" -o "$dir/$exe" ./cmd/virt-mm
	cp LICENSE README.md "$dir/"
	if [ "$os" = windows ]; then
		archive=$out/$name.zip
		(cd "$dir" && python3 -I -m zipfile -c "$OLDPWD/$archive" "$exe" LICENSE README.md)
	else
		archive=$out/$name.tar.gz
		tar -C "$dir" --sort=name --owner=0 --group=0 --numeric-owner --mtime="$mtime" \
			-cf - "$exe" LICENSE README.md | gzip -n >"$archive"
	fi
	echo "dist: $archive"
done
(cd "$out" && sha256sum -- virt-mm_* >SHA256SUMS)
echo "dist: $out/SHA256SUMS"
