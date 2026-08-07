// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"fmt"
	"io"
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

// dfDummyTypes are the pseudo-filesystems df leaves out unless -a is given.
// They store nothing, so listing them only pushes the real filesystems off the
// screen.
var dfDummyTypes = map[string]bool{
	"autofs": true, "binfmt_misc": true, "bpf": true, "cgroup": true, "cgroup2": true,
	"configfs": true, "debugfs": true, "devpts": true, "fuse.portal": true,
	"fusectl": true, "hugetlbfs": true, "mqueue": true, "nsfs": true, "proc": true,
	"pstore": true, "rpc_pipefs": true, "securityfs": true, "sysfs": true, "tracefs": true,
}

// dfRow is one line of df output, already formatted. The rows are collected
// before anything is printed because the column widths come from the widest
// value in each column.
type dfRow struct {
	source, size, used, available, percent, target string
}

func cmdDf(args []string) int {
	human, all := false, false
	var paths []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-h" || arg == "--human-readable"):
			human = true
		case parsing && (arg == "-a" || arg == "--all"):
			all = true
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
	var rows []dfRow
	status := 0
	if len(paths) == 0 {
		seenDevice := make(map[uint64]bool)
		for _, mount := range mounts {
			if !all && dfDummyTypes[mount.fstype] {
				continue
			}
			row, usageErr := filesystemUsage(mount.mountpoint, mount, human)
			// A mount the caller cannot stat is skipped rather than reported:
			// it was not asked for by name.
			if usageErr != nil || (!all && row.size == "0") {
				continue
			}
			// Bind mounts repeat a filesystem that is already listed.
			var info syscall.Stat_t
			if syscall.Stat(mount.mountpoint, &info) == nil {
				if !all && seenDevice[info.Dev] {
					continue
				}
				seenDevice[info.Dev] = true
			}
			row.source = canonicalDevice(row.source)
			rows = append(rows, row)
		}
	} else {
		for _, path := range paths {
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				fatalf("df", "%s: %s", path, errText(resolveErr))
				status = 1
				continue
			}
			absolute, absErr := filepath.Abs(resolved)
			if absErr != nil {
				fatalf("df", "%s: %s", path, errText(absErr))
				status = 1
				continue
			}
			row, usageErr := filesystemUsage(path, findMount(absolute, mounts), human)
			if usageErr != nil {
				fatalf("df", "%s: %s", path, errText(usageErr))
				status = 1
				continue
			}
			rows = append(rows, row)
		}
	}
	if err := writeDfRows(os.Stdout, rows, human); err != nil {
		fatalf("df", "write error: %v", err)
		return 1
	}
	return status
}

// writeDfRows prints the table with each column as wide as its widest entry.
// The originals size the columns from the data, so a long device name shifts
// that row's numbers instead of running into them.
func writeDfRows(w io.Writer, rows []dfRow, human bool) error {
	size, available := "1K-blocks", "Available"
	if human {
		size, available = "Size", "Avail"
	}
	header := dfRow{"Filesystem", size, "Used", available, "Use%", "Mounted on"}
	// df reserves fourteen columns for the device and five for each amount even
	// when the values are shorter.
	widths := []int{14, 5, 5, 5, 4, 0}
	for _, row := range append([]dfRow{header}, rows...) {
		for i, field := range []string{row.source, row.size, row.used, row.available, row.percent} {
			if len(field) > widths[i] {
				widths[i] = len(field)
			}
		}
	}
	for _, row := range append([]dfRow{header}, rows...) {
		if _, err := fmt.Fprintf(w, "%-*s %*s %*s %*s %*s %s\n",
			widths[0], row.source, widths[1], row.size, widths[2], row.used,
			widths[3], row.available, widths[4], row.percent, row.target); err != nil {
			return err
		}
	}
	return nil
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

// canonicalDevice resolves a device name to the node the kernel actually uses,
// so an entry mounted as /dev/mapper/NAME is reported as its /dev/dm-N target.
// df does this only when listing every filesystem; asked about one path by
// name, it prints the device exactly as the mount table spells it.
func canonicalDevice(source string) string {
	if !strings.HasPrefix(source, "/dev/") {
		return source
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return source
	}
	return resolved
}

// filesystemUsage renders one filesystem's figures. Amounts are reported in
// whole kibibytes rounded up, as df does, so a partly used block still counts.
func filesystemUsage(path string, mount mountInfo, human bool) (dfRow, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return dfRow{}, err
	}
	fragment := uint64(stat.Frsize) //nolint:gosec // Linux statfs fragment sizes are positive.
	if fragment == 0 {
		fragment = uint64(stat.Bsize) //nolint:gosec // Same.
	}
	total := stat.Blocks * fragment
	available := stat.Bavail * fragment
	used := total - stat.Bfree*fragment
	percent := uint64(0)
	if used+available > 0 {
		percent = (used*100 + used + available - 1) / (used + available)
	}
	source := mount.source
	if source == "" {
		source = mount.fstype
	}
	amount := func(value uint64) string {
		if human {
			return humanSizeUint64(value)
		}
		return strconv.FormatUint((value+1023)/1024, 10)
	}
	return dfRow{
		source:    source,
		size:      amount(total),
		used:      amount(used),
		available: amount(available),
		percent:   strconv.FormatUint(percent, 10) + "%",
		target:    mount.mountpoint,
	}, nil
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
