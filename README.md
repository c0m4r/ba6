# ba6

`ba6` is a dependency-free Linux/amd64 multicall binary containing a focused
set of Unix utilities. Invoke an applet as `ba6 cat file`, or create a symlink
whose basename is an applet name.

Run `ba6 --list` to list applets, `ba6 help COMMAND` for command-specific help,
or `ba6 COMMAND --help` for the same documentation.

## Included applets

The binary currently includes:

```text
[ adduser awk base64 basename blkid blockdev bunzip2 bzip2 cat chgrp chmod chown chroot cmp completion cp
cpio curl cut date dd df diff dig dirname dmesg du echo env expr false fdisk file find free fsck fsck.ext2
fsck.ext3 fsck.ext4 grep groupadd gunzip gzip halt head help hexdump hostname id iftop init insmod ip iptables
kill ln login losetup ls lsblk lsmod lsof man md5sum mkdir mkfs mkfs.btrfs mkfs.ext2 mkfs.ext3 mkfs.ext4
mkfs.xfs mknod mkswap mktemp modprobe mount mtr mv nano nc ncdu netstat nslookup od passwd pgrep pidof ping
pkill poweroff printenv printf ps pwd readlink realpath reboot rm rmdir rmmod sed seq sfdisk sh sha1sum
sha256sum sha512sum sleep sort ss stat strings swapoff swapon switch_root sync sysctl tail tar tee test
timeout top touch tr traceroute tree true tty udhcpc umount uname uniq unxz unzip unzstd uptime useradd watch
wc wget which whoami xargs xz zip zstd
```

## Regular-expression compatibility

`grep` and `sed` use POSIX Basic Regular Expressions by default; `grep -E`
and `sed -E` select POSIX EREs, and AWK regular expressions are POSIX EREs.
For AWK, a one-character `FS` other than space is always literal, while a
multi-character `FS` is an ERE. BA6 executes these expressions with RE2, which
cannot implement regular-expression backreferences: patterns such as
`\(x\)\1` are rejected clearly instead of being silently misinterpreted.
The common GNU BRE extensions `\|`, `\+`, and `\?` remain available.

## Bash completion

Load completion for the current Bash session with:

```sh
source <(ba6 completion bash)
```

For persistent per-user completion, generate the standard completion file:

```sh
mkdir -p ~/.local/share/bash-completion/completions
ba6 completion bash > ~/.local/share/bash-completion/completions/ba6
```

The filesystem and scripting set includes hard and symbolic links, canonical
path resolution, recursive octal permission and ownership changes, file
metadata formatting, pipeline fan-out, path component extraction, conditional
expressions, time display, and duration-based sleeping. `date` intentionally
does not change the system clock.

The extended recovery set also includes SHA-256 verification, filesystem and
directory usage, recursive search, stream editing, protected tar extraction,
gzip compression, system identity, `/proc` process and memory reporting, and
signal delivery. `find` deliberately has no `-exec`, and `sed` has no in-place
mode, preserving an explicit-write model for those individual applets.

The scripting and diagnostic set adds a small execution-capable shell, `xargs`,
environment and expression tools, byte and text inspection, process matching,
kernel logs, uptime, open-file and socket inspection, direct DNS queries,
IPv4/IPv6 ICMP, route/loss probing, interface traffic rates, verbose HTTP(S),
and TCP/UDP copying. `traceroute` probes with UDP and reads Linux's
unprivileged error queue; `mtr` prefers ICMP echo like the original, using
unprivileged ICMP datagram sockets where `net.ipv4.ping_group_range` permits
them and falling back to the UDP error queue otherwise. `mtr` refreshes a
full-screen display on a terminal and prints an mtr-compatible report when its
output is redirected or `-r` is given. `iftop` is a bounded batch sampler of
per-interface `/proc/net/dev` counters rather than an interactive per-flow
display. `top` renders the usual task, CPU, memory, and swap summaries with
procps-style task columns; it supports bounded batch collection for scripts and
a small terminal key set for sorting and view toggles. `netstat` reports sockets, the IPv4 routing table, and interface
counters in the net-tools layout; it never resolves names, so `-r` shows numeric
addresses apart from the default route. `ncdu` scans a directory and browses it
on a full-screen display, strictly read-only: it can neither delete files nor
spawn a shell. `tree` draws an indented directory listing.
`ip` accepts abbreviated objects and commands the way the original resolves
them, so `ip r s` lists routes and `ip l s eth0 up` brings a link up. `ps`
accepts the dashless BSD options, including `ps aux` and `ps axu`.
`init` provides a PID-1/container supervisor with process-group signal
forwarding, orphan reaping, descendant cleanup, and exit-status propagation.
Storage recovery includes filesystem signature probing, node creation, mounting,
unmounting, buffer flushing, and privileged halt/reboot/poweroff controls.
It now also includes block-device discovery, loop-device setup, chroot and root
switching, kernel-module management, and a focused one-shot DHCP client. Disk
recovery gains block-device controls, validated DOS/MBR partition editing,
read-only DOS/MBR and GPT partition listing,
read-only ext2/ext3/ext4 structural checking, bounded ext2, ext3, ext4, XFS,
and btrfs formatters, and Linux swap formatting and activation. Each formatter
writes exactly one fixed profile rather than a configurable layout: the ext
family uses 4 KiB blocks and a single block group from 1 MiB through 128 MiB
(8 MiB and up for the journalled ext3 and ext4 variants); `mkfs.xfs` writes a
version 5 filesystem with four allocation groups and a clean 64 MiB internal
log from 320 MiB up; and `mkfs.btrfs` writes a single-device filesystem with
unmirrored block groups from 128 MiB up. Their images are checked against
system `e2fsck`, `xfs_repair`, and `btrfs check` in development. Text
recovery gains focused AWK processing, block copying, file identification,
secure temporary files, command timeouts, and process snapshots.
Archive recovery now also includes safe ZIP and newc CPIO extraction, bzip2,
and MD5/SHA-1/SHA-512 verification. The self-contained `xz` and `zstd`
applets write interoperable raw-block streams (and Zstandard RLE blocks), so
they deliberately do not implement general compressed-block decoding.
`nano` is a compact full-screen editor with
navigation, insertion/deletion, line cutting, saving (`Ctrl-S`), and guarded
exit (`Ctrl-X`).

## Build and verify

The supported release target is Linux/amd64. The canonical build is static and
installs a seccomp filter at process startup. For environments that cannot use
the filter, invoke the binary as `ba6 --no-seccomp COMMAND ...` or
`ba6 --seccomp=off COMMAND ...`. Other startup protections remain enabled.

Applets that genuinely require a syscall denied by the normal filter
automatically skip seccomp. Mount and network applets retain `no_new_privs`;
execution frontends (`sh`, `env`, `xargs`, `timeout`, `chroot`, `login`, and
`switch_root`) do not, because changing that state would silently prevent their
children from using set-user-ID programs or file capabilities. A real PID 1
`init` likewise runs without seccomp or
`no_new_privs`. Core-dump protection remains active for every profile. Other
applets, including `nano`, continue to run with the filter enabled.

```sh
make build
make verify
```

`make verify` checks formatting, runs unit/regression tests, vet, the configured
linters, and the static-build check.

## System init

When invoked as PID 1 without a command, `init` reads `/etc/inittab`. The file
uses the traditional `id:runlevels:action:process` layout; runlevels are parsed
for compatibility but are not otherwise interpreted. A minimal configuration
for a serial-console system is:

```text
::sysinit:/bin/mount -t proc proc /proc
::sysinit:/bin/mount -t sysfs sysfs /sys
::sysinit:/bin/mount -t devtmpfs devtmpfs /dev
ttyS0::respawn:/bin/login
::shutdown:/bin/umount -a -r
```

`sysinit` and `wait` commands run synchronously in phase and file order. After
the `sysinit` phase, where `/proc` should be mounted, init validates
`/etc/hostname` and writes it to `/proc/sys/kernel/hostname`. Hostname errors
are reported but do not stop boot. `once` commands start after the `wait` phase,
and `respawn`/`askfirst` services are monitored with exponential crash backoff.
The supported event actions are `shutdown`,
`ctrlaltdel`, `powerfail`, `powerwait`, and `powerokwait`. SIGHUP reloads the
file. SIGINT runs `ctrlaltdel` and reboots; SIGUSR1 halts; SIGUSR2 powers off;
SIGTERM reboots; and SIGPWR runs power-failure actions before powering off.
If `/etc/powerstatus` begins with `O`, SIGPWR instead runs `powerokwait` actions
and resumes supervision.

PID 1 establishes `PATH=/sbin:/bin:/usr/sbin:/usr/bin` and standard root/console
environment variables, reaps orphaned processes, and never returns. Shutdown
runs configured actions, signals remaining processes, flushes buffers, unmounts
filesystems deepest-first, remounts the root filesystem read-only, and invokes
the kernel reboot operation. If reboot fails, PID 1 reports the error and stays
alive. `init -f FILE` selects an alternate inittab.

The bundled `login` applet authenticates against `/etc/passwd` and `/etc/shadow`
and supports SHA-256 (`$5$`) and SHA-512 (`$6$`) crypt hashes. It rejects locked
and expired accounts, initializes supplementary groups, drops all user and group
IDs, clears the inherited environment except for `TERM`, and executes the shell
from the passwd entry as a login shell. Configure `login` as a `respawn` service,
as above, so logging out returns to a fresh credential prompt. `login` must be
started by root and does not implement PAM-dependent policies or yescrypt/bcrypt
password hashes.

`passwd` complements `login` with interactive password changes. It verifies the
current password for non-root callers, confirms the replacement, generates a
randomly salted SHA-512 crypt hash, and atomically replaces the relevant account
database while preserving its mode and ownership. Root can reset locked accounts
and hashes unsupported by `login`; ordinary users still need filesystem permission
to update the database (the multicall binary should not be installed set-user-ID).

## Network configuration

The built-in `ip` applet uses Linux rtnetlink directly; it does not execute the
host's `ip` program.

```sh
ba6 ip addr show
ba6 ip addr add 192.0.2.10/24 dev eth0
ba6 ip addr del 192.0.2.10/24 dev eth0

ba6 ip route show
ba6 ip route get 192.0.2.25
ba6 ip route add default via 192.0.2.1 dev eth0 metric 100
ba6 ip route del default via 192.0.2.1 dev eth0 metric 100

ba6 ip neigh show dev eth0
ba6 ip rule show
```

Basic bond and VLAN lifecycle operations are also supported:

```sh
ba6 ip link add bond0 type bond mode active-backup miimon 100
ba6 ip link set dev eth0 master bond0
ba6 ip link set dev eth1 master bond0
ba6 ip link set dev bond0 up

ba6 ip link add link bond0 name bond0.100 type vlan id 100
ba6 ip link set dev bond0.100 up
ba6 ip link set dev bond0.100 mtu 1400 alias tenant-vlan
ba6 ip link show

ba6 ip link set dev eth0 nomaster
ba6 ip link delete bond0.100
ba6 ip link delete bond0
```

Showing addresses and routes normally needs no special capability. Changing
them requires the same `CAP_NET_ADMIN` privilege as iproute2.

The `iptables` applet manages an isolated IPv4 filter table through nftables
netlink without executing a host utility. Listing and changing firewall rules
requires `CAP_NET_ADMIN`:

```sh
ba6 iptables -A INPUT -p tcp --dport 22 -j ACCEPT
ba6 iptables -A INPUT -s 198.51.100.0/24 -j DROP
ba6 iptables -L --line-numbers
ba6 iptables -D INPUT 2
```

## Scope

The applets intentionally implement the options shown by each command's
`--help`; unsupported options are rejected. They are not complete GNU
coreutils, util-linux, procps, iproute2, or full POSIX-shell replacements. The
shell supports quoting, variable expansion, command lists, `&&`/`||`, pipelines,
redirections, and the `cd`, `pwd`, `echo`, `printf`, `read`, `export`, `unset`,
`exit`, and `:` builtins;
it intentionally omits a programming language (`if`, loops, functions, and
command substitution). Destructive commands include same-file, self-copy, and
filesystem-root safeguards.

See [COVERAGE.md](COVERAGE.md) for a measured, per-applet comparison against the
original tools, and [PROVENANCE.md](PROVENANCE.md) for how the applets were written.

## License

Copyright (C) 2026 c0m4r.

`ba6` is free software: you may redistribute it and/or modify it under the terms
of the GNU General Public License as published by the Free Software Foundation,
either version 3 of the License, or (at your option) any later version.

It is distributed in the hope that it will be useful, but WITHOUT ANY WARRANTY;
without even the implied warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR
PURPOSE. See the [GNU General Public License](LICENSE) for more details.

Every source file carries an `SPDX-License-Identifier: GPL-3.0-or-later` tag.
The binary bundles no third-party code: `go.mod` declares no dependencies, and
the build links only the Go standard library.

## Trademarks

The applet names are the names of the commands `ba6` aims to be compatible with,
used to identify those interfaces so that scripts and muscle memory keep working.
They are not claims of origin.

`ba6` is not affiliated with, endorsed by, or derived from the GNU Project, the
Free Software Foundation, BusyBox, util-linux, procps-ng, iproute2, the netfilter
project, the curl project, ISC, or any other upstream. All trademarks are the
property of their respective owners.

Applets differ from the originals in coverage and in behaviour; `COVERAGE.md`
documents where. If you need the original tool's exact semantics, run the
original tool.
