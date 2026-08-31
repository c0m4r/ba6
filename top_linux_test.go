// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTopOptionsCoverBatchFiltersAndSortDirection(t *testing.T) {
	options, err := parseTopOptions([]string{
		"-bn2", "-d0.1", "-c", "-H", "-i", "-S", "-1", "-Eg", "-em", "-o", "-PID", "-p1,2", "-w120",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !options.batch || options.iterations != 2 || options.delay != 100*time.Millisecond || !options.commandLine ||
		!options.threads || !options.idle || !options.cumulative || !options.singleCPU || options.summaryUnit != topMemGiB ||
		options.taskUnit != topMemMiB || options.sort != topSortPID || options.descending || options.width != 120 ||
		!options.pids[1] || !options.pids[2] {
		t.Fatalf("unexpected top options: %+v", options)
	}
	long, err := parseTopOptions([]string{"--batch", "--iterations=1", "--sort-override=+%MEM", "--filter-only-euser=0"})
	if err != nil || !long.batch || long.sort != topSortMemory || !long.descending || long.userFilter == nil || long.userFilter.any {
		t.Fatalf("long options = %+v, %v", long, err)
	}
	for _, args := range [][]string{{"-p", "1", "-u", "0"}, {"-n", "0"}, {"-E", "z"}, {"-e", "e"}, {"-w", "513"}, {"-o", "unknown"}} {
		if _, err := parseTopOptions(args); err == nil {
			t.Errorf("parseTopOptions(%q) unexpectedly succeeded", args)
		}
	}
}

func TestTopCPUAndMemoryAccounting(t *testing.T) {
	stats, err := parseTopCPUStats("cpu 100 20 30 200 10 5 5 0\ncpu0 40 10 10 100 5 2 3 0\ncpu1 60 10 20 100 5 3 2 0\n")
	if err != nil || len(stats.cores) != 2 || stats.all.total() != 370 {
		t.Fatalf("CPU stats = %+v, %v", stats, err)
	}
	usage := topCPUPercentages(
		topCPUTime{user: 120, nice: 25, system: 40, idle: 240, iowait: 15, irq: 7, softirq: 8, steal: 15},
		topCPUTime{user: 100, nice: 20, system: 30, idle: 200, iowait: 10, irq: 5, softirq: 5}, true,
	)
	if math.Abs(usage.user-20) > 0.001 || math.Abs(usage.system-10) > 0.001 || math.Abs(usage.idle-40) > 0.001 {
		t.Fatalf("CPU usage = %+v", usage)
	}
	if got := topCPUTimeString(129); got != "0:01.29" {
		t.Fatalf("CPU time = %q", got)
	}
	values := map[string]uint64{
		"MemTotal": 100 * 1024 * 1024, "MemFree": 20 * 1024 * 1024, "MemAvailable": 60 * 1024 * 1024,
		"Buffers": 5 * 1024 * 1024, "Cached": 10 * 1024 * 1024, "SReclaimable": 3 * 1024 * 1024,
		"SwapTotal": 16 * 1024 * 1024, "SwapFree": 6 * 1024 * 1024,
	}
	memory := topMemoryFromMeminfo(values)
	if memory.total != values["MemTotal"] || memory.used != 40*1024*1024 || memory.buffCache != 18*1024*1024 || memory.swapUsed != 10*1024*1024 {
		t.Fatalf("memory = %+v", memory)
	}
}

func TestTopStandardRenderingAndWidth(t *testing.T) {
	memory := topMemory{total: 64 * 1024 * 1024, free: 16 * 1024 * 1024, used: 24 * 1024 * 1024, buffCache: 20 * 1024 * 1024,
		swapTotal: 8 * 1024 * 1024, swapFree: 6 * 1024 * 1024, swapUsed: 2 * 1024 * 1024, available: 40 * 1024 * 1024}
	display := topDisplay{
		snapshot: topSnapshot{
			now:    time.Date(2026, 8, 9, 12, 34, 56, 0, time.UTC),
			uptime: 24*60*60 + 2*60,
			loads:  [3]string{"1.00", "0.50", "0.25"},
			users:  1,
			memory: memory,
		},
		cpu: topCPUUsage{user: 10, system: 5, idle: 85},
		rows: []topRow{{
			process: processInfo{pid: 17, user: "rescueuser", priority: 20, state: "R", vsz: 3 * 1024 * 1024,
				rss: 2 * 1024 * 1024, shared: 1024 * 1024, comm: "worker", args: "worker --copy /disk", utime: 123},
			ticks: 123,
		}},
	}
	options := topOptions{summaryUnit: topMemMiB, taskUnit: topMemMiB, commandLine: true}
	output := formatTopDisplay(display, options, 200, 0, "")
	for _, want := range []string{
		"top - 12:34:56 up 1 day,  0:02,  1 user", "Tasks: 1 total, 1 running", "%Cpu(s):", "MiB Mem :",
		"MiB Swap:", "PID USER      PR  NI", "3.0m", "worker --copy /disk", "0:01.23",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("top output missing %q:\n%s", want, output)
		}
	}
	narrow := formatTopDisplay(display, options, 16, 0, "")
	for _, line := range strings.Split(strings.TrimSuffix(narrow, "\n"), "\n") {
		if utf8.RuneCountInString(line) > 16 {
			t.Errorf("line exceeds width: %q", line)
		}
	}
	if got := topTaskLine([]topRow{{process: processInfo{state: "R"}}, {process: processInfo{state: "Z"}}}, true); got != "Threads: 2 total, 1 running, 0 sleep, 0 d-sleep, 0 stopped, 1 zombie" {
		t.Fatalf("task line = %q", got)
	}
	if got := topSafeText("safe\x1b[31m\n"); got != "safe?[31m?" {
		t.Fatalf("safe command text = %q", got)
	}
}

func TestTopInteractiveScreenUsesCRLF(t *testing.T) {
	screen := topInteractiveScreen("header\nrow one\nrow two\n")
	if !strings.HasPrefix(screen, "\x1b[H\x1b[2J") {
		t.Fatalf("interactive screen lacks clear/home escape: %q", screen)
	}
	if strings.Contains(screen, "header\n") || !strings.Contains(screen, "header\r\nrow one\r\nrow two\r\n") {
		t.Fatalf("interactive screen does not use CRLF: %q", screen)
	}
}

func TestTopBatchIncludesProcpsSummaryAreas(t *testing.T) {
	status, output, stderr := captureApplet(t, cmdTop, []string{"-bn1", "-p", strconv.Itoa(os.Getpid()), "-c", "-w", "200"}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("top = (%d, %q, %q)", status, output, stderr)
	}
	for _, want := range []string{"Tasks:", "%Cpu(s):", "Mem :", "Swap:", "PID USER", "COMMAND"} {
		if !strings.Contains(output, want) {
			t.Errorf("batch report missing %q:\n%s", want, output)
		}
	}
}

func TestTopUserCountSkipsSessionRefFifos(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/119344", []byte("# This is private data.\nUID=0\nCLASS=user\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	// systemd keeps a "<id>.ref" FIFO open per session; reading it blocks
	// until the session goes away, which used to hang top on boot.
	if err := syscall.Mkfifo(dir+"/119344.ref", 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	saved := systemdSessionsDir
	systemdSessionsDir = dir
	defer func() { systemdSessionsDir = saved }()

	done := make(chan int, 1)
	go func() { done <- topUserCount() }()
	select {
	case count := <-done:
		if count != 1 {
			t.Fatalf("topUserCount() = %d, want 1", count)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("topUserCount blocked on a session ref FIFO")
	}
}
