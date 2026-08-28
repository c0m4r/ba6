// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// cmdMkdir implements mkdir(1): -p (create parents, no error if existing),
// -m MODE (octal permissions for the final directory) and -v (report each
// directory that is actually created).
func cmdMkdir(args []string) int {
	args = expandShortOptions(args, "m")
	parents := false
	verbose := false
	mode := os.FileMode(0o777)
	modeSet := false
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
		case a == "-m" || a == "--mode":
			i++
			if i >= len(args) {
				fatalf("mkdir", "option requires an argument -- 'm'")
				return 1
			}
			modeArg := args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
			m, err := strconv.ParseUint(modeArg, 8, 32)
			if err != nil || m > 0o7777 {
				fatalf("mkdir", "invalid mode: %q", modeArg)
				return 1
			}
			mode, modeSet = fileModeFromOctal(m), true
		case len(a) > 1 && a[0] == '-':
			fatalf("mkdir", "invalid option %q", a)
			return 1
		default:
			dirs = append(dirs, a)
		}
	}
rest:
	dirs = append(dirs, args[i:]...)
	if len(dirs) == 0 {
		fatalf("mkdir", "missing operand")
		return 1
	}

	status := 0
	for _, d := range dirs {
		_, beforeErr := os.Stat(d)
		existed := beforeErr == nil
		var err error
		created := []string{}
		if parents {
			if verbose {
				created = missingParents(d)
			}
			err = os.MkdirAll(d, mode)
		} else {
			err = os.Mkdir(d, mode)
		}
		if err != nil {
			fatalf("mkdir", "cannot create directory '%s': %s", d, errText(err))
			status = 1
			continue
		}
		// MkdirAll/Mkdir apply umask; -m means the user asked for an exact
		// mode, so chmod the final directory to honor it.
		if modeSet && !existed {
			if err := os.Chmod(d, mode); err != nil {
				fatalf("mkdir", "cannot set mode on '%s': %s", d, errText(err))
				status = 1
			}
		}
		if verbose {
			if parents {
				for _, p := range created {
					fmt.Fprintf(os.Stdout, "mkdir: created directory '%s'\n", p)
				}
			} else if !existed {
				fmt.Fprintf(os.Stdout, "mkdir: created directory '%s'\n", d)
			}
		}
	}
	return status
}

// missingParents lists the components of dir that do not exist yet, from the
// first missing one down to dir itself, in the order mkdir -v -p reports them.
func missingParents(dir string) []string {
	var missing []string
	part := dir
	for {
		if _, err := os.Stat(part); err == nil {
			break
		}
		missing = append([]string{part}, missing...)
		parent := filepath.Dir(part)
		if parent == part || parent == "." {
			break
		}
		part = parent
	}
	return missing
}
