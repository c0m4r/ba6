package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	moved, err := moveOne(path, filepath.Join(dir, ".", "same"), true, false)
	if err != nil {
		t.Fatalf("moveOne returned an error: %v", err)
	}
	if moved {
		t.Fatal("same-file move was reported as performed")
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
	if err := c.copyPath(src, dst); err == nil {
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
	c := &copier{interactive: true, input: bufio.NewReader(strings.NewReader("n\n"))}
	if err := c.copyFile(src, dst, info); err != nil {
		t.Fatal(err)
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
	status, _, stderr := captureApplet(t, cmdMv, []string{"-i", src, dst}, "n\n")
	if status != 0 || !strings.Contains(stderr, "overwrite") {
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
