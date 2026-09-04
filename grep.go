// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// grepOpts holds parsed grep flags.
type grepOpts struct {
	ignoreCase   bool
	invert       bool
	lineNum      bool
	countOnly    bool
	filesOnly    bool // -l: only print names of matching files
	filesWithout bool // -L: only print names of files without a match
	noFilename   bool // -h
	withFn       bool // -H: force filename prefix
	wholeWord    bool // -w
	wholeLine    bool // -x
	fixed        bool // -F: pattern is a literal string
	extended     bool // -E: patterns use ERE rather than the default BRE
	recursive    bool // -r/-R
	followLinks  bool // -R: follow symlinks while descending
	quiet        bool // -q
	onlyMatch    bool // -o
	noMessages   bool // -s
	byteOffset   bool // -b
	nullData     bool // -z: input and output records end with NUL
	nullName     bool // -Z: a printed file name ends with NUL
	initialTab   bool // -T: line up the content behind a tab
	color        bool
	maxCount     int // -m, -1 = unlimited
	before       int // -B
	after        int // -A
	context      bool
	groupSep     string
	binaryFiles  string // binary, text or without-match
	directories  string // read, skip or recurse
	devices      string // read or skip
	label        string // --label: the name standard input is reported under
	includes     []string
	excludes     []string
	excludeDirs  []string
}

// The escape sequences GNU grep writes for its default GREP_COLORS.
const (
	grepColorMatch = "\x1b[01;31m\x1b[K"
	grepColorName  = "\x1b[35m\x1b[K"
	grepColorSep   = "\x1b[36m\x1b[K"
	grepColorNum   = "\x1b[32m\x1b[K"
	grepColorOff   = "\x1b[m\x1b[K"
)

// cmdGrep implements a useful subset of grep(1). Its default patterns are
// POSIX BREs and -E selects POSIX EREs. RE2 supplies execution, after the
// syntax has been translated and validated by the shared regex layer.
func cmdGrep(args []string) int {
	opts := grepOpts{
		maxCount:    -1,
		groupSep:    "--",
		binaryFiles: "binary",
		directories: "read",
		devices:     "read",
		label:       "(standard input)",
	}
	var patterns []string
	patternSet := false
	var files []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) > 1 && a[0] == '-' && a != "-" {
			consumed, status := opts.parseOption(a, args, &i, &patterns, &patternSet)
			if status != 0 {
				return status
			}
			if consumed {
				continue
			}
		}
		// The first non-flag operand is the pattern, unless -e or -f gave one.
		if !patternSet {
			patterns = append(patterns, a)
			patternSet = true
		} else {
			files = append(files, a)
		}
	}
	if !patternSet && i < len(args) {
		patterns, patternSet = append(patterns, args[i]), true
		i++
	}
	files = append(files, args[i:]...)

	if !patternSet {
		fatalf("grep", "no pattern given")
		return 2
	}

	matcher, err := newGrepMatcher(patterns, &opts)
	if err != nil {
		fatalf("grep", "%v", err)
		return 2
	}

	if len(files) == 0 {
		if opts.recursive {
			files = []string{"."}
		} else {
			files = []string{"-"}
		}
	}

	// Decide whether to prefix lines with filenames.
	showName := opts.withFn || opts.recursive || len(files) > 1
	if opts.noFilename {
		showName = false
	}

	g := &grepRun{opts: opts, matcher: matcher, showName: showName, out: bufio.NewWriter(os.Stdout)}
	for _, f := range files {
		g.operand(f)
		if g.done {
			break
		}
	}
	if err := g.out.Flush(); err != nil {
		fatalf("grep", "write error: %s", errText(err))
		g.fileError = true
	}

	switch {
	case g.fileError && !g.anyMatch:
		return 2
	case g.anyMatch:
		return 0
	}
	return 1
}

// parseOption handles one option word. It reports whether the word was an
// option at all, so a bare "-" or an operand falls through to the caller.
func (o *grepOpts) parseOption(a string, args []string, i *int, patterns *[]string, patternSet *bool) (bool, int) {
	value := func(name string) (string, bool) {
		*i++
		if *i >= len(args) {
			fatalf("grep", "option requires an argument -- '%s'", name)
			return "", false
		}
		return args[*i], true
	}
	// A long option's argument may be attached with "=" or given separately.
	name, argument, hasArgument := a, "", false
	if strings.HasPrefix(a, "--") {
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			name, argument, hasArgument = a[:eq], a[eq+1:], true
		}
	}
	longValue := func() (string, bool) {
		if hasArgument {
			return argument, true
		}
		return value(strings.TrimPrefix(name, "--"))
	}

	switch name {
	case "--ignore-case":
		o.ignoreCase = true
	case "--invert-match":
		o.invert = true
	case "--line-number":
		o.lineNum = true
	case "--count":
		o.countOnly = true
	case "--files-with-matches":
		o.filesOnly = true
	case "--files-without-match":
		o.filesWithout = true
	case "--no-filename":
		o.noFilename = true
	case "--with-filename":
		o.withFn = true
	case "--word-regexp":
		o.wholeWord = true
	case "--line-regexp":
		o.wholeLine = true
	case "--fixed-strings":
		o.fixed = true
	case "--extended-regexp":
		o.extended = true
	case "--basic-regexp":
		o.extended = false
	case "--recursive":
		o.recursive = true
	case "--dereference-recursive":
		o.recursive, o.followLinks = true, true
	case "--quiet", "--silent":
		o.quiet = true
	case "--only-matching":
		o.onlyMatch = true
	case "--no-messages":
		o.noMessages = true
	case "--byte-offset":
		o.byteOffset = true
	case "--null-data":
		o.nullData = true
	case "--null":
		o.nullName = true
	case "--text":
		o.binaryFiles = "text"
	case "--initial-tab":
		o.initialTab = true
	case "--binary":
		// There is no CRLF translation to turn off on this platform.
	case "--no-ignore-case":
		o.ignoreCase = false
	case "--label":
		v, ok := longValue()
		if !ok {
			return true, 2
		}
		o.label = v
	case "--exclude-from":
		v, ok := longValue()
		if !ok {
			return true, 2
		}
		text, err := os.ReadFile(v)
		if err != nil {
			fatalf("grep", "%s: %s", v, errText(err))
			return true, 2
		}
		for _, pattern := range strings.Split(strings.TrimSuffix(string(text), "\n"), "\n") {
			if pattern != "" {
				o.excludes = append(o.excludes, pattern)
			}
		}
	case "--devices":
		v, ok := longValue()
		if !ok {
			return true, 2
		}
		if v != "read" && v != "skip" {
			fatalf("grep", "unknown devices method")
			return true, 2
		}
		o.devices = v
	case "--line-buffered":
		// Output is flushed at the end either way.
	case "--no-group-separator":
		o.groupSep = ""
	case "--group-separator":
		v, ok := longValue()
		if !ok {
			return true, 2
		}
		o.groupSep = v
	case "--binary-files":
		v, ok := longValue()
		if !ok {
			return true, 2
		}
		switch v {
		case "binary", "text", "without-match":
			o.binaryFiles = v
		default:
			fatalf("grep", "unknown binary-files type")
			return true, 2
		}
	case "--directories":
		v, ok := longValue()
		if !ok {
			return true, 2
		}
		switch v {
		case "read", "skip", "recurse":
			o.directories = v
			o.recursive = o.recursive || v == "recurse"
		default:
			fatalf("grep", "unknown directories method")
			return true, 2
		}
	case "--color", "--colour":
		when := "auto"
		if hasArgument {
			when = argument
		}
		switch when {
		case "always", "yes", "force":
			o.color = true
		case "never", "no", "none":
			o.color = false
		case "auto", "tty", "if-tty":
			o.color = isTerminal(os.Stdout.Fd())
		default:
			fatalf("grep", "invalid argument %s for %s", quoteLocaleName(when), quoteLocaleName("--color"))
			return true, 2
		}
	case "--include", "--exclude", "--exclude-dir":
		v, ok := longValue()
		if !ok {
			return true, 2
		}
		switch name {
		case "--include":
			o.includes = append(o.includes, v)
		case "--exclude":
			o.excludes = append(o.excludes, v)
		default:
			o.excludeDirs = append(o.excludeDirs, v)
		}
	case "--regexp", "--file", "--max-count", "--after-context", "--before-context", "--context":
		v, ok := longValue()
		if !ok {
			return true, 2
		}
		return true, o.applyValued(name[2:3], v, patterns, patternSet)
	default:
		if strings.HasPrefix(a, "--") {
			fatalf("grep", "unrecognized option '%s'", a)
			return true, 2
		}
		return o.parseShortCluster(a, args, i, patterns, patternSet)
	}
	return true, 0
}

// parseShortCluster walks a bundle such as -inv or -A2, where an option that
// takes a value swallows the rest of the word or the next argument.
func (o *grepOpts) parseShortCluster(a string, args []string, i *int, patterns *[]string, patternSet *bool) (bool, int) {
	// The historical "-NUM" form is a synonym for -C NUM.
	if digits := strings.TrimLeft(a[1:], "0123456789"); digits == "" {
		return true, o.applyValued("c", a[1:], patterns, patternSet)
	}
	for j := 1; j < len(a); j++ {
		switch c := a[j]; c {
		case 'i', 'y':
			o.ignoreCase = true
		case 'v':
			o.invert = true
		case 'n':
			o.lineNum = true
		case 'c':
			o.countOnly = true
		case 'l':
			o.filesOnly = true
		case 'L':
			o.filesWithout = true
		case 'h':
			o.noFilename = true
		case 'H':
			o.withFn = true
		case 'w':
			o.wholeWord = true
		case 'x':
			o.wholeLine = true
		case 'F':
			o.fixed = true
		case 'E':
			o.extended = true
		case 'G':
			o.extended = false
		case 'r':
			o.recursive = true
		case 'R':
			o.recursive, o.followLinks = true, true
		case 'q':
			o.quiet = true
		case 'o':
			o.onlyMatch = true
		case 's':
			o.noMessages = true
		case 'b':
			o.byteOffset = true
		case 'z':
			o.nullData = true
		case 'Z':
			o.nullName = true
		case 'a':
			o.binaryFiles = "text"
		case 'I':
			o.binaryFiles = "without-match"
		case 'U':
			// No CRLF translation happens here in the first place.
		case 'T':
			o.initialTab = true
		case 'D':
			v := a[j+1:]
			if v == "" {
				*i++
				if *i >= len(args) {
					fatalf("grep", "option requires an argument -- 'D'")
					return true, 2
				}
				v = args[*i]
			}
			if v != "read" && v != "skip" {
				fatalf("grep", "unknown devices method")
				return true, 2
			}
			o.devices = v
			return true, 0
		case 'e', 'f', 'm', 'A', 'B', 'C', 'd':
			v := a[j+1:]
			if v == "" {
				*i++
				if *i >= len(args) {
					fatalf("grep", "option requires an argument -- '%c'", c)
					return true, 2
				}
				v = args[*i]
			}
			if c == 'd' {
				switch v {
				case "read", "skip", "recurse":
					o.directories = v
					o.recursive = o.recursive || v == "recurse"
				default:
					fatalf("grep", "unknown directories method")
					return true, 2
				}
				return true, 0
			}
			return true, o.applyValued(string(c), v, patterns, patternSet)
		default:
			fatalf("grep", "invalid option -- '%c'", c)
			return true, 2
		}
	}
	return true, 0
}

// applyValued stores the argument of one value-taking option.
func (o *grepOpts) applyValued(kind, v string, patterns *[]string, patternSet *bool) int {
	number := func() (int, bool) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			fatalf("grep", "%s: invalid context length argument", v)
			return 0, false
		}
		return n, true
	}
	switch kind {
	case "r", "e": // -e/--regexp
		// Several patterns may be given, and one argument may hold a list.
		*patterns = append(*patterns, strings.Split(v, "\n")...)
		*patternSet = true
	case "f": // -f/--file
		text, err := os.ReadFile(v)
		if err != nil {
			fatalf("grep", "%s: %s", v, errText(err))
			return 2
		}
		lines := strings.Split(strings.TrimSuffix(string(text), "\n"), "\n")
		*patterns = append(*patterns, lines...)
		*patternSet = true
	case "m": // -m/--max-count
		n, err := parseCount(v)
		maxInt := int64(^uint(0) >> 1)
		if err != nil || n > maxInt {
			fatalf("grep", "invalid max count %q", v)
			return 2
		}
		o.maxCount = int(n)
	case "a", "A": // -A/--after-context
		n, ok := number()
		if !ok {
			return 2
		}
		o.after, o.context = n, true
	case "b", "B": // -B/--before-context
		n, ok := number()
		if !ok {
			return 2
		}
		o.before, o.context = n, true
	case "c", "C": // -C/--context
		n, ok := number()
		if !ok {
			return 2
		}
		o.before, o.after, o.context = n, n, true
	}
	return 0
}

// grepMatcher finds the parts of a line the pattern selects. -w and -x are
// applied as position tests rather than as extra regexp syntax, so -o reports
// the pattern's own text and a rejected position can be retried further along
// the line the way the original does.
type grepMatcher struct {
	re   *regexp.Regexp
	word bool
	line bool
}

func newGrepMatcher(patterns []string, opts *grepOpts) (*grepMatcher, error) {
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if opts.fixed {
			p = regexp.QuoteMeta(p)
		} else {
			var err error
			syntax := posixBRE
			if opts.extended {
				syntax = posixERE
			}
			p, err = translatePOSIXRegexp(p, syntax)
			if err != nil {
				return nil, err
			}
		}
		parts = append(parts, "("+p+")")
	}
	re, err := compilePOSIXERE(strings.Join(parts, "|"), opts.ignoreCase)
	if err != nil {
		return nil, err
	}
	return &grepMatcher{re: re, word: opts.wholeWord, line: opts.wholeLine}, nil
}

// next returns the first acceptable match at or after from.
func (m *grepMatcher) next(s string, from int) (int, int, bool) {
	for at := from; at <= len(s); {
		loc := m.re.FindStringIndex(s[at:])
		if loc == nil {
			return 0, 0, false
		}
		start, end := at+loc[0], at+loc[1]
		if m.accepts(s, start, end) {
			return start, end, true
		}
		// Retry one byte along: a different position may satisfy -w or -x.
		at = start + 1
	}
	return 0, 0, false
}

// accepts applies -w and -x to one candidate match.
func (m *grepMatcher) accepts(s string, start, end int) bool {
	if m.line {
		return start == 0 && end == len(s)
	}
	if !m.word {
		return true
	}
	if start > 0 && isWordByte(s[start-1]) {
		return false
	}
	if end < len(s) && isWordByte(s[end]) {
		return false
	}
	return true
}

func (m *grepMatcher) match(s string) bool {
	_, _, ok := m.next(s, 0)
	return ok
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

type grepRun struct {
	opts      grepOpts
	matcher   *grepMatcher
	showName  bool
	out       *bufio.Writer
	anyMatch  bool
	fileError bool
	done      bool

	// offsetWidth right-aligns the line numbers and byte offsets -T prints.
	offsetWidth int

	// Where the previous printed line came from, so a non-contiguous group
	// can be introduced by the "--" separator the way the original does.
	lastFile string
	lastLine int
	printed  bool
}

// message reports a per-file problem unless -s silenced it. The exit status
// still changes, which is what -s leaves alone.
func (g *grepRun) message(format string, args ...any) {
	g.fileError = true
	if g.opts.noMessages {
		return
	}
	fatalf("grep", format, args...)
}

// operand handles one command-line file, directory or "-".
func (g *grepRun) operand(name string) {
	if name == "-" {
		g.scanReader(os.Stdin, g.opts.label)
		return
	}
	info, err := os.Stat(name)
	if err != nil {
		g.message("%s: %s", name, errText(err))
		return
	}
	if !info.Mode().IsRegular() && !info.IsDir() && g.opts.devices == "skip" {
		return
	}
	if info.IsDir() {
		switch {
		case g.opts.recursive || g.opts.directories == "recurse":
			g.walk(name)
		case g.opts.directories == "skip":
		default:
			g.message("%s: Is a directory", name)
		}
		return
	}
	g.scanFile(name)
}

// walk descends a directory, honouring --include, --exclude and --exclude-dir.
func (g *grepRun) walk(root string) {
	entries, err := readDirRaw(root)
	if err != nil {
		g.message("%s: %s", root, errText(err))
		return
	}
	for _, entry := range entries {
		if g.done {
			return
		}
		path := filepath.Join(root, entry)
		info, err := os.Lstat(path)
		if err != nil {
			g.message("%s: %s", path, errText(err))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !g.opts.followLinks {
				continue
			}
			if info, err = os.Stat(path); err != nil {
				g.message("%s: %s", path, errText(err))
				continue
			}
		}
		if info.IsDir() {
			if matchesAnyGlob(entry, path, g.opts.excludeDirs) {
				continue
			}
			g.walk(path)
			continue
		}
		if !info.Mode().IsRegular() && g.opts.devices == "skip" {
			continue
		}
		if len(g.opts.includes) > 0 && !matchesAnyGlob(entry, path, g.opts.includes) {
			continue
		}
		if matchesAnyGlob(entry, path, g.opts.excludes) {
			continue
		}
		g.scanFile(path)
	}
}

// matchesAnyGlob reports whether a name or its whole path matches one pattern.
func matchesAnyGlob(name, path string, patterns []string) bool {
	for _, pattern := range patterns {
		for _, candidate := range []string{name, path} {
			if ok, err := filepath.Match(pattern, candidate); err == nil && ok {
				return true
			}
		}
	}
	return false
}

func (g *grepRun) scanFile(name string) {
	f, err := os.Open(name)
	if err != nil {
		g.message("%s: %s", name, errText(err))
		return
	}
	g.scanReader(f, name)
	if closeErr := f.Close(); closeErr != nil {
		g.message("%s: %s", name, errText(closeErr))
	}
}

// grepLine is one input line held for context.
type grepLine struct {
	text   string
	number int
	offset int64
}

func (g *grepRun) scanReader(r io.Reader, display string) {
	g.offsetWidth = grepOffsetWidth(r)
	reader := bufio.NewReaderSize(r, 64*1024)
	binary := false
	// With -z a NUL is the record separator, so it is not evidence of a
	// binary file the way it is in the default mode.
	if g.opts.binaryFiles != "text" && !g.opts.nullData {
		// The original decides from the head of the file, as this does.
		if head, err := reader.Peek(32 * 1024); len(head) > 0 || err == nil {
			binary = bytes.IndexByte(head, 0) >= 0
		}
	}
	if binary && g.opts.binaryFiles == "without-match" {
		return
	}

	sc := newLineScanner(reader)
	if g.opts.nullData {
		sc.Split(scanNulLines)
	}

	// Only the plain line output is replaced by the binary notice; -c, -l and
	// -q still work through the file.
	quietBinary := binary && !g.opts.countOnly && !g.opts.filesOnly &&
		!g.opts.filesWithout && !g.opts.quiet

	count := 0
	lineNo := 0
	offset := int64(0)
	before := make([]grepLine, 0, g.opts.before)
	afterLeft := 0

	if g.opts.maxCount == 0 {
		g.reportCount(display, 0)
		return
	}
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		current := grepLine{text: line, number: lineNo, offset: offset}
		offset += int64(len(line)) + 1

		selected := g.matcher.match(line)
		if g.opts.invert {
			selected = !selected
		}
		if !selected {
			if afterLeft > 0 && !quietBinary {
				g.printLine(display, current, false)
				afterLeft--
			} else if g.opts.before > 0 {
				if len(before) == g.opts.before {
					before = before[1:]
				}
				before = append(before, current)
			}
			continue
		}

		g.anyMatch = true
		count++
		if g.opts.quiet {
			g.done = true
			return
		}
		if g.opts.filesOnly || g.opts.filesWithout {
			if g.opts.filesOnly {
				g.printName(display)
			}
			return
		}
		if !g.opts.countOnly {
			if quietBinary {
				// One notice per file, on stderr, as the original writes it.
				fatalf("grep", "%s: binary file matches", display)
				return
			}
			for _, held := range before {
				g.printLine(display, held, false)
			}
			g.printLine(display, current, true)
			afterLeft = g.opts.after
		}
		before = before[:0]
		if g.opts.maxCount > 0 && count >= g.opts.maxCount {
			// Trailing context is still printed after the last match.
			for afterLeft > 0 && sc.Scan() {
				lineNo++
				trailing := grepLine{text: sc.Text(), number: lineNo, offset: offset}
				offset += int64(len(trailing.text)) + 1
				g.printLine(display, trailing, false)
				afterLeft--
			}
			break
		}
	}
	if scanErr("grep", display, sc) {
		g.fileError = true
	}
	g.reportCount(display, count)
	// -L lists a file that held no match; that is not itself a selected line,
	// so it does not change the exit status the way a match does.
	if g.opts.filesWithout && count == 0 {
		g.printName(display)
	}
}

// reportCount writes the -c line for one file.
func (g *grepRun) reportCount(display string, count int) {
	if !g.opts.countOnly || g.opts.quiet || g.opts.filesOnly || g.opts.filesWithout {
		return
	}
	if g.showName {
		g.writeName(display, ":")
	}
	fmt.Fprintf(g.out, "%d\n", count)
}

// printName writes a bare file name, for -l and -L.
func (g *grepRun) printName(display string) {
	if g.opts.color {
		_, _ = g.out.WriteString(grepColorName + display + grepColorOff)
	} else {
		_, _ = g.out.WriteString(display)
	}
	if g.opts.nullName {
		_ = g.out.WriteByte(0)
		return
	}
	_ = g.out.WriteByte('\n')
}

// writeName writes the file-name prefix and its separator (":" for a selected
// line, "-" for a context line).
func (g *grepRun) writeName(display, sep string) {
	if g.opts.color {
		_, _ = g.out.WriteString(grepColorName + display + grepColorOff)
		if g.opts.nullName {
			_ = g.out.WriteByte(0)
			return
		}
		_, _ = g.out.WriteString(grepColorSep + sep + grepColorOff)
		return
	}
	_, _ = g.out.WriteString(display)
	if g.opts.nullName {
		_ = g.out.WriteByte(0)
		return
	}
	_, _ = g.out.WriteString(sep)
}

// grepOffsetWidth is the field -T aligns numbers in: the digit count of the
// largest offset the input can produce. That is the file's size when it has
// one, and the width of the largest representable offset when it does not.
func grepOffsetWidth(r io.Reader) int {
	if f, ok := r.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode().IsRegular() {
			return len(strconv.FormatInt(info.Size(), 10))
		}
	}
	return len(strconv.FormatInt(int64(^uint64(0)>>1), 10))
}

func (g *grepRun) writeNumber(value string, sep string) {
	if g.opts.initialTab {
		for pad := g.offsetWidth - len(value); pad > 0; pad-- {
			_ = g.out.WriteByte(' ')
		}
	}
	if g.opts.color {
		_, _ = g.out.WriteString(grepColorNum + value + grepColorOff + grepColorSep + sep + grepColorOff)
		return
	}
	_, _ = g.out.WriteString(value + sep)
}

// printLine writes one selected or context line, with the prefixes the options
// ask for. With -o the matched parts are written one per line instead.
func (g *grepRun) printLine(display string, line grepLine, selected bool) {
	sep := "-"
	if selected {
		sep = ":"
	}
	g.separate(display, line.number)
	if g.opts.onlyMatch && !selected {
		// A context line prints nothing in -o mode, but it still closes the
		// gap to the next group, so only the printing is skipped.
		return
	}
	terminator := byte('\n')
	if g.opts.nullData {
		terminator = 0
	}
	prefix := func(offset int64) {
		wrote := false
		if g.showName {
			g.writeName(display, sep)
			wrote = true
		}
		if g.opts.lineNum {
			g.writeNumber(strconv.Itoa(line.number), sep)
			wrote = true
		}
		if g.opts.byteOffset {
			g.writeNumber(strconv.FormatInt(offset, 10), sep)
			wrote = true
		}
		if g.opts.initialTab && wrote {
			_ = g.out.WriteByte('\t')
		}
	}
	if g.opts.onlyMatch {
		for at := 0; at <= len(line.text); {
			start, end, ok := g.matcher.next(line.text, at)
			if !ok {
				break
			}
			// An empty match selects the line but prints nothing, which is
			// what the original does for a pattern such as "x*".
			if end > start && !g.opts.invert {
				prefix(line.offset + int64(start))
				if g.opts.color {
					_, _ = g.out.WriteString(grepColorMatch + line.text[start:end] + grepColorOff)
				} else {
					_, _ = g.out.WriteString(line.text[start:end])
				}
				_ = g.out.WriteByte(terminator)
			}
			at = end
			if end == start {
				at++
			}
		}
		return
	}
	prefix(line.offset)
	if g.opts.color && selected && !g.opts.invert {
		_, _ = g.out.WriteString(g.colorize(line.text))
	} else {
		_, _ = g.out.WriteString(line.text)
	}
	_ = g.out.WriteByte(terminator)
}

// colorize wraps every match in a line with the match colour.
func (g *grepRun) colorize(line string) string {
	var b strings.Builder
	at := 0
	for at <= len(line) {
		start, end, ok := g.matcher.next(line, at)
		if !ok {
			break
		}
		b.WriteString(line[at:start])
		if end > start {
			// An empty match is not highlighted, as in the original.
			b.WriteString(grepColorMatch + line[start:end] + grepColorOff)
		} else if start < len(line) {
			b.WriteByte(line[start])
		}
		at = end
		if end == start {
			at++
		}
	}
	if at < len(line) {
		b.WriteString(line[at:])
	}
	return b.String()
}

// separate writes the group separator when the line about to be printed does
// not continue the previous one.
func (g *grepRun) separate(display string, number int) {
	if g.opts.context && g.printed && g.groupBroken(display, number) && g.opts.groupSep != "" {
		if g.opts.color {
			_, _ = g.out.WriteString(grepColorSep + g.opts.groupSep + grepColorOff + "\n")
		} else {
			_, _ = g.out.WriteString(g.opts.groupSep + "\n")
		}
	}
	g.lastFile, g.lastLine, g.printed = display, number, true
}

func (g *grepRun) groupBroken(display string, number int) bool {
	return display != g.lastFile || number > g.lastLine+1
}
