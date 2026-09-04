// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRootProtection(t *testing.T) {
	if !isRootPath("/") || !isRootPath("/./") {
		t.Fatal("filesystem root was not recognized")
	}
	if isRootPath(t.TempDir()) {
		t.Fatal("temporary directory was incorrectly recognized as root")
	}
}

func TestMoveForceSameFileDoesNotDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	// mv -f onto the same file is refused by name, as in the original, and the
	// file is left alone rather than removed on the way to a rename.
	status, out, errOut := captureApplet(t, cmdMv, []string{"-f", path, filepath.Join(dir, ".", "same")}, "")
	if status != 1 || out != "" || !strings.Contains(errOut, "are the same file") {
		t.Fatalf("mv -f onto itself = (%d, %q, %q)", status, out, errOut)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("source was removed: %v", err)
	}
	if string(got) != "keep me" {
		t.Fatalf("source content changed to %q", got)
	}
}

func TestCopyRejectsDestinationInsideSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(src, 0o750); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(src, "sub")
	c := &copier{recursive: true}
	if err := c.copyPath(src, dst, true); err == nil {
		t.Fatal("copying a directory into itself was accepted")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination was created before rejection: %v", err)
	}
}

func TestCopyInteractiveDeclinePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	dst := filepath.Join(dir, "destination")
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	// A declined prompt leaves the destination alone and, as in coreutils,
	// counts as a failure rather than a silent skip.
	c := &copier{interactive: true, input: bufio.NewReader(strings.NewReader("n\n"))}
	if err := c.copyFile(src, dst, info); !errors.Is(err, errCopyDeclined) {
		t.Fatalf("declined copy returned %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("declined overwrite changed destination to %q", got)
	}
}

func TestMoveInteractiveDeclinePreservesBothFiles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	dst := filepath.Join(dir, "destination")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	// coreutils treats a declined prompt as a failure: nothing is moved and the
	// status is 1, with no diagnostic beyond the prompt itself.
	status, _, stderr := captureApplet(t, cmdMv, []string{"-i", src, dst}, "n\n")
	if status != 1 || !strings.Contains(stderr, "overwrite") {
		t.Fatalf("status=%d stderr=%q", status, stderr)
	}
	for path, want := range map[string]string{src: "source", dst: "destination"} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s changed to %q", path, got)
		}
	}
}

func TestUniqRejectsSameInputAndOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(path, []byte("one\none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, _ := captureApplet(t, cmdUniq, []string{path, path}, "")
	if status == 0 {
		t.Fatal("same input and output were accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\none\n" {
		t.Fatalf("input was modified: %q", got)
	}
}

func TestMkdirParentsModeDoesNotChangeExistingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if status := cmdMkdir([]string{"-p", "-m", "700", path}); status != 0 {
		t.Fatalf("mkdir returned %d", status)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("existing mode changed to %o", got)
	}
}

func TestMkdirPreservesSpecialModeBits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sticky")
	if status := cmdMkdir([]string{"-m", "1777", path}); status != 0 {
		t.Fatalf("mkdir returned %d", status)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky == 0 || info.Mode().Perm() != 0o777 {
		t.Fatalf("mkdir -m 1777 created mode %v", info.Mode())
	}
}

func TestChmodRecursiveRepairsUnreadableDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child")
	if err := os.WriteFile(child, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o300); err != nil { //nolint:gosec // Test fixture intentionally removes read access.
		t.Fatal(err)
	}
	if status := cmdChmod([]string{"-R", "755", root}); status != 0 {
		t.Fatalf("chmod returned %d", status)
	}
	for _, path := range []string{root, child} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("%s has mode %v", path, info.Mode())
		}
	}
}

func captureApplet(t *testing.T, fn applet, args []string, input string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	inPath := filepath.Join(dir, "stdin")
	outPath := filepath.Join(dir, "stdout")
	errPath := filepath.Join(dir, "stderr")
	if err := os.WriteFile(inPath, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(inPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := os.Create(errPath)
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, out, errOut
	status := fn(args)
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	if closeErr := in.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if closeErr := out.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if closeErr := errOut.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	stdout, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.ReadFile(errPath)
	if err != nil {
		t.Fatal(err)
	}
	return status, string(stdout), string(stderr)
}

// TestBackupNaming pins the naming scheme the coreutils tools share, since ln,
// mv and cp all read it from the same options and environment variables.
func TestBackupNaming(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := backupName(path, backupNone, "~"); got != "" {
		t.Fatalf("backupNone named %q", got)
	}
	if got := backupName(filepath.Join(dir, "absent"), backupSimple, "~"); got != "" {
		t.Fatalf("a missing file was given the backup name %q", got)
	}
	if got := backupName(path, backupSimple, ".bak"); got != path+".bak" {
		t.Fatalf("simple backup = %q", got)
	}
	// "existing" is simple until a numbered backup exists, and numbered after.
	if got := backupName(path, backupExisting, "~"); got != path+"~" {
		t.Fatalf("existing backup with none present = %q", got)
	}
	if err := os.WriteFile(path+".~3~", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := backupName(path, backupExisting, "~"); got != path+".~4~" {
		t.Fatalf("existing backup beside .~3~ = %q", got)
	}
	if got := backupName(path, backupNumbered, "~"); got != path+".~4~" {
		t.Fatalf("numbered backup = %q", got)
	}
	// Every control name, and the unambiguous abbreviations the originals take.
	for name, want := range map[string]backupMethod{
		"none": backupNone, "off": backupNone, "simple": backupSimple,
		"never": backupSimple, "existing": backupExisting, "nil": backupExisting,
		"numbered": backupNumbered, "t": backupNumbered, "nu": backupNumbered,
		"si": backupSimple, "e": backupExisting,
	} {
		if got, ok := parseBackupControl(name); !ok || got != want {
			t.Fatalf("parseBackupControl(%q) = (%v, %v), want %v", name, got, ok, want)
		}
	}
	for _, name := range []string{"", "zzz", "n"} {
		if _, ok := parseBackupControl(name); ok {
			t.Fatalf("parseBackupControl(%q) was accepted", name)
		}
	}
}

// TestLnOptions covers the options ln grew past -s/-f/-n/-T/-v.
func TestLnOptions(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	source := write("source")

	// -b renames the destination out of the way instead of removing it.
	destination := write("dest")
	if status, _, errOut := captureApplet(t, cmdLn, []string{"-b", source, destination}, ""); status != 0 {
		t.Fatalf("ln -b = (%d, %q)", status, errOut)
	}
	if body, err := os.ReadFile(destination + "~"); err != nil || string(body) != "dest" {
		t.Fatalf("backup = (%q, %v)", body, err)
	}

	// -r writes the link body as a path from the link's own directory.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if status, _, errOut := captureApplet(t, cmdLn, []string{"-sr", source, filepath.Join(sub, "rel")}, ""); status != 0 {
		t.Fatalf("ln -sr = (%d, %q)", status, errOut)
	}
	if target, err := os.Readlink(filepath.Join(sub, "rel")); err != nil || target != "../source" {
		t.Fatalf("ln -sr wrote %q (%v)", target, err)
	}

	// -t puts every link in one directory, and -v names each as it is made.
	status, out, errOut := captureApplet(t, cmdLn, []string{"-v", "-t", sub, source, write("second")}, "")
	if status != 0 || !strings.Contains(out, "=>") {
		t.Fatalf("ln -v -t = (%d, %q, %q), want the hard-link arrow", status, out, errOut)
	}
	for _, name := range []string{"source", "second"} {
		if _, err := os.Lstat(filepath.Join(sub, name)); err != nil {
			t.Fatalf("ln -t did not create %s: %v", name, err)
		}
	}
	// A symbolic link is announced with the other arrow.
	if _, out, _ = captureApplet(t, cmdLn, []string{"-sv", source, filepath.Join(dir, "sym")}, ""); !strings.Contains(out, "->") {
		t.Fatalf("ln -sv printed %q", out)
	}

	// A hard link to a directory is refused by name, and -d asks the kernel.
	if status, _, errOut = captureApplet(t, cmdLn, []string{sub, filepath.Join(dir, "hard")}, ""); status != 1 ||
		!strings.Contains(errOut, "hard link not allowed for directory") {
		t.Fatalf("ln on a directory = (%d, %q)", status, errOut)
	}
	// An existing destination without -f is the original's EEXIST wording, which
	// names the destination alone.
	other := write("other")
	status, _, errOut = captureApplet(t, cmdLn, []string{source, other}, "")
	if status != 1 || !strings.Contains(errOut, "failed to create hard link '"+other+"'") {
		t.Fatalf("ln onto an existing file = (%d, %q)", status, errOut)
	}
	// A declined -i prompt fails, as it does in coreutils, and changes nothing.
	status, _, errOut = captureApplet(t, cmdLn, []string{"-i", source, other}, "n\n")
	if status != 1 || !strings.Contains(errOut, "replace") {
		t.Fatalf("ln -i declined = (%d, %q)", status, errOut)
	}
}

// TestMvOptions covers the options mv grew past -f/-i/-n/-v.
func TestMvOptions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// -b keeps the destination under its backup name, and -v says so.
	source, destination := write("a", "a"), write("b", "b")
	status, out, errOut := captureApplet(t, cmdMv, []string{"-vb", source, destination}, "")
	if status != 0 || out != "renamed '"+source+"' -> '"+destination+"' (backup: '"+destination+"~')\n" {
		t.Fatalf("mv -vb = (%d, %q, %q)", status, out, errOut)
	}

	// --backup=numbered counts up rather than overwriting one backup.
	for _, want := range []string{".~1~", ".~2~"} {
		source = write("a", "a")
		if status, _, errOut = captureApplet(t, cmdMv, []string{"--backup=numbered", source, destination}, ""); status != 0 {
			t.Fatalf("mv --backup=numbered = (%d, %q)", status, errOut)
		}
		if _, err := os.Lstat(destination + want); err != nil {
			t.Fatalf("numbered backup %s missing: %v", want, err)
		}
	}

	// -u keeps a destination that is not older than the source.
	older, newer := write("older", "older"), write("newer", "newer")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}
	if status, _, _ = captureApplet(t, cmdMv, []string{"-u", older, newer}, ""); status != 0 {
		t.Fatalf("mv -u with an older source = %d", status)
	}
	if body, err := os.ReadFile(newer); err != nil || string(body) != "newer" {
		t.Fatalf("mv -u overwrote the newer file: %q %v", body, err)
	}
	if status, _, errOut = captureApplet(t, cmdMv, []string{"-u", newer, older}, ""); status != 0 {
		t.Fatalf("mv -u with a newer source = (%d, %q)", status, errOut)
	}
	if body, err := os.ReadFile(older); err != nil || string(body) != "newer" {
		t.Fatalf("mv -u did not move the newer file: %q %v", body, err)
	}

	// -t moves into a directory named up front; -T refuses to descend into one.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	one, two := write("one", "one"), write("two", "two")
	if status, _, errOut = captureApplet(t, cmdMv, []string{"-t", sub, one, two}, ""); status != 0 {
		t.Fatalf("mv -t = (%d, %q)", status, errOut)
	}
	for _, name := range []string{"one", "two"} {
		if _, err := os.Lstat(filepath.Join(sub, name)); err != nil {
			t.Fatalf("mv -t did not move %s: %v", name, err)
		}
	}
	plain := write("plain", "plain")
	status, _, errOut = captureApplet(t, cmdMv, []string{"-T", plain, sub}, "")
	if status != 1 || !strings.Contains(errOut, "cannot overwrite directory") {
		t.Fatalf("mv -T over a directory = (%d, %q)", status, errOut)
	}

	// A directory replaces an empty directory, which the Go library's own
	// rename refuses outright.
	from, onto := filepath.Join(dir, "from"), filepath.Join(dir, "onto")
	if err := os.Mkdir(from, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(onto, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "inner"), []byte("inner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, _, errOut = captureApplet(t, cmdMv, []string{"-T", from, onto}, ""); status != 0 {
		t.Fatalf("mv -T of a directory onto an empty one = (%d, %q)", status, errOut)
	}
	if _, err := os.Lstat(filepath.Join(onto, "inner")); err != nil {
		t.Fatalf("the directory was not moved: %v", err)
	}
	// A non-empty destination directory is reported against the destination.
	again := filepath.Join(dir, "again")
	if err := os.Mkdir(again, 0o750); err != nil {
		t.Fatal(err)
	}
	status, _, errOut = captureApplet(t, cmdMv, []string{"-T", again, onto}, "")
	if status != 1 || !strings.Contains(errOut, "cannot overwrite '"+onto+"'") {
		t.Fatalf("mv -T onto a full directory = (%d, %q)", status, errOut)
	}
	// Moving a directory inside itself is refused by name.
	status, _, errOut = captureApplet(t, cmdMv, []string{again, filepath.Join(again, "inner")}, "")
	if status != 1 || !strings.Contains(errOut, "subdirectory of itself") {
		t.Fatalf("mv into itself = (%d, %q)", status, errOut)
	}

	// The operand diagnostics carry the Try line, as coreutils' do.
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "missing file operand"},
		{[]string{"x"}, "missing destination file operand after 'x'"},
		{[]string{"--backup=zzz", "a", "b"}, "invalid argument 'zzz'"},
		{[]string{"-W", "a", "b"}, "invalid option -- 'W'"},
	} {
		status, out, errOut := captureApplet(t, cmdMv, c.args, "")
		if status != 1 || out != "" || !strings.Contains(errOut, c.want) {
			t.Fatalf("mv %v = (%d, %q, %q), want %q", c.args, status, out, errOut, c.want)
		}
	}
}

// TestCpOptions covers the options cp grew past -r/-p/-f/-i/-v: the link
// modes, the symlink-following rules, --parents, -x and the backup machinery.
func TestCpOptions(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(source, "inner"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "f"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "inner", "g"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("f", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, args ...string) (int, string) {
		t.Helper()
		status, out, errOut := captureApplet(t, cmdCp, args, "")
		if status != 0 {
			t.Fatalf("cp %v = (%d, %q)", args, status, errOut)
		}
		return status, out
	}

	// A recursive copy keeps symbolic links as links; -L follows them.
	kept := filepath.Join(dir, "kept")
	run(t, "-r", source, kept)
	if info, err := os.Lstat(filepath.Join(kept, "link")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("cp -r did not keep the symlink: %v", err)
	}
	followed := filepath.Join(dir, "followed")
	run(t, "-rL", source, followed)
	if info, err := os.Lstat(filepath.Join(followed, "link")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("cp -rL did not follow the symlink: %v", err)
	}
	// A plain copy follows them, since the no-dereference default only comes
	// with -r.
	plain := filepath.Join(dir, "plain")
	run(t, filepath.Join(source, "link"), plain)
	if info, err := os.Lstat(plain); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("a plain cp did not follow the symlink: %v", err)
	}

	// -v announces a directory before its contents, in inode order.
	_, out := run(t, "-rv", source, filepath.Join(dir, "verbose"))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 5 || !strings.HasPrefix(lines[0], "'"+source+"' -> ") {
		t.Fatalf("cp -rv printed %q", out)
	}

	// -l hard-links, and follows a symbolic source because -r's
	// no-dereference default does not apply to a linking copy.
	linked := filepath.Join(dir, "linked")
	run(t, "-rl", source, linked)
	original, err := os.Stat(filepath.Join(source, "f"))
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.Stat(filepath.Join(linked, "f"))
	if err != nil || !os.SameFile(original, copied) {
		t.Fatalf("cp -rl did not hard-link: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(linked, "link")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("cp -rl kept the symlink as a link: %v", err)
	}

	// --parents rebuilds the source's own path under the destination.
	parents := filepath.Join(dir, "parents")
	if err := os.Mkdir(parents, 0o750); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("sub", "deep")
	if err := os.MkdirAll(filepath.Join(dir, relative), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, relative, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The operand is relative to the working directory, so run it from there.
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	}()
	run(t, "--parents", filepath.Join(relative, "file"), "parents")
	if _, err := os.Stat(filepath.Join(parents, relative, "file")); err != nil {
		t.Fatalf("cp --parents did not rebuild the path: %v", err)
	}

	// -s refuses a relative source when the link would land in another
	// directory, since the link body would then point at nothing.
	status, _, errOut := captureApplet(t, cmdCp, []string{"-rs", "src", "symlinked"}, "")
	if status != 1 || !strings.Contains(errOut, "can make relative symbolic links only in current directory") {
		t.Fatalf("cp -rs with a relative source = (%d, %q)", status, errOut)
	}
	// An absolute source is fine wherever the link lands.
	run(t, "-s", filepath.Join(source, "f"), filepath.Join("parents", "sym"))
	if body, err := os.Readlink(filepath.Join(parents, "sym")); err != nil || body != filepath.Join(source, "f") {
		t.Fatalf("cp -s wrote %q (%v)", body, err)
	}

	// -b keeps the destination under its backup name and -v says so.
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out = run(t, "-vb", filepath.Join(source, "f"), target)
	if !strings.Contains(out, "(backup: '"+target+"~')") {
		t.Fatalf("cp -vb printed %q", out)
	}
	if body, err := os.ReadFile(target + "~"); err != nil || string(body) != "old" {
		t.Fatalf("backup = (%q, %v)", body, err)
	}

	// -n keeps the destination and succeeds; -u keeps a newer one.
	if _, out = run(t, "-n", filepath.Join(source, "f"), target); out != "" {
		t.Fatalf("cp -n printed %q", out)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "hi\n" {
		t.Fatalf("cp -n overwrote the destination: %q %v", body, err)
	}

	// --preserve rejects an unknown attribute with the original's list.
	status, out, errOut = captureApplet(t, cmdCp, []string{"--preserve=nope", filepath.Join(source, "f"), target}, "")
	if status != 1 || out != "" || !strings.Contains(errOut, "Valid arguments are:") ||
		!strings.Contains(errOut, "'timestamps'") {
		t.Fatalf("cp --preserve=nope = (%d, %q, %q)", status, out, errOut)
	}
	// So do --sparse and --reflink.
	for _, option := range []string{"--sparse=bogus", "--reflink=bogus"} {
		if status, _, errOut = captureApplet(t, cmdCp, []string{option, filepath.Join(source, "f"), target}, ""); status != 1 ||
			!strings.Contains(errOut, "Valid arguments are:") {
			t.Fatalf("cp %s = (%d, %q)", option, status, errOut)
		}
	}

	// The operand diagnostics carry the Try line.
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "missing file operand"},
		{[]string{"x"}, "missing destination file operand after 'x'"},
		{[]string{"-W", "a", "b"}, "invalid option -- 'W'"},
		{[]string{source, target}, "-r not specified; omitting directory"},
	} {
		status, _, errOut := captureApplet(t, cmdCp, c.args, "")
		if status != 1 || !strings.Contains(errOut, c.want) {
			t.Fatalf("cp %v = (%d, %q), want %q", c.args, status, errOut, c.want)
		}
	}
}
