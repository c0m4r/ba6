// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// lnUsage reports a command-line mistake the way ln does, with the "Try ..."
// line after the diagnostic.
func lnUsage(format string, a ...interface{}) int {
	fatalf("ln", format, a...)
	fmt.Fprintln(os.Stderr, "Try 'ln --help' for more information.")
	return 1
}

// lnOptions is one ln(1) command line.
type lnOptions struct {
	symbolic      bool
	force         bool
	interactive   bool
	noTargetDir   bool // -T
	targetDir     string
	haveTargetDir bool // -t
	noDereference bool // -n
	verbose       bool
	relative      bool // -r
	directory     bool // -d/-F, hard-linking a directory
	dereference   bool // -L, hard-link what a symbolic source points at
	backup        backupMethod
	suffix        string
}

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func cmdLn(args []string) int {
	options := lnOptions{suffix: defaultBackupSuffix()}
	var operands []string
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !parsing || arg == "-" || !strings.HasPrefix(arg, "-") {
			operands = append(operands, arg)
			continue
		}
		if arg == "--" {
			parsing = false
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := arg, "", false
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
			needValue := func() (string, bool) {
				if hasValue {
					return value, true
				}
				i++
				if i >= len(args) {
					fatalf("ln", "option '%s' requires an argument", name)
					return "", false
				}
				return args[i], true
			}
			switch name {
			case "--symbolic":
				options.symbolic = true
			case "--force":
				options.force, options.interactive = true, false
			case "--interactive":
				options.interactive, options.force = true, false
			case "--no-dereference":
				options.noDereference = true
			case "--no-target-directory":
				options.noTargetDir = true
			case "--verbose":
				options.verbose = true
			case "--relative":
				options.relative = true
			case "--directory":
				options.directory = true
			case "--logical":
				options.dereference = true
			case "--physical":
				options.dereference = false
			case "--target-directory":
				text, ok := needValue()
				if !ok {
					return 1
				}
				options.targetDir, options.haveTargetDir = text, true
			case "--suffix":
				text, ok := needValue()
				if !ok {
					return 1
				}
				options.suffix = text
				if options.backup == backupNone {
					options.backup = backupExisting
				}
			case "--backup":
				// An empty "--backup=" reads as a bare --backup.
				if !hasValue || value == "" {
					method, ok := defaultBackupMethod("ln")
					if !ok {
						return 1
					}
					options.backup = method
					continue
				}
				method, ok := parseBackupControl(value)
				if !ok {
					return backupUsageError("ln", "backup type", value)
				}
				options.backup = method
			default:
				return lnUsage("unrecognized option '%s'", arg)
			}
			continue
		}
		cluster := arg[1:]
		for len(cluster) > 0 {
			flag := cluster[0]
			cluster = cluster[1:]
			switch flag {
			case 's':
				options.symbolic = true
			case 'f':
				options.force, options.interactive = true, false
			case 'i':
				options.interactive, options.force = true, false
			case 'T':
				options.noTargetDir = true
			case 'n':
				options.noDereference = true
			case 'v':
				options.verbose = true
			case 'r':
				options.relative = true
			case 'd', 'F':
				options.directory = true
			case 'L':
				options.dereference = true
			case 'P':
				options.dereference = false
			case 'b':
				method, ok := defaultBackupMethod("ln")
				if !ok {
					return 1
				}
				options.backup = method
			case 'S', 't':
				value := cluster
				cluster = ""
				if value == "" {
					i++
					if i >= len(args) {
						return lnUsage("option requires an argument -- '%c'", flag)
					}
					value = args[i]
				}
				if flag == 'S' {
					options.suffix = value
					if options.backup == backupNone {
						options.backup = backupExisting
					}
					continue
				}
				options.targetDir, options.haveTargetDir = value, true
			default:
				return lnUsage("invalid option -- '%c'", flag)
			}
		}
	}
	if len(operands) == 0 {
		return lnUsage("missing file operand")
	}

	sources, destination := operands, "."
	destinationIsDir := true
	switch {
	case options.haveTargetDir:
		destination = options.targetDir
		info, err := os.Stat(destination)
		if err != nil {
			fatalf("ln", "failed to access '%s': %s", destination, errText(err))
			return 1
		}
		if !info.IsDir() {
			// -t names the directory itself, so ln says which one is not one
			// rather than reporting an errno for it.
			fatalf("ln", "target '%s' is not a directory", destination)
			return 1
		}
	case len(operands) == 1:
		// The one-operand form links the file into the current directory.
	default:
		sources, destination = operands[:len(operands)-1], operands[len(operands)-1]
		destinationIsDir = false
		if !options.noTargetDir {
			var info os.FileInfo
			var err error
			if options.noDereference {
				info, err = os.Lstat(destination)
			} else {
				info, err = os.Stat(destination)
			}
			destinationIsDir = err == nil && info.IsDir()
			// With more than one source the destination has to be a directory,
			// and ln reports why it is not: the errno when it could not be
			// looked at, ENOTDIR when it is simply something else.
			if len(sources) > 1 && !destinationIsDir {
				if err == nil {
					err = syscall.ENOTDIR
				}
				fatalf("ln", "target '%s': %s", destination, errText(err))
				return 1
			}
		}
		if len(sources) > 1 && !destinationIsDir {
			fatalf("ln", "target '%s' is not a directory", destination)
			return 1
		}
	}

	status := 0
	input := bufio.NewReader(os.Stdin)
	for _, source := range sources {
		target := destination
		if destinationIsDir {
			target = filepath.Join(destination, filepath.Base(filepath.Clean(source)))
		}
		if !options.linkOne(input, source, target) {
			status = 1
		}
	}
	return status
}

// linkOne makes one link and reports whether it succeeded. Every diagnostic
// this can produce is the original's, including its choice of naming the source
// only for the errors where ln does.
func (options *lnOptions) linkOne(input *bufio.Reader, source, target string) bool {
	link := source
	if options.relative {
		// -r rewrites the link body as a path from the link's own directory,
		// which is what makes the result survive being moved with its tree.
		resolvedSource, sourceErr := resolvePath(source, resolveAnyMayMiss)
		resolvedDir, dirErr := resolvePath(filepath.Dir(target), resolveAnyMayMiss)
		if sourceErr == nil && dirErr == nil {
			if relative, err := filepath.Rel(resolvedDir, resolvedSource); err == nil {
				link = relative
			}
		}
	}
	if !options.symbolic {
		// A hard link needs the source to exist; -L follows a symbolic source
		// to the file it names, where the default links the symlink itself.
		info, err := os.Lstat(source)
		if err != nil {
			fatalf("ln", "failed to access '%s': %s", source, errText(err))
			return false
		}
		if info.IsDir() && !options.directory {
			// The kernel refuses this for everyone but root, but ln checks it
			// itself so the message names the directory rather than the errno.
			fatalf("ln", "%s: hard link not allowed for directory", source)
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 && options.dereference {
			resolved, resolveErr := filepath.EvalSymlinks(source)
			if resolveErr != nil {
				fatalf("ln", "failed to access '%s': %s", source, errText(resolveErr))
				return false
			}
			link = resolved
		}
		if targetInfo, targetErr := os.Lstat(target); targetErr == nil && os.SameFile(info, targetInfo) {
			fatalf("ln", "'%s' and '%s' are the same file", source, target)
			return false
		}
	}
	// The existing destination is asked about, backed up, then removed, in the
	// originals' order: a declined prompt fails without a message of its own.
	existing, existingErr := os.Lstat(target)
	if existingErr == nil {
		if options.interactive {
			confirmed, err := confirm(input, fmt.Sprintf("ln: replace '%s'? ", target))
			if err != nil || !confirmed {
				return false
			}
		}
		if options.interactive || options.force || options.backup != backupNone {
			backup, err := makeBackup(target, options.backup, options.suffix)
			if err != nil {
				fatalf("ln", "cannot backup '%s': %s", target, errText(err))
				return false
			}
			if backup == "" {
				if existing.IsDir() {
					fatalf("ln", "cannot overwrite directory '%s'", target)
					return false
				}
				if err := os.Remove(target); err != nil {
					fatalf("ln", "cannot remove '%s': %s", target, errText(err))
					return false
				}
			} else if options.verbose {
				fmt.Fprintf(os.Stdout, "'%s' ~ ", backup)
			}
		}
	}
	var err error
	if options.symbolic {
		err = os.Symlink(link, target)
	} else {
		err = os.Link(link, target)
	}
	if err != nil {
		options.reportLinkError(err, source, target)
		return false
	}
	if options.verbose {
		arrow := "=>"
		if options.symbolic {
			arrow = "->"
		}
		fmt.Fprintf(os.Stdout, "'%s' %s '%s'\n", target, arrow, link)
	}
	return true
}

// reportLinkError follows ln's own rule for which failures name the source:
// the ones that are about the destination alone do not.
func (options *lnOptions) reportLinkError(err error, source, target string) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		if pathErr, ok := err.(*os.LinkError); ok { //nolint:errorlint // *os.LinkError is what os.Link and os.Symlink return.
			err = pathErr.Err
			_ = errors.As(err, &errno)
		}
	}
	switch {
	case options.symbolic:
		fatalf("ln", "failed to create symbolic link '%s': %s", target, errText(err))
	case errno == syscall.EMLINK:
		fatalf("ln", "failed to create hard link to '%s': %s", source, errText(err))
	case errno == syscall.EEXIST || errno == syscall.EDQUOT || errno == syscall.ENOSPC || errno == syscall.EROFS:
		fatalf("ln", "failed to create hard link '%s': %s", target, errText(err))
	default:
		fatalf("ln", "failed to create hard link '%s' => '%s': %s", target, source, errText(err))
	}
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
