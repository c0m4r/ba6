// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepPatternAfterDoubleDash(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdGrep, []string{"--", "needle"}, "a needle here\n")
	if status != 0 || stdout != "a needle here\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestGrepMaxCountZeroSelectsNothing(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdGrep, []string{"-m0", "needle"}, "needle\n")
	if status != 1 || stdout != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

// TestAwkBashCompletionPrograms covers the programs the bash completions run,
// which need brace-delimited blocks, if/else, sub() and comments. The expected
// output is what gawk produces for the same input.
func TestAwkBashCompletionPrograms(t *testing.T) {
	cases := []struct {
		name, program, input, output string
	}{
		{
			name:    "available interfaces",
			program: `/^[^ \t]/ { if ($1 ~ /^[0-9]+:/) { sub(/@.*/, "", $2); print $2 } else { print $1 } }`,
			input:   "1: lo: <UP> mtu 65536\n2: veth0@if7: <UP> mtu 1500\n    link/ether 00:00:00:00:00:00\n",
			output:  "lo:\nveth0\n",
		},
		{
			name:    "systemd units",
			program: "# leading comment\nsub(/\\.service$/, \"\", $1) { print $1; next }\nsub(/\\.service$/, \"\", $2) { print $2 }\n",
			input:   "httpd.service loaded active\n* iptables.service not-found dead\nfoo.socket loaded active\n",
			output:  "httpd\niptables\n",
		},
		{
			name:    "else branch",
			program: `{ if (NF > 2) print "wide"; else print "narrow" }`,
			input:   "a b c\na b\n",
			output:  "wide\nnarrow\n",
		},
		{
			name:    "gsub counts replacements",
			program: `{ print gsub(/o/, "0"), $0 }`,
			input:   "foo boo\n",
			output:  "4 f00 b00\n",
		},
		{
			name:    "field assignment rebuilds the record",
			program: `{ $2 = "X"; print }`,
			input:   "a b c\n",
			output:  "a X c\n",
		},
		{
			name:    "ampersand keeps the match",
			program: `{ sub(/b+/, "[&]"); sub(/a/, "\\&"); print }`,
			input:   "abba\n",
			output:  "&[bb]a\n",
		},
		{
			name:    "bare regular expression matches the record",
			program: `!/skip/ { print $1 }`,
			input:   "keep me\nskip me\n",
			output:  "keep\n",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			status, stdout, stderr := captureApplet(t, cmdAwk, []string{test.program}, test.input)
			if status != 0 || stdout != test.output || stderr != "" {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
			}
		})
	}
}

func TestEchoControlCStopsBeforeNewline(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdEcho, []string{"-e", `before\cafter`}, "")
	if status != 0 || stdout != "before" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestCatShowEndsDoesNotMarkUnterminatedData(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdCat, []string{"-E"}, "abc")
	if status != 0 || stdout != "abc" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestSortReverseCheck(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdSort, []string{"-r", "-c"}, "b\na\n")
	if status != 0 || stdout != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestSortNumericUniqueUsesNumericKey(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdSort, []string{"-n", "-u"}, "1\n01\n2\n")
	if status != 0 || len(strings.Split(strings.TrimSpace(stdout), "\n")) != 2 {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestCutRejectsEmptyDelimiter(t *testing.T) {
	status, _, _ := captureApplet(t, cmdCut, []string{"-d", "", "-f", "1"}, "abc\n")
	if status == 0 {
		t.Fatal("empty delimiter was accepted")
	}
}

func TestWcCountsRuneAcrossInternalBufferBoundary(t *testing.T) {
	input := strings.Repeat("a", 64*1024-1) + "€"
	count, err := countWc(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if count.bytes != int64(len(input)) || count.chars != 64*1024 {
		t.Fatalf("bytes=%d chars=%d", count.bytes, count.chars)
	}
}

func TestParseCountRejectsNegativeAndOverflow(t *testing.T) {
	for _, value := range []string{"-1", "9223372036854775807K"} {
		if _, err := parseCount(value); err == nil {
			t.Fatalf("parseCount(%q) succeeded", value)
		}
	}
}

func TestTrDoubleDashAllowsHyphenSet(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdTr, []string{"--", "-d", "X"}, "-d")
	if status != 0 || stdout != "XX" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestTrRejectsExtraOperand(t *testing.T) {
	status, _, _ := captureApplet(t, cmdTr, []string{"a", "b", "c"}, "a")
	if status == 0 {
		t.Fatal("extra operand was accepted")
	}
}

func TestTrOctalEscape(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdTr, []string{`\101`, "Z"}, "A")
	if status != 0 || stdout != "Z" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestLsUsesOneEntryPerLineForNonTerminal(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status, stdout, stderr := captureApplet(t, cmdLs, []string{dir}, "")
	if status != 0 || stdout != "alpha\nbeta\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestLsLongShowsDirectorySymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdLs, []string{"-l", dir}, "")
	if status != 0 || !strings.Contains(stdout, "link -> target") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestLsDereferencesDirectorySymlinkWithoutLongFormat(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "item"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("real", link); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdLs, []string{link}, "")
	if status != 0 || stdout != "item\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}

func TestBufferedOutputFailureReturnsNonzero(t *testing.T) {
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if os.IsNotExist(err) {
		t.Skip("/dev/full is unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	dir := t.TempDir()
	inPath := filepath.Join(dir, "input")
	if err := os.WriteFile(inPath, []byte("b\na\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(inPath)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	errFile, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer errFile.Close()
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, full, errFile
	status := cmdSort(nil)
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	if status == 0 {
		t.Fatal("buffered write failure returned success")
	}
}
