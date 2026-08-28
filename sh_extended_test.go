// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import "testing"

func TestShellControlFlow(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   string
	}{
		{"if true", "if true; then echo yes; fi", "yes\n"},
		{"if false with else", "if false; then echo yes; else echo no; fi", "no\n"},
		{"if elif chain", "x=2; if [ $x = 1 ]; then echo one; elif [ $x = 2 ]; then echo two; else echo other; fi", "two\n"},
		{"for over list", "for i in a b c; do echo $i; done", "a\nb\nc\n"},
		{"while with arithmetic", "i=0; while [ $i -lt 3 ]; do echo $i; i=$((i+1)); done", "0\n1\n2\n"},
		{"break in for", "for i in 1 2 3 4 5; do if [ $i = 3 ]; then break; fi; echo $i; done", "1\n2\n"},
		{"continue in for", "for i in 1 2 3 4 5; do if [ $i = 3 ]; then continue; fi; echo $i; done", "1\n2\n4\n5\n"},
		{"break only stops innermost loop", "for i in 1 2; do for j in a b c; do if [ $j = b ]; then break; fi; echo $i$j; done; done", "1a\n2a\n"},
		{"nested for", "for i in 1 2; do for j in a b; do echo $i$j; done; done", "1a\n1b\n2a\n2b\n"},
		{"empty for list runs zero times", "for x in; do echo nope; done; echo after", "after\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out, stderr := captureApplet(t, cmdSh, []string{"-c", tc.script}, "")
			if status != 0 || out != tc.want {
				t.Fatalf("sh -c %q = (%d, %q, %q), want %q", tc.script, status, out, stderr, tc.want)
			}
		})
	}
}

func TestShellArithmeticExpansion(t *testing.T) {
	cases := []struct {
		script string
		want   string
	}{
		{"echo $((2+3*4))", "14\n"},
		{"echo $((10-3)) $((10/3)) $((10%3))", "7 3 1\n"},
		{"echo $(((2+3)*4))", "20\n"},
		{"n=5; echo $((n*2))", "10\n"},
		{"echo $((-3+5))", "2\n"},
	}
	for _, tc := range cases {
		t.Run(tc.script, func(t *testing.T) {
			status, out, stderr := captureApplet(t, cmdSh, []string{"-c", tc.script}, "")
			if status != 0 || out != tc.want {
				t.Fatalf("sh -c %q = (%d, %q, %q), want %q", tc.script, status, out, stderr, tc.want)
			}
		})
	}
}

func TestShellCommandSubstitution(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   string
	}{
		{"basic assignment", `x=$(echo hello); echo "got: $x"`, "got: hello\n"},
		{"inside double quotes", `echo "today is $(echo Monday)"`, "today is Monday\n"},
		{"nested substitution", "echo $(echo $(echo nested))", "nested\n"},
		{"piped command inside", `x=$(echo hello world | wc -w); echo "words: $x"`, "words: 2\n"},
		{"used in arithmetic", "n=$(echo 5); echo $((n*2))", "10\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out, stderr := captureApplet(t, cmdSh, []string{"-c", tc.script}, "")
			if status != 0 || out != tc.want {
				t.Fatalf("sh -c %q = (%d, %q, %q), want %q", tc.script, status, out, stderr, tc.want)
			}
		})
	}
}

func TestShellForListFieldSplitting(t *testing.T) {
	// An unquoted command substitution's result is split on whitespace, the
	// same as an unquoted command argument would be; a quoted item (or a
	// quoted expansion) is protected and stays one iteration value.
	status, out, _ := captureApplet(t, cmdSh, []string{"-c",
		`for f in $(echo a b c); do echo "[$f]"; done`}, "")
	if status != 0 || out != "[a]\n[b]\n[c]\n" {
		t.Fatalf("unquoted substitution in for-list = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdSh, []string{"-c",
		`for f in "file one.txt" "file two.txt"; do echo "[$f]"; done`}, "")
	if status != 0 || out != "[file one.txt]\n[file two.txt]\n" {
		t.Fatalf("quoted literal in for-list = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdSh, []string{"-c",
		`x="a b"; for f in "$x" c; do echo "[$f]"; done`}, "")
	if status != 0 || out != "[a b]\n[c]\n" {
		t.Fatalf("quoted variable in for-list = (%d, %q)", status, out)
	}
}
