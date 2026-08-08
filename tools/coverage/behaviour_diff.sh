#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 c0m4r
#
# Run ba6 and the original side by side on identical input and report where they
# disagree in stdout, stderr or exit status. This is what catches the differences
# an option count cannot: column widths, error wording, rounding, ordering.
#
# SAFETY: only applets named in ALLOWLIST below are ever executed, and only in a
# throwaway directory. Never generate this list from main.go — that registry
# contains halt, poweroff, reboot and init. Add entries by hand, and only for
# applets that cannot alter the system when run with these arguments.

# No -e: this harness runs commands that are expected to fail (missing files,
# mismatched operands) and must keep going to compare how each tool reports them.
set -u

BA6=${BA6:-$(cd "$(dirname "$0")/../.." && pwd)/ba6}
[ -x "$BA6" ] || { echo "build ba6 first (make build)" >&2; exit 1; }

ALLOWLIST="cat head tail wc sort uniq cut tr grep sed awk seq base64 sha256sum
strings echo printf expr basename dirname realpath readlink date env printenv id
whoami uname uptime free pwd df du ls stat file find cmp diff which test true
false hexdump od tree"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

printf 'alpha\nbeta\n\n\ngamma\tdelta\nalpha\n' > f.txt
printf 'x,y,z\n1,2,3\n' > c.csv
printf '\000\001hello world\177' > bin.dat
mkdir -p d/sub && touch d/sub/one d/two

same=0
diff=0

allowed() {
	for entry in $ALLOWLIST; do
		[ "$entry" = "$1" ] && return 0
	done
	return 1
}

run() {
	applet=$1
	shift
	if ! allowed "$applet"; then
		echo "REFUSED  $applet is not in ALLOWLIST" >&2
		return
	fi
	original=$(command -v "$applet" 2>/dev/null) || {
		echo "SKIP     $applet not installed on this system"
		return
	}
	a=$("$original" "$@" 2>&1 </dev/null; echo "rc=$?")
	b=$("$BA6" "$applet" "$@" 2>&1 </dev/null; echo "rc=$?")
	if [ "$a" = "$b" ]; then
		same=$((same + 1))
		echo "SAME     $applet $*"
	else
		diff=$((diff + 1))
		echo "DIFF     $applet $*"
		printf '%s\n' "$a" > .expected
		printf '%s\n' "$b" > .actual
		diff -u .expected .actual | tail -n +3 | head -12 | sed 's/^/         /' || true
	fi
}

run cat f.txt
run cat -n f.txt
run cat -A f.txt
run cat nosuch.txt
run head -n 2 f.txt
run head -c 5 f.txt
run tail -n 2 f.txt
run tail -n +2 f.txt
run wc f.txt
run wc -l f.txt
run sort f.txt
run sort -u f.txt
run uniq -c f.txt
run cut -d, -f2 c.csv
run grep alpha f.txt
run grep -c alpha f.txt
run grep -n alpha f.txt
run sed -n 2p f.txt
run sed s/alpha/A/ f.txt
run awk '{print $1}' f.txt
run seq 5
run base64 f.txt
run sha256sum f.txt
run strings bin.dat
run basename /a/b/c.txt
run dirname /a/b/c.txt
run ls -l f.txt
run stat -c '%n %s %F' f.txt
run du -sh d
run cmp f.txt c.csv
run find d -type f
run id -u
run uname -sm
# tree is skipped where the package is absent, which is the case on the machine
# these measurements were taken on.
run tree d
run tree -L 1 --noreport d

echo
echo "$same identical, $diff differing"
