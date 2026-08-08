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

func TestUnzstdRejectsCompressedBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compressed.zst")
	// A valid frame header followed by a final compressed-block header. The
	// focused decoder intentionally accepts only raw and RLE block types.
	stream := append(append([]byte{}, zstdFrameMagic...), 0, 0x38, 0x05, 0, 0)
	if err := os.WriteFile(path, stream, 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdUnzstd, []string{"-c", path}, "")
	if status == 0 || !strings.Contains(stderr, "compressed Zstandard blocks are unsupported") {
		t.Fatalf("unzstd result = (%d, %q)", status, stderr)
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
