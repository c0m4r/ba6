// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every expectation below was diffed against GNU grep 3.12 on a live system.

// grepContextInput is twelve lines with matches at 7 and 11, far enough apart
// for the group separator to appear between them at one line of context.
const grepContextInput = "a1\nb2\nc3\nd4\ne5\nf6\nmatch7\ng8\nh9\ni10\nmatch11\nj12\n"

func TestGrepContextOptions(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-A1", "match"}, "match7\ng8\n--\nmatch11\nj12\n"},
		{[]string{"-B1", "match"}, "f6\nmatch7\n--\ni10\nmatch11\n"},
		{[]string{"-C1", "match"}, "f6\nmatch7\ng8\n--\ni10\nmatch11\nj12\n"},
		{[]string{"-1", "match"}, "f6\nmatch7\ng8\n--\ni10\nmatch11\nj12\n"},
		{[]string{"-C1", "-n", "match"}, "6-f6\n7:match7\n8-g8\n--\n10-i10\n11:match11\n12-j12\n"},
		// Overlapping groups are printed once, with no separator.
		{[]string{"-A9", "match"}, "match7\ng8\nh9\ni10\nmatch11\nj12\n"},
		// The trailing context of the last match survives -m.
		{[]string{"-m1", "-A2", "match"}, "match7\ng8\nh9\n"},
		{[]string{"-m1", "-B2", "match"}, "e5\nf6\nmatch7\n"},
		{[]string{"--no-group-separator", "-A1", "match"}, "match7\ng8\nmatch11\nj12\n"},
		{[]string{"--group-separator===", "-A1", "match"}, "match7\ng8\n==\nmatch11\nj12\n"},
	} {
		status, stdout, stderr := captureApplet(t, cmdGrep, c.args, grepContextInput)
		if status != 0 || stdout != c.want {
			t.Errorf("grep %v = (%d, %q, %q), want %q", c.args, status, stdout, stderr, c.want)
		}
	}
}

func TestGrepOnlyMatching(t *testing.T) {
	for _, c := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"-o", "X"}, "aXbXc\n", "X\nX\n"},
		{[]string{"-o", "-n", "X"}, "aXbXc\nQ\nXX\n", "1:X\n1:X\n3:X\n3:X\n"},
		// -c still counts lines, not matches.
		{[]string{"-o", "-c", "X"}, "aXbXc\nXX\n", "2\n"},
		// An empty match selects the line but prints nothing.
		{[]string{"-o", "x*"}, "abc\n", ""},
		// -w keeps looking further along the line after a rejected position.
		{[]string{"-w", "-o", "foo"}, "foo foobar foo\n", "foo\nfoo\n"},
		{[]string{"-w", "-o", "foo"}, "foo foo\n", "foo\nfoo\n"},
		{[]string{"-o", "-b", "X"}, "aXbXc\nyy\nX\n", "1:X\n3:X\n9:X\n"},
		{[]string{"-b", "X"}, "aXbXc\nyy\nX\n", "0:aXbXc\n9:X\n"},
	} {
		status, stdout, stderr := captureApplet(t, cmdGrep, c.args, c.input)
		if status != 0 || stdout != c.want {
			t.Errorf("grep %v = (%d, %q, %q), want %q", c.args, status, stdout, stderr, c.want)
		}
	}
	// -o with -v prints nothing, but the non-matching lines still select.
	status, stdout, _ := captureApplet(t, cmdGrep, []string{"-o", "-v", "X"}, "aXb\ncd\n")
	if status != 0 || stdout != "" {
		t.Errorf("grep -o -v = (%d, %q)", status, stdout)
	}
}

func TestGrepWordEscapesAndPatternFile(t *testing.T) {
	const input = "foo\nfoobar\nbaz foo\n"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{`\<foo\>`}, "foo\nbaz foo\n"},
		{[]string{"-E", `\<foo\>`}, "foo\nbaz foo\n"},
		{[]string{`\bfoo`}, "foo\nfoobar\nbaz foo\n"},
		{[]string{`\w\w\w$`}, "foo\nfoobar\nbaz foo\n"},
		{[]string{`foo\sbar`}, ""},
	} {
		status, stdout, stderr := captureApplet(t, cmdGrep, c.args, input)
		if stdout != c.want || (c.want == "" && status != 1) {
			t.Errorf("grep %v = (%d, %q, %q), want %q", c.args, status, stdout, stderr, c.want)
		}
	}

	dir := t.TempDir()
	list := filepath.Join(dir, "pats")
	if err := os.WriteFile(list, []byte("foo\nbaz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdGrep, []string{"-f", list}, "nothing\nbaz\nfoo\n")
	if status != 0 || stdout != "baz\nfoo\n" {
		t.Fatalf("grep -f = (%d, %q, %q)", status, stdout, stderr)
	}
}

func TestGrepColorOutput(t *testing.T) {
	const red = "\x1b[01;31m\x1b[K"
	const off = "\x1b[m\x1b[K"
	status, stdout, stderr := captureApplet(t, cmdGrep, []string{"--color=always", "X"}, "aXbXc\n")
	want := "a" + red + "X" + off + "b" + red + "X" + off + "c\n"
	if status != 0 || stdout != want {
		t.Fatalf("grep --color=always = (%d, %q, %q), want %q", status, stdout, stderr, want)
	}
	// An empty match is never highlighted.
	status, stdout, _ = captureApplet(t, cmdGrep, []string{"--color=always", "x*"}, "a-b\n")
	if status != 0 || stdout != "a-b\n" {
		t.Fatalf("grep --color=always on an empty match = (%d, %q)", status, stdout)
	}
	// --color=never and the absence of a terminal both leave the text alone.
	status, stdout, _ = captureApplet(t, cmdGrep, []string{"--color=auto", "X"}, "aXb\n")
	if status != 0 || stdout != "aXb\n" {
		t.Fatalf("grep --color=auto off a terminal = (%d, %q)", status, stdout)
	}
}

func TestGrepInitialTabAndLabel(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "lines")
	// Fourteen bytes, so -T aligns numbers in a two-column field.
	if err := os.WriteFile(file, []byte("1\n2\nmatch\n4\n5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdGrep, []string{"-T", "-n", "match", file}, "")
	if status != 0 || stdout != " 3:\tmatch\n" {
		t.Fatalf("grep -T -n = (%d, %q, %q)", status, stdout, stderr)
	}
	// With no prefix to align, -T adds nothing.
	if status, stdout, _ := captureApplet(t, cmdGrep, []string{"-T", "match", file}, ""); status != 0 || stdout != "match\n" {
		t.Fatalf("grep -T = (%d, %q)", status, stdout)
	}
	// The field is as wide as the largest offset the input can produce: the
	// file's size where there is one, and the widest offset for a pipe.
	handle, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	if got := grepOffsetWidth(handle); got != 2 {
		t.Fatalf("grepOffsetWidth on a 14-byte file = %d, want 2", got)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	if got := grepOffsetWidth(reader); got != 19 {
		t.Fatalf("grepOffsetWidth on a pipe = %d, want 19", got)
	}
	// --label renames standard input.
	status, stdout, _ = captureApplet(t, cmdGrep, []string{"--label=LBL", "-H", "match"}, "match\n")
	if status != 0 || stdout != "LBL:match\n" {
		t.Fatalf("grep --label = (%d, %q)", status, stdout)
	}
}

func TestGrepFileSelectionAndBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "text.txt")
	logFile := filepath.Join(dir, "other.log")
	binary := filepath.Join(dir, "bin.dat")
	if err := os.WriteFile(text, []byte("match\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logFile, []byte("match\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("abc\x00def\nmatch\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A binary file reports one notice on stderr and still exits 0.
	status, stdout, stderr := captureApplet(t, cmdGrep, []string{"match", binary}, "")
	if status != 0 || stdout != "" || !strings.Contains(stderr, "binary file matches") {
		t.Errorf("grep on a binary file = (%d, %q, %q)", status, stdout, stderr)
	}
	if status, stdout, _ := captureApplet(t, cmdGrep, []string{"-a", "match", binary}, ""); status != 0 || stdout != "match\n" {
		t.Errorf("grep -a = (%d, %q)", status, stdout)
	}
	if status, stdout, _ := captureApplet(t, cmdGrep, []string{"-I", "match", binary}, ""); status != 1 || stdout != "" {
		t.Errorf("grep -I = (%d, %q)", status, stdout)
	}
	// -c reads a binary file the way it reads any other.
	if status, stdout, _ := captureApplet(t, cmdGrep, []string{"-c", "match", binary}, ""); status != 0 || stdout != "1\n" {
		t.Errorf("grep -c on a binary file = (%d, %q)", status, stdout)
	}

	// --include and --exclude pick files while recursing.
	status, stdout, _ = captureApplet(t, cmdGrep, []string{"-r", "--include=*.txt", "-l", "match", dir}, "")
	if status != 0 || stdout != text+"\n" {
		t.Errorf("grep -r --include = (%d, %q)", status, stdout)
	}
	status, stdout, _ = captureApplet(t, cmdGrep, []string{"-r", "--exclude=*.log", "-l", "-I", "match", dir}, "")
	if status != 0 || stdout != text+"\n" {
		t.Errorf("grep -r --exclude = (%d, %q)", status, stdout)
	}

	// -L names the files that held no match, without changing the status.
	status, stdout, _ = captureApplet(t, cmdGrep, []string{"-L", "nothing-here", text}, "")
	if status != 1 || stdout != text+"\n" {
		t.Errorf("grep -L = (%d, %q)", status, stdout)
	}
}

func TestGrepDirectoryAndErrorHandling(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")

	// A directory operand without -r is an error, unless -d skip is given.
	status, _, stderr := captureApplet(t, cmdGrep, []string{"match", dir}, "")
	if status != 2 || !strings.Contains(stderr, dir+": Is a directory") {
		t.Errorf("grep on a directory = (%d, %q)", status, stderr)
	}
	if status, _, stderr := captureApplet(t, cmdGrep, []string{"-d", "skip", "match", dir}, ""); status != 1 || stderr != "" {
		t.Errorf("grep -d skip = (%d, %q)", status, stderr)
	}

	// -s hides the diagnostic but leaves the exit status at 2.
	status, _, stderr = captureApplet(t, cmdGrep, []string{"match", missing}, "")
	if status != 2 || !strings.Contains(stderr, "No such file or directory") {
		t.Errorf("grep on a missing file = (%d, %q)", status, stderr)
	}
	if status, _, stderr := captureApplet(t, cmdGrep, []string{"-s", "match", missing}, ""); status != 2 || stderr != "" {
		t.Errorf("grep -s on a missing file = (%d, %q)", status, stderr)
	}
}
