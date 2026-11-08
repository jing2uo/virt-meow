#!/bin/sh
# AUTHORIZED_KEY: the public key the tests log in with. libvirtd runs in a
# loop so a test can kill it and watch virt-meow reconnect. A second sshd, with
# a host key of its own, listens only on the container's loopback (2223):
# a host behind a jump host, reached through the first one (ProxyJump).
set -e
ssh-keygen -A >/dev/null
[ -f /etc/ssh/behind_host_ed25519_key ] || ssh-keygen -q -t ed25519 -N '' -f /etc/ssh/behind_host_ed25519_key
[ -n "$AUTHORIZED_KEY" ] && printf '%s\n' "$AUTHORIZED_KEY" > /root/.ssh/authorized_keys
/usr/sbin/sshd -p "${SSH_PORT:-2222}"
/usr/sbin/sshd -o ListenAddress=127.0.0.1:2223 -o PidFile=/run/sshd-behind.pid -h /etc/ssh/behind_host_ed25519_key
/usr/sbin/virtlogd -d  # QEMU logs through it
while :; do /usr/sbin/libvirtd || true; sleep 0.2; done
