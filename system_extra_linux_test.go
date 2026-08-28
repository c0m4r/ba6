// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestParseDmesgLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		facility int
		level    int
		sec      int64
		usec     int64
		hasTS    bool
		text     string
	}{
		{"well-formed", "<6>[    0.000000] Linux version test", 0, 6, 0, 0, true, "Linux version test"},
		{"nonzero-timestamp", "<14>[  123.456789] a userspace daemon message", 1, 6, 123, 456789, true, "a userspace daemon message"},
		{"err-level", "<3>[   12.345678] an error occurred here", 0, 3, 12, 345678, true, "an error occurred here"},
		{"no-prefix-defaults-zero", "plain line without prefix", 0, 6, 0, 0, true, "plain line without prefix"},
		{"malformed-priority", "<oops>[1.0] text", 0, 6, 0, 0, true, "<oops>[1.0] text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDmesgLine(tc.line)
			if got.facility != tc.facility || got.level != tc.level || got.sec != tc.sec ||
				got.usec != tc.usec || got.hasTS != tc.hasTS || got.text != tc.text {
				t.Fatalf("parseDmesgLine(%q) = %+v, want facility=%d level=%d sec=%d usec=%d hasTS=%v text=%q",
					tc.line, got, tc.facility, tc.level, tc.sec, tc.usec, tc.hasTS, tc.text)
			}
		})
	}
}

func TestParseDmesgLevelsAndFacilities(t *testing.T) {
	levels, err := parseDmesgLevels("err")
	if err != nil || len(levels) != 1 || !levels[3] {
		t.Fatalf("parseDmesgLevels(err) = %v, %v", levels, err)
	}
	levels, err = parseDmesgLevels("err+")
	if err != nil || !levels[0] || !levels[1] || !levels[2] || !levels[3] || levels[4] {
		t.Fatalf("parseDmesgLevels(err+) = %v, %v", levels, err)
	}
	if _, err := parseDmesgLevels("bogus"); err == nil {
		t.Fatal("parseDmesgLevels(bogus) should fail")
	}
	facilities, err := parseDmesgFacilities("user,kern")
	if err != nil || !facilities[0] || !facilities[1] || len(facilities) != 2 {
		t.Fatalf("parseDmesgFacilities(user,kern) = %v, %v", facilities, err)
	}
	if _, err := parseDmesgFacilities("bogus"); err == nil {
		t.Fatal("parseDmesgFacilities(bogus) should fail")
	}
}

// captureDmesgOutput runs cmdDmesg with stdout redirected to a temp file and
// returns what it wrote, so -F tests can check formatting without touching
// the real kernel ring buffer.
func captureDmesgOutput(t *testing.T, args []string) (string, int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = file
	status := cmdDmesg(args)
	os.Stdout = old
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), status
}

func TestCmdDmesgFromFile(t *testing.T) {
	log := "<6>[    0.000000] Linux version test\n" +
		"<4>[    1.234567] some warning message\n" +
		"<3>[   12.345678] an error occurred here\n" +
		"<14>[  123.456789] a userspace daemon message\n"
	path := filepath.Join(t.TempDir(), "synth.log")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"default", []string{"-F", path}, "[    0.000000] Linux version test\n" +
			"[    1.234567] some warning message\n" +
			"[   12.345678] an error occurred here\n" +
			"[  123.456789] a userspace daemon message\n"},
		{"raw", []string{"-F", path, "-r"}, log},
		{"notime", []string{"-F", path, "-t"}, "Linux version test\nsome warning message\nan error occurred here\na userspace daemon message\n"},
		{"decode", []string{"-F", path, "-x"}, "kern  :info  : [    0.000000] Linux version test\n" +
			"kern  :warn  : [    1.234567] some warning message\n" +
			"kern  :err   : [   12.345678] an error occurred here\n" +
			"user  :info  : [  123.456789] a userspace daemon message\n"},
		{"kernel-only", []string{"-F", path, "-k"}, "[    0.000000] Linux version test\n" +
			"[    1.234567] some warning message\n" +
			"[   12.345678] an error occurred here\n"},
		{"userspace-only", []string{"-F", path, "-u"}, "[  123.456789] a userspace daemon message\n"},
		{"level-err", []string{"-F", path, "-l", "err"}, "[   12.345678] an error occurred here\n"},
		{"facility-user", []string{"-F", path, "-f", "user"}, "[  123.456789] a userspace daemon message\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, status := captureDmesgOutput(t, tc.args)
			if status != 0 {
				t.Fatalf("cmdDmesg%v returned %d", tc.args, status)
			}
			if got != tc.want {
				t.Fatalf("cmdDmesg%v = %q, want %q", tc.args, got, tc.want)
			}
		})
	}

	if _, status := captureDmesgOutput(t, []string{"-F", path, "-l", "bogus"}); status == 0 {
		t.Fatal("cmdDmesg with an unknown level should fail")
	}
	if _, status := captureDmesgOutput(t, []string{"-F", "/nonexistent-file"}); status == 0 {
		t.Fatal("cmdDmesg -F on a missing file should fail")
	}
}

func TestParseMountOptions(t *testing.T) {
	tests := []struct {
		name      string
		options   string
		wantFlags uintptr
		wantData  string
	}{
		{
			name:      "mount flags and filesystem data",
			options:   "nosuid,nodev,mode=1777",
			wantFlags: syscall.MS_NOSUID | syscall.MS_NODEV,
			wantData:  "mode=1777",
		},
		{
			name:    "defaults",
			options: "defaults",
		},
		{
			name:      "read only and filesystem data",
			options:   "ro,gid=5",
			wantFlags: syscall.MS_RDONLY,
			wantData:  "gid=5",
		},
		{
			name:     "filesystem data remains unchanged",
			options:  "gid=5,mode=620,ptmxmode=666",
			wantData: "gid=5,mode=620,ptmxmode=666",
		},
		{
			name: "empty",
		},
		{
			name:    "negative flags are clear when unset",
			options: "exec,suid",
		},
		{
			name:    "negative flags clear prior bits",
			options: "noexec,nosuid,exec,suid",
		},
		{
			name:      "additional VFS flags",
			options:   "sync,dirsync,noatime,nodiratime,relatime,strictatime,nosymfollow,silent,rbind,rprivate,rshared,rslave,runbindable",
			wantFlags: syscall.MS_SYNCHRONOUS | syscall.MS_DIRSYNC | syscall.MS_NOATIME | syscall.MS_NODIRATIME | syscall.MS_RELATIME | syscall.MS_STRICTATIME | msNoSymFollow | syscall.MS_SILENT | syscall.MS_BIND | syscall.MS_REC | syscall.MS_PRIVATE | syscall.MS_SHARED | syscall.MS_SLAVE | syscall.MS_UNBINDABLE,
		},
		{
			name:     "valued option is not a mount flag",
			options:  "ro=filesystem-value,size=64M",
			wantData: "ro=filesystem-value,size=64M",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flags, data := parseMountOptions(test.options)
			if flags != test.wantFlags || data != test.wantData {
				t.Fatalf("parseMountOptions(%q) = (%#x, %q), want (%#x, %q)", test.options, flags, data, test.wantFlags, test.wantData)
			}
		})
	}
}

func TestTtyReportsNonTerminal(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdTty, nil, "")
	if status != 1 || stdout != "not a tty\n" || stderr != "" {
		t.Fatalf("tty = (%d, %q, %q)", status, stdout, stderr)
	}
}
