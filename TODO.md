# TODO

`ba6` already provides a focused recovery and Unix toolbox. The items below
track the largest remaining gaps; they are not a commitment to full GNU,
BusyBox, or POSIX compatibility.

## Priority applets

- [x] `awk`
- [x] `dd`
- [x] `file`
- [x] `mktemp`
- [x] `timeout`
- [x] `top`
- [x] `lsblk`
- [x] `losetup`
- [x] `chroot`
- [x] `switch_root`
- [x] `lsmod`
- [x] `modprobe`
- [x] `insmod`
- [x] `rmmod`
- [x] `udhcpc` or another small DHCP client

## Disk and filesystem recovery

- [ ] `blockdev`
- [ ] `fdisk` or `sfdisk`
- [ ] `fsck` and selected filesystem-specific checkers
- [ ] `mkfs` and selected filesystem-specific formatters
- [ ] `mkswap`
- [ ] `swapon`
- [ ] `swapoff`

Filesystem creation and repair tools require particular care: partial or
incorrect implementations can destroy data. Add them only with strict format
validation, fixture-based tests, and clearly documented filesystem support.

## Boot and process recovery

- [ ] `pivot_root`
- [ ] `setsid`
- [ ] `getty`
- [ ] `nice`
- [ ] `renice`
- [ ] `nohup`
- [ ] `watch`

## Kernel and system management

- [ ] `sysctl`
- [ ] `hwclock`
- [ ] `lsusb`
- [ ] `lspci`

## Diagnostics

- [ ] `lsof`
- [ ] `traceroute`
- [ ] IPv6 support for `ping`

## Text and data processing

- [ ] `paste`
- [ ] `join`
- [ ] `comm`
- [ ] `split`
- [ ] `nl`
- [ ] `tac`
- [ ] `fold`
- [ ] `expand`
- [ ] `unexpand`
- [ ] `cksum`

## Archives, compression, and checksums

- [ ] `cpio`
- [ ] `unzip`
- [ ] `zip`
- [ ] `xz` and `unxz`
- [ ] `bzip2` and `bunzip2`
- [ ] `zstd` and `unzstd`
- [ ] `md5sum`
- [ ] `sha1sum`
- [ ] `sha512sum`

## Existing applet improvements

- [ ] Extend `sh` with `if`, loops, `case`, functions, pathname expansion, and
      command substitution.
- [ ] Consider a guarded `find -exec` implementation.
- [ ] Consider atomic in-place editing for `sed -i`.
- [ ] Expand `ip` support where recovery use cases require it.
- [ ] Add broader IPv4 firewall matches, tables, and targets to `iptables`.
- [ ] Add IPv6 firewall support.

Execution features such as `sh` command substitution, `find -exec`, and
`sed -i` expand the security and destructive-write surface. Their designs
should preserve the project's explicit-write and startup-hardening goals.
