// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// treeOptions is one tree(1) command line. The listing itself is a depth-first
// walk that carries the drawing prefix of the parent down to each child, which
// is what keeps the vertical bars connected across levels.
type treeOptions struct {
	all                bool // -a
	dirsOnly           bool // -d
	fullPath           bool // -f
	classify           bool // -F
	noIndent           bool // -i
	sizes              bool // -s
	human              bool // -h
	permission         bool // -p
	dirsFirst          bool
	reverse            bool // -r
	byTime             bool // -t
	unsorted           bool // -U
	noReport           bool
	level              int      // -L, 0 for unlimited
	patterns           []string // -P, keep matching files
	ignore             []string // -I, drop matching entries
	directories, files int
	status             int
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
		case arg == "-L" || arg == "-P" || arg == "-I":
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
			default:
				options.ignore = append(options.ignore, args[i])
			}
		case len(arg) > 1 && arg[0] == '-':
			for _, flag := range arg[1:] {
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
				case 's':
					options.sizes = true
				case 'h':
					options.human, options.sizes = true, true
				case 'p':
					options.permission = true
				case 'r':
					options.reverse = true
				case 't':
					options.byTime = true
				case 'U':
					options.unsorted = true
				case 'n', 'C':
					// Color control. This listing is never colored.
				default:
					fatalf("tree", "invalid option -- '%c'", flag)
					return 1
				}
			}
		default:
			paths = append(paths, arg)
		}
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	for index, path := range paths {
		// Successive trees are separated by a blank line.
		if index > 0 {
			fmt.Println()
		}
		options.walkRoot(path)
	}
	if !options.noReport {
		fmt.Println()
		if options.dirsOnly {
			fmt.Println(countedNoun(options.directories, "directory", "directories"))
		} else {
			fmt.Printf("%s, %s\n", countedNoun(options.directories, "directory", "directories"),
				countedNoun(options.files, "file", "files"))
		}
	}
	return options.status
}

func parseTreeLevel(value string) (int, error) {
	level := 0
	if _, err := fmt.Sscanf(value, "%d", &level); err != nil || level < 1 {
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
		fmt.Printf("%s [error opening dir]\n", path)
		t.status = 1
		return
	}
	if !info.IsDir() {
		fmt.Println(path)
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		fmt.Printf("%s [error opening dir]\n", path)
		t.status = 1
		return
	}
	fmt.Println(path)
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
		branch, continuation := treeBranch, treeVertical
		if index == len(entries)-1 {
			branch, continuation = treeBranchLast, treeBlank
		}
		if t.noIndent {
			branch, continuation = "", ""
		}
		path := filepath.Join(directory, entry.Name())
		shown := strings.TrimSuffix(display, "/") + "/" + entry.Name()
		line := prefix + branch + t.decorate(entry.Name(), shown, path, info)
		if !info.IsDir() {
			t.files++
			fmt.Println(line)
			continue
		}
		t.directories++
		var children []os.DirEntry
		if t.level == 0 || depth < t.level {
			read, err := os.ReadDir(path)
			if err != nil {
				line += " [error opening dir]"
				t.status = 1
			} else {
				children = t.selectEntries(read)
			}
		}
		fmt.Println(line)
		t.walk(path, shown, prefix+continuation, depth+1, children)
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
		// -P filters files only: a directory still has to be walked, because
		// a match may be somewhere below it.
		if len(t.patterns) > 0 && !entry.IsDir() && !matchesAnyPattern(t.patterns, entry.Name()) {
			continue
		}
		kept = append(kept, entry)
	}
	if t.unsorted {
		return kept
	}
	modified := make(map[string]time.Time, len(kept))
	if t.byTime {
		for _, entry := range kept {
			if info, err := entry.Info(); err == nil {
				modified[entry.Name()] = info.ModTime()
			}
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		left, right := kept[i], kept[j]
		if t.dirsFirst && left.IsDir() != right.IsDir() {
			return left.IsDir()
		}
		less := left.Name() < right.Name()
		if t.byTime {
			less = modified[left.Name()].Before(modified[right.Name()])
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

// decorate builds one displayed name: the optional -p and -s columns, the name
// itself or its full path, a symlink target, and the -F type marker.
func (t *treeOptions) decorate(name, display, path string, info os.FileInfo) string {
	var line strings.Builder
	if t.permission {
		fmt.Fprintf(&line, "[%s]  ", modeString(info.Mode()))
	}
	if t.sizes {
		if t.human {
			fmt.Fprintf(&line, "[%4s]  ", humanSize(info.Size()))
		} else {
			fmt.Fprintf(&line, "[%11d]  ", info.Size())
		}
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
