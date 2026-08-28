// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"sort"
)

// DOS extended/logical partitions: a chain of boot records (EBRs), each
// holding one logical partition entry (start relative to its own boot
// record's LBA) plus a link entry pointing at the next boot record (start
// relative to the *original* extended partition's own start). The layout
// below -- one boot record per logical partition, aligned the same way New
// aligns a primary -- was measured sector-for-sector against a table
// util-linux's fdisk wrote to a disposable image file, not guessed from a
// spec: fdisk 2.41.5 puts the boot record at the start of each gap and the
// logical partition's data at the next 1 MiB boundary after it.

const cfdiskEBRChainLimit = 256 // real disks have far fewer; this only bounds a corrupt or hostile chain.

// cfdiskLogical is one logical partition as ba6 keeps it in memory: the
// partition itself (absolute start/size, like a primary), plus the absolute
// LBA of the boot record that describes it.
type cfdiskLogical struct {
	partition mbrPartition
	ebrLBA    uint64
}

// cfdiskFindExtended returns the index of the disk's one extended primary
// slot, or -1 if there isn't one. validateCfdiskPartitions rejects a second.
func cfdiskFindExtended(partitions [4]mbrPartition) int {
	for index, partition := range partitions {
		if partition.size != 0 && cfdiskExtendedType(partition.kind) {
			return index
		}
	}
	return -1
}

// cfdiskReadLogicalChain walks an extended partition's boot record chain and
// returns its logical partitions in start order. A container with no boot
// record yet (freshly created, still empty) is not an error.
func cfdiskReadLogicalChain(file *os.File, extended mbrPartition, diskSectors uint64) ([]cfdiskLogical, error) {
	containerEnd := uint64(extended.start) + uint64(extended.size)
	var result []cfdiskLogical
	ebrLBA := uint64(extended.start)
	for hop := 0; ; hop++ {
		if hop >= cfdiskEBRChainLimit {
			return nil, fmt.Errorf("extended partition chain is too long or loops")
		}
		if ebrLBA < uint64(extended.start) || ebrLBA >= containerEnd || ebrLBA >= diskSectors {
			return nil, fmt.Errorf("extended partition chain runs outside its container")
		}
		sector := make([]byte, 512)
		//nolint:gosec // G115: ebrLBA is bounded above by containerEnd/diskSectors, both checked against int64 range when the session opened.
		if _, err := file.ReadAt(sector, int64(ebrLBA)*512); err != nil && err != io.EOF {
			return nil, err
		}
		if sector[510] != 0x55 || sector[511] != 0xaa {
			break
		}
		logicalEntry := cfdiskReadEntry(sector, 446)
		linkEntry := cfdiskReadEntry(sector, 462)
		if logicalEntry.size != 0 {
			dataStart := ebrLBA + uint64(logicalEntry.start)
			if dataStart > uint64(^uint32(0)) || dataStart+uint64(logicalEntry.size) > containerEnd {
				return nil, fmt.Errorf("logical partition at boot record %d lies outside the extended partition", ebrLBA)
			}
			result = append(result, cfdiskLogical{
				partition: mbrPartition{
					start:    uint32(dataStart), //nolint:gosec // G115: dataStart is bounded above.
					size:     logicalEntry.size,
					kind:     logicalEntry.kind,
					bootable: logicalEntry.bootable,
				},
				ebrLBA: ebrLBA,
			})
		}
		if linkEntry.size == 0 {
			break
		}
		nextEBR := uint64(extended.start) + uint64(linkEntry.start)
		if nextEBR <= ebrLBA {
			return nil, fmt.Errorf("extended partition chain does not advance")
		}
		ebrLBA = nextEBR
	}
	sort.Slice(result, func(i, j int) bool { return result[i].partition.start < result[j].partition.start })
	return result, nil
}

// cfdiskLogicalFreeRegions finds the gaps inside an extended container, the
// same way cfdiskFreeRegions does for the top-level primary slots, except
// each logical partition's own boot record (not just its data) counts as
// used space.
func cfdiskLogicalFreeRegions(extended mbrPartition, logical []cfdiskLogical) []cfdiskFreeRegion {
	type usedRegion struct{ start, end uint64 }
	used := make([]usedRegion, 0, len(logical))
	for _, entry := range logical {
		used = append(used, usedRegion{start: entry.ebrLBA, end: uint64(entry.partition.start) + uint64(entry.partition.size)})
	}
	sort.Slice(used, func(i, j int) bool { return used[i].start < used[j].start })
	containerEnd := uint64(extended.start) + uint64(extended.size)
	regions := make([]cfdiskFreeRegion, 0, len(used)+1)
	cursor := uint64(extended.start)
	for _, region := range used {
		if cursor < region.start {
			regions = append(regions, cfdiskFreeRegion{start: cursor, size: region.start - cursor})
		}
		cursor = region.end
	}
	if cursor < containerEnd {
		regions = append(regions, cfdiskFreeRegion{start: cursor, size: containerEnd - cursor})
	}
	return regions
}

// cfdiskLogicalRegion is a proposed new logical partition: a boot record at
// ebrLBA, and its data spanning [dataStart, dataStart+dataSize).
type cfdiskLogicalRegion struct {
	ebrLBA              uint64
	dataStart, dataSize uint64
}

// cfdiskSuggestedLogicalRegion picks the largest gap inside the extended
// container and lays out a boot record plus 1-MiB-aligned data inside it,
// mirroring cfdiskSuggestedFreeRegion's alignment for a new primary.
func cfdiskSuggestedLogicalRegion(extended mbrPartition, logical []cfdiskLogical) (cfdiskLogicalRegion, error) {
	var best cfdiskLogicalRegion
	for _, region := range cfdiskLogicalFreeRegions(extended, logical) {
		ebrLBA := region.start
		dataStart := (ebrLBA + 1 + 2047) &^ uint64(2047)
		regionEnd := region.start + region.size
		if dataStart >= regionEnd {
			continue
		}
		candidate := cfdiskLogicalRegion{ebrLBA: ebrLBA, dataStart: dataStart, dataSize: regionEnd - dataStart}
		if candidate.dataSize > best.dataSize {
			best = candidate
		}
	}
	if best.dataSize == 0 {
		return cfdiskLogicalRegion{}, fmt.Errorf("no unallocated sectors remain inside the extended partition")
	}
	return best, nil
}

// validateCfdiskLogicalPartitions checks the same invariants
// validateCfdiskPartitions checks for primaries: a real type, a boot record
// strictly before its own data, data inside the container, and no overlaps.
func validateCfdiskLogicalPartitions(extended mbrPartition, logical []cfdiskLogical) error {
	type rangeWithIndex struct {
		start, end uint64
		index      int
	}
	containerEnd := uint64(extended.start) + uint64(extended.size)
	ranges := make([]rangeWithIndex, 0, len(logical))
	for index, entry := range logical {
		if entry.partition.size == 0 {
			return fmt.Errorf("logical partition %d has no size", index+5)
		}
		if entry.partition.kind == 0 {
			return fmt.Errorf("logical partition %d has no type", index+5)
		}
		if cfdiskExtendedType(entry.partition.kind) {
			return fmt.Errorf("logical partition %d cannot itself be extended", index+5)
		}
		if entry.ebrLBA < uint64(extended.start) || entry.ebrLBA >= uint64(entry.partition.start) {
			return fmt.Errorf("logical partition %d has an invalid boot record location", index+5)
		}
		dataEnd := uint64(entry.partition.start) + uint64(entry.partition.size)
		if dataEnd > containerEnd {
			return fmt.Errorf("logical partition %d lies outside the extended partition", index+5)
		}
		ranges = append(ranges, rangeWithIndex{start: entry.ebrLBA, end: dataEnd, index: index})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	for index := 1; index < len(ranges); index++ {
		if ranges[index].start < ranges[index-1].end {
			return fmt.Errorf("logical partition %d overlaps logical partition %d", ranges[index].index+5, ranges[index-1].index+5)
		}
	}
	return nil
}

// cfdiskRowKind distinguishes what a selectable row in the DOS view
// represents, since rows past the four fixed primary slots don't map onto
// s.partitions at all.
type cfdiskRowKind uint8

const (
	cfdiskRowPrimary cfdiskRowKind = iota
	cfdiskRowLogical
	cfdiskRowLogicalFree
)

// cfdiskRow is one selectable row: the four primary slots always come
// first (as they always have), and -- only when one of them is an extended
// container -- its logical partitions in start order, then a trailing row
// for whatever space is still free inside it. This keeps the "select a
// slot; n creates in the best free region" behaviour the four primary slots
// already had, and gives logical partitions the same real-fdisk numbering
// (5, 6, 7, ...) cfdiskDump and validateCfdiskLogicalPartitions also use.
type cfdiskRow struct {
	kind   cfdiskRowKind
	index  int                 // primary slot index, or index into s.logical
	region cfdiskLogicalRegion // set when kind == cfdiskRowLogicalFree
}

func (s *cfdiskSession) partitionRows() []cfdiskRow {
	rows := make([]cfdiskRow, 4)
	for index := range rows {
		rows[index] = cfdiskRow{kind: cfdiskRowPrimary, index: index}
	}
	extIndex := cfdiskFindExtended(s.partitions)
	if extIndex < 0 {
		return rows
	}
	for index := range s.logical {
		rows = append(rows, cfdiskRow{kind: cfdiskRowLogical, index: index})
	}
	if region, err := cfdiskSuggestedLogicalRegion(s.partitions[extIndex], s.logical); err == nil {
		rows = append(rows, cfdiskRow{kind: cfdiskRowLogicalFree, region: region})
	}
	return rows
}

// selectedRow resolves s.selected against the current row list, clamping
// instead of panicking if the table shrank (a delete or a retype away from
// extended) since the last time the row count was checked.
func (s *cfdiskSession) selectedRow() cfdiskRow {
	rows := s.partitionRows()
	index := s.selected
	if index >= len(rows) {
		index = len(rows) - 1
	}
	return rows[index]
}

// cfdiskEBRWrite is one boot record ready to be written at an absolute LBA.
type cfdiskEBRWrite struct {
	lba    uint64
	sector []byte
}

// cfdiskBuildEBRChain lays out one boot record per logical partition in
// start order: each one's own entry (start relative to its own boot
// record), and -- except for the last -- a link entry pointing at the next
// boot record (start relative to the extended partition's own start, size
// covering that next boot record's whole nested sub-container). This is the
// standard DOS/EBR chain layout; see the file comment for how it was
// verified.
func cfdiskBuildEBRChain(extended mbrPartition, logical []cfdiskLogical) ([]cfdiskEBRWrite, error) {
	if len(logical) == 0 {
		return nil, nil
	}
	sorted := make([]cfdiskLogical, len(logical))
	copy(sorted, logical)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].partition.start < sorted[j].partition.start })
	containerEnd := uint64(extended.start) + uint64(extended.size)
	writes := make([]cfdiskEBRWrite, 0, len(sorted))
	for index, entry := range sorted {
		if entry.ebrLBA >= uint64(entry.partition.start) {
			return nil, fmt.Errorf("logical partition %d has an invalid boot record location", index+5)
		}
		relStart := uint64(entry.partition.start) - entry.ebrLBA
		if relStart > uint64(^uint32(0)) {
			return nil, fmt.Errorf("logical partition %d is too far from its own boot record", index+5)
		}
		var link mbrPartition
		if index+1 < len(sorted) {
			nextEBR := sorted[index+1].ebrLBA
			if nextEBR < uint64(extended.start) {
				return nil, fmt.Errorf("logical partition %d has an invalid boot record location", index+6)
			}
			linkEnd := containerEnd
			if index+2 < len(sorted) {
				linkEnd = sorted[index+2].ebrLBA
			}
			linkStart := nextEBR - uint64(extended.start)
			if linkEnd <= nextEBR || linkStart > uint64(^uint32(0)) || linkEnd-nextEBR > uint64(^uint32(0)) {
				return nil, fmt.Errorf("logical partition %d has an invalid boot record chain", index+6)
			}
			link = mbrPartition{
				start: uint32(linkStart),         //nolint:gosec // G115: bounded above.
				size:  uint32(linkEnd - nextEBR), //nolint:gosec // G115: bounded above.
				kind:  extended.kind,
			}
		}
		table := [4]mbrPartition{
			{
				start:    uint32(relStart), //nolint:gosec // G115: bounded above.
				size:     entry.partition.size,
				kind:     entry.partition.kind,
				bootable: entry.partition.bootable,
			},
			link,
		}
		sector := cfdiskBuildMBR(make([]byte, 512), table)
		writes = append(writes, cfdiskEBRWrite{lba: entry.ebrLBA, sector: sector})
	}
	return writes, nil
}

// writeCfdiskEBRChain writes every boot record before the caller writes the
// primary MBR, the same "dependents before the root pointer" order the GPT
// writer uses for its backup array and header.
func writeCfdiskEBRChain(device string, writes []cfdiskEBRWrite) error {
	if len(writes) == 0 {
		return nil
	}
	file, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, write := range writes {
		//nolint:gosec // G115: lba values come from cfdiskBuildEBRChain, already bounded to the disk.
		count, err := file.WriteAt(write.sector, int64(write.lba)*512)
		if err != nil {
			return err
		}
		if count != len(write.sector) {
			return io.ErrShortWrite
		}
	}
	return file.Sync()
}
