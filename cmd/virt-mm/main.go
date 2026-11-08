// virt-mm is virt-meow's command: it manages libvirt virtual machines, on this
// computer or on others over SSH. Without a command it opens the GUI; every command takes --json.
package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/jing2uo/virt-meow/internal/app"
	"github.com/jing2uo/virt-meow/internal/conn"
	"github.com/jing2uo/virt-meow/internal/hv"
	"github.com/jing2uo/virt-meow/internal/store"
)

// version is set by the builds (-X main.version); go install …@v0.1.0
// leaves it, and the module's version is used instead.
var version = "dev"

func init() {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version
	}
}

const usage = `virt-mm - virt-meow on the command line: libvirt virtual machines, here or over SSH

  virt-mm conn ls                     saved connections
  virt-mm conn add <uri> [--name N] [--autoconnect]
                                     qemu:///system, qemu:///session, qemu+ssh://user@host/system
  virt-mm conn rm <conn>
  virt-mm conn forget <conn>          forget the passwords and key passphrases remembered for it
                                     in the keychain (asked when they are needed, and kept
                                     only when you say so; a key's is shared by its connections)
  virt-mm conn route <conn>           how it is reached over SSH, after ~/.ssh/config and
                                     /etc/ssh/ssh_config (Host aliases, HostName, User, Port,
                                     IdentityFile, IdentitiesOnly, ProxyJump): ssh -G's part
  virt-mm ls [-c <conn>]              machines
  virt-mm ls --all                    every saved connection: each host's CPU and memory in use
                                     and machine counts, then every machine with its host
  virt-mm info <machine> [-c <conn>]  a machine in detail, with its guest's IP addresses
                                     (from the guest agent, else DHCP leases, else ARP)
  virt-mm stats [<machine>] [-c <conn>] [--watch]
                                     CPU, memory, disk and network rates of the running
                                     machines; --watch redraws every 2 s (with --json: a
                                     JSON line per sample)
  virt-mm console <machine> [-c <conn>] [--dev serial0] [--force]
                                     its serial console in this terminal; Ctrl+] leaves.
                                     --force takes it over from another client
  virt-mm vnc|spice <machine> [-c <conn>] [--port N]
                                     forward its VNC or SPICE console to 127.0.0.1:N here
                                     (any free port without --port), over SSH for a remote
                                     host; point any viewer at it; Ctrl+C stops
  virt-mm send-key <machine> <keys>… [-c <conn>] [--holdtime ms]
                                     press each combination in turn: ctrl+alt+delete,
                                     ctrl+alt+f2, a, enter (names or Linux key codes)
  virt-mm screenshot <machine> [<file.png>|-] [-c <conn>] [--screen N] [--width W]
                                     what its display shows, as PNG (default file:
                                     <machine>-<date>-<time>.png here; never overwritten)
  virt-mm events [<machine>] [-c <conn>]
                                     machines' lifecycle events as they happen, a line each
                                     (with --json, a JSON line each) until Ctrl+C; a lost
                                     connection is made again, missing what happened meanwhile
  virt-mm host [-c <conn>]            the host: versions, KVM, UEFI, TPM; CPU and memory
                                     in use (over a second), machines running
  virt-mm pools [-c <conn>]           storage pools
  virt-mm vols <pool> [-c <conn>]     a pool's volumes (disks, ISOs)
  virt-mm nets [-c <conn>]            virtual networks and host bridges
  virt-mm hostdevs [<device>] [-c <conn>]
                                     the host's PCI and USB devices: IDs, driver, IOMMU
                                     group, the machines that have each; with a device's
                                     name, its description (virsh nodedev-dumpxml). A
                                     machine gets one with virt-mm device add <m> hostdev
  virt-mm osinfo [<search>] [-c <conn>] [--detect <pool>/<volume>]
                                     the operating systems osinfo-db knows (the host's own, or
                                     the copy virt-meow ships), with what a new machine of each
                                     gets on this host; --detect: what an ISO (by its volume
                                     ID) or a disk image (by its name) is
  virt-mm create <name> [-c <conn>] --disk <pool>:<size>G | --import <pool>/<volume>
                [--image <pool>/<volume>] [--iso <pool>/<volume>] [--os linux|windows|other]
                [--os-variant <short-id>|auto]
                [--cpus 2] [--memory 4G] [--disk-bus virtio|sata|scsi]
                [--net bridge:<br>|network:<name>|none] [--nic-model virtio|e1000e|e1000|rtl8139]
                [--firmware bios|uefi|uefi-secure] [--tpm] [--start] [--print-xml | --xml <file|->]
                [--cloud-init user=<name>,password,ssh-key=<file>,hostname=<h>,user-data=<file>]
                [--listen A] [--cpu-mode host-passthrough|host-model] [--disk-format qcow2|raw]
                [--sound on|off]
                                     defaults (virt-mm defaults set changes them): linux, 2 CPUs,
                                     4G, UEFI where the host has it, the host's first bridge,
                                     else its first network; virtio disk and NIC (SATA and
                                     e1000e for Windows); a qcow2 disk, VNC on 127.0.0.1, no
                                     sound; --disk <size>G goes into the default pool;
                                     --image: the new disk is a copy of that cloud or disk
                                     image (in its pool unless --disk says), grown to --disk's
                                     size; more memory than the host has free is a warning;
                                     --os-variant: CPUs, memory, disk size (--disk <pool> alone),
                                     firmware, TPM, disk bus and NIC model from osinfo-db,
                                     where not given; auto: from the ISO or image;
                                     --cloud-init (any of its parts, or "on"): a NoCloud seed ISO
                                     beside the disk, as a CD; password is asked (or
                                     --password-stdin), stored as a hash; user-data replaces
                                     what the other parts make;
                                     --print-xml shows the definition and makes nothing,
                                     --xml creates with that definition (edited --print-xml)
  virt-mm defaults                    what new machines get, here and in the pages' form
  virt-mm defaults set [--listen A] [--cpu-mode host-passthrough|host-model]
               [--disk-format qcow2|raw] [--disk-bus B] [--nic-model N] [--sound on|off]
               [--pool P] [--net network:N|bridge:B|none]
                                     "" gives one back to virt-meow; create's flags and osinfo's
                                     proposal for the system (disk bus, NIC model) win over them
  virt-mm xml <machine> [-c <conn>] [--set <file|->]
                                     a machine's definition (what it boots with next);
                                     --set saves one, validated by libvirt
  virt-mm edit <machine> [-c <conn>]  edit the definition in $VISUAL / $EDITOR, as virsh edit
  virt-mm define <file|-> [-c <conn>] a new machine from a definition (never over an existing one)
  virt-mm set <machine> [-c <conn>] [--cpus N] [--sockets N --cores N --threads N]
                [--cpu-model host-passthrough|host-model|<name>]
                [--firmware bios|uefi|uefi-secure]  (when it is off)
                [--memory 4G] [--max-memory 8G] [--autostart on|off]
                [--title T] [--description D]
                                     what a running machine can take changes now, the rest
                                     at its next start; the output says which
  virt-mm cpu-models <machine>        the CPU models --cpu-model takes for that machine
  virt-mm rename <machine> <new-name> [-c <conn>]
  virt-mm clone <machine> <new-name> [-c <conn>]
                                     copy a machine that is off: its disks into new volumes
                                     next to the originals, its UEFI variables; new UUID,
                                     MACs and TPM (CD/DVD media and shared disks are shared)
  virt-mm disk ls|add|rm|resize <machine> …   (virt-mm disk, without arguments, for details)
                                     add: --new <pool>:<size>G | --vol <pool>/<volume> |
                                          --path <file> --format raw|qcow2  [--bus virtio|sata|scsi|usb]
                                     rm <target> [--delete-volume]; resize <target> <size>|+<size>
  virt-mm cdrom insert <machine> <pool>/<volume> [--target sdb]
  virt-mm cdrom eject <machine> [--target sdb]
  virt-mm nic ls|add|rm|link <machine> …
                                     add: --net bridge:<br>|network:<name>|user [--model M] [--mac M]
                                     rm <mac>; link <mac> up|down
  virt-mm device ls <machine>         video, graphics, TPM, sound, RNG, clipboard, host devices
  virt-mm device set <machine> video virtio|vga|qxl|bochs|ramfb|none
  virt-mm device set <machine> sound ich9|ich6|ich7|ac97|usb|virtio|none
  virt-mm device add|set <machine> graphics vnc|spice [--listen 127.0.0.1]
                                     another address than the host's loopback opens the
                                     console to the network, unencrypted and without a password
  virt-mm device set <machine> graphics vnc|spice --password <pw>|-|""
                                     the password its console asks for ("": none; -: asked
                                     here, or one line of stdin with --password-stdin); a VNC
                                     one is at most 8 characters. A running machine takes a new
                                     one now if it started with one; clearing waits for the next
                                     start. It doesn't encrypt the console: keep it on 127.0.0.1
  virt-mm device rm <machine> graphics vnc|spice
  virt-mm device add <machine> tpm [--model crb|tis] | rng | clipboard
  virt-mm device rm <machine> tpm | rng | clipboard
                                     clipboard: shared with the VNC console (qemu-vdagent; the
                                     guest needs spice-vdagent). An RNG and the clipboard come
                                     and go on a running machine; the rest at its next start
  virt-mm device add <machine> hostdev <device>
  virt-mm device rm <machine> hostdev <address|vendor:product|device>
                                     a USB or PCI device of the host (virt-mm hostdevs: its name
                                     or address), managed: taken from the host while the
                                     machine runs; the output says who else has it and what
                                     shares its IOMMU group
  virt-mm fs ls <machine>             the host's folders it is given (virtiofs)
  virt-mm fs add <machine> <host-dir> <tag>
  virt-mm fs rm <machine> <tag>       a folder of the host as virtiofs, from the next start;
                                     sets up the shared memory virtiofsd needs, saying so; in
                                     the guest: mount -t virtiofs <tag> /mnt
  virt-mm device attach|detach <machine> <file|->
                                     any device from its XML (virsh attach-device): a
                                     <hostdev>, a <filesystem>, …; into the definition, and
                                     into the running machine where it can take it
  virt-mm boot <machine> [<target>|<mac> …] [--menu on|off]
                                     show the boot order, or set it (from the next start)
  virt-mm agent password <machine> <user> [--password-stdin]
                                     set a user's password in the running guest, through its
                                     agent (qemu-guest-agent); asked twice, or one line of stdin
  virt-mm agent ssh-key ls <machine> <user>
  virt-mm agent ssh-key add|rm <machine> <user> <file|->
                                     the SSH keys the user accepts (~/.ssh/authorized_keys), as
                                     in a .pub or authorized_keys file
  virt-mm snap ls <machine>           its snapshots, as a tree
  virt-mm snap create <machine> [<name>] [--description D]
                                     of its qcow2 disks, and its memory when it runs
  virt-mm snap revert <machine> <name>
  virt-mm snap rm <machine> <name> [--children]
  virt-mm rm <machine> [-c <conn>] [--disks] --yes
                                     undefine (forcing it off); --disks deletes its disk volumes
                                     and the cloud-init seed virt-meow made for it
  virt-mm pool add <name> --type dir --path <dir> | --type logical [--source <vg>]
                | --type netfs --source <host>:/<export> [--path <mount point>]
                [--autostart on|off] [--no-start]
                                     a directory (made if missing), an existing LVM volume
                                     group (its name, or the pool's), or an NFS export the
                                     host mounts (on /var/lib/libvirt/images/<name> unless
                                     --path); --no-start only defines it
  virt-mm pool start|stop <name>;  virt-mm pool autostart <name> on|off
  virt-mm pool rm <name>              stop and forget it; its files or volume group stay
  virt-mm vol create <pool>/<name> <size> [--format qcow2|raw]
  virt-mm vol resize <pool>/<name> <size>|+<size>   (grow only)
  virt-mm vol rm <pool>/<name>        (not one a machine has as a disk)
  virt-mm vol download <pool>/<name> <file>
  virt-mm net add <name> --type nat|route|isolated [--address 192.168.100.1/24]
                [--dhcp on|off|<first>-<last>] [--bridge virbrN] [--dev <host interface>]
                [--autostart on|off]   a network with its own bridge, address and DHCP (the
                                     first free 192.168.N.1/24 and the rest of the subnet
                                     by default); nat and route through the host's routes,
                                     or --dev only
  virt-mm net add <name> --type bridge --bridge <host bridge> [--autostart on|off]
                                     guests join a bridge the host has, on its LAN
  virt-mm net start|stop <name> [--force];  virt-mm net autostart <name> on|off
  virt-mm net rm <name> [--force]     stop and forget it; --force when running machines
                                     are on it (they lose the network until restarted)
  virt-mm net leases <name>           its DHCP leases, with the machine of each
  virt-mm net host ls <net>           its fixed DHCP addresses (virsh net-update ip-dhcp-host)
  virt-mm net host add <net> <mac> <ip> [<hostname>]
  virt-mm net host rm <net> <mac>|<ip>
                                     in the definition, and in the running network now
  virt-mm net xml <net> [--set <file|->]
                                     its definition; --set saves one, validated by libvirt
  virt-mm net edit <net>              edit the definition in $VISUAL / $EDITOR (virsh net-edit);
                                     a running network takes it when it restarts
  virt-mm upload <file> <pool> [-c <conn>] [--name <volume>]
                                     copy an ISO or disk image into a storage pool
  virt-mm <action> <machine>… [-c <conn>]
                                     action: start shutdown reboot force-off pause resume;
                                     several machines are done at once, each one's outcome said
                                     reset (virsh's destroy, stop and suspend work too);
                                     reset is the reset button: the guest isn't asked
  virt-mm save <machine> [-c <conn>] [--rm]
                                     stop it with its memory saved on the host (virsh
                                     managedsave): the next start resumes it; --rm drops
                                     the saved state, so the next start boots afresh
  virt-mm hostkey trust <conn>        show an unknown host key and record it if you say so
  virt-mm hostkey forget <host[:port]>  drop virt-meow's record of a host's key
  virt-mm gui [create | console <machine> [spice]]
                                     create: the new-machine form on the selected connection;
                                     console: that machine's console there (VNC unless spice)
  virt-mm serve [--listen 127.0.0.1:8080] [--password-file F | --no-login]
               [--tls-cert C --tls-key K]
                                     the GUI's pages in a browser, for every saved connection,
                                     behind a login when it has a password (--password-file,
                                     or the one --set-password keeps hashed in the config);
                                     beyond loopback it needs one (asked for on first start),
                                     unless --no-login says a proxy that authenticates is in
                                     front; --tls-cert/--tls-key serve HTTPS
  virt-mm serve --set-password [--password-stdin]
                                     set the login's password (at least 8 characters)
  virt-mm version

<conn> is a saved connection's name or id, or a libvirt URI. Without -c:
$VIRT_MM_URI, else the only saved connection, else qemu:///system.
Global: --json. Config: $VIRT_MM_CONFIG (default: virt-mm/ in the user config directory).
`

func main() {
	os.Exit(run(os.Args[1:]))
}

type opts struct {
	json        bool
	conn        string
	name        string
	autoconnect bool
	flags       map[string]string // the create/rm/upload flags, by name without dashes
	args        []string
}

// valueFlags take a value; boolFlags don't.
var (
	valueFlags = []string{"--os", "--cpus", "--memory", "--disk", "--import", "--iso", "--net", "--firmware", "--xml", "--set",
		"--sockets", "--cores", "--threads", "--max-memory", "--autostart", "--title", "--description",
		"--cpu-model", "--new", "--vol", "--path", "--format", "--bus", "--target", "--model", "--mac", "--menu", "--listen", "--dev", "--port", "--holdtime", "--type", "--source",
		"--address", "--dhcp", "--bridge", "--screen", "--width", "--image", "--disk-bus", "--nic-model", "--cloud-init", "--os-variant", "--detect",
		"--password-file", "--tls-cert", "--tls-key", "--password", "--cpu-mode", "--disk-format", "--sound", "--pool"}
	boolFlags = []string{"--children", "--force", "--watch", "--tpm", "--start", "--disks", "--yes", "--print-xml", "--delete-volume", "--rm", "--password-stdin", "--no-start", "--all", "--set-password", "--no-login"}
)

func parse(args []string) (opts, error) {
	o := opts{flags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, error) {
			if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "-") {
				return v, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch flag, _, _ := strings.Cut(a, "="); flag {
		case "--json":
			o.json = true
		case "-c", "--connect":
			o.conn, err = value()
		case "--name":
			o.name, err = value()
		case "--autoconnect":
			o.autoconnect = true
		case "-h", "--help":
			o.args = append(o.args, "help")
		default:
			if slices.Contains(valueFlags, flag) {
				o.flags[flag[2:]], err = value()
				break
			}
			if slices.Contains(boolFlags, flag) {
				o.flags[flag[2:]] = "true"
				break
			}
			if strings.HasPrefix(a, "-") && a != "-" {
				return o, fmt.Errorf("unknown flag %s", a)
			}
			o.args = append(o.args, a)
		}
		if err != nil {
			return o, err
		}
	}
	if o.conn == "" {
		o.conn = os.Getenv("VIRT_MM_URI")
	}
	return o, nil
}

func run(args []string) int {
	o, err := parse(args)
	if err != nil {
		return fail(o, err)
	}
	if len(o.args) == 0 {
		o.args = []string{"gui"}
	}
	cmd, rest := o.args[0], o.args[1:]
	switch cmd {
	case "help":
		fmt.Print(usage)
		return 0
	case "version":
		return out(o, map[string]string{"version": version}, func(w io.Writer) { fmt.Fprintln(w, "virt-mm", version) })
	case "serve":
		if err := cmdServe(o, rest); err != nil {
			return fail(o, err)
		}
		return 0
	case "gui":
		if err := runGUI(strings.Join(rest, "/")); err != nil {
			return fail(o, err)
		}
		return 0
	case "defaults":
		if err := cmdDefaults(o, rest); err != nil {
			return fail(o, err)
		}
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	m, err := app.NewManager(app.Callbacks{}, termPrompter{})
	if err != nil {
		return fail(o, err)
	}
	defer m.Close()

	switch cmd {
	case "conn":
		err = cmdConn(o, m, rest)
	case "ls", "list":
		err = cmdList(ctx, o, m)
	case "stats", "domstats":
		err = cmdStats(ctx, o, m, rest)
	case "console":
		err = cmdConsole(ctx, o, m, rest)
	case "vnc", "vncdisplay":
		err = cmdGraphics(ctx, o, m, "vnc", rest)
	case "spice":
		err = cmdGraphics(ctx, o, m, "spice", rest)
	case "send-key":
		err = cmdSendKey(ctx, o, m, rest)
	case "events", "event":
		err = cmdEvents(ctx, o, m, rest)
	case "agent":
		err = cmdAgent(ctx, o, m, rest)
	case "set-user-password":
		err = cmdAgent(ctx, o, m, append([]string{"password"}, rest...))
	case "screenshot":
		err = cmdScreenshot(ctx, o, m, rest)
	case "save", "managedsave":
		err = cmdSave(ctx, o, m, rest)
	case "managedsave-remove":
		o.flags["rm"] = "true"
		err = cmdSave(ctx, o, m, rest)
	case "info", "dominfo":
		err = cmdInfo(ctx, o, m, rest)
	case "hostkey":
		err = cmdHostKey(ctx, o, m, rest)
	case "fs", "filesystem":
		err = cmdFS(ctx, o, m, rest)
	case "osinfo", "os-variants":
		err = cmdOSInfo(ctx, o, m, rest)
	case "hostdevs", "nodedev-list", "nodedev-dumpxml":
		err = cmdHostDevs(ctx, o, m, rest)
	case "host", "pools", "vols", "nets":
		err = cmdQuery(ctx, o, m, cmd, rest)
	case "create":
		err = cmdCreate(ctx, o, m, rest)
	case "rm", "delete":
		err = cmdDelete(ctx, o, m, rest)
	case "upload":
		err = cmdUpload(ctx, o, m, rest)
	case "pool":
		err = cmdPool(ctx, o, m, rest)
	case "vol", "volume":
		err = cmdVol(ctx, o, m, rest)
	case "net", "network":
		err = cmdNet(ctx, o, m, rest)
	case "xml", "dumpxml":
		err = cmdXML(ctx, o, m, rest)
	case "edit":
		err = cmdEdit(ctx, o, m, rest)
	case "define":
		err = cmdDefine(ctx, o, m, rest)
	case "set":
		err = cmdSet(ctx, o, m, rest)
	case "cpu-models":
		err = cmdCPUModels(ctx, o, m, rest)
	case "rename":
		err = cmdRename(ctx, o, m, rest)
	case "clone":
		err = cmdClone(ctx, o, m, rest)
	case "disk":
		err = cmdDisk(ctx, o, m, rest)
	case "cdrom":
		err = cmdCDROM(ctx, o, m, rest)
	case "nic":
		err = cmdNIC(ctx, o, m, rest)
	case "boot":
		err = cmdBoot(ctx, o, m, rest)
	case "snap", "snapshot":
		err = cmdSnap(ctx, o, m, rest)
	case "device", "dev":
		err = cmdDevice(ctx, o, m, rest)
	case "attach-device", "detach-device":
		err = cmdDevice(ctx, o, m, append([]string{strings.TrimSuffix(cmd, "-device")}, rest...))
	default:
		a, perr := hv.ParseAction(cmd)
		if perr != nil {
			fmt.Fprint(os.Stderr, usage)
			return fail(o, fmt.Errorf("unknown command %q", cmd))
		}
		err = cmdPower(ctx, o, m, a, rest)
	}
	if err != nil {
		return fail(o, err)
	}
	return 0
}

func cmdConn(o opts, m *app.Manager, args []string) error {
	sub := "ls"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "ls", "list":
		cs := m.Connections()
		out(o, cs, func(w io.Writer) {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tURI\tAUTOCONNECT")
			for _, c := range cs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%v\n", c.ID, c.Name, c.URI, c.Autoconnect)
			}
			tw.Flush()
		})
		return nil
	case "add":
		if len(args) != 1 {
			return errors.New("usage: virt-mm conn add <uri> [--name N] [--autoconnect]")
		}
		c, err := m.Add(o.name, args[0], o.autoconnect)
		if err != nil {
			return err
		}
		out(o, c, func(w io.Writer) { fmt.Fprintf(w, "saved %s as %q (%s)\n", c.URI, c.Name, c.ID) })
		return nil
	case "rm", "remove":
		if len(args) != 1 {
			return errors.New("usage: virt-mm conn rm <conn>")
		}
		c, ok := m.Find(args[0])
		if !ok {
			return fmt.Errorf("no saved connection %q", args[0])
		}
		if err := m.Remove(c.ID); err != nil {
			return err
		}
		out(o, map[string]string{"removed": c.ID}, func(w io.Writer) { fmt.Fprintf(w, "removed %q\n", c.Name) })
		return nil
	case "forget":
		if len(args) != 1 {
			return errors.New("usage: virt-mm conn forget <conn>")
		}
		ctx, cancel := callCtx(context.Background())
		defer cancel()
		forgot, err := m.ForgetSecrets(ctx, args[0])
		if err != nil {
			return err
		}
		out(o, forgot, func(w io.Writer) {
			if len(forgot) == 0 {
				fmt.Fprintln(w, "nothing was remembered for it")
			}
			for _, s := range forgot {
				fmt.Fprintln(w, "forgot", s)
			}
		})
		return nil
	case "route":
		if len(args) != 1 {
			return errors.New("usage: virt-mm conn route <conn>")
		}
		r, err := m.SSHRoute(args[0])
		if err != nil {
			return err
		}
		out(o, r, func(w io.Writer) {
			if len(r) == 0 {
				fmt.Fprintln(w, "not over SSH")
				return
			}
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "HOP\tNAME\tLOGIN\tKEYS")
			for i, h := range r {
				hop := "jump"
				if i == len(r)-1 {
					hop = "host"
				}
				keys := strings.Join(h.IdentityFiles, " ")
				if h.IdentitiesOnly {
					keys += " (only these)"
				}
				if h.Agent == "none" {
					keys += " (no agent)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", hop, h.Name, h, keys)
			}
			tw.Flush()
		})
		return nil
	}
	return fmt.Errorf("unknown conn command %q", sub)
}

// session picks the connection -c names, or the default one.
func session(o opts, m *app.Manager) (*conn.Session, error) {
	ref := o.conn
	if ref == "" {
		if cs := m.Connections(); len(cs) == 1 {
			ref = cs[0].ID
		} else {
			ref = "qemu:///system"
		}
	}
	if c, ok := m.Find(ref); ok {
		return m.Session(c.ID)
	}
	if !strings.Contains(ref, ":/") {
		return nil, fmt.Errorf("no saved connection %q (and it isn't a libvirt URI)", ref)
	}
	return m.SessionFor(ref)
}

// connID is the Manager's id of the connection -c picks, for the Manager
// methods that take one.
func connID(o opts, m *app.Manager) (string, error) {
	s, err := session(o, m)
	if err != nil {
		return "", err
	}
	if c, ok := m.Find(s.Target().URI); ok {
		return c.ID, nil
	}
	return "uri:" + s.Target().URI, nil
}

func callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 60*time.Second)
}

func cmdList(ctx context.Context, o opts, m *app.Manager) error {
	if o.flags["all"] != "" {
		return cmdListAll(ctx, o, m)
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	doms, err := hv.List(ctx, s)
	if err != nil {
		return hint(err)
	}
	if doms == nil {
		doms = []hv.Domain{}
	}
	out(o, doms, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSTATE\tVCPUS\tMEMORY\tAUTOSTART\tUUID")
		for _, d := range doms {
			name := d.Name
			if !d.Persistent {
				name += " (transient)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%v\t%s\n", name, stateText(d), d.VCPUs, kib(d.MaxMemKiB), d.Autostart, d.UUID)
		}
		tw.Flush()
	})
	return nil
}

// cmdListAll is virt-mm ls --all: every saved connection, opened, its usage
// and machines.
func cmdListAll(ctx context.Context, o opts, m *app.Manager) error {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	all := m.AllHosts(ctx, true)
	names := map[string]string{}
	for _, h := range all.Hosts {
		names[h.ID] = h.Name
	}
	out(o, all, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "HOST\tSTATE\tCPU\tMEMORY\tRUNNING\tMACHINES")
		for _, h := range all.Hosts {
			cpu, mem, run, tot := "-", "-", "-", "-"
			if u := h.Usage; u != nil {
				if u.CPUPercent != nil {
					cpu = fmt.Sprintf("%.0f%%", *u.CPUPercent)
				}
				if u.UsedKiB > 0 {
					mem = kib(u.UsedKiB) + " / " + kib(u.MemoryKiB)
				}
				run, tot = strconv.Itoa(u.Running), strconv.Itoa(u.Machines)
			}
			state := h.Status
			if e := cmp.Or(h.ListError, h.Error); e != "" {
				state += ": " + e
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", h.Name, state, cpu, mem, run, tot)
		}
		tw.Flush()
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "HOST\tNAME\tSTATE\tVCPUS\tMEMORY\tUUID")
		for _, d := range all.Machines {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", names[d.Connection], d.Name, stateText(d.Domain), d.VCPUs, kib(d.MaxMemKiB), d.UUID)
		}
		tw.Flush()
	})
	return nil
}

func cmdQuery(ctx context.Context, o opts, m *app.Manager, what string, args []string) error {
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	switch what {
	case "host":
		h, err := hv.HostInfo(ctx, s)
		if err != nil {
			return hint(err)
		}
		// CPU use is a rate: two samples a second apart.
		prev, err := hv.GetHostSample(ctx, s)
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		cur, err := hv.GetHostSample(ctx, s)
		if err != nil {
			return err
		}
		u := hv.HostRates(prev, cur)
		res := struct {
			hv.Host
			Usage hv.HostUsage `json:"usage"`
		}{h, u}
		out(o, res, func(w io.Writer) {
			fmt.Fprintf(w, "%s  %s, %d CPUs, %s (%s free)\n", h.Hostname, h.Arch, h.CPUs, kib(h.MemoryKiB), kib(h.FreeKiB))
			fmt.Fprintf(w, "libvirt %s, hypervisor %s, virt type %s\n", h.Libvirt, h.Hypervisor, h.VirtType)
			fmt.Fprintf(w, "machines %v, max vCPUs %d, UEFI %v, Secure Boot %v, TPM %v\n", h.Machines, h.MaxVCPUs, h.UEFI, h.SecureBoot, h.TPM)
			cpu, mem := "unknown", "unknown"
			if u.CPUPercent != nil {
				cpu = fmt.Sprintf("%.1f%%", *u.CPUPercent)
			}
			if u.UsedKiB > 0 {
				mem = fmt.Sprintf("%s of %s", kib(u.UsedKiB), kib(u.MemoryKiB))
			}
			fmt.Fprintf(w, "in use: CPU %s, memory %s; %d of %d machines running\n", cpu, mem, u.Running, u.Machines)
		})
	case "pools":
		ps, err := hv.Pools(ctx, s)
		if err != nil {
			return hint(err)
		}
		out(o, ps, func(w io.Writer) {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTYPE\tACTIVE\tAUTOSTART\tFREE\tSIZE\tPATH\tSOURCE")
			for _, p := range ps {
				fmt.Fprintf(tw, "%s\t%s\t%v\t%v\t%s\t%s\t%s\t%s\n", p.Name, p.Type, p.Active, p.Autostart, kib(p.Available>>10), kib(p.Capacity>>10), p.Path, or(p.Source, "-"))
			}
			tw.Flush()
		})
	case "vols":
		if len(args) != 1 {
			return errors.New("usage: virt-mm vols <pool> [-c <conn>]")
		}
		vs, err := hv.Volumes(ctx, s, args[0])
		if err != nil {
			return hint(err)
		}
		out(o, vs, func(w io.Writer) {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tFORMAT\tSIZE\tUSED\tPATH")
			for _, v := range vs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.Name, v.Format, kib(v.Capacity>>10), kib(v.Allocation>>10), v.Path)
			}
			tw.Flush()
		})
	case "nets":
		ns, err := hv.Networks(ctx, s)
		if err != nil {
			return hint(err)
		}
		bs, err := hv.Bridges(ctx, s)
		if err != nil {
			return hint(err)
		}
		out(o, map[string]any{"networks": ns, "bridges": bs}, func(w io.Writer) {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tFORWARD\tBRIDGE\tADDRESS\tDHCP\tACTIVE\tAUTOSTART")
			for _, n := range ns {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%v\t%v\n", n.Name, or(n.Forward, "isolated"), n.Bridge, or(n.Address, "-"), or(n.DHCP, "-"), n.Active, n.Autostart)
			}
			tw.Flush()
			fmt.Fprintf(w, "host bridges: %v\n", bs)
		})
	}
	return nil
}

func cmdCreate(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: virt-mm create <name> --disk <pool>:<size>G | --import <pool>/<volume> [...] (see virt-mm help)")
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	base := ctx
	ctx, cancel := callCtx(base)
	defer cancel()
	h, err := hv.HostInfo(ctx, s)
	if err != nil {
		return hint(err)
	}
	f := o.flags
	st, err := store.LoadSettings()
	if err != nil {
		return err
	}
	d := st.NewMachine // the user's defaults, under the flags
	spec := hv.Spec{Name: args[0], OS: cmp.Or(f["os"], "linux"), VCPUs: 2, MemoryMiB: 4096, NetKind: "none",
		Firmware: "bios", TPM: f["tpm"] != "", Start: f["start"] != "",
		Listen: cmp.Or(f["listen"], d.Listen), CPUMode: cmp.Or(f["cpu-mode"], d.CPUMode),
		DiskFormat: cmp.Or(f["disk-format"], d.DiskFormat), Sound: d.Sound}
	if v := f["sound"]; v != "" {
		if spec.Sound, err = parseOnOff(v); err != nil {
			return fmt.Errorf("--sound: %w", err)
		}
	}
	if h.UEFI {
		spec.Firmware = "uefi"
	}
	spec.Firmware = cmp.Or(f["firmware"], spec.Firmware)
	if v := f["cpus"]; v != "" {
		if spec.VCPUs, err = strconv.Atoi(v); err != nil {
			return fmt.Errorf("--cpus %q", v)
		}
	}
	if v := f["memory"]; v != "" {
		mib, err := parseSize(v, "M")
		if err != nil {
			return fmt.Errorf("--memory: %w", err)
		}
		spec.MemoryMiB = int(mib)
	}
	if v := f["disk"]; v != "" {
		pool, size, ok := strings.Cut(v, ":")
		if !ok {
			// A size alone goes into the default pool.
			if _, err := parseSize(v, "G"); err == nil && d.Pool != "" {
				pool, size, ok = d.Pool, v, true
			}
		}
		if !ok && f["os-variant"] != "" {
			spec.DiskPool = pool // its size from the OS
		} else if mib, err := parseSize(size, "G"); !ok || err != nil {
			return fmt.Errorf("--disk %q: want <pool>:<size>G (or <size>G with a default pool: virt-mm defaults set --pool)", v)
		} else {
			spec.DiskPool, spec.DiskGiB = pool, int(mib>>10)
		}
	}
	if v := f["import"]; v != "" {
		if spec.ImportPool, spec.ImportVolume, _ = strings.Cut(v, "/"); spec.ImportVolume == "" {
			return fmt.Errorf("--import %q: want <pool>/<volume>", v)
		}
	}
	if v := f["iso"]; v != "" {
		if spec.ISOPool, spec.ISOVolume, _ = strings.Cut(v, "/"); spec.ISOVolume == "" {
			return fmt.Errorf("--iso %q: want <pool>/<volume>", v)
		}
	}
	if v := f["image"]; v != "" {
		if spec.ImagePool, spec.ImageVolume, _ = strings.Cut(v, "/"); spec.ImageVolume == "" {
			return fmt.Errorf("--image %q: want <pool>/<volume>", v)
		}
		if spec.DiskPool == "" {
			spec.DiskPool = spec.ImagePool // and the image's size
		}
	}
	spec.DiskBus, spec.NICModel = cmp.Or(f["disk-bus"], d.DiskBus), cmp.Or(f["nic-model"], d.NICModel)
	var osNote string
	if v := f["os-variant"]; v != "" {
		if osNote, err = applyOSVariant(ctx, s, h, f, &spec, v); err != nil {
			return err
		}
	}
	if v := f["cloud-init"]; v != "" {
		if spec.CloudInit, err = cloudInitFlag(o, v); err != nil {
			return err
		}
	}
	bs, bErr := hv.Bridges(ctx, s)
	ns, nErr := hv.Networks(ctx, s)
	if v := f["net"]; v != "" {
		kind, src, _ := strings.Cut(v, ":")
		spec.NetKind, spec.NetSource = kind, src
	} else if kind, src, _ := strings.Cut(d.Network, ":"); d.Network == "none" ||
		kind == "bridge" && bErr == nil && slices.Contains(bs, src) ||
		kind == "network" && nErr == nil && slices.ContainsFunc(ns, func(n hv.Network) bool { return n.Name == src }) {
		spec.NetKind, spec.NetSource = kind, src // the default, which the host has
	} else if bErr == nil && len(bs) > 0 {
		spec.NetKind, spec.NetSource = "bridge", bs[0]
	} else if nErr == nil && len(ns) > 0 {
		spec.NetKind, spec.NetSource = "network", ns[0].Name
	}
	if f["print-xml"] != "" {
		x, err := hv.Preview(ctx, s, spec)
		if err != nil {
			return err
		}
		out(o, map[string]string{"xml": x}, func(w io.Writer) { fmt.Fprint(w, x) })
		return nil
	}
	var x string
	if v := f["xml"]; v != "" {
		b, err := readFile(v)
		if err != nil {
			return fmt.Errorf("--xml: %w", err)
		}
		x = string(b)
	}
	if spec.ImageVolume != "" {
		// Copying an image takes as long as it is big, more over SSH.
		var long context.CancelFunc
		ctx, long = context.WithTimeout(base, time.Hour)
		defer long()
	}
	c, err := hv.Create(ctx, s, spec, x)
	if err != nil {
		return err
	}
	out(o, c, func(w io.Writer) {
		if osNote != "" {
			fmt.Fprintln(w, osNote)
		}
		fmt.Fprintf(w, "created %s (%s)", c.Domain.Name, c.Domain.UUID)
		if c.Disk != "" {
			fmt.Fprintf(w, ", disk %s", c.Disk)
		}
		if c.Seed != "" {
			fmt.Fprintf(w, ", cloud-init seed %s", c.Seed)
		}
		fmt.Fprintln(w)
		for _, n := range c.Warnings {
			fmt.Fprintf(w, "note: %s\n", n)
		}
		if c.StartErr != "" {
			fmt.Fprintf(w, "it did not start: %s\n", c.StartErr)
		} else if spec.Start {
			fmt.Fprintln(w, "started")
		}
	})
	return nil
}

// applyOSVariant fills in spec from osinfo-db's OS v (a short ID, an ID,
// or auto: what the ISO or image is) where the command line didn't say;
// it returns a line saying which OS it is.
func applyOSVariant(ctx context.Context, s *conn.Session, h hv.Host, f map[string]string, spec *hv.Spec, v string) (string, error) {
	db, err := hv.LoadOSDB(ctx, s)
	if err != nil {
		return "", err
	}
	var ov hv.OSVariant
	how := "osinfo-db " + db.Source + " " + db.Version
	if v == "auto" {
		pool, vol := spec.ISOPool, spec.ISOVolume
		if vol == "" {
			pool, vol = spec.ImagePool, spec.ImageVolume
		}
		if vol == "" {
			return "", errors.New("--os-variant auto: from --iso or --image, and neither is given")
		}
		d, err := hv.DetectOS(ctx, s, db, h, pool, vol)
		if err != nil {
			return "", err
		}
		if d.Variant == nil {
			what := "its name"
			if d.VolumeID != "" {
				what = fmt.Sprintf("its volume ID %q", d.VolumeID)
			}
			return "", fmt.Errorf("--os-variant auto: osinfo-db (%s %s) doesn't know %s/%s by %s; give the OS (virt-mm osinfo lists them)", db.Source, db.Version, pool, vol, what)
		}
		ov = *d.Variant
		how = fmt.Sprintf("recognised by %s, %s", map[bool]string{true: "its volume ID", false: "its name"}[d.VolumeID != ""], how)
	} else {
		found := db.Lookup(v)
		if found == nil {
			return "", fmt.Errorf("--os-variant %s: osinfo-db (%s %s) has no such OS; virt-mm osinfo <search> lists them", v, db.Source, db.Version)
		}
		ov = found.For(h)
	}
	p := ov.Propose
	spec.OSVariant = ov.ID
	if f["os"] == "" {
		spec.OS = p.OS
	}
	if f["cpus"] == "" {
		spec.VCPUs = p.VCPUs
	}
	if f["memory"] == "" {
		spec.MemoryMiB = p.MemoryMiB
	}
	if spec.DiskPool != "" && spec.DiskGiB == 0 {
		spec.DiskGiB = p.DiskGiB
	}
	if f["firmware"] == "" {
		spec.Firmware = p.Firmware
	}
	if f["tpm"] == "" {
		spec.TPM = p.TPM
	}
	if f["disk-bus"] == "" {
		spec.DiskBus = p.DiskBus
	}
	if f["nic-model"] == "" {
		spec.NICModel = p.NICModel
	}
	return fmt.Sprintf("os %s (%s), %s", ov.Name, ov.ShortID, how), nil
}

// cmdOSInfo lists osinfo-db's operating systems, or recognises a volume.
func cmdOSInfo(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: virt-mm osinfo [<search>] [--detect <pool>/<volume>]")
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	h, err := hv.HostInfo(ctx, s)
	if err != nil {
		return hint(err)
	}
	db, err := hv.LoadOSDB(ctx, s)
	if err != nil {
		return err
	}
	if v := o.flags["detect"]; v != "" {
		pool, vol, _ := strings.Cut(v, "/")
		if vol == "" {
			return fmt.Errorf("--detect %q: want <pool>/<volume>", v)
		}
		d, err := hv.DetectOS(ctx, s, db, h, pool, vol)
		if err != nil {
			return err
		}
		out(o, d, func(w io.Writer) {
			if d.VolumeID != "" {
				fmt.Fprintf(w, "volume ID: %s\n", d.VolumeID)
			}
			if d.Variant == nil {
				fmt.Fprintf(w, "not recognised (osinfo-db %s %s)\n", db.Source, db.Version)
				return
			}
			p := d.Variant.Propose
			fmt.Fprintf(w, "%s (%s), osinfo-db %s %s\n", d.Variant.Name, d.Variant.ShortID, db.Source, db.Version)
			fmt.Fprintf(w, "proposes: %s, %d vCPUs, %d MiB, %d GiB, %s, TPM %v, disk %s, NIC %s\n",
				p.OS, p.VCPUs, p.MemoryMiB, p.DiskGiB, p.Firmware, p.TPM, p.DiskBus, p.NICModel)
		})
		return nil
	}
	list := db.Variants(h)
	if len(args) == 1 {
		q := strings.ToLower(args[0])
		list = slices.DeleteFunc(list, func(v hv.OSVariant) bool {
			return !strings.Contains(strings.ToLower(v.ShortID+" "+v.Name+" "+v.ID), q)
		})
	}
	res := app.OSList{Source: db.Source, Version: db.Version, Note: db.Note, Variants: list}
	out(o, res, func(w io.Writer) {
		fmt.Fprintf(w, "osinfo-db %s %s", db.Source, db.Version)
		if db.Note != "" {
			fmt.Fprintf(w, " (%s)", db.Note)
		}
		fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SHORT ID\tNAME\tEOL\tPROPOSES")
		for _, v := range list {
			p := v.Propose
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s %d MiB %d GiB %s %s/%s\n", v.ShortID, v.Name, or(v.EOL, "-"), p.OS, p.MemoryMiB, p.DiskGiB, p.Firmware, p.DiskBus, p.NICModel)
		}
		tw.Flush()
	})
	return nil
}

// cloudInitFlag reads --cloud-init user=alice,password,ssh-key=~/.ssh/id.pub,
// hostname=web01,user-data=file ("on" for none of them: a hostname only).
func cloudInitFlag(o opts, v string) (*hv.CloudInit, error) {
	c := &hv.CloudInit{}
	password := false
	for _, part := range strings.Split(v, ",") {
		k, val, _ := strings.Cut(strings.TrimSpace(part), "=")
		file := func() (string, error) {
			b, err := readFile(val)
			if err != nil {
				return "", fmt.Errorf("--cloud-init %s: %w", k, err)
			}
			return string(b), nil
		}
		var err error
		switch k {
		case "on", "":
		case "user":
			c.User = val
		case "password":
			password = true
		case "ssh-key":
			c.SSHKeys, err = file()
		case "hostname":
			c.Hostname = val
		case "user-data":
			c.UserData, err = file()
		default:
			return nil, fmt.Errorf("--cloud-init: %q is not user, password, ssh-key, hostname or user-data", k)
		}
		if err != nil {
			return nil, err
		}
	}
	if password {
		if c.User == "" {
			return nil, errors.New("--cloud-init password: give user= too (the image's default user takes only SSH keys)")
		}
		pw, err := newPassword(o, c.User)
		if err != nil {
			return nil, err
		}
		c.Password = pw
	}
	return c, nil
}

// parseSize reads a size such as 4096, 512M, 4G, 4GiB or 1T, in MiB; a bare
// number is in unit (M or G).
func parseSize(s, unit string) (int64, error) {
	s = strings.ToUpper(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(s), "B"), "I"))
	mult := map[string]int64{"M": 1, "G": 1 << 10, "T": 1 << 20}
	u := unit
	if n := len(s); n > 0 && mult[s[n-1:]] != 0 {
		u, s = s[n-1:], s[:n-1]
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	return v * mult[u], nil // in MiB
}

func cmdDelete(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: virt-mm rm <machine> [--disks] --yes")
	}
	if o.flags["yes"] == "" {
		return errors.New("this undefines the machine (and with --disks deletes its disks): add --yes")
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	d, err := hv.Lookup(ctx, s, args[0])
	if err != nil {
		return hint(err)
	}
	removed, err := hv.Delete(ctx, s, d.UUID, o.flags["disks"] != "")
	if removed == nil {
		removed = []string{}
	}
	out(o, map[string]any{"deleted": d.Name, "disks": removed}, func(w io.Writer) {
		fmt.Fprintf(w, "deleted %s\n", d.Name)
		for _, r := range removed {
			fmt.Fprintf(w, "deleted disk %s\n", r)
		}
	})
	return err
}

// readFile reads a file, or stdin for "-".
func readFile(name string) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(name)
}

func cmdXML(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: virt-mm xml <machine> [--set <file|->]")
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	d, err := hv.Lookup(ctx, s, args[0])
	if err != nil {
		return hint(err)
	}
	if v := o.flags["set"]; v != "" {
		b, err := readFile(v)
		if err != nil {
			return fmt.Errorf("--set: %w", err)
		}
		def, err := hv.Redefine(ctx, s, d.UUID, "", string(b))
		if err != nil {
			return err
		}
		out(o, def, func(w io.Writer) { fmt.Fprintf(w, "saved %s%s\n", def.Name, nextStart(def)) })
		return nil
	}
	def, err := hv.GetDefinition(ctx, s, d.UUID)
	if err != nil {
		return err
	}
	out(o, def, func(w io.Writer) { fmt.Fprint(w, def.XML) })
	return nil
}

func nextStart(d hv.Definition) string {
	if d.Active {
		return " (it is running: the change takes effect at its next start)"
	}
	return ""
}

// cmdEdit is virsh edit: the definition in the user's editor, saved when
// the editor exits with it changed; one libvirt refuses can be edited again.
func cmdEdit(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: virt-mm edit <machine>")
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	c, cancel := callCtx(ctx)
	d, err := hv.Lookup(c, s, args[0])
	var def hv.Definition
	if err == nil {
		def, err = hv.GetDefinition(c, s, d.UUID)
	}
	cancel()
	if err != nil {
		return hint(err)
	}
	return editLoop(ctx, def.Name, def.XML, func(ctx context.Context, x string) error {
		saved, err := hv.Redefine(ctx, s, def.UUID, def.XML, x)
		if err == nil {
			out(o, saved, func(w io.Writer) { fmt.Fprintf(w, "saved %s%s\n", saved.Name, nextStart(saved)) })
		}
		return err
	})
}

// editLoop opens x in $VISUAL or $EDITOR (vi without either) until the
// editor exits with it unchanged, or with a change save takes. A change
// save refuses can be edited again, unless the definition changed
// meanwhile (hv.ErrChanged).
func editLoop(ctx context.Context, name, x string, save func(ctx context.Context, edited string) error) error {
	editor := cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	f, err := os.CreateTemp("", "virt-mm-"+name+"-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(x)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	for {
		argv := append(strings.Fields(editor), f.Name())
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", editor, err)
		}
		b, err := os.ReadFile(f.Name())
		if err != nil {
			return err
		}
		if string(b) == x {
			fmt.Fprintf(os.Stderr, "%s unchanged\n", name)
			return nil
		}
		c, cancel := callCtx(ctx)
		err = save(c, string(b))
		cancel()
		if err == nil {
			return nil
		}
		fmt.Fprintln(os.Stderr, "virt-mm:", err)
		if errors.Is(err, hv.ErrChanged) || !confirm("edit again? [Y/n] ") {
			return errors.New("not saved")
		}
	}
}

func cmdDefine(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: virt-mm define <file|->")
	}
	b, err := readFile(args[0])
	if err != nil {
		return err
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	def, err := hv.Define(ctx, s, string(b))
	if err != nil {
		return hint(err)
	}
	out(o, def, func(w io.Writer) { fmt.Fprintf(w, "defined %s (%s)\n", def.Name, def.UUID) })
	return nil
}

func cmdSet(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: virt-mm set <machine> [--cpus N] [--memory 4G] … (see virt-mm help)")
	}
	var st hv.Settings
	f := o.flags
	for _, n := range []struct {
		flag string
		dst  **int
	}{{"cpus", &st.VCPUs}, {"sockets", &st.Sockets}, {"cores", &st.Cores}, {"threads", &st.Threads}} {
		if v := f[n.flag]; v != "" {
			i, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("--%s %q", n.flag, v)
			}
			*n.dst = &i
		}
	}
	for _, n := range []struct {
		flag string
		dst  **int
	}{{"memory", &st.MemoryMiB}, {"max-memory", &st.MaxMemoryMiB}} {
		if v := f[n.flag]; v != "" {
			size, err := parseSize(v, "M")
			if err != nil {
				return fmt.Errorf("--%s: %w", n.flag, err)
			}
			i := int(size)
			*n.dst = &i
		}
	}
	if v := f["autostart"]; v != "" {
		on, err := parseOnOff(v)
		if err != nil {
			return fmt.Errorf("--autostart: %w", err)
		}
		st.Autostart = &on
	}
	for _, n := range []struct {
		flag string
		dst  **string
	}{{"title", &st.Title}, {"description", &st.Description}, {"cpu-model", &st.CPUModel}, {"firmware", &st.Firmware}} {
		if v, ok := f[n.flag]; ok {
			*n.dst = &v
		}
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	d, err := hv.Lookup(ctx, s, args[0])
	if err != nil {
		return hint(err)
	}
	e, err := hv.Set(ctx, s, d.UUID, st)
	if err != nil {
		return err
	}
	out(o, e, func(w io.Writer) { printEdited(w, e) })
	return nil
}

func cmdCPUModels(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: virt-mm cpu-models <machine>")
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	s, d, err := machineOn(ctx, o, m, args[0])
	if err != nil {
		return err
	}
	cm, err := hv.GetCPUModels(ctx, s, d.UUID)
	if err != nil {
		return err
	}
	out(o, cm, func(w io.Writer) {
		fmt.Fprintf(w, "%s has: %s\n", d.Name, cm.Current)
		if len(cm.Modes) > 0 {
			fmt.Fprintf(w, "modes: %s\n", strings.Join(cm.Modes, ", "))
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "MODEL\tVENDOR\tUSABLE")
		for _, c := range cm.Models {
			usable := c.Usable
			if len(c.Blockers) > 0 {
				usable += " (lacks " + strings.Join(c.Blockers, ", ") + ")"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Name, c.Vendor, usable)
		}
		tw.Flush()
	})
	return nil
}

func cmdRename(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: virt-mm rename <machine> <new-name>")
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	d, err := hv.Lookup(ctx, s, args[0])
	if err != nil {
		return hint(err)
	}
	e, err := hv.Rename(ctx, s, d.UUID, args[1])
	if err != nil {
		return err
	}
	out(o, e, func(w io.Writer) { printEdited(w, e) })
	return nil
}

func cmdClone(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: virt-mm clone <machine> <new-name>")
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	c, cancel := callCtx(ctx)
	d, err := hv.Lookup(c, s, args[0])
	cancel()
	if err != nil {
		return hint(err)
	}
	// No deadline: the disks are copied in the call.
	cl, err := hv.Clone(ctx, s, d.UUID, args[1])
	if err != nil {
		return hint(err)
	}
	out(o, cl, func(w io.Writer) {
		fmt.Fprintf(w, "cloned %s as %s (%s), with new MACs\n", d.Name, cl.Domain.Name, cl.Domain.UUID)
		for _, dk := range cl.Disks {
			fmt.Fprintf(w, "  %s: %s → %s\n", dk.Target, dk.From, dk.To)
		}
		for _, sh := range cl.Shared {
			fmt.Fprintf(w, "  shared, not copied: %s\n", sh)
		}
		if cl.NVRAM != "" {
			fmt.Fprintf(w, "  UEFI variables copied to %s\n", cl.NVRAM)
		}
		for _, n := range cl.Notes {
			fmt.Fprintf(w, "  %s\n", n)
		}
	})
	return nil
}

// printEdited lists an edit's changes and, on a running machine, when
// each takes effect.
func printEdited(w io.Writer, e hv.Edited) {
	if len(e.Changes) == 0 {
		fmt.Fprintf(w, "%s: nothing changed\n", e.Name)
		return
	}
	fmt.Fprintf(w, "%s:\n", e.Name)
	for _, c := range e.Changes {
		switch {
		case !e.Active || c.Live:
			fmt.Fprintf(w, "  %s\n", c.What)
		case c.Note != "":
			fmt.Fprintf(w, "  %s  (at its next start: %s)\n", c.What, c.Note)
		default:
			fmt.Fprintf(w, "  %s  (at its next start)\n", c.What)
		}
	}
	for _, x := range e.Warnings {
		fmt.Fprintf(w, "warning: %s\n", x)
	}
}

func parseOnOff(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "on", "yes", "true", "1":
		return true, nil
	case "off", "no", "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("%q: on or off", v)
}

// confirm asks a yes/no question on the terminal; yes unless answered no.
func confirm(q string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	fmt.Fprint(os.Stderr, q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "" || a == "y" || a == "yes"
}

func cmdUpload(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: virt-mm upload <file> <pool> [--name <volume>]")
	}
	id, err := connID(o, m)
	if err != nil {
		return err
	}
	last := time.Now()
	vol, err := m.Upload(ctx, id, args[1], args[0], o.name, func(sent, total int64) {
		if time.Since(last) > time.Second || sent == total {
			last = time.Now()
			fmt.Fprintf(os.Stderr, "\r%s / %s", kib(uint64(sent)>>10), kib(uint64(total)>>10))
		}
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return hint(err)
	}
	out(o, vol, func(w io.Writer) { fmt.Fprintf(w, "uploaded %s/%s (%s)\n", vol.Pool, vol.Name, vol.Path) })
	return nil
}

func cmdPower(ctx context.Context, o opts, m *app.Manager, a hv.Action, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: virt-mm %s <machine>… [-c <conn>]", a)
	}
	if len(args) > 1 {
		return cmdPowerMany(ctx, o, m, a, args)
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	d, err := hv.Lookup(ctx, s, args[0])
	if err != nil {
		return hint(err)
	}
	if err := hv.Power(ctx, s, d.UUID, a); err != nil {
		return err
	}
	out(o, map[string]string{"uuid": d.UUID, "action": string(a)}, func(w io.Writer) { fmt.Fprintf(w, "%s: %s\n", d.Name, a) })
	return nil
}

// cmdPowerMany does a to several machines of the connection at once.
func cmdPowerMany(ctx context.Context, o opts, m *app.Manager, a hv.Action, args []string) error {
	s, err := session(o, m)
	if err != nil {
		return err
	}
	id, err := connID(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	refs := make([]app.MachineRef, len(args))
	names := map[app.MachineRef]string{}
	for i, ref := range args {
		d, err := hv.Lookup(ctx, s, ref)
		if err != nil {
			return hint(err)
		}
		refs[i] = app.MachineRef{Connection: id, UUID: d.UUID}
		names[refs[i]] = d.Name
	}
	rs, err := m.PowerMany(ctx, refs, string(a))
	if err != nil {
		return err
	}
	type result struct {
		Name   string `json:"name"`
		UUID   string `json:"uuid"`
		Action string `json:"action"`
		Error  string `json:"error,omitempty"`
	}
	res := make([]result, len(rs))
	for i, r := range rs {
		res[i] = result{names[r.MachineRef], r.UUID, string(a), r.Error}
	}
	out(o, res, func(w io.Writer) {
		for _, r := range res {
			if r.Error != "" {
				fmt.Fprintf(w, "%s: %s failed: %s\n", r.Name, a, r.Error)
			} else {
				fmt.Fprintf(w, "%s: %s\n", r.Name, a)
			}
		}
	})
	return app.PowerErrors(rs, names)
}

func cmdHostDevs(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: virt-mm hostdevs [<device>]")
	}
	s, err := session(o, m)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	if len(args) == 1 {
		x, err := hv.HostDeviceXML(ctx, s, args[0])
		if err != nil {
			return hint(err)
		}
		out(o, map[string]string{"name": args[0], "xml": x}, func(w io.Writer) { fmt.Fprint(w, x) })
		return nil
	}
	ds, err := hv.HostDevices(ctx, s)
	if err != nil {
		return hint(err)
	}
	out(o, ds, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tADDRESS\tID\tDEVICE\tDRIVER\tIOMMU\tUSED BY\tNAME")
		for _, d := range ds {
			what := strings.TrimSpace(d.Vendor + " " + d.Product)
			if what == "" && d.Class != "" {
				what = "class " + d.Class
			}
			group := "-"
			if d.IOMMUGroup != nil {
				group = strconv.Itoa(*d.IOMMUGroup)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s:%s\t%s\t%s\t%s\t%s\t%s\n", d.Kind, d.Address, d.VendorID, d.ProductID,
				or(what, "-"), or(d.Driver, "-"), group, or(strings.Join(d.UsedBy, ","), "-"), d.Name)
		}
		tw.Flush()
	})
	return nil
}

// stateText is a machine's state as the list shows it.
func stateText(d hv.Domain) string {
	if d.Saved {
		return string(d.State) + " (saved)"
	}
	return string(d.State)
}

func cmdSave(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: virt-mm save <machine> [--rm]")
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	s, d, err := machineOn(ctx, o, m, args[0])
	if err != nil {
		return err
	}
	res := map[string]any{"uuid": d.UUID, "name": d.Name}
	if o.flags["rm"] != "" {
		had, err := hv.DropSave(ctx, s, d.UUID)
		if err != nil {
			return err
		}
		res["dropped"] = had
		out(o, res, func(w io.Writer) {
			if had {
				fmt.Fprintf(w, "%s: saved state dropped; its next start boots afresh\n", d.Name)
			} else {
				fmt.Fprintf(w, "%s has no saved state\n", d.Name)
			}
		})
		return nil
	}
	if err := hv.Save(ctx, s, d.UUID); err != nil {
		return err
	}
	res["saved"] = true
	out(o, res, func(w io.Writer) {
		fmt.Fprintf(w, "%s: saved and stopped; `virt-mm start %s` resumes it\n", d.Name, d.Name)
	})
	return nil
}

func cmdHostKey(ctx context.Context, o opts, m *app.Manager, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: virt-mm hostkey trust <conn> | virt-mm hostkey forget <host[:port]>")
	}
	switch args[0] {
	case "forget":
		n, err := m.HostKeys().Forget(args[1])
		if err != nil {
			return err
		}
		out(o, map[string]int{"removed": n}, func(w io.Writer) {
			fmt.Fprintf(w, "removed %d key(s) of %s from %s\n", n, args[1], m.HostKeys().Own)
		})
		return nil
	case "trust":
		o.conn = args[1]
		s, err := session(o, m)
		if err != nil {
			return err
		}
		ctx, cancel := callCtx(ctx)
		defer cancel()
		err = s.Connect(ctx)
		var unknown *conn.UnknownHostKeyError
		if !errors.As(err, &unknown) {
			if err == nil {
				out(o, map[string]bool{"known": true}, func(w io.Writer) { fmt.Fprintln(w, "the host key is already known") })
			}
			return err
		}
		if o.json {
			return errors.New("host key unknown: run without --json to answer")
		}
		fmt.Printf("%s offers the %s key\n  %s\nTrust it? [y/N] ", unknown.Addr, unknown.Type, unknown.Fingerprint)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("not trusted")
		}
		if err := m.HostKeys().Trust(unknown.Addr, unknown.Key); err != nil {
			return err
		}
		fmt.Printf("recorded in %s\n", m.HostKeys().Own)
		return nil
	}
	return fmt.Errorf("unknown hostkey command %q", args[0])
}

// hint adds what to do next to errors the user can act on.
func hint(err error) error {
	var unknown *conn.UnknownHostKeyError
	if errors.As(err, &unknown) {
		return fmt.Errorf("%w\nrun `virt-mm hostkey trust <conn>` to check and record it", err)
	}
	var changed *conn.HostKeyChangedError
	if errors.As(err, &changed) {
		return fmt.Errorf("%w\nif you know why it changed: `virt-mm hostkey forget %s`, then trust the new key", err, changed.Addr)
	}
	return err
}

func kib(n uint64) string {
	switch {
	case n == 0:
		return "-"
	case n%(1<<20) == 0:
		return fmt.Sprintf("%d GiB", n>>20)
	case n >= 1<<20:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d MiB", n>>10)
}

func out(o opts, v any, text func(io.Writer)) int {
	if o.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(v)
	} else {
		text(os.Stdout)
	}
	return 0
}

func fail(o opts, err error) int {
	if o.json {
		json.NewEncoder(os.Stdout).Encode(map[string]string{"error": err.Error()})
	} else {
		fmt.Fprintln(os.Stderr, "virt-mm:", err)
	}
	return 1
}

// termPrompter asks on the terminal, without echo, and whether to
// remember the answer when the keychain can.
type termPrompter struct{}

func (termPrompter) Ask(q conn.Question) (conn.Answer, error) {
	if q.Refused {
		fmt.Fprintf(os.Stderr, "the remembered %s was refused; it is forgotten\n", strings.TrimPrefix(q.Secret.String(), "the "))
	}
	text := fmt.Sprintf("%s@%s's password: ", q.User, q.Addr)
	if q.Kind == "passphrase" {
		text = fmt.Sprintf("passphrase for %s: ", q.Key)
	}
	v, err := ask(text)
	if err != nil || !q.CanRemember {
		return conn.Answer{Value: v}, err
	}
	fmt.Fprint(os.Stderr, "remember it in the keychain? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return conn.Answer{Value: v, Remember: a == "y" || a == "yes"}, nil
}

func (termPrompter) NotRemembered(s conn.Secret, err error) {
	fmt.Fprintf(os.Stderr, "virt-mm: %s was not remembered: %v\n", s, err)
}

func ask(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", conn.ErrNoPrompt
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(b), err
}
