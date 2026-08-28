// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The expected outputs below were diffed byte-for-byte against GNU coreutils
// 9.7 on the same inputs.

func TestExpandBasics(t *testing.T) {
	cases := []struct {
		args []string
		in   string
		want string
	}{
		{nil, "a\tb\n", "a       b\n"},
		{nil, "   xx\n  \txx\na\tb mid\n        xx\n", "   xx\n        xx\na       b mid\n        xx\n"},
		{nil, "a\b\tb\n", "a\b        b\n"},
		{[]string{"-t", "4"}, "1234567\t89\n", "1234567 89\n"},
		{[]string{"-t", "4"}, "a\tb\n", "a   b\n"},
		{[]string{"-t", "4,8"}, "1234567890\tx\n", "1234567890 x\n"},
		{[]string{"-t", "4,8,+2"}, "1234567890\tx\n", "1234567890  x\n"},
		{[]string{"-t", "4,8,+2"}, "1234567890123\tx\n", "1234567890123 x\n"},
		{[]string{"-t", "8,/2"}, "12345678901234\tx\n", "12345678901234  x\n"},
		{[]string{"-i"}, "a\tb\n", "a\tb\n"},
		{[]string{"-i"}, "  \tx\n", "        x\n"},
		{[]string{"-i"}, "\v\tx\n", "\v\tx\n"},
		{[]string{"-t", "2,5,9"}, "\t\t\x0c\n", "     \x0c\n"},
	}
	for _, c := range cases {
		status, out, stderr := captureApplet(t, cmdExpand, c.args, c.in)
		if status != 0 || out != c.want {
			t.Fatalf("expand %q %q = (%d, %q, %q), want %q", c.args, c.in, status, out, stderr, c.want)
		}
	}
}

func TestExpandDigitFormsAndLongOption(t *testing.T) {
	for _, args := range [][]string{{"-8"}, {"-4,8"}, {"--tabs=4,8"}, {"--tabs", "4,8"}, {"-t4,8"}} {
		status, out, stderr := captureApplet(t, cmdExpand, args, "1234567\tx\n")
		if status != 0 || out != "1234567 x\n" {
			t.Fatalf("expand %q = (%d, %q, %q)", args, status, out, stderr)
		}
	}
}

func TestExpandTabStopErrors(t *testing.T) {
	cases := []struct {
		arg  string
		want string
	}{
		{"0", "tab size cannot be 0"},
		{"4,2", "tab sizes must be ascending"},
		{"4,8,/2,+3", "'/' specifier is mutually exclusive with '+'"},
		{"4,8,/2,/3", "'/' specifier only allowed with the last value"},
		{"4,8,+2,+3", "'+' specifier only allowed with the last value"},
		{"4x", "tab size contains invalid character(s)"},
	}
	for _, c := range cases {
		status, _, stderr := captureApplet(t, cmdExpand, []string{"-t", c.arg}, "x\n")
		if status != 1 || !strings.Contains(stderr, c.want) {
			t.Fatalf("expand -t %s = (%d, %q), want %q", c.arg, status, stderr, c.want)
		}
	}
}

func TestUnexpandBasics(t *testing.T) {
	cases := []struct {
		args []string
		in   string
		want string
	}{
		{nil, "        xx\n", "\txx\n"},
		{nil, "         xx\n", "\t xx\n"},
		{nil, "   xx\n  \txx\na  b\n", "   xx\n\txx\na  b\n"},
		{[]string{"-a"}, "a\n\nb\n", "a\n\nb\n"},
		{[]string{"-a"}, "a       b\n", "a\tb\n"},
		{[]string{"-a"}, "a        b\n", "a\t b\n"},
		{[]string{"-a"}, "a         b\n", "a\t  b\n"},
		{[]string{"-a"}, "a             b\n", "a\t      b\n"},
		{[]string{"-a"}, "a              b\n", "a\t       b\n"},
		{[]string{"-a"}, "a  b\n", "a  b\n"},
		{[]string{"-a"}, "a      b\n", "a      b\n"},
		{[]string{"-a"}, "abcdefg  x\n", "abcdefg\t x\n"},
		{[]string{"-a"}, "abcdefg         x\n", "abcdefg\t\tx\n"},
		{[]string{"-a"}, "ab  \n", "ab  \n"},
		{[]string{"-a"}, "a \tb\n", "a\tb\n"},
		{[]string{"-t", "4"}, "    abcd\n", "\tabcd\n"},
		{[]string{"-t", "4"}, "  abcd\n", "  abcd\n"},
		{[]string{"-a", "--first-only"}, "a      b\n", "a      b\n"},
		{[]string{"-a"}, "        \n", "\t\n"},
		{[]string{"-a"}, "abcdefg  ", "abcdefg\t "},
		{[]string{"-a"}, "abcdefg ", "abcdefg "},
		{[]string{"-t", "2,5,9,+3"}, "a\x0b   ", "a\x0b\t"},
		{[]string{"-a"}, "       \rb\n", "       \rb\n"},
	}
	for _, c := range cases {
		status, out, stderr := captureApplet(t, cmdUnexpand, c.args, c.in)
		if status != 0 || out != c.want {
			t.Fatalf("unexpand %q %q = (%d, %q, %q), want %q", c.args, c.in, status, out, stderr, c.want)
		}
	}
}

func TestExpandUnexpandMultiFileContinuesLine(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	if err := os.WriteFile(first, []byte("c\td"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("x\ty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, out, stderr := captureApplet(t, cmdExpand, []string{"-t", "4,8", first, second}, "")
	if status != 0 || out != "c   dx  y\n" {
		t.Fatalf("expand two files = (%d, %q, %q)", status, out, stderr)
	}
	status, out, stderr = captureApplet(t, cmdUnexpand, []string{"-a", first, second}, "")
	if status != 0 || out != "c\tdx\ty\n" {
		t.Fatalf("unexpand two files = (%d, %q, %q)", status, out, stderr)
	}
}
