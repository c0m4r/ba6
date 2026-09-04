// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// TestPgrepSelection covers the selection options pgrep grew beyond -f/-x/-v:
// the id filters, the listing forms, the delimiter and the exit statuses procps
// reserves for a bad command line. pid 1 is the one process every system has.
func TestPgrepSelection(t *testing.T) {
	self, err := readProcess(1)
	if err != nil {
		t.Skip("cannot read /proc/1")
	}
	status, out, _ := captureApplet(t, cmdPgrep, []string{"-p", "1"}, "")
	if status != 0 || out != "1\n" {
		t.Fatalf("pgrep -p 1 = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdPgrep, []string{"-l", "-p", "1"}, "")
	if status != 0 || out != "1 "+self.comm+"\n" {
		t.Fatalf("pgrep -l -p 1 = (%d, %q)", status, out)
	}
	if _, out, _ = captureApplet(t, cmdPgrep, []string{"-a", "-p", "1"}, ""); !strings.HasPrefix(out, "1 ") || len(out) <= len("1 "+self.comm+"\n") {
		t.Fatalf("pgrep -a -p 1 = %q, want the whole command line", out)
	}
	// -x anchors the pattern, so a prefix of the name no longer matches.
	if status, _, _ = captureApplet(t, cmdPgrep, []string{"-x", "-p", "1", self.comm}, ""); status != 0 {
		t.Fatalf("pgrep -x on the exact name = %d", status)
	}
	if status, _, _ = captureApplet(t, cmdPgrep, []string{"-x", "-p", "1", self.comm[:len(self.comm)-1]}, ""); status != 1 {
		t.Fatalf("pgrep -x on a prefix = %d, want no match", status)
	}
	// -v inverts the whole verdict, the id filters included.
	if status, out, _ = captureApplet(t, cmdPgrep, []string{"-c", "-v", "-p", "1"}, ""); status != 0 {
		t.Fatalf("pgrep -c -v -p 1 = (%d, %q)", status, out)
	}
	count, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || count < 2 {
		t.Fatalf("pgrep -c -v -p 1 counted %q", out)
	}
	// A process only matches once, and pgrep never reports itself.
	if status, out, _ = captureApplet(t, cmdPgrep, []string{"-p", strconv.Itoa(os.Getpid())}, ""); status != 1 || out != "" {
		t.Fatalf("pgrep on its own pid = (%d, %q)", status, out)
	}
	// -d joins the ids and still ends the line.
	parent := os.Getppid()
	status, out, _ = captureApplet(t, cmdPgrep, []string{"-d", ",", "-p", "1," + strconv.Itoa(parent)}, "")
	if status != 0 || out != "1,"+strconv.Itoa(parent)+"\n" {
		t.Fatalf("pgrep -d , = (%d, %q)", status, out)
	}
	// -w reports the thread ids, of which pid 1 has at least its own.
	if status, out, _ = captureApplet(t, cmdPgrep, []string{"-w", "-p", "1"}, ""); status != 0 || !strings.HasPrefix(out, "1") {
		t.Fatalf("pgrep -w -p 1 = (%d, %q)", status, out)
	}
	// --quiet reports through the status alone.
	if status, out, _ = captureApplet(t, cmdPgrep, []string{"--quiet", "-p", "1"}, ""); status != 0 || out != "" {
		t.Fatalf("pgrep --quiet = (%d, %q)", status, out)
	}
	// -O keeps only processes at least that old; pid 1 is older than the boot
	// second and no process is a century old.
	if status, _, _ = captureApplet(t, cmdPgrep, []string{"-O", "3153600000", "-p", "1"}, ""); status != 1 {
		t.Fatalf("pgrep -O on an impossible age = %d", status)
	}
	// A name in /proc/PID/stat is 15 characters at most, so a longer pattern
	// draws procps' warning and matches nothing.
	status, out, errOut := captureApplet(t, cmdPgrep, []string{"abcdefghijklmnopqrstuv"}, "")
	if status != 1 || out != "" || !strings.Contains(errOut, "longer than 15 characters") {
		t.Fatalf("pgrep on a long pattern = (%d, %q, %q)", status, out, errOut)
	}
}

// TestPgrepCommandLineErrors pins the three shapes of failure procps reports,
// each of which exits 2.
func TestPgrepCommandLineErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "no matching criteria specified"},
		{[]string{"a", "b"}, "only one pattern can be provided"},
		{[]string{"-u", "definitely-no-such-user", "x"}, "invalid user name"},
		{[]string{"-G", "definitely-no-such-group", "x"}, "invalid group name"},
		{[]string{"-P", "notanumber", "x"}, "not a number"},
		{[]string{"-Z"}, "invalid option -- 'Z'"},
		{[]string{"--nosuchoption"}, "unrecognized option '--nosuchoption'"},
	} {
		status, out, errOut := captureApplet(t, cmdPgrep, c.args, "")
		if status != 2 || out != "" || !strings.Contains(errOut, c.want) {
			t.Fatalf("pgrep %v = (%d, %q, %q), want %q", c.args, status, out, errOut, c.want)
		}
	}
	// The two selectors that keep a single process cannot be combined; procps
	// answers with its usage text and no diagnostic of its own.
	status, _, errOut := captureApplet(t, cmdPgrep, []string{"-n", "-o", "x"}, "")
	if status != 2 || !strings.Contains(errOut, "Usage: pgrep") {
		t.Fatalf("pgrep -n -o = (%d, %q)", status, errOut)
	}
	// "no matching criteria" is the one shape that carries the Try line.
	if _, _, errOut = captureApplet(t, cmdPgrep, nil, ""); !strings.Contains(errOut, "Try `pgrep --help'") {
		t.Fatalf("pgrep with no criteria = %q", errOut)
	}
}

// TestPkillSignalsMatches drives pkill against a process of the test's own
// making: the signal named as -SIGNAL reaches it, -e reports what was hit, and
// the option letters that follow keep their own meaning.
func TestPkillSignalsMatches(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	child := exec.Command(sleep, "300") //nolint:gosec // G204: the fixed sleep binary found on PATH, as the test needs a process it may signal.
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()
	pid := strconv.Itoa(child.Process.Pid)
	// Signal 0 selects without delivering anything, so the child survives it.
	if status, _, _ := captureApplet(t, cmdPkill, []string{"-0", "-x", "-p", pid, "sleep"}, ""); status != 0 {
		t.Fatalf("pkill -0 = %d", status)
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("child did not survive pkill -0: %v", err)
	}
	status, out, _ := captureApplet(t, cmdPkill, []string{"-e", "-KILL", "-x", "-p", pid, "sleep"}, "")
	if status != 0 || out != "sleep killed (pid "+pid+")\n" {
		t.Fatalf("pkill -e -KILL = (%d, %q)", status, out)
	}
	state, err := child.Process.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if waitStatus, ok := state.Sys().(syscall.WaitStatus); !ok || waitStatus.Signal() != syscall.SIGKILL {
		t.Fatalf("child ended as %v, want SIGKILL", state)
	}
	// Nothing left to match, and no output without -e.
	if status, out, _ = captureApplet(t, cmdPkill, []string{"-x", "-p", pid, "sleep"}, ""); status != 1 || out != "" {
		t.Fatalf("pkill on a dead pid = (%d, %q)", status, out)
	}
}
