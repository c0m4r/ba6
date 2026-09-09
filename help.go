// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

var appletHelp = map[string]string{ //nolint:gosec // G101: command help contains words such as "prefix", not credentials.
	"adduser": `Usage: adduser [USERADD_OPTION] USER
       adduser USER GROUP
Create a locked account with a private group and home directory, or add an
existing account to GROUP. It accepts the documented useradd options.`,
	"bzip2": `Usage: bzip2 [-cdkf] [FILE]...
Compress files as bzip2 streams. -d decompresses, -c uses standard output, -k
keeps inputs, and -f replaces an existing output.`,
	"bunzip2": `Usage: bunzip2 [-ckf] [FILE]...
Decompress bzip2 streams. -c uses standard output, -k keeps inputs, and -f
replaces an existing output.`,
	"blockdev": `Usage: blockdev OPERATION [VALUE] DEVICE
Perform a focused Linux block-device ioctl.

Operations:
  --getro/--setro/--setrw  query or change read-only state
  --getsize64              report the device size in bytes
  --getsz                  report the size in 512-byte sectors
  --getss/--getbsz         print logical sector/I/O block size
  --getra/--setra SECTORS  query or set readahead
  --flushbufs/--rereadpt   flush buffers or reread the partition table
  --report DEVICE...       display a compact device report`,
	"cfdisk": `Usage: cfdisk [-L|--color[=auto|always|never]] [--read-only] [--zero]
              [--sector-size 512] [--lock[=yes|no|nonblock]] DEVICE
       cfdisk -h|--help
       cfdisk -V|--version
Interactively edit a DOS/MBR or GPT partition table in a terminal. Up/Down
selects an MBR slot or a visible GPT entry; Left/Right selects the New/Quit/
Help/Write/Dump action bar and Enter invokes it. On a disk with no recognizable
table, a label-type selector offers GPT and DOS with GPT selected by default. n
creates, d deletes, r resizes, and s sorts partitions. For DOS, t changes a
hexadecimal type and b
toggles the boot flag. For GPT, t accepts linux, swap, efi, or a type GUID; GPT
does not have an MBR boot flag. u writes an sfdisk-style text dump, x toggles
extra information, W (or w) writes after an explicit “yes”, and q quits.
K/M/G/T and KiB/MiB suffixes are accepted for new and resized partition sizes.

Every proposed layout is range- and overlap-validated before writing. GPT
writes matching primary and backup headers and entry arrays; changed images and
mounted or active-swap targets are rejected before either label is written.
--read-only disables disk writes; --zero opens the label selector with an empty
in-memory table. --lock defaults to no lock, yes blocks for an advisory lock,
and nonblock fails immediately if it is held. --color=never disables
reverse-video styling.

GPT support is intentionally bounded to 512-byte logical sectors and the
conventional 128-entry, 128-byte-entry layout. DOS support is limited to four
primary partitions. Extended/logical MBR partitions, SGI, and SUN labels are
unsupported. ba6 sfdisk only replays DOS dumps; a GPT dump is for inspection or
compatible external tooling.`,
	"fdisk": `Usage: fdisk [-l] [-x] [-s DEVICE] [-b SIZE] [DEVICE]...
Inspect partition tables on block devices or disk images.

Options:
  -l, --list               Display partition layout for devices
  -x, --list-details       Include extra partition details in output
  -s, --getsz              Print capacity in 512-byte sectors and exit
      --bytes              Report raw byte count instead of human friendly units
  -b, --sector-size SIZE   Override logical sector dimension in bytes
  -B, --protect-boot       Preserve initial boot sector bytes
  -c, --compatibility MODE Select dos or nondos compatibility mode
  -L, --color WHEN         Control output colorization
      --lock MODE          Control device lock protocol
  -n, --noauto-pt          Do not synthesize partition table on blank media
  -o, --output COLS        Specify columns to include in listing
  -t, --type TYPE          Filter or match specific partition table format
  -u, --units UNIT         Select display units (sectors or cylinders)
  -C, --cylinders NUM      Set cylinder count for disk geometry
  -H, --heads NUM          Set head count for disk geometry
  -S, --sectors NUM        Set sectors per track for disk geometry
  -w, --wipe WHEN          Wipe signatures from disk
  -W, --wipe-partitions WHEN Wipe signatures from individual partitions`,
	"sfdisk": `Usage: sfdisk [--force] DEVICE
       sfdisk --dump DEVICE
       sfdisk --list DEVICE
Read or write a DOS/MBR partition table. Write mode accepts up to four lines
from standard input with start=, size=, type=, and bootable fields. Units are
512-byte sectors; K, M, and G suffixes are accepted. All ranges and overlaps
are validated before the single-sector write. GPT and extended partitions are
intentionally unsupported.`,
	"fsck": `Usage: fsck [OPTIONS] [-t FSTYPE] DEVICE...
Validate ext-family superblock geometry, feature flags, group metadata, the
root inode, and root directory structure. This checker is strictly read-only;
it reports damage with fsck status bit 4 and never attempts repair.

Options:
  -A             walk /etc/fstab and check listed filesystems
  -C             display graphical completion and progress meters
  -l             lock the device with exclusive flock
  -M             skip checking mounted filesystems
  -N             perform dry run showing what commands would execute
  -P             scan root filesystem concurrently with other devices
  -r             interactive repair mode and statistics reporting
  -R             omit root filesystem when checking via -A
  -s             serialize filesystem checking sequentially
  -t TYPE        explicit filesystem driver type (ext2/ext3/ext4)
  -T             suppress startup title banner display`,
	"fsck.ext2": `Usage: fsck.ext2 [OPTION]... DEVICE...
Validate ext2, ext3, or ext4 filesystem integrity.

  -a, -p          automatic non-interactive repair mode
  -b SUPERBLOCK   use alternate superblock location
  -B BLOCKSIZE    specify block size for alternate superblock
  -c              scan device blocks for read errors
  -C FD           stream completion metrics to descriptor
  -d              output diagnostic tracing information
  -D              request directory index optimization
  -E OPTS         configure filesystem tuning options
  -f              perform checks regardless of clean flag
  -F              flush disk cache buffers prior to run
  -j JOURNAL      associate external journal volume
  -k              preserve existing defect list entries
  -l FILE         read additional defect blocks from list
  -L FILE         replace defective block inventory from file
  -n              open read-only without applying modifications
  -r              interactive question mode (ignored)
  -t              display execution timing statistics
  -v              verbose inspection output
  -y              assume affirmative response to queries
  -z UNDO         record state rollbacks into undo archive`,
	"fsck.ext3": `Usage: fsck.ext3 [OPTION]... DEVICE...
Validate ext2, ext3, or ext4 filesystem integrity.

  -a, -p          automatic non-interactive repair mode
  -b SUPERBLOCK   use alternate superblock location
  -B BLOCKSIZE    specify block size for alternate superblock
  -c              scan device blocks for read errors
  -C FD           stream completion metrics to descriptor
  -d              output diagnostic tracing information
  -D              request directory index optimization
  -E OPTS         configure filesystem tuning options
  -f              perform checks regardless of clean flag
  -F              flush disk cache buffers prior to run
  -j JOURNAL      associate external journal volume
  -k              preserve existing defect list entries
  -l FILE         read additional defect blocks from list
  -L FILE         replace defective block inventory from file
  -n              open read-only without applying modifications
  -r              interactive question mode (ignored)
  -t              display execution timing statistics
  -v              verbose inspection output
  -y              assume affirmative response to queries
  -z UNDO         record state rollbacks into undo archive`,
	"fsck.ext4": `Usage: fsck.ext4 [OPTION]... DEVICE...
Validate ext2, ext3, or ext4 filesystem integrity.

  -a, -p          automatic non-interactive repair mode
  -b SUPERBLOCK   use alternate superblock location
  -B BLOCKSIZE    specify block size for alternate superblock
  -c              scan device blocks for read errors
  -C FD           stream completion metrics to descriptor
  -d              output diagnostic tracing information
  -D              request directory index optimization
  -E OPTS         configure filesystem tuning options
  -f              perform checks regardless of clean flag
  -F              flush disk cache buffers prior to run
  -j JOURNAL      associate external journal volume
  -k              preserve existing defect list entries
  -l FILE         read additional defect blocks from list
  -L FILE         replace defective block inventory from file
  -n              open read-only without applying modifications
  -r              interactive question mode (ignored)
  -t              display execution timing statistics
  -v              verbose inspection output
  -y              assume affirmative response to queries
  -z UNDO         record state rollbacks into undo archive`,
	"groupadd": `Usage: groupadd [-g GID] GROUP
Create a local group in /etc/group. Requires root.`,
	"getty": `Usage: getty [OPTION]... LINE [BAUD_RATE[,BAUD_RATE...]] [TERM]
Open LINE (a path under /dev, or "-" for an already-connected stdin), claim it
as the controlling terminal, optionally show /etc/issue, read a login name,
and exec a login program. TERM defaults to linux on a numbered virtual
console and vt100 otherwise.

Options:
  -a, --autologin USER        skip the login-name prompt, pre-filling USER
  -n, --skip-login            start the login program with no name at all
  -l, --login-program PROGRAM run PROGRAM instead of login (default login)
  -r, --chroot DIRECTORY      change root to DIRECTORY before the handoff
  -L, --local-line[=MODE]     set CLOCAL to auto, always, or never
  -J, --noclear               do not clear the screen first
  -i, --noissue               do not display /etc/issue
  -8, --8bits                 assume the line is 8-bit clean
  -p, --login-pause           wait for a key before the login prompt
  -t, --timeout SECONDS       give up if no name is entered in time

Unlike the login program's own -f autologin bypass, --autologin here only
pre-fills the name; the account's password is still required.`,
	"cpio": `Usage: cpio -o|-i|-t [-v] [-F ARCHIVE] [-H newc]
Create, extract, or list newc cpio archives. Create mode reads one input path
per line from standard input; extraction rejects escaping paths.`,
	"md5sum": `Usage: md5sum [OPTION]... [FILE]...
Compute or check MD5 digests. -c verifies checksum files; -b/-t select the
printed marker; --quiet and --status control verification output.`,
	"mkfs": `Usage: mkfs -t ext2|ext3|ext4|xfs|btrfs [OPTION]... DEVICE [BLOCKS]
Create a filesystem by dispatching to the matching bundled formatter. Each one
writes a single carefully bounded profile rather than a configurable layout.`,
	"mkfs.ext2": `Usage: mkfs.ext2 [-F] [-L LABEL] DEVICE [BLOCKS]
Create a revision-1 ext2 filesystem with 4 KiB blocks, one block group, a root
directory, and lost+found. Supported sizes are 1 MiB through 128 MiB. BLOCKS
is expressed in 1 KiB units. -F is required for regular files. Mounted devices
and active swap are rejected unless explicitly forced.`,
	"mkfs.ext3": `Usage: mkfs.ext3 [-F] [-L LABEL] DEVICE [BLOCKS]
Create the ext2 profile plus a 4 MiB JBD2 journal in reserved inode 8, marked
clean so no recovery is needed at first mount. Supported sizes are 8 MiB
through 128 MiB. BLOCKS is expressed in 1 KiB units. -F is required for
regular files. Mounted devices and active swap are rejected unless forced.`,
	"mkfs.ext4": `Usage: mkfs.ext4 [-F] [-L LABEL] DEVICE [BLOCKS]
Create the ext3 profile with 256-byte inodes and extent-mapped files, which is
the feature set that identifies a filesystem as ext4. Supported sizes are
8 MiB through 128 MiB. BLOCKS is expressed in 1 KiB units. -F is required for
regular files. Mounted devices and active swap are rejected unless forced.`,
	"mkfs.xfs": `Usage: mkfs.xfs [OPTION]... DEVICE [BLOCKS]
Format target block device or image with an XFS filesystem.

  -b OPTS         block size parameters (e.g. size=4096)
  -c OPTS         configuration file overrides
  -d OPTS         data section controls (e.g. agcount=4, size=SZ)
  -f              override existing signatures and regular file safeguards
  -i OPTS         inode table dimensions and allocation options
  -l OPTS         transaction log specifications (e.g. internal=1)
  -m OPTS         metadata attributes and feature flags
  -n OPTS         directory naming structure options
  -p FILE         read directory hierarchy prototype from file
  -q              suppress informational layout banners
  -r OPTS         realtime extent and volume parameters
  -s OPTS         sector dimension settings
  -L LABEL        assign volume identifier string
  -N              dry-run mode without modifying target storage
  -K              bypass trim discards during formatting`,
	"mkfs.btrfs": `Usage: mkfs.btrfs [OPTION]... DEVICE [BLOCKS]
Initialize a Btrfs filesystem on the given storage target.

  -b, --byte-count BYTES    restrict created volume capacity to given size
  -d, --data PROFILE        chunk profile layout for data chunks
  -m, --metadata PROFILE    chunk allocation profile for metadata blocks
  -M, --mixed               blend data and metadata inside common chunks
  -n, --nodesize SIZE       tree node allocation magnitude in bytes
  -s, --sectorsize SIZE     leaf alignment and minimum IO sector size
  -L, --label LABEL         assign filesystem volume label
  -K, --nodiscard           suppress initial discard trim operations
  -r, --rootdir DIR         populate root tree from specified directory
  -u, --subvol NAME         name initial default subvolume
  -O, --features LIST       comma-separated filesystem on-disk features
  -R, --runtime-features L  runtime filesystem attributes
  -U, --uuid UUID           assign explicit filesystem identifier
      --device-uuid UUID    assign target block storage UUID
      --csum, --checksum A  digest algorithm for chunk verification
      --compress TYPE       default data compression mode
      --inode-flags FLAGS   initial flags for created inodes
      --reflink MODE        reflink operation control
      --shrink              shrink filesystem boundary to fit media
  -f, --force               overwrite active filesystem signatures
  -q, --quiet               silence layout status reports
  -v, --verbose             display expanded filesystem parameters`,
	"mkswap": `Usage: mkswap [OPTION]... DEVICE [SIZE]
Set up a Linux swap area on a device or in a file. SIZE is in 1024 byte blocks
and defaults to the whole device.

Options:
  -c, --check           read the area first and record the pages that fail
  -f, --force           go ahead even when the area is in use or too small
  -q, --quiet           print neither the summary nor the warnings
  -p, --pagesize SIZE   use this page size instead of the kernel's
  -L, --label LABEL     store a label; anything past fifteen bytes is cut
  -v, --swapversion NUM only version 1 exists
  -U, --uuid UUID       store this UUID, or one of clear, random and time
  -e, --endianness NAME write the header native, little or big endian
  -o, --offset OFFSET   put the swap area this far into the device
  -s, --size SIZE       the size of the swap file to create, with -F
  -F, --file            make the file private to its owner, and create it
                        when a size was given
      --verbose         verbose output
      --lock[=MODE]     take a BSD lock: yes, no or nonblock
      --help            show this help

An old filesystem signature in the way is reported and erased.`,
	"mtr": `Usage: mtr [OPTION]... HOST
Probe every hop on the route to HOST and keep loss and latency statistics for
each one. On a terminal the display refreshes continuously like the original
curses interface; redirected output and -r print a one-shot report instead.

Options:
  -F, --filename FILE     read target hostnames from FILE
  -r, --report            print a report instead of the live display
  -w, --report-wide       report without truncating host names (implies -r)
  -j, --json              emit report formatted as JSON
  -x, --xml               emit report formatted as XML
  -C, --csv               emit report formatted as CSV
  -l, --raw               emit raw probe telemetry events
  -p, --split             emit space-delimited split cycle metrics
  -t, --curses            force interactive terminal display
  -c, --report-cycles N   stop after N cycles (report 10, live unlimited)
  -i, --interval SECONDS  delay between cycles (default 1)
  -Z, --timeout SECONDS   time to wait for a reply (default 1)
  -G, --gracetime SECONDS wait time between probe transmissions
  -m, --max-ttl N         highest hop to probe (default 30)
  -f, --first-ttl N       first hop to probe (default 1)
  -U, --max-unknown N     consecutive unanswered hops before stopping
  -E, --max-display-path N maximum path count to display
  -s, --psize N           probe payload bytes (default 56)
  -B, --bitpattern N      fill payload with byte pattern
  -Q, --tos N             set IP type-of-service header byte
  -M, --mark MARK         set packet socket mark
  -I, --interface NAME    route probes via network device NAME
  -a, --address ADDR      bind outgoing socket to source ADDR
  -P, --port PORT         target port number for probes
  -L, --localport PORT    originating source port number
  -o, --order FIELDS      customize statistics column order (e.g. "LSD NBAWV")
  -z, --aslookup          display autonomous system numbers
  -y, --ipinfo N          display IP prefix or AS information
  -e, --mpls              decode MPLS headers from ICMP extensions
  -n, --no-dns            show addresses instead of names
  -b, --show-ips          show names together with addresses
  -u, --udp               probe with UDP instead of ICMP echo
  -T, --tcp               probe with TCP packets
  -S, --sctp              probe with SCTP packets
  -4, -6                  force IPv4 or IPv6
  --displaymode MODE      select initial screen layout mode
  --help                  show this help

Probes are ICMP echo requests as in the original, because the high UDP ports
traceroute uses are commonly filtered before the destination. Unprivileged
ICMP datagram sockets are used where net.ipv4.ping_group_range permits them,
otherwise probing falls back to Linux's UDP error queue. A sweep stops after
five consecutive unanswered hops.

Interactive keys: h help, n toggle DNS, p pause, SPACE resume, r restart
statistics, q quit.`,
	"swapon": `Usage: swapon [OPTION]... [DEVICE]...
Enable swap devices. With no device and no option it lists the ones in use, as
the original does. Requires CAP_SYS_ADMIN to enable anything.

Options:
  -a, --all              enable every swap entry in fstab
  -e, --ifexists         pass over a device that is not there
  -p, --priority=N       give the device this priority
  -o, --options=LIST     comma-separated options; only pri= reaches the kernel
  -T, --fstab=FILE       read FILE instead of /etc/fstab
  -L LABEL, -U UUID      name the device by its label or uuid
  -s, --summary          print /proc/swaps, whose layout this is
  --show[=COLUMNS]       the table form: NAME, TYPE, SIZE, USED, PRIO, and the
                         empty UUID and LABEL columns
  --output-all           every column
  --noheadings           leave the heading out
  --raw                  one space between columns, no padding
  --bytes                sizes as byte counts rather than scaled
  -v, --verbose          name each device as it is enabled
  -f, -d                 accepted; no swap area is reinitialised here and
                         discards are left to the kernel's defaults
  --help                 show this help`,
	"sysctl": `Usage: sysctl [-aenN] [-w] NAME[=VALUE]...
Read or write Linux /proc/sys settings. -a lists all settings, -n prints values
without names, -N prints names without values, -e passes over failures, and -w
requires assignments. Settings holding several lines repeat their name on each.`,
	"swapoff": `Usage: swapoff [OPTION]... [DEVICE]...
Disable swap devices. Requires CAP_SYS_ADMIN.

Options:
  -a, --all              disable every swap area in /proc/swaps
  -L LABEL, -U UUID      name the device by its label or uuid
  -v, --verbose          name each device as it is disabled
  --help                 show this help`,
	"awk": `Usage: awk [-F SEPARATOR] [-v NAME=VALUE] PROGRAM [FILE]...
Process text as records and fields.

Supported rules include BEGIN, END, /REGEX/, and expressions. Actions support
print, printf, variable assignment and arithmetic assignment, next, and exit.
Built-ins include NR, FNR, NF, FS, OFS, ORS, length, int, substr, tolower, and
toupper. Regexes are POSIX EREs; a one-character FS (other than space) is
literal, while a multi-character FS is an ERE. Arrays, user functions, getline,
redirection, and system() are omitted.`,
	"chroot": `Usage: chroot NEW_ROOT [COMMAND [ARG]...]
Run COMMAND with NEW_ROOT as the filesystem root. The default command is
/bin/sh -i. Requires appropriate privilege.`,
	"dd": `Usage: dd [if=FILE] [of=FILE] [bs=N] [count=N] [skip=N] [seek=N]
Copy data in blocks. ibs= and obs= set separate block sizes. Supported
conversions are notrunc, sync, and noerror; status=none suppresses statistics.
Size suffixes include c, w, b, K, kB, M, MB, G, and GB.`,
	"file": `Usage: file [-b] FILE...
Identify filesystem objects and common data formats using metadata and magic
bytes. -b omits file names from output.`,
	"insmod": `Usage: insmod [-fsv] MODULE_FILE [PARAMETER=VALUE]...
Insert a kernel module using finit_module, with an init_module fallback on old
kernels. A compressed module is handed to the kernel to decompress. Requires
CAP_SYS_MODULE.

Options:
  -f, --force    load a module built for another kernel version anyway
  -v, --verbose  name the file as it is loaded
  -s, --syslog   accepted; nothing here logs
  --help         show this help`,
	"losetup": `Usage: losetup [OPTION]... [LOOPDEV [FILE]]
With no operands, list the loop devices in use as a table. Attaching or
detaching one needs CAP_SYS_ADMIN.

Options:
  -a, --all              the older listing form, "/dev/loopN: [dev]:ino (file)"
  -l, --list             the table form (the default with no operands)
  -O, --output=COLUMNS   choose the columns: NAME, SIZELIMIT, OFFSET,
                         AUTOCLEAR, RO, BACK-FILE, DIO, LOG-SEC
  -n, --noheadings       leave the heading out; only valid beside a listing
  --raw                  one space between columns, no padding
  -j, --associated=FILE  only the devices backed by FILE
  -f, --find             print the first unused device, or use it for FILE
  --show                 print the device a successful attach used
  -d, --detach=DEV       detach a device
  -D, --detach-all       detach every device in use
  -r, --read-only        attach read-only
  -o, --offset=N         start the mapping N bytes into the file
  --sizelimit=N          map only N bytes of it
  -P, -b, --direct-io, -c, -v
                         accepted; these change a device after it is attached
                         and are left to the kernel's defaults
  --help                 show this help

The bracketed device and inode of the -a form come from an ioctl the kernel
only answers for a caller that can open the device, so they are empty for an
unprivileged run — which is what the original prints then too.`,
	"login": `Usage: login [OPTION]... [USERNAME]
Authenticate a user against /etc/passwd and /etc/shadow, initialize their
supplementary groups and environment, and start their configured login shell.
SHA-256 ($5$) and SHA-512 ($6$) crypt password hashes are supported. The applet
must start as root; locked and expired accounts are rejected.

Options:
  -p              preserve existing environment variables across session setup
  -f              skip authentication for pre-authenticated user
  -H              omit printing host name in login prompt
  -s, --shell=SH  override default login shell executable path
  -h HOST         record incoming remote host identifier`,
	"passwd": `Usage: passwd [OPTIONS] [USERNAME]
Change a user's password in /etc/shadow, or in /etc/passwd for a legacy account.
Ordinary users may change only their own password and must enter the current
password; sufficient permission to update the password database is still
required. Root may name any user and can replace unsupported or locked hashes.
New passwords are stored as salted SHA-512 crypt hashes.

Options:
  -a, --all               inspect status of all account records
  -d, --delete            clear password entry making it blank
  -e, --expire            force immediate credential expiration
  -k, --keep-tokens       retain existing expired authentication tokens
  -i, --inactive DAYS     duration before expired credentials become unusable
  -l, --lock              disable account access by locking credentials
  -u, --unlock            restore access by removing credential lock
  -q, --quiet             suppress diagnostic operational messages
  -r, --repository REPO   specify database storage backend repository
  -R, --root DIR          apply modifications inside alternate chroot directory
  -P, --prefix DIR        alternate base path prefix for account files
  -S, --status            display brief account credential status summary
  -w, --warndays DAYS     advance warning interval before expiration
  -x, --maxdays DAYS      maximum lifespan of credentials in days
  -n, --mindays DAYS      minimum elapsed days between password modifications
  --expiredate DATE       set fixed account termination date
  -s, --stdin             consume new password string from standard input stream`,

	"paste": `Usage: paste [OPTION]... [FILE]...
Write the lines of each FILE side by side, separated by cycled delimiter
characters.

Options:
  -d LIST   use the characters of LIST as delimiters instead of TAB
  -s        write each FILE on a single line instead
  -z        use NUL instead of newline as the record terminator
  --help    show this help`,
	"lsof": `Usage: lsof [-nP] [-p PID,...] [-i] [FILE]...
List process file descriptors by inspecting /proc. -p selects processes, -i
selects IPv4/IPv6 sockets, and FILE operands select exact open paths. Entries
that kernel permissions hide are skipped.`,
	"lsblk": `Usage: lsblk [-abn] [-o COLUMN,...]
List Linux block devices from sysfs. Columns include NAME, KNAME, MAJ:MIN, RM,
SIZE, RO, TYPE, MOUNTPOINT, and MOUNTPOINTS.`,
	"lsmod": `Usage: lsmod [-sv]
Display the loaded kernel modules in kmod's own columns, reading the sizes and
reference counts from /proc/modules and the holder list from sysfs, which is
where kmod reads it and why the two agree on its order.

Options:
  -s, -v    accepted; nothing here logs and there is nothing more to say
  --help    show this help`,

	"lspci": `Usage: lspci [OPTION]...
List PCI devices from /sys with vendor, device, and class names from pci.ids
when it is installed.

Options:
  -n        print numeric IDs instead of names
  --help    show this help`,
	"lsusb": `Usage: lsusb [OPTION]...
List USB devices from /sys with vendor and product names from usb.ids when it
is installed.

Options:
  --help    show this help`,
	"mktemp": `Usage: mktemp [-d] [-p DIRECTORY] [TEMPLATE]
Create a securely named temporary file or directory. TEMPLATE must contain a
run of at least three X characters, which are replaced -- exactly those, and no
others -- by random alphanumerics. Text after the run is kept as a suffix.`,
	"modprobe": `Usage: modprobe [OPTION]... MODULE [PARAMETER=VALUE]...
       modprobe -r [OPTION]... MODULE...
Load or remove a module and its dependencies, using the running kernel's
modules.dep, modules.alias and modules.builtin files. Modules already loaded
are left alone, and a removal stops at a module something still refers to.

Options:
  -a, --all              treat every operand as a module name
  -r, --remove           remove instead of inserting
  -n, --dry-run          go through the motions without loading anything
  -D, --show-depends     list what would be loaded, in order
  -v, --verbose          name each module as it is handled
  -q, --quiet            say nothing about a module that is not there
  -f, --force            load a module built for another kernel version
  --first-time           fail if the module is already loaded or removed
  -d, --dirname=DIR      look under DIR instead of /lib/modules
  -S, --set-version=VER  use that kernel version's directory
  -i, -b, -s, -C, -w, --remove-holders
                         accepted; there are no install or remove commands to
                         ignore here and no blacklist is consulted
  --help                 show this help`,
	"rmmod": `Usage: rmmod [-fsv] MODULE...
Remove kernel modules. A name that is not loaded is reported as such before the
kernel is asked, as kmod reports it. Requires CAP_SYS_MODULE; -f also requires
kernel support for forced module unloading.

Options:
  -f, --force    force the unload, skipping the loaded check
  -v, --verbose  name each module as it is removed
  -s, --syslog   accepted; nothing here logs
  --help         show this help`,
	"pivot_root": `Usage: pivot_root NEW_ROOT PUT_OLD
Move the root filesystem to PUT_OLD and make NEW_ROOT the new root. A thin
wrapper around the pivot_root(2) syscall; it does not chdir or exec anything.
Requires appropriate privilege.`,
	"switch_root": `Usage: switch_root NEW_ROOT NEW_INIT [ARG]...
As PID 1, pivot to NEW_ROOT, move API filesystem mounts, detach the old root,
and execute NEW_INIT. NEW_ROOT must be a usable root filesystem.`,
	"timeout": `Usage: timeout [-s SIGNAL] [-k DURATION] DURATION COMMAND [ARG]...
Run COMMAND and signal its process group if it exceeds DURATION. Exit status
124 indicates a timeout.`,
	"top": `Usage: top [OPTION]...
Display a live Linux process monitor, or a script-friendly batch report.

Options:
  -b, --batch, --batch-mode     write reports without terminal control
  -n, --iterations N            stop after N reports
  -d, --delay SECONDS           wait between reports (default: 3)
  -p, --pid PID[,PID...]        restrict the task list (may be repeated)
  -u, --filter-only-euser USER  restrict to an effective user
  -U, --filter-any-user USER    match any saved/real/effective user ID
  -o, --sort-override FIELD     sort by PID, %CPU, %MEM, TIME+, VIRT, RES, ...
  -O, --list-fields             list sortable field names and exit
  -c, --cmdline-toggle          show full command lines
  -H, --threads-show            show individual threads
  -i, --idle-toggle             hide idle tasks after the first refresh
  -S, --accum-time-toggle       include waited-for child CPU time
  -E, --scale-summary-mem UNIT  summary unit: k, m, g, t, p, or e
  -e, --scale-task-mem UNIT     task-memory unit: k, m, g, t, or p
  -w, --width [COLUMNS]         limit report width (at most 512 columns)
  -1, --single-cpu-toggle       print one CPU summary per core
  -s, --secure-mode             accepted; ba6's display is always secure
  -A, --apply-defaults          accepted alone; ba6 has no saved top config
  -V, --version                 print the ba6 top identity

On a terminal, q quits; SPACE refreshes; c, i, S, and 1 toggle their matching
views; P, M, N, and T choose CPU, memory, PID, or time ordering; R reverses it.
Redirected top emits one report unless -b or -n requests a longer run.`,
	"traceroute": `Usage: traceroute [-46n] [-m HOPS] [-q PROBES] [-w SECONDS] HOST
Trace an IPv4 or IPv6 route with increasing UDP hop limits and Linux's
unprivileged UDP error queue. -n disables reverse DNS.`,
	"udhcpc": `Usage: udhcpc [-i INTERFACE] [-t RETRIES] [-T SECONDS]
              [-x HOSTNAME] [--no-configure]
Obtain one IPv4 DHCP lease, configure the address and default route, update
/etc/resolv.conf, and exit. --no-configure performs the exchange without
changing interface or resolver configuration.`,
	"base64": `Usage: base64 [-d] [-w COLS] [FILE]
Encode or decode base64 data.`,
	"blkid": `Usage: blkid [OPTION]... [DEVICE]...
Print the filesystem signature, label and UUID of each device. With no device
the block devices the kernel knows about are probed.

Options:
  -s, --match-tag TAG      print only this tag; repeatable
  -t, --match-token NAME=VALUE
                           print only devices carrying this tag
  -L, --label LABEL        print the name of the device with this label
  -U, --uuid UUID          print the name of the device with this UUID
  -o, --output FORMAT      full, value, device or export
  -p, --probe              low-level probe; adds the version, geometry and
                           usage tags the cache never stores
  -i, --info               print the I/O limits instead, in export format
  -l, --list-one           stop after the first matching device
  -k, --list-filesystems   list the filesystems this build recognises
  -d, -g, -c, --bytes      accepted and ignored; there is no cache here
  --help                   show this help

Exit status is 0 when something was found, 2 when nothing was, and 4 when an
argument could not be made sense of.`,
	"cmp": `Usage: cmp [-s] FILE1 FILE2
Report the offset and line number where two files first differ.

Options:
  -s        report nothing; signal the result through the exit status
  --help    show this help`,
	"completion": `Usage: completion bash
Generate a Bash completion script for ba6 on standard output. The script
completes global options, applets, documented applet options, and paths.`,
	"curl": `Usage: curl [OPTION]... URL...
Transfer HTTP or HTTPS resources to standard output. Redirects are followed only
with -L.

Options:
  -o FILE   write to FILE
  -O        write to a file named after the remote path
  -s        no progress or error chatter
  -v        trace the connection, request and response on standard error
  -i        include the response headers in the output
  -I        fetch headers only
  -L        follow redirects
  -f        no output on an HTTP error, and exit 22
  -k        skip TLS certificate verification
  -A AGENT  set User-Agent
  -u USER[:PASS]  HTTP basic credentials
  -X METHOD use METHOD instead of GET
  -d DATA   send DATA as the request body, implying POST
  -H "NAME: VALUE"  add a request header
  --max-time SEC    time limit for the transfer
  --help    show this help

Short options may be clustered, as in -sSL. Unlike wget, an HTTP error response
is not by itself a failure; use -f for that.`,
	"dig": `Usage: dig [@SERVER] [TYPE] NAME [OPTIONS]
Query DNS over UDP with automatic TCP retry for truncated replies. Supported
types are A, AAAA, CNAME, MX, NS, PTR, SOA, SRV, TXT, CAA, DS, DNSKEY, SVCB,
HTTPS, and ANY. Compressed names and response bounds are validated before
records are displayed.

Options:
  -b ADDRESS     outbound local source address binding
  -c CLASS       lookup protocol class (default IN)
  -f FILE        batch mode reading lookup targets from file
  -k KEYFILE     TSIG cryptographic authentication key file
  -m             turn on memory debugging diagnostics
  -p PORT        remote DNS server destination port number
  -q NAME        domain name to query
  -r             omit reading default user startup options
  -t TYPE        requested record resource type
  -u             report elapsed duration in microseconds
  -v             display software release identification and quit
  -x ADDR        perform reverse mapping address lookup
  -y KEY         specify transaction signature secret credentials`,
	"host": `Usage: host [-46adrsTUvw] [-c CLASS] [-p PORT] [-R RETRIES] [-t TYPE]
            [-W SECONDS] NAME [SERVER]
Look up NAME in the DNS and describe each answer in a sentence. Without -t it
asks in turn for addresses (A then AAAA), mail exchangers, and HTTPS service
bindings; an IPv4 or IPv6 address operand is converted to its reverse name and
asked for a pointer record. SERVER replaces the resolvers listed in
/etc/resolv.conf.

  -t TYPE     ask for one record type, and report when there is none
  -v, -d      print the whole response in master-file layout
  -a          the same as -v -t ANY
  -T, -U      query over TCP or force UDP; type ANY starts on TCP
  -4, -6      restrict the query transport to one address family
  -c CLASS    query class IN, CH, or HS
  -p PORT     contact the server on PORT instead of 53
  -R RETRIES  attempts per server before moving to the next one
  -W SECONDS  reply timeout; -w waits effectively forever
  -r          clear the recursion-desired bit

NAME is queried exactly as written, so the resolv.conf search list and the
ndots rule do not apply. Zone transfers, the authoritative SOA comparison, and
memory debugging are not implemented; their options are rejected rather than
quietly ignored.`,
	"diff": `Usage: diff [-u] FILE1 FILE2
Show a line-oriented difference.`,
	"dmesg": `Usage: dmesg [OPTION]...
Inspect or control kernel ring buffer log records.

Options:
  -c, --read-clear          print messages then clear ring buffer
  -C, --clear               clear ring buffer without printing
  -r, --raw                 emit unparsed log entries with priority tags
  -t, --notime              omit timestamps from records
  -d, --show-delta          include time elapsed between consecutive records
  -e, --reltime             print relative time deltas and periodic local time
  -H, --human               human-friendly layout enabling reltime formatting
  -J, --json                render output records in JSON format
  -x, --decode              prefix facility and severity names
  -T, --ctime               reconstruct human-readable wall clock times
  -k, --kernel              restrict to kernel facility records
  -u, --userspace           restrict to non-kernel facility records
  -w, --follow              monitor log stream for new arrivals
  -W, --follow-new          monitor and show exclusively new incoming lines
  -F, --file FILE           read syslog-formatted records from FILE
  -K, --kmsg-file FILE      read /dev/kmsg formatted records from FILE
  -l, --level LIST          filter by comma-separated severity levels
  -f, --facility LIST       filter by comma-separated facilities
  -s, --buffer-size N       size of buffer allocated for syslog retrieval
  -n, --console-level N     adjust console log printing threshold
  -D, --console-off         turn off console logging
  -E, --console-on          turn on console logging
  --time-format FMT         choose format: delta, reltime, ctime, notime, iso, raw
  --since TIME              filter records occurring after TIME
  --until TIME              filter records occurring before TIME
  -L, --color[=WHEN]        accepted for compatibility
  -S, -P, -p, --noescape    accepted for compatibility`,
	"env": `Usage: env [-i] [-u NAME] [NAME=VALUE]... [COMMAND [ARG]...]
Display or modify the environment and optionally run a command.`,
	"expr": `Usage: expr EXPRESSION
Evaluate arithmetic, comparisons, and boolean expressions.`,
	"halt": `Usage: halt [-nf]
Ask PID 1 to halt the machine. -f uses the reboot syscall directly and requires
CAP_SYS_BOOT; -n skips the pre-syscall sync in forced mode.`,
	"hexdump": `Usage: hexdump [OPTION]... [FILE]...
Display the input in one of the fixed formats. With no option the bare
two-byte hex form is used, which is narrower than the explicit -x.

Options:
  -C        canonical hex+ASCII, sixteen bytes to a line
  -b        octal bytes           -c  characters with C escapes
  -d        unsigned decimal words        -o  octal words
  -x        hexadecimal words
  -n N      stop after N bytes    -s N  skip N bytes first
  -v        print every line rather than eliding a repeated one with "*"
  --help    show this help

The -e and -f format strings, which the other options are shorthand for, are
not implemented.`,
	"init": `Usage: init [-f INITTAB]
       init [--] COMMAND [ARG]...
Run the system initializer when invoked as PID 1. The default inittab path is
/etc/inittab. Outside PID 1, supervise COMMAND and return its exit status.
After sysinit entries finish, set the kernel hostname from /etc/hostname via
/proc/sys/kernel/hostname; failures are reported without stopping boot.

Inittab uses id:runlevels:action:process fields. Supported actions are sysinit,
wait, once, respawn, askfirst, shutdown, ctrlaltdel, powerfail, powerwait, and
powerokwait. PID 1 never returns; SIGHUP reloads inittab, SIGINT runs ctrlaltdel
and reboots, SIGUSR1 halts, SIGUSR2 powers off, SIGTERM reboots, and SIGPWR runs
power-failure actions before powering off.`,
	"mknod": `Usage: mknod [-m MODE] NAME TYPE [MAJOR MINOR]
Create a FIFO, block device, or character device.`,
	"mount": `Usage: mount [OPTION]... [DEVICE] [DIRECTORY]
       mount [OPTION]... -a
With no operands, list the mounted filesystems as "SOURCE on TARGET type TYPE
(options)". With one, the device or mount point is looked up in fstab and its
entry supplies the rest.

Options:
  -t, --types=TYPE       filesystem type, or a comma-separated list to filter
                         the listing and -a; "no" in front negates the list
  -o, --options=LIST     mount options, accumulating across several -o
  -r, --read-only        mount read-only        -w, --rw  mount read-write
  -a, --all              mount every fstab entry that is not mounted yet,
                         skipping the ones marked noauto
  -O, --test-opts=LIST   with -a, only the entries carrying these options
  -T, --fstab=FILE       read FILE instead of /etc/fstab
  -L, --label=LABEL      name the device by its label
  -U, --uuid=UUID        name the device by its uuid
  --source=DEV, --target=DIR
                         name either half explicitly
  -B, --bind             bind an existing tree elsewhere
  -R, --rbind            as --bind, with everything mounted underneath
  -M, --move             move a mount to another place
  --make-shared, --make-private, --make-slave, --make-unbindable
  --make-rshared, --make-rprivate, --make-rslave, --make-runbindable
                         change a mount's propagation
  -f, --fake             go through the motions without mounting
  -v, --verbose          say what is being done
  -n, --no-mtab          accepted; the kernel table is the only one there is
  -l, --show-labels      accepted
  --help                 show this help

A read-only bind mount is made in two steps, as the original makes it: the bind
first, then the remount that applies the flag. Exit status is 32 when a mount
fails, and 1 for a command line the tool could not use.`,
	"less": `Usage: less [-eEFiImMnNqQrRsSXz] [-p PATTERN] [-x TABS] [-z LINES]
            [+COMMAND] [FILE]...
Page through files on a full screen. Each file is read into memory, so both
directions scroll and every position is exact. When the output is not a
terminal the input is copied through unchanged, which keeps the applet usable
in a pipeline. Keys come from /dev/tty when the text arrives on standard input.

  -N          number the lines; -n turns numbering off again
  -S          cut long lines instead of wrapping them
  -i, -I      case-insensitive search, -I even for mixed-case patterns
  -F          print and exit when the file fits on one screen
  -e, -E      stop once the last line has been shown
  -X          stay on the main screen instead of the alternate one
  -s          fold repeated blank lines into one
  -R          pass escape sequences through instead of showing them
  -m, -M      a position or a full position report on the status line
  -p PATTERN  start at the first line matching PATTERN
  -x TABS     tab stop width, 8 by default
  -z LINES    scroll this many lines per screen command
  +COMMAND    run one command at startup, such as +G or +/pattern

SPACE, b, ENTER, y, d, u, g, G, and N% move; / and ? search, n and N repeat;
= reports the position, h shows the key summary, :n and :p change file, and q
quits. A count typed before a command repeats it. Nothing in this pager runs
another program, so the editor, shell, and pipe commands are absent, and it
does not follow a growing file.`,
	"nano": `Usage: nano [OPTIONS] [+LINE[,COLUMN]] [FILE]
Small full-screen terminal editor. Shortcut keys: ^O writes out, ^X exits,
^F searches, ^\ replaces, ^K cuts, ^U pastes, ^_ jumps to line, ^C shows position.

Options:
  -v, --view              open in view (read-only) mode
  -l, --linenumbers       display line counts on the left margin
  -T, --tabsize=NUM       width of one tab stop in columns (default: 8)
  -E, --tabstospaces      transform entered tabs to spaces
  -i, --autoindent        indent newly created lines matching the current line
  -k, --cutfromcursor     snip text from cursor to end of current line
  -c, --constantshow      continuously show line/col position on screen
  -t, --saveonexit        write changes automatically upon exit without prompt
  -x, --nohelp            do not display bottom help shortcut lines
  -B, --backup            preserve a backup copy before writing
  -C, --backupdir=DIR     directory where saved backup files are stored
  -z, --listsyntaxes      display recognized highlight syntax rules and quit
  -R, --restricted        prevent editing or writing to external files
  -A, --smarthome         home key jumps to first non-space character
  -D, --boldtext          use bold styling for interface banners
  -F, --newbuffer         load additional files in distinct editing buffers
  -G, --locking           use file locks when opening buffers
  -H, --historylog        record search and replacement history strings
  -I, --ignorercfiles     skip reading external configuration rc files
  -J, --guidestripe=NUM   draw a vertical guide bar at the specified column
  -K, --rawsequences      interpret escape keystrokes directly
  -L, --nonewlines        do not add an automatic trailing line feed
  -M, --trimblanks        trim whitespace at the tail of wrapped lines
  -N, --noconvert         do not convert files between DOS and Unix formats
  -O, --bookstyle         treat leading spaces as part of paragraph blocks
  -P, --positionlog       remember previous cursor positions across sessions
  -Q, --quotestr=REGEX    pattern identifying email-style quoted paragraphs
  -S, --softwrap          render excessively long lines visually wrapped
  -U, --quickblank        clear status messages after single keystrokes
  -W, --wordbounds        use punctuation characters as word boundaries
  -X, --wordchars=STR     custom set of characters considered word parts
  -Y, --syntax=STR        name of syntax definition to apply
  -Z, --zap               let backspace or delete wipe selected selections
  -a, --atblanks          soft-wrap lines strictly at blank spaces
  -b, --breaklonglines    hard-wrap extended lines automatically
  -d, --rebinddelete      treat backspace as delete keycode
  -e, --emptyline         leave empty row directly below the header banner
  -f, --rcfile=FILE       load specific configuration rc file
  -g, --showcursor        keep hardware terminal cursor visible in browser
  -j, --jumpyscrolling    scroll viewport by chunks instead of single rows
  -m, --mouse             enable terminal mouse event tracking
  -n, --noread            treat file argument as new without disk reading
  -o, --operatingdir=DIR  restrict file operations within designated path
  -p, --preserve          preserve exact XON and XOFF signal bindings
  -q, --indicator         show visual scrollbar indicator along the edge
  -r, --fill=COL          wrap lines at designated column width
  -s, --speller=PROG      invoke external spell checking command
  -u, --unix              save files with Unix newline endings by default
  -w, --nowrap            disable automatic hard line wrapping
  -y, --afterends         move cursor past word endings on jumps
      --zero              hide title banner and status bars completely
      --solosidescroll    scroll horizontal views on individual lines`,
	"nc": `Usage: nc [-u] [-w SECONDS] HOST PORT
       nc -l [-u] [-p PORT] [PORT]
Copy data over a TCP or UDP connection.`,
	"nslookup": `Usage: nslookup [OPTION]... NAME [SERVER]
       nslookup NAME [type=TYPE] [port=PORT] [SERVER]
Query internet name servers for domain or IP address records.

Options:
  -t, -type=TYPE       specify record type to query (A, AAAA, MX, NS, TXT, PTR)
  -p, -port=PORT       destination port number on the nameserver (default: 53)
  -o, -timeout=SECS    initial response waiting interval in seconds
  -i                   enable interactive query mode
  -n                   disable recursive resolution requests`,
	"od": `Usage: od [OPTION]... [FILE]...
Write an unambiguous representation of the input, two-byte octal words by
default. Several files are read as one stream, and "-" is standard input.

Options:
  -t, --format=TYPE      choose the output format; several accumulate and are
                         printed one under the other
  -A, --address-radix=R  address column radix: d, o, x, or n for none
  -j, --skip-bytes=N     skip N bytes first
  -N, --read-bytes=N     stop after N bytes
  -w[N], --width[=N]     N bytes per line (32 when N is left out); N must be
                         attached to -w, as it is in the original
  -v, --output-duplicates
                         print every line rather than eliding a repeated one
                         with "*"
  --endian=big|little    read each unit in that byte order
  --help                 show this help

TYPE is a letter, an optional size and an optional "z":
  a          named characters, with the high bit ignored
  c          printable characters or backslash escapes
  d[SIZE]    signed decimal        o[SIZE]  octal
  u[SIZE]    unsigned decimal      x[SIZE]  hexadecimal
  f[SIZE]    floating point, printed as the shortest decimal that reads back
  SIZE is a number, or C, S, I or L for char, short, int and long; for f it may
  be F, D or L. A trailing z adds the printable-character column.

Traditional accumulating forms: -a (a), -b (o1), -c (c), -d (u2), -f (fF),
-i (dI), -l (dL), -o (o2), -s (d2), -x (x2).`,
	"pgrep": `Usage: pgrep [OPTION]... [PATTERN]
Print the process ids whose name matches an extended regular expression. The
pattern may be left out when some other selection option is given. A name in
/proc is truncated to 15 characters, so a longer pattern only matches under -f.
Exit status is 0 when something matched, 1 when nothing did and 2 for a bad
command line.

Options:
  -f, --full             match against the whole command line, not the name
  -x, --exact            the pattern must match the whole name
  -i, --ignore-case      match case insensitively
  -v, --inverse          select the processes that do not match
  -c, --count            print how many matched instead of the matches
  -n, --newest           keep only the most recently started match
  -o, --oldest           keep only the earliest started match
  -O, --older SECONDS    keep matches at least SECONDS old
  -p, --pid PID,...      match these process ids
  -P, --parent PPID,...  match children of these processes
  -g, --pgroup PGID,...  match these process groups (0 means our own)
  -s, --session SID,...  match these sessions (0 means our own)
  -u, --euid ID,...      match by effective user, by name or number
  -U, --uid ID,...       match by real user
  -G, --group ID,...     match by real group
  -t, --terminal TTY,... match by controlling terminal, as pts/0 or tty1
  -r, --runstates STATES match these process states, as in D, S or Z
  -A, --ignore-ancestors drop our own ancestors from the result
  -F, --pidfile FILE     take the process id to match from FILE
  --ns, --nslist, --cgroup and --env are not implemented.
  -l, --list-name        print the process name beside each id
  -a, --list-full        print the whole command line beside each id
  -w, --lightweight      print every thread id, not just the process id
  -d, --delimiter STR    separate ids with STR instead of a newline
  --quiet                print nothing; report the result in the exit status`,
	"pidof": `Usage: pidof [-s] [-x] [-q] [-o PID,...] [-S SEP] NAME...
Print process IDs for program names, newest first.

Options:
  -s        single shot: return the newest PID only
  -x        also find shells running the named scripts
  -q        quiet mode: only set the exit code
  -o PIDs   omit the given PIDs from the result
  -S SEP    use SEP as separator between PIDs`,
	"ping": `Usage: ping [-46] [-c COUNT] [-W SECONDS] [-i SECONDS] HOST
Send IPv4 or IPv6 ICMP echo requests. The address family is selected from the
resolved address unless -4 or -6 is specified.`,
	"pkill": `Usage: pkill [-SIGNAL] [OPTION]... [PATTERN]
Signal the processes whose name matches an extended regular expression. The
selection options are pgrep's; SIGTERM is sent unless -SIGNAL or --signal names
another. The pattern may be left out when some other selection option is given.

Options:
  -SIGNAL, --signal SIG  send this signal instead of SIGTERM
  -e, --echo             print each process as it is signalled
  -H, --require-handler  signal only processes that handle the signal
  -f, --full             match against the whole command line, not the name
  -x, --exact            the pattern must match the whole name
  -i, --ignore-case      match case insensitively
  -v, --inverse          select the processes that do not match
  -c, --count            print how many matched instead of the matches
  -n, --newest           keep only the most recently started match
  -o, --oldest           keep only the earliest started match
  -O, --older SECONDS    keep matches at least SECONDS old
  -p, --pid PID,...      match these process ids
  -P, --parent PPID,...  match children of these processes
  -g, --pgroup PGID,...  match these process groups (0 means our own)
  -s, --session SID,...  match these sessions (0 means our own)
  -u, --euid ID,...      match by effective user, by name or number
  -U, --uid ID,...       match by real user
  -G, --group ID,...     match by real group
  -t, --terminal TTY,... match by controlling terminal, as pts/0 or tty1
  -r, --runstates STATES match these process states, as in D, S or Z
  -A, --ignore-ancestors drop our own ancestors from the result
  -F, --pidfile FILE     take the process id to match from FILE
  --ns, --nslist, --cgroup and --env are not implemented.
  -c, --count            print how many were signalled
  --quiet                print nothing; report the result in the exit status
  -q/--queue and -m/--mrelease are not implemented.`,
	"poweroff": `Usage: poweroff [-nf]
Ask PID 1 to power off. -f uses the reboot syscall directly and requires
CAP_SYS_BOOT; -n skips the pre-syscall sync in forced mode.`,
	"printenv": `Usage: printenv [OPTION]... [NAME]...
Print environment variables.

Options:
  -0        separate results with NUL bytes, not newlines
  --help    show this help`,
	"printf": `Usage: printf FORMAT [ARG]...
Format and print arguments with standard escape sequences.`,
	"reboot": `Usage: reboot [-nf]
Ask PID 1 to restart. -f uses the reboot syscall directly and requires
CAP_SYS_BOOT; -n skips the pre-syscall sync in forced mode.`,

	"renice": `Usage: renice [OPTION]... TARGET...
Change the niceness of running processes.

Options:
  -n N      the new niceness
  -p PID    change process PID (the default target)
  -g PGRP   change process group PGRP
  -u USER   change every process of USER
  --help    show this help`,
	"seq": `Usage: seq [-w] [-s STRING] [-f FORMAT] [FIRST [INCREMENT]] LAST
Print a numeric sequence. How many decimals each value carries is taken from the
operands: 0.10 asks for two. -w pads with leading zeros to an equal width.`,

	"setsid": `Usage: setsid [OPTION]... COMMAND [ARG]...
Run COMMAND in a new session.

Options:
  -c        make the current terminal the controlling one
  -f        fork first, then create the session
  -w        wait for the program to finish
  --help    show this help`,
	"split": `Usage: split [OPTION]... [FILE [PREFIX]]
Write pieces of the input into files named PREFIXaa, PREFIXab, ... (1000
lines per piece by default).

Options:
  -l N      put N lines per piece
  -b SIZE   put SIZE bytes per piece (K, M, G multiply by 1024; KB by 1000)
  -n CHUNKS make CHUNKS pieces; K/N selects one, l/N keeps lines whole, r/N
            distributes lines round-robin
  -a N      use suffixes of length N
  -d        number the pieces instead of lettering them
  -x        use hexadecimal numbering
  -e        leave out empty pieces
  --additional-suffix=SUFFIX  append SUFFIX to every piece name
  --verbose print a notice as each piece opens
  -t SEP    use SEP as the record separator
  --help    show this help`,
	"sh": `Usage: sh [OPTION]... [SCRIPT [ARGUMENT]...]
Execute POSIX command language scripts or interactive sessions.

  -c STRING       evaluate instructions from supplied string
  -s              read instruction stream via standard input
  -i              launch interactive prompt session
  -e              abort execution upon non-zero command termination
  -n              validate syntax without evaluating commands
  -u              treat unresolved variable queries as fatal
  -v              echo source lines prior to execution
  -x              print executed commands prefixed with trace marker
  -a              automatically export modified variables
  -b              immediate notification of background state changes
  -C              prevent redirection from clobbering existing destinations
  -f              deactivate filename path expansion
  -m              enable job control monitoring`,
	"ss": `Usage: ss [-atuxlnp46sHQ] [-f FAMILY]
Display socket statistics and connection tables from kernel netlink and /proc.
Short options may be bundled.

Options:
  -a, --all         include both listening and active endpoints
  -l, --listening   show listening endpoints only
  -t, --tcp         filter for TCP endpoints
  -u, --udp         filter for UDP endpoints
  -x, --unix        filter for local Unix domain sockets
  -4, --ipv4        limit listing to IPv4 sockets
  -6, --ipv6        limit listing to IPv6 sockets
  -s, --summary     show summary of socket statistics
  -H, --no-header   omit the header line
  -Q, --no-queues   omit send and receive queue columns
  -n, --numeric     do not resolve service or host names
  -p, --processes   show process name and PID owning socket
  -f, --family=FAM  select socket family (inet, inet6, unix)`,
	"netstat": `Usage: netstat [-tuwxlanp] [-r] [-i] [-g] [-s] [-M] [-o] [-c] [-A FAMILY]
Display sockets, routing table, interface counters, or multicast groups from /proc.
Short options may be bundled, so -tulpn is -t -u -l -p -n.

Options:
  -t, --tcp             list TCP sockets
  -u, --udp             list UDP sockets
  -w, --raw             list raw sockets
  -x, --unix            list UNIX domain sockets
  -l, --listening       list only listening sockets
  -a, --all             list listening and established sockets
  -n, --numeric         numeric output; don't resolve hosts, ports, or users
  -p, --program         show PID and process name owning each socket
  -r, --route           display kernel routing table (also -F)
  -C                    display kernel routing cache
  -i, --interfaces      display network interface statistics table
  -g, --groups          display multicast group memberships
  -s, --statistics      display protocol summary statistics
  -M, --masquerade      display masqueraded connections
  -o, --timers          display timer states for connections
  -c, --continuous      refresh display once per second
  -A, --protocol=FAM    specify address family (inet, inet6, unix, tcp, udp, raw)

-p can only name processes the caller owns, unless netstat runs as root.`,

	"nice": `Usage: nice [OPTION]... [COMMAND [ARG]...]
Run COMMAND at an adjusted niceness (default 10).

Options:
  -n N      set the niceness adjustment to N
  --help    show this help`,
	"ncdu": `Usage: ncdu [OPTION]... [DIRECTORY]
Scan a directory and browse its disk usage on a full-screen display. The
listing is ordered by size, with a bar drawn relative to the largest entry.
This browser is strictly read-only: unlike the original it can neither delete
files nor spawn a shell.

Keys:
  up/down, j/k        move the selection
  right/enter, l      open the selected directory
  left, h             go to the parent directory
  n / s               sort by name / by size (again reverses)
  C / M               sort by item count / by mtime (again reverses)
  a                   switch between disk usage and apparent size
  c / m               toggle item count / modification time display
  g                   toggle graph bar or cycle block styles
  t                   toggle grouping directories first
  e                   toggle hidden dotfiles
  ? / q               show the key list / quit

Options:
  -f FILE             load and browse a previously exported scan
  -o FILE             export scanned data into a JSON file
  -O FILE             export scanned data as compressed JSON
  -c, --compress      compress export output with gzip
  -e, --extended      record and display extended file attributes
  -x, --one-file-system stay on initial filesystem mount
  --exclude PATTERN   omit entries matching glob PATTERN
  -X, --exclude-from FILE read exclusion patterns from FILE
  -L, --follow-symlinks traverse symlinks to inspect targets
  --exclude-caches    omit directories marked with CACHEDIR.TAG
  --exclude-kernfs    omit Linux kernel pseudo-filesystems
  --apparent-size     report apparent byte size instead of disk allocation
  --disk-usage        report physical disk block allocation (default)
  --si                use base-1000 prefixes instead of base-1024
  --show-hidden       show hidden entries beginning with a dot
  --hide-hidden       hide hidden dotfiles from view
  --show-itemcount    display count of child items
  --show-mtime        display item modification timestamps
  --show-graph        draw graphical usage bars
  --hide-graph        do not display graphical usage bars
  --show-percent      display percentage share of parent size
  --graph-style STYLE set bar style: hash, half-block, or eighth-block
  --sort COLUMN       sort by name, disk-usage, apparent-size, itemcount, mtime
  --enable-natsort    sort names using natural numeric ordering
  --group-directories-first list directories prior to regular files
  -r, -q, --color COLOR accepted for compatibility
  --help              show this help`,
	"strings": `Usage: strings [OPTION]... [FILE]...
Print runs of printable characters at least LENGTH long. With no file, or when
a scan finds nothing, nothing is printed. Standard input is read when no file
is named.

Options:
  -a, --all                 scan the whole file (the default)
  -d, --data                scan only an ELF object's loaded, non-code sections
  -f, --print-file-name     prefix each run with the file it came from
  -n LENGTH, --bytes=LENGTH minimum run length (default 4); -LENGTH also works
  -t, --radix=o|d|x         print each run's file offset in that base
  -o                        alias for -t o
  -w, --include-all-whitespace
                            count newline, return, vertical tab and form feed
                            as printable, not just tab and space
  -e, --encoding=s|S|b|l|B|L
                            character width and byte order: s 7-bit (default),
                            S 8-bit, b/l 16-bit big/little, B/L 32-bit
  -s, --output-separator=STR
                            print STR after each run instead of a newline
  -T, --target=NAME         accepted and ignored; only the native format is read
  --help                    show this help`,
	"sync": `Usage: sync
Flush filesystem buffers.`,
	"umount": `Usage: umount [OPTION]... [TARGET]...
Unmount filesystems. A target may be a mount point or the device mounted there;
it is resolved against the kernel's mount table before the unmount is tried, so
a path carrying no mount is reported as "not mounted" rather than as an errno.

Options:
  -a, --all              unmount everything but the root, deepest first
  -A, --all-targets      unmount every mount point a device is mounted at
  -R, --recursive        unmount a target and everything beneath it
  -t, --types=LIST       with -a, only these filesystem types
  -O, --test-opts=LIST   with -a, only the mounts carrying these options
  -f, --force            force the unmount
  -l, --lazy             detach now and clean up when the last user leaves
  -r, --read-only        remount read-only when the unmount fails
  -q, --quiet            do not complain about a target that is not mounted
  --fake                 go through the motions without unmounting
  -v, --verbose          say what is being done
  -n, -c, -d, -i         accepted; there is no mtab and no helper to call
  --help                 show this help

Exit status is 32 when an unmount fails, and 1 for a target the tool could not
use.`,
	"uptime": `Usage: uptime [-p] [-s] [-r] [-c]
Display system uptime and load averages.

Options:
  -p        pretty format (weeks, days, hours, minutes)
  -s        system up since, as yyyy-mm-dd HH:MM:SS
  -r        raw format: boot time, uptime, users, load averages
  -c        show container uptime (boot time minus pid 1's start)`,
	"wget": `Usage: wget [OPTION]... URL...
Download HTTP or HTTPS resources. Redirects are followed and each URL is saved
under a name taken from its path; an existing name gains a .1, .2 suffix.

Options:
  -O FILE   write to FILE instead ("-" for standard output)
  -P DIR    place downloads under DIR
  -c        resume a partial download with a range request
  -nc       leave an existing file alone instead of downloading again
  -q        say nothing; -nv keeps only the closing summary
  -S        print the response headers
  -T SEC    time limit for the transfer
  -t N      attempts before giving up (0 means keep trying)
  -U AGENT  set User-Agent
  --user/--password USER,PASS   HTTP basic credentials
  --header "NAME: VALUE"        add a request header
  --method VERB                 use VERB instead of GET
  --post-data STR/--post-file F send a request body
  --spider  check the resource without downloading it
  --no-check-certificate        skip TLS certificate verification
  --help    show this help

Exit status is 8 when the server answers with an error response.`,
	"watch": `Usage: watch [-n SECONDS] [-t] COMMAND [ARG]...
Run a command repeatedly. -n sets the interval (at least 0.1 seconds) and -t
suppresses the title line.`,
	"xz": `Usage: xz [-cdkfq] [FILE]...
Write an XZ stream of stored LZMA2 chunks, or decode one with -d. Decoding
handles LZMA2-compressed streams from any encoder, every integrity check the
format defines, multiple blocks, and concatenated streams; encoding stores
rather than compresses. -c uses standard output, -k keeps inputs, and -f
replaces an existing output.`,
	"zip": `Usage: zip [OPTION]... ARCHIVE FILE... [-x PATTERN]...
Create a ZIP archive.

Options:
  -r        descend into directories
  -j        store each file under its own name alone
  -0        store rather than deflate
  -1 .. -9  deflate; every level maps onto this writer's single one
  -q        say nothing about each member
  -x PAT    leave out the files matching PAT
  --help    show this help

Each member is written in whichever of the two forms is smaller, and one whose
name ends in .Z, .zip, .zoo, .arc, .lzh or .arj is stored without trying, which
is what the original does with them.`,
	"zstd": `Usage: zstd [OPTIONS] [FILE]...
Compress or decompress Zstandard format archives.

Options:
  -c, --stdout              Send output to standard stream
  -d, --decompress          Unpack compressed archive inputs
  -f, --force               Overwrite destination paths without prompting
  -h                        Display short usage summary
  -H, --help                Print comprehensive flag reference
  -k, --keep                Retain source input files intact
  -l, --list                Present metadata from archive headers
  -m, --manual              Display extended instructions
  -o FILE                   Store generated output into specified target path
  -q, --quiet               Suppress regular progress or notice messages
  -t, --test                Examine integrity of compressed records
  -v, --verbose             Print additional diagnostics during run
  -V, --version             Display application release identifier
      --auto-threads        Determine execution concurrency automatically
      --adapt               Dynamically adjust compression parameters
      --exclude-compressed  Skip already packed input items
  -D DICT                   Provide pre-trained compression dictionary
      --long                Enable extended sliding match window
      --no-async            Disallow asynchronous background execution
      --patch-from FILE     Provide baseline file for delta operations
      --single-thread       Restrict execution to one operating thread`,
	"which": `Usage: which [-a] COMMAND...
Print executable paths found through PATH.`,
	"xargs": `Usage: xargs [-0r] [-n NUMBER] [-L NUMBER] [-I REPLACE] [COMMAND [ARG]...]
Build and execute commands from standard input. Items are separated by blanks
and newlines; quotes and backslashes group them, and nothing is expanded. With
-I, each input line is substituted whole into one command. With -L, at most
NUMBER input lines feed one command (a trailing blank continues a line). A
value may be attached to its option (-n1) or given separately (-n 1).`,
	"df": `Usage: df [OPTION]... [FILE]...
Show filesystem space usage for FILEs, or all mounted filesystems.

Options:
  -a, --all              include pseudo, duplicate and unmeasurable mounts
  -B, --block-size=SIZE  scale sizes by SIZE (a bare unit, as -BM, is echoed
                         after each value)
  -h, --human-readable   human-readable sizes, in powers of 1024
  -H, --si               human-readable sizes, in powers of 1000
  -i, --inodes           report inode counts instead of blocks
  -k                     like --block-size=1K (the default)
  -l, --local            list local filesystems only
  -P, --portability      the POSIX layout
  -t, --type=TYPE        list only filesystems of this type
  -T, --print-type       add the filesystem type column
  -x, --exclude-type=TYPE
                         skip filesystems of this type
  --output[=FIELDS]      choose the columns: source, fstype, itotal, iused,
                         iavail, ipcent, size, used, avail, pcent, file, target
  --total                add a grand-total row
  --sync, --no-sync, -v  accepted for compatibility
  --help                 show this help`,
	"du": `Usage: du [OPTION]... [FILE]...
Estimate allocated disk usage recursively.

Options:
  -a        print sizes for files as well as directories
  -s        report one total per operand
  -c        add a grand total line
  -d N      only report entries N levels deep or less
  -S        report a directory without its subdirectories
  -x        stay on the filesystem the operand lives on
  -L        measure what a symlink points at
  -D        do that for operands only
  -h        human-readable sizes
  -k        display 1K blocks (default)
  -m        display 1M blocks
  -b        apparent bytes rather than allocated blocks
  -B SIZE   display SIZE-byte blocks; a bare unit (K, MB) is echoed back
  -t SIZE   skip entries below SIZE; a negative SIZE skips those above it
  -0        end each line with a NUL byte
  --apparent-size  count the bytes a file claims, not what it occupies
  --inodes  count inodes instead of space
  --exclude=PATTERN
            skip names matching PATTERN
  --help    show this help`,
	"find": `Usage: find [-HLP] [PATH]... [EXPRESSION]
Walk each PATH and evaluate EXPRESSION without executing external commands.

Global options:
  -H/-L/-P                follow symlinks never, always, or only for a PATH
  -follow                 the same as -L
  -mindepth/-maxdepth N   control traversal depth
  -depth                  visit a directory after what it holds
  -xdev                   stay on one filesystem

Predicates:
  -name/-iname PATTERN    match a basename
  -path/-ipath PATTERN    match the complete path
  -lname/-ilname PATTERN  match a symlink's target
  -regex/-iregex REGEX    match the complete path against REGEX
  -type [fdlbcps]         match a file type
  -empty                  match empty files or directories
  -size N[c|w|b|k|M|G]    match size; +N/-N mean greater/less
  -perm [-|/]MODE         match a mode exactly, with all, or with any of MODE
  -links/-inum N          match a link count or an inode number
  -samefile FILE          match what FILE itself is
  -fstype TYPE            match the filesystem the file lives on
  -user/-group NAME       match an owner; -uid/-gid take numbers
  -nouser/-nogroup        match an id no account or group claims
  -readable/-writable/-executable
                          match what this user may do with the file
  -mtime/-atime/-ctime N  match age in days; -mmin/-amin/-cmin use minutes
  -newer/-anewer/-cnewer FILE
                          match files modified after FILE's stamp
  !, -a, -o, ( )          boolean operators

Actions:
  -print/-print0          print matching paths
  -printf FORMAT          print FORMAT, expanding %p %f %h %P %s %m %M %n %i
                          %u %g %U %G %y %d %l %b %k %t and %T/%A/%C plus a
                          strftime letter
  -ls                     print a long listing
  -delete                 remove what matched, deepest entry first
  -prune                  do not descend into the directory just matched
  -quit                   stop the walk at once
  --help                  show this help`,
	"free": `Usage: free [OPTION]...
Display physical and swap memory usage from /proc/meminfo.

Options:
  -b, --bytes            show output in bytes
      --kilo/--mega/--giga/--tera/--peta
                         show output in powers of 1000
  -k, --kibi             show output in kibibytes (the default)
  -m, --mebi             show output in mebibytes
  -g, --gibi             show output in gibibytes
      --tebi/--pebi      show output in tebibytes or pebibytes
  -h, --human            human-readable sizes, scaled per value
      --si               use powers of 1000, not 1024
  -l, --lohi             add the low and high memory rows
  -L, --line             print everything on one line
  -t, --total            add a row totalling RAM and swap
  -v, --committed        add the commit limit and committed memory
  -w, --wide             split buff/cache into separate columns
  -s N, --seconds N      repeat every N seconds
  -c N, --count N        repeat N times, then exit
  --help                 show this help

Sizes are printed in the C locale, so a fraction reads 3.1Gi where the original
follows LC_NUMERIC.`,
	"gunzip": `Usage: gunzip [OPTION]... [FILE]...
Decompress gzip streams. With no FILE, read stdin and write stdout.
Decompressed output is limited to 64 GiB per input stream.

Options:
  -c, --stdout           write to standard output and keep the input
  -d, --decompress       decompress
  -f, --force            replace an existing output file
  -k, --keep             keep the input file
  -l, --list             list what a member holds instead of unpacking it
  -n, --no-name          store or restore neither the name nor the timestamp
  -N, --name             store or restore both
  -q, --quiet            suppress the warnings
  -r, --recursive        descend into directories
  -S, --suffix=SUF       use SUF instead of .gz
  -t, --test             check integrity without writing anything
  -v, --verbose          report each file and its ratio
  --help                 show this help`,
	"gzip": `Usage: gzip [OPTION]... [FILE]...
Compress or decompress gzip streams. With no FILE, read stdin and write stdout.
Decompressed output is limited to 64 GiB per input stream.

Options:
  -1 .. -9, --fast, --best
                         compression level, from quickest to smallest
  -c, --stdout           write to standard output and keep the input
  -d, --decompress       decompress
  -f, --force            replace an existing output file
  -k, --keep             keep the input file
  -l, --list             list what a member holds instead of unpacking it
  -n, --no-name          store or restore neither the name nor the timestamp
  -N, --name             store or restore both
  -q, --quiet            suppress the warnings
  -r, --recursive        descend into directories
  -S, --suffix=SUF       use SUF instead of .gz
  -t, --test             check integrity without writing anything
  -v, --verbose          report each file and its ratio
  --help                 show this help

The compressed bytes come from Go's deflate encoder, so a stream is a little
larger or smaller than the original tool's at the same level; both read each
other's output. The reported ratio measures the deflate stream alone, leaving
out the member's header and trailer, exactly as the original reports it.`,
	"hostname": `Usage: hostname [-a|-d|-f|-i|-s|-y] [NAME]
Display or set the system hostname.

Options:
  -a        aliases recorded for this machine
  -d        the domain part alone
  -f        the fully qualified name
  -i        every address this machine resolves to
  -s        the name up to its first dot
  -y        NIS/YP domain name
  -F FILE   set the host name from FILE (needs root)`,

	"hwclock": `Usage: hwclock [OPTION]...
Read and set the real-time clock.

Options:
  -r, --show     print the current RTC time (the default)
  --get          like --show
  -s, --hctosys  copy the RTC value into the system time
  -w, --systohc  store the system time into the RTC
  --set --date S store the given time into the RTC
  --utc          the RTC holds UTC (the default)
  --localtime    the RTC holds local time
  --help         show this help`,
	"iftop": `Usage: iftop [-t] [-i INTERFACE] [-s SECONDS] [OPTIONS]
Sample /proc/net/dev and report receive/transmit bandwidth rates per interface.

Options:
  -i INTERFACE      monitor only the specified network device
  -s SECONDS        sampling window duration in seconds
  -t                text mode display output
  -n                disable hostname address lookups
  -N                disable port number service translation
  -P                display port numbers
  -p                enable promiscuous packet capture mode
  -b                suppress progress bar graphs
  -B                display rate metrics in bytes rather than bits
  -l                display and order by local network traffic
  -m LIMIT          set scale maximum bandwidth bound
  -f FILTER         packet filtering expression rule
  -F NET/MASK       IPv4 subnet address filter
  -G NET6/MASK      IPv6 network address filter
  -c CONFIG         read settings from alternate config file
  -L LINES          maximum lines to display
  -o ORDER          sort criteria ordering`,
	"id": `Usage: id [OPTION]... [USER]
Display user and group identity information.

Options:
  -u        print only the user ID
  -g        print only the primary group ID
  -G        print all group IDs
  -n        print names instead of numbers with -u, -g, or -G
  --help    show this help`,
	"kill": `Usage: kill [OPTION]... PID...
Send a signal to each PID.

Options:
  -s SIGNAL select a signal by name or number (default TERM)
  -SIGNAL   shorthand for -s SIGNAL
  -l [SIGNAL]
            list signal names or translate one signal
  --help    show this help`,
	"ps": `Usage: ps [OPTION]... [BSD OPTIONS] [PID]...
Display processes by reading /proc. All processes are shown by default.

Options:
  -e/-A     show all processes (default)
  -f        full output
  -l        long output
  -j        jobs output
  -p LIST   restrict output to comma-separated PIDs
  -P LIST   restrict output by parent PID (--ppid)
  -u LIST   by effective user; -U by real user
  -g LIST   by session id, or by effective group name
  -G LIST   by real group
  -s LIST   by session id
  -t LIST   by terminal
  -C LIST   by command name
  -N        list everything the other selections left out
  -o LIST   columns, each optionally as NAME=HEADING
  -O LIST   sort keys (--sort), each optionally signed
  --sort LIST
            order the listing by those columns; a leading - reverses one
  --no-headers
            leave out the heading line
  -w        wide output; command lines are never truncated here
  --help    show this help

Selections are additive: a process is listed when any of them names it.

Columns: pid ppid pgid sid sess uid euid ruid gid egid rgid user ruser
group rgroup comm ucmd cmd args stat s f c pri opri ni cls addr sz vsz
rss %cpu %mem tty tname wchan nlwp thcount minflt majflt etime etimes
time cputime bsdtime start bsdstart stime start_time lstart

BSD options are written without a dash and may be bundled:
  a         processes that have a controlling terminal
  x         processes belonging to the current user
  u         user-oriented format, as in "ps aux"
  A         every process
  w         wide output; command lines are never truncated here`,
	"sed": `Usage: sed [OPTION]... SCRIPT [FILE]...
Apply a stream-editing language to input lines.

Options:
  -n, --quiet, --silent  suppress the default output
  -e SCRIPT, --expression=SCRIPT
                         add a script fragment
  -f FILE, --file=FILE   read a script from FILE
  -E, -r                 POSIX extended regular expressions (the default is BRE)
  -i[SUFFIX], --in-place[=SUFFIX]
                         edit each file in place, keeping a backup when a
                         suffix is given
  -s, --separate         treat the files separately rather than as one stream
  -z, --null-data        lines are separated by NUL, not newline
  -l N, --line-length=N  the width l wraps at (default 70; 0 never wraps)
  --posix, --sandbox, -u accepted for compatibility
  --help                 show this help

Commands: s/// with the g, p, i/I, w and occurrence-number flags, y///, the
line commands a i c d D p P g G h H x n N z F l = q Q r R w W, { } blocks,
:label with b t T, and -i's in-place editing. Addresses may be a line number,
$, /REGEX/, GNU's first~step form, or a two-address range, each negatable with
!. Regex backreferences in patterns are rejected because RE2 cannot implement
them, and a script error names the problem without the original's
"-e expression #N, char M:" prefix.`,
	"sha1sum": `Usage: sha1sum [OPTION]... [FILE]...
Compute or check SHA-1 digests. -c verifies checksum files; -b/-t select the
printed marker; --quiet and --status control verification output.`,
	"sha256sum": `Usage: sha256sum [OPTION]... [FILE]...
Compute or check SHA-256 digests. FILE '-' means standard input.

Options:
  -c        read checksums from FILEs and verify them
  --quiet   do not print successful verification lines
  --status  produce no verification output
  -b/-t     mark the file as binary (*) or text ( ) in the output
  --help    show this help`,
	"sha512sum": `Usage: sha512sum [OPTION]... [FILE]...
Compute or check SHA-512 digests. -c verifies checksum files; -b/-t select the
printed marker; --quiet and --status control verification output.`,
	"tar": `Usage: tar -c|-x|-t [OPTION]... [-f ARCHIVE] [MEMBER]...
Create, extract, or list tar archives. ARCHIVE '-' means stdin/stdout. With -x
or -t, naming members lists or extracts only those, a directory bringing its
contents with it. Extraction rejects escaping paths and is limited to 64 GiB of
regular data.

Options:
  -c, --create           create an archive
  -x, --extract, --get   extract an archive
  -t, --list             list archive members
  -f, --file=ARCHIVE     use ARCHIVE instead of stdin/stdout
  -C, --directory=DIR    read or extract relative to DIR
  -z, --gzip             filter the archive through gzip
  -j, --bzip2            through bzip2
  -J, --xz               through xz
  --zstd                 through zstandard
  -v, --verbose          list the members as they are processed; -tv prints the
                         long listing
  -k, --keep-old-files   keep existing files instead of replacing them
  --overwrite            replace them (the default)
  -O, --to-stdout        write the members' contents out instead of unpacking
  -P, --absolute-names   keep a leading "/" rather than stripping it
  -p, --preserve-permissions
                         restore each member's recorded mode
  --numeric-owner        store and show ids rather than names
  --strip-components=N   drop N leading path components while extracting
  --exclude=PATTERN      leave out the members matching PATTERN, where "*"
                         crosses a slash as it does in the original
  -T, --files-from=FILE  take the operand names from FILE, one per line
  --help                 show this help

The xz and zstandard writers store their data rather than compressing it, so an
archive written with -J or --zstd is a valid but uncompressed stream of that
format; both are read fully. Appending to an archive (-r, -u, -A, --delete) is
not implemented.`,
	"uname": `Usage: uname [OPTION]...
Display kernel and machine information.

Options:
  -a        print all fields
  -s        kernel name
  -n        network node hostname
  -r        kernel release
  -v        kernel version
  -m        machine architecture
  -o        operating system
  --help    show this help`,
	"whoami": `Usage: whoami
Print the effective user's name.

Options:
  --help    show this help`,
	"[": `Usage: [ EXPRESSION ]
Evaluate a conditional expression. See "test --help" for operators.`,
	"basename": `Usage: basename NAME [SUFFIX]
       basename -a [-s SUFFIX] NAME...
Print the final component of each NAME.

Options:
  -a        accept more than one NAME
  -s SUFFIX strip SUFFIX from the end of each NAME (turns on -a)
  -z        separate results with NUL bytes, not newlines
  --help    show this help`,
	"chgrp": `Usage: chgrp [OPTION]... GROUP FILE...
       chgrp [OPTION]... --reference=RFILE FILE...
Set the owning group of each FILE. GROUP may be a name or numeric ID.

Options:
  -R, --recursive        operate recursively
  -c, --changes          report only the files that actually change
  -f, --silent, --quiet  suppress the error messages
  -v, --verbose          report every file, changed or not
  -h, --no-dereference   act on a symbolic link itself, not on its target
  --dereference          act on what a symbolic link points to (the default)
  -H                     with -R, follow a symbolic link named on the command
                         line
  -L                     with -R, follow every symbolic link
  -P                     with -R, follow none of them (the default)
  --reference=RFILE      take the ids from RFILE instead of an operand
  --preserve-root        refuse to recurse on /
  --no-preserve-root     do not (the default)
  --help                 show this help`,
	"chmod": `Usage: chmod [OPTION]... MODE FILE...
       chmod [OPTION]... --reference=RFILE FILE...
Change file permissions. MODE is an octal number from 0000 through 7777, or a
symbolic list such as u+x,go=rX, where X only sets execute on a directory or on
a file that already has one.

Options:
  -R, --recursive        operate recursively
  -c, --changes          report only the files that actually change
  -f, --silent, --quiet  suppress the error messages
  -v, --verbose          report every file, changed or not
  --reference=RFILE      copy RFILE's mode instead of taking a MODE operand
  --preserve-root        refuse to recurse on /
  --no-preserve-root     do not (the default)
  --help                 show this help

A recursive run applies each directory's new mode after everything inside it,
so a mode that drops the search bit cannot lock the walk out part-way; the
original applies it on the way in and stops there. The reports still come out
in the original's order.`,
	"chown": `Usage: chown [OPTION]... OWNER[:GROUP] FILE...
       chown [OPTION]... --reference=RFILE FILE...
Set file ownership. OWNER and GROUP accept names or numeric IDs; an empty half
("user:" or ":group") leaves that id alone, and "user:" takes the user's own
login group.

Options:
  --from=OWNER[:GROUP]   change only the files that already carry these ids
  -R, --recursive        operate recursively
  -c, --changes          report only the files that actually change
  -f, --silent, --quiet  suppress the error messages
  -v, --verbose          report every file, changed or not
  -h, --no-dereference   act on a symbolic link itself, not on its target
  --dereference          act on what a symbolic link points to (the default)
  -H                     with -R, follow a symbolic link named on the command
                         line
  -L                     with -R, follow every symbolic link
  -P                     with -R, follow none of them (the default)
  --reference=RFILE      take the ids from RFILE instead of an operand
  --preserve-root        refuse to recurse on /
  --no-preserve-root     do not (the default)
  --help                 show this help`,
	"date": `Usage: date [OPTION]... [+FORMAT]
Display a time, or set the system clock with -s.

Options:
  -u        use UTC
  -r FILE   display FILE's modification time
  -d STRING show the time STRING names instead of now
  -s STRING set the clock to the time STRING names (needs privilege)
  -f FILE   show one time per line of FILE
  -R        write an RFC 5322 stamp
  -I[SPEC]  write an ISO 8601 stamp; SPEC is date, hours, minutes,
            seconds or ns
  --rfc-3339=SPEC
            write an RFC 3339 stamp; SPEC is date, seconds or ns
  --resolution
            print the clock's resolution
  --help    show this help

STRING may be a calendar date, a clock time, @SECONDS, a day word
(today, yesterday, tomorrow, a weekday name) or a relative amount such
as "+1 hour", "3 months ago" or "next monday", and these may be
combined.

FORMAT accepts common strftime directives including %F, %T, %Y, %m, %d,
%H, %M, %S, %s, %N, %j, %U, %W, %V, %G, %z, %:z, and %Z.`,
	"dirname": `Usage: dirname NAME...
Print the leading path of each NAME, dropping the final component.

Options:
  -z        separate results with NUL bytes, not newlines
  --help    show this help`,
	"false": `Usage: false
Return an unsuccessful status.

Options:
  --help    show this help`,
	"ln": `Usage: ln [OPTION]... TARGET [LINK_NAME]
       ln [OPTION]... TARGET... DIRECTORY
       ln [OPTION]... -t DIRECTORY TARGET...
Create hard links, or symbolic links with -s.

Options:
  -s, --symbolic         make symbolic links
  -f, --force            remove existing non-directory destinations
  -i, --interactive      prompt before removing a destination
  -r, --relative         write a symbolic link's target relative to its own
                         directory
  -n, --no-dereference   treat a destination symlink to a directory as a
                         link name
  -L, --logical          hard-link what a symbolic TARGET points at
  -P, --physical         hard-link a symbolic TARGET itself (the default)
  -d, -F, --directory    allow hard links to directories (root only, and
                         refused by most filesystems)
  -t, --target-directory=DIR
                         put every link in DIR
  -T, --no-target-directory
                         always treat the last operand as a link name
  -b                     back the destination up before replacing it
  --backup[=CONTROL]     as -b, choosing the naming scheme: none/off,
                         simple/never, existing/nil, numbered/t
  -S, --suffix=SUFFIX    the suffix a simple backup gets (default ~)
  -v, --verbose          print each link as it is created
  --help                 show this help

The environment variables VERSION_CONTROL and SIMPLE_BACKUP_SUFFIX select the
default backup scheme and suffix.`,
	"readlink": `Usage: readlink [OPTION]... FILE...
Print the value of a symbolic link, or canonicalize the path.

Options:
  -f        resolve fully; only the final name may be absent
  -e        resolve fully; every name has to be there
  -m        resolve fully; absent names are fine
  -n        leave off the trailing newline
  -z        terminate each line with NUL rather than a newline
  -s, -q    suppress error messages
  -v        report error messages`,
	"realpath": `Usage: realpath [OPTION]... FILE...
Print resolved absolute paths.

Options:
  -e        every name has to be there
  -m        absent names are fine
  -s        leave symlinks alone; fold only . and ..
  -L        fold .. before following any symlink
  -P        follow each symlink where it is met (the default)
  -z        terminate each line with NUL
  --relative-to=FILE    print paths relative to FILE
  --relative-base=DIR   print relative paths only when inside DIR`,
	"sleep": `Usage: sleep NUMBER[SUFFIX]...
Pause for the combined duration of all operands.

SUFFIX may be s for seconds (default), m for minutes, h for hours, or d for days.

Options:
  --help    show this help`,
	"stat": `Usage: stat [OPTION]... FILE...
Display file metadata.

Options:
  -L        follow symbolic links
  -c FORMAT use FORMAT instead of the default display
  --help    show this help

Common FORMAT sequences include %n, %N, %s, %a, %A, %u, %U, %g, %G,
%i, %h, %F, %x, %y, %z, %X, %Y, and %Z.`,
	"tee": `Usage: tee [OPTION]... [FILE]...
Copy standard input to standard output and each FILE.

Options:
  -a        append instead of overwriting
  -i        ignore interrupt signals
  -p        warn on write errors (default)
  --output-error[=MODE]
            MODE is warn, warn-nopipe, exit, or exit-nopipe
  --help    show this help`,
	"test": `Usage: test EXPRESSION
Evaluate EXPRESSION and return success when it is true.

Operators:
  -e/-f/-d/-L FILE       exists/regular/directory/symbolic link
  -r/-w/-x/-s FILE       readable/writable/executable/nonempty
  -n STRING, -z STRING   nonempty/empty string
  STRING = STRING        equal (also ==); !=, <, and > are supported
  INT -eq INT            numeric comparison (-ne, -lt, -le, -gt, -ge)
  FILE -nt/-ot/-ef FILE  newer/older/same file
  ! EXPR, EXPR -a EXPR, EXPR -o EXPR, ( EXPR )
  --help                  show this help`,
	"true": `Usage: true
Return a successful status.

Options:
  --help    show this help`,
	"cat": `Usage: cat [OPTION]... [FILE]...
Concatenate FILEs to standard output. FILE '-' means standard input.

Options:
  -n        number all output lines
  -b        number nonempty lines (overrides -n)
  -E        display '$' at newline boundaries
  -s        squeeze repeated empty lines
  -T        show tab characters as ^I
  -A        equivalent to -ET
  --help    show this help`,
	"echo": `Usage: echo [-neE] [ARG]...
Write ARGs separated by spaces.

Options:
  -n        suppress the newline that normally ends the output
  -e        interpret backslash escapes in ARGs
  -E        take backslash escapes literally (default)
  --help    show this help`,
	"grep": `Usage: grep [OPTION]... PATTERN [FILE]...
Print lines that match PATTERN.

Options:
  -i        ignore case
  -v        select nonmatching lines
  -n        print line numbers
  -c        print match counts
  -l        print names of matching files
  -h/-H     suppress/force filename prefixes
  -w/-x     match whole words/whole lines
  -F        match fixed strings
  -E        use POSIX extended regular expressions (the default is BRE)
  -r/-R     recurse through directories
  -q        stop after the first match
  -e PAT    add a pattern
  -f FILE   take the patterns from FILE, one to a line
  -m NUM    stop after NUM matches per file
  -L        print names of files with no match
  -o        print each match on its own line
  -A/-B/-C NUM
            print NUM lines of trailing/leading/surrounding context
  -NUM      the same as -C NUM
  -b        print each output line's byte offset
  -s        do not report unreadable files
  -a        treat a binary file as text
  -I        skip binary files
  -z        input and output records end with a NUL byte
  -Z        end a printed file name with a NUL byte
  -d ACTION what to do with a directory operand: read, skip or recurse
  -D ACTION the same for a device, FIFO or socket: read or skip
  -T        line the output up behind a tab
  --label=NAME
            report standard input under NAME
  --color[=WHEN]
            highlight matches when WHEN is always, never or auto
  --binary-files=TYPE
            binary, text or without-match
  --include/--exclude=GLOB
            select or skip files by name while recursing
  --exclude-from=FILE
            read those skip patterns from FILE
  --exclude-dir=GLOB
            skip directories by name while recursing
  --group-separator=SEP / --no-group-separator
            what to write between non-adjacent context groups
  --help    show this help

Patterns may use the GNU escapes \<, \>, \b, \B, \w, \W, \s and \S; RE2 has
one two-sided word boundary, so \< and \> both become it. Regex
backreferences are rejected because RE2 cannot implement them.`,
	"head": `Usage: head [OPTION]... [FILE]...
Print the beginning of each FILE.

Options:
  -n NUM    output NUM leading lines (default 10)
  -c NUM    output NUM leading bytes
  -q/-v     suppress/force headers
  --help    show this help`,
	"tail": `Usage: tail [OPTION]... [FILE]
Print the last part of FILE.

Options:
  -n NUM    print the last NUM lines; +NUM starts at line NUM
  -c NUM    print the last NUM bytes; +NUM starts at byte NUM
  -f        follow one file by descriptor
  -q/-v     suppress/force headers
  --help    show this help`,
	"ls": `Usage: ls [OPTION]... [FILE]...
List directory contents, or FILE itself. Output is packed into columns and
awkward names are quoted when writing to a terminal; through a pipe the entries
go one per line, written literally.

What to list:
  -a, --all              include hidden entries
  -A, --almost-all       include hidden entries but not . and ..
  -d, --directory        list a directory itself, not its contents
  -R, --recursive        descend into every subdirectory
  -B, --ignore-backups   skip the names ending in ~
  -I, --ignore=PATTERN   skip the names matching PATTERN
  --hide=PATTERN         as -I, but ignored when -a or -A is given
  -L, --dereference      report what a symbolic link points at
  -H                     do that only for the links named on the command line

Layout:
  -l                     long listing
  -1                     one entry per line
  -C                     columns, filled downwards
  -x                     columns, filled across
  -m                     comma-separated
  --format=WORD          across, commas, horizontal, long, single-column,
                         verbose or vertical
  -w, --width=N          assume N columns (0 means no limit)
  -T, --tabsize=N        assume tab stops every N columns (0 uses spaces)
  -g/-o/-G, --no-group   a long listing without the owner, the group, or both
  -n, --numeric-uid-gid  print the ids rather than looking up their names
  -i, --inode            print each entry's inode number
  -s, --size             print each entry's allocated size
  -h, --human-readable   scale sizes in powers of 1024
  --si                   scale sizes in powers of 1000
  -k, --block-size=SIZE  the unit sizes and blocks are counted in
  -Z, --context          print a security context column, which reads "?"
  -F/-p/--file-type/--indicator-style=WORD
                         append a character telling the entry's type apart
  -Q/-b/-N/--quoting-style=WORD
                         quote names with "", C escapes, literally, or in one
                         of the shell styles
  --full-time            like -l --time-style=full-iso
  --time-style=STYLE     full-iso, long-iso, iso, locale, or +FORMAT
  --time=WORD            show the access, status or modification time

Ordering:
  -r, --reverse          reverse whichever order is in force
  -t                     newest first
  -S                     largest first
  -X                     by extension
  -v                     by version, as filevercmp orders names
  -U                     unsorted, in directory order
  -f                     unsorted, and show everything
  -c/-u                  use the status or access time, and sort by it outside
                         the long listing
  --sort=WORD            none, size, time, version, extension or name
  --group-directories-first
                         list the directories before the other entries
  --color[=WHEN]         accepted; nothing is coloured
  --help                 show this help

Names are measured in the C locale, so a byte outside printable ASCII counts as
no columns at all, exactly as the original counts it under LC_ALL=C.`,
	"cp": `Usage: cp [OPTION]... SOURCE... DEST
       cp [OPTION]... -t DIRECTORY SOURCE...
Copy files, directories and symbolic links.

Options:
  -r, -R, --recursive    copy directories and their contents
  -a, --archive          same as -dR --preserve=all
  -d                     same as --no-dereference --preserve=links
  -L, --dereference      follow every symbolic link in SOURCE
  -P, --no-dereference   copy symbolic links as links (the default under -r)
  -H                     follow only the symbolic links named on the command
                         line
  -f, --force            remove a destination that cannot be opened
  -i, --interactive      prompt before overwriting
  -n, --no-clobber       do not overwrite an existing destination
  -u, --update           copy only when SOURCE is newer than the destination
  -l, --link             hard-link the files instead of copying them
  -s, --symbolic-link    make symbolic links instead of copying
  -p                     same as --preserve=mode,ownership,timestamps
  --preserve[=ATTRS]     preserve mode, timestamps, ownership, links (or all);
                         context and xattr are accepted and do nothing
  --no-preserve=ATTRS    stop preserving these
  -x, --one-file-system  do not cross into another filesystem
  --parents              rebuild each source's own path under DEST
  --remove-destination   unlink the destination before opening a replacement
  --attributes-only      create the destination without copying its contents
  --reflink[=WHEN]       ask the filesystem to share extents (auto, always,
                         never)
  --sparse=WHEN          accepted for compatibility; holes are neither
                         detected nor created
  --strip-trailing-slashes
                         drop any trailing slash from each source
  -t, --target-directory=DIR
                         copy everything into DIR
  -T, --no-target-directory
                         always treat the last operand as a file name
  -b                     back the destination up before replacing it
  --backup[=CONTROL]     as -b, choosing the naming scheme: none/off,
                         simple/never, existing/nil, numbered/t
  -S, --suffix=SUFFIX    the suffix a simple backup gets (default ~)
  -Z, -c, --context      accepted for compatibility; no SELinux labelling
  -v, --verbose          print each copy as it is made
  --help                 show this help

The environment variables VERSION_CONTROL and SIMPLE_BACKUP_SUFFIX select the
default backup scheme and suffix. Extended attributes are not copied, and a
directory the destination already contains is refused up front rather than
part-way through the walk.`,
	"mv": `Usage: mv [OPTION]... SOURCE... DEST
       mv [OPTION]... -t DIRECTORY SOURCE...
Move or rename files and directories. A rename that would cross a filesystem
falls back to a copy and a delete.

Options:
  -f, --force            replace destinations without asking
  -i, --interactive      prompt before overwriting
  -n, --no-clobber       do not overwrite existing destinations
  -u, --update           move only when the source is newer than the
                         destination, or the destination is missing
  -t, --target-directory=DIR
                         move everything into DIR
  -T, --no-target-directory
                         always treat the last operand as a file name
  --strip-trailing-slashes
                         drop any trailing slash from each source
  -b                     back the destination up before replacing it
  --backup[=CONTROL]     as -b, choosing the naming scheme: none/off,
                         simple/never, existing/nil, numbered/t
  -S, --suffix=SUFFIX    the suffix a simple backup gets (default ~)
  -Z, --context          accepted for compatibility; no SELinux labelling
  -v, --verbose          explain what is moved
  --help                 show this help

The environment variables VERSION_CONTROL and SIMPLE_BACKUP_SUFFIX select the
default backup scheme and suffix.`,
	"rm": `Usage: rm [OPTION]... FILE...
Remove files or directories.

Options:
  -r/-R     remove directories recursively
  -f        ignore missing operands and files
  -i        prompt before each top-level removal
  -d        remove empty directories
  -v        explain removals
  --no-preserve-root  allow recursive removal of '/'
  --help    show this help`,
	"mkdir": `Usage: mkdir [OPTION]... DIRECTORY...
Create directories.

Options:
  -p        create missing parents; ignore existing directories
  -m MODE   set the new final directory's octal mode
  -v        report each directory actually created
  --help    show this help`,
	"rmdir": `Usage: rmdir [OPTION]... DIRECTORY...
Remove empty directories.

Options:
  -p        remove empty parent directories too
  -v        report each directory actually removed
  --ignore-fail-on-non-empty
            ignore failures caused by nonempty directories
  --help    show this help`,
	"touch": `Usage: touch [OPTION]... FILE...
Create missing FILEs and update timestamps.

Options:
  -c        do not create files
  -a        change only access time
  -m        change only modification time
  -d STRING use the time STRING names instead of now
  -t STAMP  use [[CC]YY]MMDDhhmm[.ss]
  -r FILE   copy FILE's timestamps
  -h        act on a symlink itself, not on what it points to
  -f        accepted and ignored
  --time=WORD
            change the access time for atime/access/use, the
            modification time for mtime/modify
  --help    show this help

STRING takes the same forms date -d accepts.`,
	"pwd": `Usage: pwd [-LP]
Print the current working directory.

Options:
  -L        use the logical PWD value (default)
  -P        print the physical directory
  --help    show this help`,
	"wc": `Usage: wc [OPTION]... [FILE]...
Print newline, word, byte, and character counts.

Options:
  -l        print newline counts
  -w        print word counts
  -c        print byte counts
  -m        print character counts
  --help    show this help`,
	"sort": `Usage: sort [OPTION]... [FILE]...
Sort lines of text.

Options:
  -n        compare numeric prefixes
  -g        compare as floating point, exponents included
  -h        compare numbers with a size suffix
  -M        compare three-letter month names
  -r        reverse the result
  -u        emit one line per equal key
  -f        fold case
  -d        weigh only blanks and alphanumerics
  -i        drop unprintable bytes before comparing
  -b        ignore leading blanks
  -s        keep equal lines in input order
  -c        check ordering without producing output
  -C        check quietly, reporting only through the exit status
  -k KEYDEF sort on a part of the line: F[.C][OPTS][,F[.C][OPTS]],
            where OPTS are any of bdfgiMnr for that key alone
  -t SEP    split fields on SEP instead of at the start of a blank run
  -o FILE   write the result to FILE
  -z        lines end with a NUL byte
  --help    show this help`,
	"uniq": `Usage: uniq [OPTION]... [INPUT [OUTPUT]]
Collapse adjacent equal lines.

Options:
  -c        prefix lines with occurrence counts
  -d        print only duplicated lines
  -u        print only unique lines
  -i        ignore case when comparing
  -f N      skip N fields before comparing
  -s N      skip N characters before comparing
  -w N      limit the comparison to the first N characters
  -D        print every line of duplicated groups
  --group   separate groups with blank lines
  -z        lines are NUL-terminated`,
	"cksum": `Usage: cksum [FILE]...
Print the CRC-32/CKSUM checksum and byte count of each FILE, or of standard
input with no operands.

Options:
  --help    show this help`,

	"comm": `Usage: comm [OPTION]... FILE1 FILE2
Print the lines that only one of two sorted inputs has, and those both have.
Column 1 lists lines only in FILE1, column 2 only in FILE2, and column 3
common lines.

Options:
  -1        suppress column 1
  -2        suppress column 2
  -3        suppress column 3
  -z        use NUL instead of newline as the record terminator
  --total   print a counts summary line
  --help    show this help`,
	"expand": `Usage: expand [OPTION]... [FILE]...
Replace tab characters in each FILE with runs of spaces.

Options:
  -i        convert only initial tabs
  -t LIST   tab positions; a single number means tabs N apart (default 8), a
            comma-separated list gives explicit stops (last one may be /N for
            a repeating size or +N for an increment)
  --help    show this help`,
	"fold": `Usage: fold [OPTION]... [FILE]...
Wrap input lines to fit a maximum width.

Options:
  -b        count bytes instead of terminal columns
  -c        count terminal columns (default)
  -s        break at spaces instead of inside words
  -w N      width, in columns (default 80)
  --help    show this help`,
	"nl": `Usage: nl [OPTION]... [FILE]...
Number the lines of the input.

Options:
  -b STYLE  a (all), t (non-blank, default), n (none)
  -n FORMAT rn (right, default), rz (zero padded), ln (left)
  -s STR    separator after the number (default TAB)
  -w N      number field width (default 6)
  -v N      first line number (default 1)
  -i N      line number increment (default 1)
  -l N      count every Nth blank line as a line (default 1)
  --help    show this help`,

	"nohup": `Usage: nohup COMMAND [ARG]...
Run COMMAND immune to hangups: terminal input becomes /dev/null and terminal
output is appended to nohup.out (or $HOME/nohup.out).

Options:
  --help    show this help`,
	"tac": `Usage: tac [OPTION]... [FILE]...
Emit each FILE with its records reversed (last one first).

Options:
  -b        attach the separator before each record
  -r        take the separator as a regex
  -s STR    use STR as the record separator (default newline)
  --help    show this help`,
	"unexpand": `Usage: unexpand [OPTION]... [FILE]...
Turn runs of blanks in each FILE into tabs.

Options:
  -a        convert all blanks, not just leading ones
  --first-only
            touch only leading runs of blanks
  -t LIST   tab positions; a single number means tabs N apart (default 8), a
            comma-separated list gives explicit stops (last one may be /N for
            a repeating size or +N for an increment)
  --help    show this help`,
	"cut": `Usage: cut OPTION... [FILE]...
Select fields, characters or bytes from each line.

Options:
  -f LIST   select fields
  -c LIST   select character positions
  -b LIST   select byte positions
  -d CHAR   use CHAR as the field delimiter
  -s        suppress lines without delimiters
  -n        accepted and ignored
  -z        lines end with a NUL byte
  --complement
            keep what LIST does not select
  --output-delimiter=STRING
            write STRING between the pieces that are kept
  --help    show this help`,
	"tr": `Usage: tr [OPTION]... SET1 [SET2]
Translate, delete, or squeeze bytes from standard input.

Options:
  -d        delete bytes in SET1
  -s        squeeze repeated bytes in the last SET
  -c/-C     complement SET1
  --help    show this help`,
	"tree": `Usage: tree [OPTION]... [DIRECTORY]...
List directories as an indented tree and close with a count of what was found.

Options:
  -a        include entries whose name begins with a dot
  -d        list directories only
  -l        follow symbolic links as directories
  -f        print the full path of each entry
  -x        stay on the current filesystem
  -F        append /, *, @, =, or | to mark the file type
  -i        omit the indentation lines
  -u/-g     show file owner / group
  -s/-h     show sizes in bytes / in human-readable units
  -p        show permissions
  -D        show last modification timestamp
  -o FILE   write output to FILE instead of stdout
  -L LEVEL  descend at most LEVEL directories deep
  -P PATTERN keep only files matching PATTERN
  -I PATTERN skip entries matching PATTERN
  -t/-c/-v  sort by modification time / status change time / natural version
  -r/-U     reverse the order / do not sort
  --sort=TYPE  sort by name, version, size, mtime, or ctime
  --dirsfirst  list directories before files
  --prune      prune empty directories from the output
  --du         accumulate directory sizes
  --filelimit N  skip opening directories with more than N entries
  --inodes     show inode numbers
  --device     show device numbers
  --timefmt FMT format modification timestamp using strftime format
  --matchdirs  apply -P patterns to directories as well
  --noreport   omit the closing count
  -n/-C     accepted for compatibility; output is never colored
  --help    show this help`,
	"tty": `Usage: tty [-s]
Report stdin's terminal path, or print "not a tty". -s reports through the exit
status alone and prints nothing.`,
	"unxz": `Usage: unxz [-ckf] [FILE]...
Decode an XZ stream, including LZMA2-compressed data from any encoder, every
integrity check the format defines, multiple blocks, and concatenated streams.
-c uses standard output, -k keeps inputs, and -f replaces an existing output.`,
	"unzip": `Usage: unzip [OPTION]... ARCHIVE [MEMBER]... [-x PATTERN]...
List, test or extract a ZIP archive. A member pattern is a glob in which "*"
crosses a slash; extraction rejects paths and symbolic links that escape the
destination.

Options:
  -l        list the members as a table
  -v        list them with method, compressed size, ratio and CRC
  -t        read every member through and report the archive as sound
  -p        write the members to standard output, nothing else
  -c        as -p, but name each member first
  -d DIR    extract into DIR
  -j        drop the directories a member is stored under
  -o        overwrite existing files without asking
  -n        never overwrite an existing file
  -q        one -q drops the per-member lines, -qq the summary too
  -x PAT    skip the members matching PAT
  --help    show this help

Without -o an existing file is kept rather than prompting, since there is no
place to ask. Exit status is 9 when the archive cannot be opened and 11 when
no member matched.`,
	"unzstd": `Usage: unzstd [OPTIONS] [FILE]...
Decompress Zstandard format archives.

Options:
  -c, --stdout              Send output to standard stream
  -d, --decompress          Unpack compressed archive inputs
  -f, --force               Overwrite destination paths without prompting
  -h                        Display short usage summary
  -H, --help                Print comprehensive flag reference
  -k, --keep                Retain source input files intact
  -l, --list                Present metadata from archive headers
  -m, --manual              Display extended instructions
  -o FILE                   Store generated output into specified target path
  -q, --quiet               Suppress regular progress or notice messages
  -t, --test                Examine integrity of compressed records
  -v, --verbose             Print additional diagnostics during run
  -V, --version             Display application release identifier
      --auto-threads        Determine execution concurrency automatically
      --adapt               Dynamically adjust compression parameters
      --exclude-compressed  Skip already packed input items
  -D DICT                   Provide pre-trained compression dictionary
      --long                Enable extended sliding match window
      --no-async            Disallow asynchronous background execution
      --patch-from FILE     Provide baseline file for delta operations
      --single-thread       Restrict execution to one operating thread`,
	"useradd": `Usage: useradd [OPTIONS] USER
       useradd -D
Create a new user account or examine configuration defaults.

Options:
      --badname                 Permit non-standard account usernames
  -b, --base-dir DIR            Base prefix folder for personal home directory
      --btrfs-subvolume-home    Flag for subvolume based home storage
  -c, --comment COMMENT         Set user information field in passwd database
  -d, --home-dir HOME_DIR       Path to home directory for created account
  -D, --defaults                Display standard default account settings
  -e, --expiredate DATE         Account expiration deadline timestamp
  -f, --inactive DAYS           Days after credential expiration until lockout
  -F, --add-subids-for-system   Assign subid range to system accounts
  -g, --gid GROUP               Primary group identification name or number
  -G, --groups GROUPS           List of additional supplementary memberships
  -k, --skel SKEL_DIR           Custom template directory for home files
  -K, --key KEY=VAL             Override settings configuration variable
  -l, --no-log-init             Omit registering user in tracking databases
  -m, --create-home             Construct the home storage location
  -M, --no-create-home          Prevent construction of home location
  -N, --no-user-group           Do not allocate a matching private group
  -o, --non-unique              Permit duplicate numerical user identifiers
  -p, --password PASS           Encrypted password secret for shadow file
  -r, --system                  Generate a low-number system identity
  -R, --root DIR                Operate inside alternative root path
  -P, --prefix PREFIX_DIR       Prefix destination directory tree
  -s, --shell SHELL             Login command interpreter location
  -u, --uid UID                 Designate explicit numerical user identifier
  -U, --user-group              Allocate matching user private group
  -Z, --selinux-user SELINUX    Map user to specified selinux account
      --selinux-range RANGE     Assign specified selinux security range`,
	"ip": `Usage: ip [OPTION]... OBJECT COMMAND [ARG]...
Show or change Linux links, addresses, neighbors, routes, and rules using rtnetlink.

Objects and commands may be abbreviated to any unambiguous prefix, resolved in
the order ip(8) lists them: "ip r s" is "ip route show" and "ip l s eth0 up" is
"ip link set eth0 up". Use "ip l sh" for a link listing.

Options:
  -4, -6                      Restrict the output to IPv4 or IPv6
  -0                          Operate on link network family
  -B                          Operate on bridge network family
  -M                          Operate on mpls protocol family
  -f, -family FAMILY          Target protocol family (inet, inet6, bridge, link, mpls)
  -c, -color                  Control colorization in reports
  -b, -batch FILE             Execute batch commands loaded from file
  -force                      Continue batch execution past errors
  -s, -stats, -statistics     Display interface throughput metrics
  -d, -details                Emit comprehensive element metadata
  -l, -loops COUNT            Maximum iterations for state monitoring
  -o, -oneline                Format output records onto single text line
  -r, -resolve                Translate network numbers via DNS lookups
  -n, -netns NETNS            Switch network namespace before execution
  -N, -Numeric                Display raw numbers for protocol identifiers
  -a, -all                    Apply command against all available devices
  -t, -timestamp              Prepend current time prefix to records
  -ts, -tshort                Prepend abbreviated time representation
  -rc, -rcvbuf SIZE           Netlink socket buffer capacity
  -iec                        Display transmission rates using IEC units
  -br, -brief                 Compact tabular record summaries
  -j, -json                   Emit output formatted as JSON objects
  -p, -pretty                 Pretty print JSON payload
  -echo                       Echo applied configuration requests
  -h, -human, -human-readable Output statistics with human readable values
  -V, -Version                Display version of the ip utility

Objects and commands:
  ip link [show] [dev IFACE] [up]
  ip link add NAME type bond [mode MODE] [miimon MS]
  ip link add link PARENT name NAME type vlan id VLAN_ID
  ip link set dev IFACE up|down
  ip link set dev IFACE master BOND
  ip link set dev IFACE nomaster
  ip link set dev IFACE mtu MTU
  ip link set dev IFACE address LLADDR
  ip link set dev IFACE alias TEXT
  ip link set dev IFACE name NEWNAME
  ip link delete NAME
  ip addr [show] [dev IFACE]
  ip addr add ADDRESS dev IFACE
  ip addr del ADDRESS dev IFACE
  ip neigh [show] [dev IFACE]
  ip neigh add|replace ADDRESS dev IFACE lladdr LLADDR [nud STATE]
  ip neigh del ADDRESS dev IFACE
  ip route [show]
  ip route get ADDRESS
  ip route add PREFIX [via GATEWAY] [dev IFACE] [metric NUM]
  ip route del PREFIX [via GATEWAY] [dev IFACE] [metric NUM]
  ip rule [show]
  ip rule add|del [from PREFIX] [to PREFIX] [priority NUM] [table TABLE]
  --help    show this help`,

	"join": `Usage: join [OPTION]... FILE1 FILE2
Pair the lines of two sorted files whose join fields are equal, and write the
joined lines. The join field is the first one by default.

Options:
  -1 FIELD   use FIELD of FILE1 as the join field
  -2 FIELD   use FIELD of FILE2 as the join field
  -a NUM     also print lines of file NUM that have no partner
  -v NUM     print only lines of file NUM that have no partner
  -o LIST    print only the fields LIST names (0 for the join field)
  -t CHAR    select CHAR as the field separator
  -e STRING  substitute STRING for fields a line lacks
  -i         compare join fields ignoring case
  --header   copy each input's leading line into the output as a header
  -z         use NUL instead of newline as the record terminator
  --help     show this help`,
	"iptables": `Usage: iptables [-t TABLE] COMMAND [CHAIN] [RULE]
Inspect and edit the kernel IPv4 packet filter through the nftables API, on the
same tables the system firewall keeps its rules in.

Commands:
  -L [CHAIN]             list rules as a table
  -S [CHAIN]             print rules as the commands that would recreate them
  -A CHAIN RULE          append a rule
  -I CHAIN [NUM] RULE    insert rule at position (default 1)
  -R CHAIN NUM RULE      replace rule at position
  -D CHAIN RULE|NUMBER   delete a matching rule or rule number
  -C CHAIN RULE          verify existence of matching rule
  -F [CHAIN]             flush rules
  -Z [CHAIN]             reset packet and byte counters
  -N CHAIN               instantiate a new custom rule chain
  -X [CHAIN]             remove unused custom rule chain
  -E OLD NEW             retitle an existing custom chain
  -P CHAIN ACCEPT|DROP   set the base-chain policy

Rule matches, each of which "!" negates:
  -p PROTOCOL            protocol name or number, or all
  -s ADDRESS[/PREFIX]    source network
  -d ADDRESS[/PREFIX]    destination network
  -i INTERFACE           arriving interface, "+" matching any suffix
  -o INTERFACE           departing interface
  --sport PORT[:PORT]    TCP/UDP source port or range
  --dport PORT[:PORT]    TCP/UDP destination port or range
  --icmp-type TYPE[/CODE] ICMP type by name or number
  -f                     second and later fragments
  -j TARGET              jump to ACCEPT, DROP, RETURN, QUEUE, REJECT or a chain
  -g CHAIN               jump to a chain without returning
  --reject-with TYPE     rejection to send back, with -j REJECT

Options:
  -t TABLE               filter (the default), nat, mangle, raw or security
  -c PKTS BYTES          set initial packet and byte counter values
  -n                     leave addresses, ports and protocols as numbers
  -v                     add counters and interface columns
  -x                     print counters in full instead of rounding them
  --line-numbers         number the rules of each chain
  -w, -W                 accepted and ignored; every change is already atomic
  --modprobe PROGRAM     path to kernel module loader helper
  --help                 show this help`,
	"help": `Usage: ba6 help [COMMAND]
Show general help or detailed help for COMMAND.

Options:
  --help    show this help`,
	"man": `Usage: ba6 man [COMMAND]
Alias for help. Show general help or detailed help for COMMAND.

Options:
  --help    show this help`,
}

func helpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--help" {
			return true
		}
	}
	return false
}

// versionRequested mirrors helpRequested for -V/--version, the other flag
// nearly every original tool answers. top and cfdisk already implement it
// themselves (their own -V/--version combines with other option validation),
// so runApplet skips this generic check for those two.
func versionRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--version" || arg == "-V" {
			return true
		}
	}
	return false
}

// appletsWithOwnVersion lists applets that already implement -V/--version
// themselves, so runApplet's generic handling must not intercept it first.
var appletsWithOwnVersion = map[string]bool{
	"top":    true,
	"cfdisk": true,
}

func writeAppletHelp(w io.Writer, name string) error {
	help, ok := appletHelp[name]
	if !ok {
		return fmt.Errorf("unknown applet %q", name)
	}
	_, err := fmt.Fprintln(w, help)
	return err
}

func writeGeneralHelp(w io.Writer) error {
	for _, line := range []string{
		"Usage: ba6 [--seccomp=on|off] <applet> [args...]",
		"       ba6 help <applet>",
		"       (or symlink ba6 to an applet name)",
		"\nGlobal options:",
		"  --seccomp=on|off  enable or disable the seccomp filter (default: on)",
		"  --no-seccomp      alias for --seccomp=off",
		"\nApplets:",
	} {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(appletHelp))
	for name := range appletHelp {
		names = append(names, name)
	}
	sort.Strings(names)
	_, err := fmt.Fprintf(w, "  %s\n", strings.Join(names, " "))
	return err
}

func cmdHelp(args []string) int {
	if len(args) == 0 {
		if err := writeGeneralHelp(os.Stdout); err != nil {
			fatalf("help", "write error: %v", err)
			return 1
		}
		return 0
	}
	if len(args) > 1 {
		fatalf("help", "extra operand %q", args[1])
		return 1
	}
	if _, ok := appletHelp[args[0]]; !ok {
		fatalf("help", "unknown applet %q", args[0])
		return 1
	}
	if err := writeAppletHelp(os.Stdout, args[0]); err != nil {
		fatalf("help", "%v", err)
		return 1
	}
	return 0
}
