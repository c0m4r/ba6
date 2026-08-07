// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// cmdRm implements rm(1): -r/-R (recursive), -f (force: ignore missing, no
// prompt), -i (prompt before each removal), -v (verbose), -d (remove empty
// dirs). Without -r, directories are refused.
func cmdRm(args []string) int {
	var (
		recursive    bool
		force        bool
		prompt       bool
		verbose      bool
		dirOK        bool
		preserveRoot = true
		targets      []string
	)

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "--recursive":
			recursive = true
		case a == "--force":
			force = true
		case a == "--verbose":
			verbose = true
		case a == "--interactive":
			prompt = true
		case a == "--preserve-root" || a == "--preserve-root=all":
			preserveRoot = true
		case a == "--no-preserve-root":
			preserveRoot = false
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				switch c {
				case 'r', 'R':
					recursive = true
				case 'f':
					force, prompt = true, false
				case 'i':
					prompt, force = true, false
				case 'v':
					verbose = true
				case 'd':
					dirOK = true
				default:
					fatalf("rm", "invalid option -- '%c'", c)
					return 1
				}
			}
		default:
			targets = append(targets, a)
		}
	}
rest:
	targets = append(targets, args[i:]...)
	if len(targets) == 0 {
		if force {
			return 0
		}
		fatalf("rm", "missing operand")
		return 1
	}

	stdin := bufio.NewReader(os.Stdin)
	status := 0
	for _, t := range targets {
		if recursive && preserveRoot && isRootPath(t) {
			fatalf("rm", "refusing to recurse into the filesystem root %q", t)
			fatalf("rm", "pass --no-preserve-root if that is genuinely intended")
			status = 1
			continue
		}
		info, err := os.Lstat(t)
		if err != nil {
			if force && errors.Is(err, os.ErrNotExist) {
				continue
			}
			fatalf("rm", "cannot remove '%s': %v", t, err)
			status = 1
			continue
		}

		if info.IsDir() && !recursive && !dirOK {
			fatalf("rm", "cannot remove '%s': Is a directory", t)
			status = 1
			continue
		}

		if prompt {
			confirmed, confirmErr := confirm(stdin, fmt.Sprintf("rm: remove '%s'? ", t))
			if confirmErr != nil {
				fatalf("rm", "cannot read response: %v", confirmErr)
				status = 1
				continue
			}
			if !confirmed {
				continue
			}
		}

		if info.IsDir() && recursive {
			err = os.RemoveAll(t)
		} else {
			err = os.Remove(t)
		}
		if err != nil {
			fatalf("rm", "cannot remove '%s': %v", t, err)
			status = 1
			continue
		}
		if verbose {
			if _, writeErr := fmt.Fprintf(os.Stdout, "removed '%s'\n", t); writeErr != nil {
				fatalf("rm", "write error: %v", writeErr)
				status = 1
			}
		}
	}
	return status
}

func isRootPath(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return filepath.Clean(abs) == string(os.PathSeparator)
}

// confirm prints prompt and returns true if the user's reply begins with y/Y.
func confirm(r *bufio.Reader, prompt string) (bool, error) {
	if _, err := fmt.Fprint(os.Stderr, prompt); err != nil {
		return false, err
	}
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return len(line) > 0 && (line[0] == 'y' || line[0] == 'Y'), nil
}
