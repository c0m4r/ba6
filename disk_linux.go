// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type mountInfo struct {
	source     string
	mountpoint string
	fstype     string
}

func cmdDf(args []string) int {
	human := false
	var paths []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-h" || arg == "--human-readable"):
			human = true
		case parsing && (arg == "-k" || arg == "-P"):
		case parsing && len(arg) > 1 && arg[0] == '-':
			fatalf("df", "invalid option %q", arg)
			return 1
		default:
			paths = append(paths, arg)
		}
	}
	mounts, err := readMountInfo()
	if err != nil {
		fatalf("df", "%v", err)
		return 1
	}
	if human {
		fmt.Fprintln(os.Stdout, "Filesystem      Size  Used Avail Use% Mounted on")
	} else {
		fmt.Fprintln(os.Stdout, "Filesystem     1K-blocks    Used Available Use% Mounted on")
	}
	status := 0
	if len(paths) == 0 {
		seen := make(map[string]bool)
		for _, mount := range mounts {
			if seen[mount.mountpoint] {
				continue
			}
			seen[mount.mountpoint] = true
			if err := printFilesystemUsage(mount.mountpoint, mount, human); err != nil {
				fatalf("df", "%s: %v", mount.mountpoint, err)
				status = 1
			}
		}
		return status
	}
	for _, path := range paths {
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			fatalf("df", "%s: %v", path, resolveErr)
			status = 1
			continue
		}
		absolute, absErr := filepath.Abs(resolved)
		if absErr != nil {
			fatalf("df", "%s: %v", path, absErr)
			status = 1
			continue
		}
		mount := findMount(absolute, mounts)
		if err := printFilesystemUsage(path, mount, human); err != nil {
			fatalf("df", "%s: %v", path, err)
			status = 1
		}
	}
	return status
}

func readMountInfo() ([]mountInfo, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var mounts []mountInfo
	scanner := newLineScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if len(fields) < 6 || separator < 0 || separator+2 >= len(fields) {
			continue
		}
		mounts = append(mounts, mountInfo{
			source:     unescapeMountField(fields[separator+2]),
			mountpoint: unescapeMountField(fields[4]),
			fstype:     fields[separator+1],
		})
	}
	return mounts, scanner.Err()
}

func unescapeMountField(value string) string {
	replacer := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	return replacer.Replace(value)
}

func findMount(path string, mounts []mountInfo) mountInfo {
	best := mountInfo{}
	for _, mount := range mounts {
		point := filepath.Clean(mount.mountpoint)
		if path == point || strings.HasPrefix(path, point+string(os.PathSeparator)) || point == "/" {
			if len(point) > len(best.mountpoint) {
				best = mount
			}
		}
	}
	if best.mountpoint == "" {
		best = mountInfo{source: "-", mountpoint: "/"}
	}
	return best
}

func printFilesystemUsage(path string, mount mountInfo, human bool) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return err
	}
	blockSize := uint64(stat.Bsize) //nolint:gosec // Linux statfs block sizes are positive.
	total := stat.Blocks * blockSize
	free := stat.Bfree * blockSize
	available := stat.Bavail * blockSize
	used := total - free
	percent := uint64(0)
	if used+available > 0 {
		percent = (used*100 + used + available - 1) / (used + available)
	}
	source := mount.source
	if source == "" {
		source = mount.fstype
	}
	if human {
		_, err := fmt.Fprintf(os.Stdout, "%-14s %5s %5s %5s %3d%% %s\n", source,
			humanSizeUint64(total), humanSizeUint64(used), humanSizeUint64(available), percent, mount.mountpoint)
		return err
	}
	_, err := fmt.Fprintf(os.Stdout, "%-14s %10d %7d %9d %3d%% %s\n", source,
		(total+1023)/1024, (used+1023)/1024, (available+1023)/1024, percent, mount.mountpoint)
	return err
}

type duIdentity struct {
	dev uint64
	ino uint64
}

type duOptions struct {
	human   bool
	summary bool
	all     bool
	seen    map[duIdentity]bool
	status  int
}

func cmdDu(args []string) int {
	opts := duOptions{seen: make(map[duIdentity]bool)}
	var paths []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && len(arg) > 1 && arg[0] == '-':
			for _, flag := range arg[1:] {
				switch flag {
				case 'h':
					opts.human = true
				case 's':
					opts.summary = true
				case 'a':
					opts.all = true
				case 'k':
				default:
					fatalf("du", "invalid option -- '%c'", flag)
					return 1
				}
			}
		default:
			paths = append(paths, arg)
		}
	}
	if opts.summary && opts.all {
		fatalf("du", "options -a and -s are mutually exclusive")
		return 1
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	for _, path := range paths {
		bytes := opts.walk(path, true)
		if opts.summary {
			opts.print(bytes, path)
		}
	}
	return opts.status
}

func (d *duOptions) walk(path string, root bool) uint64 {
	info, err := os.Lstat(path)
	if err != nil {
		fatalf("du", "%s: %v", path, err)
		d.status = 1
		return 0
	}
	bytes := allocatedBytes(info)
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 && !info.IsDir() {
		identity := duIdentity{dev: st.Dev, ino: st.Ino}
		if d.seen[identity] {
			return 0
		}
		d.seen[identity] = true
	}
	if !info.IsDir() {
		if d.all || root && !d.summary {
			d.print(bytes, path)
		}
		return bytes
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		fatalf("du", "%s: %v", path, err)
		d.status = 1
		return bytes
	}
	for _, entry := range entries {
		bytes += d.walk(filepath.Join(path, entry.Name()), false)
	}
	if !d.summary {
		d.print(bytes, path)
	}
	return bytes
}

func allocatedBytes(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Blocks) * 512 //nolint:gosec // st_blocks is nonnegative for filesystem objects.
	}
	return uint64(info.Size()) //nolint:gosec // File sizes for ordinary filesystem objects are nonnegative.
}

func (d *duOptions) print(bytes uint64, path string) {
	value := strconv.FormatUint((bytes+1023)/1024, 10)
	if d.human {
		value = humanSizeUint64(bytes)
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s\t%s\n", value, path); err != nil {
		d.status = 1
	}
}
