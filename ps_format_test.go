// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"strings"
	"testing"
	"time"
)

// Every expectation below was diffed against procps-ng 4.0.6 on a live system.

// psTestRuntime is a fixed snapshot, so the computed columns render the same
// values on every run.
func psTestRuntime() psRuntime {
	now := time.Date(2026, time.September, 4, 18, 0, 0, 0, time.Local)
	boot := time.Date(2026, time.August, 30, 13, 43, 2, 0, time.Local)
	return psRuntime{
		now:      now,
		boot:     boot,
		uptime:   now.Sub(boot).Seconds(),
		memTotal: 32 * 1024 * 1024 * 1024,
		pidWidth: 7,
	}
}

// psTestProcess is one process whose fields produce the row procps printed for
// this machine's pipewire-pulse: a five-character STAT in a four-wide column.
func psTestProcess() processInfo {
	runtime := psTestRuntime()
	started := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.Local)
	return processInfo{
		pid: 441596, ppid: 441372, pgrp: 441596, session: 441596, tpgid: -1,
		uid: 1001, realUID: 1001, gid: 1001, realGID: 1001,
		user:  "c0m4r",
		state: "S", locked: true, nice: -11, priority: 20, threads: 3,
		vsz: 187796 * 1024, rss: 23484 * 1024,
		utime: 18300, stime: 0,
		startTicks: uint64(started.Sub(runtime.boot).Seconds()) * clockTicks,
		flags:      4194304, policy: 0,
		comm: "pipewire-pulse", args: "/usr/bin/pipewire-pulse",
	}
}

// renderPS lays out one process with the given columns and returns the lines.
func renderPS(t *testing.T, columns []psSpec, process processInfo) []string {
	t.Helper()
	runtime := psTestRuntime()
	_, stdout, stderr := captureApplet(t, func([]string) int {
		writePS([]processInfo{process}, columns, runtime, false)
		return 0
	}, nil, "")
	if stderr != "" {
		t.Fatalf("writePS wrote to stderr: %q", stderr)
	}
	return strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
}

func TestPsGridSurvivesAnOversizedField(t *testing.T) {
	process := psTestProcess()
	if got := psState(process); got != "S<Lsl" {
		t.Fatalf("psState = %q, want S<Lsl", got)
	}
	lines := renderPS(t, psFormat("user", "pid", "pcpu", "pmem", "vsz", "rss",
		"tname", "stat", "start_time", "bsdtime", "args"), process)
	want := []string{
		"USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND",
		"c0m4r     441596  0.0  0.0 187796 23484 ?        S<Lsl Sep01   3:03 /usr/bin/pipewire-pulse",
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("aux line %d =\n%q\nwant\n%q", i, lines[i], want[i])
		}
	}
}

func TestPsGridCatchesUpAfterAWideNumber(t *testing.T) {
	// A right-aligned value that overruns its column pushes the row along,
	// and the next right-aligned column catches back up to the grid.
	process := psTestProcess()
	process.minflt, process.majflt = 1091999, 1
	lines := renderPS(t, psFormat("pid", "minflt", "majflt", "comm"), process)
	want := []string{
		"    PID MINFLT MAJFLT COMMAND",
		" 441596 1091999     1 pipewire-pulse",
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("minflt line %d =\n%q\nwant\n%q", i, lines[i], want[i])
		}
	}
}

func TestPsLongAndJobsFormats(t *testing.T) {
	process := psTestProcess()
	process.nice, process.state = 0, "S"
	process.locked, process.threads = false, 1

	long := psOptions{longFormat: true}
	long.applyDefaultColumns()
	lines := renderPS(t, long.columns, process)
	// The ADDR column is one character wide, so its own heading overruns it
	// and the heading line alone shifts right.
	if lines[0] != "F S   UID     PID    PPID  C PRI  NI ADDR SZ WCHAN  TTY          TIME CMD" {
		t.Errorf("-l heading = %q", lines[0])
	}
	// SZ is the virtual size in pages, right after the one-wide ADDR column.
	if !strings.HasSuffix(lines[1], " pipewire-pulse") || !strings.Contains(lines[1], " - 46949 ") {
		t.Errorf("-l row = %q", lines[1])
	}

	jobs := psOptions{jobsFormat: true}
	jobs.applyDefaultColumns()
	lines = renderPS(t, jobs.columns, process)
	if lines[0] != "    PID    PGID     SID TTY          TIME CMD" {
		t.Errorf("-j heading = %q", lines[0])
	}
	if lines[1] != " 441596  441596  441596 ?        00:03:03 pipewire-pulse" {
		t.Errorf("-j row = %q", lines[1])
	}

	full := psOptions{full: true}
	full.applyDefaultColumns()
	lines = renderPS(t, full.columns, process)
	if lines[0] != "UID          PID    PPID  C STIME TTY          TIME CMD" {
		t.Errorf("-f heading = %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], " /usr/bin/pipewire-pulse") || !strings.HasPrefix(lines[1], "c0m4r  ") {
		t.Errorf("-f row = %q", lines[1])
	}
}

func TestPsColumnHeadingsAndWidths(t *testing.T) {
	process := psTestProcess()
	process.policy = 1 // a real-time process has no nice value to show
	for _, c := range []struct {
		column  string
		heading string
		value   string
	}{
		{"cmd", "CMD", "/usr/bin/pipewire-pulse"},
		{"ucmd", "CMD", "pipewire-pulse"},
		{"comm", "COMMAND", "pipewire-pulse"},
		{"tty", "TT", "?"},
		{"tname", "TTY", "?"},
		{"sz", "SZ", "46949"},
		{"ni", "NI", "-"},
		{"cls", "CLS", "FF"},
		{"f", "F", "0"},
		{"s", "S", "S"},
		{"pri", "PRI", "19"},
		{"opri", "PRI", "80"},
		{"nlwp", "NLWP", "3"},
		{"etime", "ELAPSED", "3-08:00:00"},
		{"etimes", "ELAPSED", "288000"},
		{"cputime", "TIME", "00:03:03"},
		{"bsdtime", "TIME", "3:03"},
		{"start", "STARTED", "Sep  1"},
		{"bsdstart", "START", "Sep  1"},
		{"stime", "STIME", "Sep01"},
		{"lstart", "STARTED", "Tue Sep  1 10:00:00 2026"},
		{"minflt", "MINFLT", "0"},
		{"rgroup", "RGROUP", psGroupName(1001)},
	} {
		lines := renderPS(t, psFormat(c.column, "pid"), process)
		if strings.Fields(lines[0])[0] != c.heading {
			t.Errorf("%s heading = %q, want %q", c.column, lines[0], c.heading)
		}
		if strings.Fields(lines[1])[0] != strings.Fields(c.value)[0] {
			t.Errorf("%s value = %q, want %q", c.column, lines[1], c.value)
		}
	}
}

func TestPsSelectionIsAdditive(t *testing.T) {
	processes := []processInfo{
		{pid: 1, comm: "systemd", uid: 0, realUID: 0, session: 1, pgrp: 1},
		{pid: 2, comm: "bash", uid: 1001, realUID: 1001, ppid: 1, session: 2, pgrp: 2},
		{pid: 3, comm: "sleep", uid: 1001, realUID: 1001, ppid: 2, session: 2, pgrp: 2},
	}
	pids := func(selected []processInfo) string {
		parts := make([]string, 0, len(selected))
		for _, process := range selected {
			parts = append(parts, process.comm)
		}
		return strings.Join(parts, ",")
	}
	for _, c := range []struct {
		kind  byte
		value string
		want  string
	}{
		{'p', "1", "systemd"},
		{'C', "bash,sleep", "bash,sleep"},
		{'u', "0", "systemd"},
		{'U', "1001", "bash,sleep"},
		{'P', "2", "sleep"},
		{'s', "2", "bash,sleep"},
	} {
		options := psOptions{selection: newPSSelection()}
		if !options.selection.add(c.kind, c.value) {
			t.Fatalf("-%c %s was rejected", c.kind, c.value)
		}
		if got := pids(options.filter(processes)); got != c.want {
			t.Errorf("-%c %s selected %q, want %q", c.kind, c.value, got, c.want)
		}
	}
	// Two lists select the union of both.
	options := psOptions{selection: newPSSelection()}
	options.selection.add('p', "1")
	options.selection.add('C', "sleep")
	if got := pids(options.filter(processes)); got != "systemd,sleep" {
		t.Errorf("-p 1 -C sleep selected %q", got)
	}
	// -N inverts whatever the lists chose.
	options.selection.deselect = true
	if got := pids(options.filter(processes)); got != "bash" {
		t.Errorf("-N -p 1 -C sleep selected %q", got)
	}
}

func TestPsSortKeys(t *testing.T) {
	runtime := psTestRuntime()
	processes := []processInfo{
		{pid: 3, comm: "b", rss: 3 * 1024 * 1024},
		{pid: 1, comm: "c", rss: 1 * 1024 * 1024},
		{pid: 2, comm: "a", rss: 2 * 1024 * 1024},
	}
	order := func(selected []processInfo) string {
		parts := make([]string, 0, len(selected))
		for _, process := range selected {
			parts = append(parts, process.comm)
		}
		return strings.Join(parts, "")
	}
	for _, c := range []struct {
		keys string
		want string
	}{
		{"pid", "cab"},
		{"-pid", "bac"},
		{"comm", "abc"},
		{"-rss", "bac"},
		{"+comm", "abc"},
	} {
		options := psOptions{}
		if !options.addSortKeys(c.keys) {
			t.Fatalf("--sort=%s was rejected", c.keys)
		}
		sorted := append([]processInfo(nil), processes...)
		options.sort(sorted, runtime)
		if got := order(sorted); got != c.want {
			t.Errorf("--sort=%s gave %q, want %q", c.keys, got, c.want)
		}
	}
}

func TestPsCustomHeadingsAndEscaping(t *testing.T) {
	process := psTestProcess()
	// "name=" blanks one heading; blanking every one drops the header line.
	lines := renderPS(t, splitPSColumns("pid=PROC,comm=NAME"), process)
	if lines[0] != "   PROC NAME" {
		t.Errorf("custom headings = %q", lines[0])
	}
	lines = renderPS(t, splitPSColumns("pid=,comm="), process)
	if len(lines) != 1 || strings.TrimSpace(lines[0]) != "441596 pipewire-pulse" {
		t.Errorf("blanked headings printed %q", lines)
	}
	// A command line never spills onto a second line: the NULs between
	// arguments and any newline become spaces, other control bytes a "?".
	if got := psEscapeArgs("a\x00b\nc\td\x01e"); got != "a b c?d?e" {
		t.Errorf("psEscapeArgs = %q", got)
	}
}
