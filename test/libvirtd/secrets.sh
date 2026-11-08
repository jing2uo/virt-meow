#!/bin/sh
# A D-Bus session bus on /bus/bus with GNOME Keyring's Secret Service on
# it, its login keyring made and unlocked with the password "e2e": the
# keychain virt-meow remembers secrets in, for the tests, never the user's.
# test/e2e.sh runs it as the host user (--userns=keep-id), so that the
# bus's EXTERNAL authentication takes the tests' uid. /bus/ready says the
# keychain is up.
set -e
export HOME=/tmp/home XDG_RUNTIME_DIR=/tmp/run
mkdir -p "$HOME" "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR"
dbus-daemon --session --nofork --address=unix:path=/bus/bus &
bus=$!
until [ -S /bus/bus ]; do sleep 0.1; done
export DBUS_SESSION_BUS_ADDRESS=unix:path=/bus/bus
printf e2e | gnome-keyring-daemon --unlock --components=secrets >/dev/null
touch /bus/ready
wait "$bus"
