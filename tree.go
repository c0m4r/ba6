// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// treeOptions is one tree(1) command line. The listing itself is a depth-first
// walk that carries the drawing prefix of the parent down to each child, which
// is what keeps the vertical bars connected across levels.
type treeOptions struct {
	all                bool     // -a
	dirsOnly           bool     // -d
	fullPath           bool     // -f
	classify           bool     // -F
	noIndent           bool     // -i
	followSymlinks     bool     // -l
	oneFileSystem      bool     // -x
	sizes              bool     // -s
	human              bool     // -h
	permission         bool     // -p
	user               bool     // -u
	group              bool     // -g
	modTime            bool     // -D
	timefmt            string   // --timefmt
	inodes             bool     // --inodes
	device             bool     // --device
	du                 bool     // --du
	prune              bool     // --prune
	filelimit          int      // --filelimit
	matchdirs          bool     // --matchdirs
	dirsFirst          bool     // --dirsfirst
	reverse            bool     // -r
	byTime             bool     // -t
	byCtime            bool     // -c
	byVersion          bool     // -v
	bySize             bool     // --sort=size
	unsorted           bool     // -U
	noReport           bool     // --noreport
	output             string   // -o
	level              int      // -L, 0 for unlimited
	patterns           []string // -P, keep matching files
	ignore             []string // -I, drop matching entries
	rootDev            uint64
	visitedDirs        map[fileKey]bool
	w                  io.Writer
	directories, files int
	status             int
}

func calculateDirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func formatTime(t time.Time, fmtStr string) string {
	if fmtStr == "" {
		return t.Format("Jan _2 15:04")
	}
	r := strings.NewReplacer(
		"%Y", "2006",
		"%y", "06",
		"%m", "01",
		"%d", "02",
		"%e", "_2",
		"%b", "Jan",
		"%B", "January",
		"%H", "15",
		"%M", "04",
		"%S", "05",
		"%T", "15:04:05",
		"%F", "2006-01-02",
		"%%", "%",
	)
	return t.Format(r.Replace(fmtStr))
}

func versionLess(a, b string) bool {
	for len(a) > 0 && len(b) > 0 {
		if unicode.IsDigit(rune(a[0])) && unicode.IsDigit(rune(b[0])) {
			i := 0
			for i < len(a) && unicode.IsDigit(rune(a[i])) {
				i++
			}
			j := 0
			for j < len(b) && unicode.IsDigit(rune(b[j])) {
				j++
			}
			numA := strings.TrimLeft(a[:i], "0")
			numB := strings.TrimLeft(b[:j], "0")
			if len(numA) != len(numB) {
				return len(numA) < len(numB)
			}
			if numA != numB {
				return numA < numB
			}
			if i != j {
				return i < j
			}
			a = a[i:]
			b = b[j:]
		} else {
			if a[0] != b[0] {
				return a[0] < b[0]
			}
			a = a[1:]
			b = b[1:]
		}
	}
	return len(a) < len(b)
}

func applySort(options *treeOptions, word string) bool {
	switch strings.ToLower(word) {
	case "name":
		// default
	case "version", "v":
		options.byVersion = true
	case "size", "s":
		options.bySize = true
	case "mtime", "time", "t":
		options.byTime = true
	case "ctime", "c":
		options.byCtime = true
	default:
		fatalf("tree", "invalid sort %q (expected name, version, size, mtime, or ctime)", word)
		return false
	}
	return true
}

func (t *treeOptions) hasQualifyingEntries(path string, depth int) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	selected := t.selectEntries(entries)
	for _, entry := range selected {
		if !entry.IsDir() {
			return true
		}
		if t.level == 0 || depth < t.level {
			subPath := filepath.Join(path, entry.Name())
			if t.hasQualifyingEntries(subPath, depth+1) {
				return true
			}
		}
	}
	return false
}

func cmdTree(args []string) int {
	options := treeOptions{}
	var paths []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--noreport":
			options.noReport = true
		case arg == "--dirsfirst":
			options.dirsFirst = true
		case arg == "--du":
			options.du = true
			options.sizes = true
		case arg == "--prune":
			options.prune = true
		case arg == "--matchdirs":
			options.matchdirs = true
		case arg == "--inodes":
			options.inodes = true
		case arg == "--device":
			options.device = true
		case arg == "--help":
			_ = writeAppletHelp(os.Stdout, "tree")
			return 0
		case arg == "--version":
			fmt.Fprintln(os.Stdout, "tree from ba6")
			return 0
		case arg == "--timefmt":
			if i+1 >= len(args) {
				fatalf("tree", "--timefmt requires an argument")
				return 1
			}
			i++
			options.timefmt = args[i]
			options.modTime = true
		case strings.HasPrefix(arg, "--timefmt="):
			options.timefmt = strings.TrimPrefix(arg, "--timefmt=")
			options.modTime = true
		case arg == "--filelimit":
			if i+1 >= len(args) {
				fatalf("tree", "--filelimit requires an argument")
				return 1
			}
			i++
			fl, err := strconv.Atoi(args[i])
			if err != nil || fl < 0 {
				fatalf("tree", "invalid filelimit %q", args[i])
				return 1
			}
			options.filelimit = fl
		case strings.HasPrefix(arg, "--filelimit="):
			fl, err := strconv.Atoi(strings.TrimPrefix(arg, "--filelimit="))
			if err != nil || fl < 0 {
				fatalf("tree", "invalid filelimit %q", strings.TrimPrefix(arg, "--filelimit="))
				return 1
			}
			options.filelimit = fl
		case arg == "--sort":
			if i+1 >= len(args) {
				fatalf("tree", "--sort requires an argument")
				return 1
			}
			i++
			if !applySort(&options, args[i]) {
				return 1
			}
		case strings.HasPrefix(arg, "--sort="):
			if !applySort(&options, strings.TrimPrefix(arg, "--sort=")) {
				return 1
			}
		case arg == "-L" || arg == "-P" || arg == "-I" || arg == "-o":
			if i+1 >= len(args) {
				fatalf("tree", "%s requires an argument", arg)
				return 1
			}
			i++
			switch arg {
			case "-L":
				level, err := parseTreeLevel(args[i])
				if err != nil {
					fatalf("tree", "%v", err)
					return 1
				}
				options.level = level
			case "-P":
				options.patterns = append(options.patterns, args[i])
			case "-I":
				options.ignore = append(options.ignore, args[i])
			case "-o":
				options.output = args[i]
			}
		case len(arg) > 1 && arg[0] == '-':
			for j := 1; j < len(arg); j++ {
				flag := arg[j]
				switch flag {
				case 'a':
					options.all = true
				case 'd':
					options.dirsOnly = true
				case 'f':
					options.fullPath = true
				case 'F':
					options.classify = true
				case 'i':
					options.noIndent = true
				case 'l':
					options.followSymlinks = true
				case 'x':
					options.oneFileSystem = true
				case 's':
					options.sizes = true
				case 'h':
					options.human, options.sizes = true, true
				case 'p':
					options.permission = true
				case 'u':
					options.user = true
				case 'g':
					options.group = true
				case 'D':
					options.modTime = true
				case 'r':
					options.reverse = true
				case 't':
					options.byTime = true
				case 'c':
					options.byCtime = true
				case 'v':
					options.byVersion = true
				case 'U':
					options.unsorted = true
				case 'n', 'C':
					// Color control.
				case 'o', 'L', 'P', 'I':
					val := arg[j+1:]
					if val == "" {
						if i+1 >= len(args) {
							fatalf("tree", "option -%c requires an argument", flag)
							return 1
						}
						i++
						val = args[i]
					}
					switch flag {
					case 'o':
						options.output = val
					case 'L':
						level, err := parseTreeLevel(val)
						if err != nil {
							fatalf("tree", "%v", err)
							return 1
						}
						options.level = level
					case 'P':
						options.patterns = append(options.patterns, val)
					case 'I':
						options.ignore = append(options.ignore, val)
					}
					j = len(arg)
				default:
					fatalf("tree", "invalid option -- '%c'", flag)
					return 1
				}
			}
		default:
			paths = append(paths, arg)
		}
	}

	options.w = os.Stdout
	if options.output != "" {
		cleaned := filepath.Clean(options.output)
		outFile, err := os.OpenFile(cleaned, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			fatalf("tree", "error opening output file: %v", err)
			return 1
		}
		defer outFile.Close()
		options.w = outFile
	}

	if len(paths) == 0 {
		paths = []string{"."}
	}
	for index, path := range paths {
		if index > 0 {
			fmt.Fprintln(options.w)
		}
		options.walkRoot(path)
	}
	if !options.noReport {
		fmt.Fprintln(options.w)
		if options.dirsOnly {
			fmt.Fprintln(options.w, countedNoun(options.directories, "directory", "directories"))
		} else {
			fmt.Fprintf(options.w, "%s, %s\n", countedNoun(options.directories, "directory", "directories"),
				countedNoun(options.files, "file", "files"))
		}
	}
	return options.status
}

func parseTreeLevel(value string) (int, error) {
	level, err := strconv.Atoi(value)
	if err != nil || level < 1 {
		return 0, fmt.Errorf("invalid level %q", value)
	}
	return level, nil
}

func countedNoun(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func (t *treeOptions) walkRoot(path string) {
	info, err := os.Lstat(path)
	if err != nil {
		fmt.Fprintf(t.w, "%s [error opening dir]\n", path)
		t.status = 1
		return
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		t.rootDev = stat.Dev
		if t.visitedDirs == nil {
			t.visitedDirs = make(map[fileKey]bool)
		}
		t.visitedDirs[fileKey{dev: stat.Dev, ino: stat.Ino}] = true
	}
	if !info.IsDir() {
		fmt.Fprintln(t.w, path)
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		fmt.Fprintf(t.w, "%s [error opening dir]\n", path)
		t.status = 1
		return
	}
	fmt.Fprintln(t.w, path)
	t.walk(path, path, "", 1, t.selectEntries(entries))
}

// walk lists the contents of one directory, which the caller has already read
// so that an unreadable directory can be marked on its own line the way tree
// marks it. It carries two paths: the one to read from and the one to display,
// which stays spelled the way the operand was written so that "tree -f ."
// prints "./dir/file".
func (t *treeOptions) walk(directory, display, prefix string, depth int, entries []os.DirEntry) {
	for index, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.status = 1
			continue
		}
		path := filepath.Join(directory, entry.Name())
		isDir := info.IsDir()
		isSymlink := info.Mode()&os.ModeSymlink != 0
		var loopDetected bool
		var symlinkKey fileKey
		var symlinkTracked bool

		if isSymlink && t.followSymlinks {
			if resolved, statErr := os.Stat(path); statErr == nil && resolved.IsDir() {
				if rStat, ok := resolved.Sys().(*syscall.Stat_t); ok {
					symlinkKey = fileKey{dev: rStat.Dev, ino: rStat.Ino}
					if t.visitedDirs[symlinkKey] {
						loopDetected = true
					} else {
						isDir = true
						t.visitedDirs[symlinkKey] = true
						symlinkTracked = true
					}
				}
			}
		}

		if isDir && t.prune {
			if !t.hasQualifyingEntries(path, depth) {
				if symlinkTracked {
					delete(t.visitedDirs, symlinkKey)
				}
				continue
			}
		}

		branch, continuation := treeBranch, treeVertical
		if index == len(entries)-1 {
			branch, continuation = treeBranchLast, treeBlank
		}
		if t.noIndent {
			branch, continuation = "", ""
		}
		shown := strings.TrimSuffix(display, "/") + "/" + entry.Name()
		line := prefix + branch + t.decorate(entry.Name(), shown, path, info)
		if loopDetected {
			line += "  [recursive, loop detected]"
		}

		if !isDir {
			t.files++
			fmt.Fprintln(t.w, line)
			if symlinkTracked {
				delete(t.visitedDirs, symlinkKey)
			}
			continue
		}
		t.directories++
		var children []os.DirEntry
		if !loopDetected && (t.level == 0 || depth < t.level) {
			if t.oneFileSystem {
				if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Dev != t.rootDev {
					fmt.Fprintln(t.w, line)
					if symlinkTracked {
						delete(t.visitedDirs, symlinkKey)
					}
					continue
				}
			}
			read, err := os.ReadDir(path)
			if err != nil {
				line += " [error opening dir]"
				t.status = 1
			} else if t.filelimit > 0 && len(read) > t.filelimit {
				line += fmt.Sprintf("  [%d entries exceeds filelimit, not opening dir]", len(read))
			} else {
				children = t.selectEntries(read)
			}
		}
		fmt.Fprintln(t.w, line)
		if len(children) > 0 {
			t.walk(path, shown, prefix+continuation, depth+1, children)
		}
		if symlinkTracked {
			delete(t.visitedDirs, symlinkKey)
		}
	}
}

// The drawing pieces tree uses. The last entry of a directory closes its
// vertical bar, so its children are indented with blanks instead.
const (
	treeBranch     = "├── "
	treeBranchLast = "└── "
	treeVertical   = "│   "
	treeBlank      = "    "
)

// selectEntries applies -a, -d, -I, and -P and then orders what is left.
func (t *treeOptions) selectEntries(entries []os.DirEntry) []os.DirEntry {
	kept := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if !t.all && strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if t.dirsOnly && !entry.IsDir() {
			continue
		}
		if matchesAnyPattern(t.ignore, entry.Name()) {
			continue
		}
		if len(t.patterns) > 0 {
			if !entry.IsDir() {
				if !matchesAnyPattern(t.patterns, entry.Name()) {
					continue
				}
			} else if t.matchdirs {
				if !matchesAnyPattern(t.patterns, entry.Name()) {
					continue
				}
			}
		}
		kept = append(kept, entry)
	}
	if t.unsorted {
		return kept
	}

	var modTimes map[string]time.Time
	var cTimes map[string]time.Time
	var sizes map[string]int64

	if t.byTime {
		modTimes = make(map[string]time.Time, len(kept))
		for _, e := range kept {
			if info, err := e.Info(); err == nil {
				modTimes[e.Name()] = info.ModTime()
			}
		}
	} else if t.byCtime {
		cTimes = make(map[string]time.Time, len(kept))
		for _, e := range kept {
			if info, err := e.Info(); err == nil {
				if stat, ok := info.Sys().(*syscall.Stat_t); ok {
					cTimes[e.Name()] = time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec)
				}
			}
		}
	} else if t.bySize {
		sizes = make(map[string]int64, len(kept))
		for _, e := range kept {
			if info, err := e.Info(); err == nil {
				sizes[e.Name()] = info.Size()
			}
		}
	}

	sort.SliceStable(kept, func(i, j int) bool {
		left, right := kept[i], kept[j]
		if t.dirsFirst && left.IsDir() != right.IsDir() {
			return left.IsDir()
		}
		var less bool
		switch {
		case t.byTime:
			less = modTimes[left.Name()].Before(modTimes[right.Name()])
		case t.byCtime:
			less = cTimes[left.Name()].Before(cTimes[right.Name()])
		case t.bySize:
			less = sizes[left.Name()] < sizes[right.Name()]
		case t.byVersion:
			less = versionLess(left.Name(), right.Name())
		default:
			less = left.Name() < right.Name()
		}
		if t.reverse {
			return !less
		}
		return less
	})
	return kept
}

func matchesAnyPattern(patterns []string, name string) bool {
	for _, pattern := range patterns {
		for _, alternative := range strings.Split(pattern, "|") {
			if matched, err := filepath.Match(alternative, name); err == nil && matched {
				return true
			}
		}
	}
	return false
}

// decorate builds one displayed name: the optional metadata columns, the name
// itself or its full path, a symlink target, and the -F type marker.
func (t *treeOptions) decorate(name, display, path string, info os.FileInfo) string {
	var line strings.Builder
	var meta []string

	stat, _ := info.Sys().(*syscall.Stat_t)

	if t.inodes && stat != nil {
		meta = append(meta, fmt.Sprintf("%7d", stat.Ino))
	}
	if t.device && stat != nil {
		meta = append(meta, fmt.Sprintf("%4d", stat.Dev))
	}
	if t.permission {
		meta = append(meta, modeString(info.Mode()))
	}
	if t.user && stat != nil {
		meta = append(meta, fmt.Sprintf("%-8s", userName(stat.Uid)))
	}
	if t.group && stat != nil {
		meta = append(meta, fmt.Sprintf("%-8s", groupName(stat.Gid)))
	}
	if t.sizes {
		sz := info.Size()
		if t.du && info.IsDir() {
			sz = calculateDirSize(path)
		}
		if t.human {
			meta = append(meta, fmt.Sprintf("%4s", humanSize(sz)))
		} else {
			meta = append(meta, fmt.Sprintf("%11d", sz))
		}
	}
	if t.modTime {
		meta = append(meta, formatTime(info.ModTime(), t.timefmt))
	}

	if len(meta) > 0 {
		fmt.Fprintf(&line, "[%s]  ", strings.Join(meta, " "))
	}

	if t.fullPath {
		name = display
	}
	line.WriteString(name)

	// A symlink is shown with its target, and -F then classifies what the
	// link points at rather than the link itself, as tree does.
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return line.String()
		}
		line.WriteString(" -> " + target)
		if resolved, err := os.Stat(path); err == nil {
			line.WriteString(treeClassifier(t.classify, resolved))
		}
		return line.String()
	}
	line.WriteString(treeClassifier(t.classify, info))
	return line.String()
}

func treeClassifier(classify bool, info os.FileInfo) string {
	if !classify {
		return ""
	}
	mode := info.Mode()
	switch {
	case mode.IsDir():
		return "/"
	case mode&os.ModeSocket != 0:
		return "="
	case mode&os.ModeNamedPipe != 0:
		return "|"
	case mode&0111 != 0:
		return "*"
	}
	return ""
}
