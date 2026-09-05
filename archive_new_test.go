// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	archivezip "archive/zip"
	"crypto/sha512"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZipRoundTripAndTraversalProtection(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "bundle.zip")
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "data"), []byte("zip data"), 0o600); err != nil {
		t.Fatal(err)
	}
	withWorkingDirectory(t, root, func() {
		if status := cmdZip([]string{"-r", archive, "source"}); status != 0 {
			t.Fatalf("zip returned %d", status)
		}
	})
	stored := filepath.Join(root, "stored.zip")
	withWorkingDirectory(t, root, func() {
		if status := cmdZip([]string{"-0", stored, "source/nested/data"}); status != 0 {
			t.Fatalf("stored zip returned %d", status)
		}
	})
	storedReader, err := archivezip.OpenReader(stored)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedReader.File) != 1 || storedReader.File[0].Method != archivezip.Store {
		storedReader.Close()
		t.Fatalf("-0 ZIP method = %+v", storedReader.File)
	}
	if err := storedReader.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "destination")
	if status := cmdUnzip([]string{"-d", destination, archive}); status != 0 {
		t.Fatalf("unzip returned %d", status)
	}
	data, err := os.ReadFile(filepath.Join(destination, "source", "nested", "data"))
	if err != nil || string(data) != "zip data" {
		t.Fatalf("zip extraction = %q, %v", data, err)
	}

	malicious := filepath.Join(root, "malicious.zip")
	file, err := os.Create(malicious)
	if err != nil {
		t.Fatal(err)
	}
	writer := archivezip.NewWriter(file)
	member, err := writer.Create("../escape")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if status := cmdUnzip([]string{"-d", destination, malicious}); status == 0 {
		t.Fatal("unzip accepted a traversal member")
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatalf("traversal created %v", err)
	}
}

func TestCpioRoundTripAndTraversalProtection(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("cpio data"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "bundle.cpio")
	withWorkingDirectory(t, root, func() {
		status, _, stderr := captureApplet(t, cmdCpio, []string{"-o", "-F", archive}, "source\nsource/data\n")
		if status != 0 || stderr != "" {
			t.Fatalf("cpio create = (%d, %q)", status, stderr)
		}
	})
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	withWorkingDirectory(t, destination, func() {
		if status := cmdCpio([]string{"-i", "-F", archive}); status != 0 {
			t.Fatalf("cpio extract returned %d", status)
		}
	})
	data, err := os.ReadFile(filepath.Join(destination, "source", "data"))
	if err != nil || string(data) != "cpio data" {
		t.Fatalf("cpio extraction = %q, %v", data, err)
	}

	malicious := filepath.Join(root, "malicious.cpio")
	file, err := os.Create(malicious)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCpioHeader(file, cpioHeader{mode: cpioModeRegular | 0o600, size: 1, name: "../escape"}); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writeCpioPadding(file, 1); err != nil {
		t.Fatal(err)
	}
	if err := writeCpioHeader(file, cpioHeader{name: "TRAILER!!!"}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	withWorkingDirectory(t, destination, func() {
		if status := cmdCpio([]string{"-i", "-F", malicious}); status == 0 {
			t.Fatal("cpio accepted a traversal member")
		}
	})
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatalf("traversal created %v", err)
	}
}

func TestExtraCodecAppletsRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name       string
		compress   applet
		decompress applet
		suffix     string
	}{
		{name: "bzip2", compress: cmdBzip2, decompress: cmdBunzip2, suffix: ".bz2"},
		{name: "xz", compress: cmdXz, decompress: cmdUnxz, suffix: ".xz"},
		{name: "zstd", compress: cmdZstd, decompress: cmdUnzstd, suffix: ".zst"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data")
			if err := os.WriteFile(path, []byte(strings.Repeat("round trip\n", 100)), 0o600); err != nil {
				t.Fatal(err)
			}
			if status := test.compress([]string{"-k", path}); status != 0 {
				t.Fatalf("compress returned %d", status)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if status := test.decompress([]string{"-k", path + test.suffix}); status != 0 {
				t.Fatalf("decompress returned %d", status)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != strings.Repeat("round trip\n", 100) {
				t.Fatalf("round trip = %q, %v", data, err)
			}
		})
	}
}

// A compressed block whose body is nonsense must be rejected rather than
// silently producing garbage: the entropy stages are now implemented, so this
// pins the failure path rather than the old "unsupported" refusal.
func TestUnzstdRejectsMalformedCompressedBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compressed.zst")
	stream := append(append([]byte{}, zstdFrameMagic...), 0, 0x38, 0x05, 0, 0)
	if err := os.WriteFile(path, stream, 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, _ := captureApplet(t, cmdUnzstd, []string{"-c", path}, "")
	if status == 0 || stdout != "" {
		t.Fatalf("unzstd accepted a malformed compressed block: (%d, %q)", status, stdout)
	}
}

func TestExtraChecksumApplets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		command applet
		want    string
	}{
		{name: "md5", command: cmdMd5sum, want: "900150983cd24fb0d6963f7d28e17f72"},
		{name: "sha1", command: cmdSha1sum, want: "a9993e364706816aba3e25717850c26c9cd0d89d"},
		{name: "sha512", command: cmdSha512sum, want: fmt.Sprintf("%x", sha512.Sum512([]byte("abc")))},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, stdout, stderr := captureApplet(t, test.command, []string{path}, "")
			if status != 0 || stderr != "" || stdout != test.want+"  "+path+"\n" {
				t.Fatalf("checksum = (%d, %q, %q)", status, stdout, stderr)
			}
		})
	}
}

func withWorkingDirectory(t *testing.T, directory string, fn func()) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
	}()
	fn()
}

// TestUnzipListTestAndExtractOptions covers what unzip grew past -l and -d:
// the two listing tables, -t, the member and -x patterns, -j, -p/-c and the
// exit statuses Info-ZIP reserves.
func TestUnzipListTestAndExtractOptions(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "t.zip")
	source := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(source, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"a.txt": "hello\n", "sub/b.txt": "world\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
	if status := cmdZip([]string{"-qr", archive, "src"}); status != 0 {
		t.Fatalf("zip -qr = %d", status)
	}

	// -l is the short table, -v the wide one, and both end with a count.
	status, out, errOut := captureApplet(t, cmdUnzip, []string{"-l", archive}, "")
	if status != 0 {
		t.Fatalf("unzip -l = (%d, %q)", status, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 9 || lines[1] != "  Length      Date    Time    Name" ||
		!strings.HasSuffix(lines[8], "4 files") {
		t.Fatalf("unzip -l printed %q", out)
	}
	if _, out, _ = captureApplet(t, cmdUnzip, []string{"-v", archive}, ""); !strings.Contains(out, "CRC-32") ||
		!strings.Contains(out, "Stored") {
		t.Fatalf("unzip -v printed %q", out)
	}

	// -t reads every member and says so.
	status, out, _ = captureApplet(t, cmdUnzip, []string{"-t", archive}, "")
	if status != 0 || !strings.Contains(out, "    testing: src/a.txt                OK") ||
		!strings.HasSuffix(out, "No errors detected in compressed data of "+archive+".\n") {
		t.Fatalf("unzip -t = (%d, %q)", status, out)
	}

	// A pattern selects members, and one that matches nothing is the original's
	// caution and its status 11.
	_, out, _ = captureApplet(t, cmdUnzip, []string{"-l", archive, "src/a*"}, "")
	if !strings.Contains(out, "src/a.txt") || strings.Contains(out, "b.txt") {
		t.Fatalf("unzip -l with a pattern = %q", out)
	}
	status, out, _ = captureApplet(t, cmdUnzip, []string{archive, "nomatch*"}, "")
	if status != 11 || !strings.Contains(out, "caution: filename not matched:  nomatch*") {
		t.Fatalf("unzip on an unmatched pattern = (%d, %q)", status, out)
	}
	// An archive that will not open is status 9, with all three spellings named.
	status, _, errOut = captureApplet(t, cmdUnzip, []string{filepath.Join(dir, "absent.zip")}, "")
	if status != 9 || !strings.Contains(errOut, "cannot find or open") {
		t.Fatalf("unzip on a missing archive = (%d, %q)", status, errOut)
	}

	// -p writes the contents alone; -c names each member first.
	if _, out, _ = captureApplet(t, cmdUnzip, []string{"-p", archive, "src/a.txt"}, ""); out != "hello\n" {
		t.Fatalf("unzip -p = %q", out)
	}
	if _, out, _ = captureApplet(t, cmdUnzip, []string{"-c", archive, "src/a.txt"}, ""); !strings.Contains(out, "extracting: src/a.txt") ||
		!strings.Contains(out, "hello\n") {
		t.Fatalf("unzip -c = %q", out)
	}

	// -j drops the stored directories, and -x leaves members out.
	junked := filepath.Join(dir, "junked")
	if status, _, errOut = captureApplet(t, cmdUnzip, []string{"-q", "-j", "-d", junked, archive}, ""); status != 0 {
		t.Fatalf("unzip -j = (%d, %q)", status, errOut)
	}
	if _, err := os.Stat(filepath.Join(junked, "b.txt")); err != nil {
		t.Fatalf("unzip -j did not flatten the paths: %v", err)
	}
	partial := filepath.Join(dir, "partial")
	if status, _, errOut = captureApplet(t, cmdUnzip, []string{"-q", "-d", partial, archive, "-x", "src/sub/*"}, ""); status != 0 {
		t.Fatalf("unzip -x = (%d, %q)", status, errOut)
	}
	if _, err := os.Stat(filepath.Join(partial, "src", "sub", "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("unzip -x kept the excluded member: %v", err)
	}

	// Without -o an existing file is kept; with it the member is written again.
	kept := filepath.Join(dir, "kept")
	if status := cmdUnzip([]string{"-q", "-d", kept, archive}); status != 0 {
		t.Fatalf("unzip = %d", status)
	}
	target := filepath.Join(kept, "src", "a.txt")
	if err := os.WriteFile(target, []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := cmdUnzip([]string{"-q", "-d", kept, archive}); status != 0 {
		t.Fatalf("unzip over an existing file = %d", status)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "mine\n" {
		t.Fatalf("the existing file was replaced: %q %v", body, err)
	}
	if status := cmdUnzip([]string{"-q", "-o", "-d", kept, archive}); status != 0 {
		t.Fatalf("unzip -o = %d", status)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "hello\n" {
		t.Fatalf("unzip -o did not overwrite: %q %v", body, err)
	}
}

// TestZipStoresWhatDeflateCannotShrink pins the method choice, which is what
// keeps a tiny or already-compressed member from growing.
func TestZipStoresWhatDeflateCannotShrink(t *testing.T) {
	dir := t.TempDir()
	compressible := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(compressible, []byte(strings.Repeat("hello world ", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	tiny := filepath.Join(dir, "tiny.txt")
	if err := os.WriteFile(tiny, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A name the original never tries to deflate.
	precompressed := filepath.Join(dir, "already.zip")
	if err := os.WriteFile(precompressed, []byte(strings.Repeat("hello world ", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "out.zip")
	status, out, errOut := captureApplet(t, cmdZip, []string{archive, compressible, tiny, precompressed}, "")
	if status != 0 {
		t.Fatalf("zip = (%d, %q)", status, errOut)
	}
	for _, want := range []string{"(deflated ", "tiny.txt (stored 0%)", "already.zip (stored 0%)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("zip printed %q, want %q in it", out, want)
		}
	}
	// The archive reads back with the methods that were chosen.
	_, listing, _ := captureApplet(t, cmdUnzip, []string{"-v", archive}, "")
	if !strings.Contains(listing, "Defl:N") || strings.Count(listing, "Stored") != 2 {
		t.Fatalf("unzip -v of the new archive = %q", listing)
	}
}
