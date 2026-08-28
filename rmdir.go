// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// cmdRmdir implements rmdir(1): remove empty directories, with -p to remove
// empty parent directories too, --ignore-fail-on-non-empty to suppress
// non-empty errors, and -v to report each removal.
func cmdRmdir(args []string) int {
	parents := false
	verbose := false
	ignoreNonEmpty := false
	var dirs []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-p" || a == "--parents":
			parents = true
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "--ignore-fail-on-non-empty":
			ignoreNonEmpty = true
		case len(a) > 1 && a[0] == '-':
			fatalf("rmdir", "invalid option %q", a)
			return 1
		default:
			dirs = append(dirs, a)
		}
	}
rest:
	dirs = append(dirs, args[i:]...)
	if len(dirs) == 0 {
		fatalf("rmdir", "missing operand")
		return 1
	}

	status := 0
	for _, d := range dirs {
		if !removeOneDir(d, ignoreNonEmpty, verbose) {
			status = 1
			continue
		}
		if parents {
			for p := filepath.Dir(d); p != "." && p != "/" && p != ""; p = filepath.Dir(p) {
				if !removeOneDir(p, ignoreNonEmpty, verbose) {
					status = 1
					break
				}
			}
		}
	}
	return status
}

// removeOneDir removes d with rmdir(2) rather than os.Remove, which falls back
// to unlink(2) and would silently delete a regular file named on the command
// line. rmdir(1) must fail with ENOTDIR instead.
func removeOneDir(d string, ignoreNonEmpty, verbose bool) bool {
	err := syscall.Rmdir(d)
	if err == nil {
		if verbose {
			fmt.Fprintf(os.Stdout, "rmdir: removing directory, '%s'\n", d)
		}
		return true
	}
	if ignoreNonEmpty && isNotEmpty(err) {
		return true
	}
	fatalf("rmdir", "failed to remove '%s': %s", d, errText(err))
	return false
}

// isNotEmpty reports whether err indicates a directory-not-empty condition.
func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}
