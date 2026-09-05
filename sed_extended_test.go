// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSedMultilineCommands(t *testing.T) {
	cases := []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"N joins pairs", `N;s/\n/-/`, "a\nb\nc\n", "a-b\nc\n"},
		{"N at EOF keeps and prints (GNU default)", "N", "a\nb\nc\n", "a\nb\nc\n"},
		{"D squeezes blank runs", `/^$/{N;/^\n$/D}`, "a\n\n\n\nb\n", "a\n\nb\n"},
		{"P prints only the first line", "N;P;D", "a\nb\nc\n", "a\nb\nc\n"},
		{"hold-space reverse (tac)", "1!G;h;$!d", "1\n2\n3\n", "3\n2\n1\n"},
		{"t loop until no match", ":x;s/a/b/;tx", "aaa\n", "bbb\n"},
		{"T branches when no substitution", "s/z/Z/;Tskip;s/$/-hit/;:skip", "abc\n", "abc\n"},
		{"y transliterates", "y/abc/xyz/", "abc\n", "xyz\n"},
		{"a appends after autoprint", "a appended", "hello\n", "hello\nappended\n"},
		{"a classic backslash form", "a\\\nappended", "hello\n", "hello\nappended\n"},
		{"i inserts before", "i inserted", "hello\n", "inserted\nhello\n"},
		{"c replaces a single line", "c changed", "hello\n", "changed\n"},
		{"c replaces a range once", "2,3c\\\nreplaced", "1\n2\n3\n4\n", "1\nreplaced\n4\n"},
		{"a queued text still prints after d", "1{a\\\nappended\nd\n}", "1\n2\n", "appended\n2\n"},
		{"block grouped by address", "/b/{s/b/B/;p}", "a\nb\nc\n", "a\nB\nB\nc\n"},
		{"negated block", "/b/!{s/./X/}", "a\nb\nc\n", "X\nb\nX\n"},
		{"nested blocks", "1,3{/2/{s/2/TWO/}}", "1\n2\n3\n4\n", "1\nTWO\n3\n4\n"},
		{"l escapes control characters", "l", "a\tb\n", "a\\tb$\na\tb\n"},
		{"backslash escape in a text", `a line1\tline2`, "x\n", "x\nline1\tline2\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out, stderr := captureApplet(t, cmdSed, []string{tc.script}, tc.input)
			if status != 0 || out != tc.want {
				t.Fatalf("sed %q on %q = (%d, %q, %q), want %q", tc.script, tc.input, status, out, stderr, tc.want)
			}
		})
	}
}

func TestSedQAndQuit(t *testing.T) {
	status, out, _ := captureApplet(t, cmdSed, []string{"1q5"}, "x\ny\n")
	if status != 5 || out != "x\n" {
		t.Fatalf("sed 1q5 = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdSed, []string{"1Q"}, "x\ny\n")
	if status != 0 || out != "" {
		t.Fatalf("sed 1Q = (%d, %q)", status, out)
	}
}

func TestSedBlockSyntaxErrors(t *testing.T) {
	if status, _, stderr := captureApplet(t, cmdSed, []string{"1{a text}"}, "x\n"); status == 0 || stderr == "" {
		t.Fatalf("sed with an unmatched '{' should fail, got (%d, %q)", status, stderr)
	}
	if status, _, stderr := captureApplet(t, cmdSed, []string{"1{p"}, "x\n"); status == 0 || stderr == "" {
		t.Fatalf("sed with an unclosed '{' should fail, got (%d, %q)", status, stderr)
	}
	if status, _, stderr := captureApplet(t, cmdSed, []string{"p}"}, "x\n"); status == 0 || stderr == "" {
		t.Fatalf("sed with a stray '}' should fail, got (%d, %q)", status, stderr)
	}
	if status, _, stderr := captureApplet(t, cmdSed, []string{"bnope"}, "x\n"); status == 0 || stderr == "" {
		t.Fatalf("sed branching to an unknown label should fail, got (%d, %q)", status, stderr)
	}
}

func TestSedReadAndWriteCommands(t *testing.T) {
	dir := t.TempDir()
	extra := filepath.Join(dir, "extra.txt")
	if err := os.WriteFile(extra, []byte("extra-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, out, _ := captureApplet(t, cmdSed, []string{"1r " + extra}, "x\ny\n")
	if status != 0 || out != "x\nextra-line\ny\n" {
		t.Fatalf("sed r = (%d, %q)", status, out)
	}

	// "/b/w file" and "p" are two independent commands here (no block), so
	// -n plus the unconditional "p" prints every line; only the file is
	// filtered by the /b/ address.
	outFile := filepath.Join(dir, "out.txt")
	status, stdout, _ := captureApplet(t, cmdSed, []string{"-n", "/b/w " + outFile + "\np"}, "a\nb\nc\n")
	if status != 0 || stdout != "a\nb\nc\n" {
		t.Fatalf("sed w stdout = (%d, %q)", status, stdout)
	}
	written, err := os.ReadFile(outFile)
	if err != nil || string(written) != "b\n" {
		t.Fatalf("sed w file = (%q, %v)", written, err)
	}
}

func TestSedInPlaceEditingAndBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, out, stderr := captureApplet(t, cmdSed, []string{"-i.bak", "s/two/TWO/", target}, "")
	if status != 0 || out != "" {
		t.Fatalf("sed -i = (%d, %q, %q)", status, out, stderr)
	}
	edited, err := os.ReadFile(target)
	if err != nil || string(edited) != "one\nTWO\nthree\n" {
		t.Fatalf("edited file = (%q, %v)", edited, err)
	}
	backup, err := os.ReadFile(target + ".bak")
	if err != nil || string(backup) != "one\ntwo\nthree\n" {
		t.Fatalf("backup file = (%q, %v)", backup, err)
	}

	notRegular := filepath.Join(dir, "adir")
	if err := os.Mkdir(notRegular, 0o700); err != nil {
		t.Fatal(err)
	}
	if status, _, stderr := captureApplet(t, cmdSed, []string{"-i", "s/a/b/", notRegular}, ""); status == 0 || stderr == "" {
		t.Fatalf("sed -i on a directory should fail, got (%d, %q)", status, stderr)
	}
}

func TestSedEscapeSequenceMatchesNewline(t *testing.T) {
	// \n in a BRE pattern must match an actual newline character (the way
	// GNU grep/sed treat it), not the literal letter 'n' -- a regression
	// this project's shared BRE translator once had.
	status, out, _ := captureApplet(t, cmdSed, []string{`N;s/\n/-/`}, "a\nb\n")
	if status != 0 || out != "a-b\n" {
		t.Fatalf(`sed N;s/\n/-/ = (%d, %q), want "a-b\n"`, status, out)
	}
}

// TestSedOccurrenceStepAndList covers the pieces sed grew last: the s///N
// occurrence flag, GNU's first~step addresses, l's wrapping, F and z, and the
// -s/-z/-l options.
func TestSedOccurrenceStepAndList(t *testing.T) {
	for _, c := range []struct {
		args  []string
		input string
		want  string
	}{
		// A bare number replaces only that occurrence; with g, that one and
		// everything after it.
		{[]string{"s/ /-/2"}, "a b c d\n", "a b-c d\n"},
		{[]string{"s/ /-/2g"}, "a b c d\n", "a b-c-d\n"},
		{[]string{"s/ /-/9"}, "a b c d\n", "a b c d\n"},
		// first~step selects every step'th line from first on.
		{[]string{"-n", "1~2p"}, "1\n2\n3\n4\n5\n", "1\n3\n5\n"},
		{[]string{"-n", "0~3p"}, "1\n2\n3\n4\n5\n6\n", "3\n6\n"},
		{[]string{"-n", "2~0p"}, "1\n2\n3\n", "2\n"},
		// z empties the pattern space; F prints the input's name.
		{[]string{"z;s/^$/empty/"}, "a\nb\n", "empty\nempty\n"},
		{[]string{"-n", "F"}, "a\n", "-\n"},
		// l escapes and, by default, wraps at 70 columns.
		{[]string{"-n", "l"}, "a\tb\n", "a\\tb$\n"},
		{[]string{"-n", "l 0"}, strings.Repeat("x", 80) + "\n", strings.Repeat("x", 80) + "$\n"},
		{[]string{"-n", "l 5"}, "abcdefgh\n", "abcd\\\nefgh$\n"},
		{[]string{"-n", "-l", "5", "l"}, "abcdefgh\n", "abcd\\\nefgh$\n"},
	} {
		status, out, errOut := captureApplet(t, cmdSed, c.args, c.input)
		if status != 0 || out != c.want {
			t.Fatalf("sed %v on %q = (%d, %q, %q), want %q", c.args, c.input, status, out, errOut, c.want)
		}
	}

	// The default width wraps a long line one character short of 70.
	_, out, _ := captureApplet(t, cmdSed, []string{"-n", "l"}, strings.Repeat("x", 100)+"\n")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || len(lines[0]) != 70 || !strings.HasSuffix(lines[0], "\\") {
		t.Fatalf("sed -n l wrapped as %q", out)
	}

	// -z reads and writes NUL-separated records.
	if _, out, _ = captureApplet(t, cmdSed, []string{"-z", "s/a/A/"}, "a\x00b\x00"); out != "A\x00b\x00" {
		t.Fatalf("sed -z = %q", out)
	}

	// -s gives each file its own line numbering and its own $.
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	if err := os.WriteFile(first, []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("c\nd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, out, _ = captureApplet(t, cmdSed, []string{"-s", "-n", "$p", first, second}, ""); out != "b\nd\n" {
		t.Fatalf("sed -s $p = %q", out)
	}
	if _, out, _ = captureApplet(t, cmdSed, []string{"-n", "$p", first, second}, ""); out != "d\n" {
		t.Fatalf("sed $p across files = %q", out)
	}
	// F names the file each line came from.
	if _, out, _ = captureApplet(t, cmdSed, []string{"-n", "F", first, second}, ""); out != first+"\n"+first+"\n"+second+"\n"+second+"\n" {
		t.Fatalf("sed F = %q", out)
	}
	// A zero occurrence is refused, as in the original.
	if status, _, errOut := captureApplet(t, cmdSed, []string{"s/a/b/0"}, "a\n"); status == 0 ||
		!strings.Contains(errOut, "may not be zero") {
		t.Fatalf("sed s///0 = (%d, %q)", status, errOut)
	}
}
