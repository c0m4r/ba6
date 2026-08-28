// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withStdin feeds data to readEditorKey (which reads os.Stdin) for the
// duration of fn, then restores the original stdin. It's how a test drives
// cfdisk's prompt() without a real terminal.
func withStdin(t *testing.T, data string, fn func()) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = read
	defer func() { os.Stdin = original }()
	go func() {
		_, _ = write.WriteString(data)
		write.Close()
	}()
	fn()
}

// This layout -- extended {43008, 366592} on a 409600-sector disk, holding
// logical partitions at 45056/20480, 67584/20480 and 90112/319488 -- is not
// arbitrary: it's exactly what util-linux's fdisk 2.41.5 wrote to a
// disposable 200 MiB image file, read back sector by sector. Reusing it here
// means cfdiskBuildEBRChain's output is checked against real fdisk's actual
// chain-link arithmetic, not just against itself.
var (
	cfdiskTestExtended = mbrPartition{start: 43008, size: 366592, kind: 0x05}
	cfdiskTestLogical  = []cfdiskLogical{
		{partition: mbrPartition{start: 45056, size: 20480, kind: 0x83}, ebrLBA: 43008},
		{partition: mbrPartition{start: 67584, size: 20480, kind: 0x83}, ebrLBA: 65536},
		{partition: mbrPartition{start: 90112, size: 319488, kind: 0x83}, ebrLBA: 88064},
	}
)

func TestCfdiskLogicalFreeRegionsMatchRealFdiskGaps(t *testing.T) {
	// Before any logical partition exists, the whole container is free.
	empty := cfdiskLogicalFreeRegions(cfdiskTestExtended, nil)
	if len(empty) != 1 || empty[0].start != 43008 || empty[0].size != 366592 {
		t.Fatalf("empty container regions = %#v", empty)
	}
	suggested, err := cfdiskSuggestedLogicalRegion(cfdiskTestExtended, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Real fdisk put the first boot record at the container's own start and
	// the first logical partition's data 2048 sectors later.
	if suggested.ebrLBA != 43008 || suggested.dataStart != 45056 {
		t.Fatalf("first suggestion = %#v, want ebrLBA=43008 dataStart=45056", suggested)
	}

	// With the first two logical partitions placed, the only gap left is
	// after the second one -- exactly where real fdisk put the third.
	suggested, err = cfdiskSuggestedLogicalRegion(cfdiskTestExtended, cfdiskTestLogical[:2])
	if err != nil {
		t.Fatal(err)
	}
	if suggested.ebrLBA != 88064 || suggested.dataStart != 90112 || suggested.dataSize != 366592+43008-90112 {
		t.Fatalf("third suggestion = %#v, want ebrLBA=88064 dataStart=90112", suggested)
	}
}

func TestCfdiskLogicalBootRecordFor(t *testing.T) {
	ebrLBA, err := cfdiskLogicalBootRecordFor(cfdiskTestExtended, cfdiskTestLogical[:2], 90200)
	if err != nil || ebrLBA != 88064 {
		t.Fatalf("bootRecordFor(90200) = (%d, %v), want (88064, nil)", ebrLBA, err)
	}
	if _, err := cfdiskLogicalBootRecordFor(cfdiskTestExtended, cfdiskTestLogical, 50000); err == nil {
		t.Fatal("a start sector already inside a logical partition should be rejected")
	}
}

func TestValidateCfdiskLogicalPartitions(t *testing.T) {
	if err := validateCfdiskLogicalPartitions(cfdiskTestExtended, cfdiskTestLogical); err != nil {
		t.Fatalf("the real-fdisk-derived layout should validate: %v", err)
	}

	overlapping := append([]cfdiskLogical{}, cfdiskTestLogical...)
	overlapping[1].partition.start = 50000 // now inside logical partition 0's span
	overlapping[1].ebrLBA = 49000          // still a valid (before its own data) boot record
	if err := validateCfdiskLogicalPartitions(cfdiskTestExtended, overlapping); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlap error = %v", err)
	}

	nestedExtended := append([]cfdiskLogical{}, cfdiskTestLogical...)
	nestedExtended[0].partition.kind = 0x05
	if err := validateCfdiskLogicalPartitions(cfdiskTestExtended, nestedExtended); err == nil || !strings.Contains(err.Error(), "cannot itself be extended") {
		t.Fatalf("nested-extended error = %v", err)
	}

	outside := append([]cfdiskLogical{}, cfdiskTestLogical...)
	outside[2].partition.size = 500000 // now runs past the container's end
	if err := validateCfdiskLogicalPartitions(cfdiskTestExtended, outside); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside-container error = %v", err)
	}

	badRecord := append([]cfdiskLogical{}, cfdiskTestLogical...)
	badRecord[0].ebrLBA = uint64(badRecord[0].partition.start) // boot record not before its own data
	if err := validateCfdiskLogicalPartitions(cfdiskTestExtended, badRecord); err == nil || !strings.Contains(err.Error(), "boot record") {
		t.Fatalf("bad-boot-record error = %v", err)
	}
}

func TestCfdiskBuildEBRChainMatchesRealFdiskArithmetic(t *testing.T) {
	writes, err := cfdiskBuildEBRChain(cfdiskTestExtended, cfdiskTestLogical)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 3 {
		t.Fatalf("got %d boot records, want 3", len(writes))
	}
	// Real fdisk's own three boot records, decoded by hand from the image it
	// wrote: (lba, own-entry start/size, link start/size).
	want := []struct {
		lba                 uint64
		ownStart, ownSize   uint32
		linkStart, linkSize uint32
	}{
		{43008, 2048, 20480, 22528, 22528},
		{65536, 2048, 20480, 45056, 321536},
		{88064, 2048, 319488, 0, 0},
	}
	for index, write := range writes {
		if write.lba != want[index].lba {
			t.Fatalf("boot record %d at LBA %d, want %d", index, write.lba, want[index].lba)
		}
		own := cfdiskReadEntry(write.sector, 446)
		link := cfdiskReadEntry(write.sector, 462)
		if own.start != want[index].ownStart || own.size != want[index].ownSize || own.kind != 0x83 {
			t.Fatalf("boot record %d own entry = %+v, want start=%d size=%d", index, own, want[index].ownStart, want[index].ownSize)
		}
		if link.start != want[index].linkStart || link.size != want[index].linkSize {
			t.Fatalf("boot record %d link entry = %+v, want start=%d size=%d", index, link, want[index].linkStart, want[index].linkSize)
		}
		if write.sector[510] != 0x55 || write.sector[511] != 0xaa {
			t.Fatalf("boot record %d missing 0x55aa signature", index)
		}
	}
}

func TestCfdiskEBRChainWriteReadRoundTrip(t *testing.T) {
	image := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(image, make([]byte, 410000*512), 0o600); err != nil {
		t.Fatal(err)
	}
	writes, err := cfdiskBuildEBRChain(cfdiskTestExtended, cfdiskTestLogical)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCfdiskEBRChain(image, writes); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := cfdiskReadLogicalChain(file, cfdiskTestExtended, 410000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(cfdiskTestLogical) {
		t.Fatalf("read back %d logical partitions, want %d", len(got), len(cfdiskTestLogical))
	}
	for index, entry := range got {
		want := cfdiskTestLogical[index]
		if entry.partition != want.partition || entry.ebrLBA != want.ebrLBA {
			t.Fatalf("logical[%d] = %+v, want %+v", index, entry, want)
		}
	}

	// An empty extended container (never written to) must read back as no
	// logical partitions at all, not an error.
	emptyExtended := mbrPartition{start: 200000, size: 100000, kind: 0x05}
	got, err = cfdiskReadLogicalChain(file, emptyExtended, 410000)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty container read = (%#v, %v), want (nil, nil)", got, err)
	}
}

func TestCfdiskExtendedRowsAndSlotMutations(t *testing.T) {
	session := &cfdiskSession{
		partitions:  [4]mbrPartition{{start: 2048, size: 40960, kind: 0x83, bootable: true}, cfdiskTestExtended},
		logical:     append([]cfdiskLogical{}, cfdiskTestLogical...),
		diskSectors: 409600,
	}
	rows := session.partitionRows()
	// 4 primary rows + 3 logical; the fixture's third logical partition ends
	// exactly at the container's own end, so there's no trailing free row.
	if len(rows) != 7 {
		t.Fatalf("row count = %d, want 7", len(rows))
	}
	for index := 4; index < 7; index++ {
		if rows[index].kind != cfdiskRowLogical || rows[index].index != index-4 {
			t.Fatalf("row %d = %+v", index, rows[index])
		}
	}

	// Selecting a logical row and deleting it removes just that entry.
	session.selected = 5 // the second logical partition
	session.deletePartition()
	if len(session.logical) != 2 || session.logical[0].partition.start != 45056 || session.logical[1].partition.start != 90112 {
		t.Fatalf("after deleting logical row 5: %+v", session.logical)
	}
	if !session.dirty {
		t.Fatal("deleting a logical partition should mark the session dirty")
	}

	// Toggling the boot flag on a logical row only touches that partition.
	session.selected = 5 // now the former third logical partition
	session.toggleBootable()
	if !session.logical[1].partition.bootable {
		t.Fatal("boot flag was not toggled on the selected logical partition")
	}
	if session.partitions[0].bootable != true { // the primary's own flag is untouched
		t.Fatal("toggling a logical partition's boot flag must not touch a primary's")
	}

	// Deleting the extended partition itself drops every logical partition.
	session.selected = 1
	session.deletePartition()
	if session.partitions[1].size != 0 {
		t.Fatal("extended partition was not deleted")
	}
	if len(session.logical) != 0 {
		t.Fatalf("logical partitions survived deleting their container: %+v", session.logical)
	}
	if !strings.Contains(session.message, "logical partition") {
		t.Fatalf("message should mention the removed logical partitions: %q", session.message)
	}
}

func TestCfdiskChangeTypeGuardsExtendedInvariants(t *testing.T) {
	session := &cfdiskSession{
		partitions:  [4]mbrPartition{{start: 2048, size: 40960, kind: 0x83}, {start: 45000, size: 1000, kind: 0x83}},
		diskSectors: 409600,
	}
	// Turning slot 1 into a second extended partition once slot 0 already
	// is one must be rejected.
	session.partitions[0].kind = 0x05
	session.selected = 1
	withStdin(t, "5\r", func() {
		if err := session.changeType(); err != nil {
			t.Fatal(err)
		}
	})
	if session.partitions[1].kind == 0x05 {
		t.Fatal("a second extended partition was accepted")
	}
	if !strings.Contains(session.message, "already has an extended partition") {
		t.Fatalf("message = %q", session.message)
	}

	// Retyping an extended partition away while it still holds logical
	// partitions must be rejected too.
	session.selected = 0
	session.logical = []cfdiskLogical{{partition: mbrPartition{start: 45056, size: 1000, kind: 0x83}, ebrLBA: 2048}}
	withStdin(t, "83\r", func() {
		if err := session.changeType(); err != nil {
			t.Fatal(err)
		}
	})
	if session.partitions[0].kind != 0x05 {
		t.Fatal("extended partition was retyped away while it still had logical partitions")
	}
	if !strings.Contains(session.message, "Delete its 1 logical partition") {
		t.Fatalf("message = %q", session.message)
	}
}

func TestCfdiskNewLogicalPartitionPrompts(t *testing.T) {
	session := &cfdiskSession{
		options:     cfdiskOptions{sectorSize: 512},
		partitions:  [4]mbrPartition{{start: 2048, size: 40960, kind: 0x83}, cfdiskTestExtended},
		diskSectors: 409600,
		selected:    4, // the (currently empty) logical area
	}
	// Accept the suggested start, but only take part of the offered size so
	// there's still room in the container for a second logical partition.
	withStdin(t, "\r10240\r", func() {
		if err := session.newLogicalPartition(); err != nil {
			t.Fatal(err)
		}
	})
	if len(session.logical) != 1 || session.logical[0].partition.start != 45056 ||
		session.logical[0].partition.size != 10240 || session.logical[0].ebrLBA != 43008 {
		t.Fatalf("logical after create = %+v", session.logical)
	}
	if !session.dirty || session.selected != 4 {
		t.Fatalf("session after create: dirty=%v selected=%d", session.dirty, session.selected)
	}

	// A second logical partition in the space left over, accepting whatever
	// this session now suggests for both start and size.
	session.selected = 5 // the trailing free-space row
	withStdin(t, "\r\r", func() {
		if err := session.newLogicalPartition(); err != nil {
			t.Fatal(err)
		}
	})
	if len(session.logical) != 2 {
		t.Fatalf("logical after second create = %+v", session.logical)
	}
	first, second := session.logical[0].partition, session.logical[1].partition
	if uint64(second.start) <= uint64(first.start)+uint64(first.size) {
		t.Fatalf("second logical partition doesn't start after the first: %+v then %+v", first, second)
	}
	if session.logical[1].ebrLBA < uint64(first.start)+uint64(first.size) {
		t.Fatalf("second boot record at %d overlaps the first logical partition ending at %d",
			session.logical[1].ebrLBA, uint64(first.start)+uint64(first.size))
	}

	if err := validateCfdiskLogicalPartitions(cfdiskTestExtended, session.logical); err != nil {
		t.Fatalf("session-built logical partitions should validate: %v", err)
	}
}

// TestCfdiskExtendedWriteThenReopen exercises the whole feature end to end:
// build a table with an extended partition and two logical partitions in
// memory, write it to a disk image through the normal write() prompt, then
// open a *new* session on that same image and confirm cfdiskReadLogicalChain
// reconstructs an identical layout from the bytes just written.
func TestCfdiskExtendedWriteThenReopen(t *testing.T) {
	image := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(image, make([]byte, 410000*512), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	session := &cfdiskSession{
		options:     cfdiskOptions{device: image},
		file:        opened,
		labelType:   cfdiskLabelDOS,
		sector:      make([]byte, 512),
		diskBytes:   410000 * 512,
		diskSectors: 410000,
		partitions:  [4]mbrPartition{{start: 2048, size: 40960, kind: 0x83, bootable: true}, cfdiskTestExtended},
		logical:     append([]cfdiskLogical{}, cfdiskTestLogical[:2]...),
		dirty:       true,
	}
	withStdin(t, "yes\r", func() {
		if err := session.write(); err != nil {
			t.Fatal(err)
		}
	})
	if session.dirty {
		t.Fatalf("write should clear dirty; message = %q", session.message)
	}

	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	sector := make([]byte, 512)
	if _, err := file.ReadAt(sector, 0); err != nil {
		t.Fatal(err)
	}
	reopened := cfdiskReadPartitions(sector)
	if reopened != session.partitions {
		t.Fatalf("reopened primaries = %#v, want %#v", reopened, session.partitions)
	}
	extIndex := cfdiskFindExtended(reopened)
	if extIndex < 0 {
		t.Fatal("reopened table has no extended partition")
	}
	logical, err := cfdiskReadLogicalChain(file, reopened[extIndex], 410000)
	if err != nil {
		t.Fatal(err)
	}
	if len(logical) != 2 || logical[0].partition.start != 45056 || logical[1].partition.start != 67584 {
		t.Fatalf("reopened logical partitions = %+v", logical)
	}
}
