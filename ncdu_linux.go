// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ncduOptions is one ncdu(1) command line. Nothing here can modify the
// filesystem: the browser is read-only by design, so the deletion, refresh, and
// shell features of the original are absent rather than disabled.
type ncduOptions struct {
	apparent        bool
	oneFileSystem   bool
	si              bool
	excludes        []string
	excludeCaches   bool
	excludeKernfs   bool
	followSymlinks  bool
	extended        bool
	threads         int
	compress        bool
	compressLevel   int
	exportBlockSize int

	showItems     bool
	showMtime     bool
	showGraph     bool
	showPercent   bool
	hideHidden    bool
	dirsFirst     bool
	natsort       bool
	sortCol       string
	sortDesc      bool
	graphStyle    string
	sharedColumn  string
	colorScheme   string
	confirmQuit   bool
	confirmDelete bool
	deleteCmd     string
}

// ncduEntry is one scanned file or directory. Directories carry the totals of
// everything below them, which is what the browser sorts and draws.
type ncduEntry struct {
	name      string
	size      uint64 // apparent size
	disk      uint64 // allocated size
	items     int
	directory bool
	symlink   bool
	failed    bool
	mtime     time.Time
	uid       uint32
	gid       uint32
	children  []*ncduEntry
	parent    *ncduEntry
}

func isCacheDir(dir string) bool {
	f, err := os.Open(filepath.Join(dir, "CACHEDIR.TAG"))
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 43)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return false
	}
	return bytes.HasPrefix(buf[:n], []byte("Signature: 8a477f597d28d172721ae73c2e6197ff"))
}

func isKernFS(path string) bool {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false
	}
	switch stat.Type {
	case 0x9fa0, 0x62656572, 0x1cd1, 0x27e0eb, 0x63677270, 0x73636673, 0x42494e4d, 0x64657670:
		return true
	}
	return false
}

func cmdNcdu(args []string) int {
	options := ncduOptions{
		showGraph:  true,
		sortCol:    "disk-usage",
		sortDesc:   true,
		graphStyle: "hash",
	}
	path, exportPath, importPath := "", "", ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--apparent-size":
			options.apparent = true
		case arg == "--disk-usage":
			options.apparent = false
		case arg == "--si":
			options.si = true
		case arg == "--no-si":
			options.si = false
		case arg == "-x" || arg == "--one-file-system":
			options.oneFileSystem = true
		case arg == "--cross-file-system":
			options.oneFileSystem = false
		case arg == "--exclude-caches":
			options.excludeCaches = true
		case arg == "--include-caches":
			options.excludeCaches = false
		case arg == "--exclude-kernfs":
			options.excludeKernfs = true
		case arg == "--include-kernfs":
			options.excludeKernfs = false
		case arg == "-L" || arg == "--follow-symlinks":
			options.followSymlinks = true
		case arg == "--no-follow-symlinks":
			options.followSymlinks = false
		case arg == "-e" || arg == "--extended":
			options.extended = true
		case arg == "--no-extended":
			options.extended = false
		case arg == "-c" || arg == "--compress":
			options.compress = true
		case arg == "--no-compress":
			options.compress = false
		case strings.HasPrefix(arg, "--compress-level="):
			options.compressLevel, _ = strconv.Atoi(strings.TrimPrefix(arg, "--compress-level="))
		case arg == "--compress-level":
			if i+1 >= len(args) {
				fatalf("ncdu", "--compress-level requires an argument")
				return 1
			}
			i++
			options.compressLevel, _ = strconv.Atoi(args[i])
		case strings.HasPrefix(arg, "--export-block-size="):
			options.exportBlockSize, _ = strconv.Atoi(strings.TrimPrefix(arg, "--export-block-size="))
		case arg == "--export-block-size":
			if i+1 >= len(args) {
				fatalf("ncdu", "--export-block-size requires an argument")
				return 1
			}
			i++
			options.exportBlockSize, _ = strconv.Atoi(args[i])
		case arg == "-t" || arg == "--threads":
			if i+1 >= len(args) {
				fatalf("ncdu", "%s requires an argument", arg)
				return 1
			}
			i++
			options.threads, _ = strconv.Atoi(args[i])
		case strings.HasPrefix(arg, "--threads="):
			options.threads, _ = strconv.Atoi(strings.TrimPrefix(arg, "--threads="))
		case arg == "--exclude":
			if i+1 >= len(args) {
				fatalf("ncdu", "--exclude requires a pattern")
				return 1
			}
			i++
			options.excludes = append(options.excludes, args[i])
		case strings.HasPrefix(arg, "--exclude="):
			options.excludes = append(options.excludes, strings.TrimPrefix(arg, "--exclude="))
		case arg == "-X" || arg == "--exclude-from":
			if i+1 >= len(args) {
				fatalf("ncdu", "%s requires a file", arg)
				return 1
			}
			i++
			content, err := os.ReadFile(args[i])
			if err != nil {
				fatalf("ncdu", "%v", err)
				return 1
			}
			for _, line := range strings.Split(string(content), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					options.excludes = append(options.excludes, line)
				}
			}
		case strings.HasPrefix(arg, "--exclude-from="):
			file := strings.TrimPrefix(arg, "--exclude-from=")
			content, err := os.ReadFile(file)
			if err != nil {
				fatalf("ncdu", "%v", err)
				return 1
			}
			for _, line := range strings.Split(string(content), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					options.excludes = append(options.excludes, line)
				}
			}
		case arg == "-o" || arg == "--output" || arg == "-O":
			if i+1 >= len(args) {
				fatalf("ncdu", "%s requires a file", arg)
				return 1
			}
			i++
			exportPath = args[i]
		case arg == "-f":
			if i+1 >= len(args) {
				fatalf("ncdu", "-f requires a file")
				return 1
			}
			i++
			importPath = args[i]
		case arg == "-r", arg == "-rr", arg == "--read-only",
			arg == "--enable-shell", arg == "--disable-shell",
			arg == "--enable-delete", arg == "--disable-delete",
			arg == "--enable-refresh", arg == "--disable-refresh",
			arg == "--ignore-config":
			// Accepted for compatibility: this browser is strictly read-only by design.
		case arg == "-q", arg == "--slow-ui-updates", arg == "--fast-ui-updates":
			// Scan/UI update rate compatibility options.
		case arg == "--show-itemcount":
			options.showItems = true
		case arg == "--hide-itemcount":
			options.showItems = false
		case arg == "--show-mtime":
			options.showMtime = true
		case arg == "--hide-mtime":
			options.showMtime = false
		case arg == "--show-graph":
			options.showGraph = true
		case arg == "--hide-graph":
			options.showGraph = false
		case arg == "--show-percent":
			options.showPercent = true
		case arg == "--hide-percent":
			options.showPercent = false
		case arg == "--show-hidden":
			options.hideHidden = false
		case arg == "--hide-hidden":
			options.hideHidden = true
		case arg == "--group-directories-first":
			options.dirsFirst = true
		case arg == "--no-group-directories-first":
			options.dirsFirst = false
		case arg == "--enable-natsort":
			options.natsort = true
		case arg == "--disable-natsort":
			options.natsort = false
		case arg == "--confirm-quit":
			options.confirmQuit = true
		case arg == "--no-confirm-quit":
			options.confirmQuit = false
		case arg == "--confirm-delete":
			options.confirmDelete = true
		case arg == "--no-confirm-delete":
			options.confirmDelete = false
		case strings.HasPrefix(arg, "--delete-command="):
			options.deleteCmd = strings.TrimPrefix(arg, "--delete-command=")
		case arg == "--delete-command":
			if i+1 >= len(args) {
				fatalf("ncdu", "--delete-command requires an argument")
				return 1
			}
			i++
			options.deleteCmd = args[i]
		case strings.HasPrefix(arg, "--graph-style="):
			options.graphStyle = strings.TrimPrefix(arg, "--graph-style=")
		case arg == "--graph-style":
			if i+1 >= len(args) {
				fatalf("ncdu", "--graph-style requires an argument")
				return 1
			}
			i++
			options.graphStyle = args[i]
		case strings.HasPrefix(arg, "--shared-column="):
			options.sharedColumn = strings.TrimPrefix(arg, "--shared-column=")
		case arg == "--shared-column":
			if i+1 >= len(args) {
				fatalf("ncdu", "--shared-column requires an argument")
				return 1
			}
			i++
			options.sharedColumn = args[i]
		case strings.HasPrefix(arg, "--color="):
			options.colorScheme = strings.TrimPrefix(arg, "--color=")
		case arg == "--color":
			if i+1 >= len(args) {
				fatalf("ncdu", "--color requires an argument")
				return 1
			}
			i++
			options.colorScheme = args[i]
		case strings.HasPrefix(arg, "--sort="):
			col := strings.TrimPrefix(arg, "--sort=")
			if strings.HasSuffix(col, "-asc") {
				options.sortCol = strings.TrimSuffix(col, "-asc")
				options.sortDesc = false
			} else if strings.HasSuffix(col, "-desc") {
				options.sortCol = strings.TrimSuffix(col, "-desc")
				options.sortDesc = true
			} else {
				options.sortCol = col
				options.sortDesc = true
			}
		case arg == "--sort":
			if i+1 >= len(args) {
				fatalf("ncdu", "--sort requires an argument")
				return 1
			}
			i++
			col := args[i]
			if strings.HasSuffix(col, "-asc") {
				options.sortCol = strings.TrimSuffix(col, "-asc")
				options.sortDesc = false
			} else if strings.HasSuffix(col, "-desc") {
				options.sortCol = strings.TrimSuffix(col, "-desc")
				options.sortDesc = true
			} else {
				options.sortCol = col
				options.sortDesc = true
			}
		case len(arg) > 1 && arg[0] == '-':
			for _, flag := range arg[1:] {
				switch flag {
				case 'x':
					options.oneFileSystem = true
				case 'e':
					options.extended = true
				case 'L':
					options.followSymlinks = true
				case 'c':
					options.compress = true
				case '0', '1', '2':
					// Scan-time interface selection; the scan is silent here.
				default:
					fatalf("ncdu", "invalid option -- '%c'", flag)
					return 1
				}
			}
		default:
			if path != "" {
				fatalf("ncdu", "only one directory can be scanned")
				return 1
			}
			path = arg
		}
	}

	var root *ncduEntry
	if importPath != "" {
		if path != "" {
			fatalf("ncdu", "-f cannot be combined with a directory to scan")
			return 1
		}
		imported, err := ncduImport(importPath)
		if err != nil {
			fatalf("ncdu", "%v", err)
			return 1
		}
		root = imported
	} else {
		if path == "" {
			path = "."
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			fatalf("ncdu", "%s: %v", path, err)
			return 1
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			fatalf("ncdu", "%v", err)
			return 1
		}
		if !info.IsDir() {
			fatalf("ncdu", "%s: not a directory", path)
			return 1
		}
		fmt.Print("Scanning...")
		scan := newNcduScan(absolute, options)
		root = scan.walk(absolute, info)
		root.name = absolute
		fmt.Print("\r\x1b[K")
		if exportPath != "" {
			if err := ncduExportCompressed(root, exportPath, options.compress); err != nil {
				fatalf("ncdu", "%v", err)
				return 1
			}
		}
	}

	if exportPath != "" {
		return 0
	}

	// The browser paints a full screen and reads single keys, so it needs a
	// terminal on both ends.
	if !isTerminal(os.Stdin.Fd()) || !isTerminal(os.Stdout.Fd()) {
		fatalf("ncdu", "standard input and output must be a terminal")
		return 1
	}
	browser := &ncduBrowser{root: root, current: root, options: options}
	if err := browser.run(); err != nil {
		fatalf("ncdu", "%v", err)
		return 1
	}
	return 0
}

// ncduExportEntry is one node of ncdu's own JSON export format: a leaf file
// is one such object, and a directory is a JSON array whose first element is
// its own object followed by one entry (object or, for a subdirectory,
// another such array) per child.
type ncduExportEntry struct {
	Name  string  `json:"name"`
	ASize uint64  `json:"asize"`
	DSize *uint64 `json:"dsize,omitempty"`
	Dev   *uint64 `json:"dev,omitempty"`
	Mtime *int64  `json:"mtime,omitempty"`
	Uid   *uint32 `json:"uid,omitempty"`
	Gid   *uint32 `json:"gid,omitempty"`
}

func ncduEntryToExport(e *ncduEntry, isRoot bool) any {
	obj := ncduExportEntry{Name: e.name, ASize: e.size}
	if !e.directory && e.disk != e.size {
		disk := e.disk
		obj.DSize = &disk
	}
	if !e.mtime.IsZero() {
		m := e.mtime.Unix()
		obj.Mtime = &m
	}
	if e.uid != 0 {
		u := e.uid
		obj.Uid = &u
	}
	if e.gid != 0 {
		g := e.gid
		obj.Gid = &g
	}
	if isRoot {
		var status syscall.Stat_t
		if err := syscall.Lstat(e.path(), &status); err == nil {
			dev := status.Dev
			obj.Dev = &dev
		}
	}
	if !e.directory {
		return obj
	}
	array := make([]any, 0, len(e.children)+1)
	array = append(array, obj)
	for _, child := range e.children {
		array = append(array, ncduEntryToExport(child, false))
	}
	return array
}

// ncduExport writes root in ncdu's own export format (the "ncdu -o" format,
// version 1.2), so a scan taken now -- on a disk that might not stay mounted
// or readable -- can be browsed again later, on this machine or another,
// without needing the filesystem itself.
func ncduExport(root *ncduEntry, path string) error {
	return ncduExportCompressed(root, path, false)
}

func ncduExportCompressed(root *ncduEntry, path string, compress bool) error {
	payload := []any{
		1, 2,
		map[string]any{"progname": "ba6-ncdu", "progver": "1", "timestamp": time.Now().Unix()},
		ncduEntryToExport(root, true),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if compress || strings.HasSuffix(path, ".gz") {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, err := gw.Write(data); err != nil {
			return err
		}
		if err := gw.Close(); err != nil {
			return err
		}
		data = buf.Bytes()
	}
	return os.WriteFile(path, data, 0o600)
}

// ncduImport reads back a tree written by ncduExport, or by real ncdu's own
// -o: either one is just the array-of-arrays schema above.
func ncduImport(path string) (*ncduEntry, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: ncdu -f intentionally reads a user-named export file
	if err != nil {
		return nil, err
	}
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		decompressed, err := io.ReadAll(gr)
		gr.Close()
		if err != nil {
			return nil, err
		}
		data = decompressed
	}
	var outer []json.RawMessage
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil, fmt.Errorf("not an ncdu export: %w", err)
	}
	if len(outer) < 4 {
		return nil, fmt.Errorf("not an ncdu export: too few top-level fields")
	}
	return ncduImportEntry(outer[3], nil)
}

func ncduImportEntry(raw json.RawMessage, parent *ncduEntry) (*ncduEntry, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty entry in ncdu export")
	}
	if trimmed[0] != '[' {
		var leaf ncduExportEntry
		if err := json.Unmarshal(raw, &leaf); err != nil {
			return nil, err
		}
		disk := leaf.ASize
		if leaf.DSize != nil {
			disk = *leaf.DSize
		}
		entry := &ncduEntry{name: leaf.Name, size: leaf.ASize, disk: disk, parent: parent}
		if leaf.Mtime != nil {
			entry.mtime = time.Unix(*leaf.Mtime, 0)
		}
		if leaf.Uid != nil {
			entry.uid = *leaf.Uid
		}
		if leaf.Gid != nil {
			entry.gid = *leaf.Gid
		}
		return entry, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("directory entry has no fields")
	}
	var obj ncduExportEntry
	if err := json.Unmarshal(items[0], &obj); err != nil {
		return nil, err
	}
	dir := &ncduEntry{name: obj.Name, directory: true, parent: parent}
	if obj.Mtime != nil {
		dir.mtime = time.Unix(*obj.Mtime, 0)
	}
	if obj.Uid != nil {
		dir.uid = *obj.Uid
	}
	if obj.Gid != nil {
		dir.gid = *obj.Gid
	}
	for _, child := range items[1:] {
		imported, err := ncduImportEntry(child, dir)
		if err != nil {
			return nil, err
		}
		dir.children = append(dir.children, imported)
		dir.size += imported.size
		dir.disk += imported.disk
		dir.items += imported.items + 1
	}
	return dir, nil
}

// ncduScan holds the state a single scan carries across directories: the
// filesystem it started on, for -x, and the inodes of the hard-linked files it
// has already counted.
type ncduScan struct {
	options ncduOptions
	device  uint64
	seen    map[duIdentity]bool
}

func newNcduScan(root string, options ncduOptions) *ncduScan {
	scan := &ncduScan{options: options, seen: map[duIdentity]bool{}}
	var status syscall.Stat_t
	if err := syscall.Lstat(root, &status); err == nil {
		scan.device = status.Dev
	}
	return scan
}

func (s *ncduScan) walk(path string, info os.FileInfo) *ncduEntry {
	entry := &ncduEntry{
		name:      filepath.Base(path),
		size:      uint64(info.Size()), //nolint:gosec // G115: file sizes are nonnegative.
		disk:      allocatedBytes(info),
		directory: info.IsDir(),
		symlink:   info.Mode()&os.ModeSymlink != 0,
		mtime:     info.ModTime(),
	}
	if status, ok := info.Sys().(*syscall.Stat_t); ok {
		entry.uid = status.Uid
		entry.gid = status.Gid
	}
	// A hard-linked file is counted once, the way du and ncdu both count it.
	if status, ok := info.Sys().(*syscall.Stat_t); ok && status.Nlink > 1 && !info.IsDir() {
		identity := duIdentity{dev: status.Dev, ino: status.Ino}
		if s.seen[identity] {
			entry.size, entry.disk = 0, 0
		} else {
			s.seen[identity] = true
		}
	}
	if !entry.directory {
		return entry
	}
	names, err := os.ReadDir(path)
	if err != nil {
		entry.failed = true
		return entry
	}
	for _, name := range names {
		if matchesAnyPattern(s.options.excludes, name.Name()) {
			continue
		}
		child := filepath.Join(path, name.Name())
		var childInfo os.FileInfo
		if s.options.followSymlinks && name.Type()&os.ModeSymlink != 0 {
			targetInfo, err := os.Stat(child)
			if err == nil && !targetInfo.IsDir() {
				childInfo = targetInfo
			} else {
				childInfo, _ = os.Lstat(child)
			}
		} else {
			childInfo, err = os.Lstat(child)
		}
		if err != nil {
			entry.failed = true
			continue
		}
		if s.options.oneFileSystem {
			if status, ok := childInfo.Sys().(*syscall.Stat_t); ok && status.Dev != s.device {
				continue
			}
		}
		if childInfo.IsDir() {
			if s.options.excludeCaches && isCacheDir(child) {
				continue
			}
			if s.options.excludeKernfs && isKernFS(child) {
				continue
			}
		}
		scanned := s.walk(child, childInfo)
		scanned.parent = entry
		entry.children = append(entry.children, scanned)
		entry.size += scanned.size
		entry.disk += scanned.disk
		entry.items += scanned.items + 1
	}
	return entry
}

// weight is the number the browser sorts and draws with: allocated size by
// default, apparent size under --apparent-size or after pressing "a".
func (e *ncduEntry) weight(apparent bool) uint64 {
	if apparent {
		return e.size
	}
	return e.disk
}

func (e *ncduEntry) path() string {
	if e.parent == nil {
		return e.name
	}
	return filepath.Join(e.parent.path(), e.name)
}

type ncduBrowser struct {
	root       *ncduEntry
	current    *ncduEntry
	options    ncduOptions
	cursor     int
	offset     int
	rows, cols int
	help       bool
}

func (b *ncduBrowser) run() error {
	old, err := terminalRaw(os.Stdin.Fd())
	if err != nil {
		return err
	}
	defer func() {
		restoreTerminal(os.Stdin.Fd(), old)
		// Leave the alternate screen and make the cursor visible again.
		fmt.Print("\x1b[?1049l\x1b[?25h")
	}()
	fmt.Print("\x1b[?1049h\x1b[?25l")
	b.sortChildren()
	for {
		b.draw()
		key, err := readEditorKey()
		if err != nil {
			return nil
		}
		if b.handleKey(key) {
			return nil
		}
	}
}

// handleKey acts on one key press and reports whether the browser should exit.
func (b *ncduBrowser) handleKey(key int) bool {
	if b.help {
		b.help = false
		return false
	}
	switch key {
	case 'q', 3: // q or Ctrl-C
		return true
	case '?':
		b.help = true
	case 1000, 'k': // up
		b.move(-1)
	case 1001, 'j': // down
		b.move(1)
	case 1006: // page up
		b.move(-(b.listHeight() - 1))
	case 1007: // page down
		b.move(b.listHeight() - 1)
	case 1004: // home
		b.cursor = 0
	case 1005: // end
		b.cursor = len(b.entries()) - 1
	case 1003, '\r', '\n', 'l': // right or enter
		b.descend()
	case 1002, 'h': // left
		b.ascend()
	case 'n':
		if b.options.sortCol == "name" {
			b.options.sortDesc = !b.options.sortDesc
		} else {
			b.options.sortCol = "name"
			b.options.sortDesc = false
		}
		b.sortChildren()
	case 's':
		if b.options.sortCol == "disk-usage" || b.options.sortCol == "apparent-size" {
			b.options.sortDesc = !b.options.sortDesc
		} else {
			if b.options.apparent {
				b.options.sortCol = "apparent-size"
			} else {
				b.options.sortCol = "disk-usage"
			}
			b.options.sortDesc = true
		}
		b.sortChildren()
	case 'C':
		if b.options.sortCol == "itemcount" {
			b.options.sortDesc = !b.options.sortDesc
		} else {
			b.options.sortCol = "itemcount"
			b.options.sortDesc = true
		}
		b.sortChildren()
	case 'M':
		if b.options.sortCol == "mtime" {
			b.options.sortDesc = !b.options.sortDesc
		} else {
			b.options.sortCol = "mtime"
			b.options.sortDesc = true
		}
		b.sortChildren()
	case 'a':
		b.options.apparent = !b.options.apparent
		if b.options.sortCol == "disk-usage" {
			b.options.sortCol = "apparent-size"
		} else if b.options.sortCol == "apparent-size" {
			b.options.sortCol = "disk-usage"
		}
		b.sortChildren()
	case 'c':
		b.options.showItems = !b.options.showItems
	case 'm':
		b.options.showMtime = !b.options.showMtime
	case 't':
		b.options.dirsFirst = !b.options.dirsFirst
		b.sortChildren()
	case 'e':
		b.options.hideHidden = !b.options.hideHidden
	case 'g':
		switch b.options.graphStyle {
		case "hash":
			b.options.graphStyle = "half-block"
		case "half-block":
			b.options.graphStyle = "eighth-block"
		case "eighth-block":
			if b.options.showGraph {
				b.options.showGraph = false
			} else {
				b.options.showGraph = true
				b.options.graphStyle = "hash"
			}
		default:
			b.options.showGraph = !b.options.showGraph
			b.options.graphStyle = "hash"
		}
	}
	return false
}

// entries is the current directory's listing: the parent link first, the way
// ncdu shows "/..", followed by the sorted children.
func (b *ncduBrowser) entries() []*ncduEntry {
	var list []*ncduEntry
	if b.current.parent != nil {
		list = append(list, &ncduEntry{name: "..", directory: true})
	}
	for _, child := range b.current.children {
		if b.options.hideHidden && strings.HasPrefix(child.name, ".") {
			continue
		}
		list = append(list, child)
	}
	return list
}

func (b *ncduBrowser) sortChildren() {
	children := b.current.children
	sort.SliceStable(children, func(i, j int) bool {
		c1, c2 := children[i], children[j]
		if b.options.dirsFirst && c1.directory != c2.directory {
			return c1.directory // directories first
		}
		cmp := 0
		switch b.options.sortCol {
		case "name":
			if b.options.natsort {
				if lsVersionLess(c1.name, c2.name) {
					cmp = -1
				} else if lsVersionLess(c2.name, c1.name) {
					cmp = 1
				}
			} else {
				if c1.name < c2.name {
					cmp = -1
				} else if c1.name > c2.name {
					cmp = 1
				}
			}
		case "itemcount":
			if c1.items < c2.items {
				cmp = -1
			} else if c1.items > c2.items {
				cmp = 1
			}
		case "mtime":
			if c1.mtime.Before(c2.mtime) {
				cmp = -1
			} else if c2.mtime.Before(c1.mtime) {
				cmp = 1
			}
		case "apparent-size":
			if c1.size < c2.size {
				cmp = -1
			} else if c1.size > c2.size {
				cmp = 1
			}
		case "disk-usage":
			fallthrough
		default:
			w1 := c1.weight(b.options.apparent)
			w2 := c2.weight(b.options.apparent)
			if w1 < w2 {
				cmp = -1
			} else if w1 > w2 {
				cmp = 1
			}
		}
		if cmp == 0 {
			return c1.name < c2.name
		}
		if b.options.sortDesc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func (b *ncduBrowser) move(delta int) {
	b.cursor += delta
	if count := len(b.entries()); b.cursor >= count {
		b.cursor = count - 1
	}
	if b.cursor < 0 {
		b.cursor = 0
	}
}

func (b *ncduBrowser) descend() {
	entries := b.entries()
	if b.cursor >= len(entries) {
		return
	}
	target := entries[b.cursor]
	if target.name == ".." {
		b.ascend()
		return
	}
	if !target.directory || target.symlink {
		return
	}
	b.current, b.cursor, b.offset = target, 0, 0
	b.sortChildren()
}

func (b *ncduBrowser) ascend() {
	if b.current.parent == nil {
		return
	}
	child := b.current
	b.current, b.offset = b.current.parent, 0
	b.cursor = 0
	// The parent may have been sorted differently before the browser descended.
	b.sortChildren()
	for index, entry := range b.entries() {
		if entry == child {
			b.cursor = index
		}
	}
}

func (b *ncduBrowser) listHeight() int {
	if b.rows < 4 {
		return 1
	}
	return b.rows - 3
}

// graphWidth is the width of the bar column, which ncdu derives from the
// terminal width.
func (b *ncduBrowser) graphWidth() int {
	if !b.options.showGraph {
		return 0
	}
	width := b.cols / 7
	if width < 3 {
		return 0
	}
	return width
}

func (b *ncduBrowser) draw() {
	rows, cols, ok := terminalDimensions(os.Stdout.Fd())
	if !ok {
		rows, cols = 24, 80
	}
	b.rows, b.cols = rows, cols
	entries := b.entries()
	height := b.listHeight()
	if b.cursor >= len(entries) {
		b.cursor = maxInt(0, len(entries)-1)
	}
	if b.cursor < b.offset {
		b.offset = b.cursor
	}
	if b.cursor >= b.offset+height {
		b.offset = b.cursor - height + 1
	}

	var screen strings.Builder
	screen.WriteString("\x1b[H\x1b[2J")
	screen.WriteString("\x1b[7m" +
		b.padded("ncdu ~ Use the arrow keys to navigate, press ? for help", "[readonly]") + "\x1b[m\r\n")
	screen.WriteString(b.headerPath() + "\r\n")
	if b.help {
		b.drawHelp(&screen)
	} else {
		b.drawList(&screen, entries, height)
	}
	fmt.Fprintf(&screen, "\x1b[%d;1H\x1b[7m%s\x1b[m", b.rows, b.footer())
	fmt.Print(screen.String())
}

func (b *ncduBrowser) drawList(screen *strings.Builder, entries []*ncduEntry, height int) {
	largest := uint64(0)
	dirTotal := uint64(0)
	for _, entry := range b.current.children {
		w := entry.weight(b.options.apparent)
		if w > largest {
			largest = w
		}
		dirTotal += w
	}
	for row := 0; row < height; row++ {
		index := b.offset + row
		if index >= len(entries) {
			screen.WriteString("\r\n")
			continue
		}
		line := b.entryLine(entries[index], largest, dirTotal)
		if index == b.cursor {
			// The selected row is highlighted across the whole width.
			line = "\x1b[7m" + line + strings.Repeat(" ", maxInt(0, b.cols-len(line))) + "\x1b[m"
		}
		screen.WriteString(line + "\r\n")
	}
}

// entryLine draws one row: two leading spaces, optional items/mtime/percent, size, the bar,
// and the name, which a directory prefixes with a slash.
func (b *ncduBrowser) entryLine(entry *ncduEntry, largest, dirTotal uint64) string {
	var parts []string
	if entry.name == ".." {
		parts = append(parts, fmt.Sprintf("%9s", ""))
		if b.options.showItems {
			parts = append(parts, fmt.Sprintf("%12s", ""))
		}
		if b.options.showMtime {
			parts = append(parts, strings.Repeat(" ", 19))
		}
		if b.options.showPercent {
			parts = append(parts, fmt.Sprintf("%8s", ""))
		}
		if b.options.showGraph && b.graphBrackets() > 0 {
			parts = append(parts, strings.Repeat(" ", b.graphBrackets()))
		}
		parts = append(parts, "/..")
		line := "  " + strings.Join(parts, " ")
		if len(line) > b.cols {
			line = line[:b.cols]
		}
		return line
	}

	w := entry.weight(b.options.apparent)
	parts = append(parts, ncduSize(w, b.options.si))

	if b.options.showItems {
		if entry.directory {
			parts = append(parts, fmt.Sprintf("%6d items", entry.items))
		} else {
			parts = append(parts, fmt.Sprintf("%12s", ""))
		}
	}

	if b.options.showMtime {
		if !entry.mtime.IsZero() {
			parts = append(parts, entry.mtime.Format("2006-01-02 15:04:05"))
		} else {
			parts = append(parts, strings.Repeat(" ", 19))
		}
	}

	if b.options.showPercent {
		pct := 0.0
		if dirTotal > 0 {
			pct = float64(w) / float64(dirTotal) * 100.0
		}
		parts = append(parts, fmt.Sprintf("[%5.1f%%]", pct))
	}

	if b.options.showGraph {
		if g := b.graph(w, largest); g != "" {
			parts = append(parts, g)
		}
	}

	marker := " "
	if entry.directory {
		marker = "/"
	}
	name := entry.name
	if entry.failed {
		name = "!" + name
	}
	parts = append(parts, marker+name)

	line := "  " + strings.Join(parts, " ")
	if len(line) > b.cols {
		line = line[:b.cols]
	}
	return line
}

// graph draws the bar column relative to the largest entry of the directory.
func (b *ncduBrowser) graph(value, largest uint64) string {
	width := b.graphWidth()
	if width == 0 {
		return ""
	}
	switch b.options.graphStyle {
	case "half-block":
		slots := width * 2
		filled := 0
		if largest > 0 {
			filled = int(value * uint64(slots) / largest) //nolint:gosec // quotient bounded by slots
		}
		full := filled / 2
		rem := filled % 2
		var sb strings.Builder
		sb.WriteByte('[')
		sb.WriteString(strings.Repeat("█", full))
		used := full
		if rem > 0 {
			sb.WriteString("▌")
			used++
		}
		if width > used {
			sb.WriteString(strings.Repeat(" ", width-used))
		}
		sb.WriteByte(']')
		return sb.String()

	case "eighth-block":
		slots := width * 8
		filled := 0
		if largest > 0 {
			filled = int(value * uint64(slots) / largest) //nolint:gosec // quotient bounded by slots
		}
		full := filled / 8
		rem := filled % 8
		var sb strings.Builder
		sb.WriteByte('[')
		sb.WriteString(strings.Repeat("█", full))
		eighths := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
		used := full
		if rem > 0 {
			sb.WriteString(eighths[rem])
			used++
		}
		if width > used {
			sb.WriteString(strings.Repeat(" ", width-used))
		}
		sb.WriteByte(']')
		return sb.String()

	case "hash":
		fallthrough
	default:
		filled := 0
		if largest > 0 {
			filled = int(value * uint64(width) / largest) //nolint:gosec // quotient bounded by width
		}
		return "[" + strings.Repeat("#", filled) + strings.Repeat(" ", width-filled) + "]"
	}
}

// graphBrackets is the printed width of the bar column, brackets included.
func (b *ncduBrowser) graphBrackets() int {
	if width := b.graphWidth(); width > 0 {
		return width + 2
	}
	return 0
}

// headerPath is the second line: the directory being browsed, shortened in the
// middle when it is too long, followed by a rule.
func (b *ncduBrowser) headerPath() string {
	prefix := "--- " + ncduShortenPath(b.current.path(), maxInt(0, b.cols-5)) + " "
	if len(prefix) >= b.cols {
		return prefix[:maxInt(0, b.cols)]
	}
	return prefix + strings.Repeat("-", b.cols-len(prefix))
}

func ncduShortenPath(path string, width int) string {
	if len(path) <= width {
		return path
	}
	if width <= 3 {
		return path[:maxInt(0, width)]
	}
	head := (width - 3) / 2
	return path[:head] + "..." + path[len(path)-(width-3-head):]
}

// footer reports the totals of the whole scan and marks the size the listing is
// currently sorted and drawn by.
func (b *ncduBrowser) footer() string {
	disk, apparent := "*", " "
	if b.options.apparent {
		disk, apparent = " ", "*"
	}
	text := fmt.Sprintf("%sTotal disk usage: %s  %sApparent size: %s   Items: %d",
		disk, ncduSize(b.root.disk, b.options.si), apparent, ncduSize(b.root.size, b.options.si), b.root.items)
	if len(text) > b.cols {
		return text[:b.cols]
	}
	return text + strings.Repeat(" ", b.cols-len(text))
}

// padded lays a left and a right string on the same line, filling the middle.
func (b *ncduBrowser) padded(left, right string) string {
	if len(left)+len(right)+1 > b.cols {
		if len(left) > b.cols {
			return left[:b.cols]
		}
		return left
	}
	return left + strings.Repeat(" ", b.cols-len(left)-len(right)) + right
}

func (b *ncduBrowser) drawHelp(screen *strings.Builder) {
	help := []string{
		"",
		"   up, down, j, k      move the selection",
		"   right, enter, l     open the selected directory",
		"   left, h             go to the parent directory",
		"   n                   sort by name (again to reverse)",
		"   s                   sort by size (again to reverse)",
		"   C                   sort by items (again to reverse)",
		"   M                   sort by mtime (again to reverse)",
		"   a                   switch between disk usage and apparent size",
		"   c                   toggle display of item counts",
		"   m                   toggle display of mtime",
		"   g                   toggle graph / cycle graph style",
		"   t                   toggle directories first",
		"   e                   toggle hidden files",
		"   q                   quit",
		"",
		"   This browser never deletes or modifies anything.",
		"",
		"   Press any key to return.",
	}
	for row := 0; row < b.listHeight(); row++ {
		if row < len(help) {
			screen.WriteString(help[row])
		}
		screen.WriteString("\r\n")
	}
}

// ncduSize formats a byte count the way ncdu does: five characters of number,
// a space, and the unit, which is three characters wide for the binary
// prefixes and two for the SI ones. The scale changes at 1000 rather than 1024,
// so 1023 bytes already reads as 1.0 KiB.
func ncduSize(value uint64, si bool) string {
	units, base, width := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}, 1024.0, 3
	if si {
		units, base, width = []string{"B", "kB", "MB", "GB", "TB", "PB"}, 1000.0, 2
	}
	size := float64(value)
	index := 0
	for size >= 1000 && index < len(units)-1 {
		size /= base
		index++
	}
	return fmt.Sprintf("%5.1f %*s", size, width, units[index])
}
