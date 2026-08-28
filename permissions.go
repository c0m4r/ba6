// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// modeComputer derives a file's new mode from its current mode, given whether
// it is a directory (needed for symbolic 'X').
type modeComputer func(old os.FileMode, isDir bool) os.FileMode

func cmdChmod(args []string) int {
	args = expandShortOptions(args, "")
	recursive, verbose, changesOnly, quiet := false, false, false, false
	reference := ""
	haveReference := false
	var operands []string
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-R" || arg == "--recursive"):
			recursive = true
		case parsing && (arg == "-v" || arg == "--verbose"):
			verbose = true
		case parsing && (arg == "-c" || arg == "--changes"):
			changesOnly = true
		case parsing && (arg == "-f" || arg == "--silent" || arg == "--quiet"):
			quiet = true
		case parsing && arg == "--reference":
			i++
			if i >= len(args) {
				fatalf("chmod", "option '--reference' requires an argument")
				return 1
			}
			reference, haveReference = args[i], true
		case parsing && strings.HasPrefix(arg, "--reference="):
			reference, haveReference = strings.TrimPrefix(arg, "--reference="), true
		case parsing && len(arg) > 1 && arg[0] == '-':
			fatalf("chmod", "invalid option '%s'", arg)
			return 1
		default:
			operands = append(operands, arg)
		}
	}

	var compute modeComputer
	var files []string
	if haveReference {
		if len(operands) < 1 {
			fatalf("chmod", "missing file operand")
			return 1
		}
		refInfo, err := os.Stat(reference)
		if err != nil {
			fatalf("chmod", "cannot stat attributes of '%s': %v", reference, errText(err))
			return 1
		}
		target := refInfo.Mode()
		compute = func(os.FileMode, bool) os.FileMode { return target }
		files = operands
	} else {
		if len(operands) < 1 {
			fatalf("chmod", "missing operand")
			return 1
		}
		if len(operands) < 2 {
			fatalf("chmod", "missing operand after '%s'", operands[0])
			return 1
		}
		spec := operands[0]
		if len(spec) > 0 && spec[0] >= '0' && spec[0] <= '9' {
			parsed, err := strconv.ParseUint(strings.TrimPrefix(spec, "0o"), 8, 32)
			if err != nil || parsed > 0o7777 {
				fatalf("chmod", "invalid mode: '%s'", spec)
				return 1
			}
			target := fileModeFromOctal(parsed)
			compute = func(os.FileMode, bool) os.FileMode { return target }
		} else {
			ops, err := parseSymbolicMode(spec)
			if err != nil {
				fatalf("chmod", "%v", err)
				return 1
			}
			umask := currentUmask()
			compute = func(old os.FileMode, isDir bool) os.FileMode {
				bits := octalFromFileMode(old)
				for _, op := range ops {
					bits = applyChmodClause(bits, isDir, op, umask)
				}
				return fileModeFromOctal(bits)
			}
		}
		files = operands[1:]
	}

	if recursive {
		return chmodRecursive(files, compute, quiet, verbose, changesOnly)
	}
	return chmodApply(files, compute, quiet, verbose, changesOnly)
}

func chmodApply(paths []string, compute modeComputer, quiet, verbose, changesOnly bool) int {
	status := 0
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			if !quiet {
				fatalf("chmod", "cannot access '%s': %v", path, errText(err))
			}
			status = 1
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		newMode := compute(info.Mode(), info.IsDir())
		if err := os.Chmod(path, newMode); err != nil {
			if !quiet {
				fatalf("chmod", "cannot change '%s': %v", path, errText(err))
			}
			status = 1
			continue
		}
		reportChmod(path, info.Mode(), newMode, verbose, changesOnly)
	}
	return status
}

// chmodRecursive reads each directory before applying a possibly restrictive
// final mode. If a directory is initially unreadable, owner traversal bits are
// enabled temporarily so chmod can also be used to repair locked trees.
func chmodRecursive(roots []string, compute modeComputer, quiet, verbose, changesOnly bool) int {
	status := 0
	var visit func(string)
	visit = func(path string) {
		info, err := os.Lstat(path)
		if err != nil {
			if !quiet {
				fatalf("chmod", "cannot access '%s': %v", path, errText(err))
			}
			status = 1
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return
		}
		if info.IsDir() {
			entries, readErr := os.ReadDir(path)
			if os.IsPermission(readErr) {
				if err := os.Chmod(path, info.Mode()|0o700); err == nil {
					entries, readErr = os.ReadDir(path)
				} else {
					readErr = err
				}
			}
			if readErr != nil {
				if !quiet {
					fatalf("chmod", "cannot access '%s': %v", path, errText(readErr))
				}
				status = 1
			} else {
				for _, entry := range entries {
					visit(filepath.Join(path, entry.Name()))
				}
			}
		}
		newMode := compute(info.Mode(), info.IsDir())
		if err := os.Chmod(path, newMode); err != nil {
			if !quiet {
				fatalf("chmod", "cannot change '%s': %v", path, errText(err))
			}
			status = 1
		} else {
			reportChmod(path, info.Mode(), newMode, verbose, changesOnly)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	return status
}

// reportChmod prints the -v/-c change line GNU chmod does; it is a no-op
// unless one of those flags was given.
func reportChmod(path string, old, updated os.FileMode, verbose, changesOnly bool) {
	if !verbose && !changesOnly {
		return
	}
	oldBits, newBits := octalFromFileMode(old), octalFromFileMode(updated)
	if oldBits == newBits {
		if verbose {
			fmt.Printf("mode of '%s' retained as 0%03o (%s)\n", path, newBits, modeString(updated)[1:])
		}
		return
	}
	fmt.Printf("mode of '%s' changed from 0%03o (%s) to 0%03o (%s)\n",
		path, oldBits, modeString(old)[1:], newBits, modeString(updated)[1:])
}

// currentUmask peeks at the process umask without permanently changing it.
func currentUmask() uint64 {
	old := syscall.Umask(0)
	syscall.Umask(old)
	return uint64(old) //nolint:gosec // umask(2) always returns a small mode value, never negative
}

type chmodClauseOp struct {
	who        string
	omittedWho bool
	op         byte
	permChars  string
	copyFrom   byte
}

// parseSymbolicMode parses a GNU chmod symbolic mode spec such as "u+x",
// "go-r", "a=rw", "u=rwx,g=rx" or "u+x-w" into the sequence of clauses to
// apply in order.
func parseSymbolicMode(spec string) ([]chmodClauseOp, error) {
	var ops []chmodClauseOp
	for _, clause := range strings.Split(spec, ",") {
		who, i := parseChmodWho(clause)
		omittedWho := who == ""
		if omittedWho {
			who = "ugo"
		}
		if i >= len(clause) || !strings.ContainsRune("+-=", rune(clause[i])) {
			return nil, fmt.Errorf("invalid mode: '%s'", spec)
		}
		for i < len(clause) {
			op := clause[i]
			i++
			start := i
			for i < len(clause) && strings.ContainsRune("rwxXst", rune(clause[i])) {
				i++
			}
			permChars := clause[start:i]
			var copyFrom byte
			if permChars == "" && i < len(clause) && strings.ContainsRune("ugo", rune(clause[i])) {
				copyFrom = clause[i]
				i++
			}
			ops = append(ops, chmodClauseOp{who: who, omittedWho: omittedWho, op: op, permChars: permChars, copyFrom: copyFrom})
			if i < len(clause) && strings.ContainsRune("+-=", rune(clause[i])) {
				continue
			}
			break
		}
		if i != len(clause) {
			return nil, fmt.Errorf("invalid mode: '%s'", spec)
		}
	}
	return ops, nil
}

func parseChmodWho(clause string) (string, int) {
	who := ""
	i := 0
	for i < len(clause) {
		switch clause[i] {
		case 'a':
			who = "ugo"
		case 'u', 'g', 'o':
			if !strings.ContainsRune(who, rune(clause[i])) {
				who += string(clause[i])
			}
		default:
			return who, i
		}
		i++
	}
	return who, i
}

// applyChmodClause applies one parsed clause to a 12-bit traditional mode
// value, returning the updated bits. umask only affects rwx bits, and only
// when the clause's who list was omitted (defaulted to "a").
func applyChmodClause(bits uint64, isDir bool, c chmodClauseOp, umask uint64) uint64 {
	var rwx uint64
	hasRWX := false
	setuid, setgid, sticky, touchSpecial := false, false, false, false
	if c.copyFrom != 0 {
		hasRWX = true
		switch c.copyFrom {
		case 'u':
			rwx = (bits >> 6) & 0o7
		case 'g':
			rwx = (bits >> 3) & 0o7
		case 'o':
			rwx = bits & 0o7
		}
	} else {
		for _, p := range c.permChars {
			switch p {
			case 'r':
				rwx |= 0o4
				hasRWX = true
			case 'w':
				rwx |= 0o2
				hasRWX = true
			case 'x':
				rwx |= 0o1
				hasRWX = true
			case 'X':
				hasRWX = true
				if isDir || bits&0o111 != 0 {
					rwx |= 0o1
				}
			case 's':
				touchSpecial, setuid, setgid = true, true, true
			case 't':
				touchSpecial, sticky = true, true
			}
		}
	}
	for _, w := range c.who {
		var shift uint
		switch w {
		case 'u':
			shift = 6
		case 'g':
			shift = 3
		case 'o':
			shift = 0
		}
		classRWX := rwx
		if c.omittedWho && hasRWX {
			classRWX &^= (umask >> shift) & 0o7
		}
		switch c.op {
		case '+':
			if hasRWX {
				bits |= classRWX << shift
			}
			if touchSpecial {
				if setuid && w == 'u' {
					bits |= 0o4000
				}
				if setgid && w == 'g' {
					bits |= 0o2000
				}
				if sticky {
					bits |= 0o1000
				}
			}
		case '-':
			if hasRWX {
				bits &^= classRWX << shift
			}
			if touchSpecial {
				if setuid && w == 'u' {
					bits &^= 0o4000
				}
				if setgid && w == 'g' {
					bits &^= 0o2000
				}
				if sticky {
					bits &^= 0o1000
				}
			}
		case '=':
			bits &^= 0o7 << shift
			if hasRWX {
				bits |= classRWX << shift
			}
			switch w {
			case 'u':
				if setuid {
					bits |= 0o4000
				} else {
					bits &^= 0o4000
				}
			case 'g':
				if setgid {
					bits |= 0o2000
				} else {
					bits &^= 0o2000
				}
			case 'o':
				if sticky {
					bits |= 0o1000
				} else {
					bits &^= 0o1000
				}
			}
		}
	}
	return bits
}

func cmdChown(args []string) int {
	recursive, noDereference, operands, ok := parseOwnerOptions("chown", args)
	if !ok {
		return 1
	}
	if len(operands) < 2 {
		fatalf("chown", "missing owner or file operand")
		return 1
	}
	uid, gid, err := parseOwnerSpec(operands[0])
	if err != nil {
		fatalf("chown", "%v", err)
		return 1
	}
	return changePaths("chown", operands[1:], recursive, func(path string, info os.FileInfo) error {
		if noDereference || info.Mode()&os.ModeSymlink != 0 && recursive {
			return os.Lchown(path, uid, gid)
		}
		return os.Chown(path, uid, gid)
	})
}

func cmdChgrp(args []string) int {
	recursive, noDereference, operands, ok := parseOwnerOptions("chgrp", args)
	if !ok {
		return 1
	}
	if len(operands) < 2 {
		fatalf("chgrp", "missing group or file operand")
		return 1
	}
	gid, err := resolveGroup(operands[0])
	if err != nil {
		fatalf("chgrp", "%v", err)
		return 1
	}
	return changePaths("chgrp", operands[1:], recursive, func(path string, info os.FileInfo) error {
		if noDereference || info.Mode()&os.ModeSymlink != 0 && recursive {
			return os.Lchown(path, -1, gid)
		}
		return os.Chown(path, -1, gid)
	})
}

func parseOwnerOptions(prog string, args []string) (bool, bool, []string, bool) {
	var recursive, noDereference bool
	var operands []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-R" || arg == "--recursive"):
			recursive = true
		case parsing && (arg == "-h" || arg == "--no-dereference"):
			noDereference = true
		case parsing && len(arg) > 1 && arg[0] == '-':
			fatalf(prog, "invalid option %q", arg)
			return false, false, nil, false
		default:
			operands = append(operands, arg)
		}
	}
	return recursive, noDereference, operands, true
}

func changePaths(prog string, paths []string, recursive bool, change func(string, os.FileInfo) error) int {
	status := 0
	for _, root := range paths {
		if !recursive {
			info, err := os.Lstat(root)
			if err == nil {
				err = change(root, info)
			}
			if err != nil {
				fatalf(prog, "cannot change '%s': %v", root, err)
				status = 1
			}
			continue
		}
		type entry struct {
			path string
			info os.FileInfo
		}
		var entries []entry
		err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				fatalf(prog, "cannot access '%s': %v", path, walkErr)
				status = 1
				return nil
			}
			entries = append(entries, entry{path: path, info: info})
			return nil
		})
		if err != nil {
			fatalf(prog, "cannot access '%s': %v", root, err)
			status = 1
		}
		// Apply children before parents so changing a directory's ownership or
		// search permission cannot make an already-discovered child unreachable.
		for i := len(entries) - 1; i >= 0; i-- {
			if err := change(entries[i].path, entries[i].info); err != nil {
				fatalf(prog, "cannot change '%s': %v", entries[i].path, err)
				status = 1
			}
		}
	}
	return status
}

func parseOwnerSpec(spec string) (int, int, error) {
	owner, group, hasGroup := strings.Cut(spec, ":")
	if owner == "" && (!hasGroup || group == "") {
		return -1, -1, fmt.Errorf("invalid owner: %q", spec)
	}
	uid, gid := -1, -1
	var err error
	if owner != "" {
		uid, err = resolveUser(owner)
		if err != nil {
			return -1, -1, err
		}
	}
	if hasGroup {
		if group != "" {
			gid, err = resolveGroup(group)
		} else if owner != "" {
			u, lookupErr := lookupUser(owner)
			if lookupErr != nil {
				return -1, -1, lookupErr
			}
			gid, err = strconv.Atoi(u.Gid)
		}
	}
	return uid, gid, err
}

func resolveUser(value string) (int, error) {
	if id, err := parseNonnegativeID(value); err == nil {
		return id, nil
	}
	u, err := user.Lookup(value)
	if err != nil {
		return -1, fmt.Errorf("invalid user %q", value)
	}
	return strconv.Atoi(u.Uid)
}

func lookupUser(value string) (*user.User, error) {
	if _, err := parseNonnegativeID(value); err == nil {
		u, lookupErr := user.LookupId(value)
		if lookupErr != nil {
			return nil, fmt.Errorf("invalid user %q", value)
		}
		return u, nil
	}
	u, err := user.Lookup(value)
	if err != nil {
		return nil, fmt.Errorf("invalid user %q", value)
	}
	return u, nil
}

func resolveGroup(value string) (int, error) {
	if id, err := parseNonnegativeID(value); err == nil {
		return id, nil
	}
	g, err := user.LookupGroup(value)
	if err != nil {
		return -1, fmt.Errorf("invalid group %q", value)
	}
	return strconv.Atoi(g.Gid)
}

func parseNonnegativeID(value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil || id < 0 {
		return -1, fmt.Errorf("invalid id")
	}
	return id, nil
}
