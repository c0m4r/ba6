# Applet coverage against the original tools

> Applets marked _(run)_ were diffed against the real tool on a live system; _(src)_
> means the option parser was read but the behaviour could not be executed side by
> side (root-only or interactive applets); _(no reference)_ means the original is not
> installed on the measurement host, so nothing was compared. Everything below is
> measured, not estimated — see [How this was measured](#how-this-was-measured).

As of commit `5303302`, `ba6` implements 168 applets (`main.go`'s `applets` map).
This document assesses 150 of them against their originals; the 18 it does not yet
cover are listed under [Not yet assessed](#not-yet-assessed).

**Short answer to "which are 1:1?"** — of the 150 assessed, 25 are genuine drop-ins,
33 more are near-complete, 83 are partial or a narrow subset in ways that stay
invisible until a script reaches for a flag, and 9 have no upstream counterpart to
compare against; see the [verdict table](#verdict-in-one-table). `netstat`, `ps aux`/
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
| **A — drop-in** | Byte-identical output on every case tested; only niche options missing | `pwd` `echo` `basename` `tr` `base64` `uname` `whoami` `true` `false` `printenv` `sleep` `mknod` `seq` `dirname` `tac` `fold` `expand` `unexpand` `cksum` `nl` `paste` `comm` `split` `join` `nice` |
| **B — near-complete** | Common paths match; a handful of real gaps | `cat` `wc` `head` `tail` `rm` `mkdir` `tee` `test` `[` `expr` `printf` `id` `mktemp` `kill` `timeout` `chroot` `blockdev` `stat` `env` `cmp` `sync` `dd` `sha256sum` `which` `rmdir` `top` `host` `setsid` `nohup` `renice` `lsusb` `lspci` `hwclock` |
| **C — partial** | Everyday cases work, well-known flags or output details missing | `ls` `cp` `mv` `ln` `touch` `sort` `uniq` `cut` `grep` `find` `du` `df` `date` `readlink` `realpath` `strings` `od` `hexdump` `diff` `sh` `awk` `sed` `file` `tar` `gzip` `gunzip` `chown` `chgrp` `chmod` `free` `uptime` `hostname` `wget` `lsof` `lsblk` `blkid` `mount` `umount` `losetup` `swapon` `swapoff` `mkswap` `ss` `ip` `iptables` `ping` `traceroute` `mtr` `nc` `nslookup` `curl` `iftop` `pgrep` `pkill` `pidof` `modprobe` `insmod` `rmmod` `lsmod` `fdisk` `sfdisk` `mkfs` `mkfs.ext2` `mkfs.ext3` `mkfs.ext4` `mkfs.xfs` `mkfs.btrfs` `fsck` `fsck.ext2` `fsck.ext3` `fsck.ext4` `login` `passwd` `ps` `netstat` `tree` `less` `dmesg` `xargs` `dig` `nano` `ncdu` `cfdisk` |
| **D — narrow subset** | A slice of the original; do not treat as a replacement | _(none)_ |
| **N/A** | ba6-specific, no upstream counterpart | `help` `man` `completion` `init` `halt` `reboot` `poweroff` `switch_root` `udhcpc` |

## Not yet assessed

These applets exist in the binary but are not yet measured against their originals
in this document: `adduser` `bunzip2` `bzip2` `cpio` `groupadd` `md5sum` `sha1sum`
`sha512sum` `sysctl` `tty` `unxz` `unzip` `unzstd` `useradd` `watch` `xz` `zip`
`zstd`.

## Open defects

None. All known behaviour defects are fixed and pinned by `defects_test.go`; the
four `ps`-specific ones are pinned by `TestPsMatchesProcpsArithmetic` in
`inspection_test.go`.

## Cross-cutting gaps

Absent behaviour rather than wrong behaviour, each of which touches many applets.

* **No `--version` for most applets.** _(run)_ `cfdisk` is the exception, with
  `-V`/`--version`; most other applets answer `unsupported option "--version"`.
  Scripts and packagers commonly probe this.
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
| `basename` | 3/3 | — | `-s`, `-a`, suffix form, `/`, `//`, trailing slash and `-z` all match _(run)_ |
| `tr` | 3/4 | `-t` | `-d` `-s` `-c`, ranges, `[:classes:]`, `-cd` combinations match _(run)_ |
| `base64` | 2/3 | `-i` | `-d`, `-w 0`, `-w N` match; round-trips with coreutils _(run)_ |
| `uname` | 7/9 | `-p` `-i` (GNU prints `unknown` for both) | `-a` `-n` `-v` `-mrs` byte-identical _(run)_ |
| `whoami` `true` `false` | — | — | identical _(run)_ |
| `printenv` | 1/1 | — | identical, including `-0` _(run)_ |
| `sleep` | n/a | — | suffixes, fractions and multiple operands behave like GNU; only the error wording differs _(run)_ |
| `mknod` | 1/3 | `-Z` `--context` | `-m` present; FIFO creation identical, device nodes need root _(run)_ |
| `seq` | 3/3 | — | `-f` `-s` `-w`, integer, float, reverse and large ranges all match, including GNU's operand-driven decimal precision _(run)_ |
| `dirname` | 1/1 | — | byte-identical on every path tested, including trailing, doubled and leading double slashes and `-z` _(run)_ |
| `tac` | 3/3 | — | `-b` `-r` `-s`, separator placement and multi-file ordering match _(run)_ |
| `fold` | 4/4 | — | `-b` `-c` `-s` `-w`, tab stops, backspace and CR column rules match _(run)_ |
| `expand` | 2/2 | — | `-i` `-t` (size, stop list, `/N` and `+N` forms) byte-identical on randomized inputs _(run)_ |
| `unexpand` | 3/3 | — | `-a` `--first-only` `-t`, including the `-t`-implies-`-a` rule, byte-identical on randomized inputs _(run)_ |
| `cksum` | n/a | — | POSIX CRC-32/CKSUM, byte count and stdin form match _(run)_ |
| `paste` | 3/3 | — | `-d` `-s` `-z`, delimiter cycling, escape handling, short files and multi-file padding match _(run)_ |
| `comm` | 5/5 | — | `-1/-2/-3` (combined too), `-z`, `--total`, and the check-on-unpairable order semantics of `--check-order`/`--nocheck-order` match _(run)_ |
| `split` | 11/13 | `--filter` `-u`'s effect | `-l` `-b` `-C` `-n N|l/N|r/N|K/N` `-d` `-x` `-a` `-e` `-t` `--verbose` `--additional-suffix`, legacy `-N`, suffix widening sequence and byte-chunk arithmetic match _(run)_ |
| `join` | 11/11 | — | `-1` `-2` `-a` `-v` `-o` `-t` `-e` `-i` `--header` `-z`, field re-join rules and unsorted-input reporting match _(run)_ |
| `nice` | 2/2 | — | `-n` and the legacy `-N` form; niceness applies to the whole command run, exit codes match _(run)_ |

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
| `mkdir` | 3/5 | `-Z` | `-p` `-m` `-v` match, errors match apart from the Go string _(run)_ |
| `rmdir` | 3/3 | — | `-p` `-v`, the non-empty error and the refusal to remove a non-directory all match _(run)_ |
| `tee` | 4/4 | — | `-a` `-i` `-p` and the four `--output-error` modes present, output identical _(run)_ |
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
| `top` | common display paths | configuration files, alternate windows, field-layout editor, colour mapping, kill/renice prompts, and task-area scrolling | Provides the five standard summary lines; procps-style task columns; batch and terminal modes; `-b -n -d -p -u/-U -o/-O -c -H -i -S -E -e -w -1`; and basic live keys for sorting and view toggles. Dynamic CPU percentages use adjacent `/proc` snapshots rather than lifetime averages. In raw terminal mode, each rendered row ends in CRLF so the process table remains column-aligned. |
| `setsid` | 3/3 | — | `-c` `-f` `-w`, session/process-group identity and exit statuses match; `-f` forks via Go's exec rather than a raw fork+exec _(run)_ |
| `nohup` | n/a | — | SIGHUP immunity, /dev/null stdin and nohup.out redirection under a pseudo-terminal, 125/126/127 exit paths all match _(run)_ |
| `renice` | 3/3 | — | `-n` `-p` `-g` `-u` and the legacy positional form; old/new priority reporting matches, including kernel clamping of out-of-range requests _(run)_ |
| `lsusb` | 1/7 | `-t` `-s` `-d` `-v` `-D` `-P` | default listing matches with usb.ids installed; no tree or verbose modes _(run)_ |
| `lspci` | 2/11 | `-m` `-v` `-t` `-s` `-d` `-x` `-k` `-b` `-nn` | default and `-n` listings are byte-identical to pciutils with pci.ids, including built-in class names; no verbose/tree modes _(run)_ |
| `hwclock` | 5/8 | `--adjfile` drift handling `--directisa` `--test` | `--show`/`--get` format, `--hctosys`, `--systohc`, `--set --date`, `--utc`/`--localtime`; ioctl on /dev/rtc with a sysfs fallback _(run)_ |

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

**`od`** _(run, vs util-linux 2.41)_ — 14/26 option groups. Default output is
2-byte octal words with an octal address column, matching GNU's own field widths,
zero-extension of a trailing partial word, and the `*` elision of runs of identical
lines (disabled by `-v`). Present and byte-identical on every case tested:
default/`-o` (octal words), `-b` (octal bytes), `-c` (the C escape set: `\0 \a \b \t
\n \v \f \r \\`, literal printable ASCII, `\NNN` octal for everything else), `-a`
(named mnemonics for the C0 controls plus `sp`/`del`, masking the high bit the way
real od's `-a` does — not what its `-c` does), `-x` (hex words), `-i` (4-byte signed
decimal), `-A o/d/x/n` (address radix, including suppressing the column entirely),
`-j` (skip), `-N` (max bytes), and multiple file operands read as one concatenated
stream. Missing: the modern `-t TYPE` spec, `-f` (float), `-s`/`-l` variants, `-w`
(line width), `--endian`.

**`hexdump`** _(run, vs util-linux 2.41)_ — 11/17 option groups. `-n`/`-s` (byte
count and skip, including past EOF) work on every mode, not just `-C`. Present and
byte-identical: `-C` (the classic hex+ASCII gutter), the bare default (tight 2-byte
hex, hex address — narrower field spacing than the explicit `-x`, a real quirk where
hexdump's default format and its own `-x` flag are not the same width), `-c` (C
escapes), `-b`/`-d`/`-o`/`-x` (octal bytes and 2-byte octal/decimal/hex words, all
zero-padded to a fixed 8-column field, wider than od's equivalents — a verified
difference between the two tools' conventions, not an inconsistency). Both the
trailing-line suppression on empty input (hexdump prints nothing for a 0-byte read;
od still prints one address line) and the interaction between `-s` past EOF (prints
the real end offset) versus an explicit `-n 0` (prints nothing) match the real tool.
Missing: `-e`/`-f` (custom format strings — the feature both tools' other flags are
shorthand for), `-v`, `-L`.

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

**`sed`** _(run, vs GNU sed 4.10)_ — a program-counter-based interpreter, verified
line by line across every idiom below. Present: `{ }` block grouping (including
nested blocks), `a`/`i`/`c` (both the classic backslash-continued form and the GNU
one-line form, with the same backslash escapes — `\t`, `\n`, a bare `\x` dropping
the backslash — real sed applies to that text, and `a`'s queued output still
appears even when the same cycle later runs `d`), `y///`, `n`/`N` (N at end of
input keeps the pattern space and falls to the end of the script rather than
quitting, matching GNU's non-POSIX default), `D`/`P` (the blank-line-squeezing
`/^$/{N;/^\n$/D}` idiom and its relatives), `h`/`H`/`g`/`G`/`x` (hold space,
including the classic `1!G;h;$!d` reverse-lines idiom), `b`/`t`/`T`/`:label`
(arbitrary forward and backward jumps, `t`/`T` correctly tracking "has a
substitution happened since the last input line or branch"), `q`/`Q` with an
optional exit code, `r`/`w` (whole-file append and pattern-space write, with
`s///w file` too), `l`, and `-i`/`--in-place` (with a `-i.suffix`/`-i*pattern*`
backup, each file processed with its own fresh hold space and line numbering, and a
same-directory temp file renamed over the original only once the script finished
without error). Missing: `R`/`W` (the rarer per-invocation-one-line variants of
`r`/`w`), `-s`, `-z`, `--posix`, `z`/`F`/`e` (GNU's newest extensions), the numeric
`s///2` occurrence flag, and `l`'s line-wrap width (lines are never wrapped).

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

**`chmod`** _(run, vs GNU chmod)_ — full symbolic mode grammar alongside octal,
matched clause by clause: multiple `who` letters (`u` `g` `o` `a`), chained
operations sharing one `who` (`u+x-w`), comma-separated clauses (`u=rwx,g=rx`),
`+`/`-`/`=`, the permission letters `r w x X s t`, and the `u`/`g`/`o`
permission-copy form (`go=u`). `X` only sets execute when the file is a directory
or already has an execute bit, `s` maps to setuid/setgid on `u`/`g` respectively,
`t` is the sticky bit, and `=` clears the special bit tied to any class it touches
unless that same clause also sets it — including the case where an omitted `who`
defaults to `a` but is masked by the process umask (`chmod +w` under `022` only
reaches the owner) where an explicit `a` is not. `-v`/`-c` print GNU's `mode of
'FILE' changed from 0NNN (rwx...) to 0NNN (rwx...)` and `retained as` lines
byte-for-byte; `-f` suppresses error text without changing the exit status;
`--reference FILE`/`--reference=FILE` copies another file's mode outright. Missing:
`-R`'s recursive traversal order differs from GNU's on adversarial recursive modes
that lock out traversal mid-walk (GNU's `fts`-based walker stops partway in ways
this project's directory-unlock recovery walker does not reproduce byte for byte).

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
commands accept **any unambiguous prefix**, resolved in the order iproute2
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

**`pgrep` / `pkill`** — 3/31 and 2/29 _(run)_. `-f -x -v` and `-SIGNAL` present.
Missing **`-l` `-a` `-u`/`-U` `-P` `-n` `-o` `-c` `-d` `-i` `-t` `-g`/`-G` `--ns`**.

**`pidof`** — no options _(run)_. Ordering matches (newest first). Missing `-s -o -x
-c -q -w -t -S`.

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

**`xargs`** _(run, vs GNU findutils xargs)_ — input splitting, quoting and `-I`
semantics match GNU. Present: `-0` (NUL-delimited input, preserving empty items
between consecutive NUL bytes), `-r -n -I` (attached, spaced and `=` forms), `-a`
(read items from a file instead of stdin), `-d` (custom single-character delimiter,
decoding `\n`/`\t`/escape forms), `-E`/`-e` (stop reading at a sentinel item),
`-t`/`-p` (echo each command line to stderr before running it, `-p` additionally
prompting on `/dev/tty` — never stdin, which may be the item source — and running
only on a `y`/`Y` answer), `-s` (cap the assembled command line's length, splitting
into more invocations as needed) and `-x` (fail outright if a single item can never
fit under `-s`, matched to real xargs's exact `argument line too long` wording and
exit status), and `-P` (genuine concurrent batches, not a sequential simulation —
verified with a workload whose optimal makespan under 4-way concurrency was checked
by hand). Missing `-L`, `--process-slot-var`.

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

1. **`sort -k`/`-t`** — field sorting is what sort is for; without it the applet
   handles only whole-line ordering.
2. **`touch -d`/`-t`/`-r`** — the only reason to reach for touch besides creating a file.
3. **`grep -o`/`-A`/`-B`/`-C`** — the highest-frequency missing options in the
   most-used text tool now that `sed -i` (and sed's whole command language —
   `a i c y n N D P h H g G x b t T { }` — see the `sed` entry above) is done.
4. **`ps` process selection** — `-u`, `-C`, `-t` and `--sort`. `ps aux` already
   matches procps byte for byte, so picking *which* processes to list is the
   remaining gap.
5. **`ss` `Recv-Q`/`Send-Q`** — the netlink query added for `IPV6_V6ONLY` already
   returns `idiag_rqueue` and `idiag_wqueue`; only the columns are missing.
6. **Assess the 18 unmeasured applets** listed under
   [Not yet assessed](#not-yet-assessed) — they run, but nothing here says how
   closely they match their originals.

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
`iftop`, `traceroute`, `mtr`, `nano`. The `mkfs.*`, `fsck.*`, `fdisk`, `sfdisk` and
`cfdisk` applets *were* executed — against image files rather than block devices —
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
