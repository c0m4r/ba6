// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCoreExtraApplets(t *testing.T) {
	status, out, _ := captureApplet(t, cmdPrintf, []string{"%s:%04d\\n", "ok", "7"}, "")
	if status != 0 || out != "ok:0007\n" {
		t.Fatalf("printf = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdSeq, []string{"2", "2", "6"}, "")
	if status != 0 || out != "2\n4\n6\n" {
		t.Fatalf("seq = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdExpr, []string{"2", "+", "3", "*", "4"}, "")
	if status != 0 || out != "14\n" {
		t.Fatalf("expr = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdBase64, nil, "hello")
	if status != 0 || out != "aGVsbG8=\n" {
		t.Fatalf("base64 = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdBase64, []string{"-d"}, "aGVsbG8=\n")
	if status != 0 || out != "hello" {
		t.Fatalf("base64 decode = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdStrings, []string{"-n", "4"}, "\x00hello\x01bye")
	if status != 0 || out != "hello\n" {
		t.Fatalf("strings = (%d, %q)", status, out)
	}
}

func TestOdAndHexdumpFormats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	// "abcde": an odd length exercises the zero-extended trailing word both
	// tools use, since 5 bytes is 2 full 16-bit words plus one leftover byte.
	if err := os.WriteFile(path, []byte("abcde"), 0o600); err != nil {
		t.Fatal(err)
	}

	odCases := []struct {
		name string
		args []string
		want string
	}{
		{"default-octal-words", nil, "0000000 061141 062143 000145\n0000005\n"},
		{"-c-escapes", []string{"-c"}, "0000000   a   b   c   d   e\n0000005\n"},
		{"-x-zero-extends", []string{"-x"}, "0000000 6261 6463 0065\n0000005\n"},
		{"-d-unsigned", []string{"-d"}, "0000000 25185 25699   101\n0000005\n"},
		{"-b-octal-bytes", []string{"-b"}, "0000000 141 142 143 144 145\n0000005\n"},
		{"-An-suppresses-address", []string{"-An", "-c"}, "   a   b   c   d   e\n"},
		{"-j-skip", []string{"-j", "2"}, "0000002 062143 000145\n0000005\n"},
		{"-N-limit", []string{"-N", "2"}, "0000000 061141\n0000002\n"},
	}
	for _, tc := range odCases {
		t.Run("od/"+tc.name, func(t *testing.T) {
			status, out, _ := captureApplet(t, cmdOd, append(append([]string{}, tc.args...), path), "")
			if status != 0 || out != tc.want {
				t.Fatalf("od %v = (%d, %q), want (0, %q)", tc.args, status, out, tc.want)
			}
		})
	}

	hexCases := []struct {
		name string
		args []string
		want string
	}{
		{"default-hex-addr", nil, "0000000 6261 6463 0065                         \n0000005\n"},
		{"-C-canonical", []string{"-C"}, "00000000  61 62 63 64 65                                    |abcde|\n00000005\n"},
		{"-x-wide", []string{"-x"}, "0000000    6261    6463    0065                                        \n0000005\n"},
		{"-s-skip", []string{"-C", "-s", "2"}, "00000002  63 64 65                                          |cde|\n00000005\n"},
		{"-n-limit", []string{"-C", "-n", "3"}, "00000000  61 62 63                                          |abc|\n00000003\n"},
	}
	for _, tc := range hexCases {
		t.Run("hexdump/"+tc.name, func(t *testing.T) {
			status, out, _ := captureApplet(t, cmdHexdump, append(append([]string{}, tc.args...), path), "")
			if status != 0 || out != tc.want {
				t.Fatalf("hexdump %v = (%d, %q), want (0, %q)", tc.args, status, out, tc.want)
			}
		})
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if status, out, _ := captureApplet(t, cmdOd, []string{empty}, ""); status != 0 || out != "0000000\n" {
		t.Fatalf("od on empty file = (%d, %q), want (0, \"0000000\\n\")", status, out)
	}
	if status, out, _ := captureApplet(t, cmdHexdump, []string{"-C", empty}, ""); status != 0 || out != "" {
		t.Fatalf("hexdump -C on empty file = (%d, %q), want (0, \"\")", status, out)
	}

	repeated := filepath.Join(dir, "repeat")
	if err := os.WriteFile(repeated, bytes.Repeat([]byte{0x41}, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	status, out, _ := captureApplet(t, cmdOd, []string{repeated}, "")
	if status != 0 || strings.Count(out, "*") != 1 {
		t.Fatalf("od on repeated data should collapse to one '*' line, got %q", out)
	}
	status, out, _ = captureApplet(t, cmdOd, []string{"-v", repeated}, "")
	if status != 0 || strings.Contains(out, "*") {
		t.Fatalf("od -v should disable the '*' collapse, got %q", out)
	}
}

func TestCompareAndDiff(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("one\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, _ := captureApplet(t, cmdCmp, []string{a, a}, "")
	if status != 0 {
		t.Fatalf("equal cmp status = %d", status)
	}
	status, _, _ = captureApplet(t, cmdCmp, []string{"-s", a, b}, "")
	if status != 1 {
		t.Fatalf("different cmp status = %d", status)
	}
	status, out, _ := captureApplet(t, cmdDiff, []string{a, b}, "")
	if status != 1 || out != "2c2\n< two\n---\n> three\n" {
		t.Fatalf("diff (default, normal format) = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdDiff, []string{"-u", a, b}, "")
	if status != 1 || !strings.Contains(out, "-two") || !strings.Contains(out, "+three") || !strings.HasPrefix(out, "--- "+a) {
		t.Fatalf("diff -u = (%d, %q)", status, out)
	}
}

func TestDiffOptionsAndEdgeCases(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// -q / -s report presence or absence of a difference without a diff body.
	same1, same2 := write("same1", "x\ny\n"), write("same2", "x\ny\n")
	diff1, diff2 := write("diff1", "x\ny\n"), write("diff2", "x\nz\n")
	if status, out, _ := captureApplet(t, cmdDiff, []string{"-q", diff1, diff2}, ""); status != 1 || !strings.Contains(out, "differ") {
		t.Fatalf("diff -q on differing files = (%d, %q)", status, out)
	}
	if status, out, _ := captureApplet(t, cmdDiff, []string{"-q", same1, same2}, ""); status != 0 || out != "" {
		t.Fatalf("diff -q on identical files = (%d, %q)", status, out)
	}
	if status, out, _ := captureApplet(t, cmdDiff, []string{"-s", same1, same2}, ""); status != 0 || !strings.Contains(out, "identical") {
		t.Fatalf("diff -s on identical files = (%d, %q)", status, out)
	}

	// -i/-w/-b normalize what's compared but never what's displayed.
	if status, _, _ := captureApplet(t, cmdDiff, []string{"-i", write("case1", "Hello\n"), write("case2", "hello\n")}, ""); status != 0 {
		t.Fatalf("diff -i should treat case-only changes as identical, status = %d", status)
	}
	if status, _, _ := captureApplet(t, cmdDiff, []string{"-w", write("sp1", "a  b\n"), write("sp2", "ab\n")}, ""); status != 0 {
		t.Fatalf("diff -w should ignore all whitespace, status = %d", status)
	}
	if status, _, _ := captureApplet(t, cmdDiff, []string{"-b", write("sp3", "a\tb\n"), write("sp4", "a  b\n")}, ""); status != 0 {
		t.Fatalf("diff -b should ignore runs of whitespace, status = %d", status)
	}
	if status, _, _ := captureApplet(t, cmdDiff, []string{"-b", write("sp5", "ab\n"), write("sp6", "a b\n")}, ""); status != 1 {
		t.Fatalf("diff -b should still notice whitespace where none existed, status = %d", status)
	}

	// -N treats a missing file as empty instead of erroring.
	missing := filepath.Join(dir, "does-not-exist")
	present := write("present", "a\nb\n")
	if status, _, _ := captureApplet(t, cmdDiff, []string{missing, present}, ""); status != 2 {
		t.Fatalf("diff without -N on a missing file should error, status = %d", status)
	}
	if status, out, _ := captureApplet(t, cmdDiff, []string{"-N", missing, present}, ""); status != 1 || !strings.Contains(out, "> a") {
		t.Fatalf("diff -N on a missing file should treat it as empty = (%d, %q)", status, out)
	}

	// A missing trailing newline is a real difference, marked in the output,
	// even when the visible line text is identical on both sides.
	noNL := write("no-newline", "a\nb")
	withNL := write("with-newline", "a\nb\n")
	status, out, _ := captureApplet(t, cmdDiff, []string{noNL, withNL}, "")
	if status != 1 || !strings.Contains(out, `\ No newline at end of file`) {
		t.Fatalf("diff should flag a missing trailing newline = (%d, %q)", status, out)
	}
	bothNoNL := write("also-no-newline", "a\nb")
	if status, _, _ := captureApplet(t, cmdDiff, []string{noNL, bothNoNL}, ""); status != 0 {
		t.Fatalf("two files that both lack a trailing newline with the same text should be identical, status = %d", status)
	}

	// A far-apart pair of changes must produce two separate unified hunks;
	// two changes within 2*3 lines of each other must merge into one.
	var far strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&far, "line%d\n", i)
	}
	baseLines := strings.Split(strings.TrimSuffix(far.String(), "\n"), "\n")
	farA := write("far-a", far.String())
	farLines := append([]string{}, baseLines...)
	farLines[2] = "CHANGED2"
	farLines[35] = "CHANGED35"
	farB := write("far-b", strings.Join(farLines, "\n")+"\n")
	_, out, _ = captureApplet(t, cmdDiff, []string{"-u", farA, farB}, "")
	if strings.Count(out, "@@") != 4 {
		t.Fatalf("two far-apart changes should produce two hunks (4 '@@' markers), got %q", out)
	}

	nearLines := append([]string{}, baseLines...)
	nearLines[2] = "CHANGED2"
	nearLines[7] = "CHANGED7"
	nearA := write("near-a", far.String())
	nearB := write("near-b", strings.Join(nearLines, "\n")+"\n")
	_, out, _ = captureApplet(t, cmdDiff, []string{"-u", nearA, nearB}, "")
	if strings.Count(out, "@@") != 2 {
		t.Fatalf("two nearby changes should merge into one hunk (2 '@@' markers), got %q", out)
	}
}

func TestShellParsingAndExecution(t *testing.T) {
	t.Setenv("BA6_SHELL_TEST", "expanded")
	tokens, err := shellTokens("printf '%s' '$BA6_SHELL_TEST' \"$BA6_SHELL_TEST\" ''")
	if err != nil {
		t.Fatal(err)
	}
	// Tokenising leaves words unexpanded; expansion happens when the command
	// runs, so a single-quoted $ has to survive as a literal until then.
	command, err := shellCommand(tokens)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"printf", "%s", "$BA6_SHELL_TEST", "expanded", ""}
	if strings.Join(command.argv, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %#v, want %#v", command.argv, want)
	}
	status, out, _ := captureApplet(t, cmdSh, []string{"-c", "/bin/printf shell-ok"}, "")
	if status != 0 || out != "shell-ok" {
		t.Fatalf("sh = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdXargs, []string{"-n", "1", "/bin/printf", "[%s]"}, "a b")
	if status != 0 || out != "[a][b]" {
		t.Fatalf("xargs = (%d, %q)", status, out)
	}
}

func TestXargsExtendedOptions(t *testing.T) {
	status, out, _ := captureApplet(t, cmdXargs, []string{"-d", ":", "/bin/echo"}, "a:b:c")
	if status != 0 || out != "a b c\n" {
		t.Fatalf("xargs -d = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdXargs, []string{"-0", "/bin/echo"}, "a\x00\x00b")
	if status != 0 || out != "a  b\n" {
		t.Fatalf("xargs -0 should keep empty items between delimiters = (%d, %q)", status, out)
	}
	status, _, errOut := captureApplet(t, cmdXargs, []string{"-t", "/bin/echo", "hi"}, "")
	if status != 0 || !strings.Contains(errOut, "/bin/echo hi") {
		t.Fatalf("xargs -t should echo the command to stderr = (%d, %q)", status, errOut)
	}
	status, out, _ = captureApplet(t, cmdXargs, []string{"-E", "STOP", "/bin/echo"}, "a\nb\nSTOP\nc\n")
	if status != 0 || out != "a b\n" {
		t.Fatalf("xargs -E should stop at the sentinel = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdXargs, []string{"-s", "20", "/bin/echo"}, "aaaa bbbb cccc dddd\n")
	if status != 0 || out != "aaaa bbbb\ncccc dddd\n" {
		t.Fatalf("xargs -s should split batches at the char limit = (%d, %q)", status, out)
	}
	if status, _, errOut := captureApplet(t, cmdXargs, []string{"-s", "10", "-x", "/bin/echo"}, strings.Repeat("a", 40)+"\n"); status == 0 || !strings.Contains(errOut, "too long") {
		t.Fatalf("xargs -s -x should fail on an oversized item = (%d, %q)", status, errOut)
	}

	dir := t.TempDir()
	argFile := filepath.Join(dir, "args")
	if err := os.WriteFile(argFile, []byte("x y z"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, out, _ = captureApplet(t, cmdXargs, []string{"-a", argFile, "/bin/echo"}, "")
	if status != 0 || out != "x y z\n" {
		t.Fatalf("xargs -a = (%d, %q)", status, out)
	}

	// -P runs batches concurrently; verify every item still ran exactly once
	// by having each batch touch its own file rather than relying on stdout
	// ordering, which -P makes nondeterministic.
	stampDir := t.TempDir()
	script := "for a; do : > \"" + stampDir + "/$a\"; done"
	status, _, _ = captureApplet(t, cmdXargs, []string{"-P", "4", "-n", "1", "/bin/sh", "-c", script, "sh"}, "1\n2\n3\n4\n")
	if status != 0 {
		t.Fatalf("xargs -P4 returned %d", status)
	}
	for _, name := range []string{"1", "2", "3", "4"} {
		if _, err := os.Stat(filepath.Join(stampDir, name)); err != nil {
			t.Fatalf("xargs -P4 did not run item %s: %v", name, err)
		}
	}
}

func TestInitSupervisesAndPropagatesStatus(t *testing.T) {
	status, out, stderr := captureApplet(t, cmdInit, []string{"/bin/sh", "-c", "printf init-ok; exit 7"}, "")
	if status != 7 || out != "init-ok" || stderr != "" {
		t.Fatalf("init = (%d, %q, %q)", status, out, stderr)
	}
	status, _, _ = captureApplet(t, cmdInit, []string{"/bin/sh", "-c", "kill -TERM $$"}, "")
	if status != 128+int(syscall.SIGTERM) {
		t.Fatalf("signaled init status = %d", status)
	}
}

func TestInittabParsingAndBootOrdering(t *testing.T) {
	input := strings.Join([]string{
		"# boot configuration",
		"::wait:/bin/echo wait",
		"::sysinit:/bin/mount -t proc proc /proc",
		"ttyS0::respawn:/bin/sh",
		"::once:/bin/echo once",
		"::shutdown:/bin/umount -a -r",
		"broken line",
	}, "\n")
	entries, err := parseInittab(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "line 7") {
		t.Fatalf("expected a line-numbered parse warning, got %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("parsed %d entries, want 5", len(entries))
	}
	var order []string
	for _, action := range []initAction{initSysinit, initWait, initOnce} {
		for _, entry := range entriesForAction(entries, action) {
			order = append(order, string(entry.action))
		}
	}
	if strings.Join(order, ",") != "sysinit,wait,once" {
		t.Fatalf("boot order = %v", order)
	}
	respawn := entriesForAction(entries, initRespawn)
	if len(respawn) != 1 || respawn[0].id != "ttyS0" || respawn[0].command != "/bin/sh" {
		t.Fatalf("respawn entry = %#v", respawn)
	}
}

func TestInitBootAndShutdownActionsExecuteInOrder(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "order")
	hostname := filepath.Join(directory, "hostname")
	kernelHostname := filepath.Join(directory, "kernel-hostname")
	if err := os.WriteFile(kernelHostname, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := []inittabEntry{
		{action: initWait, command: "test \"$(cat " + kernelHostname + ")\" = rescue && printf W >> " + marker, line: 1},
		{action: initSysinit, command: "printf rescue > " + hostname + "; printf S >> " + marker, line: 2},
		{action: initShutdown, command: "printf X >> " + marker, line: 3},
	}
	runInitBootActions(entries, hostname, kernelHostname)
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "SW" {
		t.Fatalf("boot order = %q, %v", data, err)
	}
	data, err = os.ReadFile(kernelHostname)
	if err != nil || string(data) != "rescue\n" {
		t.Fatalf("kernel hostname = %q, %v", data, err)
	}
	runInitActions(entries, initShutdown)
	data, err = os.ReadFile(marker)
	if err != nil || string(data) != "SWX" {
		t.Fatalf("shutdown order = %q, %v", data, err)
	}
}

func TestInitHostnameValidation(t *testing.T) {
	directory := t.TempDir()
	hostname := filepath.Join(directory, "hostname")
	kernelHostname := filepath.Join(directory, "kernel-hostname")
	if err := os.WriteFile(kernelHostname, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	invalid := []string{"", "bad name\n", "bad/name\n", "first\nsecond\n", strings.Repeat("a", maxHostnameLength+1)}
	for _, value := range invalid {
		if err := os.WriteFile(hostname, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := setInitHostname(hostname, kernelHostname); err == nil {
			t.Errorf("setInitHostname(%q) unexpectedly succeeded", value)
		}
		data, err := os.ReadFile(kernelHostname)
		if err != nil || string(data) != "unchanged\n" {
			t.Fatalf("invalid hostname changed target to %q: %v", data, err)
		}
	}
}

func TestInitEnvironmentAndHardeningProfiles(t *testing.T) {
	oldDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	oldMask := syscall.Umask(0)
	syscall.Umask(oldMask)
	t.Cleanup(func() { _ = os.Chdir(oldDirectory); syscall.Umask(oldMask) })
	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM"} {
		t.Setenv(name, "")
	}
	establishInitEnvironment()
	if os.Getenv("PATH") != "/sbin:/bin:/usr/sbin:/usr/bin" || os.Getenv("HOME") != "/root" || os.Getenv("TERM") != "linux" {
		t.Fatalf("incomplete init environment: PATH=%q HOME=%q TERM=%q", os.Getenv("PATH"), os.Getenv("HOME"), os.Getenv("TERM"))
	}
	if profile := hardeningForApplet("init", 1, true); profile.noNewPrivs || profile.seccomp {
		t.Fatalf("PID 1 profile = %+v", profile)
	}
	if profile := hardeningForApplet("init", 42, true); !profile.noNewPrivs || profile.seccomp {
		t.Fatalf("non-PID-1 init profile = %+v", profile)
	}
	if profile := hardeningForApplet("sh", 42, true); profile.noNewPrivs || profile.seccomp {
		t.Fatalf("execution frontend profile = %+v", profile)
	}
	if profile := hardeningForApplet("login", 42, true); profile.noNewPrivs || profile.seccomp {
		t.Fatalf("login profile = %+v", profile)
	}
	if profile := hardeningForApplet("cat", 1, true); !profile.noNewPrivs || !profile.seccomp {
		t.Fatalf("ordinary applet profile = %+v", profile)
	}
	if respawnDelay(1) != time.Second || respawnDelay(9) != 32*time.Second {
		t.Fatalf("unexpected respawn delays")
	}
	if !powerStatusRestored("O\n") || powerStatusRestored("F\n") {
		t.Fatalf("power status classification failed")
	}
}

func TestInitPIDNamespaceRespawnsAndStaysAlive(t *testing.T) {
	if os.Getenv("BA6_PID1_HELPER") == "1" {
		cmdInit([]string{"-f", os.Getenv("BA6_INITTAB")})
		t.Fatal("PID 1 returned")
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare is unavailable")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "respawned")
	powerMarker := filepath.Join(dir, "power")
	shutdownMarker := filepath.Join(dir, "shutdown")
	inittab := filepath.Join(dir, "inittab")
	configuration := "::respawn:/bin/sh -c 'echo x >> " + marker + "; exit 0'\n" +
		"::powerfail:/bin/sh -c 'echo power >> " + powerMarker + "'\n" +
		"::shutdown:/bin/sh -c 'echo shutdown >> " + shutdownMarker + "'\n"
	if err := os.WriteFile(inittab, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(unshare, "--user", "--map-root-user", "--mount", "--pid", "--fork", "--kill-child=SIGKILL", "--mount-proc", os.Args[0], "-test.run=^TestInitPIDNamespaceRespawnsAndStaysAlive$") //nolint:gosec // G204: fixed integration-test command using the current test binary.
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	command.Env = append(os.Environ(), "BA6_PID1_HELPER=1", "BA6_INITTAB="+inittab)
	if err := command.Start(); err != nil {
		t.Skipf("cannot start PID namespace: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if finished {
			return
		}
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		<-done
	})
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case waitErr := <-done:
			finished = true
			message := output.String()
			if strings.Contains(message, "Operation not permitted") || strings.Contains(message, "Permission denied") {
				t.Skipf("PID namespaces unavailable: %s", strings.TrimSpace(message))
			}
			t.Fatalf("PID namespace init exited early: %v: %s", waitErr, message)
		default:
		}
		data, _ := os.ReadFile(marker)
		if strings.Count(string(data), "x\n") >= 2 {
			initPID, err := namespaceInitHostPID(command.Process.Pid)
			if err != nil {
				t.Fatalf("locate namespace PID 1: %v", err)
			}
			if err := syscall.Kill(initPID, 0); err != nil {
				t.Fatalf("PID 1 did not remain alive after respawn: %v", err)
			}
			if err := syscall.Kill(initPID, syscall.SIGPWR); err != nil {
				t.Fatalf("send SIGPWR: %v", err)
			}
			shutdownDeadline := time.Now().Add(4 * time.Second)
			for time.Now().Before(shutdownDeadline) {
				power, _ := os.ReadFile(powerMarker)
				shutdown, _ := os.ReadFile(shutdownMarker)
				if strings.Contains(string(power), "power") && strings.Contains(string(shutdown), "shutdown") {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatalf("SIGPWR actions did not run: %s", output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("respawn was not observed: %s", output.String())
}

func TestInitBinaryPID1HardeningProfile(t *testing.T) {
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare is unavailable")
	}
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "ba6")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binaryPath, ".") //nolint:gosec // G204: fixed Go build integration command.
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOCACHE="+filepath.Join(dir, "go-cache"))
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build init binary: %v: %s", buildErr, output)
	}
	statusFile := filepath.Join(dir, "status")
	parentStatus, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	parentNoNewPrivs := procStatusValue(string(parentStatus), "NoNewPrivs")
	parentFilters := procStatusValue(string(parentStatus), "Seccomp_filters")
	inittab := filepath.Join(dir, "inittab")
	configuration := "::once:/bin/sh -c \"grep -E '^(NoNewPrivs|Seccomp|Seccomp_filters):' /proc/1/status > " + statusFile + "\"\n" +
		"::respawn:/bin/sleep 30\n"
	if err := os.WriteFile(inittab, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(unshare, "--user", "--map-root-user", "--mount", "--pid", "--fork", "--kill-child=SIGKILL", "--mount-proc", binaryPath, "init", "-f", inittab) //nolint:gosec // G204: fixed PID-namespace integration command.
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Skipf("cannot start PID namespace: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if finished {
			return
		}
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		<-done
	})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case waitErr := <-done:
			finished = true
			message := output.String()
			if strings.Contains(message, "Operation not permitted") || strings.Contains(message, "Permission denied") {
				t.Skipf("PID namespaces unavailable: %s", strings.TrimSpace(message))
			}
			t.Fatalf("PID 1 exited early: %v: %s", waitErr, message)
		default:
		}
		data, _ := os.ReadFile(statusFile)
		status := string(data)
		if procStatusValue(status, "NoNewPrivs") == parentNoNewPrivs && procStatusValue(status, "Seccomp_filters") == parentFilters {
			initPID, locateErr := namespaceInitHostPID(command.Process.Pid)
			if locateErr != nil {
				t.Fatal(locateErr)
			}
			if err := syscall.Kill(initPID, syscall.SIGUSR2); err != nil {
				t.Fatalf("stop namespace init: %v", err)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("PID 1 hardening status not observed: %s", output.String())
}

func procStatusValue(status, name string) string {
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimSuffix(fields[0], ":") == name {
			return fields[1]
		}
	}
	return ""
}

func namespaceInitHostPID(unsharePID int) (int, error) {
	path := fmt.Sprintf("/proc/%d/task/%d/children", unsharePID, unsharePID)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) > 0 {
				return strconv.Atoi(fields[0])
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0, fmt.Errorf("no child listed in %s", path)
}

func TestWhichAndPowerOptionValidation(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "rescue-command")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // G306: executable bit is required to test which.
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	status, out, _ := captureApplet(t, cmdWhich, []string{"rescue-command"}, "")
	if status != 0 || strings.TrimSpace(out) != executable {
		t.Fatalf("which = (%d, %q)", status, out)
	}
	status, _, _ = captureApplet(t, cmdReboot, []string{"--invalid"}, "")
	if status != 1 {
		t.Fatalf("invalid reboot status = %d", status)
	}
}

func TestHTTPClientApplet(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("downloaded"))
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	status, out, _ := captureApplet(t, cmdCurl, []string{"-s", server.URL}, "")
	if status != 0 || out != "downloaded" {
		t.Fatalf("curl = (%d, %q)", status, out)
	}

	output := filepath.Join(t.TempDir(), "download")
	status, _, stderr := captureApplet(t, cmdWget, []string{"-O", output, server.URL}, "")
	data, err := os.ReadFile(output)
	if status != 0 || err != nil || string(data) != "downloaded" {
		t.Fatalf("wget = (%d, %q, %v)", status, data, err)
	}
	if !strings.Contains(stderr, "100% (10/10 bytes)") {
		t.Fatalf("wget progress missing from stderr: %q", stderr)
	}
}

func TestIPShortAddressShow(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdIP, []string{"a", "s"}, "")
	if strings.Contains(stderr, `unknown address command "s"`) {
		t.Fatalf("ip a s = (%d, %q, %q)", status, stdout, stderr)
	}
	if status == 0 && stdout == "" {
		t.Fatal("ip a s succeeded without showing addresses")
	}
}

func TestStorageHelpersAndEditorModel(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	status, _, _ := captureApplet(t, cmdMknod, []string{fifo, "p"}, "")
	info, err := os.Stat(fifo)
	if status != 0 || err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("mknod fifo: status=%d info=%v err=%v", status, info, err)
	}

	image := make([]byte, 4096)
	image[1080], image[1081] = 0x53, 0xef
	binary.LittleEndian.PutUint32(image[1116:1120], 4)
	binary.LittleEndian.PutUint32(image[1120:1124], 0x40)
	copy(image[1144:1160], []byte("rescue"))
	device := filepath.Join(dir, "filesystem.img")
	if err := os.WriteFile(device, image, 0o600); err != nil {
		t.Fatal(err)
	}
	kind, label, _, err := probeFilesystem(device)
	if err != nil || kind != "ext4" || label != "rescue" {
		t.Fatalf("probe = (%q, %q, %v)", kind, label, err)
	}

	file := filepath.Join(dir, "edited")
	editor := newMiniEditor(file)
	editor.handleKey('a')
	editor.newline()
	editor.handleKey('b')
	if err := editor.save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "a\nb\n" {
		t.Fatalf("saved editor data = %q, %v", data, err)
	}
}

func TestEditorRefreshDoesNotWrapScreenRows(t *testing.T) {
	status, output, stderr := captureApplet(t, func(_ []string) int {
		editor := newMiniEditor("")
		editor.rows = 3
		editor.cols = 4
		editor.lines = [][]byte{[]byte("1234")}
		editor.refresh()
		return 0
	}, nil, "")
	if status != 0 || stderr != "" {
		t.Fatalf("refresh = (%d, %q)", status, stderr)
	}
	if !strings.Contains(output, "\x1b[?7l") || !strings.Contains(output, "\x1b[?7h") {
		t.Fatalf("editor refresh does not bracket repaint with autowrap control: %q", output)
	}
	if strings.Contains(output, "\r\n") {
		t.Fatalf("editor refresh can scroll via CRLF: %q", output)
	}
}

func TestUnrestrictedAppletClassification(t *testing.T) {
	for _, name := range []string{"sh", "init", "xargs", "mount", "ping", "watch", "wget", "nc"} {
		if !appletNeedsUnrestrictedSyscalls(name) {
			t.Errorf("%s should bypass seccomp", name)
		}
	}
	for _, name := range []string{"cat", "nano", "ss", "dmesg"} {
		if appletNeedsUnrestrictedSyscalls(name) {
			t.Errorf("%s should retain seccomp", name)
		}
	}
}
