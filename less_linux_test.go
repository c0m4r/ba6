// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestPager builds a pager over in-memory text on a ten-row screen, so the
// display logic can be exercised without a terminal.
func newTestPager(t *testing.T, text string, opts lessOptions, cols int) *pager {
	t.Helper()
	if opts.tabWidth == 0 {
		opts.tabWidth = 8
	}
	view := &pager{opts: opts, names: []string{"sample.txt"}, firstScreen: true, rows: 10, cols: cols}
	view.file = newPagerFile("sample.txt", []byte(text), opts.squeeze)
	return view
}

func numberedText(lines int) string {
	var text strings.Builder
	for line := 1; line <= lines; line++ {
		fmt.Fprintf(&text, "%d\n", line)
	}
	return text.String()
}

// Without a terminal on the output side the pager is a plain copy, which is
// what keeps it usable in a pipeline.
func TestLessCopiesWhenOutputIsNotATerminal(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	if err := os.WriteFile(first, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdLess, []string{first, second}, "")
	if status != 0 || stdout != "one\ntwo\nthree\n" || stderr != "" {
		t.Fatalf("less = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdLess, []string{"-"}, "piped\n")
	if status != 0 || stdout != "piped\n" || stderr != "" {
		t.Fatalf("less - = (%d, %q, %q)", status, stdout, stderr)
	}
}

func TestLessReportsUnreadableFiles(t *testing.T) {
	dir := t.TempDir()
	readable := filepath.Join(dir, "readable")
	if err := os.WriteFile(readable, []byte("text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")
	status, stdout, stderr := captureApplet(t, cmdLess, []string{missing, readable}, "")
	if status != 0 || stdout != "text\n" || stderr != missing+": No such file or directory\n" {
		t.Fatalf("skipped file = (%d, %q, %q)", status, stdout, stderr)
	}
	status, _, stderr = captureApplet(t, cmdLess, []string{dir}, "")
	if status != 1 || stderr != dir+" is a directory\n" {
		t.Fatalf("directory = (%d, %q)", status, stderr)
	}
}

// Positions are measured in bytes, the way the original measures them, so the
// line offsets have to survive the split.
func TestPagerFileOffsets(t *testing.T) {
	file := newPagerFile("x", []byte("ab\ncd\ne"), false)
	if len(file.lines) != 3 || file.size() != 7 {
		t.Fatalf("lines=%q size=%d", file.lines, file.size())
	}
	for index, want := range []int{0, 3, 6, 7} {
		if file.starts[index] != want {
			t.Errorf("start[%d] = %d, want %d", index, file.starts[index], want)
		}
	}
	squeezed := newPagerFile("x", []byte("a\n\n\n\nb\n"), true)
	if len(squeezed.lines) != 3 || string(squeezed.lines[1]) != "" || string(squeezed.lines[2]) != "b" {
		t.Fatalf("squeezed lines = %q", squeezed.lines)
	}
}

func TestPagerStatusLineForms(t *testing.T) {
	view := newTestPager(t, numberedText(100), lessOptions{}, 40)
	if got := view.statusLine(); got != "sample.txt" {
		t.Errorf("first screen = %q", got)
	}
	view.scroll(view.windowSize())
	if got := view.statusLine(); got != ":" {
		t.Errorf("after scrolling = %q", got)
	}
	view.goToLine(len(view.file.lines))
	if got := view.statusLine(); got != "(END)" {
		t.Errorf("at end = %q", got)
	}
	if view.top != 91 {
		t.Errorf("end of file starts at line %d, want 91", view.top+1)
	}

	view = newTestPager(t, numberedText(100), lessOptions{prompt: 2}, 60)
	if got := view.statusLine(); got != "sample.txt lines 1-9/100 6%" {
		t.Errorf("-M line = %q", got)
	}
	if got := view.fileStatus(); got != "sample.txt lines 1-9/100 byte 18/292 6%" {
		t.Errorf("= report = %q", got)
	}
	view.goToLine(100)
	if got := view.statusLine(); got != "sample.txt lines 92-100/100 (END)" {
		t.Errorf("-M at end = %q", got)
	}

	short := newTestPager(t, "a\nb\n", lessOptions{}, 40)
	if got := short.statusLine(); got != "sample.txt (END)" {
		t.Errorf("short file = %q", got)
	}
	empty := newTestPager(t, "", lessOptions{}, 40)
	if got := empty.statusLine(); got != "sample.txt (END)" {
		t.Errorf("empty file = %q", got)
	}
}

func TestPagerMultipleFileStatus(t *testing.T) {
	view := newTestPager(t, "a\n", lessOptions{}, 60)
	view.names = []string{"sample.txt", "next.txt"}
	if got := view.statusLine(); got != "sample.txt (file 1 of 2) (END) - Next: next.txt" {
		t.Errorf("multi-file status = %q", got)
	}
}

// A byte position drives the percentage, so half of a file is measured in bytes
// rather than in lines.
func TestPagerPercentPosition(t *testing.T) {
	view := newTestPager(t, numberedText(100), lessOptions{prompt: 2}, 60)
	view.goToPercent(50)
	if view.top != 51 {
		t.Errorf("50%% starts at line %d, want 52", view.top+1)
	}
	if got := view.statusLine(); got != "sample.txt lines 52-60/100 59%" {
		t.Errorf("status after 50%% = %q", got)
	}
}

func TestPagerScrollingStopsAtBothEnds(t *testing.T) {
	view := newTestPager(t, numberedText(20), lessOptions{}, 40)
	view.scroll(-5)
	if view.top != 0 {
		t.Errorf("scrolled above the start to %d", view.top)
	}
	view.scroll(1000)
	if view.top != 11 || !view.atEOF() {
		t.Errorf("scrolled past the end to %d (eof=%v)", view.top, view.atEOF())
	}
	// d and u move half a terminal, status line included.
	view.setTop(0)
	view.handleKey('d')
	if view.top != 5 {
		t.Errorf("half screen forward = %d, want 5", view.top)
	}
	view.handleKey('u')
	if view.top != 0 {
		t.Errorf("half screen back = %d, want 0", view.top)
	}
	// A typed count belongs to the command that follows it.
	view.handleKey('1')
	view.handleKey('2')
	view.handleKey('j')
	if view.top != 11 || view.count != 0 {
		t.Errorf("12j = top %d count %d", view.top, view.count)
	}
}

func TestPagerRendersTabsAndControlCharacters(t *testing.T) {
	view := newTestPager(t, "", lessOptions{}, 40)
	if got := view.renderLine([]byte("a\tb")); got != "a       b" {
		t.Errorf("tab expansion = %q", got)
	}
	if got := view.renderLine([]byte("\x01\x7f")); got != "^A^?" {
		t.Errorf("control characters = %q", got)
	}
	view.opts.tabWidth = 4
	if got := view.renderLine([]byte("ab\tc")); got != "ab  c" {
		t.Errorf("-x4 expansion = %q", got)
	}
	view.opts.raw = true
	if got := view.renderLine([]byte("\x1b[1mbold")); got != "\x1b[1mbold" {
		t.Errorf("-R passthrough = %q", got)
	}
}

func TestPagerWrapsAndChopsLines(t *testing.T) {
	long := strings.Repeat("x", 25)
	view := newTestPager(t, long+"\n", lessOptions{}, 10)
	rows := view.wrap(long)
	if len(rows) != 3 || rows[0] != strings.Repeat("x", 10) || rows[2] != strings.Repeat("x", 5) {
		t.Errorf("wrapped rows = %q", rows)
	}
	view.opts.chop = true
	if rows := view.wrap(long); len(rows) != 1 || rows[0] != strings.Repeat("x", 10) {
		t.Errorf("chopped rows = %q", rows)
	}
	view.shift(20)
	if rows := view.wrap(long); len(rows) != 1 || rows[0] != strings.Repeat("x", 5) {
		t.Errorf("shifted rows = %q", rows)
	}
	// -N narrows the text column by the width of the number.
	numbered := newTestPager(t, long+"\n", lessOptions{lineNumbers: true}, 20)
	if rows := numbered.wrap(long); len(rows) != 3 || len(rows[0]) != 12 {
		t.Errorf("numbered rows = %q", rows)
	}
	if got := numbered.numberPrefix(6, false); got != "\x1b[1m      7\x1b[m " {
		t.Errorf("number prefix = %q", got)
	}
	if got := numbered.numberPrefix(6, true); got != strings.Repeat(" ", 8) {
		t.Errorf("continuation prefix = %q", got)
	}
}

func TestPagerSearch(t *testing.T) {
	view := newTestPager(t, numberedText(100), lessOptions{}, 40)
	view.search("42", false, 1)
	if view.top != 41 || view.message != "" {
		t.Fatalf("forward search stopped at %d (%q)", view.top+1, view.message)
	}
	// A search with several matches walks them one at a time, forwards under n
	// and backwards under N.
	view.setTop(0)
	view.search("4", false, 1)
	view.repeatSearch(1, false)
	if view.top != 13 {
		t.Errorf("second match is at %d, want 14", view.top+1)
	}
	view.repeatSearch(1, true)
	if view.top != 3 {
		t.Errorf("backward repeat stopped at %d, want 4", view.top+1)
	}
	view.search("nothing", false, 1)
	if view.message != "Pattern not found: nothing  (press RETURN)" {
		t.Errorf("failed search message = %q", view.message)
	}
	// The message is cleared by the next key, and q still quits from it.
	view.handleKey('j')
	if view.message != "" {
		t.Errorf("message survived a key press: %q", view.message)
	}
	view.message = "pending"
	view.handleKey('q')
	if !view.quit {
		t.Error("q did not quit from a message prompt")
	}

	insensitive := newTestPager(t, numberedText(20)+"BETA\n"+numberedText(50),
		lessOptions{ignoreCase: true}, 40)
	insensitive.search("beta", false, 1)
	if insensitive.top != 20 || insensitive.message != "" {
		t.Errorf("-i search stopped at %d (%q)", insensitive.top+1, insensitive.message)
	}
	if got := insensitive.highlight("BETA"); got != "\x1b[7mBETA\x1b[27m" {
		t.Errorf("highlighted row = %q", got)
	}
}

func TestParseLessOptions(t *testing.T) {
	opts, names, err := parseLessOptions([]string{"-NSi", "-x4", "+G", "file.txt"})
	if err != nil || len(names) != 1 || names[0] != "file.txt" ||
		!opts.lineNumbers || !opts.chop || !opts.ignoreCase || opts.tabWidth != 4 ||
		len(opts.commands) != 1 || opts.commands[0] != "G" {
		t.Fatalf("opts=%+v names=%q err=%v", opts, names, err)
	}
	opts, _, err = parseLessOptions([]string{"--LINE-NUMBERS", "--tabs=2", "--quit-if-one-screen"})
	if err != nil || !opts.lineNumbers || opts.tabWidth != 2 || !opts.quitOneScreen {
		t.Fatalf("long options gave opts=%+v err=%v", opts, err)
	}
	opts, _, err = parseLessOptions([]string{"-p", "needle", "-z", "5"})
	if err != nil || len(opts.commands) != 1 || opts.commands[0] != "/needle" || opts.window != 5 {
		t.Fatalf("-p/-z gave opts=%+v err=%v", opts, err)
	}
	if _, names, err := parseLessOptions([]string{"--", "-weird-name"}); err != nil ||
		len(names) != 1 || names[0] != "-weird-name" {
		t.Fatalf("operands after -- = %q (%v)", names, err)
	}
	for _, args := range [][]string{{"-Z"}, {"--nosuch"}, {"-x"}, {"-z", "0"}} {
		if _, _, err := parseLessOptions(args); err == nil {
			t.Errorf("parseLessOptions(%q) was accepted", args)
		}
	}
}

// -z limits how far a screen-at-a-time command moves.
func TestPagerWindowOption(t *testing.T) {
	view := newTestPager(t, numberedText(100), lessOptions{window: 3}, 40)
	view.handleKey(' ')
	if view.top != 3 {
		t.Errorf("-z 3 moved to %d, want 3", view.top)
	}
}

func TestPagerStartCommands(t *testing.T) {
	view := newTestPager(t, numberedText(100), lessOptions{}, 40)
	view.runStartCommand("/42")
	if view.top != 41 {
		t.Errorf("+/42 stopped at %d", view.top+1)
	}
	view.runStartCommand("7")
	if view.top != 6 {
		t.Errorf("+7 stopped at %d", view.top+1)
	}
	view.runStartCommand("G")
	if !view.atEOF() {
		t.Error("+G did not reach the end")
	}
}
