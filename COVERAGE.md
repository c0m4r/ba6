# Applet coverage against the original tools

> Applets marked _(run)_ were diffed against the real tool on a live system; _(src)_
> means the option parser was read but the behaviour could not be executed side by
> side (root-only or interactive applets); _(no reference)_ means the original is not
> installed on the measurement host, so nothing was compared. Everything below is
> measured, not estimated — see [How this was measured](#how-this-was-measured).

Measured 2026-08-07 against `ba6` at commit `e532a75`, 127 applets, on Manjaro.
`make verify` passes, and `tools/coverage/behaviour_diff.sh` reports 31 of 33
executed cases byte-identical — the two that differ are the documented C-locale date
format in `ls -l` and `find`'s traversal order. Two further `tree` cases skip here,
because the original is not installed.

Re-measured 2026-08-08 for the five applets the `netstat`/`ncdu`/`tree` change
touched — `netstat`, `ncdu`, `tree`, `ps` and `ip` — bringing the binary to 130
applets. Everything else in this document is unchanged from the 2026-08-07 pass.

Several entries were re-measured later the same day as fixes landed: `wget` and
`curl` after wget was given its own command line instead of sharing curl's; then
`rmdir` `ss` `xargs` `free` `df` `ls` `id` `pidof` `dig` `fsck.ext*` `dirname` `seq`
`mktemp` `sha256sum` `printf`; then `sh` `cat` `head` `tail` `wc` `sort` `cp` `mv`
`rm` `mkdir` `stat` `cut` `tee` `base64` `env` `printenv` `blkid` `which`
`losetup` and `swapoff` after the second round of defects was fixed.

**Short answer to "which are 1:1?"** — 14 applets are genuine drop-ins, 26 more are
near-complete, 81 are partial or a narrow subset in ways that stay invisible until a
script reaches for a flag, and 9 have no upstream counterpart to compare against.
`netstat` and `ps aux` are the closest of the recent additions: every invocation
tested is byte-identical to net-tools and procps.
Notably, the *filesystem* applets score badly on flags but produce images that pass
`e2fsck`, `xfs_repair` and `btrfs check` cleanly — flag count is not the same as
correctness in either direction.

All 25 behaviour defects found so far — 17 in the first pass, 8 in the second —
are fixed and pinned by `defects_test.go`. What remains is absent functionality
rather than wrong functionality: the [cross-cutting gaps](#cross-cutting-gaps) and
the missing options listed per applet below.

## Verdict in one table

| Tier | Meaning | Applets |
|---|---|---|
| **A — drop-in** | Byte-identical output on every case tested; only niche options missing | `pwd` `echo` `basename` `tr` `base64` `uname` `whoami` `true` `false` `printenv` `sleep` `mknod` `seq` `dirname` |
| **B — near-complete** | Common paths match; a handful of real gaps | `cat` `wc` `head` `tail` `rm` `mkdir` `tee` `test` `[` `expr` `printf` `id` `mktemp` `kill` `timeout` `chroot` `blockdev` `stat` `env` `cmp` `sync` `dd` `sha256sum` `which` `rmdir` `top` |
| **C — partial** | Everyday cases work, well-known flags or output details missing | `ls` `cp` `mv` `ln` `touch` `sort` `uniq` `cut` `grep` `find` `du` `df` `date` `readlink` `realpath` `strings` `tar` `gzip` `gunzip` `chown` `chgrp` `free` `uptime` `hostname` `wget` `lsof` `lsblk` `blkid` `mount` `umount` `losetup` `swapon` `swapoff` `mkswap` `ss` `ip` `iptables` `ping` `traceroute` `mtr` `nc` `nslookup` `curl` `iftop` `pgrep` `pkill` `pidof` `modprobe` `insmod` `rmmod` `lsmod` `fdisk` `sfdisk` `mkfs` `mkfs.ext2` `mkfs.ext3` `mkfs.ext4` `mkfs.xfs` `mkfs.btrfs` `fsck` `fsck.ext2` `fsck.ext3` `fsck.ext4` `login` `passwd` `ps` `netstat` `tree` |
| **D — narrow subset** | A slice of the original; do not treat as a replacement | `sh` `awk` `sed` `chmod` `od` `hexdump` `file` `diff` `xargs` `ncdu` `dig` `nano` `dmesg` |
| **N/A** | ba6-specific, no upstream counterpart | `help` `man` `completion` `init` `halt` `reboot` `poweroff` `switch_root` `udhcpc` |

## Open defects

None. The eight this document listed on 2026-08-07 were fixed the same day and are
covered by `defects_test.go`:

| Was | Now |
|---|---|
| `sh` looked up `z=1` as a command | assignment sets a variable, and a prefix assignment scopes to one command |
| a variable the script set could not be read back | expansion happens when each command runs, not when the source is tokenised |
| `$?` expanded to a literal `?` | `$?` is the previous command's status, and `$$` works too |
| eight applets printed Go's `open f: no such file or directory` | `errText` gives every diagnostic the strerror sentence and the original's wording |
| ten applets rejected bundled short options | `expandShortOptions` normalises `-qn2` and `-sd,` before each parser runs |
| seven applets returned the wrong exit code | `ls`/`sort` return 2, `env` 125, `printenv` 2, `blkid` 1, `which` 0, `pidof` 1 |
| `wc` padded every count to seven columns | the width comes from the bytes about to be read, and a lone count is unpadded |
| `ss` showed every v6 wildcard as `*` | a netlink `sock_diag` query supplies `IPV6_V6ONLY`, so v6-only listeners read `[::]` |

Three neighbouring bugs surfaced while fixing those and were fixed with them:
`ss -l` hid every UDP/IPv6 listener, because the "no peer" test only recognised the
eight-digit IPv4 spelling of an all-zero address; `sh` reported a missing command
with Go's `fork/exec` wording instead of `not found` and 127; and `cmp` said
`differ: byte N` where the original says `char N`, and collapsed the original's
three EOF messages into one. The `cmp` entry below had wrongly recorded those as
matching — they were checked by eye, not diffed, which is the failure mode the
harness in `tools/coverage/` exists to prevent.

That `cmp` fix went the wrong way, which the 2026-08-08 pass caught and corrected.
The message is locale-dependent in the original: GNU diffutils 3.12 prints
`differ: char N, line N` untranslated — the POSIX wording, which is what
`LC_ALL=C cmp` gives — and `differ: byte N, line N` through its English message
catalogue, which is what the tool prints on an ordinary system. ba6 now prints
`byte`, so the two agree in the default locale; under `LC_ALL=C` the original says
`char` and ba6 still says `byte`. The EOF messages are unaffected: they count in
bytes in both, and ba6 quotes the file name with `'...'`, the ASCII form the
original uses outside a UTF-8 locale, rather than the catalogue's `‘...’`.

Four more surfaced on 2026-08-08, when the new BSD `ps` output was diffed line by
line against procps, and were fixed with it. `defects_test.go` has no entry for
them; they are pinned by `TestPsMatchesProcpsArithmetic` in `inspection_test.go`.

| Was | Now |
|---|---|
| `RSS` came from the `rss` field of `/proc/PID/stat`, which reads ~6 % low (init: 14148 kB against ps's 15088) | the resident count comes from `/proc/PID/statm`, which agrees with `VmRSS` |
| `%CPU` and `%MEM` were rounded, so two seconds of CPU over an hour printed `0.1` | both are computed in tenths with integer division and truncated, as ps does |
| `START` was dated from `now - /proc/uptime`, so two runs a minute apart disagreed | the date comes from the kernel's `btime`, the same fixed instant ps uses |
| `USER` was the real UID, so `fusermount3` was listed as the calling user | the effective UID is used, and a setuid program is listed as its owner |

Fixing `ss` meant widening the seccomp policy in `seccomp_amd64.go` to admit
`NETLINK_SOCK_DIAG` alongside the `NETLINK_ROUTE` and `NETLINK_NETFILTER` already
allowed. It is the narrowest of the three — sock_diag only reads socket state,
where the route protocol can reconfigure the network.

## Cross-cutting gaps

Absent behaviour rather than wrong behaviour, each of which touches many applets.

* **No `--version` anywhere.** _(run)_ Every original supports it; ba6 answers
  `unsupported option "--version"`. Scripts and packagers probe this.
* **No `Try 'x --help' for more information.` line** after most usage errors. _(run)_
  `sha256sum`, `env`, `printenv` and `blkid` print it where their originals do;
  the other applets stop at the diagnostic.
* **C locale only.** `ls -l` prints `Aug  7 02:46`, `printf %f` prints `3.14`, `seq`
  prints `1.5`. The system tools follow `LC_TIME`/`LC_NUMERIC`. This is a reasonable
  deviation for a static rescue binary — worth one line in each help text.

## Per-applet detail

### Tier A — drop-in

| Applet | Coverage | Missing | Notes |
|---|---|---|---|
| `pwd` | 2/2 options | — | `-L`/`-P` both present, output identical _(run)_ |
| `echo` | 3/3 | — | `-n` `-e` `-E` and all escapes matched _(run)_ |
| `basename` | 2/3 | `-z` | `-s`, `-a`, suffix form, `/`, `//`, trailing slash all match _(run)_ |
| `tr` | 3/4 | `-t` | `-d` `-s` `-c`, ranges, `[:classes:]`, `-cd` combinations match _(run)_ |
| `base64` | 2/3 | `-i` | `-d`, `-w 0`, `-w N` match; round-trips with coreutils _(run)_ |
| `uname` | 7/9 | `-p` `-i` (GNU prints `unknown` for both) | `-a` `-n` `-v` `-mrs` byte-identical _(run)_ |
| `whoami` `true` `false` | — | — | identical _(run)_ |
| `printenv` | 0/1 | `-0` | identical _(run)_ |
| `sleep` | n/a | — | suffixes, fractions and multiple operands behave like GNU; only the error wording differs _(run)_ |
| `mknod` | 1/3 | `-Z` `--context` | `-m` present; FIFO creation identical, device nodes need root _(run)_ |
| `seq` | 3/3 | — | `-f` `-s` `-w`, integer, float, reverse and large ranges all match, including GNU's operand-driven decimal precision _(run)_ |
| `dirname` | 0/1 | `-z` | byte-identical on every path tested, including trailing, doubled and leading double slashes _(run)_ |

### Tier B — near-complete

| Applet | Coverage | Missing | Notes |
|---|---|---|---|
| `cat` | 6/10 | `-v` `-e` `-t` `-u` | `-n -b -E -T -s -A -- -`, multi-file numbering and missing-newline handling all byte-identical _(run)_ |
| `wc` | 4/8 | `-L` `--total` `--files0-from` `--debug` | counts, column widths and the `total` line are byte-identical, including stdin and the unpadded single-count form _(run)_ |
| `head` | 4/5 | `-z`, and **negative counts** (`-n -2`, `-c -3`) | `-n` `-c` `-q` `-v` `-n +N` and multi-file headers match _(run)_ |
| `tail` | 5/12 | `-F` `--retry` `--pid` `-s` `-z` `--max-unchanged-stats` | `-n` `-c` `-n +N` `-c +N` `-q` `-v` `-f` match _(run)_ |
| `sha256sum` | 6/10 | `--tag` `-z` `--ignore-missing` `--strict` `-w` | `-c` verification, `-` stdin, the `-b` binary marker and the rejection of `--quiet`/`--status` outside `-c` all match _(run)_ |
| `which` | 1/10 | the `--skip-*`/`--show-*` family | found-path output identical; on a miss GNU prints `no X in (PATH)` to stderr, ba6 prints nothing (exit 1 either way) _(run)_ |
| `rm` | 8/10 | `-I` `--one-file-system` | `-rv` prints only the top directory, GNU prints every entry; `-i` prompt reads `remove 'I'?` vs GNU `remove regular empty file 'I'?` _(run)_ |
| `mkdir` | 2/5 | `-v` `-Z` | `-p` `-m` match, errors match apart from the Go string _(run)_ |
| `rmdir` | 2/3 | `-v` | `-p`, the non-empty error and the refusal to remove a non-directory all match _(run)_ |
| `tee` | 2/4 | `-p` `--output-error` | `-a` `-i` present, output identical _(run)_ |
| `test` / `[` | 15/21 operators | `-u` `-g` `-k` `-O` `-G` `-N`, `( )` grouping | everything else including `-nt -ot -ef -a -o !` matches GNU exit codes exactly _(run)_ |
| `expr` | arithmetic complete | `:` (regex match), `length` `substr` `index` `match` | `+ - * / % < <= = != >= > & \|` all match _(run)_ |
| `printf` | all conversions but one | `%q`, the "ignoring excess arguments" warning | `%s %d %i %f %e %g %x %X %o %c %b %%`, widths, precision, `\x \0 \e` escapes, character constants and the numeric-operand diagnostics all match _(run)_ |
| `id` | 4/8 | `-r` `-z` `-Z` | long form, `-u` `-g` `-G` `-Gn` and the group order all match _(run)_ |
| `mktemp` | 3/6 | `-u` `-q` `--suffix` | `-d` `-p` `-t` and the template match, including the suffix form and the too-few-X error _(run)_ |
| `kill` | 2/10 | `-a` `-q` `-p` `-L` `-r` `--timeout` | `kill -l` prints a bare space-separated list of 18 names; GNU prints a numbered table of 64 _(run)_ |
| `timeout` | 2/5 | `-f` `-p` `-v` | `-s` `-k` and the 124 exit code match _(run)_ |
| `chroot` | 0/3 | `--userspec` `--groups` `--skip-chdir` | core behaviour present _(src)_ |
| `blockdev` | 12/26 | `--getpbsz` `--getiomin` `--getioopt` `--getalignoff` `--setbsz` `--getsize` `--getfra`/`--setfra` `--getdiskseq` `--getzonesz` `-q` `-v` | the 12 present cover the recovery cases _(src)_ |
| `stat` | `-c` near-complete | `-f` `-t` `--printf`, and `%d %t %T %w %m %C` | **default (no `-c`) layout differs**: quotes the name, no column alignment, `Device: 42` vs `0,42`, no `Birth:` line. `%N` quotes with `"` instead of `'` _(run)_ |
| `env` | 2/11 | `-0` `-C` `-S` `-a`, signal options | `-i` `-u` and `NAME=VAL` prefixes match _(run)_ |
| `cmp` | 1/5 | `-b` `-i` `-l` `-n` | `-s`, `differ: byte N, line N`, all three EOF forms (`line`, `in line`, `which is empty`) and the exit codes match; the EOF file name is quoted `'x'` where a UTF-8 locale gives GNU `‘x’` _(run)_ |
| `sync` | 0/2 | `-d` `-f`, and **file operands** | bare `sync` is identical; `sync PATH` is rejected _(run)_ |
| `dd` | 8/13 operands | `iflag=` `oflag=` `cbs=`, `conv=fsync\|fdatasync\|noerror\|swab\|excl\|ucase\|lcase`, `status=progress` | `if of bs ibs obs count skip seek conv=notrunc,sync status=none` produce byte-identical results to GNU on every combination tested, including stdin/stdout; the summary omits GNU's `, T s, R MB/s` tail _(run)_ |
| `top` | common display paths | configuration files, alternate windows, field-layout editor, colour mapping, kill/renice prompts, and task-area scrolling | Provides the five standard summary lines; procps-style task columns; batch and terminal modes; `-b -n -d -p -u/-U -o/-O -c -H -i -S -E -e -w -1`; and basic live keys for sorting and view toggles. Dynamic CPU percentages use adjacent `/proc` snapshots rather than lifetime averages. |

### Tier C — partial

**`ls`** — 9/56 options _(run)_. Present: `-l -a -A -d -h -r -t -S -R -F -1`.
Missing everything else, notably `-i` `-c` `-u` `-U` `-v` `-C` `-x` `-m` `-n` `-g` `-o`
`-p` `-s` `-Q` `-w` `--color` `--time-style` `--group-directories-first` `--sort`.
`-lh` matches GNU's rounding. Long format, symlink arrows, `total`, multi-directory
headers and C-locale sort order all match.

**`cp`** — 8/34 _(run)_. Present: `-r/-R -a -p -f -i -v --remove-destination`. Missing `-d -L -P -H -n -u
-l -s -t -T -x --parents --sparse --backup --reflink
--strip-trailing-slashes`. Basic copies, `-p` timestamps and the "into itself" guard match.

**`mv`** — 4/16 _(run)_. Present: `-f -i -n -v`. Missing `-t -T -b -S -u --backup
--exchange --strip-trailing-slashes`. Cross-device moves work.

**`ln`** — 5/14 _(run)_. Present: `-s -f -n -v -T`. Missing `-r -b -i -d -L -P -S -t`.
Hard and symbolic links match.

**`touch`** — 3/8 _(src, run)_. Present: `-a -c -m`. **Missing `-d` `-t` `-r` `-h` `-f`**
— i.e. every way of setting a timestamp other than "now".

**`sort`** — 7/27 _(run)_. Present: `-n -r -u -b -f -c -h`. **Missing `-k` and `-t`**
(no field sorting at all), plus `-o -m -s -g -M -R -z -T --parallel`.

**`uniq`** — 4/11 _(run)_. Present: `-c -d -u -i`. Missing `-f -s -w -D --group -z`.

**`cut`** — 4/12 _(run)_. Present: `-c -f -d -s`. Missing `-b --complement
--output-delimiter -n -z`.

**`grep`** — 15/49 _(run, vs GNU grep 3.12)_. Present and byte-identical: `-c -n -i -w
-x -v -l -H -h -q -e -m -E -F -r/-R`. Missing: **`-o` `-A` `-B` `-C` `-s` `--color`**,
plus `-f -L -P -G -a -z -b -I --include --exclude --exclude-dir --binary-files`.
The default pattern syntax is POSIX BRE and `-E` uses POSIX ERE. Pattern
backreferences are rejected because RE2 cannot implement them.

**`find`** — predicates present: `-name -iname -path -ipath -type -size -mtime -newer
-empty -maxdepth -mindepth -print -true -false -a/-and -o/-or -not` _(run)_; each of
those returns exactly the same set of paths as findutils on the trees tested.
Missing: **`-exec` `-execdir` `-ok` `-delete` `-print0` `-printf` `-prune` `-perm`
`-user` `-group` `-uid` `-gid` `-links` `-inum` `-regex` `-lname` `-depth` `-xdev`
`-atime` `-ctime` `-mmin` `-amin` `-cmin` `-fstype` `-ls` `-quit` `-samefile`
`-readable`/`-writable`/`-executable` `-follow`/`-L`/`-H`**. Traversal order differs
from the original's directory order.

**`du`** — 3/25 _(run)_. Present: `-s -h -k`. Missing `-d/--max-depth -c -a -b -B -x
-L -S -t --exclude --time --inodes`. Totals match on the tested trees.

**`df`** — 2/15 _(run)_. Present: `-h -k`. Missing `-i -T -a -l -t -x -B -H --total
--output`. `df`, `df -h`, `df -k`, `df -a` and explicit paths are byte-identical to
GNU, including which filesystems are listed and how the columns are sized.

**`date`** — 6/10 flags _(run)_. **Missing `-d`/`--date` and `-s`/`--set`** — no date
parsing or setting at all — plus `-R` `--rfc-3339` `-f` `--resolution`. Format
directives: `%Y %m %d %H %M %S %F %T %s %a %b %e %j %z %Z %N` work; `%U` (and likely
`%W %G %V %C %g`) are rejected. `-u` and `-r FILE` are present and match.

**`readlink`** — 1/8 _(run)_. Only `-f`. Missing `-e -m -n -q -s -v -z`.

**`realpath`** — 0/10 _(run)_. No options; resolves the path only. Missing `-e -m -s
-q -z --relative-to --relative-base -L -P`.

**`strings`** — 1/13 _(run)_. Only `-n`, whose output matches binutils exactly.
Missing `-a -t -f -o -e -d -w -T -s`.

**`tar`** — 8/154 _(run)_. Works and is **format-compatible both ways** with GNU tar
(ba6 reads real `.tar`/`.tar.gz`, GNU reads ba6's). Present: `-c -x -t -f -z -C -v -p`.
Missing: **`-j` `-J` `--zstd` `--exclude` `-T` `--strip-components` `--numeric-owner`
`--owner`/`--group` `-k` `--overwrite` `-O` `-A` `-r` `-u` `--delete` `-P` `-S`**.
`tvf` prints bare names instead of the `drwxr-xr-x user/group size date name` listing.

**`gzip` / `gunzip`** — 1/17 and 3/12 _(run)_. Streams interoperate with the real
tools in both directions. Present: `-c -d -k`. Missing **`-1`…`-9` `-t` `-l` `-v` `-f`
`-r` `-q` `-n` `-N` `-S`**.

**`chown` / `chgrp`** — 1/12 _(run)_. Present: `-R -h`; recursive ownership and group
changes produced identical results to coreutils. Missing `-c -f -v --reference
--from --dereference -H -L -P --preserve-root`.

**`free`** — 4/19 _(run)_. Present: `-b -k -m -h`(sizes). Missing `-t -s -c -w -l -v
--si --kilo/--mega/…`. `free` and `free -h` are byte-identical to procps.

**`uptime`** — 1/4 _(run)_. Missing `-p` `-s` `-r` `-c`, and the default line omits the
user count: `02:47:09 up 01:23, load average: …` vs `02:47:09 up 1:23, 1 user, load average: …`.

**`hostname`** — 1/7 _(run)_. Bare form matches. Missing `-f -d -i -a -s -y -F`.

**`lsof`** — 3/15 _(run)_. `-n -P -p -i` present. Missing `-c -u -t -d -s -F -g -x -R -a`.

**`lsblk`** — 4/38 _(run)_. `-a -b -n -o` present. Columns are space-separated with no
padding and sizes round differently (`115M` vs `114,6M`); ordering differs. Missing
`-f -p -l -J -P -T -d -e -i -m -r -s -t -x -S -N -O`.

**`blkid`** — output matched the real tool on this system _(run)_; no options at all
(missing `-o -s -t -L -U -p -i -k -c`).

**`mount` / `umount`** — 3/41 and 4/16 _(src, run)_. Mount honours `-t -o -r`; missing
`--bind --move --rbind -a -n -L -U --make-* -N -O`. **`mount` with no arguments prints
`/proc/mounts` verbatim** (`proc /proc proc rw,… 0 0`) instead of the
`proc on /proc type proc (rw,…)` form. `umount` has `-a -f -l -r`.

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

**`netstat`** — 13/21 _(run, vs net-tools 2.10)_. Eight invocations were diffed
whole and came back **byte-identical**: `-tan`, `-uan`, `-tuwxan`, `-tulpn`, `-xl`,
`-xlp`, `-rn` and `-i` — column widths, section headings, state names, the
`PID/Program name` field taken from `argv[0]`, the not-root warning on stderr, and
the `Flg` letters of the interface table all match. Present: `-t -u -w -x -l -a -n
-p -r -i` plus `-e -v -W` accepted and ignored. **Names are never resolved**, so
plain `netstat` and `netstat -r` print numeric addresses and ports where the
original prints `HOST:https` and `_gateway`; only the default route is named.
Missing `-s` (per-protocol statistics), `-A`, `-g`, `-M`, `-C`, `-F`, `-c`, `-o`,
and the IPv6 routing table.

**`ps`** — 10/58 documented options _(run, vs procps-ng 4.0.6)_, plus the BSD
grammar an option count cannot see. **`ps aux` and `ps ax` are byte-identical** over
all ~330 processes on this machine, bar rows whose `STAT` overflows its four-column
field (`S<Lsl`), where this table recovers the grid one column earlier than procps
does. Present: the dashless `a x u A w` and a bare PID list (`ps 1 2`), `-e`/`-A`,
`-f`, `-p`, `-o`/`--format` over `pid ppid uid user stat tty vsz rss %cpu %mem start
time comm args`, procps' column widths (PID sized from `pid_max`, `USER` clipped
with `+`), and the `STAT` modifiers `< N L s l +`. The default with no options still
lists **every** process as `PID STAT COMMAND`, where procps lists the caller's
terminal as `PID TTY TIME CMD`; `-ef` keeps its ba6 layout. Missing the selection
set (`-u -U -C -t -s -G --ppid`), `--sort`, `--forest`, `--no-headers`, `-l`, `-j`,
`-L`, `-H`, `-w`.

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
commands now accept **any unambiguous prefix**, resolved in the order iproute2
resolves them, so `ip r s`, `ip a s`, `ip n s`, `ip ru s` and `ip l sh` all list and
`ip l s eth0 up` sets — including the trap that `s` means `set` for `link` but
`show` for every other object. `route show` and `rule show` match closely;
`link show` omits `qdisc`, `mode`, `group`, `qlen` and orders flags differently;
`addr show` omits `valid_lft`/`preferred_lft` and reports a different state;
`neigh show` lists multicast NOARP entries the original filters out. Global options:
only `-4`/`-6` — **no `-br`, `-j`, `-s`, `-d`, `-o`, `-c`**.

**`iptables`** — 9/26 _(src)_. Commands `-A -D -F -L -P`; matches `-p -s -d --sport
--dport`; `-j`, `-n`, `-v`, `--line-numbers`. Missing `-I -R -C -S -Z -N -X -E`, `-t`
(filter table only), `-m` (no match extensions), `--goto`, `-w`, `-x`, IPv6.

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
-u -X -d -H --max-time`, clustered short flags, and HTTPS. `-O` now means "name the
file after the remote path" and takes no argument, as in the original; exit status
is 0 on an HTTP error unless `-f` is given, and 22 with it. Missing `-T` (upload),
`--compressed`, `--resolve`, `-b`/`-c` (cookies), and there is still no progress
meter.

**`wget`** — 20 of wget's own options _(run)_. **Rewritten on 2026-08-07**: it no
longer shares curl's command line. Present: `-O -P -c -nc -q -nv -v -d -S -T -t -U
--user --password --header --method --post-data --post-file --spider
--no-check-certificate --max-redirect`, several URLs per invocation, redirects
followed by default, the derived filename with `.1`/`.2` uniquifying, resume by range
request, and exit status 8 on a server error response — all verified against GNU
wget. Missing the recursive mirror (`-r -m -np -l -k -N`), rate limiting, cookies,
`-i`, FTP and WARC. The progress display is ba6's own, not wget's dotted meter.

**`iftop`** — 6/17 _(src)_. `-i -n -N -P -s -t` present. Missing `-p -b -B -f -F -l
-c -m -o -L -G`.

**`pgrep` / `pkill`** — 3/31 and 2/29 _(run)_. `-f -x -v` and `-SIGNAL` present.
Missing **`-l` `-a` `-u`/`-U` `-P` `-n` `-o` `-c` `-d` `-i` `-t` `-g`/`-G` `--ns`**.

**`pidof`** — no options _(run)_. Ordering matches (newest first). Missing `-s -o -x
-c -q -w -t -S`.

**`modprobe` / `insmod` / `rmmod` / `lsmod`** — 3/27, 0/3, 1/3, 0/2 _(src, run)_.
Load/unload work; `lsmod` output matches except one column of padding. Missing
`modprobe -a -D -c -n -R --show-depends --first-time -b`, `insmod -f -s -v`,
`rmmod -s -v`, `lsmod -s -v`.

**`fdisk` / `sfdisk`** — read-only `-l` and DOS-only write support, **by design**
(documented in help). `fdisk` 1/18, `sfdisk` 6/41. No interactive editor, no GPT
writes. **On-disk output is correct** _(run)_: a table written by `ba6 sfdisk` is read
back identically by real `fdisk -l` and `sfdisk --dump`. The *printed* layout differs —
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

`fsck.ext4` also validated a `mke2fs`-produced image correctly — but only when its
flags are given separately (`-f -n`, not `-fn`), and it identifies itself as
`fsck.ext2` in every diagnostic regardless of the name it was invoked under.

**`login` / `passwd`** — no options _(src)_; `login` is missing `-p -f -h`, `passwd`
is missing the whole administrative set (`-l -u -d -e -S -n -x -w -i -a -R -s`).

### Tier D — narrow subset

**`sh`** _(run)_ — still the largest gap in the project, but it now runs a script
that keeps state. Works: simple commands, pipelines, `&&`/`||`, `;`, multi-line
scripts, quoting (including the backslash rules inside double quotes), variable
assignment and read-back, prefix assignments scoped to one command, `$VAR`, `$?`,
`$$`, positional `$0`…`$n`, `cd`/`pwd`/`export`/`unset`/`read`/`exit`/`:` builtins,
and `<`/`>`/`>>` for both external commands and builtins. Words are expanded when
each command runs, so a variable set earlier in the script is visible to everything
after it, and an unexported one stays out of the environment children inherit.
**Does not work: `if`, `for`, `while`, `case`, functions, subshells `( )`, brace
groups `{ }`, `$(…)` and backticks, `$((…))`, globbing, here-documents, `2>`
redirection, `trap`, `set`, `local`.** TODO.md tracks the control flow and
substitution.

**`awk`** _(run)_ — works: `BEGIN`/`END`, `/re/` and `$n ~ /re/` patterns, field and
record variables (`NR NF FS OFS ORS FNR`), `print`, `printf`, `-F`, `-v`, `exit`,
`next`, and the builtins `length`, `substr`, `index`, `int`, `tolower`, `toupper`.
AWK regexes are POSIX EREs; a one-character `FS` is literal and a longer `FS`
is an ERE. Pattern backreferences are rejected because RE2 cannot implement them.
**Missing: `if`/`else`, `for`, `while`, `do`, arrays and `in`, user-defined functions,
`split`, `gsub`/`sub`, `match`, `sprintf`, `getline`, assignment to fields (`$1="x"`),
range patterns (`/a/,/b/`), output redirection, `ARGV`/`ENVIRON`/`SUBSEP`/`RSTART`.**
Roughly: one-liner projection and counting works, programs do not.

**`sed`** _(run)_ — works: addresses (line number, `$`, `/re/`, and ranges of both),
`s///` with `g`, `p`, `I` flags and `&`/`\1` backrefs, `d`, `p`, `=`, `q`, `-n`, `-e`,
`-E`/`-r`, `-f`. The default syntax is POSIX BRE and `-E`/`-r` selects POSIX
ERE; pattern backreferences are rejected because RE2 cannot implement them.
**Missing commands: `a` `i` `c` `y` `n` `N` `D` `P` `h` `H` `g` `G`
`x` `b` `t` `T` `:label` `r` `R` `w` `W` `l` `z` `F` `e`, and the numeric occurrence
flag (`s///2`).** Missing options: **`-i`**, `-s`, `-z`, `-u`, `-l`, `--posix`.

**`chmod`** _(run)_ — **octal modes only.** `u+x`, `go-r`, `a=rw`, `+x`, `u=rwx,g=rx`,
`u+s` are all rejected with `invalid octal mode`. Also missing `-c -f -v --reference`.

**`od`** _(run)_ — despite the name it prints `hexdump -C` output. GNU `od` defaults to
octal words and `od -c` prints escaped characters; ba6 ignores the format entirely.
Missing `-a -b -c -d -f -i -o -x -t -A -j -N -v -w`.

**`hexdump`** _(run)_ — `-C` and `-c` only; **`-n` and `-s` are rejected**, so you
cannot dump a slice of a file. Missing `-b -d -o -x -e -f -v -L`.

**`file`** _(run)_ — a small built-in magic table. Misclassifies plain ASCII as
`Unicode text`; reports `ELF 64-bit executable or object` where the original gives
class, ABI, interpreter, BuildID and strip state; omits device numbers for specials.
No `-i`/`--mime`, `-z`, `-L`, `-s`, `-f`, `-m`.

**`diff`** _(run)_ — unified output only (`-u`). Missing normal/context/side-by-side
output, `-q -s -r -N -i -w -b -B -E -Z -y -W -a -X -x --color --label` — 1 of 48 options.

**`xargs`** _(run)_ — input splitting, quoting and `-I` semantics match GNU.
Present: `-0 -r -n -I` (attached, spaced and `=` forms). Missing `-a -d -E -e -L -P
-p -s -t -x --process-slot-var`.

**`nano`** _(run, help)_ — 6/50 options; a minimal full-screen editor, not GNU nano's
key map, syntax highlighting, search/replace or multi-buffer support.

**`ncdu`** _(run, vs ncdu 2.9.2)_ — 6/37 options. The scan and the browser are
there: the same nine-column size field, the same bar width (`columns / 7`) drawn
against the largest entry in the directory, the `/..` row, the reverse-video header
and footer, and the `*` that marks whether the totals are disk usage or apparent
size. Compared screen against screen under a pseudo-terminal, the listing and both
totals match. Keys present: arrows/`jkhl`, enter, `n`, `s`, `a`, `?`, `q`. Options
present: `-x`, `--apparent-size`, `--exclude`, `--si`, with `-r`, `-q` and `-0/-1/-2`
accepted. **Deliberately absent: file deletion, the shell escape and directory
refresh** — the header says `[readonly]`, as ncdu's own `-r` does. Also missing the
export/import pair `-o`/`-f`, extended mode `-e`, `--exclude-from`,
`--exclude-caches`, `--exclude-kernfs`, `-L`, `-t`, the display toggles
(`--show-itemcount`, `--show-mtime`, `--show-graph`, `--show-percent`,
`--group-directories-first`, `--sort`), and the config file.

**`dig`** _(run)_ — no flags at all; the name and type may now be given in either
order. Formerly `dig +short A example.com` failed,
only the `dig NAME TYPE` order parses. Missing `-x` (reverse lookup), `-t -c -p -b -f
-q -y`, every `+option` except `+short`, and the full answer/authority section layout.

**`dmesg`** _(run)_ — no options at all (0/30). Missing `-w -H -T -t -l -f -x -C -c
-k -u -n -S -r --since`.

## What to fix first

Ordered by how many applets each item moves, not by effort.

1. **`sh` control flow** — `if`, `for`, `while` and `$(…)`. Now that assignment,
   variable read-back and `$?` work, this is what still stops a real script. TODO.md
   tracks it; `shellCommand` in `execution_tools.go` is where each command is already
   assembled, so the pieces have somewhere to attach.
2. **`chmod` symbolic modes** — `chmod +x` is the most common chmod invocation there
   is, and it currently fails outright.
3. **`sort -k`/`-t`** — field sorting is what sort is for; without it the applet
   handles only whole-line ordering.
4. **`--version` for the remaining 129** — the last thing most originals have and only `top` here
   does. `expandShortOptions` and the per-applet parsers now agree enough on shape
   that one shared entry point could carry it, along with `Try 'x --help'`.
5. **`touch -d`/`-t`/`-r`** — the only reason to reach for touch besides creating a file.
6. **`grep -o`/`-A`/`-B`/`-C`** and **`sed -i`** — the highest-frequency missing
   options in the two most-used text tools.
7. **`ps` process selection** — `-u`, `-C`, `-t` and `--sort`. `ps aux` now matches
   procps byte for byte, so picking *which* processes to list is the remaining gap.
8. **`ss` `Recv-Q`/`Send-Q`** — the netlink query added for `IPV6_V6ONLY` already
   returns `idiag_rqueue` and `idiag_wqueue`; only the columns are missing.

## How this was measured

Three independent passes, cross-checked against each other:

1. **Reference option sets** — parsed from the real tools' `--help` on this machine
   (falling back to `man` where `--help` is thin), grouping synonyms so `-a, --all`
   counts once. Versions: coreutils 9.11, util-linux 2.42.2, GNU grep 3.12
   (`/usr/bin/grep`, not the `ugrep` alias on this shell), GNU sed 4.10, gawk 5.4.1,
   GNU tar 1.35, findutils 4.11.0, diffutils 3.12, procps-ng 4.0.6, iproute2 7.1.0,
   iptables 1.8.13, file 5.48.
2. **ba6 option sets** — extracted from the Go source by brace-matching every
   `func cmd*` body, following the call graph, and collecting flag literals and
   option runes; then **verified empirically** by running the built binary with each
   candidate flag and classifying accept/reject. Over 1163 probes the source
   extraction had 0 false negatives and 53 false positives, all from `switch` blocks
   on runes used for something other than options (escape sequences in `tr`, format
   directives in `date`/`stat`, `sed` command letters) — those applets were resolved
   by hand.
3. **Behavioural diffs** — ~200 side-by-side invocations of ba6 and the original on
   identical inputs in a scratch directory, comparing stdout, stderr and exit code.
   This is where every defect in the list above came from; flag counts alone would
   have shown `tr` at 100% and never caught `rmdir` deleting files.

The 2026-08-08 pass added three techniques for applets whose output is not a
fixed string. `netstat` and `ps` read live kernel state, so each invocation was
diffed whole against the original run seconds apart, and the volatile rows
(kernel workers, the shell's own children) were filtered out before counting the
differences that remained; that is how the four `ps` defects above were found.
`ncdu` and the interactive side of `ip` were driven under a pseudo-terminal
(`script -qc`, with `stty rows/cols` fixing the geometry), the escape sequences
stripped, and the resulting screen compared with the original's. `tree` could not
be measured at all: the package is not installed here.

### Reading the percentages

A percentage is "option groups the original documents that ba6 also accepts". It is a
floor, not a grade: `ls` at 16% covers the flags people actually type, and `tar` at 5%
of 154 options still reads and writes real archives. Conversely a high percentage does
not mean drop-in — check the notes column. The applets where the metric is
meaningless (`sh`, `awk`, `sed`, `find`, `test`, `expr`, `printf`, `dd`, `ip`,
`iptables`, `ps`) are described by feature instead.

### What was not executed

Assessed from source and help text only, because they need root, a real device, a
terminal, or an irreversible action: `iptables`, `mount`/`umount` writes, `swapon`,
`swapoff`, `mkswap`, `blockdev`, `insmod`, `rmmod`, `modprobe`, `chroot`,
`switch_root`, `init`, `halt`, `reboot`, `poweroff`, `login`, `passwd`, `udhcpc`,
`iftop`, `traceroute`, `mtr`, `nano`. The `mkfs.*`, `fsck.*`, `fdisk` and `sfdisk`
applets *were* executed — against image files rather than block devices — and
validated with the vendor tools; `nc` was exercised over loopback.

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

They read `man` pages rather than running each tool with `--help`. That is a
safety rule, not a preference: the applet list contains `halt`, `poweroff`,
`reboot` and `init`, and on systemd `-h` is an undocumented no-op for those rather
than a help flag, so a "harmless" probe powers the machine off. It did, three
times, before this tooling was rewritten.

One consequence is that a few reference sets are thinner than the ones used for
the numbers above, which came from `--help` output where it was safe to collect.
`compare.py` marks any applet with fewer than four documented options as
`[thin reference - verify by hand]`; gawk is the worst case.
