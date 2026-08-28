// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The expected outputs below were diffed byte-for-byte against GNU coreutils
// 9.7 on the same inputs.

func TestPasteBasics(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	p1 := write("p1", "a\nb\nc\n")
	p2 := write("p2", "1\n2\n")
	p3 := write("p3", "x\ny\nz\nw\n")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{p1, p2, p3}, "a\t1\tx\nb\t2\ty\nc\t\tz\n\t\tw\n"},
		{[]string{"-d", ":;", p1, p2, p3}, "a:1;x\nb:2;y\nc:;z\n:;w\n"},
		{[]string{"-s", p1, p2}, "a\tb\tc\n1\t2\n"},
		{[]string{"-s", "-d", "::", p1, p2}, "a:b:c\n1:2\n"},
		{[]string{"-d", "\\n", p1, p2}, "a\n1\nb\n2\nc\n\n"},
		{[]string{"-d", "\\t", p1, p2}, "a\t1\nb\t2\nc\t\n"},
		{[]string{"-z", p1, p2}, "a\nb\nc\n\t1\n2\n\x00"},
	}
	for _, c := range cases {
		status, out, stderr := captureApplet(t, cmdPaste, c.args, "")
		if status != 0 || out != c.want {
			t.Fatalf("paste %q = (%d, %q, %q), want %q", c.args, status, out, stderr, c.want)
		}
	}
}

func TestPasteErrors(t *testing.T) {
	status, _, stderr := captureApplet(t, cmdPaste, []string{"-d", "\\"}, "x\n")
	if status != 1 || !strings.Contains(stderr, "unescaped backslash") {
		t.Fatalf("paste trailing backslash = (%d, %q)", status, stderr)
	}
	dir := t.TempDir()
	status, _, stderr = captureApplet(t, cmdPaste, []string{"-d", "", filepath.Join(dir, "a"), filepath.Join(dir, "b")}, "")
	if status != 1 || !strings.Contains(stderr, "no delimiters") {
		t.Fatalf("paste empty delimiters = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdPaste, []string{filepath.Join(dir, "missing")}, "")
	if status != 1 || !strings.Contains(stderr, "No such file") {
		t.Fatalf("paste missing file = (%d, %q)", status, stderr)
	}
}

func TestCommBasics(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	c1 := write("c1", "a\nb\nc\nd\n")
	c2 := write("c2", "b\nd\ne\n")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{c1, c2}, "a\n\t\tb\nc\n\t\td\n\te\n"},
		{[]string{"-1", c1, c2}, "\tb\n\td\ne\n"},
		{[]string{"-12", c1, c2}, "b\nd\n"},
		{[]string{"-3", c1, c2}, "a\nc\n\te\n"},
	}
	for _, c := range cases {
		status, out, stderr := captureApplet(t, cmdComm, c.args, "")
		if status != 0 || out != c.want {
			t.Fatalf("comm %q = (%d, %q, %q), want %q", c.args, status, out, stderr, c.want)
		}
	}
}

func TestCommUnsortedAndTotal(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	u1 := write("u1", "z\nc\nb\na\n")
	u2 := write("u2", "b\n")
	status, out, stderr := captureApplet(t, cmdComm, []string{u1, u2}, "")
	if status != 1 || out != "\tb\nz\nc\nb\na\n" {
		t.Fatalf("comm unsorted = (%d, %q, %q)", status, out, stderr)
	}
	if !strings.Contains(stderr, "file 1 is not in sorted order") || !strings.Contains(stderr, "input is not in sorted order") {
		t.Fatalf("comm unsorted stderr = %q", stderr)
	}
	s1 := write("s1", "a\nb\nc\nd\n")
	s2 := write("s2", "b\nd\ne\n")
	status, out, _ = captureApplet(t, cmdComm, []string{"--total", s1, s2}, "")
	if status != 0 || !strings.HasSuffix(out, "2\t1\t2\ttotal\n") {
		t.Fatalf("comm --total = (%d, %q)", status, out)
	}
}

func TestSplitSuffixSequence(t *testing.T) {
	s := newSplitSuffix("abcdefghijklmnopqrstuvwxyz", "x", "", 2, true)
	var names []string
	for i := 0; i < 680; i++ {
		name, ok := s.next()
		if !ok {
			t.Fatalf("suffix exhausted at %d", i)
		}
		names = append(names, name)
	}
	if names[0] != "xaa" || names[25] != "xaz" || names[26] != "xba" {
		t.Fatalf("early names: %v", names[:30])
	}
	if names[649] != "xyz" || names[650] != "xzaaa" || names[676] != "xzaba" {
		t.Fatalf("widening names: %q %q %q", names[649], names[650], names[676])
	}
	d := newSplitSuffix("0123456789", "p", "", 2, true)
	for i := 0; i < 92; i++ {
		name, ok := d.next()
		if !ok {
			t.Fatalf("numeric suffix exhausted at %d", i)
		}
		if i == 90 && name != "p9000" {
			t.Fatalf("numeric widen produced %q", name)
		}
		if i == 91 && name != "p9001" {
			t.Fatalf("numeric widen produced %q", name)
		}
	}
	fixed := newSplitSuffix("abcdefghijklmnopqrstuvwxyz", "q", "", 1, false)
	for i := 0; i < 26; i++ {
		if _, ok := fixed.next(); !ok {
			t.Fatalf("fixed suffix exhausted at %d", i)
		}
	}
	if _, ok := fixed.next(); ok {
		t.Fatal("fixed suffix did not exhaust")
	}
}

func TestSplitBasics(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	write := func(name, contents string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	nums := write("nums", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	status, _, stderr := captureApplet(t, cmdSplit, []string{"-l", "4", nums}, "")
	if status != 0 {
		t.Fatalf("split = (%d, %q)", status, stderr)
	}
	for name, want := range map[string]string{"xaa": "1\n2\n3\n4\n", "xab": "5\n6\n7\n8\n", "xac": "9\n10\n"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("split chunk %s = %q, %v", name, data, err)
		}
	}
	status, _, stderr = captureApplet(t, cmdSplit, []string{"-b", "7", nums, "byte"}, "")
	if status != 0 {
		t.Fatalf("split -b = (%d, %q)", status, stderr)
	}
	for name, want := range map[string]string{"byteaa": "1\n2\n3\n4", "byteab": "\n5\n6\n7\n", "byteac": "8\n9\n10\n"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("split -b chunk %s = %q, %v", name, data, err)
		}
	}
}

func TestSplitChunkModes(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	nums := filepath.Join(dir, "nums")
	if err := os.WriteFile(nums, []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdSplit, []string{"-n", "3", nums, "p"}, "")
	if status != 0 {
		t.Fatalf("split -n = (%d, %q)", status, stderr)
	}
	for name, want := range map[string]string{"paa": "1\n2\n3\n4", "pab": "\n5\n6\n7\n", "pac": "8\n9\n10\n"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("split -n chunk %s = %q, %v", name, data, err)
		}
	}
	status, out, stderr := captureApplet(t, cmdSplit, []string{"-n", "2/3", nums}, "")
	if status != 0 || out != "\n5\n6\n7\n" {
		t.Fatalf("split -n 2/3 = (%d, %q, %q)", status, out, stderr)
	}
	status, _, stderr = captureApplet(t, cmdSplit, []string{"-n", "l/3", nums, "l"}, "")
	if status != 0 {
		t.Fatalf("split -n l/3 = (%d, %q)", status, stderr)
	}
	for name, want := range map[string]string{"laa": "1\n2\n3\n4\n", "lab": "5\n6\n7\n", "lac": "8\n9\n10\n"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("split -n l/3 chunk %s = %q, %v", name, data, err)
		}
	}
	status, _, stderr = captureApplet(t, cmdSplit, []string{"-n", "r/3", nums, "r"}, "")
	if status != 0 {
		t.Fatalf("split -n r/3 = (%d, %q)", status, stderr)
	}
	for name, want := range map[string]string{"raa": "1\n4\n7\n10\n", "rab": "2\n5\n8\n", "rac": "3\n6\n9\n"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("split -n r/3 chunk %s = %q, %v", name, data, err)
		}
	}
}

func TestSplitNumericAndVerbose(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	nums := filepath.Join(dir, "nums")
	if err := os.WriteFile(nums, []byte("1\n2\n3\n4\n5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdSplit, []string{"-l", "2", "-d", nums, "n"}, "")
	if status != 0 {
		t.Fatalf("split -d = (%d, %q)", status, stderr)
	}
	for name, want := range map[string]string{"n00": "1\n2\n", "n01": "3\n4\n", "n02": "5\n"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("split -d chunk %s = %q, %v", name, data, err)
		}
	}
	status, out, stderr := captureApplet(t, cmdSplit, []string{"-l", "3", "--verbose", nums, "v"}, "")
	if status != 0 || out != "creating file vaa\ncreating file vab\n" {
		t.Fatalf("split --verbose = (%d, %q, %q)", status, out, stderr)
	}
	status, _, stderr = captureApplet(t, cmdSplit, []string{"-b", "0", nums}, "")
	if status != 1 || !strings.Contains(stderr, "invalid number of bytes") {
		t.Fatalf("split -b 0 = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdSplit, []string{"-l", "2", "-b", "3", nums}, "")
	if status != 1 || !strings.Contains(stderr, "more than one way") {
		t.Fatalf("split conflicting modes = (%d, %q)", status, stderr)
	}
}

func TestJoinBasics(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	j1 := write("j1", "1 a\n2 b\n3 c\n")
	j2 := write("j2", "2 X\n3 Y\n5 Z\n")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{j1, j2}, "2 b X\n3 c Y\n"},
		{[]string{"-a", "1", j1, j2}, "1 a\n2 b X\n3 c Y\n"},
		{[]string{"-v", "1", j1, j2}, "1 a\n"},
		{[]string{"-o", "1.1,2.2,1.2", j1, j2}, "2 X b\n3 Y c\n"},
		{[]string{"-a", "1", "-e", "-", "-o", "1.1,2.2", j1, j2}, "1 -\n2 X\n3 Y\n"},
	}
	for _, c := range cases {
		status, out, stderr := captureApplet(t, cmdJoin, c.args, "")
		if status != 0 || out != c.want {
			t.Fatalf("join %q = (%d, %q, %q), want %q", c.args, status, out, stderr, c.want)
		}
	}
}

func TestJoinFieldsAndSeparators(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	k1 := write("k1", "pre 1 post\n")
	k2 := write("k2", "1 X\n")
	status, out, stderr := captureApplet(t, cmdJoin, []string{"-1", "2", k1, k2}, "")
	if status != 0 || out != "1 pre post X\n" {
		t.Fatalf("join -1 2 = (%d, %q, %q)", status, out, stderr)
	}
	t1 := write("t1", "1:a\n2:b\n")
	t2 := write("t2", "2:X\n")
	status, out, stderr = captureApplet(t, cmdJoin, []string{"-t", ":", t1, t2}, "")
	if status != 0 || out != "2:b:X\n" {
		t.Fatalf("join -t = (%d, %q, %q)", status, out, stderr)
	}
	u1 := write("u1", "b 1\na 2\n")
	status, _, stderr = captureApplet(t, cmdJoin, []string{u1, k2}, "")
	if status != 1 || !strings.Contains(stderr, "is not sorted") {
		t.Fatalf("join unsorted = (%d, %q)", status, stderr)
	}
}

func TestUniqSkipAndWidth(t *testing.T) {
	status, out, _ := captureApplet(t, cmdUniq, []string{"-f1", "-c"}, "a  x\na x\n")
	if status != 0 || out != "      1 a  x\n      1 a x\n" {
		t.Fatalf("uniq -f1 = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdUniq, []string{"-s1", "-w2", "-c"}, "x a b\nx a c\n")
	if status != 0 || out != "      2 x a b\n" {
		t.Fatalf("uniq -s1 -w2 = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdUniq, []string{"-w1", "-c"}, "ab x\nac y\n")
	if status != 0 || out != "      2 ab x\n" {
		t.Fatalf("uniq -w1 = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdUniq, []string{"-f1", "-s1", "-w1", "-c"}, "a\nb\n")
	if status != 0 || out != "      2 a\n" {
		t.Fatalf("uniq -f1 -s1 -w1 = (%d, %q)", status, out)
	}
}

func TestUniqGroupsAndZeroTerminated(t *testing.T) {
	in := "a\na\nb\nc\nc\n"
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-D"}, "a\na\nc\nc\n"},
		{[]string{"--all-repeated=prepend"}, "\na\na\n\nc\nc\n"},
		{[]string{"--all-repeated=separate"}, "a\na\n\nc\nc\n"},
		{[]string{"-D", "-u"}, "a\nc\n"},
		{[]string{"--group"}, "a\na\n\nb\n\nc\nc\n"},
		{[]string{"--group=prepend"}, "\na\na\n\nb\n\nc\nc\n"},
		{[]string{"--group=append"}, "a\na\n\nb\n\nc\nc\n\n"},
		{[]string{"--group=both"}, "\na\na\n\nb\n\nc\nc\n\n"},
	}
	for _, c := range cases {
		status, out, stderr := captureApplet(t, cmdUniq, c.args, in)
		if status != 0 || out != c.want {
			t.Fatalf("uniq %v = (%d, %q, %q)", c.args, status, out, stderr)
		}
	}
	status, out, _ := captureApplet(t, cmdUniq, []string{"-z", "-c"}, "a\x00a\x00b\x00")
	if status != 0 || out != "      2 a\x00      1 b\x00" {
		t.Fatalf("uniq -z -c = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdUniq, []string{"-z", "-D"}, "a\x00a\x00b\x00c\x00c\x00")
	if status != 0 || out != "a\x00a\x00c\x00c\x00" {
		t.Fatalf("uniq -z -D = (%d, %q)", status, out)
	}
	if status, _, stderr := captureApplet(t, cmdUniq, []string{"-D", "-c"}, "a\na\n"); status == 0 || !strings.Contains(stderr, "meaningless") {
		t.Fatalf("uniq -D -c should be rejected = (%d, %q)", status, stderr)
	}
	if status, _, stderr := captureApplet(t, cmdUniq, []string{"--group", "-d"}, "a\na\n"); status == 0 || !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("uniq --group -d should be rejected = (%d, %q)", status, stderr)
	}
}
