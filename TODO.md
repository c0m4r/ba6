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

- [x] `blockdev`
- [x] terminal DOS/MBR and conventional-GPT `cfdisk`
- [x] read-only `fdisk -l` and writable `sfdisk`
- [x] `fsck` and selected filesystem-specific checkers
- [x] `mkfs` and selected filesystem-specific formatters
      (`mkfs.ext2`, `mkfs.ext3`, `mkfs.ext4`, `mkfs.xfs`, `mkfs.btrfs`)
- [x] `mkswap`
- [x] `swapon`
- [x] `swapoff`

Filesystem creation and repair tools require particular care: partial or
incorrect implementations can destroy data. Add them only with strict format
validation, fixture-based tests, and clearly documented filesystem support.

## Boot and process recovery

- [ ] `pivot_root`
- [ ] `setsid`
- [ ] `getty`
- [x] `login`
- [ ] `nice`
- [ ] `renice`
- [ ] `nohup`
- [x] `watch`

## Kernel and system management

- [x] `sysctl`
- [ ] `hwclock`
- [ ] `lsusb`
- [ ] `lspci`
- [x] `passwd`
- [x] `useradd`
- [x] `groupadd`
- [x] `adduser`

## Diagnostics

- [x] `lsof`
- [x] `traceroute`
- [x] IPv6 support for `ping`
- [x] `mtr`
- [x] `dig`
- [x] `iftop`

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

- [x] `cpio`
- [x] `unzip`
- [x] `zip`
- [x] `xz` and `unxz`
- [x] `bzip2` and `bunzip2`
- [x] `zstd` and `unzstd`
- [x] `md5sum`
- [x] `sha1sum`
- [x] `sha512sum`

The self-contained XZ and Zstandard implementations use interoperable raw
blocks (plus Zstandard RLE blocks), not the full compressed-block decoders.

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
