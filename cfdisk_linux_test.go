// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCfdiskOptions(t *testing.T) {
	defaults, err := parseCfdiskOptions([]string{"disk.img"})
	if err != nil || defaults.lock || defaults.lockNB || defaults.color != cfdiskColorAuto {
		t.Fatalf("default options = %+v, %v", defaults, err)
	}
	options, err := parseCfdiskOptions([]string{"-rz", "-b512", "--color=never", "--lock=no", "disk.img"})
	if err != nil {
		t.Fatal(err)
	}
	if options.device != "disk.img" || !options.readOnly || !options.zero || options.lock || options.sectorSize != 512 ||
		options.color != cfdiskColorNever {
		t.Fatalf("options = %+v", options)
	}
	options, err = parseCfdiskOptions([]string{"--lock=nonblock", "--color=always", "disk.img"})
	if err != nil || !options.lock || !options.lockNB || options.color != cfdiskColorAlways {
		t.Fatalf("nonblocking lock options = %+v, %v", options, err)
	}
	options, err = parseCfdiskOptions([]string{"--lock=yes", "disk.img"})
	if err != nil || !options.lock || options.lockNB {
		t.Fatalf("blocking lock options = %+v, %v", options, err)
	}
	options, err = parseCfdiskOptions([]string{"-L", "--lock", "disk.img"})
	if err != nil || !options.lock || options.lockNB || options.color != cfdiskColorAuto {
		t.Fatalf("implicit lock/color options = %+v, %v", options, err)
	}
	for _, args := range [][]string{
		{},
		{"--sector-size", "4096", "disk.img"},
		{"--color=rainbow", "disk.img"},
		{"one.img", "two.img"},
	} {
		if _, err := parseCfdiskOptions(args); err == nil {
			t.Errorf("parseCfdiskOptions(%q) unexpectedly succeeded", args)
		}
	}
	options, err = parseCfdiskOptions([]string{"--version"})
	if err != nil || !options.version {
		t.Fatalf("--version = (%+v, %v)", options, err)
	}
}

func TestCfdiskFreeSpaceAndValidation(t *testing.T) {
	partitions := [4]mbrPartition{{start: 2048, size: 2048, kind: 0x83}}
	regions, err := cfdiskFreeRegions(partitions, 16384)
	if err != nil {
		t.Fatal(err)
	}
	want := []cfdiskFreeRegion{{start: 1, size: 2047}, {start: 4096, size: 12288}}
	if len(regions) != len(want) {
		t.Fatalf("regions = %#v, want %#v", regions, want)
	}
	for index := range want {
		if regions[index] != want[index] {
			t.Fatalf("regions[%d] = %#v, want %#v", index, regions[index], want[index])
		}
	}
	suggested, err := cfdiskSuggestedFreeRegion(partitions, 16384)
	if err != nil || suggested != want[1] {
		t.Fatalf("suggested = %#v, err = %v", suggested, err)
	}

	overlapping := partitions
	overlapping[1] = mbrPartition{start: 3000, size: 100, kind: 0x82}
	if err := validateCfdiskPartitions(overlapping, 16384); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlap error = %v", err)
	}
	extended := partitions
	extended[0].kind = 0x0f
	if err := validateCfdiskPartitions(extended, 16384); err == nil || !strings.Contains(err.Error(), "extended") {
		t.Fatalf("extended error = %v", err)
	}
}

func TestCfdiskLabelSelectorAndActionMenu(t *testing.T) {
	image := filepath.Join(t.TempDir(), "blank.img")
	if err := os.WriteFile(image, make([]byte, 4*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := newCfdiskSession(cfdiskOptions{device: image, sectorSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if !session.labelSelector || session.labelSelected != cfdiskGPTLabelIndex {
		t.Fatalf("initial label selector = %+v", session)
	}
	if _, err := session.handleKey(1001); err != nil { // Move from gpt to dos.
		t.Fatal(err)
	}
	if session.labelSelected != cfdiskDOSLabelIndex {
		t.Fatalf("label selection = %d, want dos", session.labelSelected)
	}
	if _, err := session.handleKey(1000); err != nil { // Move back to gpt.
		t.Fatal(err)
	}
	if session.labelSelected != cfdiskGPTLabelIndex {
		t.Fatalf("label selection = %d, want gpt", session.labelSelected)
	}
	if _, err := session.handleKey('\r'); err != nil {
		t.Fatal(err)
	}
	if session.labelSelector || !session.dirty || session.labelType != cfdiskLabelGPT ||
		!strings.Contains(session.message, "empty GPT") {
		t.Fatalf("GPT label selection = %+v", session)
	}
	if got := session.lines(); !strings.Contains(strings.Join(got, "\n"), "Label: gpt") {
		t.Fatalf("GPT screen = %q", got)
	}
	if _, err := session.handleKey(1001); err != nil || session.selected != 0 {
		t.Fatalf("GPT down-arrow navigation = (selected %d, err %v)", session.selected, err)
	}
	if _, err := session.handleKey(1000); err != nil || session.selected != 0 {
		t.Fatalf("GPT up-arrow navigation = (selected %d, err %v)", session.selected, err)
	}
	readOnly := &cfdiskSession{
		options:       cfdiskOptions{readOnly: true},
		labelSelector: true,
		labelSelected: cfdiskDOSLabelIndex,
	}
	if _, err := readOnly.handleKey('\r'); err != nil {
		t.Fatal(err)
	}
	if !readOnly.labelSelector || readOnly.dirty || !strings.Contains(readOnly.message, "Read-only") {
		t.Fatalf("read-only label selection = %+v", readOnly)
	}

	session.options.color = cfdiskColorNever
	bar := session.actionBar(80)
	for _, want := range []string{"> New <", "[ Quit ]", "[ Help ]", "[ Write ]", "[ Dump ]"} {
		if !strings.Contains(bar, want) {
			t.Errorf("action bar missing %q: %q", want, bar)
		}
	}
	if strings.Contains(bar, "\x1b[") {
		t.Fatalf("colour=never action bar contains ANSI styling: %q", bar)
	}

	session.dirty = false
	if _, err := session.handleKey(1003); err != nil || session.menuSelected != 1 || session.message != "" {
		t.Fatalf("right-arrow menu navigation = (%d, %q, %v)", session.menuSelected, session.message, err)
	}
	if exit, err := session.handleKey('\r'); err != nil || !exit {
		t.Fatalf("Enter on Quit = (exit %v, err %v)", exit, err)
	}
	help := &cfdiskSession{menuSelected: 2}
	if exit, err := help.handleKey('\r'); err != nil || exit || !help.help {
		t.Fatalf("Enter on Help = (exit %v, help %v, err %v)", exit, help.help, err)
	}

	var dialog strings.Builder
	(&cfdiskSession{labelSelector: true, labelSelected: cfdiskGPTLabelIndex}).drawLabelSelector(&dialog, 24, 80)
	for _, want := range []string{"Select label type", "gpt", "dos"} {
		if !strings.Contains(dialog.String(), want) {
			t.Errorf("label dialog missing %q: %q", want, dialog.String())
		}
	}
	if lower := strings.ToLower(dialog.String()); strings.Contains(lower, "sgi") || strings.Contains(lower, "sun") || strings.Contains(lower, "unsupported") {
		t.Fatalf("label dialog retained unsupported labels: %q", dialog.String())
	}
}

func TestCfdiskSizeSortingAndDump(t *testing.T) {
	for _, test := range []struct {
		input string
		want  uint64
	}{
		{"17", 17},
		{"1K", 2},
		{"2MiB", 4096},
		{"1g", 2 * 1024 * 1024},
		{"+4Ki", 8},
	} {
		got, err := cfdiskParseSize(test.input, 512)
		if err != nil || got != test.want {
			t.Errorf("cfdiskParseSize(%q) = (%d, %v), want %d", test.input, got, err, test.want)
		}
	}
	if _, err := cfdiskParseSize("nope", 512); err == nil {
		t.Fatal("invalid cfdisk size was accepted")
	}

	partitions := [4]mbrPartition{
		{start: 4096, size: 1024, kind: 0x82},
		{start: 2048, size: 1024, kind: 0x83, bootable: true},
	}
	sorted := cfdiskSortedPartitions(partitions)
	if sorted[0].start != 2048 || sorted[1].start != 4096 || sorted[0].kind != 0x83 || sorted[2].size != 0 {
		t.Fatalf("sorted partitions = %#v", sorted)
	}
	dump := cfdiskDump("/tmp/disk.img", sorted)
	for _, want := range []string{
		"label: dos", "unit: sectors", "/tmp/disk.img1 : start=2048, size=1024, type=83, bootable",
		"/tmp/disk.img2 : start=4096, size=1024, type=82",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump is missing %q:\n%s", want, dump)
		}
	}
	parsed, err := parseSfdiskScript(strings.NewReader(dump), 8192)
	if err != nil || len(parsed) != 2 || parsed[0] != sorted[0] || parsed[1] != sorted[1] {
		t.Fatalf("parsed dump = %#v, %v", parsed, err)
	}
}

func TestCfdiskSlotMutations(t *testing.T) {
	partitions := [4]mbrPartition{
		{start: 4096, size: 1024, kind: 0x82},
		{start: 2048, size: 1024, kind: 0x83},
	}
	session := &cfdiskSession{partitions: partitions}
	session.sortPartitions()
	if !session.dirty || session.selected != 1 || session.partitions[0].start != 2048 || session.partitions[1].start != 4096 {
		t.Fatalf("sorted session = %+v", session)
	}
	session.toggleBootable()
	if !session.partitions[1].bootable {
		t.Fatal("boot flag was not toggled")
	}
	session.deletePartition()
	if session.partitions[1].size != 0 || session.partitions[0].size == 0 {
		t.Fatalf("deleted session = %+v", session)
	}

	readOnly := &cfdiskSession{options: cfdiskOptions{readOnly: true}, partitions: partitions}
	readOnly.deletePartition()
	if readOnly.partitions != partitions || !strings.Contains(readOnly.message, "Read-only") {
		t.Fatalf("read-only delete = %+v", readOnly)
	}
}

func TestCfdiskBuildAndWriteMBR(t *testing.T) {
	image := filepath.Join(t.TempDir(), "disk.img")
	original := make([]byte, 4*1024*1024)
	copy(original[:446], "preserve MBR bootstrap bytes")
	copy(original[512:], "do not touch later disk bytes")
	if err := os.WriteFile(image, original, 0o600); err != nil {
		t.Fatal(err)
	}
	partitions := [4]mbrPartition{
		{start: 2048, size: 4096, kind: 0x83, bootable: true},
		{start: 6144, size: 1024, kind: 0x82},
	}
	if err := validateCfdiskPartitions(partitions, 8192); err != nil {
		t.Fatal(err)
	}
	sector := cfdiskBuildMBR(original[:512], partitions)
	if err := writeCfdiskMBR(image, sector); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:446], original[:446]) || !bytes.Equal(got[512:], original[512:]) {
		t.Fatal("cfdisk write changed data outside the MBR partition table")
	}
	if got[510] != 0x55 || got[511] != 0xaa {
		t.Fatal("missing MBR signature")
	}
	first := got[446:462]
	if first[0] != 0x80 || first[4] != 0x83 || binary.LittleEndian.Uint32(first[8:12]) != 2048 ||
		binary.LittleEndian.Uint32(first[12:16]) != 4096 {
		t.Fatalf("first partition = %x", first)
	}
	if parsed := cfdiskReadPartitions(got[:512]); parsed != partitions {
		t.Fatalf("parsed partitions = %#v, want %#v", parsed, partitions)
	}
}

func TestCfdiskEmptyDumpCreatesEmptyDOSLabel(t *testing.T) {
	image := filepath.Join(t.TempDir(), "disk.img")
	original := make([]byte, 2*1024*1024)
	copy(original[:32], "preserve bootstrap bytes")
	if err := os.WriteFile(image, original, 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdSfdisk, []string{image}, cfdiskDump(image, [4]mbrPartition{}))
	if status != 0 || stderr != "" {
		t.Fatalf("empty cfdisk dump = (%d, %q)", status, stderr)
	}
	got, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:446], original[:446]) || got[510] != 0x55 || got[511] != 0xaa {
		t.Fatal("empty dump did not write a blank DOS label safely")
	}
}

func TestCfdiskSessionRejectsInvalidGPTAndZeroOpensSelector(t *testing.T) {
	image := filepath.Join(t.TempDir(), "gpt.img")
	disk := make([]byte, 2*1024*1024)
	disk[446+4] = 0xee
	disk[510], disk[511] = 0x55, 0xaa
	if err := os.WriteFile(image, disk, 0o600); err != nil {
		t.Fatal(err)
	}
	options := cfdiskOptions{device: image, sectorSize: 512, lock: false}
	if _, err := newCfdiskSession(options); err == nil || !strings.Contains(err.Error(), "GPT") {
		t.Fatalf("GPT session error = %v", err)
	}
	options.zero = true
	session, err := newCfdiskSession(options)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if session.dirty || !session.labelSelector || session.labelType != cfdiskLabelNone || session.partitions != ([4]mbrPartition{}) {
		t.Fatalf("zeroed session = %+v", session)
	}
}

func TestCfdiskBlankDiskStartsAtLabelSelector(t *testing.T) {
	image := filepath.Join(t.TempDir(), "blank.img")
	if err := os.WriteFile(image, make([]byte, 2*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := newCfdiskSession(cfdiskOptions{device: image, sectorSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if !session.labelSelector || session.labelSelected != cfdiskGPTLabelIndex || session.dirty {
		t.Fatalf("blank session = %+v", session)
	}
}

func TestCfdiskDetectsChangedStagedTable(t *testing.T) {
	image := filepath.Join(t.TempDir(), "disk.img")
	disk := make([]byte, 2*1024*1024)
	disk[510], disk[511] = 0x55, 0xaa
	if err := os.WriteFile(image, disk, 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := newCfdiskSession(cfdiskOptions{device: image, sectorSize: 512, lock: false})
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	disk[440] = 1 // The disk signature area is preserved by a normal MBR write.
	if err := os.WriteFile(image, disk, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.verifyWriteTarget(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale-table error = %v", err)
	}
}

func TestCfdiskRequiresTerminal(t *testing.T) {
	image := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(image, make([]byte, 2*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdCfdisk, []string{image}, "")
	if status == 0 || !strings.Contains(stderr, "terminal") {
		t.Fatalf("cfdisk without terminal = (%d, %q)", status, stderr)
	}
}
