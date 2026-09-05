// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type sedAddress struct {
	line  int
	last  bool
	regex *regexp.Regexp
	// step makes this GNU's "first~step" address, which selects every step'th
	// line from first onwards.
	step int
}

func (a *sedAddress) matches(text string, line int, last bool) bool {
	if a == nil {
		return true
	}
	if a.step > 0 {
		return line >= a.line && (line-a.line)%a.step == 0
	}
	if a.line > 0 {
		return line == a.line
	}
	if a.last {
		return last
	}
	return a.regex != nil && a.regex.MatchString(text)
}

type sedCommand struct {
	first, second *sedAddress
	inRange       bool
	negated       bool
	kind          byte

	// s///
	regex         *regexp.Regexp
	replacement   string
	global        bool
	printOnChange bool
	writeFile     string

	// y///
	yFrom, yTo []rune

	// a/i/c text
	text string

	// b/t/T (jumpTarget) and :label (own index is found via the label map)
	label      string
	jumpTarget int

	// q/Q
	exitCode int

	// l's own wrap width, and whether the command carried one
	wrapWidth int
	hasWrap   bool

	// s///N: the first occurrence to act on, counting from one
	occurrence int

	// r/R/w/W
	file string
}

// selected reports whether the command applies to this line, honouring an
// address negation such as "/TYPE := /,/}/!d".
func (c *sedCommand) selected(text string, line int, last bool) bool {
	return c.inAddress(text, line, last) != c.negated
}

func (c *sedCommand) inAddress(text string, line int, last bool) bool {
	if c.second == nil {
		return c.first.matches(text, line, last)
	}
	started := false
	if !c.inRange {
		if !c.first.matches(text, line, last) {
			return false
		}
		c.inRange = true
		started = true
	}
	ends := false
	switch {
	case c.second.line > 0:
		// A numeric second address at or before the first address makes a
		// one-line range; otherwise it ends when that line is reached.
		ends = line >= c.second.line
	case c.second.regex != nil:
		// sed starts testing a regular-expression second address on the line
		// after the first address matched.
		ends = !started && c.second.matches(text, line, last)
	default:
		ends = c.second.matches(text, line, last)
	}
	if ends {
		c.inRange = false
	}
	return true
}

// rangeEnding reports whether this line is the last one a range command
// matches -- true for every match of a single-address command, and only for
// the closing line of a two-address range. 'c' uses this to print its
// replacement text once per range instead of once per line.
func (c *sedCommand) rangeEnding() bool {
	return c.second == nil || !c.inRange
}

type sedLine struct {
	text       string
	terminated bool
}

func cmdSed(args []string) int {
	options := sedRunOptions{listWidth: 70, separator: '\n'}
	noDefault := false
	extended := false
	inPlace := false
	backupSuffix := ""
	var scripts, files []string
	parsingOptions := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if parsingOptions && arg == "--" {
			parsingOptions = false
			continue
		}
		if parsingOptions && (arg == "-n" || arg == "--quiet" || arg == "--silent") {
			noDefault = true
			continue
		}
		if parsingOptions && (arg == "-s" || arg == "--separate") {
			options.separate = true
			continue
		}
		if parsingOptions && (arg == "-z" || arg == "--null-data" || arg == "--zero-terminated") {
			options.separator = 0
			continue
		}
		if parsingOptions && (arg == "--posix" || arg == "--sandbox" || arg == "-u" || arg == "--unbuffered") {
			// The POSIX subset is what this implementation already keeps to,
			// nothing here runs a command, and output is flushed at the end.
			continue
		}
		if parsingOptions && (arg == "-l" || arg == "--line-length") {
			i++
			if i >= len(args) {
				fatalf("sed", "option requires an argument -- 'l'")
				return 1
			}
			width, convErr := strconv.Atoi(args[i])
			if convErr != nil || width < 0 {
				fatalf("sed", "invalid line-wrap length: %s", args[i])
				return 1
			}
			options.listWidth = width
			continue
		}
		if parsingOptions && strings.HasPrefix(arg, "--line-length=") {
			width, convErr := strconv.Atoi(strings.TrimPrefix(arg, "--line-length="))
			if convErr != nil || width < 0 {
				fatalf("sed", "invalid line-wrap length: %s", arg)
				return 1
			}
			options.listWidth = width
			continue
		}
		if parsingOptions && (arg == "-E" || arg == "-r" || arg == "--regexp-extended") {
			extended = true
			continue
		}
		if parsingOptions && (arg == "-i" || arg == "--in-place") {
			inPlace = true
			continue
		}
		if parsingOptions && strings.HasPrefix(arg, "-i") && len(arg) > 2 {
			inPlace, backupSuffix = true, arg[2:]
			continue
		}
		if parsingOptions && strings.HasPrefix(arg, "--in-place=") {
			inPlace, backupSuffix = true, strings.TrimPrefix(arg, "--in-place=")
			continue
		}
		if parsingOptions && (arg == "-e" || arg == "--expression") {
			i++
			if i >= len(args) {
				fatalf("sed", "option requires an argument -- 'e'")
				return 1
			}
			scripts = append(scripts, args[i])
			continue
		}
		if parsingOptions && strings.HasPrefix(arg, "-e") && len(arg) > 2 {
			scripts = append(scripts, arg[2:])
			continue
		}
		if parsingOptions && (arg == "-f" || arg == "--file") {
			i++
			if i >= len(args) {
				fatalf("sed", "option requires an argument -- 'f'")
				return 1
			}
			contents, err := os.ReadFile(args[i])
			if err != nil {
				fatalf("sed", "%s: %v", args[i], err)
				return 1
			}
			scripts = append(scripts, string(contents))
			continue
		}
		if parsingOptions && strings.HasPrefix(arg, "-f") && len(arg) > 2 {
			contents, err := os.ReadFile(arg[2:])
			if err != nil {
				fatalf("sed", "%s: %v", arg[2:], err)
				return 1
			}
			scripts = append(scripts, string(contents))
			continue
		}
		if parsingOptions && len(arg) > 1 && arg[0] == '-' {
			fatalf("sed", "invalid option %q", arg)
			return 1
		}
		if len(scripts) == 0 {
			scripts = append(scripts, arg)
			parsingOptions = false
		} else {
			files = append(files, arg)
		}
	}
	if len(scripts) == 0 {
		fatalf("sed", "missing script")
		return 1
	}
	// Every -e/-f fragment is one continuous script: a trailing backslash in
	// one fragment (as in the classic "-e 'a\' -e text" idiom) must be able
	// to continue into the next.
	commands, err := parseSedScript(strings.Join(scripts, "\n"), extended)
	if err != nil {
		fatalf("sed", "%v", err)
		return 1
	}
	if err := resolveSedLabels(commands); err != nil {
		fatalf("sed", "%v", err)
		return 1
	}

	options.noDefault = noDefault
	if inPlace {
		if len(files) == 0 {
			fatalf("sed", "no input files")
			return 1
		}
		status := 0
		for _, name := range files {
			if s := runSedInPlace(commands, name, options, backupSuffix); s != 0 {
				status = s
			}
		}
		return status
	}

	if len(files) == 0 {
		files = []string{"-"}
	}
	out := bufio.NewWriter(os.Stdout)
	if options.separate {
		// -s starts each file afresh, so line numbers and $ are per file.
		status, exitCode := 0, 0
		for _, name := range files {
			fileStatus, fileExit := runSedStream(commands, []string{name}, options, out)
			if fileStatus != 0 {
				status = fileStatus
			}
			if fileExit != 0 {
				exitCode = fileExit
			}
		}
		if err := out.Flush(); err != nil {
			fatalf("sed", "write error: %v", err)
			return 1
		}
		if status != 0 {
			return status
		}
		return exitCode
	}
	status, exitCode := runSedStream(commands, files, options, out)
	if err := out.Flush(); err != nil {
		fatalf("sed", "write error: %v", err)
		return 1
	}
	if status != 0 {
		return status
	}
	return exitCode
}

// sedRunOptions is what the command line settles before the script runs.
type sedRunOptions struct {
	noDefault bool
	// listWidth is `l''s default wrap width, which -l sets and the original
	// leaves at 70.
	listWidth int
	// separator is the byte that ends a line: a newline, or NUL under -z.
	separator byte
	// separate is -s: every file gets its own line numbering and its own $.
	separate bool
}

// runSedInPlace runs the script against one file, the way -i requires: its
// own fresh command state, its own line numbering and $ (so hold space and
// range addresses do not leak between files), writing to a temp file that
// replaces the original only on success.
func runSedInPlace(commands []sedCommand, name string, options sedRunOptions, backupSuffix string) int {
	info, err := os.Stat(name)
	if err != nil {
		fatalf("sed", "can't read %s: %v", name, errText(err))
		return 1
	}
	if !info.Mode().IsRegular() {
		fatalf("sed", "couldn't edit %s: not a regular file", name)
		return 1
	}
	dir := filepath.Dir(name)
	temp, err := os.CreateTemp(dir, ".sed-"+filepath.Base(name)+"-*")
	if err != nil {
		fatalf("sed", "%v", err)
		return 1
	}
	tempName := temp.Name()
	out := bufio.NewWriter(temp)
	status, exitCode := runSedStream(commands, []string{name}, options, out)
	flushErr := out.Flush()
	closeErr := temp.Close()
	if status != 0 || flushErr != nil || closeErr != nil {
		os.Remove(tempName)
		if status != 0 {
			return status
		}
		fatalf("sed", "%v", firstNonNil(flushErr, closeErr))
		return 1
	}
	if err := os.Chmod(tempName, info.Mode()); err != nil {
		os.Remove(tempName)
		fatalf("sed", "%v", err)
		return 1
	}
	if backupSuffix != "" {
		backupName := name + backupSuffix
		if strings.Contains(backupSuffix, "*") {
			backupName = strings.ReplaceAll(backupSuffix, "*", filepath.Base(name))
			if !filepath.IsAbs(backupName) && strings.Contains(backupSuffix, string(filepath.Separator)) {
				backupName = filepath.Join(dir, backupName)
			}
		}
		if err := copyFilePreserving(name, backupName, info.Mode()); err != nil {
			os.Remove(tempName)
			fatalf("sed", "%v", err)
			return 1
		}
	}
	if err := os.Rename(tempName, name); err != nil {
		os.Remove(tempName)
		fatalf("sed", "%v", err)
		return 1
	}
	return exitCode
}

func firstNonNil(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func copyFilePreserving(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}

// sedRuntime holds the state that persists across a whole run of the
// script -- as opposed to sedCommand's inRange, which is per-command -- plus
// the file handles r/R/w/W commands touch.
type sedRuntime struct {
	hold         string
	appendQueue  []string
	writeWriters map[string]*bufio.Writer
	writeHandles map[string]*os.File
	readCursors  map[string]*bufio.Scanner
	readFiles    map[string]*os.File
	substMade    bool
}

func newSedRuntime() *sedRuntime {
	return &sedRuntime{
		writeWriters: map[string]*bufio.Writer{},
		writeHandles: map[string]*os.File{},
		readCursors:  map[string]*bufio.Scanner{},
		readFiles:    map[string]*os.File{},
	}
}

func (rt *sedRuntime) close() {
	for _, w := range rt.writeWriters {
		_ = w.Flush()
	}
	for _, f := range rt.writeHandles {
		_ = f.Close()
	}
	for _, f := range rt.readFiles {
		_ = f.Close()
	}
}

func (rt *sedRuntime) writerFor(name string) (*bufio.Writer, error) {
	if w, ok := rt.writeWriters[name]; ok {
		return w, nil
	}
	if name == "/dev/stdout" {
		w := bufio.NewWriter(os.Stdout)
		rt.writeWriters[name] = w
		return w, nil
	}
	f, err := os.Create(name) //nolint:gosec // G304: sed's w command intentionally writes to a script-named file
	if err != nil {
		return nil, err
	}
	rt.writeHandles[name] = f
	w := bufio.NewWriter(f)
	rt.writeWriters[name] = w
	return w, nil
}

// nextReadLine returns the next line of name for the R command, advancing a
// per-file cursor that is shared across every R invocation targeting it. It
// returns ok=false once the file is exhausted (R then queues nothing).
func (rt *sedRuntime) nextReadLine(name string) (string, bool) {
	scanner, ok := rt.readCursors[name]
	if !ok {
		f, err := os.Open(name) //nolint:gosec // G304: sed's R command intentionally reads a script-named file
		if err != nil {
			rt.readCursors[name] = nil
			return "", false
		}
		rt.readFiles[name] = f
		scanner = bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), maxScanLine)
		rt.readCursors[name] = scanner
	}
	if scanner == nil || !scanner.Scan() {
		return "", false
	}
	return scanner.Text(), true
}

// runSedStream is the core sed engine: one cycle per input line (or, after
// 'D' leaves text in the pattern space, one cycle with no new line read),
// walking commands with a program counter so b/t/T can jump. It returns a
// non-zero first result on an execution error, or the second result as the
// process exit code the script itself chose via q/Q.
func runSedStream(commands []sedCommand, files []string, options sedRunOptions, out *bufio.Writer) (int, int) {
	for i := range commands {
		commands[i].inRange = false
	}
	noDefault, listWidth := options.noDefault, options.listWidth
	needsLast := sedNeedsLastAddress(commands)
	rt := newSedRuntime()
	defer rt.close()

	stream := &sedStream{
		files: files, peekAhead: needsLast,
		separator: options.separator, nullData: options.separator == 0,
	}
	defer stream.Close()

	pattern, terminated, ok, err := stream.next2()
	if err != nil {
		fatalf("sed", "%v", err)
		return 1, 0
	}
	lineNumber := 0
	restart := false
	for ok {
		if !restart {
			lineNumber++
			rt.substMade = false
		}
		restart = false
		lastLine, err := stream.atEnd()
		if err != nil {
			fatalf("sed", "%v", err)
			return 1, 0
		}

		autoprintSuppressed := false
		quit, quitCode := false, 0
		pc := 0
	cycle:
		for pc < len(commands) {
			command := &commands[pc]
			if command.kind == ':' || command.kind == '}' {
				pc++
				continue
			}
			if !command.selected(pattern, lineNumber, lastLine) {
				if command.kind == '{' {
					pc = command.jumpTarget
				} else {
					pc++
				}
				continue
			}
			switch command.kind {
			case '{':
				// Selected: fall through into the block: pc++ below.
			case 's':
				if replaced, changed := sedSubstitute(command, pattern); changed {
					pattern = replaced
					rt.substMade = true
					if command.printOnChange {
						if err := writeSedLineWith(out, pattern, terminated, options.separator); err != nil {
							fatalf("sed", "write error: %v", err)
							return 1, 0
						}
					}
					if command.writeFile != "" {
						w, werr := rt.writerFor(command.writeFile)
						if werr != nil {
							fatalf("sed", "%v", werr)
							return 1, 0
						}
						fmt.Fprintln(w, pattern)
					}
				}
			case 'y':
				pattern = sedTransliterate(pattern, command.yFrom, command.yTo)
			case 'd':
				autoprintSuppressed = true
				break cycle
			case 'D':
				autoprintSuppressed = true
				if idx := strings.IndexByte(pattern, '\n'); idx >= 0 {
					pattern = pattern[idx+1:]
					restart = true
				}
				break cycle
			case 'p':
				if err := writeSedLineWith(out, pattern, terminated, options.separator); err != nil {
					fatalf("sed", "write error: %v", err)
					return 1, 0
				}
			case 'P':
				first := pattern
				if idx := strings.IndexByte(pattern, '\n'); idx >= 0 {
					first = pattern[:idx]
				}
				fmt.Fprintln(out, first)
			case 'h':
				rt.hold = pattern
			case 'H':
				rt.hold += "\n" + pattern
			case 'g':
				pattern = rt.hold
			case 'G':
				pattern += "\n" + rt.hold
			case 'x':
				pattern, rt.hold = rt.hold, pattern
			case 'n':
				if !noDefault {
					if err := writeSedLineWith(out, pattern, terminated, options.separator); err != nil {
						fatalf("sed", "write error: %v", err)
						return 1, 0
					}
				}
				next, nextTerminated, hasNext, nextErr := stream.next2()
				if nextErr != nil {
					fatalf("sed", "%v", nextErr)
					return 1, 0
				}
				if !hasNext {
					quit, autoprintSuppressed = true, true
					break cycle
				}
				pattern, terminated = next, nextTerminated
				lineNumber++
				lastLine, err = stream.atEnd()
				if err != nil {
					fatalf("sed", "%v", err)
					return 1, 0
				}
			case 'N':
				next, nextTerminated, hasNext, nextErr := stream.next2()
				if nextErr != nil {
					fatalf("sed", "%v", nextErr)
					return 1, 0
				}
				if !hasNext {
					// GNU sed (without --posix) keeps the current pattern
					// space and falls to the end of the script instead of
					// quitting without autoprint, as POSIX sed does.
					break cycle
				}
				pattern, terminated = pattern+"\n"+next, nextTerminated
				lineNumber++
				lastLine, err = stream.atEnd()
				if err != nil {
					fatalf("sed", "%v", err)
					return 1, 0
				}
			case 'a':
				rt.appendQueue = append(rt.appendQueue, command.text)
			case 'i':
				fmt.Fprintln(out, command.text)
			case 'c':
				if command.rangeEnding() {
					fmt.Fprintln(out, command.text)
				}
				autoprintSuppressed = true
				break cycle
			case 'r':
				if data, readErr := os.ReadFile(command.file); readErr == nil { //nolint:gosec // G304: sed's r command intentionally reads a script-named file
					rt.appendQueue = append(rt.appendQueue, strings.TrimSuffix(string(data), "\n"))
				}
			case 'R':
				if line, hasLine := rt.nextReadLine(command.file); hasLine {
					rt.appendQueue = append(rt.appendQueue, line)
				}
			case 'w':
				w, werr := rt.writerFor(command.file)
				if werr != nil {
					fatalf("sed", "%v", werr)
					return 1, 0
				}
				fmt.Fprintln(w, pattern)
			case 'W':
				w, werr := rt.writerFor(command.file)
				if werr != nil {
					fatalf("sed", "%v", werr)
					return 1, 0
				}
				first := pattern
				if idx := strings.IndexByte(pattern, '\n'); idx >= 0 {
					first = pattern[:idx]
				}
				fmt.Fprintln(w, first)
			case 'F':
				fmt.Fprintln(out, stream.name)
			case 'z':
				pattern = ""
			case 'l':
				width := listWidth
				if command.hasWrap {
					width = command.wrapWidth
				}
				fmt.Fprintln(out, sedListWrap(sedListEscape(pattern), width))
			case '=':
				fmt.Fprintln(out, lineNumber)
			case 'q':
				quit, quitCode = true, command.exitCode
				break cycle
			case 'Q':
				quit, quitCode, autoprintSuppressed = true, command.exitCode, true
				break cycle
			case 'b':
				if command.jumpTarget < 0 {
					break cycle
				}
				pc = command.jumpTarget
				continue
			case 't':
				if rt.substMade {
					rt.substMade = false
					if command.jumpTarget < 0 {
						break cycle
					}
					pc = command.jumpTarget
					continue
				}
			case 'T':
				if !rt.substMade {
					if command.jumpTarget < 0 {
						break cycle
					}
					pc = command.jumpTarget
					continue
				}
			}
			pc++
		}

		if !autoprintSuppressed && !noDefault {
			if err := writeSedLineWith(out, pattern, terminated, options.separator); err != nil {
				fatalf("sed", "write error: %v", err)
				return 1, 0
			}
		}
		for _, text := range rt.appendQueue {
			fmt.Fprintln(out, text)
		}
		rt.appendQueue = rt.appendQueue[:0]
		if quit {
			return 0, quitCode
		}
		if restart {
			continue
		}
		pattern, terminated, ok, err = stream.next2()
		if err != nil {
			fatalf("sed", "%v", err)
			return 1, 0
		}
	}
	return 0, 0
}

// sedTransliterate applies a y/// command's 1:1 character mapping.
func sedTransliterate(s string, from, to []rune) string {
	var out strings.Builder
	for _, r := range s {
		mapped := r
		for i, f := range from {
			if f == r {
				mapped = to[i]
				break
			}
		}
		out.WriteRune(mapped)
	}
	return out.String()
}

// sedListEscape renders the 'l' command's escaped view of the pattern
// space: control and non-ASCII bytes as their C-style or octal escape, and
// a trailing '$' marking the end of the line (sed's own line-wrap width is
// not reproduced here).
// sedSubstitute applies one s/// command, honouring the occurrence number and
// the g flag: a bare number replaces only that match, and a number with g
// replaces that one and everything after it.
func sedSubstitute(command *sedCommand, pattern string) (string, bool) {
	matches := command.regex.FindAllStringSubmatchIndex(pattern, -1)
	if len(matches) == 0 {
		return pattern, false
	}
	first := command.occurrence
	if first == 0 {
		first = 1
	}
	if first > len(matches) {
		return pattern, false
	}
	last := first
	if command.global {
		last = len(matches)
	}
	var out strings.Builder
	previous := 0
	for i := first - 1; i < last; i++ {
		indices := matches[i]
		out.WriteString(pattern[previous:indices[0]])
		out.Write(command.regex.ExpandString(nil, command.replacement, pattern, indices))
		previous = indices[1]
	}
	out.WriteString(pattern[previous:])
	return out.String(), true
}

// sedListWrap breaks an escaped line the way `l' does. The original checks the
// column before every escaped *byte*, so a multi-byte escape can be split, and
// a width of one wraps before the first byte and leaves an empty first line. A
// width of zero never wraps.
func sedListWrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	var out strings.Builder
	// The closing "$" is written without a column check, as the original does.
	body, end := text[:len(text)-1], text[len(text)-1:]
	position := 0
	for i := 0; i < len(body); i++ {
		if position >= width-1 {
			out.WriteString("\\\n")
			position = 0
		}
		out.WriteByte(body[i])
		position++
	}
	out.WriteString(end)
	return out.String()
}

func sedListEscape(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch b {
		case '\\':
			out.WriteString(`\\`)
		case '\a':
			out.WriteString(`\a`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '\v':
			out.WriteString(`\v`)
		default:
			if b < 32 || b >= 127 {
				fmt.Fprintf(&out, `\%03o`, b)
			} else {
				out.WriteByte(b)
			}
		}
	}
	out.WriteByte('$')
	return out.String()
}

func sedNeedsLastAddress(commands []sedCommand) bool {
	for i := range commands {
		if commands[i].first != nil && commands[i].first.last || commands[i].second != nil && commands[i].second.last {
			return true
		}
	}
	return false
}

// resolveSedLabels turns every b/t/T's label into an index into commands
// (or -1 for "end of script", when the label was omitted), after checking
// every referenced label actually has a matching ':'.
func resolveSedLabels(commands []sedCommand) error {
	targets := map[string]int{}
	for i := range commands {
		if commands[i].kind == ':' {
			targets[commands[i].label] = i
		}
	}
	for i := range commands {
		switch commands[i].kind {
		case 'b', 't', 'T':
			if commands[i].label == "" {
				commands[i].jumpTarget = -1
				continue
			}
			target, ok := targets[commands[i].label]
			if !ok {
				return fmt.Errorf("can't find label for jump to `%s'", commands[i].label)
			}
			commands[i].jumpTarget = target
		}
	}
	return nil
}

// writeSedLineWith ends the line with whichever byte separates them, which -z
// changes to NUL.
func writeSedLineWith(out *bufio.Writer, line string, terminated bool, separator byte) error {
	if _, err := out.WriteString(line); err != nil {
		return err
	}
	if terminated {
		return out.WriteByte(separator)
	}
	return nil
}

// sedStream reads lines across possibly several files as one stream. When
// peekAhead is set (only the scripts that reference '$' need it), it keeps
// a one-line lookahead buffer so atEnd can report whether the current line
// is the last one without consuming it; files are still opened lazily, one
// at a time, so a 'q' on an early line never touches a later, possibly
// unreadable, file.
type sedStream struct {
	files     []string
	fileIndex int
	name      string
	input     io.ReadCloser
	reader    *bufio.Reader

	// separator ends a line: a newline, or NUL under -z, which nullData marks
	// so that a zero byte is not mistaken for "unset".
	separator byte
	nullData  bool

	peekAhead  bool
	primed     bool
	peekedLine sedLine
	peekedOK   bool
	peekedErr  error
}

// next2 returns the next line as (text, terminated, ok, err), the shape
// runSedStream's cycle loop wants; next() itself returns the sedLine struct,
// which is what atEnd's peek buffer stores.
func (s *sedStream) next2() (string, bool, bool, error) {
	line, ok, err := s.next()
	return line.text, line.terminated, ok, err
}

func (s *sedStream) next() (sedLine, bool, error) {
	if s.primed {
		s.primed = false
		return s.peekedLine, s.peekedOK, s.peekedErr
	}
	return s.rawNext()
}

// atEnd reports whether there is no more input after the line already
// returned by next(), priming the lookahead buffer if this script needs it.
// When peekAhead is false, it always reports false (last-line tracking is
// unused, so no read-ahead -- and its file-opening side effect -- happens).
func (s *sedStream) atEnd() (bool, error) {
	if !s.peekAhead {
		return false, nil
	}
	if !s.primed {
		s.peekedLine, s.peekedOK, s.peekedErr = s.rawNext()
		s.primed = true
	}
	if s.peekedErr != nil {
		return false, s.peekedErr
	}
	return !s.peekedOK, nil
}

func (s *sedStream) rawNext() (sedLine, bool, error) {
	for {
		if s.input == nil {
			if s.fileIndex >= len(s.files) {
				return sedLine{}, false, nil
			}
			s.name = s.files[s.fileIndex]
			s.fileIndex++
			input, err := openInput(s.name)
			if err != nil {
				return sedLine{}, false, fmt.Errorf("can't read %s: %s", s.name, errText(err))
			}
			s.input = input
			s.reader = bufio.NewReaderSize(input, 64*1024)
		}
		separator := s.separator
		if separator == 0 && !s.nullData {
			separator = '\n'
		}
		line, readErr := s.reader.ReadString(separator)
		if readErr != nil && readErr != io.EOF {
			_ = s.Close()
			return sedLine{}, false, fmt.Errorf("%s: %w", s.name, readErr)
		}
		if readErr == io.EOF {
			if err := s.Close(); err != nil {
				return sedLine{}, false, fmt.Errorf("%s: %w", s.name, err)
			}
		}
		if len(line) > 0 {
			suffix := string(separator)
			terminated := strings.HasSuffix(line, suffix)
			line = strings.TrimSuffix(line, suffix)
			return sedLine{text: line, terminated: terminated}, true, nil
		}
	}
}

func (s *sedStream) Close() error {
	if s.input == nil {
		return nil
	}
	err := s.input.Close()
	s.input = nil
	s.reader = nil
	return err
}

func parseSedScript(script string, extended bool) ([]sedCommand, error) {
	var commands []sedCommand
	var blockStack []int
	position := 0
	for {
		for position < len(script) && (script[position] == ';' || script[position] == '\n' || script[position] == ' ' || script[position] == '\t') {
			position++
		}
		if position >= len(script) {
			break
		}
		if script[position] == '#' {
			for position < len(script) && script[position] != '\n' {
				position++
			}
			continue
		}
		var command sedCommand
		first, next, present, err := parseSedAddress(script, position, extended)
		if err != nil {
			return nil, err
		}
		if present {
			command.first, position = first, next
			for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
				position++
			}
			if position < len(script) && script[position] == ',' {
				position++
				for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
					position++
				}
				second, after, secondPresent, addressErr := parseSedAddress(script, position, extended)
				if addressErr != nil || !secondPresent {
					return nil, fmt.Errorf("invalid second address")
				}
				command.second, position = second, after
			}
		}
		for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
			position++
		}
		if position < len(script) && script[position] == '!' {
			command.negated, position = true, position+1
			for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
				position++
			}
			if position < len(script) && script[position] == '!' {
				return nil, fmt.Errorf("multiple '!'s")
			}
		}
		if position >= len(script) {
			return nil, fmt.Errorf("missing command")
		}
		command.kind = script[position]
		position++
		switch command.kind {
		case '{':
			blockStack = append(blockStack, len(commands))
		case '}':
			if command.first != nil || command.negated {
				return nil, fmt.Errorf("`}' doesn't want any addresses")
			}
			if len(blockStack) == 0 {
				return nil, fmt.Errorf("unexpected `}'")
			}
		case 'd', 'D', 'p', 'P', 'g', 'G', 'h', 'H', 'x', 'n', 'N', '=', 'F', 'z':
		case 'l':
			// An optional line-wrap width may follow, where zero means never
			// to wrap; without one the width comes from -l, or from the
			// original's default of 70.
			for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
				position++
			}
			start := position
			for position < len(script) && script[position] >= '0' && script[position] <= '9' {
				position++
			}
			if position > start {
				width, convErr := strconv.Atoi(script[start:position])
				if convErr != nil {
					return nil, fmt.Errorf("invalid line-wrap length for `l'")
				}
				command.wrapWidth, command.hasWrap = width, true
			}
		case 'q', 'Q':
			start := position
			for position < len(script) && script[position] >= '0' && script[position] <= '9' {
				position++
			}
			if position > start {
				code, convErr := strconv.Atoi(script[start:position])
				if convErr != nil {
					return nil, fmt.Errorf("invalid exit code for %q", command.kind)
				}
				command.exitCode = code
			}
		case ':':
			label, after := parseSedToken(script, position, true)
			if label == "" {
				return nil, fmt.Errorf("\":\" lacks a label")
			}
			command.label, position = label, after
		case 'b', 't', 'T':
			label, after := parseSedToken(script, position, true)
			command.label, position = label, after
		case 'r', 'R', 'w', 'W':
			for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
				position++
			}
			file, after := parseSedToken(script, position, false)
			if file == "" {
				return nil, fmt.Errorf("missing filename for %q", command.kind)
			}
			command.file, position = file, after
		case 'a', 'i', 'c':
			text, after, err := parseSedText(script, position)
			if err != nil {
				return nil, err
			}
			command.text, position = text, after
		case 'y':
			if position >= len(script) || script[position] == '\n' {
				return nil, fmt.Errorf("missing y delimiter")
			}
			delimiter := script[position]
			position++
			from, after, err := readSedDelimited(script, position, delimiter)
			if err != nil {
				return nil, err
			}
			position = after
			to, after, err := readSedDelimited(script, position, delimiter)
			if err != nil {
				return nil, err
			}
			position = after
			fromRunes, toRunes := []rune(from), []rune(to)
			if len(fromRunes) != len(toRunes) {
				return nil, fmt.Errorf("strings for `y' command are different lengths")
			}
			command.yFrom, command.yTo = fromRunes, toRunes
		case 's':
			if position >= len(script) || script[position] == '\n' {
				return nil, fmt.Errorf("missing substitution delimiter")
			}
			delimiter := script[position]
			position++
			pattern, after, err := readSedDelimited(script, position, delimiter)
			if err != nil {
				return nil, err
			}
			position = after
			replacement, after, err := readSedDelimited(script, position, delimiter)
			if err != nil {
				return nil, err
			}
			position = after
			ignoreCase := false
			for position < len(script) && script[position] != ';' && script[position] != '\n' && script[position] != '}' {
				switch script[position] {
				case 'g':
					command.global = true
				case 'p':
					command.printOnChange = true
				case 'I', 'i':
					ignoreCase = true
				case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
					// A number selects which occurrence to act on; with "g" it
					// is the first of the run to be replaced.
					start := position
					for position < len(script) && script[position] >= '0' && script[position] <= '9' {
						position++
					}
					count, convErr := strconv.Atoi(script[start:position])
					if convErr != nil || count == 0 {
						return nil, fmt.Errorf("number option to `s' command may not be zero")
					}
					command.occurrence = count
					continue
				case ' ', '\t':
				case 'w':
					position++
					for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
						position++
					}
					file, after := parseSedToken(script, position, false)
					if file == "" {
						return nil, fmt.Errorf("missing filename for `s///w'")
					}
					command.writeFile, position = file, after
					continue
				default:
					return nil, fmt.Errorf("unsupported substitution flag %q", script[position])
				}
				position++
			}
			syntax := posixBRE
			if extended {
				syntax = posixERE
			}
			compiled, compileErr := compilePOSIXRegexp(pattern, syntax, ignoreCase)
			if compileErr != nil {
				return nil, fmt.Errorf("invalid regular expression: %w", compileErr)
			}
			command.regex = compiled
			command.replacement = sedReplacement(replacement)
		default:
			return nil, fmt.Errorf("unsupported command %q", command.kind)
		}
		commands = append(commands, command)
		if command.kind == '}' {
			open := blockStack[len(blockStack)-1]
			blockStack = blockStack[:len(blockStack)-1]
			commands[open].jumpTarget = len(commands)
		}
		noSeparatorNeeded := command.kind == '{' || command.kind == 'a' || command.kind == 'i' || command.kind == 'c'
		if !noSeparatorNeeded && position < len(script) && script[position] != ';' && script[position] != '\n' && script[position] != '}' {
			return nil, fmt.Errorf("extra characters after command %q", command.kind)
		}
	}
	if len(blockStack) != 0 {
		return nil, fmt.Errorf("unmatched `{'")
	}
	return commands, nil
}

// parseSedToken reads a bare word (a label for :/b/t/T, or a filename for
// r/R/w/W) up to the next ';' or newline, trimming surrounding whitespace.
// Labels stop additionally at whitespace; filenames run to the end of the
// line since a path may itself contain spaces.
func parseSedToken(script string, position int, stopAtSpace bool) (string, int) {
	start := position
	for position < len(script) && script[position] != ';' && script[position] != '\n' && script[position] != '}' {
		if stopAtSpace && (script[position] == ' ' || script[position] == '\t') {
			break
		}
		position++
	}
	token := strings.TrimSpace(script[start:position])
	if stopAtSpace {
		for position < len(script) && script[position] != ';' && script[position] != '\n' && script[position] != '}' {
			position++
		}
	}
	return token, position
}

// parseSedText reads an a/i/c command's text: the classic "\" followed by
// (optionally backslash-continued) lines, or the GNU one-liner form where
// the text is simply the rest of the current line. Either way, backslash
// escapes in the text are processed (\t, \n, ... and a bare "\x" drops the
// backslash), and a trailing unescaped backslash joins the next raw line
// with a literal newline.
func parseSedText(script string, position int) (string, int, error) {
	for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
		position++
	}
	if position < len(script) && script[position] == '\\' {
		position++
		// If a newline follows, this is the classic "a\" form and the
		// newline itself isn't part of the text. Otherwise it's "a\text on
		// the same line", where leading whitespace from here on is part of
		// the text -- unlike the bare one-liner form's leading whitespace,
		// already skipped above.
		if position < len(script) && script[position] == '\n' {
			position++
		}
	}
	var text strings.Builder
	for {
		lineEnd := strings.IndexByte(script[position:], '\n')
		var raw string
		next := len(script)
		if lineEnd < 0 {
			raw = script[position:]
		} else {
			raw = script[position : position+lineEnd]
			next = position + lineEnd + 1
		}
		if strings.HasSuffix(raw, "\\") && !strings.HasSuffix(raw, "\\\\") {
			text.WriteString(sedUnescapeText(raw[:len(raw)-1]))
			text.WriteByte('\n')
			position = next
			if lineEnd < 0 {
				return "", position, fmt.Errorf("expected more text for `a'/`i'/`c'")
			}
			continue
		}
		text.WriteString(sedUnescapeText(raw))
		position = next
		break
	}
	return text.String(), position, nil
}

// sedUnescapeText applies a/i/c text's backslash escapes: the common
// C-style ones get their special meaning, and a backslash before any other
// character is simply dropped.
func sedUnescapeText(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 't':
			out.WriteByte('\t')
		case 'n':
			out.WriteByte('\n')
		case 'a':
			out.WriteByte('\a')
		case 'f':
			out.WriteByte('\f')
		case 'r':
			out.WriteByte('\r')
		case 'v':
			out.WriteByte('\v')
		case '\\':
			out.WriteByte('\\')
		default:
			out.WriteByte(s[i])
		}
	}
	return out.String()
}

func parseSedAddress(script string, position int, extended bool) (*sedAddress, int, bool, error) {
	if position >= len(script) {
		return nil, position, false, nil
	}
	if script[position] >= '0' && script[position] <= '9' {
		start := position
		for position < len(script) && script[position] >= '0' && script[position] <= '9' {
			position++
		}
		line, err := strconv.Atoi(script[start:position])
		if err != nil || line < 0 {
			return nil, position, false, fmt.Errorf("invalid line address")
		}
		// GNU's step form: "first~step" selects first, first+step, and so on.
		if position < len(script) && script[position] == '~' {
			position++
			stepStart := position
			for position < len(script) && script[position] >= '0' && script[position] <= '9' {
				position++
			}
			step, stepErr := strconv.Atoi(script[stepStart:position])
			if stepErr != nil || step < 0 {
				return nil, position, false, fmt.Errorf("invalid usage of line address 0")
			}
			first := line
			if first == 0 {
				// "0~N" starts at the first multiple of N.
				first = step
			}
			if step == 0 {
				// A step of zero selects the one line, and "0~0" nothing.
				if first == 0 {
					return nil, position, false, fmt.Errorf("invalid usage of line address 0")
				}
				return &sedAddress{line: first}, position, true, nil
			}
			return &sedAddress{line: first, step: step}, position, true, nil
		}
		if line < 1 {
			return nil, position, false, fmt.Errorf("invalid usage of line address 0")
		}
		return &sedAddress{line: line}, position, true, nil
	}
	if script[position] == '$' {
		return &sedAddress{last: true}, position + 1, true, nil
	}
	if script[position] == '/' {
		pattern, after, err := readSedDelimited(script, position+1, '/')
		if err != nil {
			return nil, position, false, err
		}
		syntax := posixBRE
		if extended {
			syntax = posixERE
		}
		compiled, err := compilePOSIXRegexp(pattern, syntax, false)
		if err != nil {
			return nil, position, false, fmt.Errorf("invalid address regular expression: %w", err)
		}
		return &sedAddress{regex: compiled}, after, true, nil
	}
	return nil, position, false, nil
}

func readSedDelimited(script string, position int, delimiter byte) (string, int, error) {
	var value strings.Builder
	for position < len(script) {
		current := script[position]
		position++
		if current == delimiter {
			return value.String(), position, nil
		}
		if current == '\\' && position < len(script) {
			next := script[position]
			if next == delimiter {
				value.WriteByte(next)
				position++
				continue
			}
			value.WriteByte(current)
			continue
		}
		value.WriteByte(current)
	}
	return "", position, fmt.Errorf("unterminated expression")
}

func sedReplacement(value string) string {
	var result strings.Builder
	for i := 0; i < len(value); i++ {
		switch {
		case value[i] == '&':
			result.WriteString("${0}")
		case value[i] == '$':
			result.WriteString("$$")
		case value[i] == '\\' && i+1 < len(value) && value[i+1] >= '0' && value[i+1] <= '9':
			i++
			result.WriteString("${")
			result.WriteByte(value[i])
			result.WriteByte('}')
		case value[i] == '\\' && i+1 < len(value) && value[i+1] == '&':
			i++
			result.WriteByte('&')
		default:
			result.WriteByte(value[i])
		}
	}
	return result.String()
}
