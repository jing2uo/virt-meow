#!/bin/sh
# Runs the e2e tests (test/e2e) against libvirtd in a container: builds the
# image, starts it with a fresh SSH key and a free port, and a keychain in
# a second one, runs the tests, removes them.
set -eu
cd "$(dirname "$0")/.."

name=virt-mm-e2e-$$
work=$(mktemp -d "${TMPDIR:-/tmp}/virt-mm-e2e.XXXXXX")
# libvirtd leaves files in run/ owned by the container's root, which the
# host user can't delete: a throwaway container removes them.
cleanup() {
	podman rm -f "$name" "$name-secrets" >/dev/null 2>&1 || true
	podman run --rm --entrypoint /bin/sh -v "$work/run:/w:z" virt-mm-libvirtd \
		-c 'rm -rf /w/* /w/.[!.]*' >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

podman build -q -t virt-mm-libvirtd test/libvirtd >/dev/null
ssh-keygen -q -t ed25519 -N '' -C virt-mm-e2e -f "$work/key"
mkdir "$work/run"
# NET_ADMIN, NET_RAW, a writable /proc/sys and the tun device let libvirt
# make bridges, taps and nftables rules for virtual networks: all within the
# container's own user and network namespaces, nothing on the host.
podman run -d --rm --name "$name" \
	-p 127.0.0.1::2222 \
	--cap-add NET_ADMIN,NET_RAW --security-opt unmask=/proc/sys --device /dev/net/tun \
	-e AUTHORIZED_KEY="$(cat "$work/key.pub")" \
	-v "$work/run:/run/libvirt:z" \
	virt-mm-libvirtd >/dev/null

# A keychain of the tests' own: a session bus with GNOME Keyring on it,
# which every virt-mm the tests run uses instead of the user's.
mkdir "$work/bus"
podman run -d --rm --name "$name-secrets" --userns=keep-id --user "$(id -u):$(id -g)" \
	-v "$work/bus:/bus:z" --entrypoint /bin/sh virt-mm-libvirtd /secrets.sh >/dev/null

port=$(podman port "$name" 2222/tcp | head -n1 | sed 's/.*://')
i=0
until [ -S "$work/run/libvirt-sock" ]; do
	i=$((i + 1))
	[ "$i" -gt 100 ] && { podman logs "$name"; echo "libvirtd did not start" >&2; exit 1; }
	sleep 0.1
done

i=0
until [ -e "$work/bus/ready" ]; do
	i=$((i + 1))
	[ "$i" -gt 100 ] && { podman logs "$name-secrets"; echo "the keychain did not start" >&2; exit 1; }
	sleep 0.1
done

DBUS_SESSION_BUS_ADDRESS="unix:path=$work/bus/bus" \
VIRT_MM_E2E_DBUS="unix:path=$work/bus/bus" \
VIRT_MM_E2E_RUN="$work/run" \
VIRT_MM_E2E_SSH="127.0.0.1:$port" \
VIRT_MM_E2E_KEY="$work/key" \
VIRT_MM_E2E_CONTAINER="$name" \
	go test -tags 'e2e nogui' -count=1 "$@" ./test/e2e/
