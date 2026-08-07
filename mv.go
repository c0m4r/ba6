// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
)

// cmdMv implements mv(1): rename/move files. Flags: -f (force), -v (verbose),
// -n (no-clobber). Falls back to copy+remove when renaming across filesystems
// (EXDEV). Multiple sources require the destination to be a directory.
func cmdMv(args []string) int {
	var (
		force       bool
		verbose     bool
		noClobber   bool
		interactive bool
		srcs        []string
	)

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "--force":
			force = true
		case a == "--verbose":
			verbose = true
		case a == "--no-clobber":
			noClobber = true
		case a == "--interactive":
			interactive, noClobber, force = true, false, false
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				switch c {
				case 'f':
					force, noClobber, interactive = true, false, false
				case 'n':
					noClobber, force, interactive = true, false, false
				case 'v':
					verbose = true
				case 'i':
					interactive, noClobber, force = true, false, false
				default:
					fatalf("mv", "invalid option -- '%c'", c)
					return 1
				}
			}
		default:
			srcs = append(srcs, a)
		}
	}
rest:
	srcs = append(srcs, args[i:]...)
	if len(srcs) < 2 {
		fatalf("mv", "missing destination file operand")
		return 1
	}

	dst := srcs[len(srcs)-1]
	srcs = srcs[:len(srcs)-1]

	dstInfo, dstErr := os.Stat(dst)
	dstIsDir := dstErr == nil && dstInfo.IsDir()
	if len(srcs) > 1 && !dstIsDir {
		fatalf("mv", "target '%s' is not a directory", dst)
		return 1
	}

	status := 0
	input := bufio.NewReader(os.Stdin)
	for _, src := range srcs {
		target := dst
		if dstIsDir {
			target = filepath.Join(dst, filepath.Base(src))
		}
		if interactive {
			if _, err := os.Lstat(target); err == nil {
				confirmed, confirmErr := confirm(input, fmt.Sprintf("mv: overwrite '%s'? ", target))
				if confirmErr != nil {
					fatalf("mv", "cannot read response: %s", errText(confirmErr))
					status = 1
					continue
				}
				if !confirmed {
					continue
				}
			}
		}
		moved, err := moveOne(src, target, force, noClobber)
		if err != nil {
			fatalf("mv", "%s", errText(err))
			status = 1
			continue
		}
		if verbose && moved {
			if _, writeErr := fmt.Fprintf(os.Stdout, "'%s' -> '%s'\n", src, target); writeErr != nil {
				fatalf("mv", "write error: %s", errText(writeErr))
				status = 1
			}
		}
	}
	return status
}

func moveOne(src, dst string, force, noClobber bool) (bool, error) {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return false, fmt.Errorf("cannot stat '%s': %s", src, errText(err))
	}
	dstInfo, dstErr := os.Lstat(dst)
	if dstErr == nil {
		if os.SameFile(srcInfo, dstInfo) {
			return false, nil
		}
		if noClobber {
			return false, nil
		}
	}

	err = os.Rename(src, dst)
	if err == nil {
		return true, nil
	}
	if force && dstErr == nil && !isCrossDevice(err) {
		if removeErr := os.Remove(dst); removeErr == nil {
			if retryErr := os.Rename(src, dst); retryErr == nil {
				return true, nil
			} else {
				err = retryErr
			}
		}
	}

	// Cross-device rename: fall back to recursive copy + delete.
	if isCrossDevice(err) {
		if dstErr == nil {
			if removeErr := os.Remove(dst); removeErr != nil {
				return false, removeErr
			}
		}
		c := &copier{recursive: true, force: true, preserve: true}
		if cErr := c.copyPath(src, dst); cErr != nil {
			return false, cErr
		}
		if removeErr := os.RemoveAll(src); removeErr != nil {
			return false, fmt.Errorf("cannot remove '%s': %s", src, errText(removeErr))
		}
		return true, nil
	}
	return false, fmt.Errorf("cannot move '%s' to '%s': %s", src, dst, errText(err))
}
