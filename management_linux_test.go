// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseWatchOptions(t *testing.T) {
	tests := []struct {
		args         []string
		interval     time.Duration
		noTitle      bool
		command      []string
		shouldReject bool
	}{
		{args: []string{"echo", "ready"}, interval: 2 * time.Second, command: []string{"echo", "ready"}},
		{args: []string{"-n0.5", "-t", "date"}, interval: 500 * time.Millisecond, noTitle: true, command: []string{"date"}},
		{args: []string{"--interval=3", "--", "echo", "-n"}, interval: 3 * time.Second, command: []string{"echo", "-n"}},
		{args: []string{"-n", "0"}, shouldReject: true},
	}
	for _, test := range tests {
		opts, err := parseWatchOptions(test.args)
		if test.shouldReject {
			if err == nil {
				t.Errorf("parseWatchOptions(%q) accepted invalid input", test.args)
			}
			continue
		}
		if err != nil || opts.interval != test.interval || opts.noTitle != test.noTitle || !sameStrings(opts.command, test.command) {
			t.Errorf("parseWatchOptions(%q) = %+v, %v", test.args, opts, err)
		}
	}
}

func TestSysctlTree(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "net", "ipv4", "ip_forward")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readSysctl(root, "net.ipv4.ip_forward")
	if err != nil || value != "0" {
		t.Fatalf("readSysctl = (%q, %v)", value, err)
	}
	if err := writeSysctl(root, "net/ipv4/ip_forward", "1"); err != nil {
		t.Fatal(err)
	}
	entries, err := listSysctls(root)
	if err != nil || len(entries) != 1 || entries[0] != (sysctlEntry{key: "net.ipv4.ip_forward", value: "1"}) {
		t.Fatalf("listSysctls = (%+v, %v)", entries, err)
	}
	if _, err := sysctlPath(root, "../kernel.panic"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if err := writeSysctl(root, "net.ipv4.ip_forward", "1\n2"); err == nil {
		t.Fatal("multiline sysctl value was accepted")
	}
	opts, err := parseSysctlOptions([]string{"-an"})
	if err != nil || !opts.all || !opts.nameOnly {
		t.Fatalf("parseSysctlOptions(-an) = %+v, %v", opts, err)
	}
}

func TestAccountDatabaseAddsGroupAndUser(t *testing.T) {
	paths := accountFixture(t)
	wantedGID := 1100
	if err := createAccountGroup(paths, "ops", &wantedGID); err != nil {
		t.Fatal(err)
	}
	if err := createAccountUser(paths, useraddSpec{
		name:                "alice",
		supplementaryGroups: []string{"ops"},
		shell:               "/bin/sh",
	}, time.Unix(20_000*86_400, 0)); err != nil {
		t.Fatal(err)
	}
	passwd, err := os.ReadFile(paths.passwd)
	if err != nil {
		t.Fatal(err)
	}
	wantPasswd := "root:x:0:0:root:/root:/bin/sh\nalice:x:1000:1000::" + filepath.Join(paths.homeRoot, "alice") + ":/bin/sh\n"
	if string(passwd) != wantPasswd {
		t.Fatalf("passwd = %q, want %q", passwd, wantPasswd)
	}
	shadow, err := os.ReadFile(paths.shadow)
	if err != nil {
		t.Fatal(err)
	}
	if string(shadow) != "root:*:1:0:99999:7:::\nalice:!:20000:0:99999:7:::\n" {
		t.Fatalf("shadow = %q", shadow)
	}
	group, err := os.ReadFile(paths.group)
	if err != nil {
		t.Fatal(err)
	}
	if string(group) != "root:x:0:\nops:x:1100:alice\nalice:x:1000:\n" {
		t.Fatalf("group = %q", group)
	}
	if err := addAccountUserToGroup(paths, "alice", "1100"); err != nil {
		t.Fatal(err)
	}
	group, err = os.ReadFile(paths.group)
	if err != nil || strings.Count(string(group), "alice") != 2 {
		t.Fatalf("idempotent group add = (%q, %v)", group, err)
	}
}

func TestAccountDatabaseRejectsDuplicateIdentifiers(t *testing.T) {
	paths := accountFixture(t)
	uid := 0
	if err := createAccountUser(paths, useraddSpec{name: "alice", uid: &uid, shell: "/bin/sh"}, time.Now()); err == nil {
		t.Fatal("duplicate uid was accepted")
	}
	if err := createAccountGroup(paths, "root", nil); err == nil {
		t.Fatal("duplicate group name was accepted")
	}
}

func accountFixture(t *testing.T) accountPaths {
	t.Helper()
	root := t.TempDir()
	paths := accountPaths{
		passwd: filepath.Join(root, "passwd"), shadow: filepath.Join(root, "shadow"),
		group: filepath.Join(root, "group"), homeRoot: filepath.Join(root, "home"),
	}
	if err := os.Mkdir(paths.homeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		paths.passwd: "root:x:0:0:root:/root:/bin/sh\n",
		paths.shadow: "root:*:1:0:99999:7:::\n",
		paths.group:  "root:x:0:\n",
	}
	for path, data := range files {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
