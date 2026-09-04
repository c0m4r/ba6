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
	human     bool
	summary   bool
	all       bool
	total     bool
	separate  bool
	inodes    bool
	apparent  bool
	oneFS     bool
	deref     bool
	derefArgs bool
	null      bool
	blockSize uint64
	blockUnit string
	maxDepth  int
	threshold int64
	excludes  []string
	seen      map[duIdentity]bool
	rootDev   uint64
	grand     uint64
	status    int
}

// duMaxDepth stands in for "no -d limit"; no directory tree comes near it.
const duMaxDepth = 1 << 30

func cmdDu(args []string) int {
	opts := duOptions{
		seen:      make(map[duIdentity]bool),
		blockSize: 1024,
		maxDepth:  duMaxDepth,
	}
	var paths []string
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func(name string) (string, bool) {
			i++
			if i >= len(args) {
				fatalf("du", "option requires an argument -- '%s'", name)
				return "", false
			}
			return args[i], true
		}
		var ok bool
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && strings.HasPrefix(arg, "--"):
			name, argument, hasArgument := arg, "", false
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, argument, hasArgument = arg[:eq], arg[eq+1:], true
			}
			switch name {
			case "--all":
				opts.all = true
			case "--summarize":
				opts.summary = true
			case "--human-readable":
				opts.human = true
			case "--total":
				opts.total = true
			case "--separate-dirs":
				opts.separate = true
			case "--inodes":
				opts.inodes = true
			case "--apparent-size":
				opts.apparent = true
			case "--one-file-system":
				opts.oneFS = true
			case "--dereference":
				opts.deref = true
			case "--dereference-args":
				opts.derefArgs = true
			case "--no-dereference":
				opts.deref = false
			case "--null":
				opts.null = true
			case "--bytes":
				opts.apparent, opts.blockSize, opts.human = true, 1, false
			case "--max-depth", "--block-size", "--threshold", "--exclude":
				if !hasArgument {
					if argument, ok = next(strings.TrimPrefix(name, "--")); !ok {
						return 1
					}
				}
				if !opts.setValueOption(name, argument) {
					return 1
				}
			default:
				duUsageError("unrecognized option '%s'", arg)
				return 1
			}
		case parsing && len(arg) > 1 && arg[0] == '-':
			if !opts.parseShort(arg, args, &i) {
				return 1
			}
		default:
			paths = append(paths, arg)
		}
	}
	if opts.summary && opts.all {
		duUsageError("cannot both summarize and show all entries")
		return 1
	}
	if opts.summary {
		opts.maxDepth = 0
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	for _, path := range paths {
		pathTotal, _ := opts.walk(path, 0, true)
		opts.grand += pathTotal
	}
	if opts.total {
		opts.print(opts.grand, "total")
	}
	return opts.status
}

// parseShort consumes one bundled short-option word; -d, -B and -t take the
// rest of the word or the next argument.
func (d *duOptions) parseShort(arg string, args []string, i *int) bool {
	for j := 1; j < len(arg); j++ {
		flag := arg[j]
		switch flag {
		case 'h':
			d.human = true
		case 's':
			d.summary = true
		case 'a':
			d.all = true
		case 'c':
			d.total = true
		case 'S':
			d.separate = true
		case 'x':
			d.oneFS = true
		case 'L':
			d.deref = true
		case 'D', 'H':
			d.derefArgs = true
		case 'P':
			d.deref = false
		case '0':
			d.null = true
		case 'k':
			d.blockSize, d.human = 1024, false
		case 'm':
			d.blockSize, d.human = 1024*1024, false
		case 'b':
			d.apparent, d.blockSize, d.human = true, 1, false
		case 'd', 'B', 't':
			value := arg[j+1:]
			if value == "" {
				*i++
				if *i >= len(args) {
					fatalf("du", "option requires an argument -- '%c'", flag)
					return false
				}
				value = args[*i]
			}
			name := map[byte]string{'d': "--max-depth", 'B': "-B", 't': "-t"}[flag]
			return d.setValueOption(name, value)
		default:
			duUsageError("invalid option -- '%c'", flag)
			return false
		}
	}
	return true
}

// setValueOption applies the options that carry a value.
func (d *duOptions) setValueOption(name, value string) bool {
	switch name {
	case "--max-depth":
		depth, err := strconv.Atoi(value)
		if err != nil || depth < 0 {
			duUsageError("invalid maximum depth %s", quoteLocaleName(value))
			return false
		}
		d.maxDepth = depth
	case "--block-size", "-B":
		size, err := parseByteSize(value)
		if err != nil || size == 0 {
			// This one is reported without the "Try --help" line, and names
			// the option the way it was written on the command line.
			fatalf("du", "invalid %s argument %s", name, quoteLocaleName(value))
			return false
		}
		d.blockSize, d.human = size, false
		// A spec written as a bare unit ("-B K") makes the printed values
		// carry that unit; one with a leading count ("-B 1K") does not.
		d.blockUnit = ""
		if len(value) > 0 && !isDigitByte(value[0]) {
			d.blockUnit = value
			if value == "KB" {
				// SI kilo is spelled with a small k.
				d.blockUnit = "kB"
			}
		}
	case "--threshold", "-t":
		text, sign := value, int64(1)
		if strings.HasPrefix(text, "-") {
			text, sign = text[1:], -1
		}
		size, err := parseByteSize(text)
		if err != nil {
			fatalf("du", "invalid %s argument %s", name, quoteLocaleName(value))
			return false
		}
		d.threshold = sign * int64(size) //nolint:gosec // G115: a size this large cannot come from a real filesystem.
	case "--exclude":
		d.excludes = append(d.excludes, value)
	}
	return true
}

// duUsageError reports a bad command line the way the original does, with the
// "Try ... --help" line after it.
func duUsageError(format string, args ...any) {
	fatalf("du", format, args...)
	fmt.Fprintln(os.Stderr, "Try 'du --help' for more information.")
}

// parseByteSize parses the size arguments of -B and -t: a count with an
// optional suffix, where a bare letter is a power of 1024 and a "B" suffix
// (KB, MB) is a power of 1000.
func parseByteSize(text string) (uint64, error) {
	digits := 0
	for digits < len(text) && isDigitByte(text[digits]) {
		digits++
	}
	// A bare suffix means one of that unit, as in the original's "-t K".
	amount := uint64(1)
	if digits > 0 {
		parsed, err := strconv.ParseUint(text[:digits], 10, 64)
		if err != nil {
			return 0, err
		}
		amount = parsed
	} else if text == "" {
		return 0, fmt.Errorf("empty size")
	}
	suffix := text[digits:]
	unit := uint64(1024)
	switch {
	case suffix == "":
		return amount, nil
	case strings.HasSuffix(suffix, "B") && len(suffix) == 2:
		unit = 1000
		suffix = suffix[:1]
	case strings.HasSuffix(suffix, "iB"):
		suffix = suffix[:len(suffix)-2]
	}
	powers := map[string]uint{"K": 1, "k": 1, "M": 2, "G": 3, "T": 4, "P": 5, "E": 6}
	power, ok := powers[suffix]
	if !ok {
		return 0, fmt.Errorf("unknown suffix %q", suffix)
	}
	for i := uint(0); i < power; i++ {
		amount *= unit
	}
	return amount, nil
}

// excluded reports whether --exclude covers this path. The original matches the
// pattern against the path as walked, that path without its "./" prefix, and
// the bare name.
func (d *duOptions) excluded(path string) bool {
	base := filepath.Base(path)
	trimmed := strings.TrimPrefix(path, "./")
	for _, pattern := range d.excludes {
		for _, candidate := range []string{path, trimmed, base} {
			if matched, err := filepath.Match(pattern, candidate); err == nil && matched {
				return true
			}
		}
	}
	return false
}

// countOf returns what this entry contributes: one inode for --inodes, the
// apparent size for --apparent-size/-b, and the allocated blocks otherwise.
func (d *duOptions) countOf(info os.FileInfo) uint64 {
	switch {
	case d.inodes:
		return 1
	case d.apparent:
		if info.IsDir() {
			// The original counts no apparent size for a directory itself.
			return 0
		}
		return uint64(info.Size()) //nolint:gosec // G115: file sizes are nonnegative.
	}
	return allocatedBytes(info)
}

// walk sums one path, printing the entries that -a/-d/-S/-t select. It returns
// the amount to add to the parent's total, which is not always what was
// printed: -S prints a directory without its subdirectories but still passes
// the full total up.
// It returns the total to add to the parent, and how much of that came from
// this entry itself rather than from a subdirectory, which is what -S prints.
func (d *duOptions) walk(path string, depth int, isRoot bool) (total, outsideDirs uint64) {
	if d.excluded(path) {
		return 0, 0
	}
	info, err := d.statOf(path, isRoot)
	if err != nil {
		fatalf("du", "cannot access %s: %s", quoteForceName(path), errText(err))
		d.status = 1
		return 0, 0
	}
	st, haveStat := info.Sys().(*syscall.Stat_t)
	if haveStat {
		if isRoot && d.oneFS {
			d.rootDev = st.Dev
		}
		if d.oneFS && !isRoot && st.Dev != d.rootDev {
			return 0, 0
		}
		// A hard link or a repeated operand is counted once, and the repeat is
		// not listed at all.
		if st.Nlink > 1 || info.IsDir() {
			identity := duIdentity{dev: st.Dev, ino: st.Ino}
			if d.seen[identity] {
				return 0, 0
			}
			d.seen[identity] = true
		}
	}
	own := d.countOf(info)
	if !info.IsDir() {
		if d.all && depth <= d.maxDepth || isRoot {
			d.report(own, path)
		}
		return own, own
	}

	entries, err := readDirRaw(path)
	if err != nil {
		fatalf("du", "cannot read directory %s: %s", quoteForceName(path), errText(err))
		d.status = 1
		return own, own
	}
	below, direct := uint64(0), own
	for _, name := range entries {
		childTotal, childOutside := d.walk(duJoin(path, name), depth+1, false)
		below += childTotal
		direct += childOutside
	}
	if depth <= d.maxDepth {
		shown := own + below
		if d.separate {
			shown = direct
		}
		d.report(shown, path)
	}
	return own + below, 0
}

// duJoin appends a child name to a directory path. filepath.Join would clean
// away the "./" of the default operand, which the original keeps.
func duJoin(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

// statOf follows symlinks for -L, and for -D/-H on a command-line operand.
func (d *duOptions) statOf(path string, isRoot bool) (os.FileInfo, error) {
	if d.deref || (isRoot && d.derefArgs) {
		return os.Stat(path)
	}
	return os.Lstat(path)
}

// readDirRaw lists a directory in the order the kernel returns it, which is
// the order the original walks and prints in.
func readDirRaw(path string) ([]string, error) {
	dir, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	names, err := dir.Readdirnames(-1)
	if closeErr := dir.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return names, err
}

// report prints one entry unless -t excludes it.
func (d *duOptions) report(amount uint64, path string) {
	if d.threshold > 0 && amount < uint64(d.threshold) { //nolint:gosec // G115: guarded by the sign test.
		return
	}
	if d.threshold < 0 && amount > uint64(-d.threshold) { //nolint:gosec // G115: guarded by the sign test.
		return
	}
	d.print(amount, path)
}

func (d *duOptions) print(amount uint64, path string) {
	value := strconv.FormatUint((amount+d.blockSize-1)/d.blockSize, 10) + d.blockUnit
	switch {
	case d.inodes:
		value = strconv.FormatUint(amount, 10)
		if d.human {
			value = humanSizeUint64(amount)
		}
	case d.human:
		value = humanSizeUint64(amount)
	}
	terminator := "\n"
	if d.null {
		terminator = "\x00"
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s\t%s%s", value, path, terminator); err != nil {
		d.status = 1
	}
}

func allocatedBytes(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Blocks) * 512 //nolint:gosec // st_blocks is nonnegative for filesystem objects.
	}
	return uint64(info.Size()) //nolint:gosec // File sizes for ordinary filesystem objects are nonnegative.
}
