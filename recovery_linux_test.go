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
