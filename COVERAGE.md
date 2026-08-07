# Applet coverage against the original tools

> Applets marked _(run)_ were diffed against the real tool on a live system; _(src)_
> means the option parser was read but the behaviour could not be executed side by
> side (root-only or interactive applets). Everything below is measured, not
> estimated — see [How this was measured](#how-this-was-measured).

Measured 2026-08-07 against `ba6` at commit `e532a75`, 127 applets, on Manjaro.
`make verify` passes. The `wget` and `curl` entries were re-measured later the same
day, after wget was given its own command line instead of sharing curl's, and the
`rmdir` `ss` `xargs` `free` `df` `ls` `id` `pidof` `dig` `fsck.ext*` `sh` `dirname`
`seq` `mktemp` `sha256sum` and `printf` entries were re-measured after the defects
below were fixed.

**Short answer to "which are 1:1?"** — 14 applets are genuine drop-ins, 25 more are
near-complete, 79 are partial or a narrow subset in ways that stay invisible until a
script reaches for a flag, and 9 have no upstream counterpart to compare against.
Notably, the *filesystem* applets score badly on flags but produce images that pass
`e2fsck`, `xfs_repair` and `btrfs check` cleanly — flag count is not the same as
correctness in either direction.

All 17 behaviour defects this measurement found have since been fixed and are
covered by regression tests in `defects_test.go`; they are kept below as a record
of what was wrong and what the originals actually do. The
[cross-cutting gaps](#cross-cutting-gaps) and the missing options are still open.

## Verdict in one table

| Tier | Meaning | Applets |
|---|---|---|
| **A — drop-in** | Byte-identical output on every case tested; only niche options missing | `pwd` `echo` `basename` `tr` `base64` `uname` `whoami` `true` `false` `printenv` `sleep` `mknod` `seq` `dirname` |
| **B — near-complete** | Common paths match; a handful of real gaps | `cat` `wc` `head` `tail` `rm` `mkdir` `tee` `test` `[` `expr` `printf` `id` `mktemp` `kill` `timeout` `chroot` `blockdev` `stat` `env` `cmp` `sync` `dd` `sha256sum` `which` `rmdir` |
| **C — partial** | Everyday cases work, well-known flags or output details missing | `ls` `cp` `mv` `ln` `touch` `sort` `uniq` `cut` `grep` `find` `du` `df` `date` `readlink` `realpath` `strings` `tar` `gzip` `gunzip` `chown` `chgrp` `free` `uptime` `hostname` `top` `wget` `lsof` `lsblk` `blkid` `mount` `umount` `losetup` `swapon` `swapoff` `mkswap` `ss` `ip` `iptables` `ping` `traceroute` `mtr` `nc` `nslookup` `curl` `iftop` `pgrep` `pkill` `pidof` `modprobe` `insmod` `rmmod` `lsmod` `fdisk` `sfdisk` `mkfs` `mkfs.ext2` `mkfs.ext3` `mkfs.ext4` `mkfs.xfs` `mkfs.btrfs` `fsck` `fsck.ext2` `fsck.ext3` `fsck.ext4` `login` `passwd` |
| **D — narrow subset** | A slice of the original; do not treat as a replacement | `sh` `awk` `sed` `chmod` `od` `hexdump` `file` `diff` `xargs` `ps` `dig` `nano` `dmesg` |
| **N/A** | ba6-specific, no upstream counterpart | `help` `man` `completion` `init` `halt` `reboot` `poweroff` `switch_root` `udhcpc` |

## Defects found while measuring

These were behaviour bugs, not missing features — ordered by severity as they were
found. **All 17 are now fixed**, each verified against the original tool and pinned
by a test in `defects_test.go`. The descriptions below are kept because they record
what the originals do, which is the part that is easy to get wrong again.

1. ~~**`rmdir FILE` deletes the file.**~~ _(fixed)_ `rmdir` called `os.Remove`, which
   unlinks regular files too. It now calls `rmdir(2)` directly and fails with
   `rmdir: failed to remove 'F': Not a directory`, exit 1, as GNU does.
   ```
   $ echo data > F; ba6 rmdir F; ls F
   ls: cannot access 'F': No such file or directory      # before
   rmdir: failed to remove 'F': Not a directory          # now
   ```
2. ~~**`ss` prints raw hex for IPv6 addresses.**~~ _(fixed)_ It printed
   `[410F002A7852B11C6D3D9142BC68F380]:59252` instead of
   `[2a00:f41:1cb1:5278:4291:3d6d:80f3:68bc]:59252`. Both the IPv4 and IPv6 tables
   in `/proc/net` store the address as little-endian 32-bit words, so each group of
   eight hex digits is now put back into network order. An unset port and an
   unspecified IPv6 address print as `*`, as in the original.
3. ~~**`ss` rejects bundled short options.**~~ _(fixed)_ `ss -tuln` failed with
   `unsupported option -l` because `-l` and `-a` were matched only as whole
   arguments. All short options now go through the same bundle loop.
4. ~~**`xargs` does not split newline-separated input the way the original does.**~~
   _(fixed)_ `printf 'a\nb\n' | xargs echo` gave `a \n b \n`; it now gives `a b`.
   Input splitting no longer goes through the shell's tokeniser, so `$HOME` stays a
   literal `$HOME`. `-n1`, `-I{}` and `--replace={}` parse, and `-I` substitutes one
   whole line per command as GNU does.
5. ~~**`free` computes "used" with a different formula.**~~ _(fixed)_ procps uses
   `MemTotal - MemAvailable`; ba6 used `MemTotal - MemFree - buff/cache`. Both are
   defensible, but the number people read off `free` is procps'. The columns were
   also one short of procps' twenty-character first field, and `-h` printed
   coreutils-style `27G` where procps prints `27Gi`. `free` and `free -h` are now
   byte-identical to procps.
6. ~~**`df` lists pseudo-filesystems that `df` hides**~~ _(fixed)_ — 36 rows against
   GNU's 19. It now drops the dummy filesystem types, anything reporting zero
   blocks, mounts it cannot stat, and bind mounts that repeat a device already
   listed; `-a` keeps them. Columns are sized from the data, block counts use
   `f_frsize`, and the device is canonicalised (`/dev/mapper/…` → `/dev/dm-0`) only
   in the full listing, which is where GNU does it. `df`, `df -h`, `df -k` and the
   explicit-path forms all match.
7. ~~**`ls -lh` rounds down where GNU rounds up.**~~ _(fixed)_ 3000 bytes printed
   `2.9K` instead of `3.0K`. The helper is shared, so `du -h` and `df -h` were wrong
   the same way; it now rounds up in exact integer arithmetic, and a value that
   rounds up to a whole 1024 moves to the next unit (`1047553` → `1.0M`).
8. ~~**`id` orders groups differently, and inconsistently between its own forms.**~~
   _(fixed)_ It put the primary group last in the long form and first-then-descending
   for `-G`. Both forms now list the primary group first and the supplementary ones
   ascending, as GNU does.
9. ~~**`pidof` returns PIDs in ascending order**~~ _(fixed)_; it now prints newest
   first, like the original.
10. ~~**`dig` mis-parses the common argument order.**~~ _(fixed)_ `dig +short A
    example.com` failed with `unsupported query type "EXAMPLE.COM"`. Operands are now
    recognised by what they are rather than by position, so the name and the record
    type may be given in either order.
11. ~~**`fsck.ext3` and `fsck.ext4` identify themselves as `fsck.ext2`.**~~ _(fixed)_
    The three names now have their own entry points and report themselves. The checks
    behind them are still shared, which is also true of e2fsck's three names.
12. ~~**`sh` builtins ignore redirection.**~~ _(fixed)_ `sh -c 'echo hi > o'` printed
    `hi > o` and created no file. Redirections are now parsed before a builtin is
    dispatched, and `os.Stdin`/`os.Stdout` are swapped for the duration of the call —
    this shell's equivalent of the `dup2` a real one performs. Fixing this exposed a
    second bug in the tokeniser: inside double quotes a backslash escapes only
    ``$ ` " \`` and newline, so `printf "%s\n"` now reaches printf intact.
13. ~~**`dirname` mishandles a trailing slash.**~~ _(fixed)_ `dirname /a/b/` returned
    `/a/b`; GNU and POSIX return `/a`. `filepath.Dir` cleans the path first, which is
    the wrong operation here.
14. ~~**`seq` accumulates floating-point error.**~~ _(fixed)_ `seq 0.1 0.1 0.3` printed
    `0.30000000000000004`. Values now come from `first + i*step` with the count
    computed up front, and the precision comes from how the operands are spelled
    (`0.10` asks for two decimals). `-w` was added along the way.
15. ~~**`mktemp` ignores the template.**~~ _(fixed)_ `mktemp -d -p . XXXX` created
    `./3160348960`, because `os.CreateTemp` substitutes a decimal number of whatever
    length it needs. It now replaces exactly the run of `X`s with random alphanumerics
    and reports `too few X's in template` when there are under three.
16. ~~**`sha256sum -b` is accepted and ignored.**~~ _(fixed)_ Binary mode now prints
    the `*` marker that distinguishes it from text mode in a checksum file, and
    `--quiet`/`--status` outside `-c` (and `-b`/`-t` with it) are rejected the way GNU
    rejects them.
17. ~~**`printf '%d' abc` silently prints `0` and exits 0.**~~ _(fixed)_ It now reports
    `expected a numeric value`, or `value not completely converted` for a partial
    number, prints the value anyway and exits 1 — while a *missing* operand stays a
    silent zero. Character constants (`'A` → 65) and negative operands to `%x` work
    too.

## Cross-cutting gaps

Fixing these lifts many applets at once.

* **Short-option bundling is implemented per applet, so half of them reject it.** _(run)_
  Every original accepts `-fn`, `-qn 1`, `-pm 700`.

  | Bundling works | Bundling fails |
  |---|---|
  | `ls -la` `grep -in` `rm -rf` `cp -rp` `mv -fv` `tar -cf` `sort -rn` `cat -nE` `du -sh` `wc -lw` `uniq -ci` `touch -am` `xargs -0r` `umount -lf` `ln -sfT` | `head -qn` `tail -qn` `cut -sd,` `tee -ai` `mkdir -pm` `sha256sum -bt` `base64 -dw` `ss -tuln` `swapoff -av` `losetup -fr` `fsck.ext4 -fn` |

  Applets that loop over `a[1:]` runes bundle correctly; those that `switch` on the
  whole argument do not. A shared parse helper would settle it once.

* **Go error strings leak into diagnostics.** _(run)_ Every applet that wraps an
  `error` prints the Go form:

  | | GNU | ba6 |
  |---|---|---|
  | `cat nosuch` | `cat: nosuch: No such file or directory` | `cat: nosuch: open nosuch: no such file or directory` |
  | `rm nosuch` | `rm: cannot remove 'nosuch': No such file or directory` | `… : lstat nosuch: no such file or directory` |
  | `mkdir EX` | `mkdir: cannot create directory 'EX': File exists` | `… : mkdir EX: file exists` |

  Affected at least: `cat` `head` `tail` `wc` `sort` `grep` `cp` `mv` `rm` `rmdir`
  `mkdir` `ls` `stat` `fdisk`. A single helper that maps `*os.PathError`/`syscall.Errno`
  to the strerror text (and drops the operation and path prefix) fixes all of them.
* **No `--version` anywhere.** _(run)_ Every original supports it; ba6 answers
  `unsupported option "--version"`. Scripts and packagers probe this.
* **No `Try 'x --help' for more information.` line** after a usage error. _(run)_
* **Exit codes are not always the original's.** `ls nosuch` → GNU 2, ba6 1;
  `sort nosuch` → GNU 2, ba6 1. `grep` correctly uses 2.
* **Silent acceptance of unknown options.** _(run)_ These applets accept
  `--zzz-nope` without complaint instead of failing: `seq` `od` `hexdump` `strings`
  `echo`¹ `printf`¹ `date` `env` `printenv` `whoami` `file` `lsmod` `blkid` `which`
  `diff` `pidof` `sleep` `true` `false` (¹ correct for `echo`/`printf` per POSIX).
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
| `wc` | 4/8 | `-L` `--total` `--files0-from` `--debug` | **column width differs on every invocation**, including stdin: ba6 pads every count to 7, GNU sizes to the widest value (`3 3 9` vs `      3       3       9`) _(run)_ |
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
| `cmp` | 1/5 | `-b` `-i` `-l` `-n` | `-s`, the differing-byte message, the EOF message and exit codes match _(run)_ |
| `sync` | 0/2 | `-d` `-f`, and **file operands** | bare `sync` is identical; `sync PATH` is rejected _(run)_ |
| `dd` | 8/13 operands | `iflag=` `oflag=` `cbs=`, `conv=fsync\|fdatasync\|noerror\|swab\|excl\|ucase\|lcase`, `status=progress` | `if of bs ibs obs count skip seek conv=notrunc,sync status=none` produce byte-identical results to GNU on every combination tested, including stdin/stdout; the summary omits GNU's `, T s, R MB/s` tail _(run)_ |

### Tier C — partial

**`ls`** — 9/56 options _(run)_. Present: `-l -a -A -d -h -r -t -S -R -F -1`.
Missing everything else, notably `-i` `-c` `-u` `-U` `-v` `-C` `-x` `-m` `-n` `-g` `-o`
`-p` `-s` `-Q` `-w` `--color` `--time-style` `--group-directories-first` `--sort`.
`-lh` matches GNU's rounding. Long format, symlink arrows, `total`, multi-directory
headers and C-locale sort order all match.

**`cp`** — 7/34 _(run)_. Present: `-r/-R -a -p -f -i -v`. Missing `-d -L -P -H -n -u
-l -s -t -T -x --parents --sparse --backup --reflink --remove-destination
--strip-trailing-slashes`. Basic copies, `-p` timestamps and the "into itself" guard match.

**`mv`** — 4/16 _(run)_. Present: `-f -i -n -v`. Missing `-t -T -b -S -u --backup
--exchange --strip-trailing-slashes`. Cross-device moves work.

**`ln`** — 4/14 _(run)_. Present: `-s -f -v -T`. Missing `-n -r -b -i -d -L -P -S -t`.
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
Patterns are RE2, so BRE-only escapes and backreferences differ (documented).

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

**`top`** — 3/18 _(run)_. `-b -n -d` present. Header is 3 lines instead of 5 (no
`%Cpu(s)` line, no Tasks state breakdown, no Swap line), the task table has different
columns (`PID USER %CPU VSZ RSS STAT COMMAND`), and there are no interactive keys.

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

**`ss`** — 7/44 _(run)_. Addresses decode correctly and short options bundle.
Present: `-t -u -x -a -l -n -p`
(`-n`/`-p` accepted but ignored). No `Recv-Q`/`Send-Q` columns, no service-name
resolution, no process attribution. Missing `-s -e -m -i -o -r -f -K -H -Z --ipv4/--ipv6`.

**`ip`** — objects `link`, `addr`, `route`, `neigh`, `rule` _(run)_. `route show` and
`rule show` match closely; `link show` omits `qdisc`, `mode`, `group`, `qlen` and
orders flags differently; `addr show` omits `valid_lft`/`preferred_lft` and reports a
different state; `neigh show` lists multicast NOARP entries the original filters out.
Global options: only `-4`/`-6` — **no `-br`, `-j`, `-s`, `-d`, `-o`, `-c`**.

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

**`sh`** _(run)_ — the largest gap in the project. Works: simple commands, pipelines,
`&&`/`||`, `;`, quoting, `$VAR`, `cd`/`pwd`/`export`/`unset`/`read`/`exit`/`:` builtins,
`<`/`>`/`>>` for external commands. **Does not work: variable assignment as a command
(`X=5` → "executable file not found"), `if`, `for`, `while`, `case`, functions,
subshells `( )`, `$(…)` and backticks, `$((…))`, globbing, `$?`, here-documents,
`2>` redirection, `trap`, `set`, `local`.** TODO.md already tracks control flow and
substitution; assignment and `$?` are not listed there and are more fundamental.

**`awk`** _(run)_ — works: `BEGIN`/`END`, `/re/` and `$n ~ /re/` patterns, field and
record variables (`NR NF FS OFS ORS FNR`), `print`, `printf`, `-F`, `-v`, `exit`,
`next`, and the builtins `length`, `substr`, `index`, `int`, `tolower`, `toupper`.
**Missing: `if`/`else`, `for`, `while`, `do`, arrays and `in`, user-defined functions,
`split`, `gsub`/`sub`, `match`, `sprintf`, `getline`, assignment to fields (`$1="x"`),
range patterns (`/a/,/b/`), output redirection, `ARGV`/`ENVIRON`/`SUBSEP`/`RSTART`.**
Roughly: one-liner projection and counting works, programs do not.

**`sed`** _(run)_ — works: addresses (line number, `$`, `/re/`, and ranges of both),
`s///` with `g`, `p`, `I` flags and `&`/`\1` backrefs, `d`, `p`, `=`, `q`, `-n`, `-e`,
`-E`/`-r`, `-f`. **Missing commands: `a` `i` `c` `y` `n` `N` `D` `P` `h` `H` `g` `G`
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

**`ps`** _(run)_ — 7/60. **`ps aux` — the most common invocation there is — is
rejected** (`unsupported operand "aux"`), and there is no BSD option grammar at all.
The default table is BusyBox-style `PID STAT COMMAND` for *every* process, where
procps prints `PID TTY TIME CMD` for the caller's terminal only; `-ef` prints a ba6
layout (`USER PID PPID VSZ RSS STAT COMMAND`), unpadded. Working: `-e`/`-A`, `-p`,
`-o`/`--format`. Missing: `-u -U -C -t -s -G --sort --forest --no-headers -L -H -j -l -w`.

**`dig`** _(run)_ — no flags at all; the name and type may now be given in either
order. Formerly `dig +short A example.com` failed,
only the `dig NAME TYPE` order parses. Missing `-x` (reverse lookup), `-t -c -p -b -f
-q -y`, every `+option` except `+short`, and the full answer/authority section layout.

**`dmesg`** _(run)_ — no options at all (0/30). Missing `-w -H -T -t -l -f -x -C -c
-k -u -n -S -r --since`.

## What to fix first

Ordered by how many applets each item moves, not by effort. The two items that were
defects — `rmdir` deleting files and `ss` printing undecoded hex — are done; what
remains is missing functionality.

1. **One `strerror`-style error helper** — closes the single most visible difference
   across ~15 applets, and makes ba6's output indistinguishable in scripts that grep
   stderr. `errText` in `util.go` is that helper; `rmdir` and `df` use it, the rest
   still print Go's `open f: no such file or directory` form.
2. **One shared short-option parser** — fixes bundling in the remaining applets and
   gives `--version` and `Try 'x --help'` to all 127 at once. `optionArgument` in
   `execution_tools.go` already handles the attached/spaced/`=` forms and is the
   natural starting point.
3. **`chmod` symbolic modes** — `chmod +x` is the most common chmod invocation there
   is, and it currently fails outright.
4. **`sort -k`/`-t`** — field sorting is what sort is for; without it the applet
   handles only whole-line ordering.
5. **`sh` variable assignment and `$?`** — expansion currently happens once, while the
   whole source is tokenised, so `export z=1; echo $z` prints nothing: a variable set
   by the script cannot be read back. Deferring expansion to command execution is the
   fix, and it is more fundamental than the control flow already tracked in TODO.md.
6. **`touch -d`/`-t`/`-r`** — the only reason to reach for touch besides creating a file.
7. **`grep -o`/`-A`/`-B`/`-C`** and **`sed -i`** — the highest-frequency missing
   options in the two most-used text tools.
8. **`ps aux`** — the invocation everyone types; today it errors out.

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
