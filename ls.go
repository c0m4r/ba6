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
	"strings"
	"syscall"
	"time"
)

// lsFormat is the layout ls prints in: one entry per line, packed into columns
// down or across, comma-separated, or the long listing.
type lsFormat int

const (
	lsSingle lsFormat = iota
	lsVertical
	lsAcross
	lsCommas
	lsLong
)

// lsSortKey is what the entries are ordered by before -r reverses them.
type lsSortKey int

const (
	lsSortName lsSortKey = iota
	lsSortTime
	lsSortSize
	lsSortExtension
	lsSortVersion
	lsSortNone
)

// lsTimeField selects which of an inode's three timestamps -l shows and -t
// sorts by.
type lsTimeField int

const (
	lsTimeModified lsTimeField = iota
	lsTimeAccess
	lsTimeStatus
)

// lsIndicator is the suffix appended to a name to show what it is.
type lsIndicator int

const (
	lsIndicatorNone     lsIndicator = iota
	lsIndicatorSlash                // -p: directories only
	lsIndicatorFileType             // --file-type: everything but executables
	lsIndicatorClassify             // -F: everything
)

// lsQuoting is how a name is written out.
type lsQuoting int

const (
	lsQuoteLiteral lsQuoting = iota
	lsQuoteShell
	lsQuoteShellAlways
	lsQuoteShellEscape
	lsQuoteShellEscapeAlways
	lsQuoteC
	lsQuoteEscape
)

// lsOpts holds one ls(1) command line.
type lsOpts struct {
	all       bool // -a
	almostAll bool // -A
	format    lsFormat
	formatSet bool
	reverse   bool // -r
	sortKey   lsSortKey
	sortSet   bool
	timeField lsTimeField
	recursive bool // -R
	dirSelf   bool // -d
	human     bool // -h
	si        bool // --si
	blockSize uint64
	blockUnit string
	inode     bool // -i
	blocks    bool // -s
	numericID bool // -n
	context   bool // -Z
	showOwner bool
	showGroup bool
	indicator lsIndicator
	quoting   lsQuoting
	width     int
	tabsize   int
	timeStyle string
	fullTime  bool
	ignore    []string
	hide      []string
	noBackups bool // -B
	dirsFirst bool // --group-directories-first
	deref     derefMode
	derefSet  bool
	terminal  bool
}

func lsUsage(format string, a ...interface{}) {
	fatalf("ls", format, a...)
	fmt.Fprintln(os.Stderr, "Try 'ls --help' for more information.")
}

// lsWordValue resolves one of the WORD arguments ls takes, accepting any
// unambiguous abbreviation as the original does.
func lsWordValue(option, value string, names []string) (int, bool) {
	match, count := -1, 0
	for i, name := range names {
		if name == value {
			return i, true
		}
		if strings.HasPrefix(name, value) && value != "" {
			match, count = i, count+1
		}
	}
	if count == 1 {
		return match, true
	}
	problem := "invalid"
	if count > 1 {
		problem = "ambiguous"
	}
	fatalf("ls", "%s argument %s for %s", problem, quoteLocaleName(value), quoteLocaleName(option))
	fmt.Fprintln(os.Stderr, "Valid arguments are:")
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  - %s\n", quoteLocaleName(name))
	}
	fmt.Fprintln(os.Stderr, "Try 'ls --help' for more information.")
	return -1, false
}

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func parseLsOptions(args []string) (lsOpts, []string, bool) {
	width, terminal := termWidth()
	opts := lsOpts{
		showOwner: true, showGroup: true, blockSize: 1024,
		width: width, tabsize: 8, terminal: terminal,
	}
	// In a terminal the entries are packed into columns and awkward names are
	// quoted; through a pipe they are one per line and written literally.
	opts.format = lsSingle
	if terminal {
		opts.format, opts.quoting = lsVertical, lsQuoteShellEscape
	}
	if value, set := os.LookupEnv("QUOTING_STYLE"); set {
		if style, ok := lsWordValue("--quoting-style", value,
			[]string{"literal", "shell", "shell-always", "shell-escape", "shell-escape-always", "c", "escape"}); ok {
			opts.quoting = lsQuotingStyles[style]
		}
	}
	if value, set := os.LookupEnv("TIME_STYLE"); set {
		opts.timeStyle = value
	}
	var operands []string
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !parsing || arg == "-" || !strings.HasPrefix(arg, "-") {
			operands = append(operands, arg)
			continue
		}
		if arg == "--" {
			parsing = false
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := arg, "", false
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
			needValue := func() (string, bool) {
				if hasValue {
					return value, true
				}
				i++
				if i >= len(args) {
					lsUsage("option '%s' requires an argument", name)
					return "", false
				}
				return args[i], true
			}
			switch name {
			case "--all":
				opts.all = true
			case "--almost-all":
				opts.almostAll = true
			case "--long":
				opts.format, opts.formatSet = lsLong, true
			case "--recursive":
				opts.recursive = true
			case "--directory":
				opts.dirSelf = true
			case "--reverse":
				opts.reverse = true
			case "--human-readable":
				opts.human = true
			case "--si":
				opts.si, opts.human = true, true
			case "--inode":
				opts.inode = true
			case "--size":
				opts.blocks = true
			case "--numeric-uid-gid":
				opts.numericID, opts.format, opts.formatSet = true, lsLong, true
			case "--no-group":
				opts.showGroup = false
			case "--classify":
				opts.indicator = lsIndicatorClassify
			case "--file-type":
				opts.indicator = lsIndicatorFileType
			case "--dereference":
				opts.deref, opts.derefSet = derefAlways, true
			case "--dereference-command-line":
				opts.deref, opts.derefSet = derefCommandLine, true
			case "--dereference-command-line-symlink-to-dir":
				opts.deref, opts.derefSet = derefNever, false
			case "--full-time":
				opts.format, opts.formatSet, opts.fullTime = lsLong, true, true
			case "--group-directories-first":
				opts.dirsFirst = true
			case "--ignore-backups":
				opts.noBackups = true
			case "--literal":
				opts.quoting = lsQuoteLiteral
			case "--quote-name":
				opts.quoting = lsQuoteC
			case "--color":
				// Colour is not written; the option is accepted so that the
				// aliases every distribution ships keep working.
				if !hasValue {
					continue
				}
				if _, ok := lsWordValue("--color", value, []string{"always", "yes", "force", "never", "no", "none", "auto", "tty", "if-tty"}); !ok {
					return opts, nil, false
				}
			case "--context":
				opts.context = true
			case "--author", "--no-preserve-root", "--dired":
				// Accepted and ignored: there is no author field to print.
			case "--ignore":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				opts.ignore = append(opts.ignore, text)
			case "--hide":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				opts.hide = append(opts.hide, text)
			case "--width":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				n, err := strconv.Atoi(text)
				if err != nil || n < 0 {
					fatalf("ls", "invalid line width: %s", quoteLocaleName(text))
					return opts, nil, false
				}
				opts.width = n
			case "--tabsize":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				n, err := strconv.Atoi(text)
				if err != nil || n < 0 {
					fatalf("ls", "invalid tab size: %s", quoteLocaleName(text))
					return opts, nil, false
				}
				opts.tabsize = n
			case "--block-size":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				if !opts.setBlockSize(text) {
					return opts, nil, false
				}
			case "--time-style":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				opts.timeStyle = text
			case "--time":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				field, ok := lsWordValue("--time", text,
					[]string{"atime", "access", "use", "ctime", "status", "birth", "creation", "mtime", "modification"})
				if !ok {
					return opts, nil, false
				}
				opts.timeField = []lsTimeField{
					lsTimeAccess, lsTimeAccess, lsTimeAccess,
					lsTimeStatus, lsTimeStatus, lsTimeModified,
					lsTimeModified, lsTimeModified, lsTimeModified,
				}[field]
			case "--sort":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				key, ok := lsWordValue("--sort", text, []string{"none", "size", "time", "version", "extension", "width", "name"})
				if !ok {
					return opts, nil, false
				}
				opts.sortKey, opts.sortSet = []lsSortKey{
					lsSortNone, lsSortSize, lsSortTime, lsSortVersion,
					lsSortExtension, lsSortName, lsSortName,
				}[key], true
			case "--format":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				chosen, ok := lsWordValue("--format", text,
					[]string{"across", "commas", "horizontal", "long", "single-column", "verbose", "vertical"})
				if !ok {
					return opts, nil, false
				}
				opts.format, opts.formatSet = []lsFormat{
					lsAcross, lsCommas, lsAcross, lsLong, lsSingle, lsLong, lsVertical,
				}[chosen], true
			case "--indicator-style":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				style, ok := lsWordValue("--indicator-style", text, []string{"none", "slash", "file-type", "classify"})
				if !ok {
					return opts, nil, false
				}
				opts.indicator = lsIndicator(style)
			case "--quoting-style":
				text, ok := needValue()
				if !ok {
					return opts, nil, false
				}
				style, ok := lsWordValue("--quoting-style", text,
					[]string{"literal", "shell", "shell-always", "shell-escape", "shell-escape-always", "c", "escape"})
				if !ok {
					return opts, nil, false
				}
				opts.quoting = lsQuotingStyles[style]
			default:
				lsUsage("unrecognized option '%s'", arg)
				return opts, nil, false
			}
			continue
		}
		cluster := arg[1:]
		for len(cluster) > 0 {
			flag := cluster[0]
			cluster = cluster[1:]
			switch flag {
			case 'a':
				opts.all = true
			case 'A':
				opts.almostAll = true
			case 'l':
				opts.format, opts.formatSet = lsLong, true
			case '1':
				if opts.format != lsLong {
					opts.format, opts.formatSet = lsSingle, true
				}
			case 'C':
				opts.format, opts.formatSet = lsVertical, true
			case 'x':
				opts.format, opts.formatSet = lsAcross, true
			case 'm':
				opts.format, opts.formatSet = lsCommas, true
			case 'o':
				opts.format, opts.formatSet, opts.showGroup = lsLong, true, false
			case 'g':
				opts.format, opts.formatSet, opts.showOwner = lsLong, true, false
			case 'G':
				opts.showGroup = false
			case 'n':
				opts.format, opts.formatSet, opts.numericID = lsLong, true, true
			case 'h':
				opts.human = true
			case 'r':
				opts.reverse = true
			case 't':
				opts.sortKey, opts.sortSet = lsSortTime, true
			case 'S':
				opts.sortKey, opts.sortSet = lsSortSize, true
			case 'X':
				opts.sortKey, opts.sortSet = lsSortExtension, true
			case 'v':
				opts.sortKey, opts.sortSet = lsSortVersion, true
			case 'U':
				opts.sortKey, opts.sortSet = lsSortNone, true
			case 'f':
				// -f is the historic "no sorting, show everything".
				opts.sortKey, opts.sortSet, opts.all = lsSortNone, true, true
			case 'R':
				opts.recursive = true
			case 'd':
				opts.dirSelf = true
			case 'F':
				opts.indicator = lsIndicatorClassify
			case 'p':
				opts.indicator = lsIndicatorSlash
			case 'i':
				opts.inode = true
			case 's':
				opts.blocks = true
			case 'k':
				opts.blockSize, opts.blockUnit = 1024, ""
			case 'c':
				opts.timeField = lsTimeStatus
			case 'u':
				opts.timeField = lsTimeAccess
			case 'Q':
				opts.quoting = lsQuoteC
			case 'b':
				opts.quoting = lsQuoteEscape
			case 'N':
				opts.quoting = lsQuoteLiteral
			case 'L':
				opts.deref, opts.derefSet = derefAlways, true
			case 'H':
				opts.deref, opts.derefSet = derefCommandLine, true
			case 'Z':
				// There is no SELinux policy to read a label from, so the
				// column is present and reads "?", as it does on a system
				// without one.
				opts.context = true
			case 'B':
				opts.noBackups = true
			case 'w', 'I', 'T':
				value := cluster
				cluster = ""
				if value == "" {
					i++
					if i >= len(args) {
						lsUsage("option requires an argument -- '%c'", flag)
						return opts, nil, false
					}
					value = args[i]
				}
				switch flag {
				case 'I':
					opts.ignore = append(opts.ignore, value)
				case 'w', 'T':
					n, err := strconv.Atoi(value)
					what := "line width"
					if flag == 'T' {
						what = "tab size"
					}
					if err != nil || n < 0 {
						fatalf("ls", "invalid %s: %s", what, quoteLocaleName(value))
						return opts, nil, false
					}
					if flag == 'w' {
						opts.width = n
					} else {
						opts.tabsize = n
					}
				}
			default:
				lsUsage("invalid option -- '%c'", flag)
				return opts, nil, false
			}
		}
	}
	// -c and -u pick a timestamp; outside the long listing, and with no other
	// sort asked for, they also sort by it.
	if opts.timeField != lsTimeModified && opts.format != lsLong && opts.sortKey == lsSortName && !opts.sortSet {
		opts.sortKey = lsSortTime
	}
	return opts, operands, true
}

// lsQuotingStyles maps --quoting-style's words onto the styles this
// implementation writes; the two "always" spellings quote even a plain name.
var lsQuotingStyles = []lsQuoting{
	lsQuoteLiteral, lsQuoteShell, lsQuoteShellAlways,
	lsQuoteShellEscape, lsQuoteShellEscapeAlways, lsQuoteC, lsQuoteEscape,
}

// setBlockSize applies --block-size, where a bare unit makes each printed
// value carry that unit.
func (o *lsOpts) setBlockSize(value string) bool {
	size, err := parseByteSize(value)
	if err != nil || size == 0 {
		fatalf("ls", "invalid --block-size argument %s", quoteLocaleName(value))
		return false
	}
	o.blockSize, o.blockUnit, o.human = size, "", false
	if len(value) > 0 && !isDigitByte(value[0]) {
		o.blockUnit = value
		if value == "KB" {
			o.blockUnit = "kB"
		}
	}
	return true
}

// lsEntry is one listed file: the name as it should be printed, the path its
// metadata came from, and that metadata.
type lsEntry struct {
	name string
	path string
	info os.FileInfo
	// target is a symbolic link's body, read once so the long listing and the
	// -F indicator agree about it.
	target string
	// unknown marks an entry -L could not reach, which the long listing shows
	// as a row of question marks.
	unknown bool
}

type lister struct {
	opts   lsOpts
	out    *bufio.Writer
	uids   map[uint32]string
	gids   map[uint32]string
	status int
}

// cmdLs implements ls(1).
func cmdLs(args []string) int {
	opts, operands, ok := parseLsOptions(args)
	if !ok {
		return 2
	}
	if len(operands) == 0 {
		operands = []string{"."}
	}
	out := bufio.NewWriter(os.Stdout)
	l := &lister{opts: opts, out: out, uids: map[uint32]string{}, gids: map[uint32]string{}}

	// The operands are split: everything that is not a directory is listed
	// first, together, and each directory afterwards with its contents.
	var files, dirs []lsEntry
	for _, operand := range operands {
		info, err := os.Lstat(operand)
		if err != nil {
			fatalf("ls", "cannot access '%s': %s", operand, errText(err))
			l.status = 2
			continue
		}
		entry := lsEntry{name: operand, path: operand, info: info}
		if info.Mode()&os.ModeSymlink != 0 {
			entry.target, _ = os.Readlink(operand)
			// A symbolic link named on the command line is followed unless the
			// listing is about the links themselves.
			follow := opts.deref == derefAlways || opts.deref == derefCommandLine
			if !opts.derefSet {
				follow = opts.format != lsLong && !opts.dirSelf
			}
			if follow {
				followed, followErr := os.Stat(operand)
				if followErr != nil {
					fatalf("ls", "cannot access '%s': %s", operand, errText(followErr))
					l.status = 2
					continue
				}
				entry.info = followed
				if opts.deref == derefAlways {
					// -L reports the file itself, not the path to it.
					entry.target = ""
				}
			}
		}
		if entry.info.IsDir() && !opts.dirSelf {
			dirs = append(dirs, entry)
		} else {
			files = append(files, entry)
		}
	}

	if len(files) > 0 {
		l.sortEntries(files)
		l.render(files, false)
	}
	l.sortEntries(dirs)
	header := len(dirs) > 1 || len(files) > 0 || opts.recursive
	for index, dir := range dirs {
		if index > 0 || len(files) > 0 {
			fmt.Fprintln(out)
		}
		l.listDir(dir.path, header)
	}
	if err := out.Flush(); err != nil {
		fatalf("ls", "write error: %s", errText(err))
		return 2
	}
	return l.status
}

// listDir prints one directory, and then, under -R, the directories inside it.
func (l *lister) listDir(dir string, header bool) {
	names, err := readDirRaw(dir)
	if err != nil {
		fatalf("ls", "cannot open directory '%s': %s", dir, errText(err))
		// A directory that cannot be read is a minor failure when it was found
		// during a walk and a serious one when it was named.
		l.status = 2
		return
	}
	if header {
		fmt.Fprintf(l.out, "%s:\n", dir)
	}
	entries := make([]lsEntry, 0, len(names)+2)
	if l.opts.all {
		for _, name := range []string{".", ".."} {
			path := lsAttach(dir, name)
			if info, statErr := os.Lstat(path); statErr == nil {
				entries = append(entries, lsEntry{name: name, path: path, info: info})
			}
		}
	}
	for _, name := range names {
		if !l.keeps(name) {
			continue
		}
		path := lsAttach(dir, name)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			fatalf("ls", "cannot access '%s': %s", path, errText(statErr))
			l.status = 2
			continue
		}
		entry := lsEntry{name: name, path: path, info: info}
		if info.Mode()&os.ModeSymlink != 0 {
			if l.opts.deref == derefAlways && l.needsStat() {
				// -L reports the file the link names; one that cannot be
				// reached is listed with a row of question marks and counts
				// as a minor failure.
				followed, followErr := os.Stat(path)
				if followErr != nil {
					fatalf("ls", "cannot access '%s': %s", path, errText(followErr))
					entry.unknown = true
					if l.status == 0 {
						l.status = 1
					}
				} else {
					entry.info = followed
				}
			} else {
				entry.target, _ = os.Readlink(path)
			}
		}
		entries = append(entries, entry)
	}
	l.sortEntries(entries)
	l.render(entries, true)

	if l.opts.recursive {
		for _, entry := range entries {
			if !entry.info.IsDir() || entry.name == "." || entry.name == ".." {
				continue
			}
			fmt.Fprintln(l.out)
			l.listDir(lsJoin(dir, entry.name), true)
		}
	}
}

// lsWidth is how many terminal columns a name takes. This is the C locale, so
// the printable ASCII characters take one column each and every other byte —
// a control character, or a byte of a UTF-8 sequence — takes none, which is
// what the original's own width count does under the same locale.
func lsWidth(text string) int {
	width := 0
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x20 && text[i] < 0x7f {
			width++
		}
	}
	return width
}

// lsJoin builds a child's path without cleaning it, so a listing of "." names
// its subdirectories "./sub" the way the original's own headers do.
func lsJoin(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

// lsAttach is the original's own path building for the file it opens and for
// the names in its diagnostics: a directory of exactly "." contributes nothing,
// so an entry of the current directory is reported by its bare name.
func lsAttach(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return lsJoin(dir, name)
}

// needsStat reports whether the chosen layout has to look at an inode at all.
// The original only follows a symbolic link, and only complains about one it
// cannot follow, when something it is about to print or sort by depends on it.
func (l *lister) needsStat() bool {
	return l.opts.format == lsLong || l.opts.sortKey == lsSortTime || l.opts.sortKey == lsSortSize ||
		l.opts.blocks || l.opts.context || l.opts.inode || l.opts.recursive ||
		l.opts.indicator != lsIndicatorNone || l.opts.dirsFirst
}

// keeps applies the name filters: the dot rule, -B's editor backups, and the
// -I and --hide patterns.
func (l *lister) keeps(name string) bool {
	if strings.HasPrefix(name, ".") && !l.opts.all && !l.opts.almostAll {
		return false
	}
	if l.opts.noBackups && strings.HasSuffix(name, "~") {
		return false
	}
	for _, pattern := range l.opts.ignore {
		if matched, err := filepath.Match(pattern, name); err == nil && matched {
			return false
		}
	}
	// --hide gives way to -a and -A, which ask for everything.
	if !l.opts.all && !l.opts.almostAll {
		for _, pattern := range l.opts.hide {
			if matched, err := filepath.Match(pattern, name); err == nil && matched {
				return false
			}
		}
	}
	return true
}

// entryTime is the timestamp -l shows and -t sorts by.
func (l *lister) entryTime(entry lsEntry) time.Time {
	status, ok := entry.info.Sys().(*syscall.Stat_t)
	if !ok {
		return entry.info.ModTime()
	}
	switch l.opts.timeField {
	case lsTimeAccess:
		return time.Unix(status.Atim.Sec, status.Atim.Nsec)
	case lsTimeStatus:
		return time.Unix(status.Ctim.Sec, status.Ctim.Nsec)
	default:
		return time.Unix(status.Mtim.Sec, status.Mtim.Nsec)
	}
}

func (l *lister) sortEntries(entries []lsEntry) {
	if l.opts.sortKey == lsSortNone {
		// -U and -f leave the entries in the order the directory gave them,
		// and -r still reverses that.
		if l.opts.reverse {
			lsReverse(entries)
		}
		return
	}
	less := func(i, j int) bool { return entries[i].name < entries[j].name }
	switch l.opts.sortKey {
	case lsSortTime:
		less = func(i, j int) bool {
			a, b := l.entryTime(entries[i]), l.entryTime(entries[j])
			if a.Equal(b) {
				return entries[i].name < entries[j].name
			}
			return a.After(b)
		}
	case lsSortSize:
		less = func(i, j int) bool {
			a, b := entries[i].info.Size(), entries[j].info.Size()
			if a == b {
				return entries[i].name < entries[j].name
			}
			return a > b
		}
	case lsSortExtension:
		less = func(i, j int) bool {
			a, b := lsExtension(entries[i].name), lsExtension(entries[j].name)
			if a == b {
				return entries[i].name < entries[j].name
			}
			return a < b
		}
	case lsSortVersion:
		less = func(i, j int) bool { return lsVersionLess(entries[i].name, entries[j].name) }
	}
	sort.SliceStable(entries, less)
	if l.opts.reverse {
		lsReverse(entries)
	}
	if l.opts.dirsFirst {
		// The directories keep their order among themselves, and so do the
		// rest; only the two groups move apart.
		sort.SliceStable(entries, func(i, j int) bool {
			return lsLeadsDirectory(entries[i]) && !lsLeadsDirectory(entries[j])
		})
	}
}

// lsLeadsDirectory reports whether an entry belongs in the group
// --group-directories-first puts first, which includes a symbolic link whose
// target is a directory.
func lsLeadsDirectory(entry lsEntry) bool {
	if entry.info.IsDir() {
		return true
	}
	if entry.info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Stat(entry.path)
	return err == nil && target.IsDir()
}

func lsReverse(entries []lsEntry) {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
}

// lsExtension is the suffix -X orders by: everything from the last dot, and
// nothing at all when the name has no dot after its first character.
func lsExtension(name string) string {
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		return name[dot:]
	}
	return ""
}

// The -v ordering is the originals' filevercmp: file suffixes are set aside,
// runs of digits are compared as numbers, and "~" sorts before everything so
// that a pre-release name comes before the release it belongs to.

// lsVerOrder ranks one byte for the version comparison: digits are equal to
// each other, letters keep their own order, "~" sorts before everything, and
// every other byte sorts after the letters.
func lsVerOrder(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return 0
	case isASCIILetter(c):
		return int(c)
	case c == '~':
		return -1
	}
	return int(c) + 256
}

// lsVerRevCmp is dpkg's version comparison, which filevercmp is built on: the
// non-digit runs are compared byte by byte in lsVerOrder, and the digit runs
// as numbers with their leading zeroes dropped.
func lsVerRevCmp(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		first := 0
		for (i < len(a) && !isDigitByte(a[i])) || (j < len(b) && !isDigitByte(b[j])) {
			left, right := 0, 0
			if i < len(a) {
				left = lsVerOrder(a[i])
			}
			if j < len(b) {
				right = lsVerOrder(b[j])
			}
			if left != right {
				return left - right
			}
			i++
			j++
		}
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		for i < len(a) && isDigitByte(a[i]) && j < len(b) && isDigitByte(b[j]) {
			if first == 0 {
				first = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		// The longer run of digits is the larger number.
		if i < len(a) && isDigitByte(a[i]) {
			return 1
		}
		if j < len(b) && isDigitByte(b[j]) {
			return -1
		}
		if first != 0 {
			return first
		}
	}
	return 0
}

// lsFilePrefixLen is how much of a name comes before the longest suffix
// matching (\.[A-Za-z~][A-Za-z0-9~]*)*$ — the ".tar.gz" of the world, which
// the version comparison sets aside on its first pass.
func lsFilePrefixLen(name string) int {
	i, prefix := 0, 0
	for {
		prefix = i
		for i+1 < len(name) && name[i] == '.' && (isASCIILetter(name[i+1]) || name[i+1] == '~') {
			i += 2
			for i < len(name) && (isASCIILetter(name[i]) || isDigitByte(name[i]) || name[i] == '~') {
				i++
			}
		}
		if i >= len(name) {
			return prefix
		}
		i++
	}
}

// lsVersionLess is the comparison -v sorts by: filevercmp, and plain byte order
// to break the ties it leaves — "a" and "a0" compare equal to it.
func lsVersionLess(a, b string) bool {
	if result := lsFileVerCmp(a, b); result != 0 {
		return result < 0
	}
	return a < b
}

// lsFileVerCmp orders two names the way -v and --sort=version do.
func lsFileVerCmp(a, b string) int {
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	case b == "":
		return 1
	}
	// "." comes first, then "..", then the other hidden names, then the rest.
	if a[0] == '.' || b[0] == '.' {
		switch {
		case a[0] != '.':
			return 1
		case b[0] != '.':
			return -1
		case a == ".":
			if b == "." {
				return 0
			}
			return -1
		case b == ".":
			return 1
		case a == "..":
			if b == ".." {
				return 0
			}
			return -1
		case b == "..":
			return 1
		}
	}
	aPrefix, bPrefix := lsFilePrefixLen(a), lsFilePrefixLen(b)
	// With no suffix on either side there is nothing a second pass could add.
	onePass := aPrefix == len(a) && bPrefix == len(b)
	if result := lsVerRevCmp(a[:aPrefix], b[:bPrefix]); result != 0 || onePass {
		return result
	}
	return lsVerRevCmp(a, b)
}

// render prints one batch of entries in whichever layout was chosen.
func (l *lister) render(entries []lsEntry, showTotal bool) {
	if l.opts.format == lsLong {
		l.renderLong(entries, showTotal)
		return
	}
	if showTotal && (l.opts.blocks || l.opts.format == lsLong) {
		fmt.Fprintf(l.out, "total %s\n", l.blockCount(entries))
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = l.frills(entry) + l.displayName(entry) + l.indicator(entry)
	}
	switch {
	case l.opts.format == lsSingle:
		for _, name := range names {
			fmt.Fprintln(l.out, name)
		}
	case l.opts.format == lsCommas:
		l.renderSeparated(names, ',')
	case l.opts.width == 0:
		// A width of zero asks for no wrapping at all, which the original
		// answers with one line of two-space-separated names.
		l.renderSeparated(names, ' ')
	case l.opts.format == lsAcross:
		l.renderColumns(names, false)
	default:
		l.renderColumns(names, true)
	}
}

// frills is what -i and -s put in front of a name outside the long listing.
func (l *lister) frills(entry lsEntry) string {
	text := ""
	if l.opts.inode {
		text += l.inodeString(entry) + " "
	}
	if l.opts.blocks {
		text += l.blockString(entry) + " "
	}
	if l.opts.context {
		text += "? "
	}
	return text
}

func (l *lister) inodeString(entry lsEntry) string {
	if status, ok := entry.info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(status.Ino, 10)
	}
	return "?"
}

// blockString is the allocated size in the chosen block unit, rounded up the
// way the original rounds it.
func (l *lister) blockString(entry lsEntry) string {
	status, ok := entry.info.Sys().(*syscall.Stat_t)
	if !ok {
		return "0"
	}
	return l.scale(uint64(status.Blocks) * 512) //nolint:gosec // G115: a block count from the kernel is nonnegative.
}

func (l *lister) blockCount(entries []lsEntry) string {
	var total uint64
	for _, entry := range entries {
		if status, ok := entry.info.Sys().(*syscall.Stat_t); ok {
			total += uint64(status.Blocks) * 512 //nolint:gosec // G115: same.
		}
	}
	return l.scale(total)
}

// scale renders a byte count in the unit -h, --si or --block-size asked for.
func (l *lister) scale(value uint64) string {
	if l.opts.human {
		base := uint64(1024)
		if l.opts.si {
			base = 1000
		}
		return humanSizeBase(value, base)
	}
	return strconv.FormatUint((value+l.opts.blockSize-1)/l.opts.blockSize, 10) + l.opts.blockUnit
}

// sizeString is the size column: a byte count, scaled only when -h or
// --block-size asked for it.
func (l *lister) sizeString(entry lsEntry) string {
	size := entry.info.Size()
	if l.opts.human {
		base := uint64(1024)
		if l.opts.si {
			base = 1000
		}
		return humanSizeBase(uint64(size), base) //nolint:gosec // G115: a file size is nonnegative.
	}
	if l.opts.blockSize != 1024 || l.opts.blockUnit != "" {
		return strconv.FormatUint((uint64(size)+l.opts.blockSize-1)/l.opts.blockSize, 10) + l.opts.blockUnit //nolint:gosec // G115: same.
	}
	return strconv.FormatInt(size, 10)
}

// renderSeparated prints the names on one line each time they still fit,
// breaking the line before a name that would not. This is the original's own
// loop, down to its strict comparison: a name is kept on the line only while
// the position, its width and the two-character separator stay *under* the
// line width.
func (l *lister) renderSeparated(names []string, separator byte) {
	position := 0
	for index, name := range names {
		width := lsWidth(name)
		if index != 0 {
			l.out.WriteByte(separator) //nolint:errcheck // buffered writer; the error surfaces at Flush.
			if l.opts.width == 0 || position+width+2 < l.opts.width {
				l.out.WriteByte(' ') //nolint:errcheck // same.
				position += 2
			} else {
				l.out.WriteByte('\n') //nolint:errcheck // same.
				position = 0
			}
		}
		io.WriteString(l.out, name) //nolint:errcheck // same.
		position += width
	}
	if len(names) > 0 {
		fmt.Fprintln(l.out)
	}
}

// renderColumns packs the names into columns, down first for -C and across for
// -x, using the original's own search for the widest layout that fits.
func (l *lister) renderColumns(names []string, byColumns bool) {
	if len(names) == 0 {
		return
	}
	columns, widths := l.columnLayout(names, byColumns)
	rows := (len(names) + columns - 1) / columns
	for row := 0; row < rows; row++ {
		position := 0
		for column := 0; column < columns; column++ {
			index := row*columns + column
			if byColumns {
				index = column*rows + row
			}
			if index >= len(names) {
				continue
			}
			io.WriteString(l.out, names[index]) //nolint:errcheck // buffered writer; the error surfaces at Flush.
			// The last name on a line is written without padding: down the
			// columns that is the one with no entry below-right of it, and
			// across them it is the end of the row or of the list.
			last := column == columns-1 || index+1 >= len(names)
			if byColumns {
				last = index+rows >= len(names)
			}
			if last {
				break
			}
			l.indent(position+lsWidth(names[index]), position+widths[column])
			position += widths[column]
		}
		fmt.Fprintln(l.out)
	}
}

// columnLayout is the original's search: try every column count from the most
// the width could hold down to one, and take the first whose columns fit. A
// layout counts as fitting while its total stays strictly under the line width,
// which is what leaves the last column clear of the right edge. The one case
// where this still parts company with the original is a directory of
// one-character names at a width of four or so, where it settles for a column
// fewer.
func (l *lister) columnLayout(names []string, byColumns bool) (int, []int) {
	const minColumnWidth = 3
	maxColumns := l.opts.width / minColumnWidth
	if l.opts.width == 0 {
		maxColumns = len(names)
	}
	if maxColumns < 1 {
		maxColumns = 1
	}
	if maxColumns > len(names) {
		maxColumns = len(names)
	}
	valid := make([]bool, maxColumns)
	lengths := make([]int, maxColumns)
	widths := make([][]int, maxColumns)
	for i := range widths {
		widths[i] = make([]int, i+1)
		for j := range widths[i] {
			widths[i][j] = minColumnWidth
		}
		lengths[i] = (i + 1) * minColumnWidth
		valid[i] = true
	}
	for index, name := range names {
		for i := 0; i < maxColumns; i++ {
			if !valid[i] {
				continue
			}
			column := index % (i + 1)
			if byColumns {
				column = index / ((len(names) + i) / (i + 1))
			}
			// Every column but the last carries a two-space gutter.
			needed := lsWidth(name)
			if column != i {
				needed += 2
			}
			if widths[i][column] < needed {
				lengths[i] += needed - widths[i][column]
				widths[i][column] = needed
				valid[i] = lengths[i] < l.opts.width
			}
		}
	}
	for columns := maxColumns; columns > 1; columns-- {
		if valid[columns-1] {
			return columns, widths[columns-1]
		}
	}
	return 1, widths[0]
}

// indent advances from one column position to another, using tab stops where
// they land inside the gap, exactly as the original's own indent does.
func (l *lister) indent(from, to int) {
	for from < to {
		if l.opts.tabsize != 0 && to/l.opts.tabsize > (from+1)/l.opts.tabsize {
			l.out.WriteByte('\t') //nolint:errcheck // buffered writer; the error surfaces at Flush.
			from += l.opts.tabsize - from%l.opts.tabsize
			continue
		}
		l.out.WriteByte(' ') //nolint:errcheck // same.
		from++
	}
}

// renderLong prints the long listing, sizing every column from the widest
// value in it.
func (l *lister) renderLong(entries []lsEntry, showTotal bool) {
	type row struct {
		inode, blocks, mode, nlink, owner, group, size, stamp, name string
		device                                                      bool
		major, minor                                                string
	}
	rows := make([]row, 0, len(entries))
	var wInode, wBlocks, wNlink, wOwner, wGroup, wSize, wMajor, wMinor, wStamp int
	// The original widens every mode string by one when any file in the batch
	// carries an access-control marker, so the names still line up.
	marked := false
	markers := make([]byte, len(entries))
	for i, entry := range entries {
		markers[i] = lsAccessMarker(entry.path)
		if markers[i] != ' ' {
			marked = true
		}
	}
	for index, entry := range entries {
		info := entry.info
		r := row{
			mode:  modeString(info.Mode()) + lsMarkerSuffix(marked, markers[index]),
			nlink: "1",
			owner: l.ownerName(info),
			group: l.groupName(info),
			size:  l.sizeString(entry),
			stamp: l.timeString(l.entryTime(entry)),
			name:  l.displayName(entry) + l.indicator(entry),
		}
		if entry.unknown {
			// Nothing but the name and the link's own type is known, so every
			// field the inode would have filled reads as a question mark.
			r = row{
				mode:  string(modeString(info.Mode())[0]) + "?????????",
				nlink: "?", owner: "?", group: "?", size: "?", stamp: "?",
				name: r.name,
			}
			rows = append(rows, r)
			wNlink = max(wNlink, 1)
			wOwner, wGroup, wSize = max(wOwner, 1), max(wGroup, 1), max(wSize, 1)
			continue
		}
		if l.opts.inode {
			r.inode = l.inodeString(entry)
		}
		if l.opts.blocks {
			r.blocks = l.blockString(entry)
		}
		if status, ok := info.Sys().(*syscall.Stat_t); ok {
			r.nlink = strconv.FormatUint(uint64(status.Nlink), 10) //nolint:unconvert // Nlink is uint32 on some supported Linux architectures.
			if info.Mode()&(os.ModeDevice|os.ModeCharDevice) != 0 {
				// A device has no size; it shows the numbers it is addressed by.
				r.device = true
				r.major = strconv.FormatUint(unix_major(status.Rdev), 10)
				r.minor = strconv.FormatUint(unix_minor(status.Rdev), 10)
				wMajor = max(wMajor, len(r.major))
				wMinor = max(wMinor, len(r.minor))
			}
		}
		if entry.target != "" {
			// In the long listing the classification character describes what
			// the link points at, not the link itself.
			r.name = l.displayName(entry) + " -> " + l.quote(entry.target) + l.targetIndicator(entry)
		}
		rows = append(rows, r)
		wInode = max(wInode, len(r.inode))
		wBlocks = max(wBlocks, len(r.blocks))
		wNlink = max(wNlink, len(r.nlink))
		wOwner = max(wOwner, len(r.owner))
		wGroup = max(wGroup, len(r.group))
		if !r.device {
			wSize = max(wSize, len(r.size))
		}
		wStamp = max(wStamp, len(r.stamp))
	}
	// A device's two numbers share the size column, widening it when they need
	// more room than any plain size does.
	if wMajor > 0 {
		wSize = max(wSize, wMajor+2+wMinor)
	}
	if showTotal {
		fmt.Fprintf(l.out, "total %s\n", l.blockCount(entries))
	}
	for _, r := range rows {
		if l.opts.inode {
			fmt.Fprintf(l.out, "%*s ", wInode, r.inode)
		}
		if l.opts.blocks {
			fmt.Fprintf(l.out, "%*s ", wBlocks, r.blocks)
		}
		fmt.Fprintf(l.out, "%s %*s", r.mode, wNlink, r.nlink)
		if l.opts.showOwner {
			fmt.Fprintf(l.out, " %-*s", wOwner, r.owner)
		}
		if l.opts.showGroup {
			fmt.Fprintf(l.out, " %-*s", wGroup, r.group)
		}
		if l.opts.context {
			fmt.Fprint(l.out, " ?")
		}
		if r.device {
			fmt.Fprintf(l.out, " %*s, %*s", wMajor+max(0, wSize-(wMajor+2+wMinor)), r.major, wMinor, r.minor)
		} else {
			fmt.Fprintf(l.out, " %*s", wSize, r.size)
		}
		fmt.Fprintf(l.out, " %*s %s\n", wStamp, r.stamp, r.name)
	}
}

// lsMarkerSuffix is the eleventh character of a mode string: nothing at all
// unless some file in the listing has an access-control marker of its own.
func lsMarkerSuffix(marked bool, marker byte) string {
	if !marked {
		return ""
	}
	return string(marker)
}

// lsAccessMarker reports the character the original puts after a mode string:
// "+" for a POSIX ACL, "." for a security context alone, and a space for the
// ordinary case.
func lsAccessMarker(path string) byte {
	if path == "" {
		return ' '
	}
	// The originals ask the same question through getxattr, which follows a
	// symbolic link to the file that actually carries the attribute.
	if _, err := syscall.Getxattr(path, "system.posix_acl_access", nil); err == nil {
		return '+'
	}
	if _, err := syscall.Getxattr(path, "security.selinux", nil); err == nil {
		return '.'
	}
	return ' '
}

// unix_major and unix_minor split a device number the way the kernel encodes it.
func unix_major(dev uint64) uint64 { return (dev>>8)&0xfff | (dev >> 32 & ^uint64(0xfff)) }
func unix_minor(dev uint64) uint64 { return dev&0xff | (dev >> 12 & ^uint64(0xff)) }

// displayName is the entry's name written in the chosen quoting style.
func (l *lister) displayName(entry lsEntry) string { return l.quote(entry.name) }

// quote writes one name in the style -Q, -b, -N or --quoting-style asked for.
func (l *lister) quote(name string) string {
	switch l.opts.quoting {
	case lsQuoteC:
		return `"` + lsEscape(name, '"') + `"`
	case lsQuoteEscape:
		return lsEscape(name, 0)
	case lsQuoteShell, lsQuoteShellAlways:
		return lsShellQuote(name, l.opts.quoting == lsQuoteShellAlways)
	case lsQuoteShellEscape, lsQuoteShellEscapeAlways:
		return lsShellEscapeQuote(name, l.opts.quoting == lsQuoteShellEscapeAlways)
	default:
		return name
	}
}

// lsEscape renders the C escapes -b and -Q use. quote, when set, is the
// surrounding quote character, which is escaped along with the backslash.
func lsEscape(name string, quote byte) string {
	var out strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '\\':
			out.WriteString(`\\`)
		case quote != 0 && c == quote:
			out.WriteByte('\\')
			out.WriteByte(c)
		case c == '\a':
			out.WriteString(`\a`)
		case c == '\b':
			out.WriteString(`\b`)
		case c == '\f':
			out.WriteString(`\f`)
		case c == '\n':
			out.WriteString(`\n`)
		case c == '\r':
			out.WriteString(`\r`)
		case c == '\t':
			out.WriteString(`\t`)
		case c == '\v':
			out.WriteString(`\v`)
		case c == ' ' && quote == 0:
			out.WriteString(`\ `)
		case c < 0x20 || c >= 0x7f:
			// In the C locale nothing above ASCII is printable, so the bytes
			// of a UTF-8 name are written out one octal escape at a time.
			fmt.Fprintf(&out, `\%03o`, c)
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// lsShellSafe reports whether a byte can stand in a name the shell would read
// back as itself.
func lsShellSafe(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		strings.IndexByte("%+,-./:=@_", c) >= 0 || c >= 0x80
}

// lsShellQuote is the plain shell style: a name the shell would read as itself
// is printed bare, one carrying a single quote is put in double quotes, and
// anything else in single quotes, with its bytes untouched either way.
func lsShellQuote(name string, always bool) string {
	safe := name != ""
	for i := 0; i < len(name); i++ {
		if !lsShellSafe(name[i]) {
			safe = false
			break
		}
	}
	if safe && !always {
		return name
	}
	if strings.ContainsRune(name, '\'') {
		return lsDoubleQuote(name)
	}
	return "'" + name + "'"
}

// lsDoubleQuote wraps a name in double quotes, escaping only what the shell
// would still read specially inside them.
func lsDoubleQuote(name string) string {
	var out strings.Builder
	out.WriteByte('"')
	for i := 0; i < len(name); i++ {
		if c := name[i]; c == '"' || c == '\\' || c == '$' || c == '`' {
			out.WriteByte('\\')
		}
		out.WriteByte(name[i])
	}
	out.WriteByte('"')
	return out.String()
}

// lsShellEscapeQuote is the style a terminal gets: printable runs stay inside
// ordinary single quotes and everything else moves into a $'...' segment beside
// them, so that pasting the name back into a shell yields the same bytes.
func lsShellEscapeQuote(name string, always bool) string {
	safe, escapes := name != "", false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c >= 0x7f {
			escapes, safe = true, false
			continue
		}
		if !lsShellSafe(c) {
			safe = false
		}
	}
	if safe && !always {
		return name
	}
	if !escapes {
		return lsShellQuote(name, true)
	}
	// The name is written as alternating segments, starting with a literal one
	// that is empty when the first byte already needs an escape.
	var out strings.Builder
	i := 0
	for i < len(name) {
		start := i
		for i < len(name) && name[i] >= 0x20 && name[i] < 0x7f {
			i++
		}
		fmt.Fprintf(&out, "'%s'", name[start:i])
		start = i
		for i < len(name) && (name[i] < 0x20 || name[i] >= 0x7f) {
			i++
		}
		if start == i {
			continue
		}
		out.WriteString("$'")
		for _, c := range []byte(name[start:i]) {
			out.WriteString(lsControlEscape(c))
		}
		out.WriteByte('\'')
	}
	return out.String()
}

// lsControlEscape is one byte inside a $'...' segment.
func lsControlEscape(c byte) string {
	switch c {
	case '\a':
		return `\a`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	case '\v':
		return `\v`
	}
	return fmt.Sprintf(`\%03o`, c)
}

// targetIndicator classifies a symbolic link's referent for the long listing,
// and says nothing at all when the link is broken.
func (l *lister) targetIndicator(entry lsEntry) string {
	if l.opts.indicator == lsIndicatorNone {
		return ""
	}
	info, err := os.Stat(entry.path)
	if err != nil {
		return ""
	}
	return l.indicator(lsEntry{info: info, path: entry.path})
}

// indicator is the character -F, -p or --file-type appends to a name.
func (l *lister) indicator(entry lsEntry) string {
	if l.opts.indicator == lsIndicatorNone {
		return ""
	}
	mode := entry.info.Mode()
	if mode.IsDir() {
		return "/"
	}
	if l.opts.indicator == lsIndicatorSlash {
		return ""
	}
	switch {
	case mode&os.ModeSymlink != 0:
		return "@"
	case mode&os.ModeNamedPipe != 0:
		return "|"
	case mode&os.ModeSocket != 0:
		return "="
	case mode&os.ModeDevice != 0:
		return ""
	case l.opts.indicator == lsIndicatorClassify && mode&0o111 != 0 && mode.IsRegular():
		return "*"
	}
	return ""
}

func (l *lister) ownerName(info os.FileInfo) string {
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "?"
	}
	if l.opts.numericID {
		return strconv.FormatUint(uint64(status.Uid), 10)
	}
	if name, cached := l.uids[status.Uid]; cached {
		return name
	}
	name := strconv.FormatUint(uint64(status.Uid), 10)
	if account, err := user.LookupId(name); err == nil {
		name = account.Username
	}
	l.uids[status.Uid] = name
	return name
}

func (l *lister) groupName(info os.FileInfo) string {
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "?"
	}
	if l.opts.numericID {
		return strconv.FormatUint(uint64(status.Gid), 10)
	}
	if name, cached := l.gids[status.Gid]; cached {
		return name
	}
	name := strconv.FormatUint(uint64(status.Gid), 10)
	if group, err := user.LookupGroupId(name); err == nil {
		name = group.Name
	}
	l.gids[status.Gid] = name
	return name
}

// timeString renders one timestamp in the style --time-style asked for, and
// otherwise in the original's own two-part default.
func (l *lister) timeString(value time.Time) string {
	style := l.opts.timeStyle
	if l.opts.fullTime {
		style = "full-iso"
	}
	switch {
	case style == "" || style == "locale":
		return formatLsTime(value)
	case style == "full-iso":
		return value.Format("2006-01-02 15:04:05.000000000 -0700")
	case style == "long-iso":
		return value.Format("2006-01-02 15:04")
	case style == "iso":
		if lsRecent(value) {
			return value.Format("01-02 15:04")
		}
		return value.Format("2006-01-02")
	case strings.HasPrefix(style, "+"):
		// A "+FORMAT" style may carry two lines: the first is used for recent
		// timestamps and the second for older ones.
		formats := strings.SplitN(style[1:], "\n", 2)
		chosen := formats[0]
		if len(formats) == 2 && lsRecent(value) {
			chosen = formats[1]
		}
		text, err := formatDate(value, chosen)
		if err != nil {
			return formatLsTime(value)
		}
		return text
	case strings.HasPrefix(style, "posix-"):
		// The C locale is the only one here, so posix-STYLE is the plain
		// default the original falls back to.
		return formatLsTime(value)
	}
	return formatLsTime(value)
}

// lsRecent is the original's rule for which timestamps get a clock rather than
// a year: the last six months, and nothing in the future. Six months is half an
// average Gregorian year — 31556952/2 seconds — not half of 365 days, and the
// difference is enough to move a timestamp between the two formats.
func lsRecent(value time.Time) bool {
	const sixMonths = 31556952 / 2 * time.Second
	return time.Since(value) <= sixMonths && !value.After(time.Now())
}

// formatLsTime formats a timestamp the way the default style does: "Jan _2
// 15:04" for the last six months, "Jan _2  2006" for anything older.
func formatLsTime(t time.Time) string {
	if !lsRecent(t) {
		return t.Format("Jan _2  2006")
	}
	return t.Format("Jan _2 15:04")
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

// humanSize renders n the way -h does in ls, du and df: at most three
// significant characters, always rounded up. The originals round away from zero
// so a size never reads smaller than it is -- 3000 bytes is 3.0K, not 2.9K --
// and a value that rounds up to a whole 1024 moves to the next unit.
func humanSize(n int64) string { return humanSizeBase(uint64(n), 1024) } //nolint:gosec // G115: every caller passes a nonnegative size.

// humanSizeBase is the same scaling in either base, since df's -H asks for
// powers of 1000 under the same one-letter suffixes.
func humanSizeBase(value uint64, unit uint64) string {
	if value < unit {
		return strconv.FormatUint(value, 10)
	}
	units := []string{"K", "M", "G", "T", "P", "E"}
	if unit == 1000 {
		// GNU spells the SI kilo with a small k and leaves the rest capitalised.
		units[0] = "k"
	}
	divisor, index := unit, 0
	for value/divisor >= unit && index < len(units)-1 {
		divisor *= unit
		index++
	}
	for {
		whole, remainder := value/divisor, value%divisor
		if whole < 10 {
			// One decimal below ten, and the tenth is rounded up too.
			tenths := whole*10 + (remainder*10+divisor-1)/divisor
			if tenths < 100 {
				return fmt.Sprintf("%d.%d%s", tenths/10, tenths%10, units[index])
			}
			whole = tenths / 10
		} else if remainder > 0 {
			whole++
		}
		if whole < unit || index == len(units)-1 {
			return fmt.Sprintf("%d%s", whole, units[index])
		}
		divisor *= unit
		index++
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
