#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 c0m4r
"""Reference option groups for each applet, read from the system man pages.

SAFETY: this reads man pages only. It never executes a tool to ask for its help,
because ba6's applet list contains halt, poweroff, reboot and init, and a flag the
real tool ignores turns a "--help" probe into that command. See README.md.
"""
import json
import re
import subprocess
import sys

from ba6_options import applet_registry

# Option lines start with a dash a little way into the line; synonyms share a line,
# as in "-a, --all".
OPT_LINE = re.compile(r'^\s{1,10}(-[A-Za-z0-9?]|--[A-Za-z0-9])')
TOKEN = re.compile(r'(?<![\w./-])(--?[A-Za-z0-9][A-Za-z0-9_.-]*|[a-z]{2,6}=)')
CLUSTER = re.compile(r'\[-([A-Za-z0-9]{2,})\]')
SKIP = {'--help', '-h', '--version', '-V', '-?', '--usage'}


def man_page(tool):
    """Return the rendered man page for tool, or "" when there is none."""
    for section in ('1', '8', ''):
        argv = ['man'] + ([section] if section else []) + [tool]
        try:
            page = subprocess.run(argv, capture_output=True, text=True, timeout=15,
                                  stdin=subprocess.DEVNULL,
                                  env={'PATH': '/usr/bin:/bin', 'MANWIDTH': '110'})
        except Exception:
            continue
        if page.returncode == 0 and len(page.stdout) > 200:
            return subprocess.run(['col', '-b'], input=page.stdout,
                                  capture_output=True, text=True).stdout
    return ''


def option_groups(text):
    """Group synonymous spellings so that "-a, --all" counts as one option."""
    groups, seen = [], set()
    for line in text.splitlines():
        if OPT_LINE.match(line):
            head = re.split(r'\s{2,}', line.strip(), maxsplit=1)[0]
            tokens = []
            for match in TOKEN.finditer(head):
                token = match.group(1)
                if not token.endswith('='):
                    token = token.split('=')[0]
                if re.fullmatch(r'-\d+', token) or token in tokens:
                    continue
                tokens.append(token)
            if tokens and not all(t in seen for t in tokens):
                groups.append(tokens)
                seen.update(tokens)
            continue
        # Synopsis clusters such as [-abc] document one-letter options too.
        for cluster in CLUSTER.finditer(line):
            for letter in cluster.group(1):
                if '-' + letter not in seen:
                    seen.add('-' + letter)
                    groups.append(['-' + letter])
    return [g for g in groups if not any(t in SKIP for t in g)]


def main():
    out = {}
    for applet in sorted(applet_registry()):
        page = man_page(applet)
        if not page:
            print(f'no man page: {applet}', file=sys.stderr)
            continue
        out[applet] = option_groups(page)
    json.dump(out, sys.stdout, indent=1)


if __name__ == '__main__':
    main()
