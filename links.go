// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// cmdLn implements a focused ln(1): hard and symbolic links, optional
// replacement, and the usual directory-target behavior.
func cmdLn(args []string) int {
	var symbolic, force, noTargetDir, noDereference, verbose bool
	var operands []string
	parsing := true
	for _, arg := range args {
		if parsing && arg == "--" {
			parsing = false
			continue
		}
		if parsing && arg == "--no-dereference" {
			noDereference = true
			continue
		}
		if parsing && len(arg) > 1 && arg[0] == '-' {
			for _, flag := range arg[1:] {
				switch flag {
				case 's':
					symbolic = true
				case 'f':
					force = true
				case 'T':
					noTargetDir = true
				case 'n':
					noDereference = true
				case 'v':
					verbose = true
				default:
					fatalf("ln", "invalid option -- '%c'", flag)
					return 1
				}
			}
			continue
		}
		operands = append(operands, arg)
	}
	if len(operands) == 0 {
		fatalf("ln", "missing file operand")
		return 1
	}

	sources := operands
	destination := "."
	if len(operands) > 1 {
		sources = operands[:len(operands)-1]
		destination = operands[len(operands)-1]
	}
	destinationIsDir := false
	if !noTargetDir {
		var info os.FileInfo
		var err error
		if noDereference {
			info, err = os.Lstat(destination)
		} else {
			info, err = os.Stat(destination)
		}
		if err == nil {
			destinationIsDir = info.IsDir()
		}
	}
	if len(sources) > 1 && !destinationIsDir {
		fatalf("ln", "target '%s' is not a directory", destination)
		return 1
	}

	status := 0
	for _, source := range sources {
		target := destination
		if destinationIsDir {
			target = filepath.Join(destination, filepath.Base(filepath.Clean(source)))
		}
		if err := makeLink(source, target, symbolic, force); err != nil {
			fatalf("ln", "failed to create link '%s': %v", target, err)
			status = 1
			continue
		}
		if verbose {
			if _, err := fmt.Fprintf(os.Stdout, "'%s' -> '%s'\n", target, source); err != nil {
				fatalf("ln", "write error: %v", err)
				return 1
			}
		}
	}
	return status
}

func makeLink(source, target string, symbolic, force bool) error {
	if sourceInfo, err := os.Lstat(source); !symbolic {
		if err != nil {
			return err
		}
		if targetInfo, targetErr := os.Lstat(target); targetErr == nil && os.SameFile(sourceInfo, targetInfo) {
			return fmt.Errorf("'%s' and '%s' are the same file", source, target)
		}
	}
	if force {
		if info, err := os.Lstat(target); err == nil {
			if info.IsDir() {
				return fmt.Errorf("cannot overwrite directory")
			}
			if err := os.Remove(target); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if symbolic {
		return os.Symlink(source, target)
	}
	return os.Link(source, target)
}

func cmdReadlink(args []string) int {
	canonical := false
	var names []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && arg == "-f":
			canonical = true
		case parsing && len(arg) > 1 && arg[0] == '-':
			fatalf("readlink", "invalid option %q", arg)
			return 1
		default:
			names = append(names, arg)
		}
	}
	if len(names) != 1 {
		fatalf("readlink", "expected exactly one file operand")
		return 1
	}
	var value string
	var err error
	if canonical {
		value, err = filepath.EvalSymlinks(names[0])
		if err == nil {
			value, err = filepath.Abs(value)
		}
	} else {
		value, err = os.Readlink(names[0])
	}
	if err != nil {
		fatalf("readlink", "%s: %v", names[0], err)
		return 1
	}
	if _, err := fmt.Fprintln(os.Stdout, value); err != nil {
		fatalf("readlink", "write error: %v", err)
		return 1
	}
	return 0
}

func cmdRealpath(args []string) int {
	var names []string
	parsing := true
	for _, arg := range args {
		if parsing && arg == "--" {
			parsing = false
			continue
		}
		if parsing && len(arg) > 1 && arg[0] == '-' {
			fatalf("realpath", "invalid option %q", arg)
			return 1
		}
		names = append(names, arg)
	}
	if len(names) == 0 {
		fatalf("realpath", "missing operand")
		return 1
	}
	status := 0
	for _, name := range names {
		resolved, err := filepath.EvalSymlinks(name)
		if err == nil {
			resolved, err = filepath.Abs(resolved)
		}
		if err != nil {
			fatalf("realpath", "%s: %v", name, err)
			status = 1
			continue
		}
		if _, err := fmt.Fprintln(os.Stdout, resolved); err != nil {
			fatalf("realpath", "write error: %v", err)
			return 1
		}
	}
	return status
}
