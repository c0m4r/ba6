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
