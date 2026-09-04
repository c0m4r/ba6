// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every expectation below was diffed against GNU coreutils on a live system.

func TestSortFieldKeys(t *testing.T) {
	const colons = "a:1:z\nb:3:y\nc:2:x\n"
	const blanks = "  b 2\na 10\n c 1\n"
	for _, c := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"-t:", "-k2"}, colons, "a:1:z\nc:2:x\nb:3:y\n"},
		{[]string{"-t:", "-k2,2"}, colons, "a:1:z\nc:2:x\nb:3:y\n"},
		{[]string{"-t:", "-k3"}, colons, "c:2:x\nb:3:y\na:1:z\n"},
		{[]string{"-t:", "-k2nr"}, colons, "b:3:y\nc:2:x\na:1:z\n"},
		// The default separator leaves the run of blanks with the field that
		// follows it, so every key here starts with a space.
		{[]string{"-k2"}, blanks, " c 1\na 10\n  b 2\n"},
		{[]string{"-k2n"}, blanks, " c 1\n  b 2\na 10\n"},
		{[]string{"-k1"}, blanks, "  b 2\n c 1\na 10\n"},
		// A character offset counts from the start of the field.
		{[]string{"-k1.2"}, "xb\nya\nzc\n", "ya\nxb\nzc\n"},
		{[]string{"-k1.3"}, "ab\na\nabc\n", "a\nab\nabc\n"},
		{[]string{"-k2.2"}, "q abc\nq bbb\nq acd\n", "q abc\nq acd\nq bbb\n"},
	} {
		status, stdout, stderr := captureApplet(t, cmdSort, c.args, c.input)
		if status != 0 || stdout != c.want {
			t.Errorf("sort %v = (%d, %q, %q), want %q", c.args, status, stdout, stderr, c.want)
		}
	}
}

func TestSortLastResortAndKeyModifiers(t *testing.T) {
	// Three lines whose -k2,2 keys are all equal, so only the last-resort
	// comparison can order them.
	const equal = "b 1\na 1\nc 1\n"
	for _, c := range []struct {
		args []string
		want string
	}{
		// The whole line breaks the tie.
		{[]string{"-k2,2"}, "a 1\nb 1\nc 1\n"},
		// A global -r reverses the last resort as well.
		{[]string{"-k2,2", "-r"}, "c 1\nb 1\na 1\n"},
		// A key's own r does not.
		{[]string{"-k2,2r"}, "a 1\nb 1\nc 1\n"},
		// -s drops the last resort, leaving input order.
		{[]string{"-k2,2", "-s"}, "b 1\na 1\nc 1\n"},
		{[]string{"-k2,2", "-s", "-r"}, "b 1\na 1\nc 1\n"},
		// So does -u, which then keeps the first line of the equal run.
		{[]string{"-k2,2", "-u"}, "b 1\n"},
	} {
		status, stdout, stderr := captureApplet(t, cmdSort, c.args, equal)
		if status != 0 || stdout != c.want {
			t.Errorf("sort %v = (%d, %q, %q), want %q", c.args, status, stdout, stderr, c.want)
		}
	}
}

func TestSortOrderingsAndOutputOptions(t *testing.T) {
	for _, c := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"-M"}, "Mar\nJan\nFeb\nfoo\n", "foo\nJan\nFeb\nMar\n"},
		{[]string{"-g"}, "1e3\n5\n-2.5\nabc\n", "abc\n-2.5\n5\n1e3\n"},
		{[]string{"-h"}, "1K\n2M\n500\n1G\n", "500\n1K\n2M\n1G\n"},
		{[]string{"-d"}, "a-b\nab\na b\n", "a b\na-b\nab\n"},
		{[]string{"-z"}, "b\x00a\x00", "a\x00b\x00"},
	} {
		status, stdout, stderr := captureApplet(t, cmdSort, c.args, c.input)
		if status != 0 || stdout != c.want {
			t.Errorf("sort %v = (%d, %q, %q), want %q", c.args, status, stdout, stderr, c.want)
		}
	}

	// -C reports through the exit status only; -c names the first bad line.
	if status, stdout, stderr := captureApplet(t, cmdSort, []string{"-C"}, "y\nx\n"); status != 1 || stdout != "" || stderr != "" {
		t.Errorf("sort -C = (%d, %q, %q)", status, stdout, stderr)
	}
	if status, _, stderr := captureApplet(t, cmdSort, []string{"-c"}, "y\nx\n"); status != 1 ||
		!strings.Contains(stderr, "-:2: disorder: x") {
		t.Errorf("sort -c = (%d, %q)", status, stderr)
	}

	// -o writes the result to a file instead of standard output.
	out := filepath.Join(t.TempDir(), "sorted")
	status, stdout, stderr := captureApplet(t, cmdSort, []string{"-o", out}, "b\na\n")
	if status != 0 || stdout != "" {
		t.Fatalf("sort -o = (%d, %q, %q)", status, stdout, stderr)
	}
	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "a\nb\n" {
		t.Fatalf("sort -o wrote %q", written)
	}
}

func TestSortRejectsBadKeysAndSeparators(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-k0"}, "field number is zero"},
		{[]string{"-kx"}, "invalid number at field start"},
		{[]string{"-k1Z"}, "invalid number at field start"},
		{[]string{"-t", "ab"}, "multi-character tab 'ab'"},
	} {
		status, _, stderr := captureApplet(t, cmdSort, c.args, "a\n")
		if status != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("sort %v = (%d, %q), want %q", c.args, status, stderr, c.want)
		}
	}
}

func TestCutBytesComplementAndOutputDelimiter(t *testing.T) {
	for _, c := range []struct {
		args  []string
		input string
		want  string
	}{
		// -b counts bytes where -c counts characters.
		{[]string{"-b1"}, "ąb\n", "\xc4\n"},
		{[]string{"-c1"}, "ąb\n", "ą\n"},
		{[]string{"-b", "2-"}, "abcdef\n", "bcdef\n"},
		// Selected runs are joined with the output delimiter, and a range
		// past the end of the line adds nothing.
		{[]string{"-c1-2,4-5", "--output-delimiter=:"}, "abcdef\n", "ab:de\n"},
		{[]string{"-c1-2,9-10", "--output-delimiter=:"}, "abcdef\n", "ab\n"},
		// Overlapping ranges are one run, not two.
		{[]string{"-c1-1,1-2", "--output-delimiter=:"}, "abc\n", "ab\n"},
		{[]string{"-c2-3", "--complement"}, "abcdef\n", "adef\n"},
		{[]string{"-c2-3", "--complement", "--output-delimiter=:"}, "abcdef\n", "a:def\n"},
		{[]string{"-d:", "-f2", "--complement"}, "a:b:c\n", "a:c\n"},
		{[]string{"-d:", "-f1,2", "--output-delimiter", "-"}, "a:b:c\n", "a-b\n"},
		// -n is accepted and changes nothing.
		{[]string{"-n", "-b1-2"}, "abc\n", "ab\n"},
		{[]string{"-d:", "-f2", "-z"}, "a:b\x00c:d\x00", "b\x00d\x00"},
		{[]string{"--delimiter=:", "--fields=2"}, "a:b\n", "b\n"},
		{[]string{"--characters=1-2"}, "abc\n", "ab\n"},
	} {
		status, stdout, stderr := captureApplet(t, cmdCut, c.args, c.input)
		if status != 0 || stdout != c.want {
			t.Errorf("cut %v = (%d, %q, %q), want %q", c.args, status, stdout, stderr, c.want)
		}
	}
}

func TestCutUsageErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-f1", "-c2"}, "only one list may be specified"},
		{[]string{}, "you must specify a list of bytes, characters, or fields"},
		{[]string{"-f0"}, "fields are numbered from 1"},
		{[]string{"-c0"}, "byte/character positions are numbered from 1"},
		{[]string{"-f", "x"}, "invalid field value 'x'"},
		{[]string{"-c", "a"}, "invalid byte/character position 'a'"},
		{[]string{"-c3-1"}, "invalid decreasing range"},
		{[]string{"-d", "ab", "-f1"}, "the delimiter must be a single character"},
	} {
		status, _, stderr := captureApplet(t, cmdCut, c.args, "abc\n")
		if status != 1 || !strings.Contains(stderr, c.want) ||
			!strings.Contains(stderr, "Try 'cut --help' for more information.") {
			t.Errorf("cut %v = (%d, %q), want %q", c.args, status, stderr, c.want)
		}
	}
}

// duTestTree builds a small tree whose apparent sizes and inode counts are
// fixed, so du's arithmetic can be checked without depending on the block
// layout of whatever filesystem the test runs on.
func duTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "c"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "f1"), make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b", "f2"), make([]byte, 100000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "c", "f3"), make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDuApparentSizesDepthAndTotals(t *testing.T) {
	root := duTestTree(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		// A directory contributes no apparent size of its own.
		{[]string{"-b", root}, "100000\t" + root + "/a/b\n105000\t" + root + "/a\n10\t" + root + "/c\n105010\t" + root + "\n"},
		{[]string{"-b", "-d1", root}, "105000\t" + root + "/a\n10\t" + root + "/c\n105010\t" + root + "\n"},
		{[]string{"-b", "-s", root}, "105010\t" + root + "\n"},
		{[]string{"-b", "-c", "-d0", root}, "105010\t" + root + "\n105010\ttotal\n"},
		// -S reports a directory without its subdirectories.
		{[]string{"-b", "-S", "-d1", root}, "5000\t" + root + "/a\n10\t" + root + "/c\n0\t" + root + "\n"},
		// -a adds the files, and --exclude drops a subtree.
		{[]string{"-b", "-a", "-d1", root}, "105000\t" + root + "/a\n10\t" + root + "/c\n105010\t" + root + "\n"},
		{[]string{"-b", "--exclude=b", "-d1", root}, "5000\t" + root + "/a\n10\t" + root + "/c\n5010\t" + root + "\n"},
		{[]string{"-b", "--exclude=f*", "-d1", root}, "0\t" + root + "/a\n0\t" + root + "/c\n0\t" + root + "\n"},
		// -t keeps only the entries on one side of the threshold.
		{[]string{"-b", "-t", "100000", root}, "100000\t" + root + "/a/b\n105000\t" + root + "/a\n105010\t" + root + "\n"},
		{[]string{"-b", "-t", "-100", root}, "10\t" + root + "/c\n"},
		// Every entry is one inode, directories included.
		{[]string{"--inodes", root}, "2\t" + root + "/a/b\n4\t" + root + "/a\n2\t" + root + "/c\n7\t" + root + "\n"},
	} {
		status, stdout, stderr := captureApplet(t, cmdDu, c.args, "")
		if status != 0 || sortedLines(stdout) != sortedLines(c.want) {
			t.Errorf("du %v = (%d, %q, %q), want %q", c.args, status, stdout, stderr, c.want)
		}
	}
}

func TestDuBlockSizeAndNulOutput(t *testing.T) {
	root := duTestTree(t)
	// A block size written as a bare unit is echoed after every value.
	status, stdout, stderr := captureApplet(t, cmdDu, []string{"-d0", "-B", "K", root}, "")
	if status != 0 || !strings.HasSuffix(strings.TrimSuffix(stdout, "\n"), "\t"+root) ||
		!strings.HasSuffix(strings.SplitN(stdout, "\t", 2)[0], "K") {
		t.Fatalf("du -B K = (%d, %q, %q)", status, stdout, stderr)
	}
	// One with a leading count is not.
	status, stdout, _ = captureApplet(t, cmdDu, []string{"-d0", "-B", "1K", root}, "")
	if status != 0 || strings.ContainsAny(strings.SplitN(stdout, "\t", 2)[0], "KMG") {
		t.Fatalf("du -B 1K = (%d, %q)", status, stdout)
	}
	status, stdout, _ = captureApplet(t, cmdDu, []string{"-0", "-d0", root}, "")
	if status != 0 || !strings.HasSuffix(stdout, "\x00") || strings.Contains(stdout, "\n") {
		t.Fatalf("du -0 = (%d, %q)", status, stdout)
	}
	// A repeated operand is counted, and listed, only once.
	status, stdout, _ = captureApplet(t, cmdDu, []string{"-b", "-s", root, root}, "")
	if status != 0 || stdout != "105010\t"+root+"\n" {
		t.Fatalf("du -s on a repeated operand = (%d, %q)", status, stdout)
	}
	if status, _, stderr := captureApplet(t, cmdDu, []string{"-d", "x", root}, ""); status != 1 ||
		!strings.Contains(stderr, "invalid maximum depth 'x'") {
		t.Fatalf("du -d x = (%d, %q)", status, stderr)
	}
	if status, _, stderr := captureApplet(t, cmdDu, []string{"-B", "0", root}, ""); status != 1 ||
		!strings.Contains(stderr, "invalid -B argument '0'") {
		t.Fatalf("du -B 0 = (%d, %q)", status, stderr)
	}
}

// sortedLines makes a comparison independent of the order the kernel returns
// directory entries in, which is the order du walks them in.
func sortedLines(text string) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i := 1; i < len(lines); i++ {
		for j := i; j > 0 && lines[j] < lines[j-1]; j-- {
			lines[j], lines[j-1] = lines[j-1], lines[j]
		}
	}
	return strings.Join(lines, "\n")
}
