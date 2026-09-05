// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSfdiskWritesValidatedMBR(t *testing.T) {
	image := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(image, make([]byte, 4*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "label: dos\nstart=2048, size=2048, type=83, bootable\nstart=4096, size=1024, type=82\n"
	status, _, stderr := captureApplet(t, cmdSfdisk, []string{image}, script)
	if status != 0 {
		t.Fatalf("sfdisk status=%d stderr=%q", status, stderr)
	}
	sector := make([]byte, 512)
	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.ReadAt(sector, 0)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if sector[510] != 0x55 || sector[511] != 0xaa {
		t.Fatal("missing MBR signature")
	}
	first := sector[446:462]
	if first[0] != 0x80 || first[4] != 0x83 || binary.LittleEndian.Uint32(first[8:12]) != 2048 || binary.LittleEndian.Uint32(first[12:16]) != 2048 {
		t.Fatalf("unexpected first partition: %x", first)
	}
	status, dump, stderr := captureApplet(t, cmdSfdisk, []string{"--dump", image}, "")
	if status != 0 || !strings.Contains(dump, "start=      2048") || !strings.Contains(dump, "type=82") {
		t.Fatalf("dump status=%d out=%q stderr=%q", status, dump, stderr)
	}
}

func TestSfdiskRejectsOverlapWithoutWriting(t *testing.T) {
	image := filepath.Join(t.TempDir(), "disk.img")
	original := make([]byte, 2*1024*1024)
	copy(original[:32], "preserve bootstrap bytes")
	if err := os.WriteFile(image, original, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "start=1,size=100\nstart=50,size=100\n"
	status, _, _ := captureApplet(t, cmdSfdisk, []string{image}, script)
	if status == 0 {
		t.Fatal("overlapping partition table was accepted")
	}
	got, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[:512]) != string(original[:512]) {
		t.Fatal("invalid input changed the disk image")
	}
}

func TestBlockdevReportsRegularFileSize(t *testing.T) {
	image := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(image, make([]byte, 12345), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdBlockdev, []string{"--getsize64", image}, "")
	if status != 0 || stdout != "12345\n" {
		t.Fatalf("blockdev status=%d out=%q stderr=%q", status, stdout, stderr)
	}
}

func TestMkfsExt2FixtureAndFsck(t *testing.T) {
	image := filepath.Join(t.TempDir(), "root.ext2")
	if err := os.WriteFile(image, make([]byte, 8*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdMkfsExt2, []string{"-F", "-L", "rescue", image}, "")
	if status != 0 {
		t.Fatalf("mkfs.ext2 status=%d stderr=%q", status, stderr)
	}
	data, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint16(data[1080:1082]) != 0xef53 || string(data[1144:1150]) != "rescue" {
		t.Fatal("ext2 superblock fields were not written")
	}
	status, stdout, stderr := captureApplet(t, cmdFsck, []string{"-t", "ext2", image}, "")
	if status != 0 || !strings.Contains(stdout, ": clean") {
		t.Fatalf("fsck status=%d out=%q stderr=%q", status, stdout, stderr)
	}
	data[1080] = 0
	if err := os.WriteFile(image, data, 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, _ = captureApplet(t, cmdFsck, []string{image}, "")
	if status != 4 {
		t.Fatalf("damaged filesystem returned status %d, want 4", status)
	}
}

func TestMkfsExt2RequiresForceForRegularFile(t *testing.T) {
	image := filepath.Join(t.TempDir(), "target")
	original := make([]byte, 1024*1024)
	copy(original, "do not overwrite")
	if err := os.WriteFile(image, original, 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, _ := captureApplet(t, cmdMkfsExt2, []string{image}, "")
	if status == 0 {
		t.Fatal("regular file was formatted without -F")
	}
	got, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:len("do not overwrite")], []byte("do not overwrite")) {
		t.Fatal("refused format modified its target")
	}
}

func TestMkswapWritesVersionOneHeader(t *testing.T) {
	image := filepath.Join(t.TempDir(), "swap.img")
	if err := os.WriteFile(image, make([]byte, 1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdMkswap, []string{"-L", "rescue", image}, "")
	if status != 0 {
		t.Fatalf("mkswap status=%d stderr=%q", status, stderr)
	}
	header := make([]byte, 4096)
	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.Read(header)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(header[1024:1028]) != 1 || string(header[4086:]) != "SWAPSPACE2" || string(header[1052:1058]) != "rescue" {
		t.Fatal("invalid swap header")
	}
}

// TestSwapShowTable pins the --show table and the summary form, which are what
// swapon prints when it is only asked to look.
func TestSwapShowTable(t *testing.T) {
	// The kernel's own table is the input, so the shape of each line is what
	// the test asserts.
	status, out, errOut := captureApplet(t, cmdSwapon, []string{"--show"}, "")
	if status != 0 {
		t.Fatalf("swapon --show = (%d, %q)", status, errOut)
	}
	if out != "" {
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if strings.Join(strings.Fields(lines[0]), " ") != "NAME TYPE SIZE USED PRIO" {
			t.Fatalf("swapon --show header = %q", lines[0])
		}
		// --noheadings drops the header, and with it the padding the header
		// width would have forced.
		_, plain, _ := captureApplet(t, cmdSwapon, []string{"--show", "--noheadings"}, "")
		for _, line := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
			if strings.Contains(line, "  ") {
				t.Fatalf("swapon --show --noheadings padded a column: %q", line)
			}
		}
		// --raw is single-spaced too, and --bytes prints counts.
		_, raw, _ := captureApplet(t, cmdSwapon, []string{"--show", "--raw"}, "")
		if strings.Contains(raw, "  ") {
			t.Fatalf("swapon --show --raw = %q", raw)
		}
		_, bytes, _ := captureApplet(t, cmdSwapon, []string{"--show", "--bytes", "--noheadings"}, "")
		for _, line := range strings.Split(strings.TrimRight(bytes, "\n"), "\n") {
			if fields := strings.Fields(line); len(fields) == 5 {
				if _, err := strconv.ParseUint(fields[2], 10, 64); err != nil {
					t.Fatalf("swapon --bytes size = %q", fields[2])
				}
			}
		}
	}
	// -s is /proc/swaps itself.
	_, summary, _ := captureApplet(t, cmdSwapon, []string{"-s"}, "")
	expected, err := os.ReadFile("/proc/swaps")
	if err != nil {
		t.Skip("no /proc/swaps")
	}
	if summary != string(expected) {
		t.Fatalf("swapon -s = %q, want %q", summary, expected)
	}
	// An unknown column is refused by name.
	status, out, errOut = captureApplet(t, cmdSwapon, []string{"--show=BOGUS"}, "")
	if status != 1 || out != "" || !strings.Contains(errOut, "unknown column: BOGUS") {
		t.Fatalf("swapon --show=BOGUS = (%d, %q, %q)", status, out, errOut)
	}
	// A device that is not there is reported before privilege comes into it.
	status, _, errOut = captureApplet(t, cmdSwapon, []string{"/definitely/absent"}, "")
	if status != 255 || !strings.Contains(errOut, "cannot open /definitely/absent") {
		t.Fatalf("swapon on a missing device = (%d, %q)", status, errOut)
	}
	// swapoff with no operand is the original's usage failure, status 16.
	if status, _, errOut = captureApplet(t, cmdSwapoff, nil, ""); status != 16 ||
		!strings.Contains(errOut, "bad usage") {
		t.Fatalf("swapoff with no operand = (%d, %q)", status, errOut)
	}
}

// TestMkswapHumanSize pins util-linux's size wording, which rounds to the
// nearest tenth and drops the decimal when it lands on a whole number. The
// expectations were read off the original.
func TestMkswapHumanSize(t *testing.T) {
	cases := []struct {
		bytes uint64
		want  string
	}{
		{36864, "36 KiB"},
		{98304, "96 KiB"},
		{1044480, "1020 KiB"},
		{1228800, "1.2 MiB"},
		{4993024, "4.8 MiB"},
		{9994240, "9.5 MiB"},
		{10481664, "10 MiB"},
		{33550336, "32 MiB"},
		{999993344, "953.7 MiB"},
		{1073737728, "1024 MiB"},
		{2499993600, "2.3 GiB"},
	}
	for _, tc := range cases {
		if got := mkswapHumanSize(tc.bytes); got != tc.want {
			t.Errorf("mkswapHumanSize(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

func TestMkswapSizeArgument(t *testing.T) {
	cases := []struct {
		text  string
		want  uint64
		valid bool
	}{
		{"4096", 4096, true},
		{"5K", 5120, true},
		{"5KiB", 5120, true},
		{"3MiB", 3 * 1024 * 1024, true},
		{"2G", 2 * 1024 * 1024 * 1024, true},
		{"5KB", 5000, true},
		{"notasize", 0, false},
		{"12Q", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, valid := mkswapSize(tc.text)
		if valid != tc.valid || got != tc.want {
			t.Errorf("mkswapSize(%q) = (%d, %v), want (%d, %v)", tc.text, got, valid, tc.want, tc.valid)
		}
	}
}

// TestMkswapHeaderAndSummary covers what the original prints and what it
// leaves on disk, including the label truncation and the endianness switch.
func TestMkswapHeaderAndSummary(t *testing.T) {
	newImage := func(t *testing.T, size int) string {
		t.Helper()
		image := filepath.Join(t.TempDir(), "swap.img")
		if err := os.WriteFile(image, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return image
	}

	t.Run("summary", func(t *testing.T) {
		image := newImage(t, 32*1024*1024)
		status, stdout, stderr := captureApplet(t, cmdMkswap, []string{"-L", "rescue", image}, "")
		want := "Setting up swapspace version 1, size = 32 MiB (33550336 bytes)\n"
		if status != 0 || !strings.HasPrefix(stdout, want) {
			t.Fatalf("mkswap = status %d, %q (stderr %q), want prefix %q", status, stdout, stderr, want)
		}
		if !strings.Contains(stdout, "LABEL=rescue, UUID=") {
			t.Fatalf("summary lacked the label line: %q", stdout)
		}
	})

	t.Run("no label", func(t *testing.T) {
		image := newImage(t, 1024*1024)
		_, stdout, _ := captureApplet(t, cmdMkswap, []string{image}, "")
		if !strings.Contains(stdout, "no label, UUID=") {
			t.Fatalf("summary = %q, want the unlabelled form", stdout)
		}
	})

	t.Run("quiet", func(t *testing.T) {
		image := newImage(t, 1024*1024)
		status, stdout, stderr := captureApplet(t, cmdMkswap, []string{"-q", image}, "")
		if status != 0 || stdout != "" || stderr != "" {
			t.Fatalf("mkswap -q = status %d, %q, %q; want silence", status, stdout, stderr)
		}
	})

	t.Run("truncated label", func(t *testing.T) {
		image := newImage(t, 1024*1024)
		_, stdout, stderr := captureApplet(t, cmdMkswap, []string{"-L", "0123456789abcdefXYZ", image}, "")
		if !strings.Contains(stderr, "mkswap: Label was truncated.\n") {
			t.Fatalf("stderr = %q, want the truncation warning", stderr)
		}
		if !strings.Contains(stdout, "LABEL=0123456789abcde, UUID=") {
			t.Fatalf("summary = %q, want the fifteen byte label", stdout)
		}
		header, err := os.ReadFile(image)
		if err != nil {
			t.Fatal(err)
		}
		if string(header[1052:1068]) != "0123456789abcde\x00" {
			t.Fatalf("stored label = %q", header[1052:1068])
		}
	})

	t.Run("big endian", func(t *testing.T) {
		image := newImage(t, 1024*1024)
		if status, _, stderr := captureApplet(t, cmdMkswap, []string{"-e", "big", image}, ""); status != 0 {
			t.Fatalf("mkswap -e big = status %d, stderr %q", status, stderr)
		}
		header, err := os.ReadFile(image)
		if err != nil {
			t.Fatal(err)
		}
		if binary.BigEndian.Uint32(header[1024:1028]) != 1 ||
			binary.BigEndian.Uint32(header[1028:1032]) != 1024*1024/4096-1 {
			t.Fatalf("header was not written big endian: %x", header[1024:1032])
		}
	})

	t.Run("uuid clear", func(t *testing.T) {
		image := newImage(t, 1024*1024)
		_, stdout, _ := captureApplet(t, cmdMkswap, []string{"-U", "clear", image}, "")
		if !strings.Contains(stdout, "UUID=00000000-0000-0000-0000-000000000000") {
			t.Fatalf("summary = %q, want the cleared UUID", stdout)
		}
	})

	t.Run("uuid given", func(t *testing.T) {
		image := newImage(t, 1024*1024)
		_, stdout, _ := captureApplet(t, cmdMkswap, []string{"-U", "11111111-2222-3333-4444-555555555555", image}, "")
		if !strings.Contains(stdout, "UUID=11111111-2222-3333-4444-555555555555") {
			t.Fatalf("summary = %q, want the UUID that was asked for", stdout)
		}
	})

	t.Run("block count", func(t *testing.T) {
		image := newImage(t, 32*1024*1024)
		_, stdout, _ := captureApplet(t, cmdMkswap, []string{image, "16384"}, "")
		if !strings.HasPrefix(stdout, "Setting up swapspace version 1, size = 16 MiB (16773120 bytes)\n") {
			t.Fatalf("summary = %q, want the size the block count asked for", stdout)
		}
	})

	t.Run("page size", func(t *testing.T) {
		image := newImage(t, 32*1024*1024)
		_, stdout, stderr := captureApplet(t, cmdMkswap, []string{"-p", "8192", image}, "")
		if !strings.Contains(stderr, "Using user-specified page size 8192, instead of the system value") {
			t.Fatalf("stderr = %q, want the page size notice", stderr)
		}
		if !strings.HasPrefix(stdout, "Setting up swapspace version 1, size = 32 MiB (33546240 bytes)\n") {
			t.Fatalf("summary = %q, want the size in 8 KiB pages", stdout)
		}
		header, err := os.ReadFile(image)
		if err != nil {
			t.Fatal(err)
		}
		if string(header[8182:8192]) != "SWAPSPACE2" {
			t.Fatalf("signature was not written at the end of the page: %q", header[8182:8192])
		}
	})
}

func TestMkswapRefusals(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.img")
	if err := os.WriteFile(small, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(dir, "swap.img")
	if err := os.WriteFile(image, make([]byte, 32*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		args   []string
		stderr string
	}{
		{"too small", []string{small}, "mkswap: error: swap area needs to be at least 40 KiB\n"},
		{"no device", nil, "mkswap: error: Nowhere to set up swap on?\n"},
		{"three operands", []string{image, "1", "2"}, "mkswap: only one device argument is currently supported\n"},
		{"bad block count", []string{image, "extra"}, "mkswap: invalid block count argument: 'extra'\n"},
		{"block count too large", []string{image, "999999"}, "mkswap: error: size 999996 KiB is larger than device size 32768 KiB\n"},
		{"bad page size", []string{"-p", "3000", image}, "mkswap: Bad user-specified page size 3000\n"},
		{"bad uuid", []string{"-U", "bogus", image}, "mkswap: error: parsing UUID failed\n"},
		{"bad version", []string{"-v", "0", image}, "mkswap: swapspace version 0 is not supported\n"},
		{"bad endianness", []string{"-e", "sideways", image}, "mkswap: invalid endianness sideways is not supported\n"},
		{"bad size", []string{"-s", "notasize", "-F", image}, "mkswap: invalid size: 'notasize': Invalid argument\n"},
		{"missing argument", []string{"-L"}, "mkswap: option requires an argument -- 'L'\n"},
		{"unknown short", []string{"-Z", image}, "mkswap: invalid option -- 'Z'\n"},
		{"unknown long", []string{"--nope", image}, "mkswap: unrecognized option '--nope'\n"},
		{"missing file", []string{filepath.Join(dir, "nosuch.img")}, "mkswap: cannot open "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, stderr := captureApplet(t, cmdMkswap, tc.args, "")
			if status != 1 || !strings.HasPrefix(stderr, tc.stderr) {
				t.Fatalf("mkswap %v = status %d, stderr %q; want 1 and prefix %q", tc.args, status, stderr, tc.stderr)
			}
		})
	}
}

// TestMkswapInsecurePermissions covers the complaint about a swap file anyone
// can read, and the repair -F makes after it.
func TestMkswapInsecurePermissions(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "swap.img")
	if err := os.WriteFile(image, make([]byte, 1024*1024), 0o644); err != nil { //nolint:gosec // The loose mode is the point of the test.
		t.Fatal(err)
	}
	_, _, stderr := captureApplet(t, cmdMkswap, []string{image}, "")
	want := "mkswap: " + image + ": insecure permissions 0644, fix with: chmod 0600 " + image + "\n"
	if !strings.HasPrefix(stderr, want) {
		t.Fatalf("stderr = %q, want prefix %q", stderr, want)
	}
	if info, err := os.Stat(image); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("permissions changed without -F: %v, %v", info, err)
	}
	if status, _, stderr := captureApplet(t, cmdMkswap, []string{"-F", image}, ""); status != 0 {
		t.Fatalf("mkswap -F = status %d, stderr %q", status, stderr)
	}
	if info, err := os.Stat(image); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("-F left the permissions at %v, %v", info.Mode().Perm(), err)
	}
}

// TestMkswapWipesOldSignature covers the warning and the erasure, including a
// btrfs superblock, which lives past the page the header occupies.
func TestMkswapWipesOldSignature(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "btrfs.img")
	data := make([]byte, 1024*1024)
	copy(data[65536+64:], "_BHRfS_M")
	if err := os.WriteFile(image, data, 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdMkswap, []string{image}, "")
	want := "mkswap: " + image + ": warning: wiping old btrfs signature.\n"
	if status != 0 || !strings.Contains(stderr, want) {
		t.Fatalf("mkswap = status %d, stderr %q; want %q", status, stderr, want)
	}
	after, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if string(after[65536+64:65536+72]) != "\x00\x00\x00\x00\x00\x00\x00\x00" {
		t.Fatalf("btrfs magic survived: %q", after[65536+64:65536+72])
	}
	if string(after[4086:4096]) != "SWAPSPACE2" {
		t.Fatal("swap signature was not written")
	}
}

// TestMkswapCreatesSwapFile covers "-F --size", which is the one form that
// makes a file rather than finding one.
func TestMkswapCreatesSwapFile(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "new.img")
	status, stdout, stderr := captureApplet(t, cmdMkswap, []string{"-s", "3MiB", "-F", image}, "")
	if status != 0 {
		t.Fatalf("mkswap = status %d, stderr %q", status, stderr)
	}
	if !strings.HasPrefix(stdout, "Setting up swapspace version 1, size = 3 MiB (3141632 bytes)\n") {
		t.Fatalf("summary = %q", stdout)
	}
	info, err := os.Stat(image)
	if err != nil || info.Size() != 3*1024*1024 || info.Mode().Perm() != 0o600 {
		t.Fatalf("created file = %v, %v", info, err)
	}
	// Without a size there is nothing to create, so the missing file is an
	// error rather than a new one.
	missing := filepath.Join(dir, "absent.img")
	if status, _, _ := captureApplet(t, cmdMkswap, []string{"-F", missing}, ""); status != 1 {
		t.Fatalf("mkswap -F on a missing file = status %d, want 1", status)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("mkswap -F created %s without a size", missing)
	}
}
