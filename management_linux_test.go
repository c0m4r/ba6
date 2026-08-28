// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	value, present, err := readSysctl(root, "net.ipv4.ip_forward")
	if err != nil || !present || value != "0" {
		t.Fatalf("readSysctl = (%q, %v, %v)", value, present, err)
	}
	if err := writeSysctl(root, "net/ipv4/ip_forward", "1"); err != nil {
		t.Fatal(err)
	}
	entries, denied, err := listSysctls(root, false)
	if err != nil || len(denied) != 0 || len(entries) != 1 ||
		entries[0] != (sysctlEntry{key: "net.ipv4.ip_forward", value: "1"}) {
		t.Fatalf("listSysctls = (%+v, %+v, %v)", entries, denied, err)
	}
	if _, err := sysctlPath(root, "../kernel.panic"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if err := writeSysctl(root, "net.ipv4.ip_forward", "1\n2"); err == nil {
		t.Fatal("multiline sysctl value was accepted")
	}
	opts, err := parseSysctlOptions([]string{"-an"})
	if err != nil || !opts.all || !opts.valuesOnly {
		t.Fatalf("parseSysctlOptions(-an) = %+v, %v", opts, err)
	}

	// An unreadable key must not abandon the walk, and an empty file is passed
	// over rather than printed as a bare "name = ".
	if err := os.WriteFile(filepath.Join(root, "net", "ipv4", "secret"), []byte("x\n"), 0o200); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "ipv4", "blank"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, denied, err = listSysctls(root, false)
	if err != nil {
		t.Fatalf("listSysctls after unreadable key: %v", err)
	}
	if len(entries) != 1 || entries[0].key != "net.ipv4.ip_forward" {
		t.Fatalf("listSysctls entries = %+v; want only the readable, non-empty key", entries)
	}
	// The write-only key has no read bit at all, so it is skipped silently
	// rather than reported.
	if len(denied) != 0 {
		t.Fatalf("listSysctls denied = %+v; want none", denied)
	}

	// Listing names needs no read, so the forbidden and empty keys reappear
	// and nothing is reported as denied.
	entries, denied, err = listSysctls(root, true)
	if err != nil || len(denied) != 0 {
		t.Fatalf("listSysctls(namesOnly) = (%+v, %v)", denied, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.key)
	}
	want := []string{"net.ipv4.blank", "net.ipv4.ip_forward"}
	if !sameStrings(names, want) {
		t.Fatalf("listSysctls(namesOnly) names = %v, want %v", names, want)
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

// The header must match the original's layout: interval and command on the
// left, "host: ctime" against the right edge.
func TestWatchTitleLayout(t *testing.T) {
	opts := watchOptions{interval: 500 * time.Millisecond, command: []string{"echo", "hi"}}
	when := time.Date(2026, time.August, 28, 18, 45, 9, 0, time.UTC)
	title := watchTitle(opts, "deb1", when)
	if !strings.HasPrefix(title, "Every 0.5s: echo hi") {
		t.Fatalf("title = %q", title)
	}
	if !strings.HasSuffix(title, "deb1: Fri Aug 28 18:45:09 2026") {
		t.Fatalf("title = %q; want a ctime-formatted right edge", title)
	}
	if strings.Contains(title, "\t") {
		t.Fatalf("title = %q; padding must be spaces, not tabs", title)
	}

	// A command too long to sit beside the right-hand side is clipped, never
	// wrapped onto a second line.
	long := watchOptions{interval: time.Second, command: []string{strings.Repeat("x", 200)}}
	title = watchTitle(long, "deb1", when)
	if strings.Contains(title, "\n") {
		t.Fatalf("long title wrapped: %q", title)
	}
	if !strings.HasSuffix(title, "deb1: Fri Aug 28 18:45:09 2026") {
		t.Fatalf("long title lost its right edge: %q", title)
	}
}

func TestUptimeFormats(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{30, "0 min"},
		{83 * 60, " 1:23"},
		{5*24*60*60 + 2*60*60 + 10*60, "5 days,  2:10"},
		{2 * 24 * 60 * 60, "2 days, 0 min"},
		{24*60*60 + 5*60, "1 day, 5 min"},
	}
	for _, c := range cases {
		if got := formatUptimeShort(c.seconds); got != c.want {
			t.Fatalf("formatUptimeShort(%v) = %q, want %q", c.seconds, got, c.want)
		}
	}
	pretty := []struct {
		seconds float64
		want    string
	}{
		{83 * 60, "1 hour, 23 minutes"},
		{5*24*60*60 + 2*60*60 + 10*60, "5 days, 2 hours, 10 minutes"},
		{3 * 365 * 24 * 60 * 60, "3 years"},
		{2*7*24*60*60 + 24*60*60, "2 weeks, 1 day"},
		{11 * 365 * 24 * 60 * 60, "1 decade, 1 year"},
		{90, "1 minute"},
		{150, "2 minutes"},
	}
	for _, c := range pretty {
		if got := formatUptimePretty(c.seconds); got != c.want {
			t.Fatalf("formatUptimePretty(%v) = %q, want %q", c.seconds, got, c.want)
		}
	}
	status, out, _ := captureApplet(t, cmdUptime, []string{"-s"}, "")
	if status != 0 || !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\n$`).MatchString(out) {
		t.Fatalf("uptime -s = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdUptime, []string{"-r"}, "")
	fields := strings.Fields(out)
	if status != 0 || len(fields) != 6 {
		t.Fatalf("uptime -r = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdUptime, []string{"-p"}, "")
	if status != 0 || !strings.HasPrefix(out, "up ") {
		t.Fatalf("uptime -p = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdUptime, nil, "")
	if status != 0 || !regexp.MustCompile(`^ \d{2}:\d{2}:\d{2} up .*, +\d+ users?,  load average: .*\n$`).MatchString(out) {
		t.Fatalf("uptime default = (%d, %q)", status, out)
	}
}

func TestPidofScriptsAndOptions(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pidof-script-check.sh")
	scriptSource := "#!/bin/sh\nsleep 60\n"
	if err := os.WriteFile(script, []byte(scriptSource), 0o700); err != nil {
		t.Fatal(err)
	}
	spawn := func() int {
		cmd := exec.Command(script)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return cmd.Process.Pid
	}
	first := spawn()
	time.Sleep(50 * time.Millisecond)
	second := spawn()
	time.Sleep(50 * time.Millisecond)

	name := "pidof-script-check.sh"
	// Plain pidof does not match a shebang script: comm is not consulted.
	status, out, _ := captureApplet(t, cmdPidof, []string{name}, "")
	if status == 0 {
		t.Fatalf("pidof without -x matched the script: (%d, %q)", status, out)
	}
	// -x finds the interpreters running it, newest first.
	status, out, _ = captureApplet(t, cmdPidof, []string{"-x", name}, "")
	if status != 0 {
		t.Fatalf("pidof -x = (%d, %q)", status, out)
	}
	got := strings.Fields(out)
	if len(got) != 2 || got[0] != strconv.Itoa(second) || got[1] != strconv.Itoa(first) {
		t.Fatalf("pidof -x ordering = %q, want [%d %d]", out, second, first)
	}
	// -s keeps only the newest.
	status, out, _ = captureApplet(t, cmdPidof, []string{"-x", "-s", name}, "")
	if status != 0 || strings.TrimSpace(out) != strconv.Itoa(second) {
		t.Fatalf("pidof -x -s = (%d, %q)", status, out)
	}
	// -o omits the named PID.
	status, out, _ = captureApplet(t, cmdPidof, []string{"-x", "-o", strconv.Itoa(second), name}, "")
	if status != 0 || strings.TrimSpace(out) != strconv.Itoa(first) {
		t.Fatalf("pidof -o = (%d, %q)", status, out)
	}
	// -S changes the separator.
	status, out, _ = captureApplet(t, cmdPidof, []string{"-x", "-S", ",", name}, "")
	if status != 0 || out != strconv.Itoa(second)+","+strconv.Itoa(first)+"\n" {
		t.Fatalf("pidof -S = (%d, %q)", status, out)
	}
	// -q prints nothing and only signals the result.
	status, out, _ = captureApplet(t, cmdPidof, []string{"-q", "-x", name}, "")
	if status != 0 || out != "" {
		t.Fatalf("pidof -q = (%d, %q)", status, out)
	}
	status, _, _ = captureApplet(t, cmdPidof, []string{"-q", "pidof-no-such-program"}, "")
	if status == 0 {
		t.Fatalf("pidof -q on a missing program returned 0")
	}
}
