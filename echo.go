// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"strconv"
	"strings"
)

// cmdEcho implements a subset of echo(1): -n (no trailing newline) and
// -e (interpret backslash escapes). Flags are only recognized as a contiguous
// leading run, matching common shell-builtin behavior.
func cmdEcho(args []string) int {
	noNewline := false
	interpret := false

	// Parse leading flags. echo is permissive: a "flag" that isn't valid is
	// treated as a normal operand and stops flag parsing.
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			break
		}
		ok := true
		for _, c := range a[1:] {
			if c != 'n' && c != 'e' && c != 'E' {
				ok = false
				break
			}
		}
		if !ok {
			break
		}
		for _, c := range a[1:] {
			switch c {
			case 'n':
				noNewline = true
			case 'e':
				interpret = true
			case 'E':
				interpret = false
			}
		}
	}

	out := strings.Join(args[i:], " ")
	stop := false
	if interpret {
		out, stop = expandEscapes(out)
	}
	if _, err := os.Stdout.WriteString(out); err != nil {
		fatalf("echo", "write error: %v", err)
		return 1
	}
	if !noNewline && !stop {
		if _, err := os.Stdout.WriteString("\n"); err != nil {
			fatalf("echo", "write error: %v", err)
			return 1
		}
	}
	return 0
}

// expandEscapes interprets C-style backslash escapes recognized by `echo -e`.
func expandEscapes(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '\\':
			b.WriteByte('\\')
		case '0':
			// \0NNN: up to 3 octal digits.
			j := i + 1
			for j < len(s) && j < i+4 && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			if v, err := strconv.ParseInt(s[i+1:j], 8, 16); err == nil {
				// echo \0NNN denotes an eight-bit value; mask to a byte.
				b.WriteByte(byte(v & 0xFF)) //nolint:gosec // G115: masked to 8 bits
				i = j - 1
			} else {
				b.WriteByte(0)
			}
		case 'x':
			// \xHH: up to 2 hex digits.
			j := i + 1
			for j < len(s) && j < i+3 && isHex(s[j]) {
				j++
			}
			if j > i+1 {
				v, _ := strconv.ParseInt(s[i+1:j], 16, 16)
				b.WriteByte(byte(v & 0xFF)) //nolint:gosec // G115: at most two hex digits (<= 0xFF)
				i = j - 1
			} else {
				b.WriteByte('\\')
				b.WriteByte('x')
			}
		case 'c':
			// \c: suppress all further output.
			return b.String(), true
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String(), false
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
