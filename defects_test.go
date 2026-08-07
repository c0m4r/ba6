// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

// Regression tests for the behaviour differences found by comparing each applet
// against the tool it replaces. Every case here is one that COVERAGE.md once
// listed as a defect; the expected values are what the original prints.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rmdir must use rmdir(2), which refuses anything that is not a directory.
// os.Remove falls back to unlink(2) and silently deleted the file instead.
func TestRmdirRefusesRegularFile(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "F")
	if err := os.WriteFile(victim, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdRmdir, []string{victim}, "")
	if status != 1 {
		t.Fatalf("status=%d, want 1", status)
	}
	if !strings.Contains(stderr, "Not a directory") {
		t.Fatalf("stderr=%q, want a not-a-directory message", stderr)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("rmdir deleted a regular file: %v", err)
	}
}

func TestDirnameTrailingSlash(t *testing.T) {
	cases := map[string]string{
		"/a/b/":   "/a",
		"/a/b":    "/a",
		"a":       ".",
		"/":       "/",
		"//":      "/",
		"///":     "/",
		"":        ".",
		"/a//b//": "/a",
		"a/":      ".",
		"//a":     "/",
		"//a//b":  "//a",
		"../a":    "..",
	}
	for input, want := range cases {
		if got := parentPath(input); got != want {
			t.Errorf("parentPath(%q) = %q, want %q", input, got, want)
		}
	}
}

// printf reports an operand that is not a number and exits 1, but still writes
// a value; a missing operand is a plain zero and no error at all.
func TestPrintfRejectsNonNumeric(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdPrintf, []string{"%d\n", "abc"}, "")
	if status != 1 || stdout != "0\n" {
		t.Fatalf("status=%d stdout=%q", status, stdout)
	}
	if !strings.Contains(stderr, "expected a numeric value") {
		t.Fatalf("stderr=%q", stderr)
	}

	status, stdout, stderr = captureApplet(t, cmdPrintf, []string{"%d\n", "12abc"}, "")
	if status != 1 || stdout != "12\n" {
		t.Fatalf("status=%d stdout=%q", status, stdout)
	}
	if !strings.Contains(stderr, "not completely converted") {
		t.Fatalf("stderr=%q", stderr)
	}

	if status, stdout, _ = captureApplet(t, cmdPrintf, []string{"%d\n"}, ""); status != 0 || stdout != "0\n" {
		t.Fatalf("missing operand: status=%d stdout=%q", status, stdout)
	}
	if status, stdout, _ = captureApplet(t, cmdPrintf, []string{"%d\n", "'A"}, ""); status != 0 || stdout != "65\n" {
		t.Fatalf("character constant: status=%d stdout=%q", status, stdout)
	}
}

// Values come from first+i*step, so no rounding error accumulates, and the
// operands' spelling fixes the number of decimals.
func TestSeqDoesNotAccumulateError(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"0.1", "0.1", "0.3"}, "0.1\n0.2\n0.3\n"},
		{[]string{"1", "0.5", "3"}, "1.0\n1.5\n2.0\n2.5\n3.0\n"},
		{[]string{"1", "3"}, "1\n2\n3\n"},
		{[]string{"1e2", "1e2", "3e2"}, "100\n200\n300\n"},
		{[]string{"1", "0.10", "1.5"}, "1.00\n1.10\n1.20\n1.30\n1.40\n1.50\n"},
		{[]string{"-w", "8", "12"}, "08\n09\n10\n11\n12\n"},
		{[]string{"5", "1"}, ""},
	}
	for _, c := range cases {
		status, stdout, stderr := captureApplet(t, cmdSeq, c.args, "")
		if status != 0 || stdout != c.want {
			t.Errorf("seq %v = %q (status %d, stderr %q), want %q", c.args, stdout, status, stderr, c.want)
		}
	}
}

// mktemp replaces exactly the run of X's. os.CreateTemp substituted a decimal
// number of arbitrary length, so "XXXX" became names like "3160348960".
func TestMktempHonoursTemplate(t *testing.T) {
	dir := t.TempDir()
	status, stdout, stderr := captureApplet(t, cmdMktemp, []string{"-p", dir, "fooXXXX.txt"}, "")
	if status != 0 {
		t.Fatalf("status=%d stderr=%q", status, stderr)
	}
	name := filepath.Base(strings.TrimSpace(stdout))
	if len(name) != len("fooXXXX.txt") {
		t.Fatalf("name %q does not match the template's length", name)
	}
	if !strings.HasPrefix(name, "foo") || !strings.HasSuffix(name, ".txt") {
		t.Fatalf("name %q lost the literal parts of the template", name)
	}
	if strings.Contains(name, "X") {
		t.Fatalf("name %q still contains placeholders", name)
	}
	if _, err := os.Stat(strings.TrimSpace(stdout)); err != nil {
		t.Fatalf("mktemp did not create the file: %v", err)
	}

	status, _, stderr = captureApplet(t, cmdMktemp, []string{"-p", dir, "XX"}, "")
	if status != 1 || !strings.Contains(stderr, "too few X's") {
		t.Fatalf("short template: status=%d stderr=%q", status, stderr)
	}
}

// -b marks the file as binary with an asterisk, which is what distinguishes the
// two modes in a checksum file; --quiet outside -c is rejected.
func TestSha256sumBinaryMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const digest = "98ea6e4f216f2fb4b69fff9b3a44842c38686ca685f3f55dc48c5d3fb1107be4"

	status, stdout, _ := captureApplet(t, cmdSha256sum, []string{"-b", path}, "")
	if status != 0 || stdout != digest+" *"+path+"\n" {
		t.Fatalf("binary mode: status=%d stdout=%q", status, stdout)
	}
	status, stdout, _ = captureApplet(t, cmdSha256sum, []string{path}, "")
	if status != 0 || stdout != digest+"  "+path+"\n" {
		t.Fatalf("text mode: status=%d stdout=%q", status, stdout)
	}
	status, _, stderr := captureApplet(t, cmdSha256sum, []string{"--quiet", path}, "")
	if status != 1 || !strings.Contains(stderr, "only when verifying") {
		t.Fatalf("misplaced --quiet: status=%d stderr=%q", status, stderr)
	}
}

// Each fsck.extN name reports itself, not the one whose code path they share.
func TestFsckExtNamesItself(t *testing.T) {
	for name, fn := range map[string]applet{
		"fsck.ext2": cmdFsckExt2,
		"fsck.ext3": cmdFsckExt3,
		"fsck.ext4": cmdFsckExt4,
	} {
		_, _, stderr := captureApplet(t, fn, []string{"--bogus"}, "")
		if !strings.HasPrefix(stderr, name+":") {
			t.Errorf("%s reported itself as %q", name, stderr)
		}
	}
}

// -h rounds up, as the coreutils tools do: 3000 bytes is 3.0K, not 2.9K, and a
// value that rounds up to a whole 1024 moves to the next unit.
func TestHumanSizeRoundsUp(t *testing.T) {
	cases := map[int64]string{
		0: "0", 1023: "1023", 1024: "1.0K", 1025: "1.1K", 3000: "3.0K",
		5000: "4.9K", 5119: "5.0K", 9999: "9.8K", 10229: "10K", 10240: "10K",
		10485: "11K", 999999: "977K", 1047552: "1023K", 1047553: "1.0M",
		1048575: "1.0M", 1048576: "1.0M", 10485760: "10M",
		1073741823: "1.0G", 1500000000: "1.4G",
	}
	for size, want := range cases {
		if got := humanSize(size); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", size, got, want)
		}
	}
}

// The long form and -G have to agree: the primary group first, then the
// supplementary ones ascending.
func TestIdGroupOrder(t *testing.T) {
	got := orderGroupIDs("1001", []string{"944", "998", "1001", "951", "957"})
	want := []string{"1001", "944", "951", "957", "998"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("orderGroupIDs = %v, want %v", got, want)
	}
	if got := orderGroupIDs("0", []string{"957", "0"}); strings.Join(got, ",") != "0,957" {
		t.Fatalf("orderGroupIDs = %v, want [0 957]", got)
	}
}

// free reports amounts the way procps does: IEC suffixes, one decimal below
// ten, and truncation above it.
func TestScaleMemoryMatchesProcps(t *testing.T) {
	cases := map[uint64]string{
		0: "0B", 512: "512B", 29263302656: "27Gi", 6960316416: "6.5Gi",
		12576038912: "11Gi", 159666176: "152Mi", 17179865088: "15Gi",
	}
	for value, want := range cases {
		if got := scaleMemory(value); got != want {
			t.Errorf("scaleMemory(%d) = %q, want %q", value, got, want)
		}
	}
}

// /proc/net stores addresses as little-endian 32-bit words. Printing the hex as
// it appears gave an address that was not merely unformatted but wrong.
func TestSocketAddressDecoding(t *testing.T) {
	cases := map[string]string{
		"0100007F:0016":                         "127.0.0.1:22",
		"00000000:0000":                         "0.0.0.0:*",
		"00000000000000000000000000000000:06B4": "*:1716",
		"00000000000000000000000001000000:0277": "[::1]:631",
		"410F002A7852B11C6D3D9142BC68F380:E794": "[2a00:f41:1cb1:5278:4291:3d6d:80f3:68bc]:59284",
	}
	for raw, want := range cases {
		if got := decodeSocketAddr(raw); got != want {
			t.Errorf("decodeSocketAddr(%q) = %q, want %q", raw, got, want)
		}
	}
}

// xargs splits on blanks and newlines alike, honours quotes and backslashes,
// and expands nothing.
func TestXargsInputSplitting(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"a\nb\n", []string{"a", "b"}},
		{"'a b' c\n", []string{"a b", "c"}},
		{"a\\ b c\n", []string{"a b", "c"}},
		{"$HOME\n", []string{"$HOME"}},
		{"  \n\n a \n", []string{"a"}},
		{"", nil},
	}
	for _, c := range cases {
		got, err := splitXargsInput(c.input)
		if err != nil {
			t.Errorf("splitXargsInput(%q): %v", c.input, err)
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
			t.Errorf("splitXargsInput(%q) = %v, want %v", c.input, got, c.want)
		}
	}
	if _, err := splitXargsInput("x'y z\n"); err == nil {
		t.Error("an unterminated quote should be an error")
	}
}

// -n and -I accept their value attached, separately, or after an equals sign.
func TestOptionArgumentForms(t *testing.T) {
	cases := []struct {
		args  []string
		forms []string
		want  string
	}{
		{[]string{"-n1"}, []string{"-n", "--max-args"}, "1"},
		{[]string{"-n", "1"}, []string{"-n", "--max-args"}, "1"},
		{[]string{"--max-args=1"}, []string{"-n", "--max-args"}, "1"},
		{[]string{"-I{}"}, []string{"-I", "--replace"}, "{}"},
		{[]string{"--replace={}"}, []string{"-I", "--replace"}, "{}"},
	}
	for _, c := range cases {
		got, _, ok := optionArgument(c.args, 0, c.forms...)
		if !ok || got != c.want {
			t.Errorf("optionArgument(%v) = %q, %v; want %q", c.args, got, ok, c.want)
		}
	}
	if _, _, ok := optionArgument([]string{"-n"}, 0, "-n"); ok {
		t.Error("a trailing option with no value should not report success")
	}
}

// A builtin runs in the shell's own process, so its redirections have to be
// applied here; passing the raw tokens through made "echo hi > o" print the
// redirection instead of performing it.
func TestShellBuiltinRedirection(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "o")
	status, stdout, stderr := captureApplet(t, cmdSh, []string{"-c", "echo hi > " + target}, "")
	if status != 0 || stdout != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("redirection created no file: %v", err)
	}
	if string(data) != "hi\n" {
		t.Fatalf("file contains %q, want %q", data, "hi\n")
	}

	status, _, _ = captureApplet(t, cmdSh, []string{"-c", "echo two >> " + target}, "")
	if status != 0 {
		t.Fatalf("append: status=%d", status)
	}
	if data, err = os.ReadFile(target); err != nil || string(data) != "hi\ntwo\n" {
		t.Fatalf("append gave %q (%v)", data, err)
	}
}

// Inside double quotes a backslash escapes only $ ` " \ and newline, so a
// format string reaches printf with its escapes intact.
func TestShellKeepsBackslashInDoubleQuotes(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdSh, []string{"-c", `printf "%s\n" x`}, "")
	if status != 0 || stdout != "x\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
}
