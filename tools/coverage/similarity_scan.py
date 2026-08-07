#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 c0m4r
"""Check ba6's source for verbatim overlap with upstream implementations.

This is the check that backs PROVENANCE.md. It matters because much of ba6 was
written with AI assistance, where the risk is reproduced training data rather than
deliberate copying.

Two passes:

  1. Go against Go, token n-grams versus u-root -- the large Go implementation of
     these same utilities, and so the most plausible source of reproduction.
     Matches shorter than about 40 tokens are language boilerplate; Go's error
     handling and subprocess wiring cannot be written any other way.

  2. Prose string literals against the C implementations. Program names, format
     identifiers ("Linux swap") and stock phrases are expected and unprotectable.
     A whole sentence is not: on 2026-08-07 this found rm's two-line root
     safeguard copied verbatim from coreutils lib/root-dev-ino.h, and it was
     rewritten.

Fetch the corpora first (any versions will do):

    mkdir -p ~/.cache/ba6-license-scan && cd ~/.cache/ba6-license-scan
    curl -LO https://ftp.gnu.org/gnu/coreutils/coreutils-9.11.tar.xz
    curl -LO https://busybox.net/downloads/busybox-1.36.1.tar.bz2
    curl -LO https://www.kernel.org/pub/linux/utils/util-linux/v2.39/util-linux-2.39.3.tar.xz
    curl -L -o u-root.tar.gz https://github.com/u-root/u-root/archive/refs/tags/v0.14.0.tar.gz
    for a in *.tar.*; do d=${a%%.tar.*}; mkdir -p "$d"; tar xf "$a" -C "$d" --strip-components=1; done

    python3 similarity_scan.py ~/.cache/ba6-license-scan
"""
import collections
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
TOKENS = re.compile(r'[A-Za-z_][A-Za-z0-9_]*|\d+|[^\sA-Za-z0-9_]')
STRING = re.compile(r'"((?:[^"\\]|\\.){8,})"')
REPORT_AT = 40  # tokens; below this, matches are Go boilerplate


def go_tokens(source):
    source = re.sub(r'//[^\n]*', ' ', source)
    source = re.sub(r'/\*.*?\*/', ' ', source, flags=re.S)
    return TOKENS.findall(source)


def ngrams(tokens, n):
    return {tuple(tokens[i:i + n]) for i in range(len(tokens) - n + 1)}


def ba6_sources():
    return {f: open(os.path.join(ROOT, f), encoding='utf-8', errors='ignore').read()
            for f in sorted(os.listdir(ROOT)) if f.endswith('.go')}


def scan_go(corpus, sources):
    index = {}
    for name, src in sources.items():
        for gram in ngrams(go_tokens(src), REPORT_AT):
            index.setdefault(gram, name)
    hits, examples = collections.Counter(), {}
    for root, _, files in os.walk(corpus):
        for name in files:
            if not name.endswith('.go'):
                continue
            path = os.path.join(root, name)
            try:
                src = open(path, encoding='utf-8', errors='ignore').read()
            except OSError:
                continue
            for gram in ngrams(go_tokens(src), REPORT_AT):
                if gram in index:
                    key = (index[gram], os.path.relpath(path, corpus))
                    hits[key] += 1
                    examples.setdefault(key, ' '.join(gram))
    return hits, examples


def prose_strings(sources):
    found = set()
    for src in sources.values():
        found |= {m.group(1) for m in STRING.finditer(src)}
    return {s for s in found
            if len(re.findall(r'[a-z]{3,}', s)) >= 2 and not s.startswith('/')}


def scan_strings(corpus, prose):
    shared = set()
    for root, _, files in os.walk(corpus):
        for name in files:
            if not name.endswith(('.c', '.h')):
                continue
            try:
                src = open(os.path.join(root, name), encoding='utf-8', errors='ignore').read()
            except OSError:
                continue
            shared |= {m.group(1) for m in STRING.finditer(src) if m.group(1) in prose}
    return shared


def main():
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    base = sys.argv[1]
    sources = ba6_sources()
    print(f'ba6: {len(sources)} Go files\n')

    for entry in sorted(os.listdir(base)):
        corpus = os.path.join(base, entry)
        if not os.path.isdir(corpus):
            continue
        if any(f.endswith('.go') for _, _, fs in os.walk(corpus) for f in fs):
            hits, examples = scan_go(corpus, sources)
            print(f'=== {entry}: Go sequences of {REPORT_AT}+ tokens: '
                  f'{len(hits)} file pairs')
            for (ours, theirs), count in hits.most_common(10):
                print(f'  {count:3d}x  {ours}  <->  {theirs}')
                print(f'        {examples[(ours, theirs)][:120]}')
        else:
            shared = scan_strings(corpus, prose_strings(sources))
            print(f'=== {entry}: shared prose string literals: {len(shared)}')
            for text in sorted(shared):
                print(f'      {text!r}')
        print()
    return 0


if __name__ == '__main__':
    sys.exit(main())
