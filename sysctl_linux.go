// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sysctlRoot is a variable so parser and filesystem handling can be tested
// against a disposable tree. Production always uses the kernel's procfs tree.
var sysctlRoot = "/proc/sys"

type sysctlOptions struct {
	all          bool
	valuesOnly   bool
	namesOnly    bool
	write        bool
	ignoreErrors bool
	keys         []string
}

func cmdSysctl(args []string) int {
	opts, err := parseSysctlOptions(args)
	if err != nil {
		fatalf("sysctl", "%v", err)
		return 1
	}
	if opts.all {
		entries, denied, err := listSysctls(sysctlRoot, opts.namesOnly)
		if err != nil {
			fatalf("sysctl", "%v", err)
			return 1
		}
		status := 0
		for _, key := range denied {
			// A key the kernel exposes but will not let us read is reported and
			// skipped; it must not abandon the rest of the tree.
			if !opts.ignoreErrors {
				fmt.Fprintf(os.Stderr, "sysctl: permission denied on key '%s'\n", key)
				status = 1
			}
		}
		for _, entry := range entries {
			writeSysctlEntry(os.Stdout, opts, entry.key, entry.value)
		}
		return status
	}
	status := 0
	for _, argument := range opts.keys {
		key, value, isWrite := strings.Cut(argument, "=")
		if opts.write && !isWrite {
			fatalf("sysctl", "%s: expected NAME=VALUE", argument)
			status = 1
			continue
		}
		if isWrite {
			if err := writeSysctl(sysctlRoot, key, value); err != nil {
				if !opts.ignoreErrors {
					reportSysctlError(key, err)
					status = 1
				}
				continue
			}
			writeSysctlEntry(os.Stdout, opts, canonicalSysctlKey(key), value)
			continue
		}
		value, present, err := readSysctl(sysctlRoot, key)
		if err != nil {
			if !opts.ignoreErrors {
				reportSysctlError(key, err)
				status = 1
			}
			continue
		}
		if !present {
			continue
		}
		writeSysctlEntry(os.Stdout, opts, canonicalSysctlKey(key), value)
	}
	return status
}

// writeSysctlEntry prints one setting in whichever of the three shapes the
// options select: name and value, the value alone (-n), or the name alone (-N).
// A handful of keys hold several lines (fs.binfmt_misc.*); the original repeats
// the name on each one rather than emitting a value with newlines buried in it,
// which would not survive being read back.
func writeSysctlEntry(out *os.File, opts sysctlOptions, key, value string) {
	if opts.namesOnly {
		fmt.Fprintln(out, key)
		return
	}
	for _, line := range strings.Split(value, "\n") {
		if opts.valuesOnly {
			fmt.Fprintln(out, line)
		} else {
			fmt.Fprintf(out, "%s = %s\n", key, line)
		}
	}
}

// sysctlDeprecated are the keys the original leaves out of -a: the kernel
// warns when they are read, and their values are meaningless.
var sysctlDeprecated = map[string]bool{
	"base_reachable_time": true,
	"retrans_time":        true,
}

// sysctlSkipInAll are keys whose value the original refuses to fetch during a
// sweep, because reading them is not free: vm.stat_refresh makes the kernel
// recompute its statistics as a side effect. Naming one directly still reads
// it, as in the original.
var sysctlSkipInAll = map[string]bool{
	"vm.stat_refresh": true,
}

// reportSysctlError renders a failed lookup the way the original does: a
// missing key is reported against the procfs path it resolved to, while a
// readable-but-forbidden key is reported by its dotted name.
func reportSysctlError(key string, err error) {
	if errors.Is(err, fs.ErrPermission) {
		fmt.Fprintf(os.Stderr, "sysctl: permission denied on key '%s'\n", canonicalSysctlKey(key))
		return
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		fmt.Fprintf(os.Stderr, "sysctl: cannot stat %s: %s\n", pathErr.Path, errText(err))
		return
	}
	fatalf("sysctl", "%s: %v", key, err)
}

func parseSysctlOptions(args []string) (sysctlOptions, error) {
	opts := sysctlOptions{}
	args = expandShortOptions(args, "")
	parsing := true
	for _, arg := range args {
		if parsing && arg == "--" {
			parsing = false
			continue
		}
		if parsing {
			switch arg {
			case "-a", "-A", "--all":
				opts.all = true
				continue
			case "-n", "--values":
				opts.valuesOnly = true
				continue
			case "-N", "--names":
				opts.namesOnly = true
				continue
			case "-e", "--ignore":
				opts.ignoreErrors = true
				continue
			case "-w", "--write":
				opts.write = true
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unsupported option %q", arg)
			}
		}
		opts.keys = append(opts.keys, arg)
	}
	if opts.all && len(opts.keys) != 0 {
		return opts, fmt.Errorf("-a cannot be combined with names")
	}
	if !opts.all && len(opts.keys) == 0 {
		return opts, fmt.Errorf("missing name")
	}
	return opts, nil
}

type sysctlEntry struct {
	key, value string
}

// listSysctls walks the whole tree, returning every setting it could read plus
// the names of those it could not. A single unreadable key must never abandon
// the walk: /proc/sys holds write-only entries (fs.binfmt_misc.register) and
// root-only ones (kernel.cad_pid), so aborting there would hide most of the
// tree from an unprivileged caller.
func listSysctls(root string, namesOnly bool) ([]sysctlEntry, []string, error) {
	entries := []sysctlEntry{}
	denied := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable directory is skipped along with everything under it.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if sysctlDeprecated[entry.Name()] || sysctlSkipInAll[canonicalSysctlKey(relative)] {
			return nil
		}
		// A key with no read bit at all is write-only by design; the original
		// passes over those without complaint.
		info, err := entry.Info()
		if err != nil || info.Mode().Perm()&0o444 == 0 {
			return nil
		}
		// Listing names alone needs no read, so a key that is merely forbidden
		// or empty still appears -- as it does in the original.
		if namesOnly {
			entries = append(entries, sysctlEntry{key: canonicalSysctlKey(relative)})
			return nil
		}
		value, present, err := readSysctl(root, relative)
		if err != nil {
			denied = append(denied, canonicalSysctlKey(relative))
			return nil
		}
		if !present {
			return nil
		}
		entries = append(entries, sysctlEntry{key: canonicalSysctlKey(relative), value: value})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	sort.Strings(denied)
	return entries, denied, nil
}

// readSysctl returns the setting's value with the single trailing newline the
// kernel adds removed. The second result is false when the file held no bytes
// at all, which the original passes over silently; that is a different thing
// from a file holding just a newline, which is a real empty value and is
// printed as one.
func readSysctl(root, key string) (string, bool, error) {
	path, err := sysctlPath(root, key)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	if len(data) == 0 {
		return "", false, nil
	}
	return strings.TrimSuffix(string(data), "\n"), true, nil
}

func writeSysctl(root, key, value string) error {
	path, err := sysctlPath(root, key)
	if err != nil {
		return err
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("value contains a line break")
	}
	return os.WriteFile(path, []byte(value+"\n"), 0)
}

func sysctlPath(root, key string) (string, error) {
	key = strings.ReplaceAll(key, ".", "/")
	if key == "" || filepath.IsAbs(key) {
		return "", fmt.Errorf("invalid name")
	}
	components := strings.Split(key, "/")
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return "", fmt.Errorf("invalid name")
		}
		for _, character := range component {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' || character == '_' || character == '-' {
				continue
			}
			return "", fmt.Errorf("invalid name")
		}
	}
	return filepath.Join(root, filepath.Join(components...)), nil
}

func canonicalSysctlKey(key string) string {
	return strings.ReplaceAll(filepath.ToSlash(key), "/", ".")
}
