// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// lsOpts holds parsed ls flags.
type lsOpts struct {
	all       bool // -a: include entries starting with '.'
	almostAll bool // -A: like -a but skip . and ..
	long      bool // -l: long listing
	one       bool // -1: one entry per line
	human     bool // -h: human-readable sizes (with -l)
	reverse   bool // -r
	byTime    bool // -t
	bySize    bool // -S
	recursive bool // -R
	dirSelf   bool // -d: list directories themselves, not contents
	classify  bool // -F: append type indicator
}

// cmdLs implements a practical subset of ls(1).
func cmdLs(args []string) int {
	var opts lsOpts
	var operands []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				switch c {
				case 'a':
					opts.all = true
				case 'A':
					opts.almostAll = true
				case 'l':
					opts.long = true
				case '1':
					opts.one = true
				case 'h':
					opts.human = true
				case 'r':
					opts.reverse = true
				case 't':
					opts.byTime = true
				case 'S':
					opts.bySize = true
				case 'R':
					opts.recursive = true
				case 'd':
					opts.dirSelf = true
				case 'F':
					opts.classify = true
				default:
					fatalf("ls", "invalid option -- '%c'", c)
					return 2
				}
			}
		default:
			operands = append(operands, a)
		}
	}
rest:
	operands = append(operands, args[i:]...)
	if len(operands) == 0 {
		operands = []string{"."}
	}

	width, terminal := termWidth()
	if !terminal {
		opts.one = true
	}
	out := bufio.NewWriter(os.Stdout)
	l := &lister{opts: opts, width: width, out: out, uids: map[uint32]string{}, gids: map[uint32]string{}}

	// Split operands into non-directories (listed first, together) and
	// directories (listed afterward, each with contents).
	var files, dirs []string
	status := 0
	for _, op := range operands {
		info, err := os.Lstat(op)
		if err != nil {
			fatalf("ls", "cannot access '%s': %v", op, err)
			status = 1
			continue
		}
		isDirectory := info.IsDir()
		if info.Mode()&os.ModeSymlink != 0 && !opts.long && !opts.dirSelf {
			if targetInfo, targetErr := os.Stat(op); targetErr == nil {
				isDirectory = targetInfo.IsDir()
			}
		}
		if isDirectory && !opts.dirSelf {
			dirs = append(dirs, op)
		} else {
			files = append(files, op)
		}
	}

	if len(files) > 0 {
		entries := make([]lsEntry, 0, len(files))
		for _, f := range files {
			if info, err := os.Lstat(f); err == nil {
				entries = append(entries, lsEntry{name: f, path: f, info: info})
			}
		}
		l.sortEntries(entries)
		l.render(entries, false)
	}

	multiHeader := len(dirs) > 1 || len(files) > 0 || opts.recursive
	for idx, d := range dirs {
		if idx > 0 || len(files) > 0 {
			fmt.Fprintln(out)
		}
		if !l.listDir(d, multiHeader) {
			status = 1
		}
	}
	if err := out.Flush(); err != nil {
		fatalf("ls", "write error: %v", err)
		status = 1
	}
	return status
}

type lsEntry struct {
	name string // display name (basename for dir contents)
	path string // complete path used for metadata such as symlink targets
	info os.FileInfo
}

type lister struct {
	opts  lsOpts
	width int
	out   *bufio.Writer
	uids  map[uint32]string
	gids  map[uint32]string
}

func (l *lister) listDir(dir string, header bool) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fatalf("ls", "cannot open directory '%s': %v", dir, err)
		return false
	}

	if header {
		fmt.Fprintf(l.out, "%s:\n", dir)
	}

	items := make([]lsEntry, 0, len(entries)+2)
	if l.opts.all {
		if di, err := os.Stat(dir); err == nil {
			items = append(items, lsEntry{name: ".", path: dir, info: di})
		}
		if pi, err := os.Stat(filepath.Join(dir, "..")); err == nil {
			items = append(items, lsEntry{name: "..", path: filepath.Join(dir, ".."), info: pi})
		}
	}
	ok := true
	for _, e := range entries {
		name := e.Name()
		if name[0] == '.' && !l.opts.all && !l.opts.almostAll {
			continue
		}
		info, err := e.Info()
		if err != nil {
			fatalf("ls", "cannot access '%s': %v", filepath.Join(dir, name), err)
			ok = false
			continue
		}
		items = append(items, lsEntry{name: name, path: filepath.Join(dir, name), info: info})
	}

	l.sortEntries(items)
	l.render(items, true)

	if l.opts.recursive {
		for _, it := range items {
			if it.info.IsDir() && it.name != "." && it.name != ".." {
				fmt.Fprintln(l.out)
				if !l.listDir(filepath.Join(dir, it.name), true) {
					ok = false
				}
			}
		}
	}
	return ok
}

func (l *lister) sortEntries(e []lsEntry) {
	less := func(i, j int) bool { return e[i].name < e[j].name }
	switch {
	case l.opts.byTime:
		less = func(i, j int) bool {
			ti, tj := e[i].info.ModTime(), e[j].info.ModTime()
			if ti.Equal(tj) {
				return e[i].name < e[j].name
			}
			return ti.After(tj)
		}
	case l.opts.bySize:
		less = func(i, j int) bool {
			si, sj := e[i].info.Size(), e[j].info.Size()
			if si == sj {
				return e[i].name < e[j].name
			}
			return si > sj
		}
	}
	sort.SliceStable(e, less)
	if l.opts.reverse {
		for i, j := 0, len(e)-1; i < j; i, j = i+1, j-1 {
			e[i], e[j] = e[j], e[i]
		}
	}
}

func (l *lister) render(entries []lsEntry, showTotal bool) {
	if l.opts.long {
		l.renderLong(entries, showTotal)
		return
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.name + l.indicator(e.info)
	}
	if l.opts.one || l.width <= 0 {
		for _, n := range names {
			fmt.Fprintln(l.out, n)
		}
		return
	}
	renderColumns(l.out, names, l.width)
}

func (l *lister) renderLong(entries []lsEntry, showTotal bool) {
	type row struct{ mode, nlink, owner, group, size, mtime, name string }
	rows := make([]row, 0, len(entries))
	var wNlink, wOwner, wGroup, wSize int
	var total int64

	for _, e := range entries {
		info := e.info
		var nlink uint64 = 1
		var sizeBlocks int64
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			nlink = uint64(st.Nlink) //nolint:unconvert // Nlink is uint32 on some supported Linux architectures.
			sizeBlocks = st.Blocks
		}
		total += sizeBlocks

		r := row{
			mode:  modeString(info.Mode()),
			nlink: strconv.FormatUint(nlink, 10),
			owner: l.ownerName(info),
			group: l.groupName(info),
			size:  l.sizeString(info.Size()),
			mtime: formatLsTime(info.ModTime()),
			name:  e.name + l.indicator(info),
		}
		if t := symlinkTarget(e, info); t != "" {
			r.name += " -> " + t
		}
		rows = append(rows, r)
		wNlink = max(wNlink, len(r.nlink))
		wOwner = max(wOwner, len(r.owner))
		wGroup = max(wGroup, len(r.group))
		wSize = max(wSize, len(r.size))
	}

	// "total" line counts 1K blocks (st_blocks is in 512-byte units). Only
	// shown for directory listings, not for explicit file arguments.
	if showTotal {
		fmt.Fprintf(l.out, "total %d\n", total/2)
	}
	for _, r := range rows {
		fmt.Fprintf(l.out, "%s %*s %-*s %-*s %*s %s %s\n",
			r.mode, wNlink, r.nlink, wOwner, r.owner, wGroup, r.group,
			wSize, r.size, r.mtime, r.name)
	}
}

// symlinkTarget returns the link target for a symlink entry, else "".
func symlinkTarget(e lsEntry, info os.FileInfo) string {
	if info.Mode()&os.ModeSymlink == 0 {
		return ""
	}
	// e.name may be a basename; we cannot always resolve without the dir.
	path := e.path
	if path == "" {
		path = e.name
	}
	if t, err := os.Readlink(path); err == nil {
		return t
	}
	return ""
}

func (l *lister) indicator(info os.FileInfo) string {
	if !l.opts.classify {
		return ""
	}
	m := info.Mode()
	switch {
	case m.IsDir():
		return "/"
	case m&os.ModeSymlink != 0:
		return "@"
	case m&os.ModeNamedPipe != 0:
		return "|"
	case m&os.ModeSocket != 0:
		return "="
	case m&0o111 != 0 && m.IsRegular():
		return "*"
	}
	return ""
}

func (l *lister) sizeString(n int64) string {
	if l.opts.human {
		return humanSize(n)
	}
	return strconv.FormatInt(n, 10)
}

func (l *lister) ownerName(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "?"
	}
	if name, cached := l.uids[st.Uid]; cached {
		return name
	}
	name := strconv.FormatUint(uint64(st.Uid), 10)
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	l.uids[st.Uid] = name
	return name
}

func (l *lister) groupName(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "?"
	}
	if name, cached := l.gids[st.Gid]; cached {
		return name
	}
	name := strconv.FormatUint(uint64(st.Gid), 10)
	if g, err := user.LookupGroupId(name); err == nil {
		name = g.Name
	}
	l.gids[st.Gid] = name
	return name
}

// modeString renders a FileMode as the 10-char "drwxr-xr-x" string used by ls,
// including setuid/setgid/sticky bits.
func modeString(m os.FileMode) string {
	buf := []byte("----------")
	switch {
	case m&os.ModeDir != 0:
		buf[0] = 'd'
	case m&os.ModeSymlink != 0:
		buf[0] = 'l'
	case m&os.ModeNamedPipe != 0:
		buf[0] = 'p'
	case m&os.ModeSocket != 0:
		buf[0] = 's'
	case m&os.ModeDevice != 0:
		if m&os.ModeCharDevice != 0 {
			buf[0] = 'c'
		} else {
			buf[0] = 'b'
		}
	}

	const rwx = "rwxrwxrwx"
	for i := 0; i < 9; i++ {
		if m&(1<<uint(8-i)) != 0 {
			buf[i+1] = rwx[i]
		}
	}
	if m&os.ModeSetuid != 0 {
		buf[3] = setcase(buf[3], 's', 'S')
	}
	if m&os.ModeSetgid != 0 {
		buf[6] = setcase(buf[6], 's', 'S')
	}
	if m&os.ModeSticky != 0 {
		buf[9] = setcase(buf[9], 't', 'T')
	}
	return string(buf)
}

// setcase returns lower if the slot was executable ('x'), else upper.
func setcase(cur byte, lower, upper byte) byte {
	if cur == 'x' {
		return lower
	}
	return upper
}

// formatLsTime formats a modtime like ls: "Jan _2 15:04" for entries within
// the last six months, "Jan _2  2006" otherwise.
func formatLsTime(t time.Time) string {
	sixMonths := 182 * 24 * time.Hour
	if time.Since(t) > sixMonths || t.After(time.Now()) {
		return t.Format("Jan _2  2006")
	}
	return t.Format("Jan _2 15:04")
}

// humanSize renders a byte count with a binary unit suffix (ls -h style).
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10)
	}
	units := []string{"K", "M", "G", "T", "P", "E"}
	f := float64(n)
	i := -1
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	if f < 10 {
		return fmt.Sprintf("%.1f%s", f, units[i])
	}
	return fmt.Sprintf("%.0f%s", f, units[i])
}

// renderColumns lays names out in down-then-across columns fitting width.
func renderColumns(w io.Writer, names []string, width int) {
	if len(names) == 0 {
		return
	}
	maxLen := 0
	for _, n := range names {
		if len(n) > maxLen {
			maxLen = len(n)
		}
	}
	colWidth := maxLen + 2
	cols := width / colWidth
	if cols < 1 {
		cols = 1
	}
	rows := (len(names) + cols - 1) / cols

	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			idx := c*rows + r
			if idx >= len(names) {
				continue
			}
			if c == cols-1 || idx+rows >= len(names) {
				fmt.Fprint(w, names[idx])
			} else {
				fmt.Fprintf(w, "%-*s", colWidth, names[idx])
			}
		}
		fmt.Fprintln(w)
	}
}

// termWidth returns the terminal width of stdout, or 80 if it isn't a terminal.
func termWidth() (int, bool) {
	info, statErr := os.Stdout.Stat()
	terminal := statErr == nil && info.Mode()&os.ModeCharDevice != 0
	if !terminal {
		return 80, false
	}
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if n, err := strconv.Atoi(cols); err == nil && n > 0 {
			return n, true
		}
	}
	ws, err := unixWinsize(os.Stdout.Fd())
	if err == nil && ws > 0 {
		return ws, true
	}
	return 80, true
}
