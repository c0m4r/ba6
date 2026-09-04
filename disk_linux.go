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
	// dev is the device the mount table itself reports, which differs from the
	// device found at the mount point when a later mount shadows this one.
	dev     uint64
	haveDev bool
}

// mountDevice encodes a mountinfo "major:minor" field the way st_dev does.
func mountDevice(field string) (uint64, bool) {
	majorText, minorText, found := strings.Cut(field, ":")
	if !found {
		return 0, false
	}
	major, err := strconv.ParseUint(majorText, 10, 32)
	if err != nil {
		return 0, false
	}
	minor, err := strconv.ParseUint(minorText, 10, 32)
	if err != nil {
		return 0, false
	}
	return (major&0xfff)<<8 | minor&0xff | (major&^uint64(0xfff))<<32 | (minor&^uint64(0xff))<<12, true
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

// dfEntry is one filesystem's raw figures, in bytes and inodes, before any
// unit or field selection is applied. The rows are collected first because
// every column is sized from the widest value in it.
type dfEntry struct {
	source, fstype, target, file string
	total, used, avail           uint64
	itotal, iused, iavail        uint64
	isTotal                      bool
	// unavailable marks a mount df could not measure. Under -a it is still
	// listed, with a dash in every figure rather than a row of zeroes.
	unavailable bool
}

// dfField is one --output column. GNU df keeps a minimum width per column, so
// short values still line up under the heading they belong to.
type dfField struct {
	name     string
	minWidth int
	left     bool
}

var dfFields = []dfField{
	{"source", 14, true}, {"fstype", 4, true},
	{"itotal", 5, false}, {"iused", 5, false}, {"iavail", 5, false}, {"ipcent", 4, false},
	{"size", 5, false}, {"used", 5, false}, {"avail", 5, false}, {"pcent", 4, false},
	{"file", 0, true}, {"target", 0, true},
}

// dfHeaderMode picks the wording of the size, avail and pcent headings, which
// differ between the plain table, -h, -P and --output.
type dfHeaderMode int

const (
	dfHeaderDefault dfHeaderMode = iota
	dfHeaderHuman
	dfHeaderPosix
	dfHeaderOutput
)

// dfOptions is one df(1) command line.
type dfOptions struct {
	all        bool
	human      uint64 // 0, or the base -h/-H scale values in
	inodes     bool
	printType  bool
	local      bool
	total      bool
	blockSize  uint64
	blockUnit  string // echoed after each value for a bare-unit -B
	headerMode dfHeaderMode
	fields     []string
	types      []string
	exclude    []string
}

// dfRemoteTypes are the filesystem types -l leaves out. GNU decides remoteness
// from the device name too: a "host:/path" source, or a "//server/share" one.
var dfRemoteTypes = map[string]bool{
	"acfs": true, "afs": true, "auristorfs": true, "ceph": true, "cifs": true,
	"coda": true, "davfs": true, "fhgfs": true, "ftpfs": true, "fuse.sshfs": true,
	"gfs": true, "gfs2": true, "glusterfs": true, "gpfs": true, "ibrix": true,
	"lustre": true, "mfs": true, "ncpfs": true, "nfs": true, "nfs4": true,
	"ocfs2": true, "pvfs2": true, "smb3": true, "smbfs": true, "sshfs": true,
	"vxfs": true,
}

func dfIsRemote(source, fstype string) bool {
	switch {
	case dfRemoteTypes[fstype]:
		return true
	case strings.Contains(source, ":"):
		return true
	case strings.HasPrefix(source, "//") && (fstype == "smbfs" || fstype == "smb3" || fstype == "cifs"):
		return true
	case source == "-hosts":
		return true
	}
	return false
}

func dfUsage(format string, a ...interface{}) {
	fatalf("df", format, a...)
	fmt.Fprintln(os.Stderr, "Try 'df --help' for more information.")
}

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func parseDfOptions(args []string) (dfOptions, []string, bool) {
	options := dfOptions{blockSize: 1024}
	var paths []string
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !parsing || arg == "-" || !strings.HasPrefix(arg, "-") {
			paths = append(paths, arg)
			continue
		}
		if arg == "--" {
			parsing = false
			continue
		}
		// takeValue returns a long option's "=value" or the next operand.
		name, value, hasValue := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
		}
		takeValue := func(rest string) (string, bool) {
			switch {
			case hasValue:
				return value, true
			case rest != "":
				return rest, true
			}
			i++
			if i >= len(args) {
				return "", false
			}
			return args[i], true
		}
		if strings.HasPrefix(arg, "--") {
			switch name {
			case "--all":
				options.all = true
			case "--human-readable":
				options.human = 1024
			case "--si":
				options.human = 1000
			case "--inodes":
				options.inodes = true
			case "--print-type":
				options.printType = true
			case "--local":
				options.local = true
			case "--portability":
				options.headerMode = dfHeaderPosix
			case "--total":
				options.total = true
			case "--sync", "--no-sync":
				// df syncs only on request, and never needs to here.
			case "--block-size":
				text, ok := takeValue("")
				if !ok || !options.setBlockSize("--block-size", text) {
					return options, nil, false
				}
			case "--type":
				text, ok := takeValue("")
				if !ok {
					return options, nil, false
				}
				options.types = append(options.types, text)
			case "--exclude-type":
				text, ok := takeValue("")
				if !ok {
					return options, nil, false
				}
				options.exclude = append(options.exclude, text)
			case "--output":
				options.headerMode = dfHeaderOutput
				if !hasValue {
					options.fields = dfAllFieldNames()
					continue
				}
				for _, field := range strings.Split(value, ",") {
					if !dfKnownField(field) {
						dfUsage("option --output: field '%s' unknown", field)
						return options, nil, false
					}
					options.fields = append(options.fields, field)
				}
			default:
				dfUsage("unrecognized option '%s'", arg)
				return options, nil, false
			}
			continue
		}
		cluster := arg[1:]
		for len(cluster) > 0 {
			letter := cluster[0]
			cluster = cluster[1:]
			switch letter {
			case 'a':
				options.all = true
			case 'h':
				options.human = 1024
			case 'H':
				options.human = 1000
			case 'i':
				options.inodes = true
			case 'T':
				options.printType = true
			case 'l':
				options.local = true
			case 'P':
				options.headerMode = dfHeaderPosix
			case 'k':
				options.blockSize, options.blockUnit, options.human = 1024, "", 0
			case 'v':
				// Accepted and ignored, as in the original.
			case 'B', 't', 'x':
				rest := cluster
				cluster = ""
				text, ok := takeValue(rest)
				if !ok {
					dfUsage("option requires an argument -- '%c'", letter)
					return options, nil, false
				}
				switch letter {
				case 'B':
					if !options.setBlockSize("-B", text) {
						return options, nil, false
					}
				case 't':
					options.types = append(options.types, text)
				case 'x':
					options.exclude = append(options.exclude, text)
				}
			default:
				dfUsage("invalid option -- '%c'", letter)
				return options, nil, false
			}
		}
	}
	// -h and -H scale each value themselves, and rename two of the headings;
	// they win over -P, which only changes the wording of the plain table.
	switch {
	case options.human != 0:
		options.headerMode = dfHeaderHuman
	case options.headerMode == dfHeaderPosix:
		options.blockSize, options.blockUnit = 1024, ""
	}
	if options.fields == nil {
		options.fields = options.defaultFields()
	}
	return options, paths, true
}

// setBlockSize applies -B. A spec written as a bare unit ("-B M") makes the
// printed values carry that unit; one with a leading count ("-B 1M") does not.
func (options *dfOptions) setBlockSize(name, value string) bool {
	size, err := parseByteSize(value)
	if err != nil || size == 0 {
		fatalf("df", "invalid %s argument %s", name, quoteLocaleName(value))
		return false
	}
	options.blockSize, options.blockUnit, options.human = size, "", 0
	if len(value) > 0 && !isDigitByte(value[0]) {
		options.blockUnit = value
		if value == "KB" {
			options.blockUnit = "kB"
		}
	}
	return true
}

func (options *dfOptions) defaultFields() []string {
	fields := []string{"source"}
	if options.printType {
		fields = append(fields, "fstype")
	}
	if options.inodes {
		fields = append(fields, "itotal", "iused", "iavail", "ipcent")
	} else {
		fields = append(fields, "size", "used", "avail", "pcent")
	}
	return append(fields, "target")
}

func dfAllFieldNames() []string {
	names := make([]string, 0, len(dfFields))
	for _, field := range dfFields {
		names = append(names, field.name)
	}
	return names
}

func dfKnownField(name string) bool {
	for _, field := range dfFields {
		if field.name == name {
			return true
		}
	}
	return false
}

// heading is the column title, which depends on the header mode for the three
// columns whose wording GNU changes.
func (options *dfOptions) heading(name string) string {
	switch name {
	case "source":
		return "Filesystem"
	case "fstype":
		return "Type"
	case "itotal":
		return "Inodes"
	case "iused":
		return "IUsed"
	case "iavail":
		return "IFree"
	case "ipcent":
		return "IUse%"
	case "used":
		return "Used"
	case "file":
		return "File"
	case "target":
		return "Mounted on"
	case "size":
		switch options.headerMode {
		case dfHeaderHuman:
			return "Size"
		case dfHeaderPosix:
			return "1024-blocks"
		default:
			return dfBlockHeading(options.blockSize, options.blockUnit)
		}
	case "avail":
		if options.headerMode == dfHeaderHuman || options.headerMode == dfHeaderOutput {
			return "Avail"
		}
		return "Available"
	case "pcent":
		if options.headerMode == dfHeaderPosix {
			return "Capacity"
		}
		return "Use%"
	}
	return name
}

// dfBlockHeading names the block size the way df does: "1K-blocks" for the
// default, "1M-blocks" for -BM, and the raw count for a size with no unit.
func dfBlockHeading(size uint64, unit string) string {
	if unit != "" {
		return "1" + unit + "-blocks"
	}
	for _, step := range []struct {
		value  uint64
		suffix string
	}{{1 << 50, "P"}, {1 << 40, "T"}, {1 << 30, "G"}, {1 << 20, "M"}, {1 << 10, "K"}} {
		if size >= step.value && size%step.value == 0 {
			return strconv.FormatUint(size/step.value, 10) + step.suffix + "-blocks"
		}
	}
	return strconv.FormatUint(size, 10) + "-blocks"
}

// value renders one field of one row.
func (options *dfOptions) value(name string, entry dfEntry) string {
	amount := func(bytes uint64) string {
		if options.human != 0 {
			return humanSizeBase(bytes, options.human)
		}
		return strconv.FormatUint((bytes+options.blockSize-1)/options.blockSize, 10) + options.blockUnit
	}
	count := func(n uint64) string {
		if options.human != 0 {
			return humanSizeBase(n, options.human)
		}
		return strconv.FormatUint(n, 10)
	}
	// A percentage is only meaningful when the filesystem reports a capacity;
	// df prints a bare dash when it does not.
	percent := func(used, avail uint64) string {
		if used+avail == 0 {
			return "-"
		}
		return strconv.FormatUint((used*100+used+avail-1)/(used+avail), 10) + "%"
	}
	if entry.unavailable {
		switch name {
		case "fstype", "itotal", "iused", "iavail", "ipcent", "size", "used", "avail", "pcent":
			return "-"
		}
	}
	switch name {
	case "source":
		return entry.source
	case "fstype":
		if entry.isTotal {
			return "-"
		}
		return entry.fstype
	case "itotal":
		return count(entry.itotal)
	case "iused":
		return count(entry.iused)
	case "iavail":
		return count(entry.iavail)
	case "ipcent":
		return percent(entry.iused, entry.iavail)
	case "size":
		return amount(entry.total)
	case "used":
		return amount(entry.used)
	case "avail":
		return amount(entry.avail)
	case "pcent":
		return percent(entry.used, entry.avail)
	case "file":
		if entry.file == "" {
			return "-"
		}
		return entry.file
	case "target":
		return entry.target
	}
	return ""
}

func cmdDf(args []string) int {
	options, paths, ok := parseDfOptions(args)
	if !ok {
		return 1
	}
	mounts, err := readMountInfo()
	if err != nil {
		fatalf("df", "%v", err)
		return 1
	}
	var entries []dfEntry
	status := 0
	if len(paths) == 0 {
		seenDevice := make(map[uint64]bool)
		for _, mount := range mounts {
			if !options.keepsType(mount) {
				continue
			}
			entry, usageErr := dfMeasure(mount.mountpoint, mount)
			if usageErr != nil {
				// A mount the caller cannot measure is skipped rather than
				// reported: it was not asked for by name. -a still lists it,
				// with a dash in place of every figure.
				if !options.all {
					continue
				}
				entry = dfEntry{source: mount.source, fstype: mount.fstype, target: mount.mountpoint, unavailable: true}
			} else if !options.all && entry.total == 0 && entry.itotal == 0 {
				continue
			}
			// Bind mounts repeat a filesystem that is already listed.
			var info syscall.Stat_t
			if syscall.Stat(mount.mountpoint, &info) == nil {
				if !options.all && seenDevice[info.Dev] {
					continue
				}
				seenDevice[info.Dev] = true
				// A mount that another has since been stacked on top of cannot
				// be measured through its own path: statfs answers for the
				// filesystem on top. df lists it with dashes rather than with
				// its neighbour's figures.
				if mount.haveDev && info.Dev != mount.dev {
					entry = dfEntry{source: mount.source, fstype: mount.fstype, target: mount.mountpoint, unavailable: true}
				}
			}
			entry.source = canonicalDevice(entry.source)
			entries = append(entries, entry)
		}
		if len(entries) == 0 && (len(options.types) > 0 || len(options.exclude) > 0) {
			fatalf("df", "no file systems processed")
			return 1
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
			entry, usageErr := dfMeasure(path, findMount(absolute, mounts))
			if usageErr != nil {
				fatalf("df", "%s: %s", path, errText(usageErr))
				status = 1
				continue
			}
			entry.file = path
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		// Every operand failed; df prints its diagnostics and no table at all.
		return status
	}
	if options.total {
		entries = append(entries, dfTotal(entries))
	}
	if err := options.writeTable(os.Stdout, entries); err != nil {
		fatalf("df", "write error: %v", err)
		return 1
	}
	return status
}

// keepsType applies the mount-table filters: the pseudo-filesystems df hides
// without -a, and the -t, -x and -l restrictions.
func (options *dfOptions) keepsType(mount mountInfo) bool {
	if !options.all && dfDummyTypes[mount.fstype] {
		return false
	}
	if options.local && dfIsRemote(mount.source, mount.fstype) {
		return false
	}
	for _, excluded := range options.exclude {
		if excluded == mount.fstype {
			return false
		}
	}
	if len(options.types) > 0 {
		for _, wanted := range options.types {
			if wanted == mount.fstype {
				return true
			}
		}
		return false
	}
	return true
}

// dfTotal is the grand total row --total adds: the sums of the columns above
// it, labelled the way df labels it.
func dfTotal(entries []dfEntry) dfEntry {
	total := dfEntry{source: "total", target: "-", isTotal: true}
	for _, entry := range entries {
		total.total += entry.total
		total.used += entry.used
		total.avail += entry.avail
		total.itotal += entry.itotal
		total.iused += entry.iused
		total.iavail += entry.iavail
	}
	return total
}

// writeTable prints the selected fields with each column as wide as its widest
// entry, and never narrower than the minimum df keeps for it. The last column
// is not padded.
func (options *dfOptions) writeTable(w io.Writer, entries []dfEntry) error {
	rows := make([][]string, 0, len(entries)+1)
	header := make([]string, 0, len(options.fields))
	for _, name := range options.fields {
		header = append(header, options.heading(name))
	}
	rows = append(rows, header)
	for _, entry := range entries {
		row := make([]string, 0, len(options.fields))
		for _, name := range options.fields {
			row = append(row, options.value(name, entry))
		}
		rows = append(rows, row)
	}
	widths := make([]int, len(options.fields))
	lefts := make([]bool, len(options.fields))
	for i, name := range options.fields {
		for _, field := range dfFields {
			if field.name == name {
				widths[i], lefts[i] = field.minWidth, field.left
			}
		}
	}
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				if _, err := io.WriteString(w, " "); err != nil {
					return err
				}
			}
			pad := widths[i]
			if i == len(row)-1 && lefts[i] {
				pad = 0
			}
			if _, err := fmt.Fprintf(w, "%*s", padWidth(pad, lefts[i]), cell); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	return nil
}

// padWidth turns a column width into the sign fmt uses for its alignment.
func padWidth(width int, left bool) int {
	if left {
		return -width
	}
	return width
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
		device, haveDevice := mountDevice(fields[2])
		mounts = append(mounts, mountInfo{
			source:     unescapeMountField(fields[separator+2]),
			mountpoint: unescapeMountField(fields[4]),
			fstype:     fields[separator+1],
			dev:        device,
			haveDev:    haveDevice,
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

// dfMeasure reads one filesystem's figures. Everything is kept in bytes and
// inodes here; the unit options are applied when the row is rendered.
func dfMeasure(path string, mount mountInfo) (dfEntry, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return dfEntry{}, err
	}
	fragment := uint64(stat.Frsize) //nolint:gosec // Linux statfs fragment sizes are positive.
	if fragment == 0 {
		fragment = uint64(stat.Bsize) //nolint:gosec // Same.
	}
	source := mount.source
	if source == "" {
		source = mount.fstype
	}
	entry := dfEntry{
		source: source,
		fstype: mount.fstype,
		target: mount.mountpoint,
		total:  stat.Blocks * fragment,
		avail:  stat.Bavail * fragment,
		itotal: stat.Files,
		iavail: stat.Ffree,
	}
	entry.used = entry.total - stat.Bfree*fragment
	if stat.Files >= stat.Ffree {
		entry.iused = stat.Files - stat.Ffree
	}
	return entry, nil
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
