// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

type nanoOptions struct {
	filename       string
	startLine      int
	startCol       int
	backup         bool
	backupDir      string
	boldText       bool
	tabToSpaces    bool
	newBuffer      bool
	locking        bool
	historyLog     bool
	ignoreRcFiles  bool
	guideStripe    int
	rawSequences   bool
	noNewlines     bool
	trimBlanks     bool
	noConvert      bool
	bookStyle      bool
	positionLog    bool
	quoteStr       string
	restricted     bool
	softWrap       bool
	tabSize        int
	quickBlank     bool
	wordBounds     bool
	wordChars      string
	syntax         string
	zap            bool
	atBlanks       bool
	breakLongLines bool
	constantShow   bool
	rebindDelete   bool
	emptyLine      bool
	rcFile         string
	showCursor     bool
	autoIndent     bool
	jumpyScrolling bool
	cutFromCursor  bool
	lineNumbers    bool
	mouse          bool
	noRead         bool
	operatingDir   string
	preserve       bool
	indicator      bool
	fill           int
	speller        string
	saveOnExit     bool
	unix           bool
	viewMode       bool
	noWrap         bool
	noHelp         bool
	afterEnds      bool
	listSyntaxes   bool
	zero           bool
	soloSideScroll bool
	smartHome      bool
}

func parseNanoOptions(args []string) (nanoOptions, error) {
	opts := nanoOptions{tabSize: 8}
	var operands []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "+") {
			pos := strings.TrimPrefix(arg, "+")
			pos = strings.Trim(pos, "[]")
			if l, c, ok := strings.Cut(pos, ","); ok {
				if line, err := strconv.Atoi(l); err == nil && line > 0 {
					opts.startLine = line
				}
				if col, err := strconv.Atoi(c); err == nil && col > 0 {
					opts.startCol = col
				}
			} else if line, err := strconv.Atoi(pos); err == nil && line > 0 {
				opts.startLine = line
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			operands = append(operands, arg)
			continue
		}

		next := func() (string, bool) {
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}

		switch {
		case arg == "-A" || arg == "--smarthome":
			opts.smartHome = true
		case arg == "-B" || arg == "--backup":
			opts.backup = true
		case arg == "-C" || arg == "--backupdir" || strings.HasPrefix(arg, "--backupdir="):
			v := ""
			if strings.HasPrefix(arg, "--backupdir=") {
				v = strings.TrimPrefix(arg, "--backupdir=")
			} else if val, ok := next(); ok {
				v = val
			} else {
				return opts, fmt.Errorf("option '%s' requires an argument", arg)
			}
			opts.backupDir = v
		case arg == "-D" || arg == "--boldtext":
			opts.boldText = true
		case arg == "-E" || arg == "--tabstospaces":
			opts.tabToSpaces = true
		case arg == "-F" || arg == "--newbuffer":
			opts.newBuffer = true
		case arg == "-G" || arg == "--locking":
			opts.locking = true
		case arg == "-H" || arg == "--historylog":
			opts.historyLog = true
		case arg == "-I" || arg == "--ignorercfiles":
			opts.ignoreRcFiles = true
		case arg == "-J" || arg == "--guidestripe" || strings.HasPrefix(arg, "--guidestripe="):
			v := ""
			if strings.HasPrefix(arg, "--guidestripe=") {
				v = strings.TrimPrefix(arg, "--guidestripe=")
			} else if val, ok := next(); ok {
				v = val
			} else {
				return opts, fmt.Errorf("option '%s' requires an argument", arg)
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, fmt.Errorf("invalid guidestripe '%s'", v)
			}
			opts.guideStripe = n
		case arg == "-K" || arg == "--rawsequences":
			opts.rawSequences = true
		case arg == "-L" || arg == "--nonewlines":
			opts.noNewlines = true
		case arg == "-M" || arg == "--trimblanks":
			opts.trimBlanks = true
		case arg == "-N" || arg == "--noconvert":
			opts.noConvert = true
		case arg == "-O" || arg == "--bookstyle":
			opts.bookStyle = true
		case arg == "-P" || arg == "--positionlog":
			opts.positionLog = true
		case arg == "-Q" || arg == "--quotestr" || strings.HasPrefix(arg, "--quotestr="):
			v := ""
			if strings.HasPrefix(arg, "--quotestr=") {
				v = strings.TrimPrefix(arg, "--quotestr=")
			} else if val, ok := next(); ok {
				v = val
			} else {
				return opts, fmt.Errorf("option '%s' requires an argument", arg)
			}
			opts.quoteStr = v
		case arg == "-R" || arg == "--restricted":
			opts.restricted = true
		case arg == "-S" || arg == "--softwrap":
			opts.softWrap = true
		case arg == "-T" || arg == "--tabsize" || strings.HasPrefix(arg, "--tabsize="):
			v := ""
			if strings.HasPrefix(arg, "--tabsize=") {
				v = strings.TrimPrefix(arg, "--tabsize=")
			} else if val, ok := next(); ok {
				v = val
			} else {
				return opts, fmt.Errorf("option '%s' requires an argument", arg)
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return opts, fmt.Errorf("invalid tabsize '%s'", v)
			}
			opts.tabSize = n
		case arg == "-U" || arg == "--quickblank":
			opts.quickBlank = true
		case arg == "-W" || arg == "--wordbounds":
			opts.wordBounds = true
		case arg == "-X" || arg == "--wordchars" || strings.HasPrefix(arg, "--wordchars="):
			v := ""
			if strings.HasPrefix(arg, "--wordchars=") {
				v = strings.TrimPrefix(arg, "--wordchars=")
			} else if val, ok := next(); ok {
				v = val
			} else {
				return opts, fmt.Errorf("option '%s' requires an argument", arg)
			}
			opts.wordChars = v
		case arg == "-Y" || arg == "--syntax" || strings.HasPrefix(arg, "--syntax="):
			v := ""
			if strings.HasPrefix(arg, "--syntax=") {
				v = strings.TrimPrefix(arg, "--syntax=")
			} else if val, ok := next(); ok {
				v = val
			} else {
				return opts, fmt.Errorf("option '%s' requires an argument", arg)
			}
			opts.syntax = v
		case arg == "-Z" || arg == "--zap":
			opts.zap = true
		case arg == "-a" || arg == "--atblanks":
			opts.atBlanks = true
		case arg == "-b" || arg == "--breaklonglines":
			opts.breakLongLines = true
		case arg == "-c" || arg == "--constantshow":
			opts.constantShow = true
		case arg == "-d" || arg == "--rebinddelete":
			opts.rebindDelete = true
		case arg == "-e" || arg == "--emptyline":
			opts.emptyLine = true
		case arg == "-f" || arg == "--rcfile" || strings.HasPrefix(arg, "--rcfile="):
			v := ""
			if strings.HasPrefix(arg, "--rcfile=") {
				v = strings.TrimPrefix(arg, "--rcfile=")
			} else if val, ok := next(); ok {
				v = val
			} else {
				return opts, fmt.Errorf("option '%s' requires an argument", arg)
			}
			opts.rcFile = v
		case arg == "-g" || arg == "--showcursor":
			opts.showCursor = true
		case arg == "-i" || arg == "--autoindent":
			opts.autoIndent = true
		case arg == "-j" || arg == "--jumpyscrolling":
			opts.jumpyScrolling = true
		case arg == "-k" || arg == "--cutfromcursor":
			opts.cutFromCursor = true
		case arg == "-l" || arg == "--linenumbers":
			opts.lineNumbers = true
		case arg == "-m" || arg == "--mouse":
			opts.mouse = true
		case arg == "-n" || arg == "--noread":
			opts.noRead = true
		case arg == "-o" || arg == "--operatingdir" || strings.HasPrefix(arg, "--operatingdir="):
			v := ""
			if strings.HasPrefix(arg, "--operatingdir=") {
				v = strings.TrimPrefix(arg, "--operatingdir=")
			} else if val, ok := next(); ok {
				v = val
			} else {
				return opts, fmt.Errorf("option '%s' requires an argument", arg)
			}
			opts.operatingDir = v
		case arg == "-p" || arg == "--preserve":
			opts.preserve = true
		case arg == "-q" || arg == "--indicator":
			opts.indicator = true
		case arg == "-r" || arg == "--fill" || strings.HasPrefix(arg, "--fill="):
			v := ""
			if strings.HasPrefix(arg, "--fill=") {
				v = strings.TrimPrefix(arg, "--fill=")
			} else if val, ok := next(); ok {
				v = val
			} else {
				return opts, fmt.Errorf("option '%s' requires an argument", arg)
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, fmt.Errorf("invalid fill '%s'", v)
			}
			opts.fill = n
		case arg == "-s" || arg == "--speller" || strings.HasPrefix(arg, "--speller="):
			v := ""
			if strings.HasPrefix(arg, "--speller=") {
				v = strings.TrimPrefix(arg, "--speller=")
			} else if val, ok := next(); ok {
				v = val
			} else {
				return opts, fmt.Errorf("option '%s' requires an argument", arg)
			}
			opts.speller = v
		case arg == "-t" || arg == "--saveonexit":
			opts.saveOnExit = true
		case arg == "-u" || arg == "--unix":
			opts.unix = true
		case arg == "-v" || arg == "--view":
			opts.viewMode = true
		case arg == "-w" || arg == "--nowrap":
			opts.noWrap = true
		case arg == "-x" || arg == "--nohelp":
			opts.noHelp = true
		case arg == "-y" || arg == "--afterends":
			opts.afterEnds = true
		case arg == "-z" || arg == "--listsyntaxes":
			opts.listSyntaxes = true
		case arg == "--zero":
			opts.zero = true
		case arg == "--solosidescroll":
			opts.soloSideScroll = true
		default:
			if strings.HasPrefix(arg, "-") && len(arg) > 2 && !strings.HasPrefix(arg, "--") {
				for _, ch := range arg[1:] {
					switch ch {
					case 'A':
						opts.smartHome = true
					case 'B':
						opts.backup = true
					case 'D':
						opts.boldText = true
					case 'E':
						opts.tabToSpaces = true
					case 'F':
						opts.newBuffer = true
					case 'G':
						opts.locking = true
					case 'H':
						opts.historyLog = true
					case 'I':
						opts.ignoreRcFiles = true
					case 'K':
						opts.rawSequences = true
					case 'L':
						opts.noNewlines = true
					case 'M':
						opts.trimBlanks = true
					case 'N':
						opts.noConvert = true
					case 'O':
						opts.bookStyle = true
					case 'P':
						opts.positionLog = true
					case 'R':
						opts.restricted = true
					case 'S':
						opts.softWrap = true
					case 'U':
						opts.quickBlank = true
					case 'W':
						opts.wordBounds = true
					case 'Z':
						opts.zap = true
					case 'a':
						opts.atBlanks = true
					case 'b':
						opts.breakLongLines = true
					case 'c':
						opts.constantShow = true
					case 'd':
						opts.rebindDelete = true
					case 'e':
						opts.emptyLine = true
					case 'g':
						opts.showCursor = true
					case 'i':
						opts.autoIndent = true
					case 'j':
						opts.jumpyScrolling = true
					case 'k':
						opts.cutFromCursor = true
					case 'l':
						opts.lineNumbers = true
					case 'm':
						opts.mouse = true
					case 'n':
						opts.noRead = true
					case 'p':
						opts.preserve = true
					case 'q':
						opts.indicator = true
					case 't':
						opts.saveOnExit = true
					case 'u':
						opts.unix = true
					case 'v':
						opts.viewMode = true
					case 'w':
						opts.noWrap = true
					case 'x':
						opts.noHelp = true
					case 'y':
						opts.afterEnds = true
					case 'z':
						opts.listSyntaxes = true
					default:
						return opts, fmt.Errorf("unknown option '-%c'", ch)
					}
				}
				continue
			}
			return opts, fmt.Errorf("unknown option '%s'", arg)
		}
	}
	if len(operands) > 1 {
		return opts, fmt.Errorf("too many operands")
	}
	if len(operands) == 1 {
		opts.filename = operands[0]
	}
	return opts, nil
}

type miniEditor struct {
	lines                          [][]byte
	row, col, rowOffset, colOffset int
	rows, cols                     int
	filename, message              string
	dirty                          bool
	lastSearch                     string
	cutBuffer                      []byte
	haveCut                        bool
	opts                           nanoOptions
}

func cmdNano(args []string) int {
	opts, err := parseNanoOptions(args)
	if err != nil {
		fatalf("nano", "%v", err)
		return 1
	}
	if opts.listSyntaxes {
		fmt.Println("Available syntaxes: none (plain text)")
		return 0
	}
	editor := newMiniEditorWithOptions(opts)
	if err := editor.run(); err != nil {
		fatalf("nano", "%v", err)
		return 1
	}
	return 0
}

func newMiniEditor(name string) *miniEditor {
	return newMiniEditorWithOptions(nanoOptions{filename: name, tabSize: 8})
}

func newMiniEditorWithOptions(opts nanoOptions) *miniEditor {
	if opts.tabSize <= 0 {
		opts.tabSize = 8
	}
	e := &miniEditor{
		filename: opts.filename,
		message:  "^G Help  ^O Save  ^X Exit  ^F Search  ^\\ Replace  ^K Cut  ^U Paste",
		rows:     24,
		cols:     80,
		opts:     opts,
	}
	if opts.filename != "" {
		if data, err := os.ReadFile(opts.filename); err == nil {
			parts := bytes.Split(data, []byte{'\n'})
			if len(parts) > 1 && len(parts[len(parts)-1]) == 0 {
				parts = parts[:len(parts)-1]
			}
			e.lines = parts
		} else if !os.IsNotExist(err) {
			e.message = err.Error()
		}
	}
	if len(e.lines) == 0 {
		e.lines = [][]byte{{}}
	}
	if opts.startLine > 0 {
		e.goToLine(opts.startLine, opts.startCol)
	}
	return e
}
func (e *miniEditor) run() error {
	fd := os.Stdin.Fd()
	old, err := terminalRaw(fd)
	if err != nil {
		return fmt.Errorf("stdin is not a terminal: %w", err)
	}
	defer restoreTerminal(fd, old)
	if r, c, ok := terminalDimensions(fd); ok {
		e.rows, e.cols = r, c
	}
	defer fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")
	for {
		e.refresh()
		key, err := readEditorKey()
		if err != nil {
			return err
		}
		switch key {
		case 24: // ^X Exit
			if e.dirty {
				if e.opts.saveOnExit && !e.opts.viewMode && !e.opts.restricted {
					if err := e.save(); err != nil {
						continue
					}
					return nil
				}
				switch e.confirmPrompt("Save modified buffer? ") {
				case 'y':
					if !e.saveInteractive() {
						continue
					}
				case 'c':
					continue
				}
			}
			return nil
		case 15: // ^O Write Out
			if e.opts.viewMode || e.opts.restricted {
				e.message = "File is read-only"
			} else {
				e.saveInteractive()
			}
		case 11: // ^K Cut
			if e.opts.viewMode {
				e.message = "Key is invalid in view mode"
			} else {
				e.cutLine()
			}
		case 21: // ^U Paste
			if e.opts.viewMode {
				e.message = "Key is invalid in view mode"
			} else {
				e.paste()
			}
		case 6: // ^F Where Is (search)
			e.searchInteractive()
		case 28: // ^\ Replace
			if e.opts.viewMode {
				e.message = "Key is invalid in view mode"
			} else {
				e.replaceInteractive()
			}
		case 3: // ^C show cursor position
			e.message = fmt.Sprintf("line %d/%d, col %d", e.row+1, len(e.lines), e.col+1)
		case 31: // ^_ / ^/ Go To Line
			e.goToLineInteractive()
		case 7: // ^G Help
			e.message = "^O Save  ^X Exit  ^F Search  ^\\ Replace  ^K Cut  ^U Paste  ^_ Go To Line  ^C Position"
		default:
			e.handleKey(key)
		}
	}
}

// confirmPrompt shows a yes/no/cancel question on the message line and
// returns 'y', 'n', or 'c', the way nano's own exit confirmation does.
func (e *miniEditor) confirmPrompt(label string) byte {
	e.message = label + "(Y)es, (N)o, (^C) Cancel"
	e.refresh()
	key, err := readEditorKey()
	if err != nil {
		return 'c'
	}
	switch key {
	case 'y', 'Y':
		return 'y'
	case 'n', 'N':
		return 'n'
	default:
		return 'c'
	}
}

// shellPromptResult is one answer from prompt: the text entered, and
// whether it was confirmed with Enter rather than cancelled.
type shellPromptResult struct {
	text      string
	confirmed bool
}

// prompt draws label followed by an editable line seeded with initial on
// the message line, and reads keys until Enter (confirmed) or ^C/Esc
// (cancelled).
func (e *miniEditor) prompt(label, initial string) shellPromptResult {
	input := []byte(initial)
	for {
		e.message = label + string(input)
		e.refresh()
		height := e.rows - 2
		col := len(label) + len(input) + 1
		if col > e.cols {
			col = e.cols
		}
		fmt.Fprintf(os.Stdout, "\x1b[%d;%dH", height+2, col)
		key, err := readEditorKey()
		if err != nil {
			return shellPromptResult{"", false}
		}
		switch key {
		case '\r', '\n':
			return shellPromptResult{string(input), true}
		case 3, 27:
			return shellPromptResult{"", false}
		case 127, 8:
			if len(input) > 0 {
				input = input[:len(input)-1]
			}
		default:
			if key >= 32 && key < 127 {
				input = append(input, byte(key))
			}
		}
	}
}

// saveInteractive prompts for a filename (seeded with the current one, as
// nano's own ^O does) and saves to it, returning whether it succeeded.
func (e *miniEditor) saveInteractive() bool {
	result := e.prompt("File Name to Write: ", e.filename)
	if !result.confirmed || result.text == "" {
		e.message = "Cancelled"
		return false
	}
	e.filename = result.text
	return e.save() == nil
}

// searchInteractive prompts for a search string (seeded with the last one
// searched, so pressing Enter alone repeats it) and jumps to the next match.
func (e *miniEditor) searchInteractive() {
	result := e.prompt("Search: ", e.lastSearch)
	if !result.confirmed || result.text == "" {
		return
	}
	e.lastSearch = result.text
	if e.find(result.text) {
		e.message = "Found"
	} else {
		e.message = fmt.Sprintf("%q not found", result.text)
	}
}

// replaceInteractive prompts for a search string and a replacement, then
// replaces every occurrence in the buffer. Real nano confirms each match
// individually (Y/N/A); this always acts as if "All" were chosen, which is
// simpler to reason about safely in a rescue tool and is documented as such.
func (e *miniEditor) replaceInteractive() {
	search := e.prompt("Search (to replace): ", e.lastSearch)
	if !search.confirmed || search.text == "" {
		return
	}
	e.lastSearch = search.text
	replacement := e.prompt("Replace with: ", "")
	if !replacement.confirmed {
		return
	}
	count := e.replaceAll(search.text, replacement.text)
	e.message = fmt.Sprintf("Replaced %d occurrence(s)", count)
}

// goToLineInteractive implements ^_ / ^/: jump the cursor to a 1-indexed
// line (and, if given as "line,column", a column too).
func (e *miniEditor) goToLineInteractive() {
	result := e.prompt("Enter line number, column number: ", "")
	if !result.confirmed || result.text == "" {
		return
	}
	linePart, colPart, _ := strings.Cut(result.text, ",")
	line, err := strconv.Atoi(strings.TrimSpace(linePart))
	if err != nil || line < 1 {
		e.message = "Invalid line number"
		return
	}
	column := 0
	if colPart != "" {
		if c, err := strconv.Atoi(strings.TrimSpace(colPart)); err == nil && c > 0 {
			column = c
		}
	}
	e.goToLine(line, column)
}

// goToLine moves the cursor to 1-indexed line and, if column > 0, to that
// 1-indexed column too, clamping both to the buffer's actual bounds.
func (e *miniEditor) goToLine(line, column int) {
	if line > len(e.lines) {
		line = len(e.lines)
	}
	if line < 1 {
		line = 1
	}
	e.row = line - 1
	e.col = 0
	if column > 0 {
		e.col = column - 1
		if e.col > len(e.lines[e.row]) {
			e.col = len(e.lines[e.row])
		}
	}
	e.scroll()
}
func (e *miniEditor) handleKey(key int) {
	if e.opts.viewMode {
		switch key {
		case 1000:
			if e.row > 0 {
				e.row--
				if e.col > len(e.lines[e.row]) {
					e.col = len(e.lines[e.row])
				}
			}
		case 1001:
			if e.row+1 < len(e.lines) {
				e.row++
				if e.col > len(e.lines[e.row]) {
					e.col = len(e.lines[e.row])
				}
			}
		case 1002:
			if e.col > 0 {
				e.col--
			} else if e.row > 0 {
				e.row--
				e.col = len(e.lines[e.row])
			}
		case 1003:
			if e.col < len(e.lines[e.row]) {
				e.col++
			} else if e.row+1 < len(e.lines) {
				e.row++
				e.col = 0
			}
		case 1004:
			e.col = 0
		case 1005:
			e.col = len(e.lines[e.row])
		case 1006:
			e.row -= e.rows - 2
			if e.row < 0 {
				e.row = 0
			}
		case 1007:
			e.row += e.rows - 2
			if e.row >= len(e.lines) {
				e.row = len(e.lines) - 1
			}
		default:
			if key >= 32 && key < 127 || key == '\r' || key == '\n' || key == 127 || key == 8 || key == '\t' {
				e.message = "Key is invalid in view mode"
			}
		}
		e.scroll()
		return
	}

	switch key {
	case 1000:
		if e.row > 0 {
			e.row--
			if e.col > len(e.lines[e.row]) {
				e.col = len(e.lines[e.row])
			}
		}
	case 1001:
		if e.row+1 < len(e.lines) {
			e.row++
			if e.col > len(e.lines[e.row]) {
				e.col = len(e.lines[e.row])
			}
		}
	case 1002:
		if e.col > 0 {
			e.col--
		} else if e.row > 0 {
			e.row--
			e.col = len(e.lines[e.row])
		}
	case 1003:
		if e.col < len(e.lines[e.row]) {
			e.col++
		} else if e.row+1 < len(e.lines) {
			e.row++
			e.col = 0
		}
	case 1004:
		e.col = 0
	case 1005:
		e.col = len(e.lines[e.row])
	case 1006:
		e.row -= e.rows - 2
		if e.row < 0 {
			e.row = 0
		}
	case 1007:
		e.row += e.rows - 2
		if e.row >= len(e.lines) {
			e.row = len(e.lines) - 1
		}
	case 127, 8:
		e.backspace()
	case '\r', '\n':
		e.newline()
	case '\t':
		if e.opts.tabToSpaces {
			ts := e.opts.tabSize
			if ts <= 0 {
				ts = 8
			}
			for s := 0; s < ts; s++ {
				e.insertByte(' ')
			}
		} else {
			e.insertByte('\t')
		}
	default:
		if key >= 32 && key < 127 {
			e.insertByte(byte(key))
		}
	}
	e.scroll()
}

func (e *miniEditor) insertByte(b byte) {
	line := e.lines[e.row]
	line = append(line, 0)
	copy(line[e.col+1:], line[e.col:])
	line[e.col] = b
	e.lines[e.row] = line
	e.col++
	e.dirty = true
}

func (e *miniEditor) backspace() {
	if e.col > 0 {
		line := e.lines[e.row]
		e.lines[e.row] = append(line[:e.col-1], line[e.col:]...)
		e.col--
		e.dirty = true
	} else if e.row > 0 {
		previous := len(e.lines[e.row-1])
		e.lines[e.row-1] = append(e.lines[e.row-1], e.lines[e.row]...)
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
		e.row--
		e.col = previous
		e.dirty = true
	}
}

func (e *miniEditor) newline() {
	line := e.lines[e.row]
	left := append([]byte{}, line[:e.col]...)
	right := append([]byte{}, line[e.col:]...)
	var indent []byte
	if e.opts.autoIndent {
		for _, b := range line {
			if b == ' ' || b == '\t' {
				indent = append(indent, b)
			} else {
				break
			}
		}
	}
	e.lines[e.row] = left
	newLine := append(indent, right...)
	e.lines = append(e.lines, nil)
	copy(e.lines[e.row+2:], e.lines[e.row+1:])
	e.lines[e.row+1] = newLine
	e.row++
	e.col = len(indent)
	e.dirty = true
}

func (e *miniEditor) cutLine() {
	if e.opts.cutFromCursor {
		line := e.lines[e.row]
		if e.col < len(line) {
			e.cutBuffer = append([]byte{}, line[e.col:]...)
			e.lines[e.row] = append([]byte{}, line[:e.col]...)
			e.haveCut = true
			e.dirty = true
			e.scroll()
			return
		}
		if e.row+1 < len(e.lines) {
			e.cutBuffer = []byte{'\n'}
			e.lines[e.row] = append(e.lines[e.row], e.lines[e.row+1]...)
			e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
			e.haveCut = true
			e.dirty = true
			e.scroll()
			return
		}
	}
	e.cutBuffer = append([]byte{}, e.lines[e.row]...)
	e.haveCut = true
	if len(e.lines) == 1 {
		e.lines[0] = nil
		e.col = 0
	} else {
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
		if e.row >= len(e.lines) {
			e.row = len(e.lines) - 1
		}
		if e.col > len(e.lines[e.row]) {
			e.col = len(e.lines[e.row])
		}
	}
	e.dirty = true
	e.scroll()
}

// paste inserts the last cut line above the cursor's current line, the way
// nano's ^U un-cuts (only single-line, not nano's multi-line cut
// accumulation from repeated ^K -- a documented simplification).
func (e *miniEditor) paste() {
	if !e.haveCut {
		return
	}
	e.lines = append(e.lines, nil)
	copy(e.lines[e.row+1:], e.lines[e.row:])
	e.lines[e.row] = append([]byte{}, e.cutBuffer...)
	e.row++
	e.col = 0
	e.dirty = true
	e.scroll()
}

// find searches for query starting just after the cursor (forward only --
// nano's M-B backward search is a documented gap), wrapping around the
// whole buffer once. On a match it moves the cursor there and returns true.
func (e *miniEditor) find(query string) bool {
	if query == "" || len(e.lines) == 0 {
		return false
	}
	n := len(e.lines)
	for offset := 0; offset <= n; offset++ {
		row := (e.row + offset) % n
		line := string(e.lines[row])
		from := 0
		if offset == 0 {
			from = e.col + 1
		}
		if from > len(line) {
			continue
		}
		if idx := strings.Index(line[from:], query); idx >= 0 {
			e.row, e.col = row, from+idx
			e.scroll()
			return true
		}
	}
	return false
}

// replaceAll substitutes every occurrence of from with to across the whole
// buffer and reports how many it made.
func (e *miniEditor) replaceAll(from, to string) int {
	if from == "" {
		return 0
	}
	count := 0
	for i, line := range e.lines {
		text := string(line)
		if n := strings.Count(text, from); n > 0 {
			e.lines[i] = []byte(strings.ReplaceAll(text, from, to))
			count += n
		}
	}
	if count > 0 {
		e.dirty = true
	}
	return count
}

func (e *miniEditor) save() error {
	if e.filename == "" {
		e.message = "Cannot save: start nano with a filename"
		return fmt.Errorf("no filename")
	}
	if e.opts.backup {
		backupPath := e.filename + "~"
		if e.opts.backupDir != "" {
			_ = os.MkdirAll(e.opts.backupDir, 0o750)
			backupPath = filepath.Join(e.opts.backupDir, filepath.Base(e.filename)+"~")
		}
		if origData, err := os.ReadFile(e.filename); err == nil {
			_ = os.WriteFile(backupPath, origData, 0o666) //nolint:gosec // G306: backup honors umask
		}
	}
	data := bytes.Join(e.lines, []byte{'\n'})
	if !e.opts.noNewlines {
		data = append(data, '\n')
	}
	if err := os.WriteFile(e.filename, data, 0o666); err != nil { //nolint:gosec // G306: editor-created files honor the caller's umask.
		e.message = "Save failed: " + err.Error()
		return err
	}
	e.dirty = false
	e.message = fmt.Sprintf("Wrote %d lines", len(e.lines))
	return nil
}

func (e *miniEditor) scroll() {
	height := e.rows - 2
	if e.row < e.rowOffset {
		e.rowOffset = e.row
	}
	if e.row >= e.rowOffset+height {
		e.rowOffset = e.row - height + 1
	}
	margin := 0
	if e.opts.lineNumbers {
		margin = 5
	}
	textCols := e.cols - margin
	if textCols < 1 {
		textCols = 1
	}
	if e.col < e.colOffset {
		e.colOffset = e.col
	}
	if e.col >= e.colOffset+textCols {
		e.colOffset = e.col - textCols + 1
	}
}

func (e *miniEditor) refresh() {
	var out strings.Builder
	// Disable autowrap while painting. Filling the last terminal column and then
	// writing CRLF can otherwise wrap twice and scroll the first editor rows off
	// the top of a narrow terminal.
	out.WriteString("\x1b[?25l\x1b[?7l")
	height := e.rows - 2
	if height < 1 {
		height = 1
	}
	tabW := e.opts.tabSize
	if tabW <= 0 {
		tabW = 8
	}
	tabSpaces := strings.Repeat(" ", tabW)

	margin := 0
	if e.opts.lineNumbers {
		margin = 5
	}
	textCols := e.cols - margin
	if textCols < 1 {
		textCols = 1
	}

	for y := 0; y < height; y++ {
		fmt.Fprintf(&out, "\x1b[%d;1H", y+1)
		index := e.rowOffset + y
		if index < len(e.lines) {
			if e.opts.lineNumbers {
				fmt.Fprintf(&out, "\x1b[33m%4d\x1b[m ", index+1)
			}
			line := e.lines[index]
			start := e.colOffset
			if start > len(line) {
				start = len(line)
			}
			end := start + textCols
			if end > len(line) {
				end = len(line)
			}
			for _, b := range line[start:end] {
				if b == '\t' {
					out.WriteString(tabSpaces)
				} else if b >= 32 {
					out.WriteByte(b)
				}
			}
		} else {
			if e.opts.lineNumbers {
				out.WriteString("     ")
			}
			out.WriteByte('~')
		}
		out.WriteString("\x1b[K")
	}
	title := " ba6 nano "
	if e.filename != "" {
		title += "- " + e.filename
	}
	if e.opts.viewMode {
		title += " [view mode]"
	}
	if e.dirty {
		title += " [modified]"
	}
	if e.opts.constantShow {
		title += fmt.Sprintf(" [line %d/%d, col %d]", e.row+1, len(e.lines), e.col+1)
	}
	if len(title) > e.cols {
		title = title[:e.cols]
	}
	fmt.Fprintf(&out, "\x1b[%d;1H", height+1)
	out.WriteString("\x1b[7m" + title + strings.Repeat(" ", maxInt(0, e.cols-len(title))) + "\x1b[m")
	message := e.message
	if len(message) > e.cols {
		message = message[:e.cols]
	}
	fmt.Fprintf(&out, "\x1b[%d;1H", height+2)
	out.WriteString(message + "\x1b[K")
	cursorRow := e.row - e.rowOffset + 1
	cursorCol := e.col - e.colOffset + 1 + margin
	fmt.Fprintf(&out, "\x1b[%d;%dH\x1b[?7h\x1b[?25h", cursorRow, cursorCol)
	fmt.Fprint(os.Stdout, out.String())
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func terminalRaw(fd uintptr) (*syscall.Termios, error) {
	old := new(syscall.Termios)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(old))); errno != 0 { //nolint:gosec // G103: fixed Termios buffer for TCGETS.
		return nil, errno
	}
	raw := *old
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&raw))); errno != 0 { //nolint:gosec // G103: fixed Termios buffer for TCSETS.
		return nil, errno
	}
	return old, nil
}
func restoreTerminal(fd uintptr, old *syscall.Termios) {
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(old))) //nolint:gosec // G103: restoring the previously read Termios value.
}
func terminalDimensions(fd uintptr) (int, int, bool) {
	var ws winsize
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); errno != 0 { //nolint:gosec // G103: fixed winsize buffer for TIOCGWINSZ.
		return 0, 0, false
	}
	return int(ws.rows), int(ws.cols), ws.rows > 2 && ws.cols > 0
}
func readEditorKey() (int, error) {
	return readKeyFrom(os.Stdin)
}

// readKeyFrom reads one key press from a terminal in raw mode. Arrow, page and
// home/end keys arrive as escape sequences and are reported as codes above
// 1000. The source is a parameter because a pager reading its data from a pipe
// must take its keys from /dev/tty instead of standard input.
func readKeyFrom(input *os.File) (int, error) {
	one := []byte{0}
	if _, err := input.Read(one); err != nil {
		return 0, err
	}
	if one[0] != 27 {
		return int(one[0]), nil
	}
	seq := make([]byte, 1)
	if _, err := input.Read(seq); err != nil {
		return 27, nil
	}
	if seq[0] != '[' && seq[0] != 'O' {
		return 27, nil
	}
	if _, err := input.Read(seq); err != nil {
		return 27, nil
	}
	switch seq[0] {
	case 'A':
		return 1000, nil
	case 'B':
		return 1001, nil
	case 'D':
		return 1002, nil
	case 'C':
		return 1003, nil
	case 'H':
		return 1004, nil
	case 'F':
		return 1005, nil
	case '5', '6':
		number := seq[0]
		if _, err := input.Read(seq); err != nil {
			return 27, nil
		}
		if number == '5' {
			return 1006, nil
		}
		return 1007, nil
	}
	return 27, nil
}
