// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLnReadlinkAndRealpath(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	hard := filepath.Join(dir, "hard")
	symlink := filepath.Join(dir, "symbolic")
	if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := cmdLn([]string{source, hard}); status != 0 {
		t.Fatalf("ln returned %d", status)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	hardInfo, err := os.Stat(hard)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(sourceInfo, hardInfo) {
		t.Fatal("hard link does not refer to the source inode")
	}
	if status := cmdLn([]string{"-s", "source", symlink}); status != 0 {
		t.Fatalf("ln -s returned %d", status)
	}
	status, stdout, stderr := captureApplet(t, cmdReadlink, []string{symlink}, "")
	if status != 0 || stdout != "source\n" {
		t.Fatalf("readlink=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdRealpath, []string{symlink}, "")
	if status != 0 || strings.TrimSpace(stdout) != source {
		t.Fatalf("realpath=(%d,%q,%q), want %q", status, stdout, stderr, source)
	}
}

func TestLnForceMissingSourcePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "destination")
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, _ := captureApplet(t, cmdLn, []string{"-f", filepath.Join(dir, "missing"), destination}, "")
	if status == 0 {
		t.Fatal("linking a missing source succeeded")
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("destination was not preserved: contents=%q err=%v", contents, err)
	}
}

func TestChmodRecursiveAndOwnership(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tree")
	file := filepath.Join(dir, "file")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if status := cmdChmod([]string{"-R", "4750", dir}); status != 0 {
		t.Fatalf("chmod returned %d", status)
	}
	for _, path := range []string{dir, file} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o750 || info.Mode()&os.ModeSetuid == 0 {
			t.Fatalf("%s mode is %v", path, info.Mode())
		}
	}
	owner := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	if status := cmdChown([]string{owner, file}); status != 0 {
		t.Fatalf("chown to current identity returned %d", status)
	}
	if status := cmdChgrp([]string{strconv.Itoa(os.Getgid()), file}); status != 0 {
		t.Fatalf("chgrp to current group returned %d", status)
	}
}

func TestChmodSymbolicModes(t *testing.T) {
	cases := []struct {
		name string
		init os.FileMode
		mode string
		want os.FileMode
	}{
		{"add-owner-exec", 0o644, "u+x", 0o744},
		{"remove-group-other-read", 0o644, "go-r", 0o600},
		{"set-all-rw-clears-special", fileModeFromOctal(0o1755), "a=rw", 0o666},
		{"chained-ops-same-who", 0o644, "u+x-w", 0o544},
		{"remove-only-requested-bit", fileModeFromOctal(0o4755), "u-x", fileModeFromOctal(0o4655)},
		{"capital-x-skips-plain-file", 0o644, "a+X", 0o644},
		{"capital-x-extends-existing-exec", 0o744, "a+X", 0o755},
		{"copy-from-owner", 0o700, "go=u", 0o777},
		{"equals-preserves-other-special-bit", fileModeFromOctal(0o4755), "g=rx", fileModeFromOctal(0o4755)},
		{"multiple-clauses", 0o000, "u=rwx,g=rx", 0o750},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.init); err != nil { //nolint:gosec // G302: exercising chmod's symbolic-mode math needs the exact starting bits
				t.Fatal(err)
			}
			if status := cmdChmod([]string{tc.mode, path}); status != 0 {
				t.Fatalf("chmod %s returned %d", tc.mode, status)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode() != tc.want {
				t.Fatalf("chmod %s on %v: got %v, want %v", tc.mode, tc.init, info.Mode(), tc.want)
			}
		})
	}
}

func TestChmodOmittedWhoHonoursUmask(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil { //nolint:gosec // G302: read-only starting mode is the point of this test
		t.Fatal(err)
	}
	if status := cmdChmod([]string{"+w", path}); status != 0 {
		t.Fatalf("chmod +w returned %d", status)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// umask 022 masks the write bit out of group/other, so only the owner
	// gains it: 0444 -> 0644, not 0666.
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode is %v, want 0644", info.Mode().Perm())
	}
}

func TestChmodReferenceAndReporting(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(ref, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ref, 0o640); err != nil { //nolint:gosec // G302: --reference needs a specific source mode to copy
		t.Fatal(err)
	}
	if err := os.WriteFile(target, nil, 0o644); err != nil { //nolint:gosec // G306: chmod --reference target starts wider than 0600 to prove the mode actually narrows
		t.Fatal(err)
	}
	if status := cmdChmod([]string{"--reference=" + ref, target}); status != 0 {
		t.Fatalf("chmod --reference returned %d", status)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("target mode is %v, want 0640", info.Mode())
	}

	unchanged := filepath.Join(dir, "unchanged")
	if err := os.WriteFile(unchanged, nil, 0o644); err != nil { //nolint:gosec // G306: chmod -c must observe the file is already 0644 to correctly report no change
		t.Fatal(err)
	}
	if status := cmdChmod([]string{"-c", "644", unchanged}); status != 0 {
		t.Fatalf("chmod -c returned %d", status)
	}
	if status := cmdChmod([]string{"zz", unchanged}); status == 0 {
		t.Fatal("chmod with an invalid symbolic mode should fail")
	}
	if status := cmdChmod([]string{"+x"}); status == 0 {
		t.Fatal("chmod with a mode but no file operand should fail")
	}
}

func TestStatFormatsMetadataAndEmptyFormat(t *testing.T) {
	file := filepath.Join(t.TempDir(), "item")
	if err := os.WriteFile(file, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdStat, []string{"-c", "%n:%s:%a:%F", file}, "")
	want := file + ":3:600:regular file\n"
	if status != 0 || stdout != want {
		t.Fatalf("stat=(%d,%q,%q), want %q", status, stdout, stderr, want)
	}
	status, stdout, stderr = captureApplet(t, cmdStat, []string{"-c", "", file}, "")
	if status != 0 || stdout != "\n" {
		t.Fatalf("empty stat format=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestTeeCopiesAndAppends(t *testing.T) {
	file := filepath.Join(t.TempDir(), "output")
	status, stdout, stderr := captureApplet(t, cmdTee, []string{file}, "one\n")
	if status != 0 || stdout != "one\n" {
		t.Fatalf("tee=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdTee, []string{"-a", file}, "two\n")
	if status != 0 || stdout != "two\n" {
		t.Fatalf("tee -a=(%d,%q,%q)", status, stdout, stderr)
	}
	contents, err := os.ReadFile(file)
	if err != nil || string(contents) != "one\ntwo\n" {
		t.Fatalf("tee file=%q err=%v", contents, err)
	}
}

func TestBasenameAndDirname(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdBasename, []string{"-s", ".go", "/tmp/one.go", "two.go"}, "")
	if status != 0 || stdout != "one\ntwo\n" {
		t.Fatalf("basename=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdDirname, []string{"/tmp/one", "plain"}, "")
	if status != 0 || stdout != "/tmp\n.\n" {
		t.Fatalf("dirname=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestTestAndBracketExpressions(t *testing.T) {
	file := filepath.Join(t.TempDir(), "item")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, expression := range [][]string{
		{"5", "-gt", "2", "-a", "-f", file},
		{"!", "-z", "value"},
		{"!", "=", "!"},
		{"(", "x", "=", "x", ")", "-o", "", "=", "x"},
	} {
		if status := cmdTest(expression); status != 0 {
			t.Fatalf("test %q returned %d", expression, status)
		}
	}
	if status := cmdBracket([]string{"-s", file, "]"}); status != 0 {
		t.Fatalf("[ -s file ] returned %d", status)
	}
	if status, _, _ := captureApplet(t, cmdBracket, []string{"x"}, ""); status != 2 {
		t.Fatalf("bracket without closing delimiter returned %d", status)
	}
}

func TestDateSleepTrueAndFalse(t *testing.T) {
	file := filepath.Join(t.TempDir(), "reference")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	epoch := time.Unix(0, 123456789)
	if err := os.Chtimes(file, epoch, epoch); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdDate, []string{"-u", "-r", file, "+%F %T.%N %Z"}, "")
	if status != 0 || stdout != "1970-01-01 00:00:00.123456789 UTC\n" {
		t.Fatalf("date=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdDate, []string{"-r", file, "+"}, "")
	if status != 0 || stdout != "\n" {
		t.Fatalf("empty date format=(%d,%q,%q)", status, stdout, stderr)
	}
	if status := cmdSleep([]string{"0", "0s"}); status != 0 {
		t.Fatalf("sleep returned %d", status)
	}
	if cmdTrue(nil) != 0 || cmdFalse(nil) != 1 {
		t.Fatal("true/false returned incorrect statuses")
	}
}

func TestTeeOutputErrorModes(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full is unavailable")
	}
	status, _, stderr := captureApplet(t, cmdTee, []string{"/dev/full"}, "x\n")
	if status != 1 || !strings.Contains(stderr, "/dev/full") {
		t.Fatalf("tee default = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdTee, []string{"-p", "/dev/full"}, "x\n")
	if status != 1 || !strings.Contains(stderr, "/dev/full") {
		t.Fatalf("tee -p = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdTee, []string{"--output-error=warn", "/dev/full"}, "x\n")
	if status != 1 || !strings.Contains(stderr, "/dev/full") {
		t.Fatalf("tee warn = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdTee, []string{"--output-error=warn-nopipe", "/dev/full"}, "x\n")
	if status != 1 || !strings.Contains(stderr, "/dev/full") {
		t.Fatalf("tee warn-nopipe = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdTee, []string{"--output-error=exit", "/dev/full"}, "x\n")
	if status != 1 || !strings.Contains(stderr, "/dev/full") {
		t.Fatalf("tee exit = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdTee, []string{"--output-error=exit-nopipe", "/dev/full"}, "x\n")
	if status != 1 || !strings.Contains(stderr, "/dev/full") {
		t.Fatalf("tee exit-nopipe = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdTee, []string{"--output-error=bogus", "/dev/full"}, "x\n")
	if status != 1 || !strings.Contains(stderr, "invalid mode") {
		t.Fatalf("tee bogus mode = (%d, %q)", status, stderr)
	}
	file := filepath.Join(t.TempDir(), "output")
	status, stdout, stderr := captureApplet(t, cmdTee, []string{"--output-error=exit-nopipe", file}, "ok\n")
	if status != 0 || stdout != "ok\n" || stderr != "" {
		t.Fatalf("tee healthy exit-nopipe = (%d, %q, %q)", status, stdout, stderr)
	}
}

func TestMkdirRmdirVerbose(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(dir, "a")
	nested := filepath.Join(dir, "a", "b", "c")
	status, stdout, stderr := captureApplet(t, cmdMkdir, []string{"-v", "-p", nested}, "")
	if status != 0 {
		t.Fatalf("mkdir -v -p = (%d, %q)", status, stderr)
	}
	want := "mkdir: created directory '" + a + "'\nmkdir: created directory '" + filepath.Join(dir, "a", "b") + "'\nmkdir: created directory '" + nested + "'\n"
	if stdout != want {
		t.Fatalf("mkdir -v -p output = %q, want %q", stdout, want)
	}
	status, stdout, stderr = captureApplet(t, cmdMkdir, []string{"-v", nested}, "")
	if status != 1 || stdout != "" || !strings.Contains(stderr, "File exists") {
		t.Fatalf("mkdir -v existing = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdRmdir, []string{"-v", "-p", nested}, "")
	if status != 1 {
		t.Fatalf("rmdir -v -p = (%d, %q)", status, stderr)
	}
	want = "rmdir: removing directory, '" + nested + "'\nrmdir: removing directory, '" + filepath.Join(dir, "a", "b") + "'\nrmdir: removing directory, '" + a + "'\n"
	if stdout != want {
		t.Fatalf("rmdir -v -p output = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "rmdir: failed to remove '"+dir+"'") {
		t.Fatalf("rmdir -v -p stderr = %q", stderr)
	}
}

func TestPrintenvNullSeparator(t *testing.T) {
	t.Setenv("BA6_TEST_PRINTENV_A", "v1")
	t.Setenv("BA6_TEST_PRINTENV_B", "v2")
	status, stdout, stderr := captureApplet(t, cmdPrintenv, []string{"BA6_TEST_PRINTENV_A", "BA6_TEST_PRINTENV_B"}, "")
	if status != 0 || stdout != "v1\nv2\n" {
		t.Fatalf("printenv = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdPrintenv, []string{"-0", "BA6_TEST_PRINTENV_A", "BA6_TEST_PRINTENV_B"}, "")
	if status != 0 || stdout != "v1\x00v2\x00" {
		t.Fatalf("printenv -0 = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdPrintenv, []string{"--null", "BA6_TEST_PRINTENV_A"}, "")
	if status != 0 || stdout != "v1\x00" {
		t.Fatalf("printenv --null = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdPrintenv, []string{"BA6_TEST_PRINTENV_A", "-0"}, "")
	if status != 1 || stdout != "v1\n" {
		t.Fatalf("printenv stops options at first operand = (%d, %q, %q)", status, stdout, stderr)
	}
}

func TestBasenameDirnameZero(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdBasename, []string{"-z", "/tmp/one"}, "")
	if status != 0 || stdout != "one\x00" {
		t.Fatalf("basename -z = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdBasename, []string{"-z", "-a", "/tmp/one", "plain"}, "")
	if status != 0 || stdout != "one\x00plain\x00" {
		t.Fatalf("basename -z -a = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdDirname, []string{"-z", "/tmp/one", "plain"}, "")
	if status != 0 || stdout != "/tmp\x00.\x00" {
		t.Fatalf("dirname -z = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdDirname, []string{"--zero", "/tmp/one"}, "")
	if status != 0 || stdout != "/tmp\x00" {
		t.Fatalf("dirname --zero = (%d, %q, %q)", status, stdout, stderr)
	}
}
