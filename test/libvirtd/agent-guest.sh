#!/bin/sh
# Builds the e2e tests' agent guest into /usr/share/virt-mm-e2e: Debian's
# kernel and an initramfs with busybox and Debian's qemu-guest-agent, booted
# straight from them by libvirt (<kernel>, <initrd>) with no disk. Under TCG
# it is up in seconds, which no cloud image is. /init gives the guest a
# hostname, an os-release, a root and an "e2e" user, starts qemu-ga on its
# virtio port and leaves a shell on the serial console, for the tests to
# look at what the agent changed. Booted with virt-mm.nocloud, it does what
# cloud-init's NoCloud source does first: loads the SATA CD and ISO 9660
# drivers, finds the filesystem labelled cidata with blkid as cloud-init
# does, mounts it and takes the hostname
# from its meta-data (it is not cloud-init: users and keys aren't made).
# Booted with virt-mm.virtiofs=<tag>, it mounts that virtiofs folder and
# lists it on the console.
set -eu
out=/usr/share/virt-mm-e2e
root=$(mktemp -d)
mkdir -p "$out" "$root/bin" "$root/sbin" "$root/etc" "$root/proc" "$root/sys" "$root/dev" \
	"$root/run" "$root/tmp" "$root/root" "$root/home/e2e"
cp /boot/vmlinuz-* "$out/vmlinuz"

cp /bin/busybox "$root/bin/busybox"
for a in $("$root/bin/busybox" --list); do
	[ "$a" = busybox ] || ln -s busybox "$root/bin/$a"
done
# qemu-ga runs chpasswd for guest-set-user-password, with "user:password"
# lines on its stdin (-e: already hashed). Debian's busybox has no
# chpasswd and shadow's needs PAM, so this stands in for it.
cat > "$root/sbin/chpasswd" <<'CHPASSWD'
#!/bin/sh
enc=
[ "${1-}" = -e ] && enc=1
while IFS= read -r line; do
	user=${line%%:*} pw=${line#*:}
	grep -q "^$user:" /etc/shadow || { echo "chpasswd: no user $user" >&2; exit 1; }
	[ -n "$enc" ] || pw=$(mkpasswd -m sha512 "$pw") || exit 1
	sed -i "s|^$user:[^:]*:|$user:$pw:|" /etc/shadow
done
CHPASSWD
chmod 755 "$root/sbin/chpasswd"

# qemu-ga, and util-linux's blkid for virt-mm.nocloud (busybox's findfs
# reads no ISO 9660 labels), with their libraries.
for bin in /usr/sbin/qemu-ga "$(command -v blkid)"; do
	cp "$bin" "$root/sbin/"
	for lib in $(ldd "$bin" | grep -o '/[^ ]*'); do
		mkdir -p "$root$(dirname "$lib")"
		cp -L "$lib" "$root$lib"
	done
done

# The CD and its filesystem, for virt-mm.nocloud, and virtiofs: the modules
# ahci, sr_mod, isofs and virtiofs need, in the order modprobe would load
# them, uncompressed for busybox's insmod.
kver=$(ls /lib/modules | head -n1)
mkdir -p "$root/lib/modules"
for m in ahci sr_mod isofs virtiofs; do modprobe -D -S "$kver" "$m"; done |
	awk '$1 == "insmod" && !seen[$2]++ { print $2 }' | while read -r ko; do
	base=${ko##*/}
	case $ko in
	*.xz) "$root/bin/busybox" unxz -c "$ko" > "$root/lib/modules/${base%.xz}"; base=${base%.xz} ;;
	*) cp "$ko" "$root/lib/modules/$base" ;;
	esac
	echo "$base" >> "$root/lib/modules/order"
done

cp /usr/lib/os-release "$root/etc/os-release"
printf '%s\n' 'root:x:0:0:root:/root:/bin/sh' 'e2e:x:1000:1000:e2e:/home/e2e:/bin/sh' > "$root/etc/passwd"
printf '%s\n' 'root:x:0:' 'e2e:x:1000:' > "$root/etc/group"
printf '%s\n' 'root:!:20000::::::' 'e2e:!:20000::::::' > "$root/etc/shadow"
chmod 600 "$root/etc/shadow"
chown 1000:1000 "$root/home/e2e"

cat > "$root/init" <<'INIT'
#!/bin/sh
export PATH=/bin:/sbin
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
hostname virt-mm-agent-guest
if grep -qE 'virt-mm\.(nocloud|virtiofs)' /proc/cmdline; then
	for m in $(cat /lib/modules/order); do insmod "/lib/modules/$m"; done
fi
if grep -qw virt-mm.nocloud /proc/cmdline; then
	i=0 dev=
	until dev=$(blkid -t LABEL=cidata -o device) || [ "$i" -gt 50 ]; do i=$((i + 1)); sleep 0.2; done
	if [ -n "$dev" ] && mkdir -p /run/cidata && mount -t iso9660 -o ro "$dev" /run/cidata; then
		h=$(sed -n 's/^local-hostname: *"*\([^"]*\)"*$/\1/p' /run/cidata/meta-data)
		[ -n "$h" ] && hostname "$h"
		echo "virt-mm-e2e: cidata on $dev: $(ls /run/cidata | tr '\n' ' ')hostname $h"
	else
		echo "virt-mm-e2e: no cidata"
	fi
fi
tag=$(sed -n 's/.*virt-mm\.virtiofs=\([^ ]*\).*/\1/p' /proc/cmdline)
if [ -n "$tag" ]; then
	mkdir -p /mnt/fs
	if mount -t virtiofs "$tag" /mnt/fs; then
		echo "virt-mm-e2e: virtiofs $tag: $(ls /mnt/fs | tr '\n' ' ')"
	else
		echo "virt-mm-e2e: virtiofs $tag failed"
	fi
fi
port=
for p in /sys/class/virtio-ports/*; do
	[ "$(cat "$p/name" 2>/dev/null)" = org.qemu.guest_agent.0 ] && port=/dev/${p##*/}
done
if [ -n "$port" ]; then
	qemu-ga -m virtio-serial -p "$port" -t /run &
	echo "virt-mm-e2e: qemu-ga on $port"
else
	echo "virt-mm-e2e: no agent port"
fi
exec setsid cttyhack sh
INIT
chmod 755 "$root/init"
(cd "$root" && find . | cpio -o -H newc --quiet | gzip -9) > "$out/initrd.gz"
rm -rf "$root"
