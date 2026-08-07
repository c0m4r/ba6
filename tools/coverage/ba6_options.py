#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 c0m4r
"""Option sets each ba6 applet accepts, extracted from the Go source.

Every top-level func is brace-matched, a crude call graph is built, and the flag
literals and option runes reachable from each cmdXxx entry point are unioned.

The result is a SUPERSET: `switch` statements over runes are also used for escape
sequences (tr), format directives (date, stat) and command letters (sed). Confirm
anything surprising with behaviour_diff.sh.
"""
import json
import os
import re
import sys
from collections import defaultdict

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

FUNC = re.compile(r'^func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(', re.M)
CALL = re.compile(r'\b([A-Za-z_][A-Za-z0-9_]*)\s*\(')
FLAG_LITERAL = re.compile(r'^--?[A-Za-z0-9][A-Za-z0-9_.-]*=?$')
RUNE_CASE = re.compile(r"case\s+((?:'(?:\\.|[^'])'(?:\s*,\s*)?)+)\s*:")
MAX_DEPTH = 3


def blank_literals(source):
    """Blank out strings, runes and comments; return the code plus what was removed."""
    out, strings, i, n = [], [], 0, len(source)
    while i < n:
        c = source[i]
        if c == '/' and i + 1 < n and source[i + 1] == '/':
            j = source.find('\n', i)
            j = n if j < 0 else j
        elif c == '/' and i + 1 < n and source[i + 1] == '*':
            j = source.find('*/', i + 2)
            j = n if j < 0 else j + 2
        elif c in '"`\'':
            closing = c
            j = i + 1
            buf = []
            while j < n:
                if source[j] == '\\' and closing != '`':
                    buf.append(source[j:j + 2])
                    j += 2
                    continue
                if source[j] == closing:
                    break
                buf.append(source[j])
                j += 1
            if c != "'":
                strings.append(''.join(buf))
            j = min(j + 1, n)
        else:
            out.append(c)
            i += 1
            continue
        out.append(' ' * (j - i))
        i = j
    return ''.join(out), strings


def parse_funcs():
    """Map every top-level func name to the list of its bodies."""
    funcs = defaultdict(list)
    for name in sorted(os.listdir(ROOT)):
        if not name.endswith('.go') or name.endswith('_test.go'):
            continue
        text = open(os.path.join(ROOT, name), encoding='utf-8').read()
        code, _ = blank_literals(text)
        for match in FUNC.finditer(code):
            open_brace = code.find('{', match.end())
            if open_brace < 0:
                continue
            depth, j = 0, open_brace
            while j < len(code):
                if code[j] == '{':
                    depth += 1
                elif code[j] == '}':
                    depth -= 1
                    if depth == 0:
                        break
                j += 1
            funcs[match.group(1)].append(text[open_brace:j + 1])
    return funcs


def rune_options(body):
    """Single-letter options taken from switch cases over runes."""
    found = set()
    for case in RUNE_CASE.finditer(body):
        for rune in re.finditer(r"'(\\.|[^'])'", case.group(1)):
            ch = rune.group(1)
            if len(ch) == 1 and ch.isalnum():
                found.add('-' + ch)
    return found


def applet_registry():
    """Applet name -> entry point func, read from main.go."""
    main = open(os.path.join(ROOT, 'main.go'), encoding='utf-8').read()
    return dict(re.findall(r'"([^"]+)":\s*(cmd[A-Za-z0-9_]+),', main))


def main():
    funcs = parse_funcs()
    direct, graph = {}, {}
    for name, bodies in funcs.items():
        flags, runes, callees = set(), set(), set()
        for body in bodies:
            code, strings = blank_literals(body)
            flags |= {s.rstrip('=') for s in strings if FLAG_LITERAL.match(s)}
            runes |= rune_options(body)
            callees |= {c for c in CALL.findall(code) if c in funcs and c != name}
        direct[name] = flags | runes
        graph[name] = callees

    out = {}
    for applet, entry in applet_registry().items():
        seen, stack, depth, options = set(), [entry], {entry: 0}, set()
        while stack:
            current = stack.pop()
            if current in seen or depth[current] > MAX_DEPTH:
                continue
            seen.add(current)
            options |= direct.get(current, set())
            for callee in graph.get(current, ()):
                depth.setdefault(callee, depth[current] + 1)
                stack.append(callee)
        out[applet] = sorted(options)
    json.dump(out, sys.stdout, indent=1)


if __name__ == '__main__':
    main()
