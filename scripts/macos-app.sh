#!/bin/sh
# virt-meow.app from the GUI built for darwin/amd64 and darwin/arm64 (make
# gui-native on a Mac), as one universal binary, zipped with LICENSE and
# README.md:
#
#   VERSION=0.1.0 scripts/macos-app.sh <amd64 binary> <arm64 binary> <out.zip>
#
# On macOS only (lipo, sips, iconutil, codesign, ditto). Not signed with an
# identity nor notarized: signed ad hoc, which Apple silicon needs to run it
# at all, so a downloaded copy is "from an unidentified developer", not
# "damaged" (README.md says how to open it).
set -eu
cd "$(dirname "$0")/.."
amd64=$1 arm64=$2 out=$(cd "$(dirname "$3")" && pwd)/$(basename "$3")
version=${VERSION:-$(scripts/version.sh)}
# 0.1.0 of 0.1.0-3-gabc1234: the bundle versions are numbers.
short=${version%%-*}
work=$(mktemp -d "${TMPDIR:-/tmp}/virt-mm-macos.XXXXXX")
trap 'rm -rf "$work"' EXIT

app=$work/zip/virt-meow.app
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
lipo -create -output "$app/Contents/MacOS/virt-mm" "$amd64" "$arm64"
sed -e "s/@VERSION@/$version/g" -e "s/@SHORT_VERSION@/$short/g" \
	packaging/macos/Info.plist >"$app/Contents/Info.plist"
plutil -lint "$app/Contents/Info.plist"
printf 'APPL????' >"$app/Contents/PkgInfo"

# The icon, from the Flatpak's 512x512 PNG.
set=$work/virt-meow.iconset
mkdir "$set"
png=packaging/flatpak/io.github.jing2uo.VirtMeow.png
for s in 16 32 128 256 512; do
	sips -z "$s" "$s" "$png" --out "$set/icon_${s}x${s}.png" >/dev/null
	[ "$s" = 512 ] || sips -z $((s * 2)) $((s * 2)) "$png" --out "$set/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns -o "$app/Contents/Resources/virt-meow.icns" "$set"

codesign --force --sign - "$app"
codesign --verify --verbose "$app"
lipo -info "$app/Contents/MacOS/virt-mm"
"$app/Contents/MacOS/virt-mm" version

cp LICENSE README.md "$work/zip/"
rm -f "$out"
(cd "$work/zip" && ditto -c -k --sequesterRsrc . "$out")
echo "macos-app: $out"
