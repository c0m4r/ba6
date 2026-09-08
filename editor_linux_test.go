// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func newTestEditor(lines ...string) *miniEditor {
	e := &miniEditor{rows: 24, cols: 80}
	for _, l := range lines {
		e.lines = append(e.lines, []byte(l))
	}
	if len(e.lines) == 0 {
		e.lines = [][]byte{{}}
	}
	return e
}

func linesOf(e *miniEditor) []string {
	out := make([]string, len(e.lines))
	for i, l := range e.lines {
		out[i] = string(l)
	}
	return out
}

func TestMiniEditorFind(t *testing.T) {
	e := newTestEditor("hello world", "foo bar", "hello again")
	if !e.find("again") {
		t.Fatal("expected to find 'again'")
	}
	if e.row != 2 || e.col != 6 {
		t.Fatalf("cursor = (%d,%d), want (2,6)", e.row, e.col)
	}

	// Search starts just after the cursor, so sitting exactly on a match (as
	// this leaves the cursor on the "hello" at (0,0)) finds the *next* one,
	// not the one it's already on -- matching a normal editor's "find next".
	e.row, e.col = 0, 0
	if !e.find("hello") {
		t.Fatal("expected to find the next 'hello'")
	}
	if e.row != 2 || e.col != 0 {
		t.Fatalf("first hello search = (%d,%d), want (2,0)", e.row, e.col)
	}
	if !e.find("hello") {
		t.Fatal("expected search to wrap back to the first 'hello'")
	}
	if e.row != 0 || e.col != 0 {
		t.Fatalf("wrapped hello = (%d,%d), want (0,0)", e.row, e.col)
	}

	if e.find("nonexistent") {
		t.Fatal("should not find a string that isn't there")
	}
}

func TestMiniEditorReplaceAll(t *testing.T) {
	e := newTestEditor("hello world", "foo bar", "hello again")
	count := e.replaceAll("hello", "HI")
	if count != 2 {
		t.Fatalf("replaceAll count = %d, want 2", count)
	}
	want := []string{"HI world", "foo bar", "HI again"}
	if got := linesOf(e); !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	if !e.dirty {
		t.Fatal("replaceAll should mark the buffer dirty")
	}

	e2 := newTestEditor("nothing here")
	if count := e2.replaceAll("missing", "x"); count != 0 {
		t.Fatalf("replaceAll with no matches = %d, want 0", count)
	}
	if e2.dirty {
		t.Fatal("replaceAll with no matches should not mark the buffer dirty")
	}
}

func TestMiniEditorCutAndPaste(t *testing.T) {
	e := newTestEditor("AAA", "BBB", "CCC")
	e.cutLine()
	if got := linesOf(e); !slices.Equal(got, []string{"BBB", "CCC"}) {
		t.Fatalf("after cut = %v", got)
	}
	e.row = 1 // now on "CCC"
	e.paste()
	if got := linesOf(e); !slices.Equal(got, []string{"BBB", "AAA", "CCC"}) {
		t.Fatalf("after paste = %v, want [BBB AAA CCC]", got)
	}
}

func TestMiniEditorPasteWithoutCutIsNoop(t *testing.T) {
	e := newTestEditor("only")
	e.paste()
	if got := linesOf(e); !slices.Equal(got, []string{"only"}) {
		t.Fatalf("paste with nothing cut should be a no-op, got %v", got)
	}
}

func TestMiniEditorGoToLineInteractive(t *testing.T) {
	e := newTestEditor("l1", "l2", "l3", "l4", "l5")
	e.goToLine(4, 0)
	if e.row != 3 {
		t.Fatalf("row = %d, want 3 (1-indexed line 4)", e.row)
	}
	e.goToLine(999, 0) // clamps to the last line rather than erroring
	if e.row != 4 {
		t.Fatalf("out-of-range goto = %d, want clamped to 4", e.row)
	}
}

func TestNanoSaveWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	e := newTestEditor("one", "two", "three")
	e.filename = path
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "one\ntwo\nthree\n" {
		t.Fatalf("saved file = (%q, %v)", data, err)
	}
	if e.dirty {
		t.Fatal("save should clear the dirty flag")
	}
}

func TestNanoOptionsParsing(t *testing.T) {
	args := []string{
		"-v", "-l", "-E", "-i", "-k", "-c", "-t", "-B",
		"-T", "4",
		"--backupdir=/tmp/backup",
		"--guidestripe=80",
		"--quotestr=^> *",
		"--wordchars=abc",
		"--syntax=go",
		"--rcfile=/etc/nanorc",
		"--operatingdir=/home",
		"--fill=72",
		"--speller=aspell",
		"--zero",
		"--solosidescroll",
		"+15,5",
		"myfile.txt",
	}
	opts, err := parseNanoOptions(args)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !opts.viewMode || !opts.lineNumbers || !opts.tabToSpaces || !opts.autoIndent ||
		!opts.cutFromCursor || !opts.constantShow || !opts.saveOnExit || !opts.backup {
		t.Errorf("boolean flags not set properly: %+v", opts)
	}
	if opts.tabSize != 4 {
		t.Errorf("tabSize = %d, want 4", opts.tabSize)
	}
	if opts.backupDir != "/tmp/backup" {
		t.Errorf("backupDir = %s, want /tmp/backup", opts.backupDir)
	}
	if opts.guideStripe != 80 {
		t.Errorf("guideStripe = %d, want 80", opts.guideStripe)
	}
	if opts.quoteStr != "^> *" {
		t.Errorf("quoteStr = %s, want ^> *", opts.quoteStr)
	}
	if opts.wordChars != "abc" || opts.syntax != "go" || opts.rcFile != "/etc/nanorc" ||
		opts.operatingDir != "/home" || opts.fill != 72 || opts.speller != "aspell" {
		t.Errorf("string/int options parsed incorrectly: %+v", opts)
	}
	if !opts.zero || !opts.soloSideScroll {
		t.Errorf("zero/soloSideScroll not set: %+v", opts)
	}
	if opts.startLine != 15 || opts.startCol != 5 {
		t.Errorf("start pos = (%d,%d), want (15,5)", opts.startLine, opts.startCol)
	}
	if opts.filename != "myfile.txt" {
		t.Errorf("filename = %s, want myfile.txt", opts.filename)
	}

	// Test short option clustering
	clustered, err := parseNanoOptions([]string{"-vlEkic", "file2.txt"})
	if err != nil {
		t.Fatalf("unexpected parse error on cluster: %v", err)
	}
	if !clustered.viewMode || !clustered.lineNumbers || !clustered.tabToSpaces ||
		!clustered.autoIndent || !clustered.cutFromCursor || !clustered.constantShow {
		t.Errorf("clustered flags not set: %+v", clustered)
	}

	// Test error cases
	if _, err := parseNanoOptions([]string{"--tabsize"}); err == nil {
		t.Error("expected error for missing tabsize argument")
	}
	if _, err := parseNanoOptions([]string{"-T", "invalid"}); err == nil {
		t.Error("expected error for invalid tabsize")
	}
	if _, err := parseNanoOptions([]string{"--guidestripe"}); err == nil {
		t.Error("expected error for missing guidestripe argument")
	}
	if _, err := parseNanoOptions([]string{"-unknownflag"}); err == nil {
		t.Error("expected error for unknown flag")
	}
	if _, err := parseNanoOptions([]string{"file1.txt", "file2.txt"}); err == nil {
		t.Error("expected error for too many operands")
	}
}

func TestNanoViewMode(t *testing.T) {
	e := newMiniEditorWithOptions(nanoOptions{viewMode: true})
	e.lines = [][]byte{[]byte("read only text")}
	e.handleKey('x') // printable key should be ignored
	if string(e.lines[0]) != "read only text" {
		t.Fatalf("viewMode allowed editing: %s", string(e.lines[0]))
	}
	if e.message != "Key is invalid in view mode" {
		t.Fatalf("expected view mode message, got %s", e.message)
	}
	e.handleKey(127) // backspace should be ignored
	if string(e.lines[0]) != "read only text" {
		t.Fatalf("viewMode allowed backspace: %s", string(e.lines[0]))
	}
}

func TestNanoTabToSpaces(t *testing.T) {
	e := newMiniEditorWithOptions(nanoOptions{tabToSpaces: true, tabSize: 4})
	e.lines = [][]byte{[]byte("")}
	e.handleKey('\t')
	if got := string(e.lines[0]); got != "    " {
		t.Fatalf("tabToSpaces inserted %q, want 4 spaces", got)
	}
}

func TestNanoAutoIndent(t *testing.T) {
	e := newMiniEditorWithOptions(nanoOptions{autoIndent: true})
	e.lines = [][]byte{[]byte("    indented line")}
	e.col = len(e.lines[0])
	e.handleKey('\n')
	if len(e.lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(e.lines))
	}
	if got := string(e.lines[1]); got != "    " {
		t.Fatalf("expected 4 leading spaces preserved, got %q", got)
	}
	if e.col != 4 {
		t.Fatalf("expected cursor col 4, got %d", e.col)
	}
}

func TestNanoCutFromCursor(t *testing.T) {
	e := newMiniEditorWithOptions(nanoOptions{cutFromCursor: true})
	e.lines = [][]byte{[]byte("hello world")}
	e.col = 5
	e.cutLine()
	if got := string(e.lines[0]); got != "hello" {
		t.Fatalf("expected line trimmed to 'hello', got %q", got)
	}
	if got := string(e.cutBuffer); got != " world" {
		t.Fatalf("expected cutBuffer ' world', got %q", got)
	}
}

func TestNanoBackup(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(filePath, []byte("version 1\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	backupDir := filepath.Join(dir, "backups")
	e := newMiniEditorWithOptions(nanoOptions{
		filename:  filePath,
		backup:    true,
		backupDir: backupDir,
	})
	e.lines = [][]byte{[]byte("version 2")}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}

	// Check new content
	newData, err := os.ReadFile(filePath)
	if err != nil || string(newData) != "version 2\n" {
		t.Fatalf("target file content = %q, want 'version 2\\n'", string(newData))
	}

	// Check backup file
	backupFile := filepath.Join(backupDir, "target.txt~")
	backupData, err := os.ReadFile(backupFile)
	if err != nil || string(backupData) != "version 1\n" {
		t.Fatalf("backup content = %q, want 'version 1\\n'", string(backupData))
	}
}

func TestNanoListSyntaxes(t *testing.T) {
	code := cmdNano([]string{"-z"})
	if code != 0 {
		t.Fatalf("cmdNano(-z) returned %d, want 0", code)
	}
	code = cmdNano([]string{"--listsyntaxes"})
	if code != 0 {
		t.Fatalf("cmdNano(--listsyntaxes) returned %d, want 0", code)
	}
}

