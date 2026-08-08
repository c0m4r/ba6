// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
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
	all      bool
	nameOnly bool
	write    bool
	keys     []string
}

func cmdSysctl(args []string) int {
	opts, err := parseSysctlOptions(args)
	if err != nil {
		fatalf("sysctl", "%v", err)
		return 1
	}
	if opts.all {
		entries, err := listSysctls(sysctlRoot)
		if err != nil {
			fatalf("sysctl", "%v", err)
			return 1
		}
		for _, entry := range entries {
			if opts.nameOnly {
				fmt.Fprintln(os.Stdout, entry.value)
			} else {
				fmt.Fprintf(os.Stdout, "%s = %s\n", entry.key, entry.value)
			}
		}
		return 0
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
				fatalf("sysctl", "%s: %v", key, err)
				status = 1
				continue
			}
			if opts.nameOnly {
				fmt.Fprintln(os.Stdout, value)
			} else {
				fmt.Fprintf(os.Stdout, "%s = %s\n", canonicalSysctlKey(key), value)
			}
			continue
		}
		value, err := readSysctl(sysctlRoot, key)
		if err != nil {
			fatalf("sysctl", "%s: %v", key, err)
			status = 1
			continue
		}
		if opts.nameOnly {
			fmt.Fprintln(os.Stdout, value)
		} else {
			fmt.Fprintf(os.Stdout, "%s = %s\n", canonicalSysctlKey(key), value)
		}
	}
	return status
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
				opts.nameOnly = true
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

func listSysctls(root string) ([]sysctlEntry, error) {
	entries := []sysctlEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value, err := readSysctl(root, relative)
		if err != nil {
			return err
		}
		entries = append(entries, sysctlEntry{key: canonicalSysctlKey(relative), value: value})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	return entries, nil
}

func readSysctl(root, key string) (string, error) {
	path, err := sysctlPath(root, key)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(data), "\n"), nil
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
