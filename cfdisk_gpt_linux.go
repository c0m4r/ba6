// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf16"
)

// cfdisk supports the conventional GPT geometry created by util-linux: 128
// 128-byte entries at LBAs 2-33, mirrored immediately before the backup
// header. Restricting the writable representation to that ubiquitous layout
// lets every byte, header and checksum be independently validated before any
// write reaches a disk.
const (
	cfdiskGPTSectorSize   = uint64(512)
	cfdiskGPTHeaderSize   = 92
	cfdiskGPTEntryCount   = 128
	cfdiskGPTEntrySize    = 128
	cfdiskGPTEntrySectors = cfdiskGPTEntryCount * cfdiskGPTEntrySize / cfdiskGPTSectorSize
)

var (
	cfdiskGPTLinuxFilesystemType = [16]byte{0xaf, 0x3d, 0xc6, 0x0f, 0x83, 0x84, 0x72, 0x47, 0x8e, 0x79, 0x3d, 0x69, 0xd8, 0x47, 0x7d, 0xe4}
	cfdiskGPTLinuxSwapType       = [16]byte{0x6d, 0xfd, 0x57, 0x06, 0xab, 0xa4, 0xc4, 0x43, 0x84, 0xe5, 0x09, 0x33, 0xc8, 0x4b, 0x4f, 0x4f}
	cfdiskGPTEFISystemType       = [16]byte{0x28, 0x73, 0x2a, 0xc1, 0x1f, 0xf8, 0xd2, 0x11, 0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b}
)

type cfdiskGPTPartition struct {
	typeGUID, uniqueGUID [16]byte
	start, end           uint64 // Inclusive LBAs, as stored by GPT.
	attributes           uint64
	name                 string
}

type cfdiskGPTTable struct {
	diskGUID                [16]byte
	firstUsable, lastUsable uint64
	partitions              [cfdiskGPTEntryCount]cfdiskGPTPartition
}

type cfdiskGPTRawState struct {
	protectiveMBR, primaryHeader, primaryEntries []byte
	backupEntries, backupHeader                  []byte
}

func (state cfdiskGPTRawState) equal(other cfdiskGPTRawState) bool {
	return bytes.Equal(state.protectiveMBR, other.protectiveMBR) &&
		bytes.Equal(state.primaryHeader, other.primaryHeader) &&
		bytes.Equal(state.primaryEntries, other.primaryEntries) &&
		bytes.Equal(state.backupEntries, other.backupEntries) &&
		bytes.Equal(state.backupHeader, other.backupHeader)
}

func cfdiskGPTGeometry(sectors uint64) (firstUsable, lastUsable, backupEntries, backupHeader uint64, err error) {
	// LBA 0 is the protective MBR, LBA 1 the primary header, and LBAs
	// 2..33 the primary entries. The backup arrangement mirrors those at the
	// end of the device, leaving at least one usable LBA between them.
	if sectors < 2*cfdiskGPTEntrySectors+4 {
		return 0, 0, 0, 0, fmt.Errorf("disk is too small for a GPT partition table")
	}
	firstUsable = 2 + cfdiskGPTEntrySectors
	backupHeader = sectors - 1
	backupEntries = backupHeader - cfdiskGPTEntrySectors
	lastUsable = backupEntries - 1
	if firstUsable > lastUsable {
		return 0, 0, 0, 0, fmt.Errorf("disk has no usable GPT sectors")
	}
	return firstUsable, lastUsable, backupEntries, backupHeader, nil
}

func cfdiskNewGPT(file *os.File, sectors uint64) (cfdiskGPTTable, cfdiskGPTRawState, error) {
	if file == nil {
		return cfdiskGPTTable{}, cfdiskGPTRawState{}, fmt.Errorf("device is closed")
	}
	firstUsable, lastUsable, _, _, err := cfdiskGPTGeometry(sectors)
	if err != nil {
		return cfdiskGPTTable{}, cfdiskGPTRawState{}, err
	}
	diskGUID, err := cfdiskRandomGPTGUID()
	if err != nil {
		return cfdiskGPTTable{}, cfdiskGPTRawState{}, err
	}
	original, err := cfdiskReadGPTRawState(file, sectors)
	if err != nil {
		return cfdiskGPTTable{}, cfdiskGPTRawState{}, err
	}
	return cfdiskGPTTable{diskGUID: diskGUID, firstUsable: firstUsable, lastUsable: lastUsable}, original, nil
}

func cfdiskReadGPT(file *os.File, sectors uint64) (cfdiskGPTTable, cfdiskGPTRawState, error) {
	state, err := cfdiskReadGPTRawState(file, sectors)
	if err != nil {
		return cfdiskGPTTable{}, cfdiskGPTRawState{}, err
	}
	table, err := cfdiskGPTTableFromRawState(state, sectors)
	if err != nil {
		return cfdiskGPTTable{}, cfdiskGPTRawState{}, err
	}
	return table, state, nil
}

// cfdiskGPTTableFromRawState validates a complete, conventional GPT pair and
// converts it to the representation the editor can safely mutate. It is also
// used immediately before a write so an invalid generated state can never be
// sent to the device by a future caller.
func cfdiskGPTTableFromRawState(state cfdiskGPTRawState, sectors uint64) (cfdiskGPTTable, error) {
	firstUsable, lastUsable, backupEntriesLBA, backupHeaderLBA, err := cfdiskGPTGeometry(sectors)
	if err != nil {
		return cfdiskGPTTable{}, err
	}
	if len(state.protectiveMBR) != 512 || state.protectiveMBR[510] != 0x55 || state.protectiveMBR[511] != 0xaa ||
		state.protectiveMBR[446+4] != 0xee {
		return cfdiskGPTTable{}, fmt.Errorf("invalid GPT protective MBR")
	}
	primary, err := cfdiskParseGPTHeader(state.primaryHeader, 1, backupHeaderLBA, 2, firstUsable, lastUsable, state.primaryEntries)
	if err != nil {
		return cfdiskGPTTable{}, fmt.Errorf("primary GPT header: %w", err)
	}
	backup, err := cfdiskParseGPTHeader(state.backupHeader, backupHeaderLBA, 1, backupEntriesLBA, firstUsable, lastUsable, state.backupEntries)
	if err != nil {
		return cfdiskGPTTable{}, fmt.Errorf("backup GPT header: %w", err)
	}
	if primary.diskGUID != backup.diskGUID || primary.entryCRC != backup.entryCRC || !bytes.Equal(state.primaryEntries, state.backupEntries) {
		return cfdiskGPTTable{}, fmt.Errorf("primary and backup GPT data disagree")
	}
	table := cfdiskGPTTable{diskGUID: primary.diskGUID, firstUsable: firstUsable, lastUsable: lastUsable}
	for index := 0; index < cfdiskGPTEntryCount; index++ {
		entry := state.primaryEntries[index*cfdiskGPTEntrySize : (index+1)*cfdiskGPTEntrySize]
		if allZero(entry) {
			continue
		}
		if allZero(entry[:16]) {
			return cfdiskGPTTable{}, fmt.Errorf("GPT entry %d is unused but contains data", index+1)
		}
		partition := cfdiskGPTPartition{
			start:      binary.LittleEndian.Uint64(entry[32:40]),
			end:        binary.LittleEndian.Uint64(entry[40:48]),
			attributes: binary.LittleEndian.Uint64(entry[48:56]),
			name:       decodeGPTName(entry[56:]),
		}
		copy(partition.typeGUID[:], entry[:16])
		copy(partition.uniqueGUID[:], entry[16:32])
		table.partitions[index] = partition
	}
	if err := validateCfdiskGPTTable(table); err != nil {
		return cfdiskGPTTable{}, err
	}
	return table, nil
}

type cfdiskGPTHeader struct {
	diskGUID [16]byte
	entryCRC uint32
}

func cfdiskParseGPTHeader(header []byte, current, backup, entriesLBA, firstUsable, lastUsable uint64, entries []byte) (cfdiskGPTHeader, error) {
	if len(header) != int(cfdiskGPTSectorSize) {
		return cfdiskGPTHeader{}, fmt.Errorf("invalid sector size")
	}
	if string(header[:8]) != "EFI PART" || binary.LittleEndian.Uint32(header[8:12]) != 0x00010000 ||
		binary.LittleEndian.Uint32(header[12:16]) != cfdiskGPTHeaderSize {
		return cfdiskGPTHeader{}, fmt.Errorf("unsupported GPT header")
	}
	copyHeader := append([]byte(nil), header[:cfdiskGPTHeaderSize]...)
	wantCRC := binary.LittleEndian.Uint32(copyHeader[16:20])
	for index := 16; index < 20; index++ {
		copyHeader[index] = 0
	}
	if crc32.ChecksumIEEE(copyHeader) != wantCRC {
		return cfdiskGPTHeader{}, fmt.Errorf("invalid checksum")
	}
	if binary.LittleEndian.Uint64(header[24:32]) != current || binary.LittleEndian.Uint64(header[32:40]) != backup ||
		binary.LittleEndian.Uint64(header[40:48]) != firstUsable || binary.LittleEndian.Uint64(header[48:56]) != lastUsable ||
		binary.LittleEndian.Uint64(header[72:80]) != entriesLBA || binary.LittleEndian.Uint32(header[80:84]) != cfdiskGPTEntryCount ||
		binary.LittleEndian.Uint32(header[84:88]) != cfdiskGPTEntrySize {
		return cfdiskGPTHeader{}, fmt.Errorf("unsupported GPT geometry")
	}
	if len(entries) != cfdiskGPTEntryCount*cfdiskGPTEntrySize || crc32.ChecksumIEEE(entries) != binary.LittleEndian.Uint32(header[88:92]) {
		return cfdiskGPTHeader{}, fmt.Errorf("invalid partition-entry checksum")
	}
	var parsed cfdiskGPTHeader
	copy(parsed.diskGUID[:], header[56:72])
	if allZero(parsed.diskGUID[:]) {
		return cfdiskGPTHeader{}, fmt.Errorf("missing disk GUID")
	}
	parsed.entryCRC = binary.LittleEndian.Uint32(header[88:92])
	return parsed, nil
}

func cfdiskReadGPTRawState(file *os.File, sectors uint64) (cfdiskGPTRawState, error) {
	_, _, backupEntriesLBA, backupHeaderLBA, err := cfdiskGPTGeometry(sectors)
	if err != nil {
		return cfdiskGPTRawState{}, err
	}
	entryBytes := cfdiskGPTEntryCount * cfdiskGPTEntrySize
	state := cfdiskGPTRawState{}
	if state.protectiveMBR, err = cfdiskReadGPTBytes(file, 0, 512); err != nil {
		return cfdiskGPTRawState{}, err
	}
	if state.primaryHeader, err = cfdiskReadGPTBytes(file, 1, 512); err != nil {
		return cfdiskGPTRawState{}, err
	}
	if state.primaryEntries, err = cfdiskReadGPTBytes(file, 2, entryBytes); err != nil {
		return cfdiskGPTRawState{}, err
	}
	if state.backupEntries, err = cfdiskReadGPTBytes(file, backupEntriesLBA, entryBytes); err != nil {
		return cfdiskGPTRawState{}, err
	}
	if state.backupHeader, err = cfdiskReadGPTBytes(file, backupHeaderLBA, 512); err != nil {
		return cfdiskGPTRawState{}, err
	}
	return state, nil
}

func cfdiskReadGPTBytes(file *os.File, lba uint64, length int) ([]byte, error) {
	const maxSignedOffset = ^uint64(0) >> 1
	if lba > maxSignedOffset/cfdiskGPTSectorSize || uint64(length) > maxSignedOffset-lba*cfdiskGPTSectorSize { //nolint:gosec // G115: length is always a non-negative caller-supplied constant.
		return nil, fmt.Errorf("GPT data lies beyond addressable file offsets")
	}
	data := make([]byte, length)
	count, err := file.ReadAt(data, int64(lba*cfdiskGPTSectorSize)) //nolint:gosec // Bounds above keep the offset in int64 range.
	if err != nil && (!errors.Is(err, io.EOF) || count != len(data)) {
		return nil, err
	}
	if count != len(data) {
		return nil, io.ErrUnexpectedEOF
	}
	return data, nil
}

func cfdiskRandomGPTGUID() ([16]byte, error) {
	var guid [16]byte
	if _, err := rand.Read(guid[:]); err != nil {
		return guid, err
	}
	// RFC 4122 version 4 / variant bits. The first three UUID fields are
	// little-endian in GPT storage, hence the version nibble is byte 7.
	guid[7] = (guid[7] & 0x0f) | 0x40
	guid[8] = (guid[8] & 0x3f) | 0x80
	return guid, nil
}

func gptPartitionEmpty(partition cfdiskGPTPartition) bool {
	return allZero(partition.typeGUID[:])
}

func validateCfdiskGPTTable(table cfdiskGPTTable) error {
	if table.firstUsable == 0 || table.lastUsable < table.firstUsable || allZero(table.diskGUID[:]) {
		return fmt.Errorf("invalid GPT geometry")
	}
	type partitionRange struct {
		start, end uint64
		index      int
	}
	ranges := make([]partitionRange, 0, len(table.partitions))
	seenGUIDs := make(map[[16]byte]int)
	for index, partition := range table.partitions {
		if gptPartitionEmpty(partition) {
			continue
		}
		if allZero(partition.uniqueGUID[:]) {
			return fmt.Errorf("GPT partition %d has no unique GUID", index+1)
		}
		if previous, found := seenGUIDs[partition.uniqueGUID]; found {
			return fmt.Errorf("GPT partitions %d and %d have the same unique GUID", previous+1, index+1)
		}
		seenGUIDs[partition.uniqueGUID] = index
		if partition.start < table.firstUsable || partition.end < partition.start || partition.end > table.lastUsable {
			return fmt.Errorf("GPT partition %d lies outside the usable range", index+1)
		}
		ranges = append(ranges, partitionRange{start: partition.start, end: partition.end, index: index})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	for index := 1; index < len(ranges); index++ {
		if ranges[index].start <= ranges[index-1].end {
			return fmt.Errorf("GPT partition %d overlaps partition %d", ranges[index].index+1, ranges[index-1].index+1)
		}
	}
	return nil
}

func cfdiskGPTFreeRegions(table cfdiskGPTTable) ([]cfdiskFreeRegion, error) {
	if err := validateCfdiskGPTTable(table); err != nil {
		return nil, err
	}
	type usedRegion struct{ start, end uint64 }
	used := make([]usedRegion, 0, len(table.partitions))
	for _, partition := range table.partitions {
		if !gptPartitionEmpty(partition) {
			used = append(used, usedRegion{start: partition.start, end: partition.end})
		}
	}
	sort.Slice(used, func(i, j int) bool { return used[i].start < used[j].start })
	regions := make([]cfdiskFreeRegion, 0, len(used)+1)
	cursor := table.firstUsable
	for _, region := range used {
		if cursor < region.start {
			regions = append(regions, cfdiskFreeRegion{start: cursor, size: region.start - cursor})
		}
		cursor = region.end + 1
	}
	if cursor <= table.lastUsable {
		regions = append(regions, cfdiskFreeRegion{start: cursor, size: table.lastUsable - cursor + 1})
	}
	return regions, nil
}

func cfdiskSuggestedGPTFreeRegion(table cfdiskGPTTable) (cfdiskFreeRegion, error) {
	regions, err := cfdiskGPTFreeRegions(table)
	if err != nil {
		return cfdiskFreeRegion{}, err
	}
	best := cfdiskFreeRegion{}
	for _, region := range regions {
		start := region.start
		aligned := (start + 2047) &^ uint64(2047)
		if aligned >= table.firstUsable && aligned < start+region.size {
			start = aligned
		}
		candidate := cfdiskFreeRegion{start: start, size: region.start + region.size - start}
		if candidate.size > best.size {
			best = candidate
		}
	}
	if best.size == 0 {
		return cfdiskFreeRegion{}, fmt.Errorf("no unallocated sectors remain")
	}
	return best, nil
}

func (s *cfdiskSession) gptVisibleSlots() []int {
	slots := make([]int, 0, len(s.gpt.partitions))
	emptySlot := -1
	for index, partition := range s.gpt.partitions {
		if !gptPartitionEmpty(partition) {
			slots = append(slots, index)
			continue
		}
		if emptySlot < 0 {
			emptySlot = index
		}
	}
	if s.selected >= 0 && s.selected < len(s.gpt.partitions) && gptPartitionEmpty(s.gpt.partitions[s.selected]) {
		emptySlot = s.selected
	}
	if emptySlot >= 0 {
		slots = append(slots, emptySlot)
	}
	sort.Ints(slots)
	return slots
}

func (s *cfdiskSession) moveGPTSelection(delta int) {
	slots := s.gptVisibleSlots()
	if len(slots) == 0 {
		return
	}
	position := -1
	for index, slot := range slots {
		if slot == s.selected {
			position = index
			break
		}
	}
	if position < 0 {
		s.selected = slots[0]
		return
	}
	position += delta
	if position < 0 {
		position = 0
	}
	if position >= len(slots) {
		position = len(slots) - 1
	}
	s.selected = slots[position]
}

func (s *cfdiskSession) newGPTPartition() error {
	if s.readOnly("create a partition") {
		return nil
	}
	slot := s.selected
	if slot < 0 || slot >= len(s.gpt.partitions) || !gptPartitionEmpty(s.gpt.partitions[slot]) {
		slot = -1
		for index, partition := range s.gpt.partitions {
			if gptPartitionEmpty(partition) {
				slot = index
				break
			}
		}
	}
	if slot < 0 {
		s.message = "GPT has no unused partition entries"
		return nil
	}
	region, err := cfdiskSuggestedGPTFreeRegion(s.gpt)
	if err != nil {
		s.message = "Cannot create a partition: " + err.Error()
		return nil
	}
	startText, ok, err := s.prompt(fmt.Sprintf("Start sector [%d]: ", region.start))
	if err != nil || !ok {
		return err
	}
	start := region.start
	if startText != "" {
		start, err = parseSectorNumber(startText)
		if err != nil {
			s.message = fmt.Sprintf("Invalid start sector %q", startText)
			return nil
		}
	}
	sizeText, ok, err := s.prompt(fmt.Sprintf("Size in sectors [%d]: ", region.size))
	if err != nil || !ok {
		return err
	}
	size := region.size
	if sizeText != "" {
		size, err = cfdiskParseSize(sizeText, s.options.sectorSize)
		if err != nil || size == 0 {
			s.message = fmt.Sprintf("Invalid partition size %q", sizeText)
			return nil
		}
	}
	if start == 0 || size == 0 || start > ^uint64(0)-size+1 {
		s.message = "Partition range is outside the GPT address space"
		return nil
	}
	uniqueGUID, err := cfdiskRandomGPTGUID()
	if err != nil {
		return err
	}
	proposed := s.gpt
	proposed.partitions[slot] = cfdiskGPTPartition{
		typeGUID:   cfdiskGPTLinuxFilesystemType,
		uniqueGUID: uniqueGUID,
		start:      start,
		end:        start + size - 1,
		name:       "Linux filesystem",
	}
	if err := validateCfdiskGPTTable(proposed); err != nil {
		s.message = "Cannot create partition: " + err.Error()
		return nil
	}
	s.gpt, s.selected, s.dirty = proposed, slot, true
	s.message = fmt.Sprintf("Created GPT partition %d", slot+1)
	return nil
}

func (s *cfdiskSession) deleteGPTPartition() {
	if s.readOnly("delete a partition") {
		return
	}
	if s.selected < 0 || s.selected >= len(s.gpt.partitions) || gptPartitionEmpty(s.gpt.partitions[s.selected]) {
		s.message = "Select an existing GPT partition first"
		return
	}
	s.gpt.partitions[s.selected] = cfdiskGPTPartition{}
	s.dirty = true
	s.message = fmt.Sprintf("Deleted GPT partition %d in memory", s.selected+1)
}

func (s *cfdiskSession) resizeGPTPartition() error {
	if s.readOnly("resize a partition") {
		return nil
	}
	if s.selected < 0 || s.selected >= len(s.gpt.partitions) || gptPartitionEmpty(s.gpt.partitions[s.selected]) {
		s.message = "Select an existing GPT partition first"
		return nil
	}
	partition := s.gpt.partitions[s.selected]
	oldSize := partition.end - partition.start + 1
	value, ok, err := s.prompt(fmt.Sprintf("New size in sectors [%d]: ", oldSize))
	if err != nil || !ok {
		return err
	}
	if value == "" {
		return nil
	}
	size, parseErr := cfdiskParseSize(value, s.options.sectorSize)
	if parseErr != nil || size == 0 || partition.start > ^uint64(0)-size+1 {
		s.message = fmt.Sprintf("Invalid partition size %q", value)
		return nil
	}
	proposed := s.gpt
	proposed.partitions[s.selected].end = partition.start + size - 1
	if err := validateCfdiskGPTTable(proposed); err != nil {
		s.message = "Cannot resize partition: " + err.Error()
		return nil
	}
	if proposed == s.gpt {
		s.message = fmt.Sprintf("GPT partition %d size is unchanged", s.selected+1)
		return nil
	}
	s.gpt, s.dirty = proposed, true
	s.message = fmt.Sprintf("Resized GPT partition %d to %d sectors", s.selected+1, size)
	return nil
}

func (s *cfdiskSession) sortGPTPartitions() {
	if s.readOnly("sort partitions") {
		return
	}
	selected := cfdiskGPTPartition{}
	if s.selected >= 0 && s.selected < len(s.gpt.partitions) {
		selected = s.gpt.partitions[s.selected]
	}
	ordered := make([]cfdiskGPTPartition, 0, len(s.gpt.partitions))
	for _, partition := range s.gpt.partitions {
		if !gptPartitionEmpty(partition) {
			ordered = append(ordered, partition)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].start < ordered[j].start })
	proposed := s.gpt
	proposed.partitions = [cfdiskGPTEntryCount]cfdiskGPTPartition{}
	copy(proposed.partitions[:], ordered)
	if proposed == s.gpt {
		s.message = "GPT partition entries are already ordered by start sector"
		return
	}
	s.selected = 0
	if !gptPartitionEmpty(selected) {
		for index, partition := range proposed.partitions {
			if partition.uniqueGUID == selected.uniqueGUID {
				s.selected = index
				break
			}
		}
	}
	s.gpt, s.dirty = proposed, true
	s.message = "Sorted GPT partition entries by start sector"
}

func (s *cfdiskSession) changeGPTType() error {
	if s.readOnly("change a partition type") {
		return nil
	}
	if s.selected < 0 || s.selected >= len(s.gpt.partitions) || gptPartitionEmpty(s.gpt.partitions[s.selected]) {
		s.message = "Select an existing GPT partition first"
		return nil
	}
	partition := s.gpt.partitions[s.selected]
	value, ok, err := s.prompt(fmt.Sprintf("GPT type [%s]: ", gptTypeName(partition.typeGUID[:])))
	if err != nil || !ok {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return nil
	}
	typeGUID, parseErr := cfdiskParseGPTType(value)
	if parseErr != nil {
		s.message = fmt.Sprintf("Invalid GPT partition type %q", value)
		return nil
	}
	s.gpt.partitions[s.selected].typeGUID = typeGUID
	s.dirty = true
	s.message = fmt.Sprintf("Changed GPT partition %d type to %s", s.selected+1, gptTypeName(typeGUID[:]))
	return nil
}

func cfdiskParseGPTType(value string) ([16]byte, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "linux", "linux filesystem", "0fc63daf-8483-4772-8e79-3d69d8477de4":
		return cfdiskGPTLinuxFilesystemType, nil
	case "swap", "linux swap", "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f":
		return cfdiskGPTLinuxSwapType, nil
	case "efi", "efi system", "c12a7328-f81f-11d2-ba4b-00a0c93ec93b":
		return cfdiskGPTEFISystemType, nil
	default:
		guid, err := cfdiskParseGPTGUID(value)
		if err != nil {
			return guid, err
		}
		if allZero(guid[:]) {
			return guid, fmt.Errorf("GPT partition type cannot be empty")
		}
		return guid, nil
	}
}

func cfdiskParseGPTGUID(value string) ([16]byte, error) {
	var raw [16]byte
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		return raw, fmt.Errorf("invalid GUID")
	}
	encoded := strings.Join(parts, "")
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != 16 {
		return raw, fmt.Errorf("invalid GUID")
	}
	// GPT stores the first three UUID fields in little-endian byte order.
	raw[0], raw[1], raw[2], raw[3] = decoded[3], decoded[2], decoded[1], decoded[0]
	raw[4], raw[5] = decoded[5], decoded[4]
	raw[6], raw[7] = decoded[7], decoded[6]
	copy(raw[8:], decoded[8:])
	return raw, nil
}

func cfdiskGPTDump(device string, table cfdiskGPTTable) string {
	var dump strings.Builder
	fmt.Fprintf(&dump, "label: gpt\nunit: sectors\nfirst-lba: %d\nlast-lba: %d\n", table.firstUsable, table.lastUsable)
	for index, partition := range table.partitions {
		if gptPartitionEmpty(partition) {
			continue
		}
		fmt.Fprintf(&dump, "%s : start=%d, size=%d, type=%s, uuid=%s", partitionName(device, index+1), partition.start,
			partition.end-partition.start+1, formatGPTGUID(partition.typeGUID[:]), formatGPTGUID(partition.uniqueGUID[:]))
		if partition.name != "" {
			fmt.Fprintf(&dump, ", name=%q", partition.name)
		}
		if partition.attributes != 0 {
			fmt.Fprintf(&dump, ", attrs=0x%x", partition.attributes)
		}
		dump.WriteByte('\n')
	}
	return dump.String()
}

func (s *cfdiskSession) verifyGPTWriteTarget() error {
	if s.file == nil {
		return fmt.Errorf("device is closed")
	}
	size, err := deviceSize(s.file)
	if err != nil {
		return err
	}
	if size != s.diskBytes {
		return fmt.Errorf("disk size changed; reopen the device")
	}
	current, err := cfdiskReadGPTRawState(s.file, s.diskSectors)
	if err != nil {
		return err
	}
	if !current.equal(s.gptOriginal) {
		return fmt.Errorf("GPT data changed; reopen the device")
	}
	inUse, err := pathIsInUse(s.options.device)
	if err != nil {
		return fmt.Errorf("cannot verify mount status: %w", err)
	}
	if inUse {
		return fmt.Errorf("disk is mounted or active swap")
	}
	return nil
}

func (s *cfdiskSession) writeGPT() error {
	if s.readOnly("write the GPT") {
		return nil
	}
	if !s.dirty {
		s.message = "No changes to write"
		return nil
	}
	if err := validateCfdiskGPTTable(s.gpt); err != nil {
		s.message = "Cannot write GPT: " + err.Error()
		return nil
	}
	if err := s.verifyGPTWriteTarget(); err != nil {
		s.message = "Cannot write GPT: " + err.Error()
		return nil
	}
	answer, ok, err := s.prompt("Write GPT to disk? Type yes: ")
	if err != nil || !ok {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		s.message = "Write cancelled"
		return nil
	}
	if err := s.verifyGPTWriteTarget(); err != nil {
		s.message = "Cannot write GPT: " + err.Error()
		return nil
	}
	state, err := cfdiskBuildGPT(s.sector, s.diskSectors, s.gpt)
	if err != nil {
		s.message = "Cannot write GPT: " + err.Error()
		return nil
	}
	if err := writeCfdiskGPT(s.options.device, s.diskSectors, state); err != nil {
		s.message = "Write failed: " + err.Error()
		return nil
	}
	s.sector, s.gptOriginal, s.dirty = state.protectiveMBR, state, false
	s.message = "GPT written; ask the kernel to reread it if needed"
	return nil
}

func cfdiskBuildGPT(originalMBR []byte, sectors uint64, table cfdiskGPTTable) (cfdiskGPTRawState, error) {
	firstUsable, lastUsable, backupEntriesLBA, backupHeaderLBA, err := cfdiskGPTGeometry(sectors)
	if err != nil {
		return cfdiskGPTRawState{}, err
	}
	if table.firstUsable != firstUsable || table.lastUsable != lastUsable {
		return cfdiskGPTRawState{}, fmt.Errorf("GPT geometry changed")
	}
	if err := validateCfdiskGPTTable(table); err != nil {
		return cfdiskGPTRawState{}, err
	}
	entries := make([]byte, cfdiskGPTEntryCount*cfdiskGPTEntrySize)
	for index, partition := range table.partitions {
		if gptPartitionEmpty(partition) {
			continue
		}
		entry := entries[index*cfdiskGPTEntrySize : (index+1)*cfdiskGPTEntrySize]
		copy(entry[:16], partition.typeGUID[:])
		copy(entry[16:32], partition.uniqueGUID[:])
		binary.LittleEndian.PutUint64(entry[32:40], partition.start)
		binary.LittleEndian.PutUint64(entry[40:48], partition.end)
		binary.LittleEndian.PutUint64(entry[48:56], partition.attributes)
		cfdiskEncodeGPTName(entry[56:], partition.name)
	}
	entryCRC := crc32.ChecksumIEEE(entries)
	state := cfdiskGPTRawState{
		protectiveMBR:  cfdiskBuildProtectiveMBR(originalMBR, sectors),
		primaryEntries: append([]byte(nil), entries...),
		backupEntries:  append([]byte(nil), entries...),
	}
	state.primaryHeader = cfdiskBuildGPTHeader(1, backupHeaderLBA, 2, firstUsable, lastUsable, table.diskGUID, entryCRC)
	state.backupHeader = cfdiskBuildGPTHeader(backupHeaderLBA, 1, backupEntriesLBA, firstUsable, lastUsable, table.diskGUID, entryCRC)
	return state, nil
}

func cfdiskBuildProtectiveMBR(original []byte, sectors uint64) []byte {
	mbr := make([]byte, 512)
	copy(mbr, original)
	for index := 446; index < 510; index++ {
		mbr[index] = 0
	}
	entry := mbr[446:462]
	entry[1], entry[2], entry[3] = 0xfe, 0xff, 0xff
	entry[4] = 0xee
	entry[5], entry[6], entry[7] = 0xfe, 0xff, 0xff
	binary.LittleEndian.PutUint32(entry[8:12], 1)
	length := sectors - 1
	if length > uint64(^uint32(0)) {
		length = uint64(^uint32(0))
	}
	binary.LittleEndian.PutUint32(entry[12:16], uint32(length)) //nolint:gosec // Length was bounded above.
	mbr[510], mbr[511] = 0x55, 0xaa
	return mbr
}

func cfdiskBuildGPTHeader(current, backup, entriesLBA, firstUsable, lastUsable uint64, diskGUID [16]byte, entryCRC uint32) []byte {
	header := make([]byte, cfdiskGPTSectorSize)
	copy(header[:8], "EFI PART")
	binary.LittleEndian.PutUint32(header[8:12], 0x00010000)
	binary.LittleEndian.PutUint32(header[12:16], cfdiskGPTHeaderSize)
	binary.LittleEndian.PutUint64(header[24:32], current)
	binary.LittleEndian.PutUint64(header[32:40], backup)
	binary.LittleEndian.PutUint64(header[40:48], firstUsable)
	binary.LittleEndian.PutUint64(header[48:56], lastUsable)
	copy(header[56:72], diskGUID[:])
	binary.LittleEndian.PutUint64(header[72:80], entriesLBA)
	binary.LittleEndian.PutUint32(header[80:84], cfdiskGPTEntryCount)
	binary.LittleEndian.PutUint32(header[84:88], cfdiskGPTEntrySize)
	binary.LittleEndian.PutUint32(header[88:92], entryCRC)
	binary.LittleEndian.PutUint32(header[16:20], crc32.ChecksumIEEE(header[:cfdiskGPTHeaderSize]))
	return header
}

func cfdiskEncodeGPTName(destination []byte, name string) {
	for index := range destination {
		destination[index] = 0
	}
	units := utf16.Encode([]rune(name))
	if len(units) > len(destination)/2 {
		units = units[:len(destination)/2]
	}
	for index, unit := range units {
		binary.LittleEndian.PutUint16(destination[index*2:index*2+2], unit)
	}
}

func writeCfdiskGPT(device string, sectors uint64, state cfdiskGPTRawState) error {
	_, _, backupEntriesLBA, backupHeaderLBA, err := cfdiskGPTGeometry(sectors)
	if err != nil {
		return err
	}
	if len(state.protectiveMBR) != 512 || len(state.primaryHeader) != 512 || len(state.backupHeader) != 512 ||
		len(state.primaryEntries) != cfdiskGPTEntryCount*cfdiskGPTEntrySize || len(state.backupEntries) != cfdiskGPTEntryCount*cfdiskGPTEntrySize {
		return fmt.Errorf("invalid generated GPT data")
	}
	if _, err := cfdiskGPTTableFromRawState(state, sectors); err != nil {
		return fmt.Errorf("invalid generated GPT data: %w", err)
	}
	file, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	write := func(lba uint64, data []byte) error {
		if lba > (^uint64(0)>>1)/cfdiskGPTSectorSize {
			return fmt.Errorf("GPT data lies beyond addressable file offsets")
		}
		count, writeErr := file.WriteAt(data, int64(lba*cfdiskGPTSectorSize)) //nolint:gosec // The bound above keeps the offset in int64 range.
		if writeErr == nil && count != len(data) {
			writeErr = io.ErrShortWrite
		}
		return writeErr
	}
	// Write and sync the backup first. During replacement of the primary array,
	// the newly complete backup remains a valid recovery source.
	if err = write(backupEntriesLBA, state.backupEntries); err == nil {
		err = write(backupHeaderLBA, state.backupHeader)
	}
	if err == nil {
		err = file.Sync()
	}
	if err == nil {
		err = write(2, state.primaryEntries)
	}
	if err == nil {
		err = write(1, state.primaryHeader)
	}
	if err == nil {
		err = write(0, state.protectiveMBR)
	}
	if err == nil {
		err = file.Sync()
	}
	info, statErr := file.Stat()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if statErr == nil && isBlockDevice(info.Mode()) {
		if reread, openErr := os.Open(device); openErr == nil {
			_ = ioctlNoArg(reread.Fd(), blkRereadPT)
			_ = reread.Close()
		}
	}
	return nil
}

func (s *cfdiskSession) gptLines() []string {
	state := "read-write"
	if s.options.readOnly {
		state = "read-only"
	}
	if s.dirty {
		state += ", modified"
	}
	lines := []string{
		" cfdisk (ba6)  " + s.options.device,
		fmt.Sprintf(" Disk: %s  Size: %s  Sectors: %d (512 bytes)  %s", s.options.device,
			humanSizeUint64(s.diskBytes), s.diskSectors, state),
		fmt.Sprintf(" Label: gpt  Disk identifier: %s", formatGPTGUID(s.gpt.diskGUID[:])),
		"",
		"    #       Start          End      Sectors    Size Type                 Name",
	}
	for _, index := range s.gptVisibleSlots() {
		partition := s.gpt.partitions[index]
		marker := " "
		if index == s.selected {
			marker = ">"
		}
		if gptPartitionEmpty(partition) {
			lines = append(lines, fmt.Sprintf("%s %2d  <empty entry>", marker, index+1))
			continue
		}
		size := partition.end - partition.start + 1
		lines = append(lines, fmt.Sprintf("%s %2d %11d %12d %12d %7s %-20s %s", marker, index+1,
			partition.start, partition.end, size, humanSizeUint64(size*cfdiskGPTSectorSize),
			cfdiskShortGPTType(partition.typeGUID), cfdiskGPTDisplayName(partition.name)))
	}
	if regions, err := cfdiskGPTFreeRegions(s.gpt); err == nil {
		var free uint64
		for _, region := range regions {
			free += region.size
		}
		lines = append(lines, "", fmt.Sprintf(" Free space: %s in %d region(s)", humanSizeUint64(free*cfdiskGPTSectorSize), len(regions)))
	} else {
		lines = append(lines, "", " Current layout is invalid: "+err.Error())
	}
	if s.extra {
		lines = append(lines, fmt.Sprintf(" GPT usable LBAs: %d-%d  Entries: %d x %d bytes", s.gpt.firstUsable,
			s.gpt.lastUsable, cfdiskGPTEntryCount, cfdiskGPTEntrySize))
		if s.selected >= 0 && s.selected < len(s.gpt.partitions) {
			partition := s.gpt.partitions[s.selected]
			if !gptPartitionEmpty(partition) {
				lines = append(lines, fmt.Sprintf(" Selected %d: type %s  UUID %s  attributes 0x%x", s.selected+1,
					formatGPTGUID(partition.typeGUID[:]), formatGPTGUID(partition.uniqueGUID[:]), partition.attributes))
			}
		}
	}
	return lines
}

func cfdiskShortGPTType(guid [16]byte) string {
	name := gptTypeName(guid[:])
	if len(name) > 20 {
		return formatGPTGUID(guid[:])[:8]
	}
	return name
}

// GPT names are supplied by the on-disk table. Do not let control characters
// in one turn a full-screen editor redraw into terminal escape sequences.
func cfdiskGPTDisplayName(name string) string {
	return strings.Map(func(runeValue rune) rune {
		if runeValue < ' ' || runeValue == 0x7f {
			return '?'
		}
		return runeValue
	}, name)
}
