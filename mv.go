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

// mvOptions is one mv(1) command line.
type mvOptions struct {
	force         bool
	noClobber     bool
	interactive   bool
	update        bool
	verbose       bool
	noTargetDir   bool // -T
	targetDir     string
	haveTargetDir bool // -t
	stripSlashes  bool
	backup        backupMethod
	suffix        string
}

func mvUsage(format string, a ...interface{}) int {
	fatalf("mv", format, a...)
	fmt.Fprintln(os.Stderr, "Try 'mv --help' for more information.")
	return 1
}

// cmdMv implements mv(1): rename or move files, falling back to copy and
// remove when a rename crosses a filesystem (EXDEV).
//
//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func cmdMv(args []string) int {
	options := mvOptions{suffix: defaultBackupSuffix()}
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
					mvUsage("option '%s' requires an argument", name)
					return "", false
				}
				return args[i], true
			}
			switch name {
			case "--force":
				options.force, options.noClobber, options.interactive = true, false, false
			case "--no-clobber":
				options.noClobber, options.force, options.interactive = true, false, false
			case "--interactive":
				options.interactive, options.noClobber, options.force = true, false, false
			case "--update":
				options.update = true
			case "--verbose":
				options.verbose = true
			case "--no-target-directory":
				options.noTargetDir = true
			case "--strip-trailing-slashes":
				options.stripSlashes = true
			case "--context":
				// SELinux labelling; nothing to do without a policy.
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
					method, ok := defaultBackupMethod("mv")
					if !ok {
						return 1
					}
					options.backup = method
					continue
				}
				method, ok := parseBackupControl(value)
				if !ok {
					return backupUsageError("mv", "backup type", value)
				}
				options.backup = method
			default:
				return mvUsage("unrecognized option '%s'", arg)
			}
			continue
		}
		cluster := arg[1:]
		for len(cluster) > 0 {
			flag := cluster[0]
			cluster = cluster[1:]
			switch flag {
			case 'f':
				options.force, options.noClobber, options.interactive = true, false, false
			case 'n':
				options.noClobber, options.force, options.interactive = true, false, false
			case 'i':
				options.interactive, options.noClobber, options.force = true, false, false
			case 'u':
				options.update = true
			case 'v':
				options.verbose = true
			case 'T':
				options.noTargetDir = true
			case 'Z':
				// SELinux labelling; nothing to do without a policy.
			case 'b':
				method, ok := defaultBackupMethod("mv")
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
						return mvUsage("option requires an argument -- '%c'", flag)
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
				return mvUsage("invalid option -- '%c'", flag)
			}
		}
	}

	sources, destination := operands, ""
	destinationIsDir := true
	switch {
	case options.haveTargetDir:
		destination = options.targetDir
		if len(operands) == 0 {
			return mvUsage("missing file operand")
		}
		info, err := os.Stat(destination)
		if err == nil && !info.IsDir() {
			err = syscall.ENOTDIR
		}
		if err != nil {
			fatalf("mv", "target directory '%s': %s", destination, errText(err))
			return 1
		}
	case len(operands) == 0:
		return mvUsage("missing file operand")
	case len(operands) == 1:
		return mvUsage("missing destination file operand after '%s'", operands[0])
	default:
		sources, destination = operands[:len(operands)-1], operands[len(operands)-1]
		info, err := os.Stat(destination)
		destinationIsDir = !options.noTargetDir && err == nil && info.IsDir()
		if len(sources) > 1 && !destinationIsDir {
			if err == nil {
				err = syscall.ENOTDIR
			}
			fatalf("mv", "target '%s': %s", destination, errText(err))
			return 1
		}
	}

	status := 0
	input := bufio.NewReader(os.Stdin)
	for _, source := range sources {
		if options.stripSlashes {
			// Only a real trailing slash is dropped; "/" itself keeps its name.
			if trimmed := strings.TrimRight(source, "/"); trimmed != "" {
				source = trimmed
			}
		}
		target := destination
		if destinationIsDir {
			target = filepath.Join(destination, filepath.Base(filepath.Clean(source)))
		}
		if !options.moveOne(input, source, target) {
			status = 1
		}
	}
	return status
}

// moveOne moves one file and reports whether it succeeded. A declined -i prompt
// counts as a failure, as it does in the original.
func (options *mvOptions) moveOne(input *bufio.Reader, source, target string) bool {
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		fatalf("mv", "cannot stat '%s': %s", source, errText(err))
		return false
	}
	targetInfo, targetErr := os.Lstat(target)
	if targetErr == nil {
		if os.SameFile(sourceInfo, targetInfo) {
			fatalf("mv", "'%s' and '%s' are the same file", source, target)
			return false
		}
		if options.noClobber {
			return true
		}
		// -u keeps the destination when it is at least as new as the source.
		if options.update && !sourceInfo.ModTime().After(targetInfo.ModTime()) {
			return true
		}
		if options.interactive {
			confirmed, confirmErr := confirm(input, fmt.Sprintf("mv: overwrite '%s'? ", target))
			if confirmErr != nil || !confirmed {
				return false
			}
		}
	}
	// A directory moved into itself would vanish; mv refuses by name.
	if sourceInfo.IsDir() {
		clean := filepath.Clean(source)
		if strings.HasPrefix(filepath.Clean(target), clean+string(os.PathSeparator)) {
			fatalf("mv", "cannot move '%s' to a subdirectory of itself, '%s'", source, target)
			return false
		}
	}
	backup := ""
	if targetErr == nil {
		backup, err = makeBackup(target, options.backup, options.suffix)
		if err != nil {
			fatalf("mv", "cannot backup '%s': %s", target, errText(err))
			return false
		}
		// A directory and a plain file never replace one another, and mv says
		// which way round the mismatch is rather than passing on an errno. A
		// backup has already moved the destination aside, so nothing is left
		// to conflict with.
		if backup == "" {
			switch {
			case targetInfo.IsDir() && !sourceInfo.IsDir():
				fatalf("mv", "cannot overwrite directory '%s' with non-directory '%s'", target, source)
				return false
			case !targetInfo.IsDir() && sourceInfo.IsDir():
				fatalf("mv", "cannot overwrite non-directory '%s' with directory '%s'", target, source)
				return false
			}
		}
	}
	if err := moveFile(source, target, options.force || options.interactive || backup != ""); err != nil {
		// Replacing a directory that still holds something is reported against
		// the destination rather than as a failed move.
		var errno syscall.Errno
		switch {
		case (errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)) &&
			targetErr == nil && targetInfo.IsDir():
			fatalf("mv", "cannot overwrite '%s': %s", target, errText(syscall.ENOTEMPTY))
		case errors.As(err, &errno):
			fatalf("mv", "cannot move '%s' to '%s': %s", source, target, errText(err))
		default:
			fatalf("mv", "%s", errText(err))
		}
		return false
	}
	if options.verbose {
		suffix := ""
		if backup != "" {
			suffix = fmt.Sprintf(" (backup: '%s')", backup)
		}
		if _, writeErr := fmt.Fprintf(os.Stdout, "renamed '%s' -> '%s'%s\n", source, target, suffix); writeErr != nil {
			fatalf("mv", "write error: %s", errText(writeErr))
			return false
		}
	}
	return true
}

// moveFile renames one path over another, falling back to a copy and a delete
// when the rename would cross a filesystem. replace says an existing
// destination may be removed to make the rename go through.
func moveFile(src, dst string, replace bool) error {
	_, dstErr := os.Lstat(dst)
	// syscall.Rename rather than os.Rename: the library call refuses outright
	// to rename anything onto an existing directory, where the system call
	// replaces an empty one, which is what mv relies on.
	err := syscall.Rename(src, dst)
	if err == nil {
		return nil
	}
	force := replace
	if force && dstErr == nil && !isCrossDevice(err) {
		if removeErr := os.Remove(dst); removeErr == nil {
			retryErr := syscall.Rename(src, dst)
			if retryErr == nil {
				return nil
			}
			err = retryErr
		}
	}

	// Cross-device rename: fall back to recursive copy + delete.
	if isCrossDevice(err) {
		if dstErr == nil {
			if removeErr := os.Remove(dst); removeErr != nil {
				return removeErr
			}
		}
		c := &copier{recursive: true, force: true, links: map[fileKey]string{}}
		c.setPreserveAll(true)
		if cErr := c.copyPath(src, dst, true); cErr != nil {
			return cErr
		}
		if removeErr := os.RemoveAll(src); removeErr != nil {
			return fmt.Errorf("cannot remove '%s': %s", src, errText(removeErr))
		}
		return nil
	}
	// The bare errno is returned: the caller decides how to describe it, since
	// mv words a few of these failures against the destination alone.
	return err
}
