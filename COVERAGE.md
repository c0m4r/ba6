# Applet coverage against the original tools

> Applets marked _(run)_ were diffed against the real tool on a live system; _(src)_
> means the option parser was read but the behaviour could not be executed side by
> side (root-only or interactive applets); _(no reference)_ means the original is not
> installed on the measurement host, so nothing was compared. Everything below is
> measured, not estimated — see [How this was measured](#how-this-was-measured).

As of commit `5303302` plus the `pivot_root` and `getty` additions below, `ba6`
implements 170 applets (`main.go`'s `applets` map). Every one of them has now been
measured against its original; nothing is left under
[Not yet assessed](#not-yet-assessed).

**Short answer to "which are 1:1?"** — of the 170, 29 are genuine drop-ins,
75 more are near-complete, 57 are partial in ways that stay invisible until a
script reaches for a flag, none are so narrow that they should not be treated as
a replacement at all, and 9 have no upstream counterpart to compare
against; see the [verdict table](#verdict-in-one-table). `netstat`, `ps aux`/
`ps ax` and `host` are the closest matches recorded: every invocation tested against
them is byte-identical to the original. The *filesystem* applets (`mkfs.*`, `fsck.*`,
`fdisk`, `sfdisk`, `cfdisk`) score badly on flags but produce images that pass
`e2fsck`, `xfs_repair` and `btrfs check` cleanly — flag count is not the same as
correctness in either direction.

All known behaviour defects are fixed and pinned by `defects_test.go` — see
[Open defects](#open-defects). What remains is absent functionality rather than
wrong functionality: the [cross-cutting gaps](#cross-cutting-gaps) and the missing
options listed per applet below.

## Verdict in one table

| Tier | Meaning | Applets |
|---|---|---|
| **A — drop-in** | Byte-identical output on every case tested; only niche options missing | `base64` `basename` `cksum` `comm` `cut` `dirname` `echo` `expand` `false` `fold` `join` `mknod` `nice` `nl` `paste` `pivot_root` `printenv` `pwd` `seq` `sleep` `split` `tac` `touch` `tr` `true` `tty` `uname` `unexpand` `whoami` |
| **B — near-complete** | Common paths match; a handful of real gaps | `[` `blockdev` `bunzip2` `bzip2` `cat` `chgrp` `chmod` `chown` `chroot` `cmp` `cp` `date` `dd` `df` `du` `env` `expr` `find` `free` `grep` `groupadd` `gunzip` `gzip` `head` `hexdump` `host` `hostname` `hwclock` `id` `kill` `ln` `ls` `lspci` `lsusb` `md5sum` `mkdir` `mktemp` `mount` `mv` `nohup` `od` `pgrep` `pidof` `pkill` `printf` `ps` `readlink` `realpath` `renice` `rm` `rmdir` `sed` `setsid` `sha1sum` `sha256sum` `sha512sum` `sort` `stat` `strings` `sync` `sysctl` `tail` `tar` `tee` `test` `timeout` `top` `umount` `uniq` `unzip` `uptime` `wc` `which` `xargs` `zip` |
| **C — partial** | Everyday cases work, well-known flags or output details missing | `adduser` `awk` `blkid` `cfdisk` `cpio` `curl` `diff` `dig` `dmesg` `fdisk` `file` `fsck` `fsck.ext2` `fsck.ext3` `fsck.ext4` `getty` `iftop` `insmod` `ip` `iptables` `less` `login` `losetup` `lsblk` `lsmod` `lsof` `mkfs` `mkfs.btrfs` `mkfs.ext2` `mkfs.ext3` `mkfs.ext4` `mkfs.xfs` `mkswap` `modprobe` `mtr` `nano` `nc` `ncdu` `netstat` `nslookup` `passwd` `ping` `rmmod` `sfdisk` `sh` `ss` `swapoff` `swapon` `traceroute` `tree` `unxz` `unzstd` `useradd` `watch` `wget` `xz` `zstd` |
| **D — narrow subset** | A slice of the original; do not treat as a replacement | _(none)_ |
| **N/A** | ba6-specific, no upstream counterpart | `completion` `halt` `help` `init` `man` `poweroff` `reboot` `switch_root` `udhcpc` |

## Not yet assessed

None. The eighteen applets previously listed here — `adduser` `bunzip2` `bzip2`
`cpio` `groupadd` `md5sum` `sha1sum` `sha512sum` `sysctl` `tty` `unxz` `unzip`
`unzstd` `useradd` `watch` `xz` `zip` `zstd` — were measured against their
originals and are now placed in the [verdict table](#verdict-in-one-table), with
per-applet notes below. The account tools (`useradd`, `groupadd`, `adduser`) and
`watch` were exercised on a disposable VM, because they modify a real account
database or need a terminal.

## Open defects

None. All known behaviour defects are fixed and pinned by `defects_test.go`; the
four `ps`-specific ones are pinned by `TestPsMatchesProcpsArithmetic` in
`inspection_test.go`, and the checksum, `sysctl` and `watch` defects found while
assessing the applets above are pinned by `checksum_escape_test.go`,
`TestSysctlTree` and `TestWatchTitleLayout` in `management_linux_test.go`.

Fixed during that assessment pass, each previously a silent wrong answer rather
than a missing option:

* **`sysctl -a` printed nothing at all.** The walk returned the first error it
  met, and `/proc/sys/fs/binfmt_misc/register` is write-only, so an unprivileged
  sweep aborted on it and reported zero settings. Unreadable keys are now
  skipped — silently when the file carries no read bit, and with the original's
  `permission denied on key` line otherwise. `sysctl -a` now matches procps key
  for key and value for value (1590 keys here), stderr included.
* **`sysctl` flattened multi-line values.** Keys such as `fs.binfmt_misc.CLR`
  hold several lines; ba6 emitted them as one record with embedded newlines,
  which does not survive being read back. The original repeats the key on each
  line, and now so does ba6.
* **The digest tools could not read their own or GNU's escaped lines.** A name
  containing a backslash, newline or carriage return is written with those
  characters escaped and the line flagged with a leading `\`. ba6 wrote such
  names raw and rejected the originals' escaped lines as malformed, so any
  checksum file covering such a name failed to verify in either direction.
* **`-c` treated a malformed line as a failure.** GNU counts them and warns once
  (`WARNING: N lines are improperly formatted`) while leaving the exit status at
  zero; ba6 printed a diagnostic per line and exited 1, so a checksum file with
  a stray comment failed the build. The `could not be read` and `did NOT match`
  warnings, the `FAILED open or read` marker, and the
  `no properly formatted checksum lines found` error were all missing too.
* **`tty -s` was rejected** as an extra operand instead of selecting the
  status-only mode scripts use it for.
* **`watch` drew a tab-separated header** rather than the original's layout with
  `host: ctime` aligned against the right edge of the terminal.
* **The account tools exited 1 for every failure.** shadow-utils uses distinct
  statuses that scripts branch on — 9 for a name already in use, 4 for a UID or
  GID already taken, 6 for a missing group, 3 and 19 for invalid names — and ba6
  now returns those, with the originals' message wording.

## Cross-cutting gaps

Absent behaviour rather than wrong behaviour, each of which touches many applets.

* **No `--version` for most applets.** _(run)_ `cfdisk` is the exception, with
  `-V`/`--version`; most other applets answer `unsupported option "--version"`.
  Scripts and packagers commonly probe this.
* **No `Try 'x --help' for more information.` line** after most usage errors. _(run)_
  `sha256sum`, `env`, `printenv`, `blkid`, `cut` and `du` print it where their
  originals do; the other applets stop at the diagnostic.
* **C locale only.** `ls -l` prints `Aug  7 02:46`, `printf %f` prints `3.14`, `seq`
  prints `1.5`. The system tools follow `LC_TIME`/`LC_NUMERIC`. This is a reasonable
  deviation for a static rescue binary — worth one line in each help text.

## Per-applet detail

### Tier A — drop-in

| Applet | Coverage | Missing | Notes |
|---|---|---|---|
| `base64` | 2/3 | `-i` | `-d`, `-w 0`, `-w N` match; round-trips with coreutils _(run)_ |
| `basename` | 3/3 | — | `-s`, `-a`, suffix form, `/`, `//`, trailing slash and `-z` all match _(run)_ |
| `cksum` | n/a | — | POSIX CRC-32/CKSUM, byte count and stdin form match _(run)_ |
| `comm` | 5/5 | — | `-1/-2/-3` (combined too), `-z`, `--total`, and the check-on-unpairable order semantics of `--check-order`/`--nocheck-order` match _(run)_ |
| `cut` | 9/9 | — | every option the original has: `-b -c -f -d -s -n -z --complement --output-delimiter`. Ranges are merged before use (`-c1-1,1-2` is one run), a range past the end of the line adds nothing and no delimiter, `-c` counts characters where `-b` counts bytes, and the usage diagnostics — `only one list may be specified`, `fields are numbered from 1`, `invalid decreasing range` and their `Try 'cut --help'` line — match verbatim _(run)_ |
| `dirname` | 1/1 | — | byte-identical on every path tested, including trailing, doubled and leading double slashes and `-z` _(run)_ |
| `echo` | 3/3 | — | `-n` `-e` `-E` and all escapes matched _(run)_ |
| `expand` | 2/2 | — | `-i` `-t` (size, stop list, `/N` and `+N` forms) byte-identical on randomized inputs _(run)_ |
| `false` `true` `whoami` | — | — | identical _(run)_ |
| `fold` | 4/4 | — | `-b` `-c` `-s` `-w`, tab stops, backspace and CR column rules match _(run)_ |
| `join` | 11/11 | — | `-1` `-2` `-a` `-v` `-o` `-t` `-e` `-i` `--header` `-z`, field re-join rules and unsorted-input reporting match _(run)_ |
| `mknod` | 1/3 | `-Z` `--context` | `-m` present; FIFO creation identical, device nodes need root _(run)_ |
| `nice` | 2/2 | — | `-n` and the legacy `-N` form; niceness applies to the whole command run, exit codes match _(run)_ |
| `od` | 24/26 | the `f16`/`fL` long double, whose range this build cannot print, and `-S`/`--strings` and `--traditional` | rewritten around the original's own format model and diffed against coreutils 9.11 over ~100 invocations on text, binary, float and all-zero inputs. `-t` takes every type and size the original does — `a c d f o u x` with a numeric size or the `C S I L` letters (`B H F D` for floats) and the `z` gutter — and several specifications stack under one address, with the narrower one's columns widened so the group lines up, exactly as the original lines them up. The traditional letters (`-a -b -c -d -f -i -l -o -s -x`) accumulate the same way. Also present: `-A`, `-j`, `-N`, `-v`, `-w[N]` (whose argument must be attached, as in the original) and `--endian`. Two details worth naming: the field widths are the ones the original derives from each type, down to the sign column a signed type reserves; and a float is printed at its type's own precision, widened only until the text reads back as the same value — which is why 1.0 is "1" while 1.2345679e+08 needs the exponent, and why a subnormal starts its search at one digit and prints "5e-324". A width no C type has draws the original's two-line refusal _(run)_ |
| `paste` | 3/3 | — | `-d` `-s` `-z`, delimiter cycling, escape handling, short files and multi-file padding match _(run)_ |
| `pivot_root` | n/a | — | thin `pivot_root(2)` wrapper, no chdir/exec; success, `EBUSY` on a non-mount-point `new_root`, `EPERM` for a non-root caller, and the missing-argument guard all match util-linux, tested under `unshare --mount --propagation private` on a disposable VM so nothing touched a real root filesystem _(run)_ |
| `printenv` | 1/1 | — | identical, including `-0` _(run)_ |
| `pwd` | 2/2 options | — | `-L`/`-P` both present, output identical _(run)_ |
| `seq` | 3/3 | — | `-f` `-s` `-w`, integer, float, reverse and large ranges all match, including GNU's operand-driven decimal precision _(run)_ |
| `sleep` | n/a | — | suffixes, fractions and multiple operands behave like GNU; only the error wording differs _(run)_ |
| `split` | 11/13 | `--filter` `-u`'s effect | `-l` `-b` `-C` `-n N|l/N|r/N|K/N` `-d` `-x` `-a` `-e` `-t` `--verbose` `--additional-suffix`, legacy `-N`, suffix widening sequence and byte-chunk arithmetic match _(run)_ |
| `tac` | 3/3 | — | `-b` `-r` `-s`, separator placement and multi-file ordering match _(run)_ |
| `touch` | 8/8 | — | `-a -c -d -f -m -r -t -h --time`; `-d` takes the same date strings as `date -d`, `-t` the POSIX `[[CC]YY]MMDDhhmm[.ss]` stamp with its 69/68 century split, `-r` copies both stamps, and `-a`/`-m` leave the other timestamp alone through `UTIME_OMIT` rather than a read-back. `-h` retimes a symlink itself. Timestamps, exit statuses and the `cannot touch 'x'`/`invalid date format 'x'` wording all match _(run)_ |
| `tr` | 3/4 | `-t` | `-d` `-s` `-c`, ranges, `[:classes:]`, `-cd` combinations match _(run)_ |
| `tty` | 1/1 | — | `-s`/`--silent`/`--quiet` and the bare form; `/dev/pts/N` name, the `not a tty` message and both exit statuses match, checked under a pseudo-terminal _(run)_ |
| `uname` | 7/9 | `-p` `-i` (GNU prints `unknown` for both) | `-a` `-n` `-v` `-mrs` byte-identical _(run)_ |
| `unexpand` | 3/3 | — | `-a` `--first-only` `-t`, including the `-t`-implies-`-a` rule, byte-identical on randomized inputs _(run)_ |

### Tier B — near-complete

| Applet | Coverage | Missing | Notes |
|---|---|---|---|
| `[` / `test` | 15/21 operators | `-u` `-g` `-k` `-O` `-G` `-N`, `( )` grouping | everything else including `-nt -ot -ef -a -o !` matches GNU exit codes exactly _(run)_ |
| `blockdev` | 12/26 | `--getpbsz` `--getiomin` `--getioopt` `--getalignoff` `--setbsz` `--getsize` `--getfra`/`--setfra` `--getdiskseq` `--getzonesz` `-q` `-v` | the 12 present cover the recovery cases _(src)_ |
| `bunzip2` / `bzip2` | 6/12 | `-t` `-1`..`-9` `-v` `-z` `-L` | round-trips against the real tool in both directions, unlike the xz and zstd pair. `-c` `-d` `-k` `-f`, the in-place convention (replace the file, add or strip `.bz2`) and stdin/stdout streaming match _(run)_ |
| `cat` | 6/10 | `-v` `-e` `-t` `-u` | `-n -b -E -T -s -A -- -`, multi-file numbering and missing-newline handling all byte-identical _(run)_ |
| `chmod` | 8/8 | — | every option coreutils has: `-R -c -f -v --reference --preserve-root --no-preserve-root`, and the full symbolic mode grammar beside octal — multiple `who` letters, chained operations sharing one `who` (`u+x-w`), comma-separated clauses, `+`/`-`/`=`, the letters `r w x X s t`, and the `go=u` copy form. `X` only sets execute on a directory or a file that already has one, `=` clears the special bit tied to any class it touches unless the same clause sets it, and an omitted `who` is masked by the umask where an explicit `a` is not. `-v`/`-c` print GNU's `changed from`/`retained as` lines byte for byte, `-f` drops the message without changing the status, and the operand diagnostics carry the `Try ...` line. Diffed against coreutils 9.11 over the same tree in ~30 cases. One deliberate difference: a recursive run applies each directory's new mode *after* everything inside it, so `chmod -R 600` cannot lock the walk out part-way where the original's `fts` walk stops there; the reports are held back and printed in the original's parent-first order, and each operand is finished before the next begins so naming a directory twice reads the second pass's modes _(run)_ |
| `chown` / `chgrp` | 12/12 and 11/11 | — | every option coreutils has: `-R -h -c -f -v -H -L -P --dereference --reference --preserve-root/--no-preserve-root`, plus chown's `--from`. Diffed against coreutils 9.11 by running both binaries over the same tree and comparing output, exit status and the resulting ownership in ~40 cases — the ids used are the caller's own, so an unprivileged run exercises every path but the privileged change itself. The reporting matches word for word: `changed ownership of 'f' from a:b to a:c` against `ownership of 'f' retained as a`, where chgrp names the group alone and chown names the group beside the owner only when one was asked for — and the *requested* ids on the "to" side, so `chown :group` shows an empty owner there, exactly as the original does. `-R` visits children before their parents in kernel directory order, `--from` reports a skipped file as unchanged, `-f` drops the message but keeps the failing status, and the operand and id diagnostics carry the original's quoting and `Try ...` line _(run)_ |
| `chroot` | 0/3 | `--userspec` `--groups` `--skip-chdir` | core behaviour present _(src)_ |
| `cmp` | 1/5 | `-b` `-i` `-l` `-n` | `-s`, `differ: byte N, line N`, all three EOF forms (`line`, `in line`, `which is empty`) and the exit codes match; the EOF file name is quoted `'x'` where a UTF-8 locale gives GNU `‘x’` _(run)_ |
| `cp` | 31/34 | extended attributes and SELinux labels are never carried (`--preserve=xattr`/`context`, `-Z`, `-c` are accepted and do nothing), `--sparse` is accepted but holes are neither detected nor created, and `--copy-contents` is absent | `-r/-R -a -d -L -P -H -f -i -n -u -l -s -p --preserve/--no-preserve -x --parents --remove-destination --attributes-only --reflink --strip-trailing-slashes -t -T -b -S --backup -v`. Diffed against coreutils 9.11 by running both binaries over the same tree and comparing output, exit status and the resulting files in ~50 cases. The rules that are easy to get subtly wrong all match: the no-dereference default applies to `-r` but **not** to `-l`, so `cp -lr` hard-links what a symbolic source points at while `cp -lrP` links the symlink; `-v` announces a directory before its contents and walks entries in **inode** order, which is the order the original's `fts` walk sorts them into; a per-entry failure is reported where it happens and the walk carries on; `-s` refuses a relative source whose link would land in another directory; `--parents` rebuilds the operand's own path; and a declined `-i` prompt exits 1. `--reflink` really asks the filesystem, through `FICLONE`, and `always` fails where the filesystem cannot share extents. One deliberate difference: copying a directory into a destination it already contains is refused up front, where the original copies part of the tree before detecting the cycle _(run)_ |
| `date` | 10/11 | `--debug` | `-d`/`--date`, `-s`/`--set`, `-f`/`--file`, `-R`, `-I[SPEC]`, `--rfc-3339=SPEC`, `--resolution`, plus `-u` and `-r`. The date-string parser covers epoch stamps, calendar dates (ISO, `YYYY/MM/DD`, `MM/DD/YYYY`, compact `YYYYMMDD`, month names either side of the day), clock times with meridiem and zone, the day words, weekday names with `next`/`last`, and relative items in any combination — including GNU's rule that `ago` reverses only the item before it, and that any absolute item truncates the nanoseconds while a purely relative one keeps them. Not covered: named zones beyond `UTC`/`GMT`/`Z`, `--debug`, and the more exotic corners of GNU's parser. Format directives now include `%U %W %V %G %g %k %l %q %:z %::z`; `-s` reports the same failure as the original when the caller lacks `CAP_SYS_TIME`, and still prints the requested time _(run)_ |
| `dd` | 8/13 operands | `iflag=` `oflag=` `cbs=`, `conv=fsync\|fdatasync\|noerror\|swab\|excl\|ucase\|lcase`, `status=progress` | `if of bs ibs obs count skip seek conv=notrunc,sync status=none` produce byte-identical results to GNU on every combination tested, including stdin/stdout; the summary omits GNU's `, T s, R MB/s` tail _(run)_ |
| `df` | 15/15 | — | every option coreutils has: `-a -B -h -H -i -k -l -P -t -T -x -v --output --total --sync/--no-sync`. Diffed whole against GNU coreutils 9.11 over the live mount table in 30 option combinations, all byte-identical — the field selection, every heading (`1K-blocks` against `1024-blocks` against `Size`, `Available` against `Avail`, `Use%` against `Capacity`), the per-column minimum widths, the `-BM`-echoes-its-unit rule, and the SI kilo's small `k`. Two cases that only show up on a real system also match: a mount another has since been stacked on top of is listed with a dash in every figure rather than its neighbour's numbers, and so is one that cannot be measured at all, while a filesystem that genuinely reports no blocks still prints zeroes _(run)_ |
| `du` | 19/25 | `--files0-from` `-l`/`--count-links` `--si` `--time` `--time-style` `-X`/`--exclude-from` | `-a -s -c -d/--max-depth -S -x -L -D/-H -P -h -k -m -b -B/--block-size -t/--threshold -0 --apparent-size --inodes --exclude`. Byte-identical to GNU on every case tested, including the details: directories contribute no apparent size, a hard link or a repeated operand is counted and listed once, `-B` with a bare unit (`-B K`) echoes that unit after each value while `-B 1K` does not, `-S` still passes the full total up to the parent, and entries are walked in kernel directory order the way the original's `fts` walk is _(run)_ |
| `env` | 2/11 | `-0` `-C` `-S` `-a`, signal options | `-i` `-u` and `NAME=VAL` prefixes match _(run)_ |
| `expr` | arithmetic complete | `:` (regex match), `length` `substr` `index` `match` | `+ - * / % < <= = != >= > & \|` all match _(run)_ |
| `free` | 19/19 | — | every option procps has: `-b -k -m -g --kilo/--mega/--giga/--tera/--peta --kibi/--mebi/--gibi/--tebi/--pebi -h --si -l -L -t -v -w -s -c`. The extra rows (`Low:`/`High:`, `Total:`, `Comm:`) appear in procps' own order, `-w` splits buff/cache into its two columns, `-L` reproduces the single-line form's field widths and the leading space on its `MemUse` label, and the repeat options print a blank line between blocks. The bad-count and bad-interval diagnostics match word for word _(run)_ |
| `find` | every predicate and action but the four that run a command | `-exec` `-execdir` `-ok` `-okdir` (see below), `-fprint`/`-fprint0`/`-fprintf`/`-fls`, `-newerXY`, `-daystart`, `-used`, `-context`, `-regextype` | diffed against findutils 4.11 over 300 randomized expression/operand pairs and 60 hand-built cases, all identical, output order included: the walk now visits entries in kernel directory order the way the original's `fts` does, and keeps each operand's spelling (`./x`, `x/`, `x//y`) instead of cleaning it. Added since the last pass: `-depth -xdev -follow -H -L -P`, `-perm` (all three forms, octal or symbolic), `-user -group -uid -gid -nouser -nogroup`, `-links -inum -samefile -fstype`, `-regex -iregex -lname -ilname -wholename`, `-xtype`, `-readable -writable -executable`, `-atime -ctime -mmin -amin -cmin -anewer -cnewer`, and the actions `-printf -ls -delete -prune -quit` (`-delete` turns on `-depth`, `-quit` stops before the implicit `-print`, and an unusable `-printf` directive is reported once and then written out verbatim). Patterns use fnmatch semantics rather than a shell glob's, so a `*` in `-path` crosses `/`. The diagnostics use find's own `` `x' `` quoting _(run)_ |
| `grep` | 43/45 | `-P`/`--perl-regexp` (RE2 is not PCRE) | measured against GNU grep 3.12 and diffed on 1000 randomized inputs across 35 option sets plus 60 hand-built cases, every one byte-identical. Present: `-i -v -n -c -l -L -h -H -w -x -F -E -G -r -R -q -o -s -b -a -I -z -Z -T -U -e -f -m -A -B -C -NUM -d -D --color --binary-files --include --exclude --exclude-from --exclude-dir --label --group-separator --no-group-separator --line-buffered`. The context machinery matches GNU's exactly: `:` for a selected line against `-` for a context line, the `--` separator only where the printed *coverage* is non-contiguous (so `-A1 -o` prints one where the groups really are apart and none where the trailing context closed the gap), trailing context surviving `-m`, and an empty match selecting a line while printing and highlighting nothing. Binary files report `binary file matches` on stderr while `-c`, `-l` and `-q` still read them; `-T` right-aligns numbers in a field as wide as the input's largest possible offset (the file's size, or 19 digits for a pipe). `--color=always` writes GNU's own escapes for matches, names, separators and line numbers. Patterns gained the GNU escapes `\< \> \b \B \w \W \s \S` in both BRE and ERE, and `-w`/`-x` are applied as position tests rather than as regexp syntax, so `-o -w` finds every word on a line the way the original does _(run)_ |
| `groupadd` | 2/10 | `-f` `-o` `-r`/`--system` `-p` `-K` `-R` `-P` `-U` | `-g` and the bare form; the created `/etc/group` line, shadow-utils exit statuses (9 name in use, 4 GID in use, 3 invalid name) and message wording match, tested against a real account database on a disposable VM _(run)_ |
| `gzip` / `gunzip` | 17/17 and 12/12 | — | every option the originals have: `-1`…`-9` `--fast` `--best` `-c -d -f -k -l -n -N -q -r -S -t -v`. Streams interoperate in both directions, and the bookkeeping was diffed against gzip 1.14 over ~30 invocations: the `-l` table's column widths, the `-v` `NAME:\t 99.1% -- replaced with NAME.gz` line, `-tv`'s ` OK`, the `(totals)` row, and the two exit statuses the original distinguishes — 1 for a failure, 2 for the warnings it prints for an unknown suffix or an output file that is already there. Two details that are easy to get wrong both match: the reported ratio measures the deflate stream alone, so the member's header and trailer come off the compressed side first (and the totals row discounts only the last member's, which is where the original keeps that figure), and it is *rounded* to a tenth rather than truncated, negative ratios included. `-l` names the file the member would unpack to, and only `-N` reads the name stored inside it — which is also the name `-dN` writes to, so its "already exists" check sees it. The compressed bytes come from Go's deflate encoder, so a stream is a little larger or smaller than the original's at the same level _(run)_ |
| `hexdump` | 12/17 | `-e`/`-f` (the custom format strings the other options are shorthand for) and `-L` | `-C` (the classic hex+ASCII gutter), the bare default (tight 2-byte hex — narrower than the explicit `-x`, a real quirk of the tool), `-b -c -d -o -x`, `-n`/`-s` on every mode rather than only `-C`, and `-v`. Byte-identical to util-linux 2.41 on text, binary and all-zero inputs, including the zero-padded 8-column fields that are wider than od's equivalents, the trailing-line suppression on empty input, and `-s` past EOF against an explicit `-n 0` _(run)_ |
| `head` | 4/5 | `-z`, and **negative counts** (`-n -2`, `-c -3`) | `-n` `-c` `-q` `-v` `-n +N` and multi-file headers match _(run)_ |
| `hostname` | 7/7 | — | measured against inetutils 2.8 (this host's reference). `-a` (hosts-file aliases, each with the original's trailing blank), `-d` (`(none)` without a dot), `-f` (canonical name via /etc/hosts then DNS, falling back to the short name), `-i` (all addresses, space-separated), `-s`, `-y` (kernel domainname) plus `-F FILE` and the bare `NAME` set form — the `sethostname:`, `Empty hostname`, `fopen:` and `getline: No text` error paths all match; setting needs root, verified by comparing the unprivileged failure. Names are looked up in /etc/hosts first and DNS second; the DNS step needs a socket, so under the default seccomp filter only /etc/hosts names resolve (give `--seccomp=off` for full DNS) _(run)_ |
| `hwclock` | 5/8 | `--adjfile` drift handling `--directisa` `--test` | `--show`/`--get` format, `--hctosys`, `--systohc`, `--set --date`, `--utc`/`--localtime`; ioctl on /dev/rtc with a sysfs fallback _(run)_ |
| `id` | 4/8 | `-r` `-z` `-Z` | long form, `-u` `-g` `-G` `-Gn` and the group order all match _(run)_ |
| `kill` | 2/10 | `-a` `-q` `-p` `-L` `-r` `--timeout` | `kill -l` prints a bare space-separated list of 18 names; GNU prints a numbered table of 64 _(run)_ |
| `ln` | 14/14 | — | every option coreutils has: `-s -f -i -n -r -L -P -d/-F -t -T -v -b -S --backup`. Diffed against coreutils 9.11 by running both binaries over the same tree and comparing output, exit status and the resulting directory in ~40 cases. The details that only show up on a real run match: a hard link is announced with `=>` where a symbolic one uses `->`, a declined `-i` prompt fails rather than skipping, `-r` writes the body as a path from the link's own directory, and each failure picks the wording ln picks — the destination alone for `EEXIST`, `failed to create hard link 'x' => 'y'` otherwise, `failed to access` for an unreadable source, and `hard link not allowed for directory` before the kernel ever sees it _(run)_ |
| `ls` | 48/56 | `--color` (accepted, nothing is coloured), `--dired`, `--author`, `--zero`, `--hyperlink`, `--si`'s effect on the block columns alone, and SELinux contexts, which read `?` | rewritten around the original's own model and diffed against coreutils 9.11 over `/usr/bin`, `/usr/lib`, `/etc`, `/dev`, `/usr/share/man/man1` and hand-built fixtures — ~100 option combinations, output and exit status identical. Present: `-a -A -B -C -F -G -H -I -L -N -Q -R -S -T -U -X -Z -b -c -d -f -g -h -i -k -l -m -n -o -p -r -s -t -u -v -w -x -1 --block-size --file-type --format --full-time --group-directories-first --hide --indicator-style --quoting-style --sort --time --time-style`. The details that are easy to get wrong all match: the column search is the original's, tab stops and all, so `-C`/`-x` line up character for character; a name is measured the way the original measures it under `LC_ALL=C`, where a byte outside printable ASCII counts as no columns; the six-month boundary between a clock and a year is half an *average* Gregorian year, not half of 365 days; `-v` is a faithful `filevercmp`, suffix cutting and `~` ordering included, with the original's `strcmp` tie-break; `-c`/`-u` also *sort* by their timestamp outside the long listing; the mode string grows an eleventh column when any file in the batch carries an ACL or a security context; a device shows its major and minor numbers in the size column, sized the way the original sizes them; `-L` only follows a link when the layout needs the stat, and reports one it cannot follow with a row of question marks; and the five quoting styles, `shell-escape`'s `$'...'` splicing included, are byte-identical. One known gap in the column search: a directory of one-character names at a width of about four settles for one column fewer than the original _(run)_ |
| `lspci` | 2/11 | `-m` `-v` `-t` `-s` `-d` `-x` `-k` `-b` `-nn` | default and `-n` listings are byte-identical to pciutils with pci.ids, including built-in class names; no verbose/tree modes _(run)_ |
| `lsusb` | 1/7 | `-t` `-s` `-d` `-v` `-D` `-P` | default listing matches with usb.ids installed; no tree or verbose modes _(run)_ |
| `md5sum` `sha1sum` `sha512sum` | 5/10 | `--tag` `-z` `--ignore-missing` `--strict` `-w` | share one implementation with `sha256sum`. Byte-identical to coreutils across 2361 adversarial file names covering backslash, newline, carriage-return, quote and control-character cases: the escaped-line format and its `\` marker, the shell-style quoting of names in `-c` output, all three `WARNING` summaries with correct singular/plural, `FAILED open or read`, `no properly formatted checksum lines found`, and the exit status in every combination of `--quiet`/`--status` _(run)_ |
| `mkdir` | 3/5 | `-Z` | `-p` `-m` `-v` match, errors match apart from the Go string _(run)_ |
| `mktemp` | 3/6 | `-u` `-q` `--suffix` | `-d` `-p` `-t` and the template match, including the suffix form and the too-few-X error _(run)_ |
| `mount` / `umount` | 24/41 and 16/16 | mount's `-N`/`--namespace`, `--options-mode`/`--options-source`, `-s`/`--sloppy`, the `umount.TYPE` and `mount.TYPE` helpers, and the mtab that no longer exists | mount: `-t -o -r -w -a -O -T -L -U --source --target -B/--bind -R/--rbind -M/--move --make-{,r}{shared,private,slave,unbindable} -f/--fake -v -n -l`, plus the one-operand form that looks a device or mount point up in fstab and takes its entry. With no operands it prints the original's own listing — `SOURCE on TARGET type TYPE (options)` — rather than the kernel's table verbatim, which is where the old defect was: the userspace-only options recorded in `/run/mount/utab` are merged in, and a loop device is shown as the file it stands for, so the lines match util-linux 2.41 exactly on a live system. A read-only bind is made in two steps, as the original makes it. umount: the whole option set — `-a -A -R -t -O -f -l -r -q -v --fake -n -c -d -i` — and a target is resolved against the mount table before the kernel sees it, so a device names its mount point and a path with no mount reads "not mounted." rather than an errno. Both keep the original's exit statuses: 32 for a mount or unmount the kernel refused, 1 for a command line it could not use _(run, and the mounting itself exercised under `unshare --user --map-root-user --mount`)_ |
| `mv` | 15/16 | `--exchange` (the atomic swap, which needs `renameat2(RENAME_EXCHANGE)`) | `-f -i -n -u -v -t -T -b -S -Z --backup --strip-trailing-slashes`, diffed against coreutils 9.11 over the same tree in ~40 cases including the directory ones. Matching behaviour that is easy to miss: a declined `-i` prompt exits 1, `-b` moves the destination aside so a directory may then replace a file, a directory replaces an *empty* directory (Go's own `os.Rename` refuses this outright, so the raw system call is used), a non-empty one is reported as `cannot overwrite 'd': Directory not empty`, and moving a directory into itself is refused by name. Backups are the shared coreutils scheme, `VERSION_CONTROL` and `SIMPLE_BACKUP_SUFFIX` included, with the originals' distinction between an `invalid` and an `ambiguous` control name. Cross-device moves still fall back to copy-and-delete _(run)_ |
| `nohup` | n/a | — | SIGHUP immunity, /dev/null stdin and nohup.out redirection under a pseudo-terminal, 125/126/127 exit paths all match _(run)_ |
| `pidof` | 8/8 | — | `-s` (one PID per name), `-c` (root only, like procps itself), `-q`, `-w`, `-x`, `-o` (including the historic `%PPID` token), `-t`, `-S`/`-d`. Matching follows procps' own rules — argv0 (login-shell `-` stripped), basename comparisons, the executable link, comm only under `-w` or a rewritten argv0, and `-x`'s interpreter check against argv1 — so a shebang script is found only with `-x`, exactly like the original. Newest-first ordering, the shared separator and the exit status match; the `illegal omit pid value` warning wording too _(run)_ |
| `pgrep` / `pkill` | 22/31 and 21/29 | `-Q`/`--shell-quote`, `--ns`/`--nslist`, `--cgroup`, `--env`, and pkill's `-q`/`--queue` and `-m`/`--mrelease`; `-L` is accepted but the pidfile's lock is not checked | measured against procps-ng 4.0.7 and diffed invocation by invocation over the live process table: `-f -x -v -i -c -n -o -O -p -P -g -G -s -t -u -U -r -A -F -d -l -a -w --quiet`, plus pkill's `-SIGNAL`/`--signal`, `-e` and `-H`. The selection rules follow procps' own rather than an approximation: `-v` inverts the *whole* verdict, so `pgrep -v -u root` lists the processes root does not own; `-x` anchors the pattern itself (`^(...)$`) so an alternation is anchored as a whole; a 0 in a `-s` or `-g` list means the caller's own session or process group; a pattern longer than the 15 characters `/proc` keeps draws the original's warning and then matches nothing; and pkill picks its `-SIGNAL` out of the command line before the options are parsed, the way procps' `signal_option` does, so `-o` keeps meaning oldest. All three shapes of procps command-line failure are reproduced with their exit status 2 — the `Try ... for more information.` line after a missing selection, the bare diagnostic after a bad option value, and the usage text after an unusable option _(run)_ |
| `printf` | all conversions but one | `%q`, the "ignoring excess arguments" warning | `%s %d %i %f %e %g %x %X %o %c %b %%`, widths, precision, `\x \0 \e` escapes, character constants and the numeric-operand diagnostics all match _(run)_ |
| `ps` | 49/58 | `-T`/`-L`/`-m` (threads), `--context`/`-M` (security labels), `--info`, `--forest` (accepted, but the listing is not drawn as a tree) | measured against procps-ng 4.0.6. The selection set is complete: `-p`/`-q` `--ppid` `-u`/`-U` `-g`/`-G` `-s` `-t` `-C` and `-N`/`--deselect`, all additive the way the original combines them, with `-g` taking a session id when it is a number and an effective group name otherwise. Formats: the BSD `a x u w A`, `-f`, `-F`, `-l`, `-j`, `-ly`, and `-o` over the full column set (`pid ppid pgid sid sess uid euid ruid gid egid rgid user ruser group rgroup comm ucmd cmd args stat s f c pri opri ni cls addr sz vsz rss %cpu %mem tty tname wchan psr nlwp thcount minflt majflt etime etimes time cputime bsdtime start bsdstart stime start_time lstart`), each accepting `name=HEADING`, where blanking every heading drops the header line. Also `--sort`/`-O`, `--no-headers`/`--headers`, `--cumulative` (which the original applies to the BSD TIME column alone) and the screen-size options, which change nothing because output is never truncated. The layout rules were derived from the original row by row: a column keeps the width its spec declares even when its own heading is longer (that is how `-l`'s one-character ADDR column works), a right-aligned column catches up to the grid after an oversized *number* but keeps its full width after an oversized *left*-aligned value such as `S<Lsl`, and the heading line always catches up. `ps aux`, `ps ax`, `ps -ef`, `ps -l` and `ps -j` are byte-identical over every process on the measurement host. Two deliberate differences remain: the default with no options still lists **every** process as `PID STAT COMMAND` where procps lists the caller's own terminal, and a command line's non-ASCII bytes are passed through where the original in a C locale renders them as `?` (it passes them through in a UTF-8 locale, which is what a terminal shows) _(run)_ |
| `readlink` | 8/8 | — | `-f -e -m` (the three canonicalize modes with their exact existence rules — `-f` allows a missing final component, `-e` none, `-m` anything), `-n`, `-z`, `-q/-s`, `-v` and multiple operands. GNU coreutils 9.11 prints **no** diagnostic on a canonicalization failure unless `-v` is given (only the exit status signals it); ba6 replicates that, and `-s`/`-v` keep the last-one-wins rule _(run)_ |
| `realpath` | 8/8 | — | default mode (all but the last component must exist), `-e`, `-m` (missing components resolved lexically), `-s` (no symlink expansion), `-L` (`..` resolved before symlinks), `-P`, `-q`, `-z`, `--relative-to` (relative to the file or directory named, via the original's filepath.Rel semantics) and `--relative-base` (relative only when inside the base) all match coreutils on symlink, missing-component and multi-operand cases _(run)_ |
| `renice` | 3/3 | — | `-n` `-p` `-g` `-u` and the legacy positional form; old/new priority reporting matches, including kernel clamping of out-of-range requests _(run)_ |
| `rm` | 8/10 | `-I` `--one-file-system` | `-rv` prints only the top directory, GNU prints every entry; `-i` prompt reads `remove 'I'?` vs GNU `remove regular empty file 'I'?` _(run)_ |
| `rmdir` | 3/3 | — | `-p` `-v`, the non-empty error and the refusal to remove a non-directory all match _(run)_ |
| `sed` | 15/17 | `-e`'s `--debug` trace, and `e` (the command that runs a shell command, which the seccomp filter would refuse anyway) | a program-counter-based interpreter, verified line by line against GNU sed 4.10. Commands: `{ }` blocks (nested), `a`/`i`/`c` in both the backslash-continued and one-line forms with real sed's escape handling, `y///`, `n`/`N` (with GNU's non-POSIX end-of-input rule), `D`/`P`, `h`/`H`/`g`/`G`/`x`, `b`/`t`/`T`/`:label`, `q`/`Q` with an exit code, `r`/`R`/`w`/`W`, `z`, `F`, `l` with its wrapping, `=`, and `s///` with `g`, `p`, `i`/`I`, `w file` and the occurrence number — where a bare number replaces only that match and a number with `g` replaces it and everything after. Addresses take a line number, `$`, `/REGEX/`, GNU's `first~step` form and two-address ranges, each negatable. Options: `-n -e -f -E/-r -i[SUFFIX] -s -z -l --posix`. `l`'s wrapping follows the original byte for byte, including the empty first line a width of one produces, because the column is checked before every escaped byte rather than every character. One difference remains: a script error names the problem without the original's `-e expression #N, char M:` prefix _(run)_ |
| `setsid` | 3/3 | — | `-c` `-f` `-w`, session/process-group identity and exit statuses match; `-f` forks via Go's exec rather than a raw fork+exec _(run)_ |
| `sha256sum` | 6/10 | `--tag` `-z` `--ignore-missing` `--strict` `-w` | `-c` verification, `-` stdin, the `-b` binary marker and the rejection of `--quiet`/`--status` outside `-c` all match _(run)_ |
| `sort` | 20/27 | `-R`/`--random-sort` `--random-source` `-S`/`--buffer-size` `-T` `--parallel` `--batch-size` `--compress-program` `--debug` `--files0-from` `-V` | `-k` field keys with per-key modifiers (`bdfgiMnr`), `-t`, `-n -g -h -M -f -d -i -b -r -u -s -c -C -o -z`, and `-m` (every input is read and ordered, so a merge of sorted inputs is identical). The field rules match GNU's: without `-t` a field starts at the first blank of the run preceding it, with `-t` the separator belongs to neither neighbour, and a `.C` offset counts from there. The last-resort whole-line comparison is applied unless `-s` or `-u` is given, a global `-r` reverses it while a key's own `r` does not, and a key with no modifiers of its own inherits the global ones. Verified against GNU on 400 randomized inputs across 31 option sets, plus the `-c` disorder message and `-o` _(run)_ |
| `stat` | `-c` near-complete | `-f` `-t` `--printf`, and `%d %t %T %w %m %C` | **default (no `-c`) layout differs**: quotes the name, no column alignment, `Device: 42` vs `0,42`, no `Birth:` line. `%N` quotes with `"` instead of `'` _(run)_ |
| `strings` | 12/13 | `-U`/`--unicode` handling, and `-T`/`--target` is accepted but only the native format is read | diffed against GNU binutils 2.47 over every option combination on plain data, on stripped and unstripped ELF executables and shared objects: `-a -d -f -n -t -o -w -e -s`, the clustered and attached forms getopt accepts (`-at x`, `-n8`, the historic `-8`), and the error wording for a missing file, an unreadable one and a directory. Two details that are easy to get wrong both match: a wide encoding is retried one *byte* later rather than at the next character boundary, so a UTF-16 string at an odd offset is still found; and `-d` keeps every allocated section, code included — BFD's "loaded data" drops the sections a loader never maps, not the instructions _(run)_ |
| `sync` | 0/2 | `-d` `-f`, and **file operands** | bare `sync` is identical; `sync PATH` is rejected _(run)_ |
| `sysctl` | 6/17 | `-p`/`--load` `--system` `-r`/`--pattern` `-q` `-b` `--deprecated` | `-a`, `-n`, `-N`, `-e`, `-w` and bare reads. `sysctl -a` matches procps key for key and value for value (1590 keys on the measurement host) with identical stderr, including the write-only, permission-denied, empty-file, multi-line and deprecated-key rules _(run)_ |
| `tail` | 5/12 | `-F` `--retry` `--pid` `-s` `-z` `--max-unchanged-stats` | `-n` `-c` `-n +N` `-c +N` `-q` `-v` `-f` match _(run)_ |
| `tar` | 24/154 | appending to an existing archive (`-r -u -A --delete`), `--owner`/`--group`, `-S`/`--sparse`, `--wildcards` control, and the many format and device options | **format-compatible both ways** with GNU tar 1.35 in all four codecs: ba6 reads real `.tar`, `.tar.gz`, `.tar.bz2`, `.tar.xz` and `.tar.zst`, and GNU reads ba6's. Present: `-c -x -t -f -C -z -j -J --zstd -v -k --overwrite -O -P -p --numeric-owner --strip-components --exclude -T`, and naming members on `-x`/`-t` now selects them, a directory bringing its contents. Diffed against the original over the same tree in ~20 invocations: the `-tv` long listing down to its 19-column user/group/size field, the readdir order members are stored and listed in, `--exclude`'s fnmatch semantics where `*` crosses a slash and a directory whose contents are excluded still appears, the `Removing leading \`/' from member names` warning, and the three failure shapes — `NAME: Not found in archive` and `Cannot open: File exists` with the run carrying on to exit 2, and `Cannot open:` plus `Error is not recoverable: exiting now` for an archive that will not open. The xz and zstandard writers store rather than compress, so an archive written with `-J` or `--zstd` is a valid but uncompressed stream of that format _(run)_ |
| `tee` | 4/4 | — | `-a` `-i` `-p` and the four `--output-error` modes present, output identical _(run)_ |
| `timeout` | 2/5 | `-f` `-p` `-v` | `-s` `-k` and the 124 exit code match _(run)_ |
| `top` | common display paths | configuration files, alternate windows, field-layout editor, colour mapping, kill/renice prompts, and task-area scrolling | Provides the five standard summary lines; procps-style task columns; batch and terminal modes; `-b -n -d -p -u/-U -o/-O -c -H -i -S -E -e -w -1`; and basic live keys for sorting and view toggles. Dynamic CPU percentages use adjacent `/proc` snapshots rather than lifetime averages. In raw terminal mode, each rendered row ends in CRLF so the process table remains column-aligned. |
| `uniq` | 10/11 | — | `-c -d -u -i -f -s -w -D --group -z` all present; the field/char skipping and `-w` truncation follow GNU's skipfield exactly (the blanks after a skipped field stay compared), and the `-D`/`--all-repeated` (`none`/`prepend`/`separate`) and `--group` (`separate`/`prepend`/`append`/`both`) blank-line placement was diffed byte for byte, including the quirks: `-D -u` prints one line per group, `-D -c` is rejected as `meaningless`, `--group` refuses to combine with `-c/-d/-D/-u` _(run)_ |
| `uptime` | 4/4 | — | `-p` (decades/years/weeks/days/hours/minutes with singular/plural units), `-s` (boot time as `yyyy-mm-dd HH:MM:SS`), `-r` (boot epoch, uptime with six decimals, user count, three load averages) and `-c` (CLOCK_BOOTTIME minus pid 1's start) all present, and the default line now carries the user count with procps' exact `, %2d users?,  ` spacing and `%2d:%02d` uptime layout — byte-identical under C locale. The user count comes from `/run/systemd/sessions` `CLASS=user` entries when systemd runs, else from utmp records; both match procps' own `sd_get_sessions`-then-utmp order _(run)_ |
| `wc` | 4/8 | `-L` `--total` `--files0-from` `--debug` | counts, column widths and the `total` line are byte-identical, including stdin and the unpadded single-count form _(run)_ |
| `which` | 1/10 | the `--skip-*`/`--show-*` family | found-path output identical; on a miss GNU prints `no X in (PATH)` to stderr, ba6 prints nothing (exit 1 either way) _(run)_ |
| `zip` / `unzip` | 8/175 and 14/35 | zip's update, delete and move modes (`-u -d -m`), encryption, split archives and the wide option surface; unzip's `-Z` info mode, `-a`/`-b` text conversion, `-C` case-insensitive matching and the overwrite prompt | both interoperate with Info-ZIP 6.00 in either direction, and the output was diffed invocation by invocation. unzip: `-l -v -t -p -c -d -j -o -n -q/-qq -x` and member patterns, whose globs let `*` cross a slash as Info-ZIP's do; the `-l` and `-v` tables, the `testing: NAME   OK` lines and their summary, the `creating:`/`extracting:`/`inflating:` verbs with the name padded the way the original pads it, and the statuses it reserves — 9 for an archive it cannot open, 11 for a pattern nothing matched, with the `caution: filename not matched` line. Without `-o` an existing file is kept rather than prompted for, since there is no terminal to ask at. zip: `-r -j -0 -1`…`-9 -q -x` with the short options clustering, members written in whichever of the stored and deflated forms is smaller — and stored outright for the `.Z .zip .zoo .arc .lzh .arj` suffixes the original never tries — so the `adding: NAME (method N%)` lines match. Extraction rejects paths that escape the destination _(run)_ |
| `xargs` | 14/14 | — | every option group present: `-0 -a -d -E -e -I -L -n -p -P -r -s -t -x --process-slot-var`. Input splitting, quoting, `-I` substitution, `-s` line-length capping, `-x`'s exact `argument line too long` wording, `-P` concurrency and `-L` line batching (blank lines skipped, a trailing blank continues a line, quoting honoured within a line) all byte-identical to GNU findutils, including the `--max-lines`/`--max-args`/`--replace` mutual-exclusion warnings and their last-option-wins rule, and `--process-slot-var`'s 0-based slot numbering. Quotes and backslash escapes do not span physical lines under `-L` the way they do without it _(run)_ |

### Tier C — partial

**`diff`** _(run, vs GNU diffutils 3.10/3.12)_ — 15/48 options. Default output is
GNU diff's classic format (`NcN`/`NaN`/`NdN` headers, `<`/`>`/`---`). Present, diffed
line-for-line across single-line, pure-insert, pure-delete, replace-block,
identical-file, and boundary (insert/delete at the very first or last line) cases:
default and `-u`/`--unified` (real hunk splitting — up to 3 lines of context, hunks
within 6 lines of each other merged into one — and file modification-time headers),
`-q`/`--brief`, `-s`/`--report-identical-files`, `-i`/`--ignore-case`,
`-w`/`--ignore-all-space`, `-b`/`--ignore-space-change` (collapses a run of
whitespace to one space and drops trailing whitespace, but a run where the other
side has none is still a difference — a real, non-obvious asymmetry in GNU diff's
own `-b`), `-N`/`--new-file` (a missing operand reads as empty instead of erroring),
and `--label` (one or two, suppressing that side's timestamp rather than appending
one). A missing trailing newline is treated as a real difference and reported with
`\ No newline at end of file`, even when the visible line text is otherwise
identical on both sides — including the case where *both* files lack a final
newline with matching text, which correctly reports no difference at all. Missing:
normal format has no context diff (`-c`) or side-by-side (`-y`) alternative, `-r`
(recursive directory diff), `-B`, `-E`/`-Z`, `-a`, `-X`/`-x`, `--color`.

**`sh`** _(run, vs bash)_ — checked line by line across every construct below.
Present: `if`/`then`/`elif`/`else`/`fi`, `for VAR in LIST; do ... done` (an unquoted
list item's expansion is field-split on whitespace before iterating — `for f in
$(echo a b c)` visits three items, not one — while a quoted item or quoted `"$var"`
stays one iteration value, matching bash for both cases), `while ... do ... done`,
`break` and `continue` (correctly stopping only the *nearest* enclosing loop when
nested), `$(...)` command substitution (including nested and piped: `x=$(cmd1 |
cmd2)`, and inside double quotes), `$((...))` arithmetic expansion (`+ - * / %`,
unary `-`, parens, bare names read as variables, so `i=$((i+1))` works), pipelines,
`&&`/`||`, `;`, quoting, variable assignment/read-back, prefix assignments,
`$?`/`$$`/positional parameters, `cd`/`pwd`/`export`/`unset`/`read`/`exit`/`:`
builtins, and `<`/`>`/`>>`. Missing: `case`/`esac`, functions, subshells `( )`,
brace groups `{ }`, backticks (only the modern `$(...)` form works), globbing,
here-documents, `2>` redirection, `trap`, `set`, `local`, and multi-line control
structures typed interactively line-by-line (a `for`/`if`/`while` spanning several
lines works in a script or `-c` string, since the whole text parses at once, but the
interactive REPL still submits one line at a time with no continuation prompt). A
command substitution's own variable assignments are not isolated the way a real
subshell's are — a documented simplification, since the common uses of `$(...)` are
read-only queries (`$(hostname)`, `$(date)`), not stateful scripts.

**`awk`** _(run, vs gawk 5.4.1)_ — verified line by line across the
pattern/action/field engine. Present: `if`/`else`, `for` (both `for(init;cond;post)`
and `for (key in array)`), `while`, `do`/`while`, `break`, `continue`, associative
arrays (`a[x]=v`, `a[x]`, `delete a[x]`, whole-array `delete a`, and
multi-dimensional `a[i,j]` joined on `SUBSEP`), the `in` operator, string
concatenation by juxtaposition (`print "x=" x`), prefix/postfix `++`/`--`, range
patterns (`/a/,/b/`), and the builtins `split`, `match` (setting
`RSTART`/`RLENGTH`), `sprintf`, `index`, `length`, `substr`, `int`, `tolower`,
`toupper`. A real word-frequency program
(`{for(i=1;i<=NF;i++)count[$i]++} END{for(w in count)...}`) and FizzBuzz both run
and match gawk's output. Missing: user-defined functions, `getline`, output/input
redirection (`print > file`, `cmd | getline`), `ARGV`/`ENVIRON`, `(i,j) in array`
(only `a[i,j]` itself works), and gawk-specific extensions beyond POSIX.

**`file`** _(run, vs file 5.46)_ — real ELF introspection via Go's `debug/elf`.
Diffed against a dynamically-linked PIE executable (`/bin/ls`), a shared object
(`ld-linux-x86-64.so.2`), a statically-linked binary and its stripped copy, and
ba6's own binary — every case byte-identical, including class, data encoding, type
(with the `pie executable`/`shared object` distinction based on `PT_INTERP`),
architecture, ABI, interpreter path, the GNU and Go BuildID notes, the
`.note.ABI-tag` "for GNU/Linux X.Y.Z" line, and stripped/not-stripped (and `with
debug_info`) from the section table. Plain ASCII reads as `ASCII text`, matching
the original's own distinction from `Unicode text`; special files show
`(major/minor)`; directories report `setuid`/`setgid`/`sticky` state. Present:
`-b`, `-L`/`--dereference`, `-i`/`--mime`/`--mime-type` (a description-to-MIME
table covering every case this applet's own probe can produce, including the
`inode/*` types for non-regular files). Missing: `-z`, `-s`, `-f`, `-m`, and the
wider libmagic database (image/audio/font formats beyond the handful of magic
bytes already matched, non-x86 architecture names beyond the common set, core
files).

**`lsof`** — 3/15 _(run)_. `-n -P -p -i` present. Missing `-c -u -t -d -s -F -g -x -R -a`.

**`lsblk`** — 4/38 _(run)_. `-a -b -n -o` present. Columns are space-separated with no
padding and sizes round differently (`115M` vs `114,6M`); ordering differs. Missing
`-f -p -l -J -P -T -d -e -i -m -r -s -t -x -S -N -O`.

**`blkid`** — output matched the real tool on this system _(run)_; no options at all
(missing `-o -s -t -L -U -p -i -k -c`).

**`losetup`** — 7/22 _(run)_. `-a -d -f -o -r --show --sizelimit` present. `-a` output
omits the `[]:` back-file field. Missing `-D -c -j -L -P -b -J -l -O --direct-io`.

**`swapon` / `swapoff`** — 2/17 and 1/4 _(src)_. `-a -p` present. Missing `-s --show
-L -U -v -d -e -f -o`.

**`mkswap`** — 2/13 _(src)_. `-f -L` present. Missing `-U -p -c -s -v -o --lock`.

**`ss`** — 7/44 _(run)_. Addresses decode correctly, short options bundle, and
v6-only listeners render as `[::]` against `*` for dual-stack ones, matched against
the original socket by socket. Present: `-t -u -x -a -l -n -p` (`-n`/`-p` accepted
but ignored). No `Recv-Q`/`Send-Q` columns, no service-name resolution, no process
attribution, and the `Netid` column says `tcp6`/`udp6` where the original says `tcp`
and `udp` for both families. Missing `-s -e -m -i -o -r -f -K -H -Z --ipv4/--ipv6`.

**`netstat`** — 13/21 _(run, vs net-tools 2.10)_. Eight invocations diffed whole
come back **byte-identical**: `-tan`, `-uan`, `-tuwxan`, `-tulpn`, `-xl`, `-xlp`,
`-rn` and `-i` — column widths, section headings, state names, the `PID/Program
name` field taken from `argv[0]`, the not-root warning on stderr, and the `Flg`
letters of the interface table all match. Present: `-t -u -w -x -l -a -n -p -r -i`
plus `-e -v -W` accepted and ignored. **Names are never resolved**, so plain
`netstat` and `netstat -r` print numeric addresses and ports where the original
prints `HOST:https` and `_gateway`; only the default route is named. Missing `-s`
(per-protocol statistics), `-A`, `-g`, `-M`, `-C`, `-F`, `-c`, `-o`, and the IPv6
routing table.

**`tree`** — _(no reference)_: `tree(1)` is not installed on the measurement host, so
nothing here was diffed and the option list below is what ba6 accepts, not a
coverage ratio. Present: `-a -d -f -F -i -L -P -I -s -h -p -t -r -U -n -C
--dirsfirst --noreport`. Drawing, the `name -> target` form for symlinks, the
`[error opening dir]` marker on the directory's own line, and the closing
`N directories, M files` line follow the original's layout. Missing `-l` (follow
symlinks), `-x`, `-D`, `-u`/`-g`, `-J`/`-X`/`-H` (JSON, XML, HTML), `--du`,
`--prune`, `--filelimit`, `--timefmt`, `--charset`, `--matchdirs`, `--inodes`,
`--device`, `--sort=`, `-o`.

**`ip`** — objects `link`, `addr`, `route`, `neigh`, `rule` _(run)_. Objects and
commands accept **any unambiguous prefix**, resolved in the order iproute2
resolves them, so `ip r s`, `ip a s`, `ip n s`, `ip ru s` and `ip l sh` all list and
`ip l s eth0 up` sets — including the trap that `s` means `set` for `link` but
`show` for every other object. `route show` and `rule show` match closely;
`link show` omits `qdisc`, `mode`, `group`, `qlen` and orders flags differently;
`addr show` omits `valid_lft`/`preferred_lft` and reports a different state;
`neigh show` lists multicast NOARP entries the original filters out. Global options:
only `-4`/`-6` — **no `-br`, `-j`, `-s`, `-d`, `-o`, `-c`**.

**`iptables`** — 18/27 _(run)_. Works on the tables the system tool works on: the
nftables filter/nat/mangle/raw/security tables of iptables-nft, not a private one
of its own, so `-L` and `-S` report the ruleset that is actually filtering — user
chains, the jumps between them, reference counts and counters included. Listing
output is byte-identical to iptables 1.8.13 for the rules it decodes, in every
combination of `-v`, `-x`, `-n` and `--line-numbers`, and so is `-S`; a rule ba6
appends is read back by the original as the same rule. Commands `-A -D -F -L -P
-S`, and `-t` selects the table. Matches `-p -s -d -i -o -f --sport --dport
--icmp-type`, each negatable with `!`, and `-j`/`-g` targeting ACCEPT, DROP,
RETURN, QUEUE, REJECT (`--reject-with`) or a user chain. Rules **read back**
decode more than rules can be written with: `-m multiport`, `-m conntrack` and
`-m state`, `-m comment`, `-m limit`, and the LOG, SNAT, DNAT, MASQUERADE and
REDIRECT targets; an extension with no decoder prints its name rather than being
dropped, so a listed rule never looks broader than it is. Missing `-I -R -C -Z -N
-X -E -c --modprobe`, IPv6, and `-m` extensions when *writing* a rule. Without
`-n`, addresses resolve through `/etc/hosts` and ports and protocols through
`/etc/services` and `/etc/protocols`; there is no DNS fallback, because the
seccomp profile permits netlink and nothing else.

**`ping`** — 3/33 _(run)_. `-c -i -W -4 -6` present. **Needs `CAP_NET_RAW`/root** —
it opens a raw ICMP socket where iputils falls back to an unprivileged ICMP datagram
socket, so unprivileged `ping` fails with `socket: operation not permitted`. Missing
`-s -t -q -f -I -M -D -O -w -p -A -n`.

**`traceroute`** — `-4 -6 -m -n -q -w` _(src)_. Missing `-I -T -U -p -s -i -f -g -z -A`.

**`mtr`** — 14/38 _(src)_. Curses-style live display and `--report` both present, with
`-c -i -m -f -s -n -u -b -r -w -Z -H -N -P -R -4 -6 --icmp`. Missing `-T` (TCP) `-a`
`-Q` `-e` `-M` `-j`/`-x`/`-C`/`-l` (json/xml/csv/raw output) `-o` `-y` `-z` `-U` `-E` `-L` `-B` `-G`.

**`nc`** — 4/34 _(run)_. `-l -p -u -w` present; a `ba6 nc -l -p PORT` listener accepted
a connection from the real `nc` and received the payload intact. Missing `-z -v -n -k
-e -U -4 -6 -s -q -N -C -D -x/-X`.

**`nslookup`** — _(run)_ prints only the `Name:`/`Address:` lines; missing the
`Server:`/`Address:` header and `Non-authoritative answer:` block, and the
`type=`/`class=`/`port=` operands.

**`curl`** — 15 of the common set _(run)_. Present: `-o -O -s -v -i -I -L -f -k -A
-u -X -d -H --max-time`, clustered short flags, and HTTPS. `-O` means "name the
file after the remote path" and takes no argument, as in the original; exit status
is 0 on an HTTP error unless `-f` is given, and 22 with it. Missing `-T` (upload),
`--compressed`, `--resolve`, `-b`/`-c` (cookies), and there is still no progress
meter.

**`wget`** — 20 of wget's own options _(run)_. Has its own command line, independent
of curl's. Present: `-O -P -c -nc -q -nv -v -d -S -T -t -U --user --password --header
--method --post-data --post-file --spider --no-check-certificate --max-redirect`,
several URLs per invocation, redirects followed by default, the derived filename with
`.1`/`.2` uniquifying, resume by range request, and exit status 8 on a server error
response — all verified against GNU wget. Missing the recursive mirror (`-r -m -np
-l -k -N`), rate limiting, cookies, `-i`, FTP and WARC. The progress display is ba6's
own, not wget's dotted meter.

**`iftop`** — 6/17 _(src)_. `-i -n -N -P -s -t` present. Missing `-p -b -B -f -F -l
-c -m -o -L -G`.

**`modprobe` / `insmod` / `rmmod` / `lsmod`** — 3/27, 0/3, 1/3, 0/2 _(src, run)_.
Load/unload work; `lsmod` output matches except one column of padding. Missing
`modprobe -a -D -c -n -R --show-depends --first-time -b`, `insmod -f -s -v`,
`rmmod -s -v`, `lsmod -s -v`.

**`fdisk` / `sfdisk`** — read-only `-l` and DOS-only write support, **by design**
(documented in help). `fdisk` 1/18, `sfdisk` 6/41. `fdisk` itself has no
interactive editor; the separately documented `cfdisk` applet provides the bounded
DOS/MBR and GPT terminal editor. `sfdisk` has no GPT write path. **On-disk output
is correct** _(run)_: a table written by `ba6 sfdisk` is read back identically by
real `fdisk -l` and `sfdisk --dump`. The *printed* layout differs —
ba6's `fdisk -l` has no column alignment and omits the `Disk model`, `Units`,
`Sector size` and `Disklabel type` lines; ba6's `sfdisk --dump` omits `label-id`,
`device` and `sector-size` and pads columns differently.

**`mkfs.*` / `fsck.*`** — **by design** each formatter writes one fixed profile and
each checker is read-only; that is stated in the help text and is a deliberate safety
constraint, not an oversight. Consequence for compatibility: `-b -I -N -O -U -m -i -c
-q -v -n -j -E` (mke2fs) and `-d -m -n -s -O -r -K` (mkfs.btrfs/xfs) are absent, and
`fsck` never repairs (`-y -r -c -D -b -B -j -l -L` absent).

**The images themselves are sound** _(run)_ — this is where ba6 scores far better than
its flag count suggests. Freshly created images pass the real vendor checkers with no
complaints:

| ba6 command | validated with | result |
|---|---|---|
| `mkfs.ext2 -F 64M` | `e2fsck -fn` | clean, `11/1024 files, 38/16384 blocks` |
| `mkfs.ext4 -F 64M` | `e2fsck -fn` | clean, `11/1024 files, 1094/16384 blocks` |
| `mkfs.xfs -f 400M` | `xfs_repair -n` | all 7 phases clean |
| `mkfs.btrfs -f 200M` | `btrfs check` | no errors |
| `sfdisk` DOS table | `fdisk -l`, `sfdisk --dump` | read back byte-for-byte |
| `cfdisk` DOS table | `fdisk -l`, `sfdisk --dump` | confirmed write read back correctly; its `u` dump replays to a byte-identical MBR |
| `cfdisk` GPT table | real `fdisk -l`, `sfdisk --verify` | interactive GPT creation and type change read as Linux swap; both headers and 128-entry arrays validate |

`fsck.ext4` also validates a `mke2fs`-produced image correctly — but only when its
flags are given separately (`-f -n`, not `-fn`), and it identifies itself as
`fsck.ext2` in every diagnostic regardless of the name it was invoked under.

**`login` / `passwd`** — no options _(src)_; `login` is missing `-p -f -h`, `passwd`
is missing the whole administrative set (`-l -u -d -e -S -n -x -w -i -a -R -s`).

**`useradd` / `adduser`** — 8/27 options _(run, against a real account database on
a disposable VM)_. Present: `-u` `-g` `-G` `-d` `-s` `-c` `-m` `-M`. The resulting
`/etc/passwd`, `/etc/shadow` and `/etc/group` lines match shadow-utils, as do the
home-directory creation, the private-group default, and the exit statuses and
message wording for every failure path tested — 9 for a name already in use, 4 for
a UID already taken, 6 for a missing group, 19 for an invalid user name. Missing:
`-r`/`--system`, `-o`/`--non-unique`, `-e`/`--expiredate`, `-f`/`--inactive`,
`-k`/`--skel`, `-p`/`--password`, `-D`/`--defaults`, `-b`/`--base-dir`,
`-R`/`--root`, `-N`/`-U` group control, and the SELinux and subid options. The one
wording difference left is deliberate: the original's invalid-name message ends
`: use --badname to ignore`, naming a flag ba6 does not implement. `adduser` is
the same implementation, plus the two-operand `adduser USER GROUP` form; it is not
Debian's interactive Perl `adduser` and asks no questions.

**`cpio`** — 10/61 options _(run)_. `-o` `-i` `-t` `-v` `-F` `-H newc` and the
`-d`/`--make-directories` behaviour; archives round-trip against GNU cpio in both
directions. Only the `newc` format is implemented, so the `-H` variants (`odc`,
`crc`, the binary formats) and `-p`/`--pass-through` are absent, along with
`-a` `-m` `-u` `-l` `--sparse` and the rename and pattern-file options.
Extraction rejects paths that escape the destination.

**`xz` / `unxz` / `zstd` / `unzstd`** — 5/62 and 1/21 options _(run)_. The
decoders are complete: `unxz` implements the LZMA range coder, the full LZMA2
chunk layer, every integrity check XZ defines (none/CRC32/CRC64/SHA-256),
multi-block streams and concatenated streams; `unzstd` implements the reversed
bitstream, FSE and Huffman entropy stages, the sequence and repeat-offset
rules, the sliding window, skippable and concatenated frames, and verifies the
frame's XXH64. Both were checked against the vendor tools over ~630 randomised
archives spanning every preset, all four XZ check types, `--block-size`,
threading, and hand-picked `lc`/`lp`/`pb` and dictionary settings, plus every
`.xz` and `.zst` file installed on two machines (kernel modules and firmware
among them) and Go's adversarial zstd fuzz corpus — all byte-identical, with
corrupted and truncated inputs rejected as the originals reject them. What
remains missing is the *encoder* side and the option surface: ba6 writes stored
blocks rather than compressing, and has no `-t`/`--test`, `-l`/`--list`,
compression levels, threading, or filter-chain options. The XZ BCJ and delta
filters are also unimplemented, so a stream using them is refused rather than
mis-decoded. Zstandard dictionaries are not supported; a frame carrying a
nonzero dictionary id is refused, though a present-but-zero id decodes
normally.

**`watch`** — 2/16 options _(run, under a pseudo-terminal on a disposable VM)_.
`-n`/`--interval` and `-t`/`--no-title`, the alternate-screen clear-and-redraw
cycle, and the header layout — interval and command on the left, `host: ctime`
against the right edge, left side clipped rather than wrapped — are byte-identical
to procps at an 80-column width. Missing: `-d`/`--differences` highlighting,
`-g`/`--chgexit`, `-q`/`--equexit`, `-e`/`--errexit`, `-b`/`--beep`,
`-c`/`--color`, `-x`/`--exec`, `-p`/`--precise` and `-w`/`--no-wrap`. The command
is run directly rather than through `sh -c`, so shell syntax in the command needs
an explicit `sh -c`.

**`getty`** — 10/34 options against `agetty --help` (util-linux 2.42.2), counted by
hand rather than by the automated man-page comparison behind the other rows: the
real tool on the measurement host is only installed as `agetty`, and the
comparison script matches ba6 applet names to their originals 1:1. Present: `-8`
`-a` `-i` `-J` `-l` `-L` `-n` `-p` `-r` `-t`. Missing: modem/dial-up-era options
(`-m`/`--extract-baud`, `-s`/`--keep-baud`, `-w`/`--wait-cr`, `-R`/`--hangup`,
`-I`/`--init-string`), telnet-style remote-host spoofing (`-E`, `-H`), issue-file
selection and cosmetics (`-f`, `--show-issue`, `-N`, `--nohints`, `--nohostname`,
`--long-hostname`), `-c`/`--noreset`, `-h`/`--flow-control`, `-o`/`--login-options`,
`-U`/`--detect-case`, `--erase-chars`, `--kill-chars`, `--chdir`, `--delay`,
`--nice`, `--reload`, and `--list-speeds`. Requires root and a real terminal
device, so it was validated end to end on a disposable VM rather than in the
regular test suite _(run)_: opening a real pty, claiming it as the controlling
terminal, applying a baud rate and CLOCAL, clearing the screen, expanding
`/etc/issue`'s `\n`/`\l` escapes, reading a login name (and separately confirming
`-a`/`--autologin` skips that prompt), and handing off to a real `login` that
authenticated an actual `/etc/shadow` SHA-512 entry and landed in that account's
shell with the correct uid/gid. The `\s`/`\r`/`\v`/`\m`/`\d`/`\t` issue escapes and
all option parsing are covered by unit tests instead, since they need no
privilege or terminal.

**`nano`** _(run, driven under a pseudo-terminal and checked screen by screen —
cursor-position escapes rendered by a small VT100 interpreter, matched against real
GNU nano 9.2's own responses to the same keystrokes)_ — 6/50 command-line options
(`cmdNano` takes only a filename; `+LINE`, `--tabsize` and the rest are not
implemented). The in-editor key map matches GNU nano's own bindings — command-line
flags were never nano's main interface, its in-editor keys are. Present: `^O` Write
Out (prompts "File Name to Write: " seeded with the current name, matching real
nano), `^X` Exit (prompting "Save modified buffer?" with Y/N/^C when there are
unsaved changes, verified for all three answers — discard, save-and-exit, and
cancel-the-exit), `^K` Cut and `^U` Paste (single-line only — real nano accumulates
consecutive `^K`s into one multi-line cut, which is a documented simplification
here), `^F` Search (seeded with the last search so pressing Enter alone repeats it,
wrapping around the whole buffer), `^\` Replace (prompts for the search and
replacement text and replaces every occurrence at once, rather than real nano's
per-instance Y/N/A confirmation — a deliberate simplification favouring a bounded,
easy-to-reason-about operation in a recovery tool over an interactive loop that
risks hanging), `^_`/`^/` Go To Line (with an optional `,column`, clamped to the
buffer's actual bounds rather than erroring on an out-of-range line), `^C` show
cursor position, and `^G` a one-line help reminder. Missing: syntax highlighting,
multi-buffer support, undo/redo, soft-wrapping, `M-B` backward search, mouse
support, and most of nano's `-`/`--` options (line/column positioning via `+LINE`,
`--tabsize`, etc.).

**`dig`** _(run, vs BIND 9.20.26 against a live resolver, plus a synthetic
local-UDP-server test harness for NXDOMAIN/reverse-lookup/error cases)_ — the full
multi-section default report, not a one-line summary: `;; ->>HEADER<<-` status and
id, the `qr`/`aa`/`tc`/`rd`/`ra`/`ad`/`cd` flags line (only the bits actually set, in
dig's own order) with real QUERY/ANSWER/AUTHORITY/ADDITIONAL counts read from the
header, `;; QUESTION SECTION:` and `;; ANSWER SECTION:` in the same tab-stop-aligned
columns `host`'s `-v` dump already uses, `;; AUTHORITY SECTION:` (the SOA an NXDOMAIN
returns), and the `;; Query time:` / `;; SERVER:` / `;; WHEN:` / `;; MSG SIZE  rcvd:`
footer. Present: `-x` (reverse lookup, building the `in-addr.arpa`/`ip6.arpa` name),
`-t` and `-c` (IN only) alongside the existing bare-type-operand form, `-p`,
`+short`, `+noall`/`+question`/`+noquestion`/`+answer`/`+noanswer`/`+comments`/
`+nocomments`/`+stats`/`+nostats` (enough for the common `+noall +answer` scripting
idiom, which also correctly suppresses the section headers, not just the other
sections), and `+time=`/`+tcp`. Exit status matches dig's own convention: 0 for any
answer actually received (including NXDOMAIN — dig's exit code reflects whether the
query was answered, not whether the name resolved) and 9 for a query that reached no
server, with the same `;; communications error to ADDR#port` / `;; no servers could
be reached` narration `host` uses. **By design, ba6's query never attaches an EDNS
OPT record** (every response behaves as if `+noedns` were given), so `ADDITIONAL` is
always the server's own count minus any OPT record and there is no `OPT
PSEUDOSECTION`; a resolver that only sets the `ad` (DNSSEC authenticated) flag on
EDNS-signalled queries won't set it here either — a real, one-line difference from a
bare `dig` invocation, not a bug. Missing: `-b -f -q -y`, `+dnssec` and every
DNSSEC-related option, the `; <<>> DiG VERSION <<>>` command-line banner and
`+cmd`'s `global options:` line (ba6 isn't claiming to be a specific dig build),
zone transfers, and most other `+option`s.

**`cfdisk`** _(run, vs util-linux 2.42.2)_ — a terminal partition editor.
Recognizes util-linux's seven option groups: `-L`/`--color`, `--lock`,
`-r`/`--read-only`, `-b`/`--sector-size`, `-z`/`--zero`, `-h`/`--help`, and
`-V`/`--version`. A pseudo-terminal session exercises create, delete, resize, sort,
type, boot flag, extra information, dump, confirmed write, and quit for DOS, and
separately GPT (creating a partition, changing its type to swap, and writing it) —
real `fdisk -l` and `sfdisk --verify` confirm both. Image tests cover construction,
readback, type parsing, rendering, primary and backup header/entry-array checksums,
and stale-target rejection. The GPT writer installs its backup array/header before
the primary copy, then the protective MBR. Proposed layouts are overlap- and
bounds-checked, stale images and mounted/active-swap targets are rejected before a
write, and writes preserve MBR boot code outside the partition entries. On an
unlabelled disk, a selector presents GPT and DOS only, with GPT selected by
default. Up/Down selects a partition or label, and Left/Right plus Enter navigates
the New/Quit/Help/Write/Dump action bar.

DOS extended/logical partitions are supported: `t` on a primary sets its type to
`5`/`f`/`85` to make it the disk's one extended container, `n` on the free space
inside it adds a logical partition (numbered from 5, as real fdisk does), and
`d`/`r`/`t`/`b` work on a selected logical partition the same way they do on a
primary. Deleting the extended partition drops every logical partition inside it;
retyping it away while it still holds any is refused. The boot-record chain layout
— one boot record per logical partition, its own entry's start relative to its own
LBA, its link entry's start relative to the extended partition's own start —
matches the layout util-linux's own fdisk writes sector-for-sector
(`cfdiskBuildEBRChain`'s tests check its output against those exact numbers),
verified bidirectionally: a table ba6 built with a primary, an extended partition
and two logical partitions is read correctly by real `fdisk -l` and `sfdisk
--verify`, and a table real fdisk built is read back correctly by ba6's own
cfdisk. DOS is still one label instance: only one extended partition is allowed,
and a logical partition cannot itself be extended.

This is **not** a general replacement for cfdisk: DOS is 512-byte-sector with four
primary partitions (one of which may be extended, holding any number of logical
partitions), and GPT is conventional-geometry only (128-entry, 128-byte-entry,
primary entries at LBAs 2–33, with a mirrored backup array immediately before the
final backup header). GPT `t` handles `linux`, `swap`, `efi`, or an explicit GUID
and preserves partition names and attributes; it has no MBR boot flag. SGI and SUN
labels, nonstandard GPT geometry, cfdisk script input, the full curses menu/type UI,
and colour themes are absent. `u` emits a GPT sfdisk-style dump for inspection or
compatible external tooling; ba6 `sfdisk` replays DOS only (primary and logical
partitions alike). `--color=never` disables reverse-video styling and
`--sector-size` accepts 512 only. The screen layout and key presentation
intentionally differ from util-linux's curses interface.

**`ncdu`** _(run, vs ncdu 2.9.2)_ — 8/37 options. The scan and the browser are
there: the same nine-column size field, the same bar width (`columns / 7`) drawn
against the largest entry in the directory, the `/..` row, the reverse-video header
and footer, and the `*` that marks whether the totals are disk usage or apparent
size. Compared screen against screen under a pseudo-terminal, the listing and both
totals match. Keys present: arrows/`jkhl`, enter, `n`, `s`, `a`, `?`, `q`. Options
present: `-x`, `--apparent-size`, `--exclude`, `--si`, `-o`/`--output`, `-f`, with
`-r`, `-q` and `-0/-1/-2` accepted. `-o` writes the same
`[major, minor, {metadata}, [rootObj, ...children]]` export ncdu itself uses — a
directory is a JSON array whose first element is its own info object followed by
its children, a file is a plain object, `dsize` is omitted when it equals `asize`,
and `dev` appears only on the root — verified by exporting a scanned tree and
re-importing it with real ncdu's `-f`, and by importing an export written by real
ncdu. `-f` browses a saved export instead of scanning; because the export schema
carries no field for a directory's own dirent overhead, a directory's size on
import is always the sum of its children, same as ncdu's own `-f` reader.
**Deliberately absent: file deletion, the shell escape and directory refresh** —
the header says `[readonly]`, as ncdu's own `-r` does. Also missing extended mode
`-e`, `--exclude-from`, `--exclude-caches`, `--exclude-kernfs`, `-L`, `-t`, the
display toggles (`--show-itemcount`, `--show-mtime`, `--show-graph`,
`--show-percent`, `--group-directories-first`, `--sort`), and the config file.

**`host`** _(run, vs BIND 9.20.26)_ — 18/19 option groups, and every invocation
tested matches byte for byte. Present: `-t -c -p -R -W -w -T -U -4 -6 -r -s -a -d
-v`, the default A/AAAA/MX/HTTPS question set, the address-to-`in-addr.arpa`/
`ip6.arpa` conversion, the `Using domain server:` block once an answer arrives,
`Host NAME not found: 3(NXDOMAIN)` with status 1, `NAME has no TYPE record` for an
explicit `-t`, the per-server `;; communications error to ADDR#port` narration
followed by `;; no servers could be reached`, and the `-v` dump down to dig's
tab-stop columns and its `Received N bytes from ADDR#port in N ms` footer. Record
wording covers A, AAAA, CNAME, MX, NS, PTR, TXT, SOA, SRV, CAA and the RFC 9460
HTTPS/SVCB presentation form. Missing `-l` (AXFR), `-C`, `-A`, `-N` and the
resolv.conf search list, `-m`, `-k` and `-V`; `-s` is accepted but redundant,
because one server is asked at a time.

**`less`** _(run, vs less 704)_ — 41/107 option groups and the everyday command
set, compared screen by screen under a pseudo-terminal. Present: `-N -n -S -i -I
-F -e -E -X -s -r -R -m -M -p -x -z`, `+command`, and `--` plus the long
spellings of those; `-a -A -c -C -d -f -g -G -J -K -L -q -Q -u -U -w -W -~` are
accepted without effect. Keys present: SPACE/`f`/PgDn, `b`/PgUp, ENTER/`j`/`e`/
DOWN, `y`/`k`/UP, `d`, `u`, `g`/`<`/HOME, `G`/`>`/END, `p`/`%`, LEFT/RIGHT,
`/`, `?`, `n`, `N`, `=`, `h`, `r`, `-` for the toggles, `:n`/`:p`/`:q`, `q`, and
a numeric count before any of them. Status lines, the `=` report, byte-measured
percentages, wrapping, `-S` chopping, tab expansion and caret notation all
match. Each file is read into memory instead of streamed, so a file larger than
memory is out of reach and `F` (follow) is absent, as are marks, bracket
matching, `-P` prompts, the `LESS` variable, lesskey files, `-b -h -j -k -o -O
-t -T -y -D -#`, and — by design, since this binary starts no child processes —
the `v`, `!` and `|` commands and `LESSOPEN` filters.

**`dmesg`** _(run, vs util-linux 2.41 on a live kernel ring buffer)_ — 20/30 option
groups. Present and verified byte-for-byte against a ~570-line real kernel ring
buffer, apart from one gap noted below: the default view (strips the `<PRI>` prefix
real dmesg also hides), `-r`/`--raw` (keeps it), `-t`/`--notime`, `-x`/`--decode`
(`kern  :info  : ` style facility/level prefixes), `-k`/`--kernel`,
`-u`/`--userspace`, `-l`/`--level` (including the `err+` "and more severe" suffix),
`-f`/`--facility`, `-c`/`--read-clear`, `-C`/`--clear`, `-s`/`--buffer-size`,
`-F`/`--file` (reads the same `<PRI>[sec.usec] text` format from an arbitrary file
instead of the kernel buffer — useful for testing without root), and
`--since`/`--until` (absolute timestamps or `"N unit(s) ago"`). `-T`/`--ctime` and
`--time-format iso` reconstruct wall-clock time from `CLOCK_MONOTONIC`, matched to
the real tool's own output down to the microsecond, day-name/month-name spelling
aside (the project's standing C-locale-only gap). `-n`/`--console-level`,
`-D`/`--console-off` and `-E`/`--console-on` drive the same `syslog(2)` console
actions dmesg does, verified against `/proc/sys/kernel/printk` and a `strace` of the
real tool's own syscall. `-p`/`--force-prefix`, `-S`/`--syslog`, `-P`/`--nopager`
and `--noescape` are accepted as no-ops (ba6 already behaves as they'd request: no
pager, no colour, no escaping). Known gap: ba6 reads the legacy `syslog(2)` ring
buffer rather than `/dev/kmsg`, so a multi-line `KERN_CONT` kernel message reprints
its own `<PRI>[timestamp]` on the continuation line instead of the blank-padded
alignment `/dev/kmsg`-backed dmesg produces. Missing: `-H`/`--human`,
`-e`/`--reltime`, `-d`/`--show-delta`, `-w`/`--follow`, `-W`/`--follow-new`,
`-J`/`--json`, `-L`/`--color`, `-K`/`--kmsg-file`, `--time-format delta`.

### Tier D — narrow subset

_(none)_

## What to fix first

Ordered by how many applets each item moves, not by effort.

1. **`find -exec`/`-ok`** — every other predicate and action is now in place,
   but running a command per result is what a script reaches for `find` to do.
   It is the one gap here that is a *policy* question rather than work: the
   seccomp denylist kills `execve`, so `find` would have to join the applets
   listed in `appletNeedsUnrestrictedSyscalls` (as `xargs`, `nice`, `nohup` and
   `setsid` already are) and give up its own filter. Until then `find -print0 |
   xargs -0` covers the same ground with the exemption confined to `xargs`.
2. **`ss` `Recv-Q`/`Send-Q`** — the netlink query added for `IPV6_V6ONLY` already
   returns `idiag_rqueue` and `idiag_wqueue`; only the columns are missing.
3. **Compression for `xz` and `zstd`.** Both now *decode* the real formats in
   full, but still write stored blocks, so `ba6 xz file` produces a valid but
   much larger archive than the original would. This is the remaining half of
   the job and matters far less than decoding did.
4. **`watch -d` and `-g`** — change highlighting and exit-on-change are most of
   why `watch` gets reached for interactively.

## How this was measured

Three independent, cross-checked techniques:

1. **Reference option sets** — parsed from the real tools' `--help` on this
   machine (falling back to `man` where `--help` is thin), grouping synonyms so
   `-a, --all` counts once. Versions: coreutils 9.11, util-linux 2.42.2, GNU grep
   3.12 (`/usr/bin/grep`, not the `ugrep` alias on this shell), GNU sed 4.10, gawk
   5.4.1, GNU tar 1.35, findutils 4.11.0, diffutils 3.12, procps-ng 4.0.6,
   iproute2 7.1.0, iptables 1.8.13, file 5.48.
2. **ba6 option sets** — extracted from the Go source by brace-matching every
   `func cmd*` body, following the call graph, and collecting flag literals and
   option runes, then verified empirically by running the built binary with each
   candidate flag and classifying accept/reject. Source extraction agrees with
   empirical probing except for `switch` blocks on runes used for something other
   than options (escape sequences in `tr`, format directives in `date`/`stat`,
   `sed` command letters) — those applets are resolved by hand.
3. **Behavioural diffs** — side-by-side invocations of ba6 and the original on
   identical inputs in a scratch directory, comparing stdout, stderr and exit
   code. This is where every defect in this document's history was found; flag
   counts alone would show `tr` at 100% and never catch `rmdir` deleting files.

Applets whose output is not a fixed string need extra technique: `netstat` and
`ps` read live kernel state, so each invocation is diffed whole against the
original run seconds apart, with volatile rows (kernel workers, the shell's own
children) filtered out before counting differences. `ncdu` and the interactive
side of `ip` and `cfdisk` are driven under a pseudo-terminal (`script -qc`, with
`stty rows/cols` fixing the geometry), the escape sequences stripped, and the
resulting screen compared with the original's; `cfdisk`'s on-disk table and dump
round trip are additionally checked with `fdisk` and `sfdisk`. `tree` cannot be
measured this way because the package is not installed on the measurement host.
`iptables` needs `CAP_NET_ADMIN` rather than root as such, so it is diffed inside
an unprivileged network namespace (`unshare -rn`): a ruleset is built with the
original and listed with both tools, then built with ba6 and listed with both,
which catches a divergence in either direction without touching the host's own
firewall.

### Reading the percentages

A percentage is "option groups the original documents that ba6 also accepts". It is a
floor, not a grade: `ls` at 16% covers the flags people actually type, and `tar` at 5%
of 154 options still reads and writes real archives. Conversely a high percentage does
not mean drop-in — check the notes column. The applets where the metric is
meaningless (`sh`, `awk`, `sed`, `find`, `test`, `expr`, `printf`, `dd`, `ip`,
`iptables`, `ps`) are described by feature instead.

### What was not executed

Assessed from source and help text only, because they need root, a real device, a
terminal, or an irreversible action: `mount`/`umount` writes, `swapon`,
`swapoff`, `mkswap`, `blockdev`, `insmod`, `rmmod`, `modprobe`, `chroot`,
`switch_root`, `init`, `halt`, `reboot`, `poweroff`, `login`, `passwd`, `udhcpc`,
`iftop`, `traceroute`, `mtr`, `nano`. The account tools (`useradd`, `groupadd`,
`adduser`), `watch`, `getty` and `pivot_root` were executed after all, against a
real account database, a real terminal and a private mount namespace on a
disposable Debian 13 VM rather than on the measurement host. The `mkfs.*`,
`fsck.*`, `fdisk`, `sfdisk` and `cfdisk` applets *were* executed — against image files rather than block devices —
and validated with the vendor tools; `nc` was exercised over loopback.

`tree` is the one applet with an upstream counterpart that was not compared at all,
because `tree(1)` is not installed on this machine; its entry is marked
_(no reference)_ and carries no percentage. `ncdu` *was* executed, under a
pseudo-terminal, and is marked _(run)_ on that basis.

`dd` is scored by operands rather than flags, since that is how the original is
driven. `sh`, `awk`, `sed`, `find`, `test`, `expr`, `printf`, `ip`, `iptables` and
`ps` are described by feature, because an option count says nothing useful about a
language or a subcommand grammar.

### Reproducing this

The scripts are in [`tools/coverage/`](tools/coverage/):

```sh
cd tools/coverage
python3 ref_options.py > ref.json     # option groups from man pages
python3 ba6_options.py > ba6.json     # option sets from the Go source
python3 compare.py ref.json ba6.json  # this report's coverage numbers
sh behaviour_diff.sh                  # side-by-side output diffs
python3 text_overlap.py               # verbatim help-text check (PROVENANCE.md)
```

They read `man` pages rather than running each tool with `--help`, because the
applet list includes `halt`, `poweroff`, `reboot` and `init`, and under systemd
`-h`/`--help` is an undocumented no-op for those that falls through to the default
action — shutting the machine down — rather than printing help text.

One consequence is that a few reference sets are thinner than the ones used for
the numbers above, which came from `--help` output where it was safe to collect.
`compare.py` marks any applet with fewer than four documented options as
`[thin reference - verify by hand]`; gawk is the worst case.
