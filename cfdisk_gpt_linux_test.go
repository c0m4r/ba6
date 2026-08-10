// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cfdiskTestGPTGUID(t *testing.T, text string) [16]byte {
	t.Helper()
	guid, err := cfdiskParseGPTGUID(text)
	if err != nil {
		t.Fatalf("parse GPT GUID %q: %v", text, err)
	}
	return guid
}

func cfdiskGPTTestImage(t *testing.T) (string, []byte, cfdiskGPTTable, cfdiskGPTRawState) {
	t.Helper()
	image := filepath.Join(t.TempDir(), "gpt.img")
	disk := make([]byte, 8*1024*1024)
	copy(disk[:446], "preserve MBR bootstrap bytes")
	copy(disk[100*512:], "do not touch ordinary disk data")
	if err := os.WriteFile(image, disk, 0o600); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	table, _, err := cfdiskNewGPT(file, uint64(len(disk)/512))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	table.diskGUID = cfdiskTestGPTGUID(t, "01234567-89ab-4def-8123-456789abcdef")
	table.partitions[0] = cfdiskGPTPartition{
		typeGUID:   cfdiskGPTLinuxFilesystemType,
		uniqueGUID: cfdiskTestGPTGUID(t, "11111111-2222-4333-8444-555555555555"),
		start:      2048,
		end:        4095,
		attributes: 1 << 60,
		name:       "root",
	}
	state, err := cfdiskBuildGPT(disk[:512], uint64(len(disk)/512), table)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCfdiskGPT(image, uint64(len(disk)/512), state); err != nil {
		t.Fatal(err)
	}
	return image, disk, table, state
}

func TestCfdiskBuildWriteAndReadGPT(t *testing.T) {
	image, original, want, written := cfdiskGPTTestImage(t)
	gotDisk, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotDisk[:446], original[:446]) || !bytes.Equal(gotDisk[100*512:100*512+32], original[100*512:100*512+32]) {
		t.Fatal("GPT write changed bytes outside its metadata regions")
	}
	if gotDisk[446+4] != 0xee || gotDisk[510] != 0x55 || gotDisk[511] != 0xaa {
		t.Fatalf("protective MBR = %x", gotDisk[446:462])
	}

	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	got, state, err := cfdiskReadGPT(file, uint64(len(gotDisk)/512))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("read GPT = %#v, want %#v", got, want)
	}
	if !state.equal(written) {
		t.Fatal("read GPT state differs from the matching primary/backup data written")
	}

	status, stdout, stderr := captureApplet(t, cmdFdisk, []string{"-l", image}, "")
	if status != 0 || stderr != "" || !strings.Contains(stdout, "Disklabel type: gpt") ||
		!strings.Contains(stdout, image+"1") || !strings.Contains(stdout, "root") {
		t.Fatalf("fdisk list of cfdisk GPT = (%d, %q, %q)", status, stdout, stderr)
	}
}

func TestCfdiskGPTRejectsBackupDamageAndStaleTables(t *testing.T) {
	image, _, _, state := cfdiskGPTTestImage(t)
	before, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	invalid := state
	invalid.primaryHeader[0] ^= 1
	if err := writeCfdiskGPT(image, uint64(len(before)/512), invalid); err == nil || !strings.Contains(err.Error(), "invalid generated GPT data") {
		t.Fatalf("invalid generated GPT write error = %v", err)
	}
	after, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rejected GPT data changed the image")
	}
	disk := after
	disk[len(disk)-512] ^= 1
	if err := os.WriteFile(image, disk, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = cfdiskReadGPT(file, uint64(len(disk)/512))
	_ = file.Close()
	if err == nil || !strings.Contains(err.Error(), "backup GPT header") {
		t.Fatalf("damaged backup GPT error = %v", err)
	}

	image, _, _, state = cfdiskGPTTestImage(t)
	session, err := newCfdiskSession(cfdiskOptions{device: image, sectorSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if session.labelType != cfdiskLabelGPT || !session.gptOriginal.equal(state) {
		t.Fatalf("loaded GPT session = %+v", session)
	}
	mutator, err := os.OpenFile(image, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mutator.WriteAt([]byte{state.backupEntries[0] ^ 1}, int64((session.diskSectors-cfdiskGPTEntrySectors-1)*512))
	if closeErr := mutator.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := session.verifyGPTWriteTarget(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale GPT table error = %v", err)
	}
}

func TestCfdiskGPTTableHelpersAndRendering(t *testing.T) {
	if got := formatGPTGUID(cfdiskGPTLinuxFilesystemType[:]); got != "0fc63daf-8483-4772-8e79-3d69d8477de4" {
		t.Fatalf("Linux filesystem GPT GUID = %q", got)
	}
	if got := formatGPTGUID(cfdiskGPTLinuxSwapType[:]); got != "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f" {
		t.Fatalf("Linux swap GPT GUID = %q", got)
	}
	if got := formatGPTGUID(cfdiskGPTEFISystemType[:]); got != "c12a7328-f81f-11d2-ba4b-00a0c93ec93b" {
		t.Fatalf("EFI System GPT GUID = %q", got)
	}
	first, last, _, _, err := cfdiskGPTGeometry(16 * 1024)
	if err != nil {
		t.Fatal(err)
	}
	table := cfdiskGPTTable{
		diskGUID:    cfdiskTestGPTGUID(t, "01234567-89ab-4def-8123-456789abcdef"),
		firstUsable: first,
		lastUsable:  last,
	}
	table.partitions[3] = cfdiskGPTPartition{
		typeGUID:   cfdiskGPTLinuxSwapType,
		uniqueGUID: cfdiskTestGPTGUID(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"),
		start:      8192,
		end:        9215,
		name:       "swap",
	}
	table.partitions[8] = cfdiskGPTPartition{
		typeGUID:   cfdiskGPTLinuxFilesystemType,
		uniqueGUID: cfdiskTestGPTGUID(t, "11111111-2222-4333-8444-555555555555"),
		start:      2048,
		end:        4095,
		name:       "root\x1b[31m",
	}
	if err := validateCfdiskGPTTable(table); err != nil {
		t.Fatal(err)
	}
	regions, err := cfdiskGPTFreeRegions(table)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 3 || regions[0] != (cfdiskFreeRegion{start: first, size: 2048 - first}) ||
		regions[1] != (cfdiskFreeRegion{start: 4096, size: 4096}) || regions[2].start != 9216 {
		t.Fatalf("GPT free regions = %#v", regions)
	}

	session := &cfdiskSession{labelType: cfdiskLabelGPT, gpt: table, selected: 8}
	if _, err := session.handleKey(1000); err != nil || session.selected != 3 {
		t.Fatalf("GPT up-arrow selection = (%d, %v)", session.selected, err)
	}
	if _, err := session.handleKey(1001); err != nil || session.selected != 8 {
		t.Fatalf("GPT down-arrow selection = (%d, %v)", session.selected, err)
	}
	session.sortPartitions()
	if !session.dirty || session.selected != 0 || session.gpt.partitions[0].start != 2048 || session.gpt.partitions[1].start != 8192 {
		t.Fatalf("sorted GPT session = %+v", session)
	}
	joined := strings.Join(session.gptLines(), "\n")
	if strings.Contains(joined, "\x1b") || !strings.Contains(joined, "root?[31m") {
		t.Fatalf("unsafe GPT name rendering: %q", joined)
	}

	if got, err := cfdiskParseGPTType("swap"); err != nil || got != cfdiskGPTLinuxSwapType {
		t.Fatalf("swap GPT type = %x, %v", got, err)
	}
	if got, err := cfdiskParseGPTType("0FC63DAF-8483-4772-8E79-3D69D8477DE4"); err != nil || got != cfdiskGPTLinuxFilesystemType {
		t.Fatalf("GUID GPT type = %x, %v", got, err)
	}
	if _, err := cfdiskParseGPTType("00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("empty GPT type was accepted")
	}
	dump := cfdiskGPTDump("/tmp/disk.img", session.gpt)
	if !strings.Contains(dump, "label: gpt") || !strings.Contains(dump, "uuid=11111111-2222-4333-8444-555555555555") {
		t.Fatalf("GPT dump = %q", dump)
	}
}
