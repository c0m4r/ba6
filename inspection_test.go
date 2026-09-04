// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// treeFixture builds a small directory with a hidden file, a nested directory,
// and a symlink, which is enough to exercise every drawing rule.
func treeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"dir1/sub", "dir2"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"b.txt", "dir1/a.txt", "dir1/sub/deep.txt", ".hidden"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("hi\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "dir1", "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

// leafByteTotal sums the apparent size of every non-directory entry in the
// tree, recursively. Unlike a directory's own .size, this is exactly what
// the ncdu export format can carry, so it's the right thing to compare
// across an export/import round trip.
func leafByteTotal(e *ncduEntry) uint64 {
	if !e.directory {
		return e.size
	}
	var total uint64
	for _, child := range e.children {
		total += leafByteTotal(child)
	}
	return total
}

func TestTreeListing(t *testing.T) {
	root := treeFixture(t)
	status, stdout, stderr := captureApplet(t, cmdTree, []string{root}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("tree=(%d,%q)", status, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	want := []string{
		root,
		"├── b.txt",
		"├── dir1",
		"│   ├── a.txt",
		"│   ├── link -> a.txt",
		"│   └── sub",
		"│       └── deep.txt",
		"└── dir2",
		"",
		"3 directories, 4 files",
	}
	if len(lines) != len(want) {
		t.Fatalf("tree wrote %d lines, want %d: %q", len(lines), len(want), stdout)
	}
	for i, line := range want {
		if lines[i] != line {
			t.Errorf("line %d = %q, want %q", i, lines[i], line)
		}
	}
}

func TestTreeOptions(t *testing.T) {
	root := treeFixture(t)
	tests := []struct {
		args    []string
		present []string
		absent  []string
	}{
		{args: []string{"-a", root}, present: []string{".hidden", "5 files"}},
		{args: []string{"-d", root}, present: []string{"3 directories"}, absent: []string{"b.txt", "files"}},
		{args: []string{"-L", "1", root}, present: []string{"2 directories, 1 file"}, absent: []string{"deep.txt"}},
		{args: []string{"-F", root}, present: []string{"dir1/", "sub/"}},
		{args: []string{"-I", "dir*", root}, present: []string{"0 directories, 1 file"}, absent: []string{"dir1"}},
		{args: []string{"-P", "deep*", root}, present: []string{"deep.txt"}, absent: []string{"b.txt"}},
		{args: []string{"--noreport", root}, absent: []string{"directories,"}},
		{args: []string{"-f", root}, present: []string{filepath.Join(root, "dir1") + "/a.txt"}},
	}
	for _, test := range tests {
		status, stdout, stderr := captureApplet(t, cmdTree, test.args, "")
		if status != 0 || stderr != "" {
			t.Fatalf("tree %q = (%d,%q)", test.args, status, stderr)
		}
		for _, fragment := range test.present {
			if !strings.Contains(stdout, fragment) {
				t.Errorf("tree %q is missing %q:\n%s", test.args, fragment, stdout)
			}
		}
		for _, fragment := range test.absent {
			if strings.Contains(stdout, fragment) {
				t.Errorf("tree %q unexpectedly contains %q:\n%s", test.args, fragment, stdout)
			}
		}
	}
	if status, _, stderr := captureApplet(t, cmdTree, []string{"-Z", root}, ""); status == 0 ||
		!strings.Contains(stderr, "invalid option") {
		t.Fatalf("tree -Z = (%d,%q)", status, stderr)
	}
	if status, stdout, _ := captureApplet(t, cmdTree, []string{filepath.Join(root, "missing")}, ""); status == 0 ||
		!strings.Contains(stdout, "[error opening dir]") {
		t.Fatalf("tree on a missing path = (%d,%q)", status, stdout)
	}
}

func TestNetstatSocketFormatting(t *testing.T) {
	// An IPv4 loopback address with port 22, and the IPv6 wildcard with no
	// port, both as /proc/net writes them.
	if got := netstatAddress("0100007F:0016"); got != "127.0.0.1:22" {
		t.Errorf("netstatAddress(v4) = %q", got)
	}
	if got := netstatAddress("00000000000000000000000000000000:0000"); got != ":::*" {
		t.Errorf("netstatAddress(v6 wildcard) = %q", got)
	}
	// A long IPv6 address is cut to keep the column aligned.
	long := netstatAddress("B80D012078563412F0DEBC9A78563412:0016")
	if len(long) != netstatAddressWidth {
		t.Errorf("netstatAddress(long) = %q, want %d characters", long, netstatAddressWidth)
	}
	if received, sent := splitSocketQueues("00000005:0000000A"); received != 10 || sent != 5 {
		t.Errorf("splitSocketQueues = (%d,%d), want (10,5)", received, sent)
	}
	if got := netstatSocketState("tcp", "0A"); got != "LISTEN" {
		t.Errorf("tcp state 0A = %q", got)
	}
	if got := netstatSocketState("udp", "07"); got != "" {
		t.Errorf("udp state 07 = %q, want the empty state", got)
	}
	if got := netstatSocketState("raw6", "07"); got != "7" {
		t.Errorf("raw state 07 = %q", got)
	}
	if got := routeFlagNames(routeFlagUp | routeFlagGateway); got != "UG" {
		t.Errorf("routeFlagNames = %q", got)
	}
	if got := hexIPv4("0100A8C0"); got != "192.168.0.1" {
		t.Errorf("hexIPv4 = %q", got)
	}
	if got := interfaceFlagNames(0x1043); got != "BMRU" {
		t.Errorf("interfaceFlagNames = %q", got)
	}
}

func TestNetstatReports(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdNetstat, []string{"-tuan"}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("netstat -tuan = (%d,%q)", status, stderr)
	}
	for _, fragment := range []string{
		"Active Internet connections (servers and established)",
		"Proto Recv-Q Send-Q Local Address           Foreign Address         State",
	} {
		if !strings.Contains(stdout, fragment) {
			t.Errorf("netstat -tuan is missing %q:\n%s", fragment, stdout)
		}
	}
	status, stdout, stderr = captureApplet(t, cmdNetstat, []string{"-rn"}, "")
	if status != 0 || stderr != "" || !strings.Contains(stdout, "Kernel IP routing table") {
		t.Fatalf("netstat -rn = (%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdNetstat, []string{"-i"}, "")
	if status != 0 || stderr != "" || !strings.Contains(stdout, "Kernel Interface table") ||
		!strings.Contains(stdout, "lo ") {
		t.Fatalf("netstat -i = (%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdNetstat, []string{"-xl"}, "")
	if status != 0 || stderr != "" || !strings.Contains(stdout, "Active UNIX domain sockets (only servers)") {
		t.Fatalf("netstat -xl = (%d,%q,%q)", status, stdout, stderr)
	}
	if status, _, stderr := captureApplet(t, cmdNetstat, []string{"-Z"}, ""); status == 0 ||
		!strings.Contains(stderr, "invalid option") {
		t.Fatalf("netstat -Z = (%d,%q)", status, stderr)
	}
}

func TestNcduScanAndFormatting(t *testing.T) {
	root := treeFixture(t)
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	scan := newNcduScan(root, ncduOptions{})
	scanned := scan.walk(root, info)
	// Three directories, four regular files, and one symlink live below root.
	if scanned.items != 8 {
		t.Errorf("scanned %d items, want 8", scanned.items)
	}
	if !scanned.directory || scanned.size == 0 || scanned.disk == 0 {
		t.Errorf("root entry = %+v", scanned)
	}
	// The scan sees every entry, including the hidden one tree would skip.
	if len(scanned.children) != 4 {
		t.Errorf("root has %d children, want 4", len(scanned.children))
	}
	excluded := newNcduScan(root, ncduOptions{excludes: []string{"dir*"}}).walk(root, info)
	if len(excluded.children) != 2 {
		t.Errorf("--exclude kept %d children, want 2", len(excluded.children))
	}

	for _, test := range []struct {
		value uint64
		si    bool
		want  string
	}{
		{value: 100, want: "100.0   B"},
		{value: 4096, want: "  4.0 KiB"},
		{value: 303104, want: "296.0 KiB"},
		{value: 5000, si: true, want: "  5.0 kB"},
	} {
		if got := ncduSize(test.value, test.si); got != test.want {
			t.Errorf("ncduSize(%d, si=%v) = %q, want %q", test.value, test.si, got, test.want)
		}
	}
	if got := ncduShortenPath("/one/two/three/four", 13); got != "/one/...e/four" && len(got) != 13 {
		t.Errorf("ncduShortenPath = %q (%d characters)", got, len(got))
	}
	// The browser needs a terminal, and the test harness never has one.
	if status, _, stderr := captureApplet(t, cmdNcdu, []string{root}, ""); status == 0 ||
		!strings.Contains(stderr, "terminal") {
		t.Fatalf("ncdu without a terminal = (%d,%q)", status, stderr)
	}
	if status, _, stderr := captureApplet(t, cmdNcdu, []string{filepath.Join(root, "b.txt")}, ""); status == 0 ||
		!strings.Contains(stderr, "not a directory") {
		t.Fatalf("ncdu on a file = (%d,%q)", status, stderr)
	}
}

func TestNcduExportImportRoundTrip(t *testing.T) {
	root := treeFixture(t)
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	scanned := newNcduScan(root, ncduOptions{}).walk(root, info)
	scanned.name = root

	exportPath := filepath.Join(t.TempDir(), "export.json")
	if err := ncduExport(scanned, exportPath); err != nil {
		t.Fatal(err)
	}

	// The file must match ncdu's own documented schema: [major, minor,
	// {metadata}, [rootObj, ...children]], so real ncdu can read it back too.
	raw, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	var outer []json.RawMessage
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
	if len(outer) != 4 {
		t.Fatalf("export has %d top-level fields, want 4", len(outer))
	}
	var major, minor int
	if err := json.Unmarshal(outer[0], &major); err != nil || major != 1 {
		t.Fatalf("major version = %v, %v, want 1", major, err)
	}
	if err := json.Unmarshal(outer[1], &minor); err != nil || minor != 2 {
		t.Fatalf("minor version = %v, %v, want 2", minor, err)
	}

	imported, err := ncduImport(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if imported.name != scanned.name {
		t.Fatalf("imported name = %q, want %q", imported.name, scanned.name)
	}
	// Directory entries carry their own dirent/inode size in scanned.size
	// (set from info.Size() before children are added in), but the export
	// schema has no field for that -- only leaf sizes round-trip exactly, and
	// any reader (real ncdu included) rebuilds a directory's total purely as
	// the sum of its children. So compare leaf-byte totals, not raw .size.
	if got, want := leafByteTotal(imported), leafByteTotal(scanned); got != want {
		t.Fatalf("imported leaf byte total = %d, want %d", got, want)
	}
	if imported.items != scanned.items {
		t.Fatalf("imported item count = %d, want %d", imported.items, scanned.items)
	}
	if len(imported.children) != len(scanned.children) {
		t.Fatalf("imported %d children, want %d", len(imported.children), len(scanned.children))
	}

	// A file real ncdu itself could have written -- no "dsize" on
	// directories, an explicit "dev" only on the root -- must import too.
	handwritten := `[1, 2, {"progname":"ncdu","progver":"2.9.2"}, [` +
		`{"name":"/mnt/data","asize":6,"dev":43}, ` +
		`{"name":"f1.txt","asize":4,"dsize":4096}, ` +
		`[{"name":"sub","asize":2}, {"name":"f2.txt","asize":2,"dsize":4096}]` +
		`]]`
	handwrittenPath := filepath.Join(t.TempDir(), "real-ncdu.json")
	if err := os.WriteFile(handwrittenPath, []byte(handwritten), 0o600); err != nil {
		t.Fatal(err)
	}
	fromReal, err := ncduImport(handwrittenPath)
	if err != nil {
		t.Fatalf("importing a real-ncdu-shaped export failed: %v", err)
	}
	if fromReal.size != 6 || fromReal.items != 3 || len(fromReal.children) != 2 {
		t.Fatalf("imported real-ncdu export = %+v", fromReal)
	}
}

func TestPsBSDOptionsAndColumns(t *testing.T) {
	options := psOptions{selection: newPSSelection()}
	if err := options.parseBSD("axu"); err != nil {
		t.Fatalf("parseBSD(axu): %v", err)
	}
	if !options.bsdTerminal || !options.bsdOwn || !options.userFormat {
		t.Fatalf("parseBSD(axu) = %+v", options)
	}
	options.applyDefaultColumns()
	names := make([]string, len(options.columns))
	for i, column := range options.columns {
		names[i] = column.name
	}
	if strings.Join(names, ",") != "user,pid,pcpu,pmem,vsz,rss,tname,stat,start_time,bsdtime,args" {
		t.Fatalf("aux columns = %q", names)
	}
	pidOptions := psOptions{selection: newPSSelection()}
	if err := pidOptions.parseBSD("1,2"); err != nil || !pidOptions.selection.pids[1] || !pidOptions.selection.pids[2] {
		t.Fatalf("parseBSD(1,2) = %+v err=%v", pidOptions.selection.pids, err)
	}
	if err := (&psOptions{selection: newPSSelection()}).parseBSD("zz"); err == nil {
		t.Fatal("an unknown BSD option was accepted")
	}

	// "a" alone keeps the processes that hold a terminal; "x" alone keeps the
	// caller's own.
	mine := uint32(os.Geteuid()) //nolint:gosec // G115: a Linux user ID is a 32-bit unsigned value.
	processes := []processInfo{
		{pid: 1, uid: 0, tty: 0},
		{pid: 2, uid: mine, tty: 0},
		{pid: 3, uid: 0, tty: 0x8801},
	}
	terminal := (&psOptions{bsdTerminal: true, selection: newPSSelection()}).filter(processes)
	if len(terminal) != 1 || terminal[0].pid != 3 {
		t.Errorf("ps a selected %+v", terminal)
	}
	own := (&psOptions{bsdOwn: true, selection: newPSSelection()}).filter(processes)
	if len(own) != 1 || own[0].pid != 2 {
		t.Errorf("ps x selected %+v", own)
	}
	both := (&psOptions{bsdTerminal: true, bsdOwn: true, selection: newPSSelection()}).filter(processes)
	if len(both) != 3 {
		t.Errorf("ps ax selected %+v", both)
	}
}

func TestPsColumnValues(t *testing.T) {
	process := processInfo{pid: 7, session: 7, threads: 4, nice: -5, tty: 0x8801, tpgid: 7, pgrp: 7,
		utime: 100 * 61, stime: 0}
	if got := psState(process); got != "<sl+" {
		t.Errorf("psState = %q, want %q", got, "<sl+")
	}
	if got := psCPUTime(process, false); got != "1:01" {
		t.Errorf("psCPUTime = %q", got)
	}
	// The BSD TIME field has no hours: an hour of CPU reads as 61 minutes.
	if got := psCPUTime(processInfo{utime: 100 * 3661}, false); got != "61:01" {
		t.Errorf("psCPUTime over an hour = %q", got)
	}
	if got := psCPUTimeLong(processInfo{utime: 100 * 3661}); got != "01:01:01" {
		t.Errorf("psCPUTimeLong = %q", got)
	}
	if got := ttyName(0); got != "?" {
		t.Errorf("ttyName(0) = %q", got)
	}
	if got := ttyName(0x8801); got != "pts/1" {
		t.Errorf("ttyName(pts) = %q", got)
	}
	if got := ttyName(0x0403); got != "tty3" {
		t.Errorf("ttyName(console) = %q", got)
	}
	if got := psColumnName("%cpu"); got != "pcpu" {
		t.Errorf("psColumnName(%%cpu) = %q", got)
	}
}

// TestPsMatchesProcpsArithmetic pins the four differences from procps that the
// 2026-08-08 side-by-side measurement of "ps aux" turned up.
func TestPsMatchesProcpsArithmetic(t *testing.T) {
	process, err := readProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	// RSS comes from statm, which agrees with VmRSS; the counter in stat is
	// smaller.
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if fields := strings.Fields(line); len(fields) > 1 && fields[0] == "VmRSS:" {
			resident, convErr := strconv.ParseUint(fields[1], 10, 64)
			if convErr != nil {
				t.Fatal(convErr)
			}
			// The two readings are a moment apart, so allow a little drift.
			if difference := int64(process.rss/1024) - int64(resident); difference > 512 || difference < -512 { //nolint:gosec // G115: both values are small.
				t.Errorf("rss = %d kB, VmRSS = %d kB", process.rss/1024, resident)
			}
		}
	}
	// The user is the effective one, so a setuid program is listed as its owner.
	if int(process.uid) != os.Geteuid() {
		t.Errorf("uid = %d, want the effective %d", process.uid, os.Geteuid())
	}
	// Percentages are truncated to a tenth rather than rounded.
	runtime := psRuntime{uptime: 3700, memTotal: 1000000}
	if got := runtime.cpuPercent(processInfo{utime: 200}); got != "0.0" {
		t.Errorf("cpuPercent(2s over 3700s) = %q, want 0.0", got)
	}
	if got := runtime.cpuPercent(processInfo{utime: 100 * 3700}); got != "100" {
		t.Errorf("cpuPercent(full core) = %q", got)
	}
	if got := runtime.memoryPercent(processInfo{rss: 19999}); got != "1.9" {
		t.Errorf("memoryPercent = %q, want 1.9", got)
	}
	// Start times are dated from the kernel's boot timestamp, not from the
	// moment ps happened to read /proc/uptime.
	boot := newPSRuntime().boot
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if seconds, found := strings.CutPrefix(line, "btime "); found {
			epoch, convErr := strconv.ParseInt(strings.TrimSpace(seconds), 10, 64)
			if convErr != nil {
				t.Fatal(convErr)
			}
			if boot.Unix() != epoch {
				t.Errorf("boot time = %d, want btime %d", boot.Unix(), epoch)
			}
		}
	}
}

func TestPsUserFormatOutput(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdPs, []string{"axu"}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("ps axu = (%d,%q)", status, stderr)
	}
	lines := strings.Split(stdout, "\n")
	if !strings.HasPrefix(lines[0], "USER") || !strings.Contains(lines[0], "%CPU %MEM") ||
		!strings.HasSuffix(lines[0], "COMMAND") {
		t.Fatalf("ps axu heading = %q", lines[0])
	}
	self := strconv.Itoa(os.Getpid())
	found := false
	for _, line := range lines[1:] {
		if fields := strings.Fields(line); len(fields) > 1 && fields[1] == self {
			found = true
		}
	}
	if !found {
		t.Fatalf("ps axu did not list the test process %s:\n%s", self, stdout)
	}
	// "ps ax" uses the terminal-oriented format instead.
	status, stdout, stderr = captureApplet(t, cmdPs, []string{"ax"}, "")
	if status != 0 || stderr != "" || !strings.HasPrefix(strings.TrimLeft(stdout, " "), "PID TTY      STAT   TIME COMMAND") {
		t.Fatalf("ps ax = (%d,%q,%q)", status, stdout, stderr)
	}
}

func TestIPAbbreviations(t *testing.T) {
	if !ipMatches("s", "show", "list") || !ipMatches("sh", "show") || !ipMatches("show", "show") {
		t.Error("a prefix did not match its command")
	}
	if ipMatches("", "show") || ipMatches("shows", "show") || ipMatches("x", "show") {
		t.Error("a non-prefix matched")
	}
	// "ip l s" is "link set", so it must reach the setter and fail on the
	// missing device rather than list the links.
	err := ipLink([]string{"s"})
	if err == nil || strings.Contains(err.Error(), "unknown link command") {
		t.Fatalf("ip l s = %v, want a link-set error", err)
	}
	if err := ipLink([]string{"zzz"}); err == nil || !strings.Contains(err.Error(), "unknown link command") {
		t.Fatalf("ip l zzz = %v", err)
	}
	// "ip r s" and "ip a s" are listings, which succeed against the live
	// kernel tables.
	if err := captureStdout(t, func() error { return ipRoute(syscall.AF_INET, []string{"s"}) }); err != nil {
		t.Fatalf("ip r s: %v", err)
	}
	if err := captureStdout(t, func() error { return ipAddress(syscall.AF_UNSPEC, []string{"s"}) }); err != nil {
		t.Fatalf("ip a s: %v", err)
	}
	if err := captureStdout(t, func() error { return ipLink([]string{"sh"}) }); err != nil {
		t.Fatalf("ip l sh: %v", err)
	}
	if err := captureStdout(t, func() error { return ipRoute(syscall.AF_INET, []string{"zzz"}) }); err == nil {
		t.Fatal("ip r zzz was accepted")
	}
}

// captureStdout runs fn with standard output redirected to a temporary file, so
// that a listing under test does not pollute the test log.
func captureStdout(t *testing.T, fn func() error) error {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = file
	err = fn()
	os.Stdout = old
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return err
}
