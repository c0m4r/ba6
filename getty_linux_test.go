// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseGettyArgsPositional(t *testing.T) {
	_, positional, ok := parseGettyArgs([]string{"tty1", "38400", "vt220"})
	if !ok || len(positional) != 3 || positional[0] != "tty1" || positional[1] != "38400" || positional[2] != "vt220" {
		t.Fatalf("positional = (%v, %v)", positional, ok)
	}
	_, positional, ok = parseGettyArgs([]string{"-a", "root", "ttyS0", "115200", "vt100"})
	if !ok || len(positional) != 3 || positional[0] != "ttyS0" {
		t.Fatalf("autologin positional = (%v, %v)", positional, ok)
	}
}

func TestParseGettyArgsFlags(t *testing.T) {
	opts, positional, ok := parseGettyArgs([]string{
		"-a", "root", "-n", "-l", "/bin/mylogin", "-r", "/mnt/root",
		"-L", "-J", "-i", "-8", "-p", "-t", "30", "tty1",
	})
	if !ok {
		t.Fatalf("parse failed")
	}
	if opts.autologin != "root" || !opts.skipLogin || opts.loginProgram != "/bin/mylogin" ||
		opts.chrootDir != "/mnt/root" || opts.localLine != "always" || !opts.noClear ||
		!opts.noIssue || !opts.eightBits || !opts.loginPause || opts.timeout != 30 {
		t.Fatalf("opts = %+v", opts)
	}
	if len(positional) != 1 || positional[0] != "tty1" {
		t.Fatalf("positional = %v", positional)
	}
}

func TestParseGettyArgsLongForms(t *testing.T) {
	opts, _, ok := parseGettyArgs([]string{
		"--autologin=root", "--login-program=/bin/mylogin", "--chroot=/mnt",
		"--local-line=never", "--noclear", "--noissue", "--timeout=5", "tty1",
	})
	if !ok {
		t.Fatalf("parse failed")
	}
	if opts.autologin != "root" || opts.loginProgram != "/bin/mylogin" || opts.chrootDir != "/mnt" ||
		opts.localLine != "never" || !opts.noClear || !opts.noIssue || opts.timeout != 5 {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseGettyArgsDoubleDashAndDashLine(t *testing.T) {
	_, positional, ok := parseGettyArgs([]string{"--", "-oddname", "38400"})
	if !ok || len(positional) != 2 || positional[0] != "-oddname" {
		t.Fatalf("double-dash positional = (%v, %v)", positional, ok)
	}
	_, positional, ok = parseGettyArgs([]string{"-"})
	if !ok || len(positional) != 1 || positional[0] != "-" {
		t.Fatalf("bare dash positional = (%v, %v)", positional, ok)
	}
}

func TestParseGettyArgsErrors(t *testing.T) {
	if _, _, ok := parseGettyArgs([]string{"-a"}); ok {
		t.Fatalf("-a with no value should fail")
	}
	if _, _, ok := parseGettyArgs([]string{"-t", "notanumber", "tty1"}); ok {
		t.Fatalf("invalid timeout should fail")
	}
	if _, _, ok := parseGettyArgs([]string{"-t", "-1", "tty1"}); ok {
		t.Fatalf("negative timeout should fail")
	}
	if _, _, ok := parseGettyArgs([]string{"--bogus", "tty1"}); ok {
		t.Fatalf("unsupported option should fail")
	}
}

func TestIsBaudList(t *testing.T) {
	cases := map[string]bool{
		"38400": true, "9600,38400": true, "": false, "vt100": false,
		"38400,": false, "115200x": false,
	}
	for input, want := range cases {
		if got := isBaudList(input); got != want {
			t.Errorf("isBaudList(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestGettyLocalLineMode(t *testing.T) {
	for _, mode := range []string{"auto", "always", "never"} {
		if got := gettyLocalLineMode(mode); got != mode {
			t.Errorf("gettyLocalLineMode(%q) = %q", mode, got)
		}
	}
	if got := gettyLocalLineMode(""); got != "always" {
		t.Errorf("gettyLocalLineMode(\"\") = %q, want always", got)
	}
	if got := gettyLocalLineMode("bogus"); got != "always" {
		t.Errorf("gettyLocalLineMode(bogus) = %q, want always", got)
	}
}

func TestGettyLineName(t *testing.T) {
	cases := map[string]string{
		"tty1": "tty1", "/dev/tty1": "tty1", "/dev/pts/3": "pts/3", "-": "-",
	}
	for line, want := range cases {
		if got := gettyLineName(line); got != want {
			t.Errorf("gettyLineName(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestGettyDefaultTerm(t *testing.T) {
	cases := map[string]string{
		"tty1": "linux", "/dev/tty12": "linux", "ttyS0": "vt100",
		"ttyUSB0": "vt100", "tty": "vt100",
	}
	for line, want := range cases {
		if got := gettyDefaultTerm(line); got != want {
			t.Errorf("gettyDefaultTerm(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestGettyExpandIssue(t *testing.T) {
	hostname, _ := os.Hostname()
	got := gettyExpandIssue(`Welcome to \n on \l`, "/dev/tty1")
	want := "Welcome to " + hostname + " on tty1"
	if got != want {
		t.Fatalf("gettyExpandIssue(\\n \\l) = %q, want %q", got, want)
	}
	if got := gettyExpandIssue(`escaped \\n stays literal`, "tty1"); got != `escaped \n stays literal` {
		t.Fatalf("escaped backslash mishandled: %q", got)
	}
	if got := gettyExpandIssue(`trailing backslash \`, "tty1"); got != `trailing backslash \` {
		t.Fatalf("trailing backslash mishandled: %q", got)
	}
	if got := gettyExpandIssue(`unknown \q escape`, "tty1"); got != `unknown \q escape` {
		t.Fatalf("unknown escape mishandled: %q", got)
	}
}

func TestGettyMissingLine(t *testing.T) {
	status, _, stderr := captureApplet(t, cmdGetty, nil, "")
	if status != 1 || !strings.Contains(stderr, "expected LINE") {
		t.Fatalf("missing line = (%d, %q)", status, stderr)
	}
}

func TestGettyExtraOperand(t *testing.T) {
	status, _, stderr := captureApplet(t, cmdGetty, []string{"-", "38400", "vt100", "extra"}, "")
	if status != 1 || !strings.Contains(stderr, `extra operand "extra"`) {
		t.Fatalf("extra operand = (%d, %q)", status, stderr)
	}
}

func TestGettyUnopenableLine(t *testing.T) {
	status, _, stderr := captureApplet(t, cmdGetty, []string{"ba6-getty-test-does-not-exist"}, "")
	if status != 1 || !strings.Contains(stderr, "cannot open") {
		t.Fatalf("unopenable line = (%d, %q)", status, stderr)
	}
}
