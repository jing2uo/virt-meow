#!/bin/sh
# Runs virt-mm serve on a libvirtd in a container, to work on the pages
# against: the e2e image, reached over SSH as a remote host is (its VNC
# consoles are on the container's loopback), with a default pool and one
# small machine. Nothing on the host's libvirt, nothing on a real host.
# Ctrl+C stops it and removes the container and its files.
#
#   test/dev-serve.sh [port]       serve on 127.0.0.1:port (18080)
#
# While it runs, $work/env (printed at the start) has the VIRT_MM_CONFIG,
# HOME, SSH_AUTH_SOCK and D-Bus session (whose keychain is its own) to
# point dist/virt-mm-cli at the same host. The user pw logs in to it with
# the password e2e-password.
set -eu
cd "$(dirname "$0")/.."
port=${1:-18080}
name=virt-mm-dev-$$
work=$(mktemp -d "${TMPDIR:-/tmp}/virt-mm-dev.XXXXXX")
cleanup() {
	[ -n "${serve:-}" ] && kill "$serve" 2>/dev/null && wait "$serve" 2>/dev/null
	podman rm -f "$name" "$name-secrets" >/dev/null 2>&1 || true
	podman run --rm --entrypoint /bin/sh -v "$work/run:/w:z" virt-mm-libvirtd \
		-c 'rm -rf /w/* /w/.[!.]*' >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

make -s cli
podman build -q -t virt-mm-libvirtd test/libvirtd >/dev/null
mkdir -p "$work/run" "$work/home/.ssh" "$work/cfg"
ssh-keygen -q -t ed25519 -N '' -C virt-mm-dev -f "$work/home/.ssh/id_ed25519"
podman run -d --rm --name "$name" -p 127.0.0.1::2222 \
	--cap-add NET_ADMIN,NET_RAW --security-opt unmask=/proc/sys --device /dev/net/tun \
	-e AUTHORIZED_KEY="$(cat "$work/home/.ssh/id_ed25519.pub")" \
	-v "$work/run:/run/libvirt:z" virt-mm-libvirtd >/dev/null
# A keychain of its own (a session bus with GNOME Keyring), not the user's.
mkdir -p "$work/bus"
podman run -d --rm --name "$name-secrets" --userns=keep-id --user "$(id -u):$(id -g)" \
	-v "$work/bus:/bus:z" --entrypoint /bin/sh virt-mm-libvirtd /secrets.sh >/dev/null
ssh=$(podman port "$name" 2222/tcp | head -n1 | sed 's/.*://')
i=0
until [ -S "$work/run/libvirt-sock" ]; do
	i=$((i + 1))
	[ "$i" -gt 100 ] && { podman logs "$name"; echo "libvirtd did not start" >&2; exit 1; }
	sleep 0.1
done
podman exec "$name" sh -c 'virsh -q pool-define-as default dir --target /var/lib/libvirt/images &&
	virsh -q pool-build default && virsh -q pool-start default && virsh -q pool-autostart default' >/dev/null

cat > "$work/env" <<ENV
export VIRT_MM_CONFIG=$work/cfg HOME=$work/home SSH_AUTH_SOCK= DBUS_SESSION_BUS_ADDRESS=unix:path=$work/bus/bus
ENV
. "$work/env"
h=./dist/virt-mm-cli
$h conn add "qemu+ssh://root@127.0.0.1:$ssh/system" --name dev --autoconnect >/dev/null
echo y | $h hostkey trust dev >/dev/null
$h create web01 -c dev --disk default:1G --cpus 1 --memory 256M --net none --firmware bios --start >/dev/null

echo "virt-mm dev: http://127.0.0.1:$port/  (the CLI: . $work/env; ./dist/virt-mm-cli … -c dev)  Ctrl+C stops"
$h serve --listen "127.0.0.1:$port" &
serve=$!
wait "$serve"
