#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 c0m4r
"""Compare ba6's option sets against the originals' and report what is missing.

    python3 ref_options.py > ref.json
    python3 ba6_options.py > ba6.json
    python3 compare.py ref.json ba6.json

A percentage here is "documented option groups ba6 also accepts". Treat it as a
floor rather than a grade: ls covers the flags people actually type at 16%, and
tar reads and writes real archives at 5%. For sh, awk, sed, find, test, expr,
printf, dd, ip, iptables and ps the number means nothing at all, because an option
count says nothing about a language or a subcommand grammar.
"""
import json
import sys


def main():
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    ref = json.load(open(sys.argv[1], encoding='utf-8'))
    ba6 = json.load(open(sys.argv[2], encoding='utf-8'))

    rows = []
    for applet, groups in ref.items():
        have = set(ba6.get(applet, []))
        hit = [g for g in groups if any(t in have for t in g)]
        missing = ['/'.join(g) for g in groups if not any(t in have for t in g)]
        percent = round(len(hit) / len(groups) * 100) if groups else None
        rows.append((percent if percent is not None else -1, applet, len(hit),
                     len(groups), missing))

    rows.sort(key=lambda r: -r[0])
    for percent, applet, hit, total, missing in rows:
        shown = 'n/a' if percent < 0 else f'{percent}%'
        # A handful of man pages document options in prose the parser cannot see,
        # so a tiny denominator means the reference is incomplete, not that the
        # applet is finished. gawk(1) is the worst offender.
        note = '  [thin reference - verify by hand]' if 0 < total < 4 else ''
        print(f'{shown:>5}  {applet:14s} {hit:3d}/{total:<3d}  '
              f'missing: {", ".join(missing)}{note}')
    covered = [r for r in rows if r[0] >= 0]
    if covered:
        print(f'\n{len(covered)} applets compared, '
              f'mean coverage {sum(r[0] for r in covered) / len(covered):.0f}%')
    return 0


if __name__ == '__main__':
    sys.exit(main())
