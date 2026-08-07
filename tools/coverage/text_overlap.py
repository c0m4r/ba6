#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 c0m4r
"""Check that no ba6 help text reproduces wording from an original's man page.

Exits non-zero if any applet shares a run of N or more words with the manual of
the tool it implements, so this can run in CI. Short functional phrases are hard
to avoid; long ones should never appear. See PROVENANCE.md.

SAFETY: reads man pages only, never executes a tool. See README.md.
"""
import os
import re
import subprocess
import sys

from ref_options import man_page

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
WORDS = 5
HELP_ENTRY = re.compile(r'"([a-z0-9_.\[\]]+)":\s*`([^`]*)`')


def ngrams(text, n=WORDS):
    words = re.sub(r'\s+', ' ', text).strip().lower().split(' ')
    return {' '.join(words[i:i + n]) for i in range(len(words) - n + 1)}


def main():
    source = open(os.path.join(ROOT, 'help.go'), encoding='utf-8').read()
    findings = []
    compared = 0
    for applet, text in HELP_ENTRY.findall(source):
        page = man_page(applet)
        if not page:
            continue
        compared += 1
        # A usage synopsis is the calling convention itself and cannot differ.
        shared = {s for s in ngrams(text) & ngrams(page) if not s.startswith('usage:')}
        if shared:
            findings.append((applet, sorted(shared)))

    print(f'compared {compared} applets against their man pages')
    for applet, shared in findings:
        print(f'\n{applet}: {len(shared)} shared {WORDS}-word sequences')
        for phrase in shared:
            print(f'    "{phrase}"')
    if findings:
        print(f'\nFAIL: {len(findings)} applets reuse wording from the originals.')
        return 1
    print(f'OK: no {WORDS}-word sequence is shared with any original.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
