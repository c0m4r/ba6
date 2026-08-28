// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ncduOptions is one ncdu(1) command line. Nothing here can modify the
// filesystem: the browser is read-only by design, so the deletion, refresh, and
// shell features of the original are absent rather than disabled.
type ncduOptions struct {
	apparent      bool
	oneFileSystem bool
	si            bool
	excludes      []string
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
	children  []*ncduEntry
	parent    *ncduEntry
}

func cmdNcdu(args []string) int {
	var options ncduOptions
	path, exportPath, importPath := "", "", ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--apparent-size":
			options.apparent = true
		case arg == "--si":
			options.si = true
		case arg == "--one-file-system":
			options.oneFileSystem = true
		case arg == "--exclude":
			if i+1 >= len(args) {
				fatalf("ncdu", "--exclude requires a pattern")
				return 1
			}
			i++
			options.excludes = append(options.excludes, args[i])
		case arg == "-o" || arg == "--output":
			if i+1 >= len(args) {
				fatalf("ncdu", "-o requires a file")
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
		case arg == "-r", arg == "-rr", arg == "--read-only":
			// Accepted for compatibility: this browser never writes anything.
		case arg == "-q", arg == "--slow-ui-updates":
			// The screen is only drawn in response to a key press anyway.
		case len(arg) > 1 && arg[0] == '-':
			for _, flag := range arg[1:] {
				switch flag {
				case 'x':
					options.oneFileSystem = true
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
			if err := ncduExport(root, exportPath); err != nil {
				fatalf("ncdu", "%v", err)
				return 1
			}
		}
	}

	// The browser paints a full screen and reads single keys, so it needs a
	// terminal on both ends.
	if !isTerminal(os.Stdin.Fd()) || !isTerminal(os.Stdout.Fd()) {
		fatalf("ncdu", "standard input and output must be a terminal")
		return 1
	}
	browser := &ncduBrowser{root: root, current: root, options: options, sortBySize: true}
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
}

func ncduEntryToExport(e *ncduEntry, isRoot bool) any {
	obj := ncduExportEntry{Name: e.name, ASize: e.size}
	if !e.directory && e.disk != e.size {
		disk := e.disk
		obj.DSize = &disk
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
	payload := []any{
		1, 2,
		map[string]any{"progname": "ba6-ncdu", "progver": "1", "timestamp": time.Now().Unix()},
		ncduEntryToExport(root, true),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
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
		return &ncduEntry{name: leaf.Name, size: leaf.ASize, disk: disk, parent: parent}, nil
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
		childInfo, err := os.Lstat(child)
		if err != nil {
			entry.failed = true
			continue
		}
		if s.options.oneFileSystem {
			if status, ok := childInfo.Sys().(*syscall.Stat_t); ok && status.Dev != s.device {
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
	sortBySize bool
	reverse    bool
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
		b.sortBySize, b.reverse = false, b.sortBySize && b.reverse
		b.sortChildren()
	case 's':
		b.reverse = b.sortBySize && !b.reverse
		b.sortBySize = true
		b.sortChildren()
	case 'a':
		b.options.apparent = !b.options.apparent
		b.sortChildren()
	}
	return false
}

// entries is the current directory's listing: the parent link first, the way
// ncdu shows "/..", followed by the sorted children.
func (b *ncduBrowser) entries() []*ncduEntry {
	if b.current.parent == nil {
		return b.current.children
	}
	return append([]*ncduEntry{{name: "..", directory: true}}, b.current.children...)
}

func (b *ncduBrowser) sortChildren() {
	children := b.current.children
	sort.SliceStable(children, func(i, j int) bool {
		less := children[i].name < children[j].name
		if b.sortBySize {
			less = children[i].weight(b.options.apparent) > children[j].weight(b.options.apparent)
		}
		if b.reverse {
			return !less
		}
		return less
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
	for _, entry := range b.current.children {
		if weight := entry.weight(b.options.apparent); weight > largest {
			largest = weight
		}
	}
	for row := 0; row < height; row++ {
		index := b.offset + row
		if index >= len(entries) {
			screen.WriteString("\r\n")
			continue
		}
		line := b.entryLine(entries[index], largest)
		if index == b.cursor {
			// The selected row is highlighted across the whole width.
			line = "\x1b[7m" + line + strings.Repeat(" ", maxInt(0, b.cols-len(line))) + "\x1b[m"
		}
		screen.WriteString(line + "\r\n")
	}
}

// entryLine draws one row: two leading spaces, the nine-column size, the bar,
// and the name, which a directory prefixes with a slash.
func (b *ncduBrowser) entryLine(entry *ncduEntry, largest uint64) string {
	if entry.name == ".." {
		return fmt.Sprintf("  %9s %s /..", "", strings.Repeat(" ", b.graphBrackets()))
	}
	marker := " "
	if entry.directory {
		marker = "/"
	}
	name := entry.name
	if entry.failed {
		name = "!" + name
	}
	line := fmt.Sprintf("  %s %s %s%s", ncduSize(entry.weight(b.options.apparent), b.options.si),
		b.graph(entry.weight(b.options.apparent), largest), marker, name)
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
	filled := 0
	if largest > 0 {
		filled = int(value * uint64(width) / largest) //nolint:gosec // G115: the quotient is bounded by width.
	}
	return "[" + strings.Repeat("#", filled) + strings.Repeat(" ", width-filled) + "]"
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
		"   n                   sort by name",
		"   s                   sort by size (again to reverse)",
		"   a                   switch between disk usage and apparent size",
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
