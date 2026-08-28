# ba6

`ba6` is a dependency-free Linux/amd64 multicall binary bundling a focused set
of Unix utilities in one static, hardened binary with no third-party code.
Invoke an applet as `ba6 cat file`, or symlink `cat` → `ba6` and call it
directly.

```sh
ba6 --list              # list applets
ba6 help COMMAND        # or: ba6 COMMAND --help
```

## Applets

```text
[ adduser awk base64 basename blkid blockdev bunzip2 bzip2 cat cfdisk chgrp chmod chown chroot cmp completion cp
cpio curl cut date dd df diff dig dirname dmesg du echo env expr false fdisk file find free fsck fsck.ext2
fsck.ext3 fsck.ext4 grep groupadd gunzip gzip halt head help hexdump host hostname id iftop init insmod ip
iptables kill less ln login losetup ls lsblk lsmod lsof man md5sum mkdir mkfs mkfs.btrfs mkfs.ext2 mkfs.ext3
mkfs.ext4 mkfs.xfs mknod mkswap mktemp modprobe mount mtr mv nano nc ncdu netstat nslookup od passwd pgrep pidof
ping pkill poweroff printenv printf ps pwd readlink realpath reboot rm rmdir rmmod sed seq sfdisk sh sha1sum
sha256sum sha512sum sleep sort ss stat strings swapoff swapon switch_root sync sysctl tail tar tee test
timeout top touch tr traceroute tree true tty udhcpc umount uname uniq unxz unzip unzstd uptime useradd watch
wc wget which whoami xargs xz zip zstd
```

Coverage against the real tools is measured, not estimated — see
[COVERAGE.md](COVERAGE.md). See [PROVENANCE.md](PROVENANCE.md) for how the
applets were written.

## Behavior notes

- **Regexes**: `grep`/`sed` default to POSIX BRE, `-E` selects ERE; AWK
  regexes are ERE (a one-character `FS` is literal, a multi-character `FS`
  is an ERE). Matching runs on RE2, so backreferences (`\(x\)\1`) are
  rejected outright instead of being silently mishandled; the GNU BRE
  extensions `\|`, `\+`, `\?` work.
- **Shell** (`sh`): quoting, variable/arithmetic expansion, command
  substitution, pipelines, `&&`/`||`, redirections, `if`/`for`/`while`,
  `break`/`continue`, and the `cd pwd echo printf read export unset exit :`
  builtins. No functions, subshells, `case`, globbing, or here-documents.
- `find` has no `-exec`; `sed` has no `-i` — both keep an explicit-write
  model. `tar`/`zip`/`cpio` extraction is guarded against path traversal
  (`../`, absolute member paths).
- Destructive commands (`rm`, `cp`, `mv`, disk formatters, …) carry
  same-file, self-copy, and filesystem-root safeguards.
- **Filesystem tools**: each `mkfs.*` writes one fixed on-disk profile
  instead of a configurable layout — ext2/3/4 use 4 KiB blocks and a single
  block group (ext2 from 1 MiB, the journalled ext3/ext4 from 8 MiB, all up
  to 128 MiB); `mkfs.xfs` writes v5 with four allocation groups and a 64 MiB
  log from 320 MiB; `mkfs.btrfs` writes single-device, unmirrored, from
  128 MiB. Images are checked against `e2fsck`/`xfs_repair`/`btrfs check` in
  development. `fsck.*` is read-only. `cfdisk` edits DOS/MBR and
  conventional GPT tables on a terminal; `sfdisk` adds scripted DOS/MBR
  editing; `fdisk`/`sfdisk` add read-only listing of both label types.
- **Networking**: `traceroute` probes with UDP and Linux's unprivileged
  error queue; `mtr` prefers ICMP echo (unprivileged sockets where
  `net.ipv4.ping_group_range` allows, else the UDP error queue) and shows a
  live display or an `-r` report. `iftop` is a bounded batch sampler of
  `/proc/net/dev` counters, not an interactive per-flow view. `netstat`
  never resolves names except the default route. `host` queries the exact
  name given — no search list, no `ndots`; neither `host` nor `dig`
  performs zone transfers. `ip` accepts any unambiguous abbreviation of an
  object/command, resolved the way the original does (`ip r s` lists
  routes, `ip l s eth0 up` brings a link up). `ps` accepts the dashless BSD
  option style, including `ps aux`/`ps axu`.
- **TUI applets** (`top`, `less`, `ncdu`, `nano`, `cfdisk`) run full-screen,
  with a bounded batch/report mode for scripts where the original has one.
  `ncdu` is strictly read-only (no delete, no shell escape); `less` holds
  each file in memory and has no command that spawns another program.
- **Self-contained formats**: `xz`/`zstd` write interoperable raw-block
  streams (and Zstandard RLE blocks) rather than implementing general
  compressed-block decoding.

## Build and verify

```sh
make build
make verify   # formatting, tests, vet, linters, static-build check
```

The release target is Linux/amd64, statically linked, with a seccomp filter
installed at startup (plus `no_new_privs` and core-dump protection).
Disable it with `ba6 --no-seccomp COMMAND` / `--seccomp=off` where the
environment can't support it. Applets that genuinely need a denied syscall
skip seccomp automatically; execution frontends (`sh`, `env`, `xargs`,
`timeout`, `chroot`, `login`, `switch_root`) and a real PID-1 `init` also
skip `no_new_privs`, so their children can still use set-user-ID programs
and file capabilities.

## System init

As PID 1 with no command, `init` acts as a process-group signal forwarder,
orphan reaper, and exit-status propagator, and reads `/etc/inittab`
(`id:runlevels:action:process`; runlevels are parsed but not interpreted):

```text
::sysinit:/bin/mount -t proc proc /proc
::sysinit:/bin/mount -t sysfs sysfs /sys
::sysinit:/bin/mount -t devtmpfs devtmpfs /dev
ttyS0::respawn:/bin/login
::shutdown:/bin/umount -a -r
```

`sysinit`/`wait` entries run synchronously in file order; `/etc/hostname` is
applied right after `sysinit`; `once` runs after `wait`; `respawn`/
`askfirst` services are supervised with crash backoff. Signals: SIGHUP
reloads the file; SIGINT runs `ctrlaltdel` and reboots; SIGUSR1 halts;
SIGUSR2 powers off; SIGTERM reboots; SIGPWR runs `powerfail`/`powerwait`
(or `powerokwait` if `/etc/powerstatus` starts with `O`). Shutdown signals
remaining processes, flushes buffers, unmounts deepest-first, remounts root
read-only, then reboots — a failed reboot is reported and init stays alive.
`init -f FILE` selects an alternate inittab.

`login` authenticates against `/etc/passwd`/`/etc/shadow` (SHA-256/SHA-512
crypt only — no PAM, no yescrypt/bcrypt), drops privileges, and execs a
login shell; run it as a `respawn` service, as above. `passwd` changes a
password in place with a freshly salted SHA-512 hash; root can reset locked
accounts. The binary should not be installed set-user-ID.

## Scope

Applets implement the options shown by their own `--help`; unsupported
options are rejected. This is not a complete GNU coreutils, util-linux,
procps, iproute2, or POSIX-shell replacement — see
[COVERAGE.md](COVERAGE.md) for exactly where each applet falls short of its
original.

## Bash completion

```sh
source <(ba6 completion bash)                                  # current session
ba6 completion bash > ~/.local/share/bash-completion/completions/ba6  # persistent
```

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
