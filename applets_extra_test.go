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

func TestReadlinkCanonicalizeModes(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("file", link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		want string
		rc   int
	}{
		{[]string{"-f", link}, file + "\n", 0},
		{[]string{"-e", link}, file + "\n", 0},
		{[]string{"-m", filepath.Join(dir, "missing", "child")}, filepath.Join(dir, "missing", "child") + "\n", 0},
		{[]string{"-f", filepath.Join(dir, "missing")}, filepath.Join(dir, "missing") + "\n", 0},
		{[]string{"-e", filepath.Join(dir, "missing")}, "", 1},
		{[]string{"-f", filepath.Join(dir, "missing", "child")}, "", 1},
		{[]string{"-n", "-f", link}, file, 0},
		{[]string{"-z", "-f", link}, file + "\x00", 0},
	}
	for _, c := range cases {
		status, out, stderr := captureApplet(t, cmdReadlink, c.args, "")
		if status != c.rc || out != c.want || stderr != "" {
			t.Fatalf("readlink %v = (%d, %q, %q), want (%d, %q)", c.args, status, out, stderr, c.rc, c.want)
		}
	}
	if status, _, stderr := captureApplet(t, cmdReadlink, []string{"-v", filepath.Join(dir, "missing")}, ""); status != 1 || !strings.Contains(stderr, "No such file or directory") {
		t.Fatalf("readlink -v should report the error = (%d, %q)", status, stderr)
	}
}

func TestRealpathModesAndRelatives(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		want string
		rc   int
	}{
		{[]string{"-s", filepath.Join(dir, "link")}, filepath.Join(dir, "link"), 0},
		{[]string{filepath.Join(dir, "link")}, sub, 0},
		{[]string{"-L", filepath.Join(dir, "link", "..")}, dir, 0},
		{[]string{"-e", filepath.Join(dir, "missing")}, "", 1},
		{[]string{"-m", filepath.Join(dir, "missing", "child")}, filepath.Join(dir, "missing", "child"), 0},
		{[]string{"-s", filepath.Join(dir, "link", "..", "fl")}, filepath.Join(dir, "fl"), 0},
		{[]string{"--relative-to=" + dir, sub}, "sub", 0},
		{[]string{"--relative-to=" + file, sub}, filepath.Join("..", "sub"), 0},
		{[]string{"--relative-base=" + dir, sub}, "sub", 0},
		{[]string{"--relative-base=" + dir, "/etc/hosts"}, "/etc/hosts", 0},
		{[]string{"/"}, "/", 0},
	}
	for _, c := range cases {
		status, out, stderr := captureApplet(t, cmdRealpath, c.args, "")
		if status != c.rc || strings.TrimSuffix(out, "\n") != c.want {
			t.Fatalf("realpath %v = (%d, %q, %q), want (%d, %q)", c.args, status, out, stderr, c.rc, c.want)
		}
	}
	if status, _, _ := captureApplet(t, cmdRealpath, []string{"-q", "-e", filepath.Join(dir, "missing")}, ""); status != 1 {
		t.Fatalf("realpath -q -e = %d", status)
	}
}

func TestHostnameModes(t *testing.T) {
	dir := t.TempDir()
	hosts := filepath.Join(dir, "hosts")
	contents := "127.0.1.1 box.example.org box alias1 alias2\n"
	if err := os.WriteFile(hosts, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	old := hostsFilePath
	hostsFilePath = hosts
	t.Cleanup(func() { hostsFilePath = old })

	canonical, addrs, aliases, ok := hostLookup("box")
	if !ok || canonical != "box.example.org" || len(addrs) != 1 || addrs[0] != "127.0.1.1" || strings.Join(aliases, " ") != "box alias1 alias2" {
		t.Fatalf("hostLookup = (%q, %v, %v, %v)", canonical, addrs, aliases, ok)
	}
	if got := hostnameAliases("box"); got != "box alias1 alias2 " {
		t.Fatalf("hostnameAliases = %q", got)
	}
	if got := hostnameFqdn("box"); got != "box.example.org" {
		t.Fatalf("hostnameFqdn = %q", got)
	}
	if got := hostnameDomain("box.example.org"); got != "example.org" {
		t.Fatalf("hostnameDomain = %q", got)
	}
	if got := hostnameDomain("box"); got != "(none)" {
		t.Fatalf("hostnameDomain short = %q", got)
	}
	if got := hostnameShort("box.example.org"); got != "box" {
		t.Fatalf("hostnameShort = %q", got)
	}
	nameFile := filepath.Join(dir, "name")
	if err := os.WriteFile(nameFile, []byte("# comment\n\ntesthost extra\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := hostnameFileToken(nameFile); err != nil || got != "testhost" {
		t.Fatalf("hostnameFileToken = (%q, %v)", got, err)
	}
	if _, err := hostnameFileToken(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("hostnameFileToken missing file succeeded")
	}
	if _, err := hostnameFileToken(filepath.Join(dir, "empty")); err == nil || !strings.Contains(err.Error(), "No text") {
		// Write the empty file first: the previous check cannot exist twice.
		if os.WriteFile(filepath.Join(dir, "empty"), nil, 0o600) == nil {
			if _, e := hostnameFileToken(filepath.Join(dir, "empty")); e == nil || !strings.Contains(e.Error(), "No text") {
				t.Fatalf("hostnameFileToken empty = %v", e)
			}
		}
	}
	// Setting a name requires root; the failure path still has to be clean.
	if status, _, stderr := captureApplet(t, cmdHostname, []string{"-F", filepath.Join(dir, "missing")}, ""); status == 0 || !strings.Contains(stderr, "fopen") {
		t.Fatalf("hostname -F missing = (%d, %q)", status, stderr)
	}
}

// TestChownOptions covers the options chown and chgrp grew past -R/-h. The ids
// used are the caller's own, so the test needs no privilege: what is checked is
// the reporting, the selection rules and the diagnostics, all of which the
// originals produce identically for a no-op change.
func TestChownOptions(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	user := strconv.Itoa(os.Getuid())
	group := strconv.Itoa(os.Getgid())
	userLabel := userName(uint32(os.Getuid()))   //nolint:gosec // G115: a real uid is nonnegative and fits.
	groupLabel := groupName(uint32(os.Getgid())) //nolint:gosec // G115: same for a gid.

	// -v reports a file whose ownership did not change; -c says nothing.
	status, out, errOut := captureApplet(t, cmdChown, []string{"-v", user, file}, "")
	if status != 0 || out != "ownership of '"+file+"' retained as "+userLabel+"\n" {
		t.Fatalf("chown -v = (%d, %q, %q)", status, out, errOut)
	}
	if status, out, _ = captureApplet(t, cmdChown, []string{"-c", user, file}, ""); status != 0 || out != "" {
		t.Fatalf("chown -c = (%d, %q)", status, out)
	}
	// A group in the spec makes the message name both ids.
	if _, out, _ = captureApplet(t, cmdChown, []string{"-v", user + ":" + group, file}, ""); out !=
		"ownership of '"+file+"' retained as "+userLabel+":"+groupLabel+"\n" {
		t.Fatalf("chown -v with a group = %q", out)
	}
	if _, out, _ = captureApplet(t, cmdChgrp, []string{"-v", group, file}, ""); out !=
		"group of '"+file+"' retained as "+groupLabel+"\n" {
		t.Fatalf("chgrp -v = %q", out)
	}

	// --reference takes both ids from another file.
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, _, errOut = captureApplet(t, cmdChown, []string{"--reference=" + other, file}, ""); status != 0 {
		t.Fatalf("chown --reference = (%d, %q)", status, errOut)
	}
	status, _, errOut = captureApplet(t, cmdChown, []string{"--reference=" + filepath.Join(dir, "absent"), file}, "")
	if status != 1 || !strings.Contains(errOut, "failed to get attributes of") {
		t.Fatalf("chown --reference on a missing file = (%d, %q)", status, errOut)
	}

	// --from only touches files that already carry those ids, and reports the
	// others as unchanged under -v.
	if _, out, _ = captureApplet(t, cmdChown, []string{"-v", "--from=" + user, user, file}, ""); !strings.Contains(out, "retained as") {
		t.Fatalf("chown --from on a matching file = %q", out)
	}

	// -f suppresses the message but not the failing status.
	missing := filepath.Join(dir, "absent")
	status, _, errOut = captureApplet(t, cmdChgrp, []string{group, missing}, "")
	if status != 1 || !strings.Contains(errOut, "cannot access '"+missing+"'") {
		t.Fatalf("chgrp on a missing file = (%d, %q)", status, errOut)
	}
	if status, _, errOut = captureApplet(t, cmdChgrp, []string{"-f", group, missing}, ""); status != 1 || errOut != "" {
		t.Fatalf("chgrp -f on a missing file = (%d, %q)", status, errOut)
	}

	// --preserve-root refuses a recursive run on /, with the original's two
	// lines, and is off by default.
	status, _, errOut = captureApplet(t, cmdChgrp, []string{"-R", "--preserve-root", group, "/"}, "")
	if status != 1 || !strings.Contains(errOut, "it is dangerous to operate recursively on '/'") ||
		!strings.Contains(errOut, "use --no-preserve-root to override this failsafe") {
		t.Fatalf("chgrp --preserve-root / = (%d, %q)", status, errOut)
	}

	// The command-line diagnostics match the originals', Try line included.
	for _, c := range []struct {
		applet applet
		args   []string
		want   string
	}{
		{cmdChgrp, nil, "missing operand"},
		{cmdChgrp, []string{"x"}, "missing operand after 'x'"},
		{cmdChgrp, []string{"definitely-no-such-group", file}, "invalid group: 'definitely-no-such-group'"},
		{cmdChown, []string{"definitely-no-such-user:x", file}, "invalid user: 'definitely-no-such-user:x'"},
		{cmdChown, []string{":definitely-no-such-group", file}, "invalid group: ':definitely-no-such-group'"},
		{cmdChown, []string{"-Q", user, file}, "invalid option -- 'Q'"},
	} {
		status, out, errOut := captureApplet(t, c.applet, c.args, "")
		if status != 1 || out != "" || !strings.Contains(errOut, c.want) {
			t.Fatalf("%v = (%d, %q, %q), want %q", c.args, status, out, errOut, c.want)
		}
	}

	// -R walks children before their parents, so a directory's own line comes
	// last, and every entry is reported under -v.
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "sub", "deep"), []byte("z"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, _ = captureApplet(t, cmdChgrp, []string{"-Rv", group, tree}, "")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], "'"+tree+"'") {
		t.Fatalf("chgrp -Rv printed %q", out)
	}
}
