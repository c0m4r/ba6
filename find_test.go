// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Every expectation below was diffed against findutils 4.11 on a live system.

// findTestTree builds a small tree with known modes, sizes and stamps:
//
//	root/a/f1        6 bytes, mode 0751
//	root/a/b/f2      3 bytes, mode 0644
//	root/a/link      symlink to f1
//	root/c/empty     0 bytes
//	root/c/broken    symlink to /nowhere
func findTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"a/b", "c"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// The modes are set afterwards: -perm is one of the things under test, so
	// they have to be exactly these values.
	for _, file := range []struct {
		path string
		data string
		mode os.FileMode
	}{
		{filepath.Join(root, "a", "f1"), "hello\n", 0o751},
		{filepath.Join(root, "a", "b", "f2"), "hi\n", 0o644},
		{filepath.Join(root, "c", "empty"), "", 0o644},
	} {
		if err := os.WriteFile(file.path, []byte(file.data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file.path, file.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("f1", filepath.Join(root, "a", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere", filepath.Join(root, "c", "broken")); err != nil {
		t.Fatal(err)
	}
	return root
}

// findPaths runs find and returns its output sorted, so the comparison does not
// depend on the order the kernel hands back directory entries.
func findPaths(t *testing.T, args []string) (int, []string, string) {
	t.Helper()
	status, stdout, stderr := captureApplet(t, cmdFind, args, "")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if stdout == "" {
		lines = nil
	}
	sort.Strings(lines)
	return status, lines, stderr
}

func TestFindPredicates(t *testing.T) {
	root := findTestTree(t)
	rel := func(names ...string) []string {
		out := make([]string, 0, len(names))
		for _, name := range names {
			if name == "" {
				out = append(out, root)
				continue
			}
			out = append(out, root+"/"+name)
		}
		sort.Strings(out)
		return out
	}
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{root, "-type", "f"}, rel("a/f1", "a/b/f2", "c/empty")},
		{[]string{root, "-type", "l"}, rel("a/link", "c/broken")},
		{[]string{root, "-type", "d"}, rel("", "a", "a/b", "c")},
		{[]string{root, "-name", "f?"}, rel("a/f1", "a/b/f2")},
		{[]string{root, "-name", "[fl]*"}, rel("a/f1", "a/b/f2", "a/link")},
		// A -path pattern's "*" crosses slashes, unlike a shell glob.
		{[]string{root, "-path", "*/a/*"}, rel("a/f1", "a/b", "a/b/f2", "a/link")},
		{[]string{root, "-regex", ".*/f[0-9]"}, rel("a/f1", "a/b/f2")},
		{[]string{root, "-lname", "f1"}, rel("a/link")},
		{[]string{root, "-empty", "-type", "f"}, rel("c/empty")},
		// A size in 1K blocks rounds up, so only the empty file is under one.
		{[]string{root, "-size", "-1k", "-type", "f"}, rel("c/empty")},
		{[]string{root, "-size", "+0c", "-type", "f"}, rel("a/f1", "a/b/f2")},
		{[]string{root, "-perm", "0751"}, rel("a/f1")},
		{[]string{root, "-perm", "-0100", "-type", "f"}, rel("a/f1")},
		{[]string{root, "-perm", "/0111", "-type", "f"}, rel("a/f1")},
		{[]string{root, "-perm", "-u+x", "-type", "f"}, rel("a/f1")},
		{[]string{root, "-links", "1", "-type", "f"}, rel("a/f1", "a/b/f2", "c/empty")},
		{[]string{root, "-samefile", filepath.Join(root, "a", "f1")}, rel("a/f1")},
		{[]string{root, "-maxdepth", "1", "-type", "d"}, rel("", "a", "c")},
		{[]string{root, "-mindepth", "3"}, rel("a/b/f2")},
		{[]string{root, "-readable", "-type", "f"}, rel("a/f1", "a/b/f2", "c/empty")},
		{[]string{root, "-executable", "-type", "f"}, rel("a/f1")},
		{[]string{root, "(", "-name", "f1", "-o", "-name", "f2", ")"}, rel("a/f1", "a/b/f2")},
		{[]string{root, "!", "-type", "f", "-maxdepth", "1"}, rel("", "a", "c")},
		// -prune stops the descent but still matches the directory itself.
		{[]string{root, "-name", "b", "-prune", "-o", "-print"},
			rel("", "a", "a/f1", "a/link", "c", "c/empty", "c/broken")},
	} {
		status, got, stderr := findPaths(t, c.args)
		if status != 0 || !equalStrings(got, c.want) {
			t.Errorf("find %v = (%d, %v, %q), want %v", c.args, status, got, stderr, c.want)
		}
	}
}

func TestFindTraversalOrderAndDepth(t *testing.T) {
	root := findTestTree(t)
	// -depth reverses the order of a directory against what it holds; every
	// child must appear before its parent.
	_, stdout, stderr := captureApplet(t, cmdFind, []string{root, "-depth"}, "")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 9 {
		t.Fatalf("find -depth printed %d lines (%q, %q)", len(lines), stdout, stderr)
	}
	seen := map[string]int{}
	for i, line := range lines {
		seen[line] = i
	}
	for _, line := range lines {
		parent := filepath.Dir(line)
		if at, ok := seen[parent]; ok && at < seen[line] {
			t.Errorf("find -depth printed %q before its child %q", parent, line)
		}
	}
	// -quit stops the walk before the implicit -print runs.
	status, stdout, _ := captureApplet(t, cmdFind, []string{root, "-type", "f", "-quit"}, "")
	if status != 0 || stdout != "" {
		t.Errorf("find -quit = (%d, %q)", status, stdout)
	}
	status, stdout, _ = captureApplet(t, cmdFind, []string{root, "-name", "f1", "-print", "-quit"}, "")
	if status != 0 || stdout != root+"/a/f1\n" {
		t.Errorf("find -print -quit = (%d, %q)", status, stdout)
	}
}

func TestFindPrintfAndLs(t *testing.T) {
	root := findTestTree(t)
	file := filepath.Join(root, "a", "f1")
	stamp := time.Date(2020, time.January, 2, 3, 4, 5, 0, time.Local)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdFind,
		[]string{file, "-printf", `%p|%f|%h|%P|%s|%m|%M|%n|%y|%d|%TY-%Tm-%Td %TH:%TM\n`}, "")
	want := file + "|f1|" + filepath.Join(root, "a") + "||6|751|-rwxr-x--x|1|f|0|2020-01-02 03:04\n"
	if status != 0 || stdout != want {
		t.Fatalf("find -printf = (%d, %q, %q), want %q", status, stdout, stderr, want)
	}
	// %P strips the operand the entry was found under.
	status, stdout, _ = captureApplet(t, cmdFind, []string{filepath.Join(root, "a"), "-printf", `%P\n`}, "")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	sort.Strings(lines)
	if status != 0 || !equalStrings(lines, []string{"", "b", "b/f2", "f1", "link"}) {
		t.Fatalf("find -printf %%P = (%d, %q)", status, stdout)
	}
	// An unusable directive is reported once and then printed as written.
	status, stdout, stderr = captureApplet(t, cmdFind, []string{file, "-printf", `%q\n`}, "")
	if status != 0 || stdout != "%q\n" || !strings.Contains(stderr, "unrecognized format directive") {
		t.Fatalf("find -printf %%q = (%d, %q, %q)", status, stdout, stderr)
	}
	// -ls holds the ls -dils fields, ending with the name.
	status, stdout, _ = captureApplet(t, cmdFind, []string{file, "-ls"}, "")
	fields := strings.Fields(stdout)
	if status != 0 || len(fields) < 9 || fields[2] != "-rwxr-x--x" || fields[len(fields)-1] != file {
		t.Fatalf("find -ls = (%d, %q)", status, stdout)
	}
}

func TestFindDeleteAndDiagnostics(t *testing.T) {
	root := findTestTree(t)
	status, _, stderr := captureApplet(t, cmdFind, []string{root, "-name", "f?", "-delete"}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("find -delete = (%d, %q)", status, stderr)
	}
	if _, err := os.Lstat(filepath.Join(root, "a", "f1")); !os.IsNotExist(err) {
		t.Error("find -delete left a/f1 behind")
	}
	if _, err := os.Lstat(filepath.Join(root, "a", "link")); err != nil {
		t.Error("find -delete removed more than it matched")
	}
	// A non-empty directory cannot be removed, and that is reported.
	status, _, stderr = captureApplet(t, cmdFind, []string{root, "-name", "a", "-delete"}, "")
	if status != 1 || !strings.Contains(stderr, "Directory not empty") {
		t.Errorf("find -delete on a full directory = (%d, %q)", status, stderr)
	}

	// The diagnostics use find's own `x' quoting.
	status, _, stderr = captureApplet(t, cmdFind, []string{root, "-bogus"}, "")
	if status != 1 || !strings.Contains(stderr, "unknown predicate `-bogus'") {
		t.Errorf("find -bogus = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdFind, []string{root, "-name"}, "")
	if status != 1 || !strings.Contains(stderr, "missing argument to `-name'") {
		t.Errorf("find -name with no argument = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdFind, []string{filepath.Join(root, "missing")}, "")
	if status != 1 || !strings.Contains(stderr, "No such file or directory") {
		t.Errorf("find on a missing path = (%d, %q)", status, stderr)
	}
}

func TestGlobMatchCrossesSlashes(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"*", "a/b", true},
		{"a/*", "a/b/c", true},
		{"a?c", "abc", true},
		{"a?c", "a/c", true},
		{"[abc]x", "bx", true},
		{"[!abc]x", "bx", false},
		{"[!abc]x", "dx", true},
		{"[a-c]x", "cx", true},
		{"[a-c]x", "dx", false},
		{"a[", "a[", true},
		{`a\*b`, "a*b", true},
		{`a\*b`, "axb", false},
		{"*.txt", "notes.txt", true},
		{"*.txt", "notes.txt.bak", false},
	} {
		if got := globMatch(c.pattern, c.name); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
