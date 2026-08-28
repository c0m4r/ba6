// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChecksumNameEscapeRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		escaped string
		marked  bool
	}{
		{"plain.txt", "plain.txt", false},
		{"with space.txt", "with space.txt", false},
		{"tab\there", "tab\there", false}, // only \, LF and CR are escaped
		{`back\slash`, `back\\slash`, true},
		{"new\nline", `new\nline`, true},
		{"carriage\rreturn", `carriage\rreturn`, true},
		{"both\\\n", `both\\\n`, true},
	}
	for _, c := range cases {
		got, marked := escapeChecksumName(c.name)
		if got != c.escaped || marked != c.marked {
			t.Errorf("escapeChecksumName(%q) = (%q, %v), want (%q, %v)", c.name, got, marked, c.escaped, c.marked)
		}
		if !marked {
			continue
		}
		back, ok := unescapeChecksumName(got)
		if !ok || back != c.name {
			t.Errorf("unescapeChecksumName(%q) = (%q, %v), want (%q, true)", got, back, ok, c.name)
		}
	}
	for _, bad := range []string{`trailing\`, `bad\qescape`} {
		if _, ok := unescapeChecksumName(bad); ok {
			t.Errorf("unescapeChecksumName(%q) should reject", bad)
		}
	}
}

// A checksum list written for a name needing escapes must be readable back by
// -c, which is what makes the files interchangeable with the originals'.
func TestChecksumEscapedLineRoundTripsThroughCheck(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "odd\\name\nhere")
	if err := os.WriteFile(name, []byte("payload"), 0o600); err != nil {
		t.Skipf("filesystem rejects the name: %v", err)
	}
	status, stdout, stderr := captureApplet(t, cmdMd5sum, []string{name}, "")
	if status != 0 || !strings.HasPrefix(stdout, `\`) {
		t.Fatalf("md5sum = (%d, %q, %q); want a backslash-marked line", status, stdout, stderr)
	}
	list := filepath.Join(dir, "list.md5")
	if err := os.WriteFile(list, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr = captureApplet(t, cmdMd5sum, []string{"-c", list}, "")
	if status != 0 || !strings.HasSuffix(stdout, ": OK\n") {
		t.Fatalf("md5sum -c = (%d, %q, %q)", status, stdout, stderr)
	}
}

// Malformed lines are only ever a warning: alongside a valid line they leave
// the exit status at zero, which is what scripts gate on.
func TestChecksumMalformedLinesWarnButDoNotFail(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data")
	if err := os.WriteFile(file, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, sum, _ := captureApplet(t, cmdMd5sum, []string{file}, "")
	list := filepath.Join(dir, "list.md5")
	if err := os.WriteFile(list, []byte(sum+"garbage\nmore garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdMd5sum, []string{"-c", list}, "")
	if status != 0 {
		t.Fatalf("status = %d, want 0 (bad lines only warn); stderr=%q", status, stderr)
	}
	if !strings.HasSuffix(stdout, ": OK\n") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "WARNING: 2 lines are improperly formatted") {
		t.Fatalf("stderr = %q", stderr)
	}

	// With nothing usable at all it is an error instead, and the per-category
	// warning gives way to a single diagnostic.
	onlyBad := filepath.Join(dir, "bad.md5")
	if err := os.WriteFile(onlyBad, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr = captureApplet(t, cmdMd5sum, []string{"-c", onlyBad}, "")
	if status != 1 || !strings.Contains(stderr, "no properly formatted checksum lines found") {
		t.Fatalf("empty list = (%d, %q)", status, stderr)
	}
}

func TestChecksumWarningsAndPlurals(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, goodLine, _ := captureApplet(t, cmdMd5sum, []string{good}, "")
	zero := strings.Repeat("0", 32)
	missing := filepath.Join(dir, "absent")
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "all.md5")
	body := goodLine + "garbage\n" +
		fmt.Sprintf("%s  %s\n", zero, missing) +
		fmt.Sprintf("%s  %s\n", zero, other)
	if err := os.WriteFile(list, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdMd5sum, []string{"-c", list}, "")
	if status != 1 {
		t.Fatalf("status = %d, want 1", status)
	}
	if !strings.Contains(stdout, "FAILED open or read") {
		t.Fatalf("stdout = %q; want an open-or-read marker", stdout)
	}
	for _, want := range []string{
		"WARNING: 1 line is improperly formatted",
		"WARNING: 1 listed file could not be read",
		"WARNING: 1 computed checksum did NOT match",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q; got %q", want, stderr)
		}
	}
	// --status stays silent and reports only through the exit code.
	status, stdout, stderr = captureApplet(t, cmdMd5sum, []string{"-c", "--status", list}, "")
	if status != 1 || stdout != "" || stderr != "" {
		t.Fatalf("--status = (%d, %q, %q)", status, stdout, stderr)
	}
}

func TestShellQuoteName(t *testing.T) {
	cases := map[string]string{
		"plain.txt":   "plain.txt",
		"a/b_c-d.txt": "a/b_c-d.txt",
		"with space":  "'with space'",
		"a'b":         `"a'b"`, // quote alone is cheaper in double quotes
		"'":           `"'"`,   //
		`a"b`:         `'a"b'`, // a double quote forces single quotes
		`a'b"c`:       `'a'\''b"c'`,
		`a'b\c`:       `'a'\''b\c'`,
		"a\nb":        `'a'$'\n''b'`,
		"a\n\tb":      `'a'$'\n\t''b'`, // adjacent control bytes share one group
		"a\n":         `'a'$'\n'`,
		"\n'":         `$'\n'\'''`,
		"''":          `"''"`,  // quotes alone still fit the double-quote form
		"a=b":         "'a=b'", // '=' is not left bare
		"a{}b":        "a{}b",  // but braces are
	}
	for input, want := range cases {
		if got := shellQuoteName(input); got != want {
			t.Errorf("shellQuoteName(%q) = %q, want %q", input, got, want)
		}
	}
}
