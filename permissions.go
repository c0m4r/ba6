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
)

func cmdChmod(args []string) int {
	recursive := false
	var operands []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-R" || arg == "--recursive"):
			recursive = true
		case parsing && len(arg) > 1 && arg[0] == '-':
			fatalf("chmod", "invalid option %q", arg)
			return 1
		default:
			operands = append(operands, arg)
		}
	}
	if len(operands) < 2 {
		fatalf("chmod", "missing mode or file operand")
		return 1
	}
	parsed, err := strconv.ParseUint(strings.TrimPrefix(operands[0], "0o"), 8, 32)
	if err != nil || parsed > 0o7777 {
		fatalf("chmod", "invalid octal mode: %q", operands[0])
		return 1
	}
	mode := fileModeFromOctal(parsed)
	change := func(path string, info os.FileInfo) error {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chmod(path, mode)
	}
	if recursive {
		return chmodRecursive(operands[1:], mode)
	}
	return changePaths("chmod", operands[1:], recursive, change)
}

// chmodRecursive reads each directory before applying a possibly restrictive
// final mode. If a directory is initially unreadable, owner traversal bits are
// enabled temporarily so chmod can also be used to repair locked trees.
func chmodRecursive(roots []string, mode os.FileMode) int {
	status := 0
	var visit func(string)
	visit = func(path string) {
		info, err := os.Lstat(path)
		if err != nil {
			fatalf("chmod", "cannot access '%s': %v", path, err)
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
				fatalf("chmod", "cannot access '%s': %v", path, readErr)
				status = 1
			} else {
				for _, entry := range entries {
					visit(filepath.Join(path, entry.Name()))
				}
			}
		}
		if err := os.Chmod(path, mode); err != nil {
			fatalf("chmod", "cannot change '%s': %v", path, err)
			status = 1
		}
	}
	for _, root := range roots {
		visit(root)
	}
	return status
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
