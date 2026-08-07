// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
