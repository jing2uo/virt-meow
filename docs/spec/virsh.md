# virsh → virt-mm

Every command of `virsh help` (libvirt 12.8.0), and what the CLI does about it. The CLI
covers what virsh does, the commonly used part of it; what virsh can't do from its own commands
goes to the pages, with a CLI command or flag where one reads naturally (`virt-mm create
--cloud-init`, `--os-variant`, `virt-mm device add <m> hostdev`, `virt-mm fs`).
[ADR-0003](../adr/0003-front-ends.md) says why the CLI comes first.

✓ done · in the CLI next ◦ after P0 ✗ not in the CLI (the reason is under each group)

Changes to a machine follow one rule: what can change on a running machine changes now **and** in
its definition; the rest goes into the definition only, and the command says it takes effect at
the next start.

## Domain Management: 43 ✓, 0 ·, 7 ◦, 66 ✗ of 116

| virsh | virt-mm | |
|---|---|---|
| `attach-device` | `virt-mm device attach <file>` | ✓ |
| `attach-disk` | `virt-mm disk add` | ✓ |
| `attach-interface` | `virt-mm nic add` | ✓ |
| `autostart` | `virt-mm set --autostart` | ✓ |
| `blockresize` | `virt-mm disk resize` | ✓ |
| `change-media` | `virt-mm cdrom insert|eject` | ✓ |
| `console` | `virt-mm console` | ✓ |
| `cpu-stats` | `virt-mm stats` | ✓ |
| `define` | `virt-mm define, xml --set` | ✓ |
| `desc` | `virt-mm set --title --description` | ✓ |
| `destroy` | `virt-mm force-off` | ✓ |
| `detach-device` | `virt-mm device detach <file>` | ✓ |
| `detach-disk` | `virt-mm disk rm` | ✓ |
| `detach-interface` | `virt-mm nic rm` | ✓ |
| `domdisplay` | `virt-mm vnc (spice)` | ✓ |
| `domhostname` | `virt-mm info (from the agent)` | ✓ |
| `domid` | `virt-mm ls` | ✓ |
| `domif-setlink` | `virt-mm nic link` | ✓ |
| `domname` | `virt-mm ls` | ✓ |
| `domrename` | `virt-mm rename` | ✓ |
| `domuuid` | `virt-mm ls` | ✓ |
| `dumpxml` | `virt-mm xml` | ✓ |
| `edit` | `virt-mm edit` | ✓ |
| `get-user-sshkeys` | `virt-mm agent ssh-key ls` | ✓ |
| `send-key` | `virt-mm send-key` | ✓ |
| `managedsave` | `virt-mm save` | ✓ |
| `managedsave-remove` | `virt-mm save --rm` | ✓ |
| `migrate` | `virt-mm migrate` | ◦ |
| `migrate-setmaxdowntime` | `virt-mm migrate` | ◦ |
| `migrate-getmaxdowntime` | `virt-mm migrate` | ◦ |
| `migrate-compcache` | `virt-mm migrate` | ◦ |
| `migrate-setspeed` | `virt-mm migrate` | ◦ |
| `migrate-getspeed` | `virt-mm migrate` | ◦ |
| `migrate-postcopy` | `virt-mm migrate` | ◦ |
| `reboot` | `virt-mm reboot` | ✓ |
| `reset` | `virt-mm reset` | ✓ |
| `resume` | `virt-mm resume` | ✓ |
| `screenshot` | `virt-mm screenshot` | ✓ |
| `set-user-sshkeys` | `virt-mm agent ssh-key add|rm` | ✓ |
| `set-user-password` | `virt-mm agent password` | ✓ |
| `setmaxmem` | `virt-mm set --max-memory` | ✓ |
| `setmem` | `virt-mm set --memory` | ✓ |
| `setvcpus` | `virt-mm set --cpus (--sockets --cores --threads)` | ✓ |
| `shutdown` | `virt-mm shutdown` | ✓ |
| `start` | `virt-mm start` | ✓ |
| `suspend` | `virt-mm pause` | ✓ |
| `undefine` | `virt-mm rm [--disks]` | ✓ |
| `vcpucount` | `virt-mm info` | ✓ |
| `vncdisplay` | `virt-mm vnc` | ✓ |
| `guestinfo` | `virt-mm info (from the agent)` | ✓ |

✗ `blkdeviotune`, `domthrottlegroupset`, `domthrottlegroupdel`, `domthrottlegroupinfo`, `domthrottlegrouplist`, `blkiotune`, `blockcommit`, `blockcopy`, `blockjob`, `blockpull`, `create`, `detach-device-alias`, `domfsfreeze`, `domfsthaw`, `domfsinfo`, `domfstrim`, `domifannounce`, `domiftune`, `domjobabort`, `domjobinfo`, `domlaunchsecinfo`, `domsetlaunchsecstate`, `dompmsuspend`, `dompmwakeup`, `domxml-from-native`, `domxml-to-native`, `dump`, `inject-nmi`, `iothreadinfo`, `iothreadpin`, `iothreadadd`, `iothreadset`, `iothreaddel`, `send-process-signal`, `lxc-enter-namespace`, `managedsave-edit`, `managedsave-dumpxml`, `managedsave-define`, `memtune`, `perf`, `metadata`, `numatune`, `qemu-attach`, `qemu-monitor-command`, `qemu-monitor-event`, `qemu-agent-command`, `guest-agent-timeout`, `restore`, `save`, `save-image-define`, `save-image-dumpxml`, `save-image-edit`, `schedinfo`, `set-lifecycle-action`, `ttyconsole`, `update-device`, `update-memory-device`, `vcpuinfo`, `vcpupin`, `emulatorpin`, `guestvcpus`, `setvcpu`, `domblkthreshold`, `domdirtyrate-calc`, `dom-fd-associate`, `domdisplay-reload`: tuning (pinning, I/O and bandwidth limits, NUMA, IOThreads), block jobs, saved-state files, guest PM, the QEMU monitor and LXC: rarely used, or for debugging; `virt-mm edit` reaches the XML ones, and the pages may grow forms for some.

## Domain Monitoring: 12 ✓, 0 ·, 0 ◦, 3 ✗ of 15

| virsh | virt-mm | |
|---|---|---|
| `domblkinfo` | `virt-mm disk ls` | ✓ |
| `domblklist` | `virt-mm disk ls` | ✓ |
| `domblkstat` | `virt-mm stats` | ✓ |
| `domif-getlink` | `virt-mm nic ls` | ✓ |
| `domifaddr` | `virt-mm info` | ✓ |
| `domiflist` | `virt-mm nic ls` | ✓ |
| `domifstat` | `virt-mm stats` | ✓ |
| `dominfo` | `virt-mm info` | ✓ |
| `dommemstat` | `virt-mm stats` | ✓ |
| `domstate` | `virt-mm ls` | ✓ |
| `domstats` | `virt-mm stats [--watch]` | ✓ |
| `list` | `virt-mm ls` | ✓ |

✗ `domblkerror`, `domcontrol`, `domtime`: rarely needed.

## Domain Events: 1 ✓, 0 ·, 0 ◦, 1 ✗ of 2

| virsh | virt-mm | |
|---|---|---|
| `event` | `virt-mm events` | ✓ |

✗ `await`: `events` follows them instead.

## Host and Hypervisor: 11 ✓, 0 ·, 0 ◦, 12 ✗ of 23

| virsh | virt-mm | |
|---|---|---|
| `capabilities` | `virt-mm host` | ✓ |
| `cpu-models` | `virt-mm cpu-models` | ✓ |
| `domcapabilities` | `virt-mm host` | ✓ |
| `hostname` | `virt-mm host` | ✓ |
| `hypervisor-cpu-models` | `virt-mm cpu-models` | ✓ |
| `maxvcpus` | `virt-mm host` | ✓ |
| `nodecpustats` | `virt-mm host` | ✓ |
| `nodeinfo` | `virt-mm host` | ✓ |
| `nodememstats` | `virt-mm host` | ✓ |
| `uri` | `virt-mm conn ls` | ✓ |
| `version` | `virt-mm host` | ✓ |

✗ `allocpages`, `cpu-baseline`, `cpu-compare`, `freecell`, `freepages`, `hypervisor-cpu-baseline`, `hypervisor-cpu-compare`, `node-memory-tune`, `nodecpumap`, `nodesevinfo`, `nodesuspend`, `sysinfo`: NUMA pages, CPU baselines for migration, SEV, host suspend: specialist.

## Checkpoint: 0 ✓, 0 ·, 8 ◦, 0 ✗ of 8

| virsh | virt-mm | |
|---|---|---|
| `checkpoint-create` | `virt-mm backup` | ◦ |
| `checkpoint-create-as` | `virt-mm backup` | ◦ |
| `checkpoint-delete` | `virt-mm backup` | ◦ |
| `checkpoint-dumpxml` | `virt-mm backup` | ◦ |
| `checkpoint-edit` | `virt-mm backup` | ◦ |
| `checkpoint-info` | `virt-mm backup` | ◦ |
| `checkpoint-list` | `virt-mm backup` | ◦ |
| `checkpoint-parent` | `virt-mm backup` | ◦ |

## Interface: 0 ✓, 0 ·, 0 ◦, 14 ✗ of 14

✗ `iface-begin`, `iface-bridge`, `iface-commit`, `iface-define`, `iface-destroy`, `iface-dumpxml`, `iface-edit`, `iface-list`, `iface-mac`, `iface-name`, `iface-rollback`, `iface-start`, `iface-unbridge`, `iface-undefine`: netcf, which distributions have dropped; virt-meow reads the host's bridges from sysfs (`nets`).

## Network Filter: 0 ✓, 0 ·, 0 ◦, 9 ✗ of 9

✗ `nwfilter-define`, `nwfilter-dumpxml`, `nwfilter-edit`, `nwfilter-list`, `nwfilter-undefine`, `nwfilter-binding-create`, `nwfilter-binding-delete`, `nwfilter-binding-dumpxml`, `nwfilter-binding-list`: rarely used.

## Networking: 11 ✓, 0 ·, 0 ◦, 10 ✗ of 21

| virsh | virt-mm | |
|---|---|---|
| `net-autostart` | `virt-mm net autostart` | ✓ |
| `net-define` | `virt-mm net add` | ✓ |
| `net-destroy` | `virt-mm net stop` | ✓ |
| `net-dhcp-leases` | `virt-mm net leases` | ✓ |
| `net-dumpxml` | `virt-mm net xml` | ✓ |
| `net-edit` | `virt-mm net edit` | ✓ |
| `net-info` | `virt-mm nets` | ✓ |
| `net-list` | `virt-mm nets` | ✓ |
| `net-start` | `virt-mm net start` | ✓ |
| `net-undefine` | `virt-mm net rm` | ✓ |
| `net-update` | `virt-mm net host add|rm (fixed DHCP addresses)` | ✓ |

✗ `net-create`, `net-desc`, `net-event`, `net-metadata`, `net-name`, `net-uuid`, `net-port-list`, `net-port-create`, `net-port-dumpxml`, `net-port-delete`: transient networks, ports, metadata: rarely used.

## Node Device: 2 ✓, 0 ·, 0 ◦, 12 ✗ of 14

| virsh | virt-mm | |
|---|---|---|
| `nodedev-dumpxml` | `virt-mm hostdevs` | ✓ |
| `nodedev-list` | `virt-mm hostdevs (USB, PCI, IOMMU groups)` | ✓ |

✗ `nodedev-create`, `nodedev-destroy`, `nodedev-detach`, `nodedev-reattach`, `nodedev-reset`, `nodedev-event`, `nodedev-define`, `nodedev-undefine`, `nodedev-start`, `nodedev-autostart`, `nodedev-info`, `nodedev-update`: `hostdevs` lists devices; a managed `<hostdev>` detaches and reattaches by itself.

## Secret: 0 ✓, 0 ·, 0 ◦, 7 ✗ of 7

✗ `secret-define`, `secret-dumpxml`, `secret-event`, `secret-get-value`, `secret-list`, `secret-set-value`, `secret-undefine`: needed only for iSCSI, RBD and LUKS disks, which aren't planned.

## Snapshot: 5 ✓, 0 ·, 0 ◦, 5 ✗ of 10

| virsh | virt-mm | |
|---|---|---|
| `snapshot-create-as` | `virt-mm snap create` | ✓ |
| `snapshot-current` | `virt-mm snap ls` | ✓ |
| `snapshot-delete` | `virt-mm snap rm` | ✓ |
| `snapshot-list` | `virt-mm snap ls` | ✓ |
| `snapshot-revert` | `virt-mm snap revert` | ✓ |

✗ `snapshot-create`, `snapshot-dumpxml`, `snapshot-edit`, `snapshot-info`, `snapshot-parent`: `snap ls` shows the tree and the current one; XML editing of snapshots is rare.

## Backup: 0 ✓, 0 ·, 2 ◦, 0 ✗ of 2

| virsh | virt-mm | |
|---|---|---|
| `backup-begin` | `virt-mm backup` | ◦ |
| `backup-dumpxml` | `virt-mm backup` | ◦ |

## Storage Pool: 10 ✓, 0 ·, 0 ◦, 11 ✗ of 21

| virsh | virt-mm | |
|---|---|---|
| `pool-autostart` | `virt-mm pool autostart` | ✓ |
| `pool-build` | `virt-mm pool add` | ✓ |
| `pool-define-as` | `virt-mm pool add (dir, logical, netfs) [--no-start]` | ✓ |
| `pool-delete` | `virt-mm pool rm` | ✓ |
| `pool-destroy` | `virt-mm pool stop` | ✓ |
| `pool-info` | `virt-mm pools` | ✓ |
| `pool-list` | `virt-mm pools` | ✓ |
| `pool-refresh` | `virt-mm vols (refreshes)` | ✓ |
| `pool-start` | `virt-mm pool start` | ✓ |
| `pool-undefine` | `virt-mm pool rm` | ✓ |

✗ `find-storage-pool-sources-as`, `find-storage-pool-sources`, `pool-create-as`, `pool-create`, `pool-define`, `pool-dumpxml`, `pool-edit`, `pool-name`, `pool-uuid`, `pool-event`, `pool-capabilities`: pool XML editing, source discovery, transient pools: rarely used.

## Storage Volume: 8 ✓, 0 ·, 0 ◦, 8 ✗ of 16

| virsh | virt-mm | |
|---|---|---|
| `vol-create-as` | `virt-mm vol create` | ✓ |
| `vol-delete` | `virt-mm vol rm` | ✓ |
| `vol-download` | `virt-mm vol download` | ✓ |
| `vol-info` | `virt-mm vols` | ✓ |
| `vol-list` | `virt-mm vols` | ✓ |
| `vol-path` | `virt-mm vols` | ✓ |
| `vol-resize` | `virt-mm vol resize` | ✓ |
| `vol-upload` | `virt-mm upload` | ✓ |

✗ `vol-clone`, `vol-create`, `vol-create-from`, `vol-dumpxml`, `vol-key`, `vol-name`, `vol-pool`, `vol-wipe`: keys, wiping, XML: rarely used.

## Beyond virsh

| | virt-mm | |
|---|---|---|
| virt-install | `virt-mm create [--image --disk-bus --nic-model --print-xml|--xml]` | ✓ |
| virt-clone | `virt-mm clone` | ✓ |
| defaults for new machines (virt-manager's "New VM" preferences) | `virt-mm defaults [set]`, used by `virt-mm create` and the form | ✓ |
| files on this computer | `virt-mm upload`, `virt-mm vol download` | ✓ |
| connections, SSH host keys | `virt-mm conn`, `virt-mm hostkey` | ✓ |
| `~/.ssh/config` (aliases, keys, ProxyJump), as virsh's ssh reads it | `qemu+ssh://alias/system`, `virt-mm conn route` | ✓ |
| SSH passwords and key passphrases remembered in the OS keychain | asked, then `virt-mm conn forget` | ✓ |
| every host at once, several machines at once | `virt-mm ls --all`, `virt-mm <action> <machine>…`, the pages' overview | ✓ |
| set the firmware, CPU model, boot order, devices | `virt-mm set --firmware --cpu-model`, `boot`, `device` | ✓ |
| NFS pools | `virt-mm pool add --type netfs` | ✓ |
| cloud-init (a NoCloud seed made in Go) | `virt-mm create --cloud-init`, the new-machine form | ✓ |
| osinfo (the host's osinfo-db, or the copy virt-meow ships) | `virt-mm osinfo [--detect]`, `virt-mm create --os-variant`, the form | ✓ |
| host devices from a list (USB, PCI: IOMMU group, who has it) | `virt-mm device add|rm <machine> hostdev`, the details page | ✓ |
| shared folders (virtiofs, with the shared memory it needs) | `virt-mm fs ls|add|rm`, the details page | ✓ |
| a console's password (set and cleared, now on a running machine where QEMU can) | `virt-mm device set <machine> graphics vnc --password` | ✓ |
| the clipboard shared with the VNC console (qemu-vdagent) | `virt-mm device add|rm <machine> clipboard`, new machines, the console | ✓ |
| virt-admin (the daemon's own settings), virt-host-validate |  | ✗ |

## Totals

| group | ✓ | · | ◦ | ✗ | of |
|---|---|---|---|---|---|
| Domain Management | 43 | 0 | 7 | 66 | 116 |
| Domain Monitoring | 12 | 0 | 0 | 3 | 15 |
| Domain Events | 1 | 0 | 0 | 1 | 2 |
| Host and Hypervisor | 11 | 0 | 0 | 12 | 23 |
| Checkpoint | 0 | 0 | 8 | 0 | 8 |
| Interface | 0 | 0 | 0 | 14 | 14 |
| Network Filter | 0 | 0 | 0 | 9 | 9 |
| Networking | 11 | 0 | 0 | 10 | 21 |
| Node Device | 2 | 0 | 0 | 12 | 14 |
| Secret | 0 | 0 | 0 | 7 | 7 |
| Snapshot | 5 | 0 | 0 | 5 | 10 |
| Backup | 0 | 0 | 2 | 0 | 2 |
| Storage Pool | 10 | 0 | 0 | 11 | 21 |
| Storage Volume | 8 | 0 | 0 | 8 | 16 |
| **all** | **103** | **0** | **17** | **158** | **278** |
