# Provenance

How the code in this repository was produced, and how that claim can be checked.

## The claim

Every applet in `ba6` was written from published interface descriptions — man
pages, `--help` output, POSIX and filesystem format documentation — and from the
**observed behaviour and output** of the original tools. No upstream implementation
source was copied, translated, or adapted.

This matters for two reasons. `ba6` is licensed GPL-3.0-or-later, which is
one-way incompatible with the GPL-2.0-**only** licence of BusyBox, e2fsprogs,
xfsprogs, procps-ng and parts of util-linux: code derived from those could not
lawfully be combined with this project even in principle. And much of this code
was written with AI assistance, where the risk to guard against is verbatim
reproduction of training data rather than deliberate copying.

## Method

Interfaces come from documentation. Behaviour comes from running the original and
comparing, never from reading its source:

* **Options and semantics** — each applet's man page and `--help` text, plus POSIX
  where it applies.
* **Output formats** — produced by the original tool, captured, and matched column
  by column. `COVERAGE.md` records roughly 250 such comparisons.
* **On-disk filesystem formats** — the reference image is built with the system
  tool (`mke2fs`, `mkfs.xfs`, `mkfs.btrfs`), then **dumped** (`dumpe2fs`,
  `debugfs`, `xfs_db -c p`, `btrfs inspect-internal dump-tree`) and the field
  values mirrored. The result is validated with the vendor checker (`e2fsck -fn`,
  `xfs_repair -n`, `btrfs check`). Observing a tool's *output* creates no
  derivative work; reading its *source* would.
* **Kernel interfaces** — netlink (`RTM_*`, `IFLA_*`, `NLM_F_*`), ioctl numbers and
  seccomp-BPF encodings come from the Linux UAPI headers. These are the constants
  required to talk to the kernel, and Linux's syscall-note exception exists
  precisely so userspace may use them.

## Evidence

These checks are reproducible; the scripts live in [`tools/coverage/`](tools/coverage/).

| Check | Tool | Result |
|---|---|---|
| Third-party dependencies | `go.mod` | none declared, no vendor directory |
| Upstream format constants (`EXT4_*`, `XFS_SB_MAGIC`, `BTRFS_*`, `JBD2_*`) | grep | 0 occurrences; constants are independently named (`xfsSuperMagic = 0x58465342`) |
| Copied comments or "adapted from" attributions | grep | none |
| Help text vs 122 original man pages, verbatim 5-word runs | `text_overlap.py` | **0** |
| Go source vs u-root, sequences of 40+ tokens | `similarity_scan.py` | 1 idiom, in 4 files |
| Prose string literals vs coreutils, busybox, util-linux | `similarity_scan.py` | 3, all unprotectable |

**Go against Go.** u-root is the large Go implementation of these same utilities
and therefore the most plausible source of reproduced training data. The only
sequence of 40 or more tokens shared with it is

```go
cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
if err := cmd.Run(); err != nil {
```

which is how the Go standard library documentation itself wires up a subprocess.
(u-root is BSD-3-Clause in any case.) Below roughly 40 tokens every match is
language boilerplate — `if err != nil { return err }` cannot be written any other
way.

**Strings against C.** Of 3582 prose string literals in ba6, three appear in
coreutils, busybox or util-linux: `sha256sum` (a program name), `standard output`
(a stock phrase), and `Linux swap` (an identifier written into the swap header,
which must match exactly to work).

**Neither number was clean on the first run**, which is the point of keeping the
checks. On 2026-08-07 the first measurement found short descriptive fragments in
eleven applets' help text — `do not output the trailing newline` and similar, in
`echo`, `basename`, `dirname`, `cmp`, `chgrp`, `chown`, `head`, `blockdev`, `du`,
`ln` and `cat` — and one complete two-line message in `rm`, copied verbatim from
coreutils `lib/root-dev-ino.h`:

> it is dangerous to operate recursively on '%s'
> use --no-preserve-root to override this failsafe

All were rewritten. The checks are kept so the counts stay where they are.

## Magic numbers and interface names

`ba6` contains filesystem magic numbers (`0x58465342`, `0xfeedbabe`), superblock
field offsets, netlink message types and signal names identical to those upstream.
They cannot differ: a filesystem that writes a different magic number is not that
filesystem, and a program that sends a different netlink type does not work. These
are facts about an interface, not expression.

## Scope of this document

This records engineering practice and the tests that back it. It is not legal
advice and carries no warranty. If you intend to redistribute `ba6` — especially
inside a product — do your own review.
