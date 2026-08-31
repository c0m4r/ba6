// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	archivetar "archive/tar"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestSha256sumComputeAndCheck(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data")
	if err := os.WriteFile(file, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte("abc")))
	status, stdout, stderr := captureApplet(t, cmdSha256sum, []string{file}, "")
	if status != 0 || stdout != expected+"  "+file+"\n" {
		t.Fatalf("sha256sum=(%d,%q,%q)", status, stdout, stderr)
	}
	list := filepath.Join(dir, "checksums")
	if err := os.WriteFile(list, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr = captureApplet(t, cmdSha256sum, []string{"-c", list}, "")
	if status != 0 || stdout != file+": OK\n" {
		t.Fatalf("sha256sum -c=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestDfAndDu(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "one")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdDf, []string{dir}, "")
	if status != 0 || !strings.Contains(stdout, "Filesystem") || !strings.Contains(stdout, "Mounted on") {
		t.Fatalf("df=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdDu, []string{"-s", dir}, "")
	if status != 0 || !strings.HasSuffix(strings.TrimSpace(stdout), dir) {
		t.Fatalf("du=(%d,%q,%q)", status, stdout, stderr)
	}
	procLink := filepath.Join(dir, "proc-link")
	if err := os.Symlink("/proc", procLink); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr = captureApplet(t, cmdDf, []string{procLink}, "")
	if status != 0 || !strings.HasSuffix(strings.TrimSpace(stdout), "/proc") {
		t.Fatalf("df symlink target=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestFindPredicatesDepthAndSize(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "one.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "two.txt"), []byte("yy"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdFind,
		[]string{dir, "-maxdepth", "1", "-type", "f", "-name", "*.txt", "-size", "1c"}, "")
	if status != 0 || stdout != file+"\n" {
		t.Fatalf("find=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestSedSubstitutionAddressesAndDelete(t *testing.T) {
	// sed defaults to BREs, so grouping uses \( and \) rather than ERE's
	// unescaped parentheses.
	script := `1,2s/\(foo\) \([0-9]\)/\1=x\2/;3d`
	status, stdout, stderr := captureApplet(t, cmdSed, []string{script}, "foo 1\nfoo 2\nbar 3\n")
	if status != 0 || stdout != "foo=x1\nfoo=x2\n" {
		t.Fatalf("sed=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"-n", `/foo/p`}, "foo\nbar\n")
	if status != 0 || stdout != "foo\n" {
		t.Fatalf("sed -n=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"-n", "2,1p"}, "one\ntwo\nthree\n")
	if status != 0 || stdout != "two\n" {
		t.Fatalf("sed numeric reverse range=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"-n", "/x/,/x/p"}, "one\nx\nbetween\nx\nlast\n")
	if status != 0 || stdout != "x\nbetween\nx\n" {
		t.Fatalf("sed regex range=(%d,%q,%q)", status, stdout, stderr)
	}
	// Negated addresses, as used by the iproute2 bash completion to keep only
	// the "TYPE := { ... }" block of ip's help output.
	status, stdout, stderr = captureApplet(t, cmdSed, []string{`/TYPE := /,/}/!d`},
		"Usage: ip link add\nTYPE := { bridge |\n  veth }\ntrailing\n")
	if status != 0 || stdout != "TYPE := { bridge |\n  veth }\n" {
		t.Fatalf("sed negated range=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"-n", `$!p`}, "one\ntwo\n")
	if status != 0 || stdout != "one\n" {
		t.Fatalf("sed negated last line=(%d,%q,%q)", status, stdout, stderr)
	}
	status, _, stderr = captureApplet(t, cmdSed, []string{`/x/!!d`}, "x\n")
	if status == 0 || !strings.Contains(stderr, "multiple") {
		t.Fatalf("sed repeated negation=(%d,%q)", status, stderr)
	}
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	if err := os.WriteFile(first, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr = captureApplet(t, cmdSed, []string{"q", first, filepath.Join(dir, "missing")}, "")
	if status != 0 || stdout != "first\n" {
		t.Fatalf("sed q eagerly opened later input=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestTarGzipRoundTripAndTraversalProtection(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	extracted := filepath.Join(root, "extracted")
	archive := filepath.Join(root, "bundle.tar.gz")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("archive data"), 0o600); err != nil {
		t.Fatal(err)
	}
	insideArchive := filepath.Join(source, "inside.tar")
	status, _, _ := captureApplet(t, cmdTar, []string{"-cf", insideArchive, "-C", source, "."}, "")
	if status == 0 {
		t.Fatal("tar accepted an output archive inside its input directory")
	}
	if _, err := os.Stat(insideArchive); !os.IsNotExist(err) {
		t.Fatalf("rejected archive was created: %v", err)
	}
	if status := cmdTar([]string{"-czf", archive, "-C", source, "."}); status != 0 {
		t.Fatalf("tar create returned %d", status)
	}
	if status := cmdTar([]string{"-xzf", archive, "-C", extracted}); status != 0 {
		t.Fatalf("tar extract returned %d", status)
	}
	contents, err := os.ReadFile(filepath.Join(extracted, "payload"))
	if err != nil || string(contents) != "archive data" {
		t.Fatalf("extracted contents=%q err=%v", contents, err)
	}

	malicious := filepath.Join(root, "malicious.tar")
	file, err := os.Create(malicious)
	if err != nil {
		t.Fatal(err)
	}
	writer := archivetar.NewWriter(file)
	if err := writer.WriteHeader(&archivetar.Header{Name: "../escape", Mode: 0o600, Size: 1, Typeflag: archivetar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	status, _, _ = captureApplet(t, cmdTar, []string{"-xf", malicious, "-C", extracted}, "")
	if status == 0 {
		t.Fatal("tar accepted a path traversal member")
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatalf("path traversal created a file: %v", err)
	}
}

// A symbolic link whose target is lexically inside the archive can still
// escape once it is resolved through a link extracted earlier from the same
// archive: "subdir/parent/.." folds to "subdir" lexically, but reaches the
// parent of the destination when "subdir/parent" is a link to "..".
func TestTarRejectsSymlinkEscapingThroughExtractedLink(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	malicious := filepath.Join(root, "malicious.tar")
	file, err := os.Create(malicious)
	if err != nil {
		t.Fatal(err)
	}
	writer := archivetar.NewWriter(file)
	headers := []*archivetar.Header{
		{Name: "subdir", Mode: 0o755, Typeflag: archivetar.TypeDir},
		{Name: "subdir/parent", Mode: 0o777, Typeflag: archivetar.TypeSymlink, Linkname: ".."},
		{Name: "escape", Mode: 0o777, Typeflag: archivetar.TypeSymlink, Linkname: "subdir/parent/.."},
	}
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdTar, []string{"-xf", malicious, "-C", destination}, "")
	if status == 0 || !strings.Contains(stderr, "escapes destination") {
		t.Fatalf("tar extract = (%d, %q)", status, stderr)
	}
	if _, err := os.Lstat(filepath.Join(destination, "escape")); !os.IsNotExist(err) {
		t.Fatalf("escaping symbolic link was created: %v", err)
	}
}

func TestTarExtractionReplacesExistingHardLink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	archive := filepath.Join(root, "archive.tar")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "member"), []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := cmdTar([]string{"-cf", archive, "-C", source, "member"}); status != 0 {
		t.Fatalf("tar create returned %d", status)
	}
	victim := filepath.Join(root, "victim")
	member := filepath.Join(destination, "member")
	if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, member); err != nil {
		t.Fatal(err)
	}
	if status := cmdTar([]string{"-xf", archive, "-C", destination}); status != 0 {
		t.Fatalf("tar extract returned %d", status)
	}
	victimData, err := os.ReadFile(victim)
	if err != nil || string(victimData) != "victim" {
		t.Fatalf("outside hard link changed to %q: %v", victimData, err)
	}
	memberData, err := os.ReadFile(member)
	if err != nil || string(memberData) != "archive" {
		t.Fatalf("extracted member=%q: %v", memberData, err)
	}
	victimInfo, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	memberInfo, err := os.Stat(member)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(victimInfo, memberInfo) {
		t.Fatal("extraction retained the pre-existing hard link")
	}
}

func TestGzipAndGunzipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input")
	if err := os.WriteFile(input, []byte("compressed data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := cmdGzip([]string{"-k", input}); status != 0 {
		t.Fatalf("gzip returned %d", status)
	}
	if err := os.Remove(input); err != nil {
		t.Fatal(err)
	}
	if status := cmdGunzip([]string{"-k", input + ".gz"}); status != 0 {
		t.Fatalf("gunzip returned %d", status)
	}
	contents, err := os.ReadFile(input)
	if err != nil || string(contents) != "compressed data" {
		t.Fatalf("gunzip contents=%q err=%v", contents, err)
	}
}

func TestSystemAndProcApplets(t *testing.T) {
	status, stdout, stderr := captureApplet(t, cmdUname, []string{"-s"}, "")
	if status != 0 || strings.TrimSpace(stdout) == "" {
		t.Fatalf("uname=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdId, []string{"-u"}, "")
	if status != 0 || strings.TrimSpace(stdout) != strconv.Itoa(os.Getuid()) {
		t.Fatalf("id=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdHostname, nil, "")
	if status != 0 || strings.TrimSpace(stdout) == "" {
		t.Fatalf("hostname=(%d,%q,%q)", status, stdout, stderr)
	}
	status, stdout, stderr = captureApplet(t, cmdWhoami, nil, "")
	if status != 0 || strings.TrimSpace(stdout) == "" {
		t.Fatalf("whoami=(%d,%q,%q)", status, stdout, stderr)
	}
	pid := strconv.Itoa(os.Getpid())
	status, stdout, stderr = captureApplet(t, cmdPs, []string{"-p", pid, "-o", "pid,comm"}, "")
	if status != 0 || !strings.Contains(stdout, pid) {
		t.Fatalf("ps=(%d,%q,%q)", status, stdout, stderr)
	}
	if status := cmdKill([]string{"-0", pid}); status != 0 {
		t.Fatalf("kill -0 returned %d", status)
	}
	status, stdout, stderr = captureApplet(t, cmdFree, []string{"-k"}, "")
	if status != 0 || !strings.Contains(stdout, "Mem:") || !strings.Contains(stdout, "Swap:") {
		t.Fatalf("free=(%d,%q,%q)", status, stdout, stderr)
	}
}

func TestParseNeighborRuleAndExtendedLinkSet(t *testing.T) {
	neighbor, err := parseNeighborSpec(syscall.AF_UNSPEC,
		[]string{"192.0.2.2", "dev", "eth0", "lladdr", "02:00:00:00:00:02", "nud", "reachable"})
	if err != nil || neighbor.family != syscall.AF_INET || neighbor.state != neighborStates["reachable"] {
		t.Fatalf("neighbor=%+v err=%v", neighbor, err)
	}
	rule, err := parseRuleSpec(syscall.AF_UNSPEC,
		[]string{"from", "192.0.2.0/24", "priority", "100", "table", "200"})
	if err != nil || rule.family != syscall.AF_INET || rule.from.prefix != 24 || rule.priority == nil || *rule.priority != 100 || rule.table != 200 {
		t.Fatalf("rule=%+v err=%v", rule, err)
	}
	link, err := parseLinkSet([]string{"dev", "eth0", "mtu", "1400", "address", "02:00:00:00:00:01", "alias", "uplink", "name", "wan0"})
	if err != nil || link.mtu == nil || *link.mtu != 1400 || link.address.String() != "02:00:00:00:00:01" ||
		link.alias == nil || *link.alias != "uplink" || link.rename != "wan0" {
		t.Fatalf("link=%+v err=%v", link, err)
	}
}
