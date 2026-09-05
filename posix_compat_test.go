// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepUsesPOSIXBREByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("a|1|x\nd|2|y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdGrep, []string{"^d|", path}, "")
	if status != 0 || stdout != "d|2|y\n" || stderr != "" {
		t.Fatalf("grep BRE = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdGrep, []string{"-v", "^d|", path}, "")
	if status != 0 || stdout != "a|1|x\n" || stderr != "" {
		t.Fatalf("grep -v BRE = (%d, %q, %q)", status, stdout, stderr)
	}

	status, stdout, stderr = captureApplet(t, cmdGrep, []string{"a+"}, "aaa\na+b\n")
	if status != 0 || stdout != "a+b\n" || stderr != "" {
		t.Fatalf("grep literal plus = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdGrep, []string{`a\{2\}`}, "aaa\n")
	if status != 0 || stdout != "aaa\n" || stderr != "" {
		t.Fatalf("grep BRE interval = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdGrep, []string{`^a\+$`}, "aaa\na+b\n")
	if status != 0 || stdout != "aaa\n" || stderr != "" {
		t.Fatalf("grep GNU BRE extension = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdGrep, []string{`^a\?$`}, "a\naa\n")
	if status != 0 || stdout != "a\n" || stderr != "" {
		t.Fatalf("grep GNU BRE optional extension = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdGrep, []string{`x\|y`}, "x\ny\nz\n")
	if status != 0 || stdout != "x\ny\n" || stderr != "" {
		t.Fatalf("grep GNU BRE alternation extension = (%d, %q, %q)", status, stdout, stderr)
	}

	status, stdout, stderr = captureApplet(t, cmdGrep, []string{"-E", "a|b"}, "a\nb\nc\n")
	if status != 0 || stdout != "a\nb\n" || stderr != "" {
		t.Fatalf("grep -E = (%d, %q, %q)", status, stdout, stderr)
	}
}

// TestBREControlCharacterEscapes pins a fix to the shared BRE translator:
// \n and \t used to be quoted down to the literal letters 'n'/'t' (the
// generic "escape whatever follows a backslash" fallback), instead of
// matching an actual newline or tab the way GNU grep/sed treat them. That
// silently broke every multi-line sed idiom built on N;s/\n/.../ .
func TestBREControlCharacterEscapes(t *testing.T) {
	status, stdout, _ := captureApplet(t, cmdGrep, []string{`a\tb`}, "a\tb\naxb\n")
	if status != 0 || stdout != "a\tb\n" {
		t.Fatalf(`grep 'a\tb' = (%d, %q), want to match a real tab, not the letter t`, status, stdout)
	}
	status, stdout, _ = captureApplet(t, cmdSed, []string{`N;s/\n/-/`}, "a\nb\n")
	if status != 0 || stdout != "a-b\n" {
		t.Fatalf(`sed N;s/\n/-/ = (%d, %q), want "a-b\n"`, status, stdout)
	}
}

func TestSedUsesPOSIXBREByDefault(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdSed, []string{`s/^[^|]*|[^|]*|//`}, "a|1|x\n")
	if status != 0 || stdout != "x\n" || stderr != "" {
		t.Fatalf("sed BRE substitution = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"-n", `/^d|/p`}, "a|1|x\nd|2|y\n")
	if status != 0 || stdout != "d|2|y\n" || stderr != "" {
		t.Fatalf("sed BRE address = (%d, %q, %q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"-E", `s/(a+)/x/`}, "aaa\n")
	if status != 0 || stdout != "x\n" || stderr != "" {
		t.Fatalf("sed ERE substitution = (%d, %q, %q)", status, stdout, stderr)
	}
}

func TestRegexBackreferencesFailClearly(t *testing.T) {
	for _, test := range []struct {
		name string
		fn   applet
		args []string
	}{
		{"grep BRE", cmdGrep, []string{`\(x\)\1`}},
		{"sed BRE", cmdSed, []string{`s/\(x\)\1/y/`}},
		{"awk ERE", cmdAwk, []string{`/(x)\1/ { print }`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, _, stderr := captureApplet(t, test.fn, test.args, "xx\n")
			if status == 0 || !strings.Contains(stderr, "backreferences") || !strings.Contains(stderr, "RE2") {
				t.Fatalf("result = (%d, %q)", status, stderr)
			}
		})
	}
}

func TestAwkSingleCharacterFSIsLiteral(t *testing.T) {
	for _, test := range []struct {
		name, separator, input, program, want string
	}{
		{"pipe option", "|", "a|1|x\n", `{print NF}`, "3\n"},
		{"comma option", ",", "a,1,x\n", `{print NF}`, "3\n"},
		{"dot option", ".", "a.1.x\n", `{print NF}`, "3\n"},
		{"tab escape", `\t`, "a\t1\tx\n", `{print NF}`, "3\n"},
		{"multi-character ERE", "[,;]", "a,1;x\n", `{print NF}`, "3\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, stdout, stderr := captureApplet(t, cmdAwk, []string{"-F" + test.separator, test.program}, test.input)
			if status != 0 || stdout != test.want || stderr != "" {
				t.Fatalf("awk -F%q = (%d, %q, %q)", test.separator, status, stdout, stderr)
			}
		})
	}
	status, stdout, stderr := captureApplet(t, cmdAwk, []string{`BEGIN{FS="|"}{print $2}`}, "a|1|x\n")
	if status != 0 || stdout != "1\n" || stderr != "" {
		t.Fatalf("awk BEGIN FS = (%d, %q, %q)", status, stdout, stderr)
	}
}

func TestTarReextractReplacesSymlinkAndKeepOldFilesRetainsIt(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	archive := filepath.Join(root, "bundle.tar.gz")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "real"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdTar, []string{"-czf", archive, "-C", source, "link"}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("tar create = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdTar, []string{"-xzf", archive, "-C", destination}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("first tar extract = (%d, %q)", status, stderr)
	}
	link := filepath.Join(destination, "link")
	if target, err := os.Readlink(link); err != nil || target != "real" {
		t.Fatalf("first link target = %q, %v", target, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("stale", link); err != nil {
		t.Fatal(err)
	}
	status, _, stderr = captureApplet(t, cmdTar, []string{"-xzf", archive, "-C", destination}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("re-extract = (%d, %q)", status, stderr)
	}
	if target, err := os.Readlink(link); err != nil || target != "real" {
		t.Fatalf("replaced link target = %q, %v", target, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("stale", link); err != nil {
		t.Fatal(err)
	}
	// -k reports the clash the way the original does — per member, with the
	// walk carrying on and the run exiting 2 — and leaves the link alone.
	status, _, stderr = captureApplet(t, cmdTar, []string{"-xzkf", archive, "-C", destination}, "")
	if status != 2 || !strings.Contains(stderr, "Cannot create symlink to 'real': File exists") {
		t.Fatalf("tar -k = (%d, %q)", status, stderr)
	}
	if target, err := os.Readlink(link); err != nil || target != "stale" {
		t.Fatalf("tar -k changed link to %q, %v", target, err)
	}
}

func TestLnNoDereferenceReplacesSymlinkToDirectory(t *testing.T) {
	directory := t.TempDir()
	targetDirectory := filepath.Join(directory, "target-directory")
	if err := os.Mkdir(targetDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		link string
	}{
		{"short", []string{"-sfn", "foo"}, filepath.Join(directory, "short")},
		{"long", []string{"-sf", "--no-dereference", "foo"}, filepath.Join(directory, "long")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.Symlink(targetDirectory, test.link); err != nil {
				t.Fatal(err)
			}
			args := append(append([]string{}, test.args...), test.link)
			status, _, stderr := captureApplet(t, cmdLn, args, "")
			if status != 0 || stderr != "" {
				t.Fatalf("ln = (%d, %q)", status, stderr)
			}
			if target, err := os.Readlink(test.link); err != nil || target != "foo" {
				t.Fatalf("link target = %q, %v", target, err)
			}
		})
	}
}

func TestCpRemoveDestinationUsesANewInode(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "destination")
	peer := filepath.Join(directory, "peer")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(destination, peer); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdCp, []string{"--remove-destination", source, destination}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("cp --remove-destination = (%d, %q)", status, stderr)
	}
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "new" {
		t.Fatalf("destination = %q, %v", contents, err)
	}
	if contents, err := os.ReadFile(peer); err != nil || string(contents) != "old" {
		t.Fatalf("hard-linked peer = %q, %v", contents, err)
	}
	destinationInfo, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	peerInfo, err := os.Stat(peer)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(destinationInfo, peerInfo) {
		t.Fatal("--remove-destination retained the destination inode")
	}
}
