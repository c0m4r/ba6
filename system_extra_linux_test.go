// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"syscall"
	"testing"
)

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
