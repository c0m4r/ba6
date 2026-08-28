// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	mode := resolveNone
	noNewline, zeroTerm := false, false
	// GNU readlink is silent on errors unless -v is given; -s/-q restore the
	// silence, so the last of -v/-s/-q wins.
	quiet := true
	var names []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && arg == "-f":
			mode = resolveLastMayMiss
		case parsing && arg == "-e":
			mode = resolveAllExist
		case parsing && arg == "-m":
			mode = resolveAnyMayMiss
		case parsing && (arg == "-n" || arg == "--no-newline"):
			noNewline = true
		case parsing && (arg == "-z" || arg == "--zero"):
			zeroTerm = true
		case parsing && (arg == "-q" || arg == "--quiet" || arg == "-s" || arg == "--silent"):
			quiet = true
		case parsing && (arg == "-v" || arg == "--verbose"):
			quiet = false
		case parsing && len(arg) > 1 && arg[0] == '-':
			fatalf("readlink", "invalid option %q", arg)
			return 1
		default:
			names = append(names, arg)
		}
	}
	if len(names) == 0 {
		fatalf("readlink", "missing operand")
		return 1
	}
	status := 0
	for _, name := range names {
		var value string
		var err error
		switch mode {
		case resolveNone:
			value, err = os.Readlink(name)
		default:
			value, err = resolvePath(name, mode)
		}
		if err != nil {
			if !quiet {
				fatalf("readlink", "%s: %s", name, errText(err))
			}
			status = 1
			continue
		}
		term := "\n"
		if zeroTerm {
			term = "\x00"
		} else if noNewline {
			term = ""
		}
		if _, err := fmt.Fprint(os.Stdout, value+term); err != nil {
			fatalf("readlink", "write error: %v", err)
			return 1
		}
	}
	return status
}

func cmdRealpath(args []string) int {
	mode := resolveLastMayMiss
	strip, logical := false, false
	quiet, zeroTerm := false, false
	relTo, relBase := "", ""
	var names []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-e" || arg == "--canonicalize-existing"):
			mode = resolveAllExist
		case parsing && (arg == "-m" || arg == "--canonicalize-missing"):
			mode = resolveAnyMayMiss
		case parsing && (arg == "-s" || arg == "--strip"):
			strip = true
		case parsing && (arg == "-L" || arg == "--logical"):
			logical = true
			strip = false
		case parsing && (arg == "-P" || arg == "--physical"):
			logical = false
			strip = false
		case parsing && (arg == "-q" || arg == "--quiet"):
			quiet = true
		case parsing && (arg == "-z" || arg == "--zero"):
			zeroTerm = true
		case parsing && strings.HasPrefix(arg, "--relative-to="):
			relTo = strings.TrimPrefix(arg, "--relative-to=")
		case parsing && strings.HasPrefix(arg, "--relative-base="):
			relBase = strings.TrimPrefix(arg, "--relative-base=")
		case parsing && len(arg) > 1 && arg[0] == '-':
			fatalf("realpath", "invalid option %q", arg)
			return 1
		default:
			names = append(names, arg)
		}
	}
	if len(names) == 0 {
		fatalf("realpath", "missing operand")
		return 1
	}
	status := 0
	for _, name := range names {
		resolved := ""
		var err error
		if strip {
			resolved, err = stripResolve(name)
		} else if logical {
			if resolved, err = stripResolve(name); err == nil {
				resolved, err = resolvePath(resolved, mode)
			}
		} else {
			resolved, err = resolvePath(name, mode)
		}
		if err == nil && relTo != "" {
			if base, e := resolvePath(relTo, resolveAnyMayMiss); e == nil {
				resolved, err = filepath.Rel(base, resolved)
			}
		} else if err == nil && relBase != "" {
			if rel, e := filepath.Rel(relBase, resolved); e == nil && !strings.HasPrefix(rel, "..") {
				resolved = rel
			}
		}
		if err != nil {
			if !quiet {
				fatalf("realpath", "%s: %s", name, errText(err))
			}
			status = 1
			continue
		}
		term := "\n"
		if zeroTerm {
			term = "\x00"
		}
		if _, err := fmt.Fprint(os.Stdout, resolved+term); err != nil {
			fatalf("realpath", "write error: %v", err)
			return 1
		}
	}
	return status
}

// resolveMode says how missing path components are treated by resolvePath.
type resolveMode int

const (
	resolveNone        resolveMode = iota // print the link target itself (readlink)
	resolveAllExist                       // -e: every component must exist
	resolveLastMayMiss                    // -f / realpath default: the final component may be missing
	resolveAnyMayMiss                     // -m: nothing need exist; the rest is resolved lexically
)

// resolvePath canonicalizes an absolute path, following symlinks in every
// component that exists. resolveAllExist fails on any missing component;
// resolveLastMayMiss only allows the final component to be missing;
// resolveAnyMayMiss resolves the surviving prefix and appends the remaining
// components lexically.
func resolvePath(path string, mode resolveMode) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	comps := []string{}
	for _, comp := range strings.Split(abs, string(filepath.Separator)) {
		if comp != "" && comp != "." {
			comps = append(comps, comp)
		}
	}
	result := string(filepath.Separator)
	for i, comp := range comps {
		candidate := filepath.Join(result, comp)
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			result = resolved
			continue
		}
		last := i == len(comps)-1
		switch mode {
		case resolveAllExist:
			return "", err
		case resolveLastMayMiss:
			if !last {
				return "", err
			}
			result = candidate
		case resolveAnyMayMiss:
			for j := i; j < len(comps); j++ {
				if comps[j] == ".." {
					result = filepath.Dir(result)
				} else {
					result = filepath.Join(result, comps[j])
				}
			}
			return result, nil
		}
	}
	return result, nil
}

// stripResolve collapses "." and ".." components lexically, without expanding
// any symlink, and makes the path absolute. realpath -s and -L use it.
func stripResolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	comps := []string{}
	for _, comp := range strings.Split(abs, string(filepath.Separator)) {
		switch comp {
		case "", ".":
		case "..":
			if len(comps) > 0 {
				comps = comps[:len(comps)-1]
			}
		default:
			comps = append(comps, comp)
		}
	}
	return string(filepath.Separator) + strings.Join(comps, string(filepath.Separator)), nil
}
