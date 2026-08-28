// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdBasename(args []string) int {
	multiple := false
	zero := false
	suffix := ""
	suffixSet := false
	var operands []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		case arg == "-a" || arg == "--multiple":
			multiple = true
		case arg == "-z" || arg == "--zero":
			zero = true
		case arg == "-s" || arg == "--suffix":
			i++
			if i >= len(args) {
				fatalf("basename", "option requires an argument -- 's'")
				return 1
			}
			suffix, suffixSet, multiple = args[i], true, true
		case strings.HasPrefix(arg, "--suffix="):
			suffix, suffixSet, multiple = strings.TrimPrefix(arg, "--suffix="), true, true
		case len(arg) > 1 && arg[0] == '-':
			fatalf("basename", "invalid option %q", arg)
			return 1
		default:
			operands = append(operands, arg)
		}
	}
	if len(operands) == 0 {
		fatalf("basename", "missing operand")
		return 1
	}
	if !multiple && len(operands) > 2 {
		fatalf("basename", "extra operand %q", operands[2])
		return 1
	}
	if !multiple && len(operands) == 2 {
		suffix = operands[1]
		suffixSet = true
		operands = operands[:1]
	}
	terminator := "\n"
	if zero {
		terminator = "\x00"
	}
	for _, name := range operands {
		base := filepath.Base(name)
		if name == "" {
			base = ""
		}
		if suffixSet && suffix != base && strings.HasSuffix(base, suffix) {
			base = strings.TrimSuffix(base, suffix)
		}
		if _, err := fmt.Fprint(os.Stdout, base+terminator); err != nil {
			fatalf("basename", "write error: %v", err)
			return 1
		}
	}
	return 0
}

func cmdDirname(args []string) int {
	var names []string
	zero := false
	parsing := true
	for _, arg := range args {
		if parsing && arg == "--" {
			parsing = false
			continue
		}
		if parsing && (arg == "-z" || arg == "--zero") {
			zero = true
			continue
		}
		if parsing && len(arg) > 1 && arg[0] == '-' {
			fatalf("dirname", "invalid option %q", arg)
			return 1
		}
		names = append(names, arg)
	}
	if len(names) == 0 {
		fatalf("dirname", "missing operand")
		return 1
	}
	terminator := "\n"
	if zero {
		terminator = "\x00"
	}
	for _, name := range names {
		if _, err := fmt.Fprint(os.Stdout, parentPath(name)+terminator); err != nil {
			fatalf("dirname", "write error: %v", err)
			return 1
		}
	}
	return 0
}

// parentPath strips the last component of name the way dirname(1) and POSIX
// define it. filepath.Dir cleans the path first, so it answers "/a/b" for
// "/a/b/" where dirname answers "/a": trailing slashes belong to the component
// being removed, and interior slashes are left exactly as given.
func parentPath(name string) string {
	trimmed := strings.TrimRight(name, "/")
	if trimmed == "" {
		if name == "" {
			return "."
		}
		return "/"
	}
	cut := strings.LastIndexByte(trimmed, '/')
	if cut < 0 {
		return "."
	}
	parent := strings.TrimRight(trimmed[:cut], "/")
	if parent == "" {
		return "/"
	}
	return parent
}
