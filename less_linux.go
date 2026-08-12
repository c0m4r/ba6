// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// lessNumberWidth is the width of the -N line-number column, digits and the
// separating space together.
const lessNumberWidth = 8

// lessOptions is one less(1) command line.
type lessOptions struct {
	lineNumbers   bool // -N
	chop          bool // -S
	ignoreCase    bool // -i
	ignoreCaseAll bool // -I
	quitOneScreen bool // -F
	quitAtEOF     bool // -e and -E
	noInit        bool // -X
	squeeze       bool // -s
	raw           bool // -r and -R
	prompt        int  // 0 plain, 1 for -m, 2 for -M
	tabWidth      int  // -x
	window        int  // -z, 0 means one screen
	commands      []string
}

// pagerFile is one loaded input. The whole file is held in memory: it makes
// backward movement, percentages and searching exact, at the cost of not being
// able to page a file larger than memory.
type pagerFile struct {
	name   string
	lines  [][]byte
	starts []int // byte offset of every line, plus the total size at the end
}

func (f *pagerFile) size() int {
	if len(f.starts) == 0 {
		return 0
	}
	return f.starts[len(f.starts)-1]
}

// pager is the full-screen display: which file is open, where the view sits in
// it, and the last search.
type pager struct {
	opts        lessOptions
	names       []string
	index       int
	file        pagerFile
	top, left   int
	rows, cols  int
	count       int  // the numeric prefix typed before a command
	firstScreen bool // the status line names the file until the view moves
	message     string
	pattern     *regexp.Regexp
	patternText string
	backward    bool
	keys        *os.File
	help        bool
	quit        bool
}

func cmdLess(args []string) int {
	opts, names, err := parseLessOptions(args)
	if err != nil {
		fatalf("less", "%v", err)
		return 1
	}
	if len(names) == 0 {
		names = []string{"-"}
	}
	// With no terminal to paint, less is a plain copy; that is what keeps
	// "less file | grep x" and "less file > copy" working.
	if !isTerminal(os.Stdout.Fd()) {
		return lessCopy(names)
	}
	keys, opened, err := pagerKeyboard()
	if err != nil {
		return lessCopy(names)
	}
	if opened {
		defer keys.Close()
	}

	view := &pager{opts: opts, names: names, keys: keys, firstScreen: true}
	if !view.openFirst() {
		return 1
	}
	return view.run()
}

// lessCopy writes the inputs out unchanged, which is what the original does
// when its output is not a terminal. A file that cannot be read is reported and
// skipped, so the readable ones still come through.
func lessCopy(names []string) int {
	copied := 0
	for _, name := range names {
		err := lessCheckFile(name)
		if err == nil {
			var input io.ReadCloser
			if input, err = openInput(name); err == nil {
				_, err = io.Copy(os.Stdout, input)
				input.Close()
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		copied++
	}
	if copied == 0 {
		return 1
	}
	return 0
}

// lessCheckFile reports a file that cannot be shown, worded the way the
// original words it: the name without an applet prefix, and a directory
// described rather than read.
func lessCheckFile(name string) error {
	if name == "-" {
		return nil
	}
	info, err := os.Stat(name)
	if err != nil {
		return fmt.Errorf("%s: %s", name, errText(err))
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", name)
	}
	return nil
}

// pagerKeyboard returns the file the key presses are read from: standard input
// when it is a terminal, and the controlling terminal when the data being paged
// arrives on standard input instead. The flag reports whether the caller has to
// close it.
func pagerKeyboard() (*os.File, bool, error) {
	if isTerminal(os.Stdin.Fd()) {
		return os.Stdin, false, nil
	}
	terminal, err := os.Open("/dev/tty")
	return terminal, err == nil, err
}

// openFirst loads the first file that can be read, reporting the ones that
// cannot. It fails only when none of them can be shown.
func (p *pager) openFirst() bool {
	for index := range p.names {
		err := p.open(index)
		if err == nil {
			return true
		}
		fmt.Fprintln(os.Stderr, err)
	}
	return false
}

// open loads the file at the given position in the command line and resets the
// view to its top.
func (p *pager) open(index int) error {
	if index < 0 || index >= len(p.names) {
		return fmt.Errorf("no such file position")
	}
	name := p.names[index]
	if err := lessCheckFile(name); err != nil {
		return err
	}
	var data []byte
	var err error
	if name == "-" {
		// Paged standard input has no name to show, so the status line falls
		// back to the bare prompt.
		data, err = io.ReadAll(os.Stdin)
		name = ""
	} else {
		data, err = os.ReadFile(name)
	}
	if err != nil {
		return fmt.Errorf("%s: %s", p.names[index], errText(err))
	}
	p.index = index
	p.file = newPagerFile(name, data, p.opts.squeeze)
	p.top, p.left, p.firstScreen = 0, 0, true
	return nil
}

// newPagerFile splits the input into lines and records where each one starts,
// which is what the byte and percentage positions are computed from.
func newPagerFile(name string, data []byte, squeeze bool) pagerFile {
	file := pagerFile{name: name}
	offset := 0
	blank := false
	for len(data) > 0 {
		length := len(data)
		if end := bytes.IndexByte(data, '\n'); end >= 0 {
			length = end + 1
		}
		line := bytes.TrimSuffix(data[:length], []byte{'\n'})
		// -s folds a run of blank lines into one, keeping the offsets of the
		// lines that are actually displayed.
		if !squeeze || len(line) > 0 || !blank {
			file.lines = append(file.lines, line)
			file.starts = append(file.starts, offset)
		}
		blank = len(line) == 0
		offset += length
		data = data[length:]
	}
	file.starts = append(file.starts, offset)
	return file
}

func (p *pager) run() int {
	p.measure()
	// -F prints a file that already fits and exits without taking over the
	// screen at all, so this happens before the terminal is put in raw mode.
	if p.opts.quitOneScreen && len(p.names) == 1 && len(p.file.lines) <= p.height() {
		for _, line := range p.file.lines {
			fmt.Println(p.renderLine(line))
		}
		return 0
	}
	old, err := terminalRaw(p.keys.Fd())
	if err != nil {
		// Without raw input there is no way to read keys, so what was loaded is
		// written out instead. Re-reading is not an option: piped input is
		// already consumed by now.
		return p.dump()
	}
	defer func() {
		restoreTerminal(p.keys.Fd(), old)
		if !p.opts.noInit {
			fmt.Print("\x1b[?1049l")
		}
		fmt.Print("\x1b[?25h")
	}()
	if !p.opts.noInit {
		fmt.Print("\x1b[?1049h")
	}
	fmt.Print("\x1b[?25l")
	for _, command := range p.opts.commands {
		p.runStartCommand(command)
	}
	for !p.quit {
		p.draw()
		key, err := readKeyFrom(p.keys)
		if err != nil {
			return 0
		}
		p.handleKey(key)
	}
	return 0
}

// dump writes the loaded file out unchanged and copies whatever files follow
// it, which is the best a pager can do once it turns out it cannot page.
func (p *pager) dump() int {
	for _, line := range p.file.lines {
		fmt.Println(string(line))
	}
	if p.index+1 < len(p.names) {
		return lessCopy(p.names[p.index+1:])
	}
	return 0
}

// runStartCommand replays a "+command" from the command line: a line number, a
// search, or one of the movement keys.
func (p *pager) runStartCommand(command string) {
	switch {
	case command == "":
	case command[0] == '/':
		p.search(command[1:], false, 1)
	case command[0] == '?':
		p.search(command[1:], true, 1)
	case command[0] >= '0' && command[0] <= '9':
		if line, err := strconv.Atoi(command); err == nil {
			p.goToLine(line)
		}
	default:
		for _, key := range command {
			p.handleKey(int(key))
		}
	}
}

// measure reads the screen size from the terminal being painted, falling back
// to the keyboard, then to the environment, and finally to a plain 24x80. The
// environment matters for terminals that answer no size ioctl at all.
func (p *pager) measure() {
	rows, cols, ok := terminalDimensions(os.Stdout.Fd())
	if !ok {
		rows, cols, ok = terminalDimensions(p.keys.Fd())
	}
	if !ok {
		rows, cols = pagerEnvSize()
	}
	p.rows, p.cols = rows, cols
}

func pagerEnvSize() (int, int) {
	rows, cols := 24, 80
	if value, err := strconv.Atoi(os.Getenv("LINES")); err == nil && value > 2 && value < 1000 {
		rows = value
	}
	if value, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && value > 0 && value < 1000 {
		cols = value
	}
	return rows, cols
}

// height is the number of text rows: everything but the status line.
func (p *pager) height() int {
	if p.rows < 2 {
		return 1
	}
	return p.rows - 1
}

// windowSize is how far the screen-at-a-time commands move, which -z can make
// smaller than the screen.
func (p *pager) windowSize() int {
	if p.opts.window > 0 && p.opts.window < p.height() {
		return p.opts.window
	}
	return p.height()
}

// halfScreen is how far d and u move: half the terminal, status line included,
// which is what makes them move five lines on a ten-row screen.
func (p *pager) halfScreen() int {
	return maxInt(1, p.rows/2)
}

// take returns the numeric prefix typed before the current command, or the
// given default when none was typed.
func (p *pager) take(fallback int) int {
	if p.count > 0 {
		count := p.count
		p.count = 0
		return count
	}
	return fallback
}

func (p *pager) handleKey(key int) {
	// A message or the help screen is dismissed by the next key press, which is
	// consumed doing so. Quitting is the exception: it works from a prompt as
	// well, so a stray message never traps the reader.
	if p.message != "" || p.help {
		p.message, p.help = "", false
		if key != 'q' && key != 'Q' && key != 3 {
			return
		}
	}
	if key >= '0' && key <= '9' {
		p.count = p.count*10 + (key - '0')
		return
	}
	switch key {
	case 'q', 'Q', 3: // q or Ctrl-C
		p.quit = true
	case ' ', 'f', 6, 1007: // space, f, Ctrl-F, page down
		p.scroll(p.take(1) * p.windowSize())
	case 'b', 2, 1006: // b, Ctrl-B, page up
		p.scroll(-p.take(1) * p.windowSize())
	case '\r', '\n', 'e', 'j', 5, 14, 1001: // enter, e, j, Ctrl-E, Ctrl-N, down
		p.scroll(p.take(1))
	case 'y', 'k', 25, 16, 1000: // y, k, Ctrl-Y, Ctrl-P, up
		p.scroll(-p.take(1))
	case 'd', 4: // d, Ctrl-D
		p.scroll(p.take(1) * p.halfScreen())
	case 'u', 21: // u, Ctrl-U
		p.scroll(-p.take(1) * p.halfScreen())
	case 'g', '<', 1004: // g, <, home
		p.goToLine(p.take(1))
	case 'G', '>', 1005: // G, >, end
		p.goToLine(p.take(len(p.file.lines)))
	case 'p', '%':
		p.goToPercent(p.take(0))
	case 1003: // right
		p.shift(p.take(maxInt(1, p.cols/2)))
	case 1002: // left
		p.shift(-p.take(maxInt(1, p.cols/2)))
	case '/':
		p.readPattern(false)
	case '?':
		p.readPattern(true)
	case 'n':
		p.repeatSearch(p.take(1), false)
	case 'N':
		p.repeatSearch(p.take(1), true)
	case '=', 7: // =, Ctrl-G
		p.message = p.fileStatus() + "  (press RETURN)"
	case 'h', 'H':
		p.help = true
	case '-':
		p.toggleOption()
	case ':':
		p.fileCommand()
	case 'r', 'R', 12, 18: // r, R, Ctrl-L, Ctrl-R
		p.measure()
	}
	// A count belongs to the command it precedes and does not carry over.
	p.count = 0
}

// scroll moves the view by delta lines, stopping at both ends of the file. It
// is also where "quit at end of file" takes effect.
func (p *pager) scroll(delta int) {
	if delta > 0 && p.atEOF() && p.opts.quitAtEOF {
		p.quit = true
		return
	}
	p.setTop(p.top + delta)
}

func (p *pager) setTop(line int) {
	if last := p.maxTop(); line > last {
		line = last
	}
	if line < 0 {
		line = 0
	}
	// Once the file has been paged the status line stops naming it, exactly as
	// the original stops showing the file name after the first movement.
	if line != p.top || !p.firstScreen {
		p.firstScreen = false
	}
	p.top = line
}

// maxTop is the furthest the view can scroll: the position that leaves the
// last line of the file on the bottom row, which is where the original stops
// scrolling too.
func (p *pager) maxTop() int {
	rows := 0
	for index := len(p.file.lines) - 1; index >= 0; index-- {
		rows += len(p.wrap(p.renderLine(p.file.lines[index])))
		if rows > p.height() {
			return index + 1
		}
	}
	return 0
}

func (p *pager) shift(columns int) {
	p.left = maxInt(0, p.left+columns)
	p.firstScreen = false
}

func (p *pager) goToLine(line int) {
	p.setTop(line - 1)
	p.firstScreen = false
}

// goToPercent positions the view at the line holding the byte that lies the
// given percentage into the file, which is how the original measures it.
func (p *pager) goToPercent(percent int) {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	target := p.file.size() * percent / 100
	line := 0
	for index := range p.file.lines {
		if p.file.starts[index] > target {
			break
		}
		line = index
	}
	p.setTop(line)
	p.firstScreen = false
}

// atEOF reports whether the last line of the file is on the screen.
func (p *pager) atEOF() bool {
	return p.lastVisible() >= len(p.file.lines)-1
}

// lastVisible is the index of the last file line the current screen shows,
// accounting for lines that wrap onto several rows.
func (p *pager) lastVisible() int {
	rows, last := 0, p.top-1
	for index := p.top; index < len(p.file.lines) && rows < p.height(); index++ {
		rows += len(p.wrap(p.renderLine(p.file.lines[index])))
		last = index
	}
	return last
}

func (p *pager) draw() {
	p.measure()
	var screen strings.Builder
	screen.WriteString("\x1b[H\x1b[2J")
	if p.help {
		p.drawHelp(&screen)
	} else {
		p.drawText(&screen)
	}
	fmt.Fprintf(&screen, "\x1b[%d;1H\x1b[7m%s\x1b[27m\x1b[K", p.rows, p.statusLine())
	fmt.Print(screen.String())
}

func (p *pager) drawText(screen *strings.Builder) {
	rows := 0
	for index := p.top; index < len(p.file.lines) && rows < p.height(); index++ {
		for row, text := range p.wrap(p.renderLine(p.file.lines[index])) {
			if rows >= p.height() {
				break
			}
			// Only the first row of a wrapped line carries its number.
			screen.WriteString(p.numberPrefix(index, row > 0) + p.highlight(text) + "\r\n")
			rows++
		}
	}
	for ; rows < p.height(); rows++ {
		screen.WriteString("\r\n")
	}
}

// numberPrefix is the -N line-number column, blank on the continuation rows of
// a wrapped line.
func (p *pager) numberPrefix(index int, continuation bool) string {
	if !p.opts.lineNumbers {
		return ""
	}
	if continuation {
		return strings.Repeat(" ", lessNumberWidth)
	}
	return fmt.Sprintf("\x1b[1m%*d\x1b[m ", lessNumberWidth-1, index+1)
}

// renderLine turns one file line into displayable text: tabs are expanded to
// the next tab stop and control characters are shown in caret notation, unless
// -R lets escape sequences through.
func (p *pager) renderLine(line []byte) string {
	var out strings.Builder
	width := 0
	for _, character := range line {
		switch {
		case character == '\t':
			spaces := p.opts.tabWidth - width%p.opts.tabWidth
			out.WriteString(strings.Repeat(" ", spaces))
			width += spaces
		case character == 27 && p.opts.raw:
			out.WriteByte(character)
		case character < 32 || character == 127:
			out.WriteString("^" + string(rune(character^0x40)))
			width += 2
		default:
			out.WriteByte(character)
			width++
		}
	}
	return out.String()
}

// wrap splits a rendered line into the rows it occupies. With -S, or while the
// view is scrolled sideways, the line is cut instead of wrapped.
func (p *pager) wrap(line string) []string {
	width := p.textWidth()
	if p.opts.chop || p.left > 0 {
		if p.left < len(line) {
			line = line[p.left:]
		} else {
			line = ""
		}
		if len(line) > width {
			line = line[:width]
		}
		return []string{line}
	}
	if line == "" {
		return []string{""}
	}
	var rows []string
	for start := 0; start < len(line); start += width {
		end := minInt(start+width, len(line))
		rows = append(rows, line[start:end])
	}
	return rows
}

// textWidth is how much of the screen a line of text gets, which -N narrows by
// the width of the number column.
func (p *pager) textWidth() int {
	if p.opts.lineNumbers {
		return maxInt(1, p.cols-lessNumberWidth)
	}
	return maxInt(1, p.cols)
}

// highlight marks the matches of the current search pattern on one screen row.
func (p *pager) highlight(row string) string {
	if p.pattern == nil {
		return row
	}
	var out strings.Builder
	last := 0
	for _, match := range p.pattern.FindAllStringIndex(row, -1) {
		if match[0] == match[1] {
			continue
		}
		out.WriteString(row[last:match[0]] + "\x1b[7m" + row[match[0]:match[1]] + "\x1b[27m")
		last = match[1]
	}
	out.WriteString(row[last:])
	return out.String()
}

// statusLine is the bottom line: a message if one is pending, otherwise the
// position report the -m and -M options select between.
func (p *pager) statusLine() string {
	if p.message != "" {
		return p.clip(p.message)
	}
	if p.help {
		return p.clip("HELP -- press any key to return")
	}
	// Paged standard input has no name, so it never gets the naming forms.
	name := p.displayName()
	switch {
	case p.opts.prompt >= 2:
		return p.clip(strings.TrimLeft(fmt.Sprintf("%s lines %d-%d/%d %s",
			name, p.top+1, p.lastVisible()+1, len(p.file.lines), p.positionMark()), " "))
	case p.opts.prompt == 1:
		return p.clip(strings.TrimLeft(name+" "+p.positionMark(), " "))
	case p.firstScreen && name != "" && p.atEOF():
		return p.clip(name + " (END)" + p.nextFileMark())
	case p.firstScreen && name != "":
		return p.clip(name)
	case p.atEOF():
		return p.clip("(END)" + p.nextFileMark())
	}
	return ":"
}

// positionMark is the end marker or the percentage of the file that has been
// displayed, measured in bytes as the original measures it.
func (p *pager) positionMark() string {
	if p.atEOF() {
		return "(END)" + p.nextFileMark()
	}
	return strconv.Itoa(p.percent()) + "%"
}

func (p *pager) nextFileMark() string {
	if p.index+1 < len(p.names) {
		return " - Next: " + p.names[p.index+1]
	}
	return ""
}

func (p *pager) percent() int {
	size := p.file.size()
	if size == 0 {
		return 100
	}
	return (p.bytePosition()*100 + size/2) / size
}

// bytePosition is the offset just past the last displayed line.
func (p *pager) bytePosition() int {
	last := p.lastVisible()
	if last < 0 || last+1 >= len(p.file.starts) {
		return p.file.size()
	}
	return p.file.starts[last+1]
}

// displayName is the file name, with its position in the command line when
// more than one file was named.
func (p *pager) displayName() string {
	if len(p.names) > 1 {
		return fmt.Sprintf("%s (file %d of %d)", p.file.name, p.index+1, len(p.names))
	}
	return p.file.name
}

// fileStatus is the report the "=" command prints.
func (p *pager) fileStatus() string {
	return fmt.Sprintf("%s lines %d-%d/%d byte %d/%d %d%%", p.displayName(),
		p.top+1, p.lastVisible()+1, len(p.file.lines), p.bytePosition(), p.file.size(), p.percent())
}

func (p *pager) clip(text string) string {
	if len(text) > p.cols {
		return text[:p.cols]
	}
	return text
}

// readPattern prompts for a search pattern on the status line and runs the
// search, unless the entry is cancelled with ESC.
func (p *pager) readPattern(backward bool) {
	marker := "/"
	if backward {
		marker = "?"
	}
	pattern, ok := p.readInput(marker)
	if !ok {
		return
	}
	if pattern == "" {
		p.repeatSearch(1, false)
		return
	}
	p.search(pattern, backward, p.take(1))
}

// readInput edits one line of text at the bottom of the screen.
func (p *pager) readInput(prefix string) (string, bool) {
	text := ""
	for {
		fmt.Printf("\x1b[%d;1H\x1b[K%s%s", p.rows, prefix, text)
		key, err := readKeyFrom(p.keys)
		if err != nil {
			return "", false
		}
		switch key {
		case '\r', '\n':
			return text, true
		case 27, 3: // ESC or Ctrl-C
			return "", false
		case 127, 8:
			if text == "" {
				return "", false
			}
			text = text[:len(text)-1]
		default:
			if key >= 32 && key < 127 {
				text += string(rune(key))
			}
		}
	}
}

// search compiles the pattern and moves to the count-th line that matches it.
func (p *pager) search(pattern string, backward bool, count int) {
	// -i ignores case unless the pattern itself is mixed case; -I always does.
	ignoreCase := p.opts.ignoreCaseAll || (p.opts.ignoreCase && pattern == strings.ToLower(pattern))
	compiled, err := compilePOSIXERE(pattern, ignoreCase)
	if err != nil {
		p.message = "Invalid pattern: " + pattern + "  (press RETURN)"
		return
	}
	p.pattern, p.patternText, p.backward = compiled, pattern, backward
	p.moveToMatch(count, backward)
}

func (p *pager) repeatSearch(count int, reverse bool) {
	if p.pattern == nil {
		p.message = "No previous regular expression  (press RETURN)"
		return
	}
	p.moveToMatch(count, p.backward != reverse)
}

// moveToMatch walks the file from the displayed line looking for the count-th
// matching line, and reports a failed search on the status line.
func (p *pager) moveToMatch(count int, backward bool) {
	step, index := 1, p.top+1
	if backward {
		step, index = -1, p.top-1
	}
	for ; index >= 0 && index < len(p.file.lines); index += step {
		if !p.pattern.Match(p.file.lines[index]) {
			continue
		}
		count--
		if count > 0 {
			continue
		}
		p.setTop(index)
		p.firstScreen = false
		return
	}
	p.message = "Pattern not found: " + p.patternText + "  (press RETURN)"
}

// toggleOption implements the "-" command for the display options that can be
// changed without reloading the file.
func (p *pager) toggleOption() {
	key, err := readKeyFrom(p.keys)
	if err != nil {
		return
	}
	state := func(name string, on bool) string {
		if on {
			return name + " is now in effect"
		}
		return name + " is no longer in effect"
	}
	switch key {
	case 'N':
		p.opts.lineNumbers = !p.opts.lineNumbers
		p.message = state("Line numbers", p.opts.lineNumbers) + "  (press RETURN)"
	case 'S':
		p.opts.chop = !p.opts.chop
		p.message = state("Chopping long lines", p.opts.chop) + "  (press RETURN)"
	case 'i':
		p.opts.ignoreCase = !p.opts.ignoreCase
		p.message = state("Case-insensitive searching", p.opts.ignoreCase) + "  (press RETURN)"
	case 'm', 'M':
		p.opts.prompt = (p.opts.prompt + 1) % 3
		p.message = "Prompt style changed  (press RETURN)"
	default:
		p.message = "Option cannot be changed here  (press RETURN)"
	}
}

// fileCommand handles the two-key ":" commands that move between the files
// named on the command line.
func (p *pager) fileCommand() {
	key, err := readKeyFrom(p.keys)
	if err != nil {
		return
	}
	switch key {
	case 'q', 'Q':
		p.quit = true
	case 'n':
		p.switchFile(p.index + 1)
	case 'p':
		p.switchFile(p.index - 1)
	}
}

func (p *pager) switchFile(index int) {
	if index < 0 || index >= len(p.names) {
		p.message = "No such file  (press RETURN)"
		return
	}
	if err := p.open(index); err != nil {
		p.message = err.Error() + "  (press RETURN)"
	}
}

func (p *pager) drawHelp(screen *strings.Builder) {
	help := []string{
		"",
		"   SPACE, f, PgDn      one screen forward       q        quit",
		"   b, PgUp             one screen back          h        this help",
		"   ENTER, j, DOWN      one line forward         =        file position",
		"   y, k, UP            one line back            r        repaint",
		"   d / u               half a screen            -N       toggle numbering",
		"   g / G               start / end of file      -S       toggle chopping",
		"   N% or Np            percentage position      -i       toggle case",
		"   LEFT / RIGHT        scroll sideways          :n / :p  next/previous file",
		"   /pattern            search forward           n / N    repeat search",
		"   ?pattern            search backward",
		"",
		"   A command may be preceded by a count, as in 12j or 50p.",
		"   This pager never runs another program: there is no editor,",
		"   shell or pipe command.",
		"",
		"   Press any key to return.",
	}
	for row := 0; row < p.height(); row++ {
		if row < len(help) {
			screen.WriteString(p.clip(help[row]))
		}
		screen.WriteString("\r\n")
	}
}

func parseLessOptions(args []string) (lessOptions, []string, error) {
	opts := lessOptions{tabWidth: 8}
	var names []string
	operandsOnly := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case operandsOnly, argument == "", argument == "-", !strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "+"):
			names = append(names, argument)
			continue
		case argument == "--":
			operandsOnly = true
			continue
		case strings.HasPrefix(argument, "+"):
			opts.commands = append(opts.commands, strings.TrimPrefix(argument, "+"))
			continue
		}
		if strings.HasPrefix(argument, "--") {
			// A long option may carry its value after an equals sign; the rest
			// of the parsing is done on the short spelling it maps to.
			name, value, split := strings.Cut(argument, "=")
			short, ok := lessLongOption(name)
			if !ok {
				return opts, nil, fmt.Errorf("unsupported option %s", name)
			}
			argument = short
			if split {
				argument += value
			}
		}
		// Short options cluster, and the ones taking a number accept it either
		// attached to the letter or as the next argument.
		for position := 1; position < len(argument); position++ {
			letter := argument[position]
			number := argument[position+1:]
			switch letter {
			case 'N':
				opts.lineNumbers = true
			case 'n':
				opts.lineNumbers = false
			case 'S':
				opts.chop = true
			case 'i':
				opts.ignoreCase = true
			case 'I':
				opts.ignoreCase, opts.ignoreCaseAll = true, true
			case 'F':
				opts.quitOneScreen = true
			case 'e', 'E':
				opts.quitAtEOF = true
			case 'X':
				opts.noInit = true
			case 's':
				opts.squeeze = true
			case 'r', 'R':
				opts.raw = true
			case 'm':
				opts.prompt = 1
			case 'M':
				opts.prompt = 2
			case 'q', 'Q', 'c', 'C', 'a', 'A', 'd', 'f', 'g', 'G', 'J', 'K', 'L', 'u', 'U', 'w', 'W', '~':
				// Accepted and without effect here: this pager never rings a
				// bell, keeps no log file and always repaints from memory.
			case 'p':
				// A starting pattern is the same thing as a "+/pattern"
				// command, so it is recorded as one.
				value, err := lessOptionValue(number, args, &index, letter)
				if err != nil {
					return opts, nil, err
				}
				opts.commands = append(opts.commands, "/"+value)
				position = len(argument)
			case 'x', 'z':
				value, err := lessOptionValue(number, args, &index, letter)
				if err != nil {
					return opts, nil, err
				}
				count, err := strconv.Atoi(value)
				if err != nil || count < 1 || count > 1024 {
					return opts, nil, fmt.Errorf("invalid value %q for -%c", value, letter)
				}
				if letter == 'x' {
					opts.tabWidth = count
				} else {
					opts.window = count
				}
				position = len(argument)
			default:
				return opts, nil, fmt.Errorf("invalid option -- '%c'", letter)
			}
		}
	}
	return opts, names, nil
}

// lessOptionValue reads the value of an option, taken either from the rest of
// the argument or from the one after it.
func lessOptionValue(attached string, args []string, index *int, letter byte) (string, error) {
	if attached != "" {
		return attached, nil
	}
	if *index+1 >= len(args) {
		return "", fmt.Errorf("option -%c requires a value", letter)
	}
	*index++
	return args[*index], nil
}

// lessLongOption maps the long spellings this pager understands onto their
// short forms.
func lessLongOption(argument string) (string, bool) {
	long := map[string]string{
		"--LINE-NUMBERS": "-N", "--line-numbers": "-n", "--chop-long-lines": "-S",
		"--ignore-case": "-i", "--IGNORE-CASE": "-I", "--quit-if-one-screen": "-F",
		"--quit-at-eof": "-e", "--QUIT-AT-EOF": "-E", "--no-init": "-X",
		"--squeeze-blank-lines": "-s", "--raw-control-chars": "-r",
		"--RAW-CONTROL-CHARS": "-R", "--long-prompt": "-m", "--LONG-PROMPT": "-M",
		"--tabs": "-x", "--window": "-z", "--quiet": "-q", "--silent": "-q",
		"--pattern": "-p", "--force": "-f", "--clear-screen": "-c",
	}
	short, ok := long[argument]
	return short, ok
}
