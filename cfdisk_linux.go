// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// cfdisk edits a deliberately bounded DOS/MBR or conventional GPT table. The
// UI changes only an in-memory table; nothing reaches the device until the
// user types "yes" at the Write prompt.
type cfdiskOptions struct {
	device                       string
	readOnly, zero, lock, lockNB bool
	sectorSize                   uint64
	color                        cfdiskColorMode
	help, version                bool
}

type cfdiskColorMode uint8

const (
	cfdiskColorAuto cfdiskColorMode = iota
	cfdiskColorAlways
	cfdiskColorNever
)

type cfdiskLabelKind uint8

const (
	cfdiskLabelNone cfdiskLabelKind = iota
	cfdiskLabelGPT
	cfdiskLabelDOS
)

type cfdiskLabelType struct {
	name string
	kind cfdiskLabelKind
}

var cfdiskLabelTypes = [...]cfdiskLabelType{
	{name: "gpt", kind: cfdiskLabelGPT},
	{name: "dos", kind: cfdiskLabelDOS},
}

const (
	cfdiskGPTLabelIndex     = 0
	cfdiskDOSLabelIndex     = 1
	cfdiskDefaultLabelIndex = cfdiskGPTLabelIndex
)

type cfdiskMenuAction uint8

const (
	cfdiskMenuNew cfdiskMenuAction = iota
	cfdiskMenuQuit
	cfdiskMenuHelp
	cfdiskMenuWrite
	cfdiskMenuDump
)

type cfdiskMenuItem struct {
	label, description string
	action             cfdiskMenuAction
}

var cfdiskMenuItems = [...]cfdiskMenuItem{
	{label: "New", description: "Create a new partition from free space", action: cfdiskMenuNew},
	{label: "Quit", description: "Quit without writing changes", action: cfdiskMenuQuit},
	{label: "Help", description: "Show help", action: cfdiskMenuHelp},
	{label: "Write", description: "Write the partition table to disk", action: cfdiskMenuWrite},
	{label: "Dump", description: "Dump the table as an sfdisk-style script", action: cfdiskMenuDump},
}

func cmdCfdisk(args []string) int {
	options, err := parseCfdiskOptions(args)
	if err != nil {
		fatalf("cfdisk", "%v", err)
		return 1
	}
	if options.help {
		if err := writeAppletHelp(os.Stdout, "cfdisk"); err != nil {
			fatalf("cfdisk", "write error: %v", err)
			return 1
		}
		return 0
	}
	if options.version {
		fmt.Fprintln(os.Stdout, "cfdisk from ba6")
		return 0
	}

	session, err := newCfdiskSession(options)
	if err != nil {
		fatalf("cfdisk", "%s: %v", options.device, err)
		return 1
	}
	defer session.close()
	if !isTerminal(os.Stdin.Fd()) || !isTerminal(os.Stdout.Fd()) {
		fatalf("cfdisk", "standard input and output must be a terminal")
		return 1
	}
	if err := session.run(); err != nil {
		fatalf("cfdisk", "%v", err)
		return 1
	}
	return 0
}

func parseCfdiskOptions(args []string) (cfdiskOptions, error) {
	options := cfdiskOptions{sectorSize: 512, color: cfdiskColorAuto}
	args = expandShortOptions(args, "b")
	parsing := true
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("option %s requires an argument", arg)
			}
			return args[index], nil
		}
		setDevice := func(device string) error {
			if options.device != "" {
				return fmt.Errorf("expected exactly one disk")
			}
			options.device = device
			return nil
		}

		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-h" || arg == "--help"):
			options.help = true
		case parsing && (arg == "-V" || arg == "--version"):
			options.version = true
		case parsing && (arg == "-r" || arg == "--read-only"):
			options.readOnly = true
		case parsing && (arg == "-z" || arg == "--zero"):
			options.zero = true
		case parsing && (arg == "-b" || arg == "--sector-size"):
			parsed, valueErr := value()
			if valueErr != nil {
				return options, valueErr
			}
			if valueErr := setCfdiskSectorSize(&options, parsed); valueErr != nil {
				return options, valueErr
			}
		case parsing && strings.HasPrefix(arg, "--sector-size="):
			if valueErr := setCfdiskSectorSize(&options, strings.TrimPrefix(arg, "--sector-size=")); valueErr != nil {
				return options, valueErr
			}
		case parsing && (arg == "-L" || arg == "--color"):
			options.color = cfdiskColorAuto
		case parsing && strings.HasPrefix(arg, "--color="):
			switch strings.TrimPrefix(arg, "--color=") {
			case "auto":
				options.color = cfdiskColorAuto
			case "always":
				options.color = cfdiskColorAlways
			case "never":
				options.color = cfdiskColorNever
			default:
				return options, fmt.Errorf("invalid color mode %q", strings.TrimPrefix(arg, "--color="))
			}
		case parsing && arg == "--lock":
			options.lock, options.lockNB = true, false
		case parsing && strings.HasPrefix(arg, "--lock="):
			switch strings.TrimPrefix(arg, "--lock=") {
			case "yes", "1":
				options.lock, options.lockNB = true, false
			case "nonblock":
				options.lock, options.lockNB = true, true
			case "no", "0":
				options.lock, options.lockNB = false, false
			default:
				return options, fmt.Errorf("invalid lock mode %q", strings.TrimPrefix(arg, "--lock="))
			}
		case parsing && strings.HasPrefix(arg, "-"):
			return options, fmt.Errorf("unsupported option %q", arg)
		default:
			if err := setDevice(arg); err != nil {
				return options, err
			}
		}
	}
	if options.help || options.version {
		if options.device != "" {
			return options, fmt.Errorf("--help and --version do not take a disk")
		}
		return options, nil
	}
	if options.device == "" {
		return options, fmt.Errorf("missing disk")
	}
	return options, nil
}

func setCfdiskSectorSize(options *cfdiskOptions, value string) error {
	size, err := strconv.ParseUint(value, 10, 64)
	if err != nil || size != 512 {
		return fmt.Errorf("only a 512-byte logical sector size is supported")
	}
	options.sectorSize = size
	return nil
}

type cfdiskSession struct {
	options                           cfdiskOptions
	file                              *os.File
	diskBytes                         uint64
	diskSectors                       uint64
	sector                            []byte
	partitions                        [4]mbrPartition
	logical                           []cfdiskLogical
	labelType                         cfdiskLabelKind
	gpt                               cfdiskGPTTable
	gptOriginal                       cfdiskGPTRawState
	selected, menuSelected            int
	labelSelected                     int
	dirty, help, labelSelector, extra bool
	message                           string
	rows, cols                        int
}

func newCfdiskSession(options cfdiskOptions) (_ *cfdiskSession, err error) {
	file, err := os.Open(options.device)
	if err != nil {
		return nil, err
	}
	keepFile := false
	defer func() {
		if !keepFile {
			_ = file.Close()
		}
	}()
	if options.lock {
		mode := syscall.LOCK_SH
		if !options.readOnly {
			mode = syscall.LOCK_EX
		}
		if options.lockNB {
			mode |= syscall.LOCK_NB
		}
		if lockErr := syscall.Flock(int(file.Fd()), mode); lockErr != nil {
			return nil, fmt.Errorf("cannot lock device: %w", lockErr)
		}
	}
	size, err := deviceSize(file)
	if err != nil {
		return nil, err
	}
	// GPT locations are addressed through int64-backed ReadAt/WriteAt calls.
	// A partial logical sector would make its final backup header ambiguous.
	if size < 1024 || size%512 != 0 || size > ^uint64(0)>>1 {
		return nil, fmt.Errorf("unsupported disk geometry")
	}
	sector := make([]byte, 512)
	if _, err := file.ReadAt(sector, 0); err != nil && err != io.EOF {
		return nil, err
	}
	session := &cfdiskSession{
		options:       options,
		file:          file,
		diskBytes:     size,
		diskSectors:   size / 512,
		sector:        sector,
		labelSelected: cfdiskDefaultLabelIndex,
	}
	if options.zero {
		session.labelSelector = true
		session.message = "Started with an empty in-memory table"
	} else if cfdiskHasProtectiveGPT(sector) {
		table, original, loadErr := cfdiskReadGPT(file, session.diskSectors)
		if loadErr != nil {
			return nil, loadErr
		}
		session.labelType, session.gpt, session.gptOriginal = cfdiskLabelGPT, table, original
	} else if sector[510] == 0x55 && sector[511] == 0xaa {
		session.labelType = cfdiskLabelDOS
		session.partitions = cfdiskReadPartitions(sector)
		if extIndex := cfdiskFindExtended(session.partitions); extIndex >= 0 {
			logical, chainErr := cfdiskReadLogicalChain(file, session.partitions[extIndex], session.diskSectors)
			if chainErr != nil {
				return nil, chainErr
			}
			session.logical = logical
		}
	} else {
		session.labelSelector = true
		session.message = "No recognized partition table found"
	}
	keepFile = true
	return session, nil
}

func (s *cfdiskSession) close() {
	if s.file == nil {
		return
	}
	if s.options.lock {
		_ = syscall.Flock(int(s.file.Fd()), syscall.LOCK_UN)
	}
	_ = s.file.Close()
	s.file = nil
}

func cfdiskHasProtectiveGPT(sector []byte) bool {
	for index := 0; index < 4; index++ {
		if len(sector) >= 446+(index+1)*16 && sector[446+index*16+4] == 0xee {
			return true
		}
	}
	return false
}

func cfdiskReadPartitions(sector []byte) [4]mbrPartition {
	var partitions [4]mbrPartition
	for index := range partitions {
		partitions[index] = cfdiskReadEntry(sector, 446+index*16)
	}
	return partitions
}

// cfdiskReadEntry decodes one 16-byte MBR/EBR partition table entry. It is
// shared by the four primary slots and, in cfdisk_extended_linux.go, the two
// live entries of an extended boot record.
func cfdiskReadEntry(sector []byte, offset int) mbrPartition {
	entry := sector[offset : offset+16]
	partition := mbrPartition{
		start:    binary.LittleEndian.Uint32(entry[8:12]),
		size:     binary.LittleEndian.Uint32(entry[12:16]),
		kind:     entry[4],
		bootable: entry[0] == 0x80,
	}
	if partition.size == 0 {
		return mbrPartition{}
	}
	return partition
}

func (s *cfdiskSession) run() error {
	old, err := terminalRaw(os.Stdin.Fd())
	if err != nil {
		return fmt.Errorf("stdin is not a terminal: %w", err)
	}
	defer func() {
		restoreTerminal(os.Stdin.Fd(), old)
		fmt.Fprint(os.Stdout, "\x1b[?1049l\x1b[?25h")
	}()
	fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25l")
	for {
		s.draw("", "")
		key, readErr := readEditorKey()
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
		exit, handleErr := s.handleKey(key)
		if handleErr != nil {
			return handleErr
		}
		if exit {
			return nil
		}
	}
}

func (s *cfdiskSession) handleKey(key int) (bool, error) {
	if s.help {
		s.help = false
		return false, nil
	}
	if s.labelSelector {
		return s.handleLabelSelectorKey(key)
	}
	switch key {
	case '?', 'h', 'H':
		s.help = true
	case 1000, 'k':
		if s.labelType == cfdiskLabelGPT {
			s.moveGPTSelection(-1)
		} else if s.selected > 0 {
			s.selected--
		}
		s.message = ""
	case 1001, 'j':
		if s.labelType == cfdiskLabelGPT {
			s.moveGPTSelection(1)
		} else if s.selected+1 < len(s.partitionRows()) {
			s.selected++
		}
		s.message = ""
	case 1002:
		s.menuSelected = (s.menuSelected + len(cfdiskMenuItems) - 1) % len(cfdiskMenuItems)
		s.message = ""
	case 1003:
		s.menuSelected = (s.menuSelected + 1) % len(cfdiskMenuItems)
		s.message = ""
	case '\r', '\n':
		return s.activateMenu()
	case 'n', 'N':
		return false, s.newPartition()
	case 'd', 'D':
		s.deletePartition()
	case 'r', 'R':
		return false, s.resizePartition()
	case 's', 'S':
		s.sortPartitions()
	case 't', 'T':
		return false, s.changeType()
	case 'b', 'B':
		s.toggleBootable()
	case 'u', 'U':
		return false, s.dump()
	case 'x', 'X':
		s.extra = !s.extra
		if s.extra {
			s.message = "Extra partition information shown"
		} else {
			s.message = "Extra partition information hidden"
		}
	case 'w', 'W':
		return false, s.write()
	case 'q', 'Q', 3:
		return s.quit()
	}
	return false, nil
}

func (s *cfdiskSession) handleLabelSelectorKey(key int) (bool, error) {
	switch key {
	case 1000, 'k':
		if s.labelSelected > 0 {
			s.labelSelected--
		}
		s.message = ""
	case 1001, 'j':
		if s.labelSelected+1 < len(cfdiskLabelTypes) {
			s.labelSelected++
		}
		s.message = ""
	case '?', 'h', 'H':
		s.help = true
	case '\r', '\n':
		choice := cfdiskLabelTypes[s.labelSelected]
		if s.readOnly("create a partition table") {
			return false, nil
		}
		switch choice.kind {
		case cfdiskLabelDOS:
			s.labelType = cfdiskLabelDOS
			s.partitions = [4]mbrPartition{}
			s.gpt, s.gptOriginal = cfdiskGPTTable{}, cfdiskGPTRawState{}
			s.labelSelector, s.dirty = false, true
			s.message = "Started with an empty DOS/MBR partition table"
		case cfdiskLabelGPT:
			table, original, createErr := cfdiskNewGPT(s.file, s.diskSectors)
			if createErr != nil {
				s.message = "Cannot create GPT: " + createErr.Error()
				return false, nil
			}
			s.labelType, s.gpt, s.gptOriginal = cfdiskLabelGPT, table, original
			s.partitions = [4]mbrPartition{}
			s.labelSelector, s.dirty = false, true
			s.message = "Started with an empty GPT partition table"
		}
	case 'q', 'Q', 3, 27:
		return true, nil
	}
	return false, nil
}

func (s *cfdiskSession) activateMenu() (bool, error) {
	if s.menuSelected < 0 || s.menuSelected >= len(cfdiskMenuItems) {
		s.menuSelected = 0
	}
	switch cfdiskMenuItems[s.menuSelected].action {
	case cfdiskMenuNew:
		return false, s.newPartition()
	case cfdiskMenuQuit:
		return s.quit()
	case cfdiskMenuHelp:
		s.help = true
	case cfdiskMenuWrite:
		return false, s.write()
	case cfdiskMenuDump:
		return false, s.dump()
	}
	return false, nil
}

func (s *cfdiskSession) newPartition() error {
	if s.labelType == cfdiskLabelGPT {
		return s.newGPTPartition()
	}
	if s.readOnly("create a partition") {
		return nil
	}
	if row := s.selectedRow(); row.kind == cfdiskRowLogical || row.kind == cfdiskRowLogicalFree {
		return s.newLogicalPartition()
	}
	slot := s.selected
	if s.partitions[slot].size != 0 {
		slot = -1
		for index, partition := range s.partitions {
			if partition.size == 0 {
				slot = index
				break
			}
		}
	}
	if slot < 0 {
		s.message = "DOS labels have at most four primary partitions"
		return nil
	}
	region, err := cfdiskSuggestedFreeRegion(s.partitions, s.diskSectors)
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
	if start == 0 || start > uint64(^uint32(0)) || size > uint64(^uint32(0)) {
		s.message = "Partition range is outside the DOS/MBR address space"
		return nil
	}
	proposed := s.partitions
	proposed[slot] = mbrPartition{start: uint32(start), size: uint32(size), kind: 0x83} //nolint:gosec // The bounds above make both conversions safe.
	if err := validateCfdiskPartitions(proposed, s.diskSectors); err != nil {
		s.message = "Cannot create partition: " + err.Error()
		return nil
	}
	s.partitions, s.selected, s.dirty = proposed, slot, true
	s.message = fmt.Sprintf("Created partition %d", slot+1)
	return nil
}

// newLogicalPartition creates a logical partition inside the disk's one
// extended container. The boot record always goes at the front of whichever
// free gap the chosen start sector falls into (cfdiskLogicalBootRecordFor),
// mirroring how real fdisk places it -- there is no separate prompt for it.
func (s *cfdiskSession) newLogicalPartition() error {
	extIndex := cfdiskFindExtended(s.partitions)
	if extIndex < 0 {
		s.message = "No extended partition exists yet -- create one and set its type to Extended (t) first"
		return nil
	}
	extended := s.partitions[extIndex]
	suggested, err := cfdiskSuggestedLogicalRegion(extended, s.logical)
	if err != nil {
		s.message = "Cannot create a logical partition: " + err.Error()
		return nil
	}
	startText, ok, err := s.prompt(fmt.Sprintf("Start sector [%d]: ", suggested.dataStart))
	if err != nil || !ok {
		return err
	}
	start := suggested.dataStart
	if startText != "" {
		start, err = parseSectorNumber(startText)
		if err != nil {
			s.message = fmt.Sprintf("Invalid start sector %q", startText)
			return nil
		}
	}
	ebrLBA, findErr := cfdiskLogicalBootRecordFor(extended, s.logical, start)
	if findErr != nil {
		s.message = "Cannot create a logical partition: " + findErr.Error()
		return nil
	}
	defaultSize := uint64(extended.start) + uint64(extended.size) - start
	if start == suggested.dataStart {
		defaultSize = suggested.dataSize
	}
	sizeText, ok, err := s.prompt(fmt.Sprintf("Size in sectors [%d]: ", defaultSize))
	if err != nil || !ok {
		return err
	}
	size := defaultSize
	if sizeText != "" {
		size, err = cfdiskParseSize(sizeText, s.options.sectorSize)
		if err != nil || size == 0 {
			s.message = fmt.Sprintf("Invalid partition size %q", sizeText)
			return nil
		}
	}
	if start == 0 || start > uint64(^uint32(0)) || size > uint64(^uint32(0)) {
		s.message = "Partition range is outside the DOS/MBR address space"
		return nil
	}
	proposed := append(append([]cfdiskLogical{}, s.logical...), cfdiskLogical{
		partition: mbrPartition{start: uint32(start), size: uint32(size), kind: 0x83}, //nolint:gosec // The bounds above make both conversions safe.
		ebrLBA:    ebrLBA,
	})
	if err := validateCfdiskLogicalPartitions(extended, proposed); err != nil {
		s.message = "Cannot create logical partition: " + err.Error()
		return nil
	}
	sort.Slice(proposed, func(i, j int) bool { return proposed[i].partition.start < proposed[j].partition.start })
	s.logical, s.dirty = proposed, true
	s.selected = 4
	for index, entry := range s.logical {
		if uint64(entry.partition.start) == start {
			s.selected = 4 + index
			break
		}
	}
	s.message = fmt.Sprintf("Created logical partition %d", s.selected+1)
	return nil
}

// cfdiskLogicalBootRecordFor finds the free gap containing a chosen logical
// partition start and returns where its boot record belongs: the start of
// that gap, mirroring how fdisk always places a boot record at the front of
// whichever run of free space its logical partition falls into.
func cfdiskLogicalBootRecordFor(extended mbrPartition, logical []cfdiskLogical, start uint64) (uint64, error) {
	for _, region := range cfdiskLogicalFreeRegions(extended, logical) {
		if start > region.start && start < region.start+region.size {
			return region.start, nil
		}
	}
	return 0, fmt.Errorf("start sector %d is not inside a free region of the extended partition", start)
}

func (s *cfdiskSession) deletePartition() {
	if s.labelType == cfdiskLabelGPT {
		s.deleteGPTPartition()
		return
	}
	if s.readOnly("delete a partition") {
		return
	}
	switch row := s.selectedRow(); row.kind {
	case cfdiskRowLogical:
		s.logical = append(s.logical[:row.index], s.logical[row.index+1:]...)
		s.dirty = true
		s.message = fmt.Sprintf("Deleted logical partition %d in memory", row.index+5)
	case cfdiskRowLogicalFree:
		s.message = "Select an existing partition first"
	default:
		if s.partitions[s.selected].size == 0 {
			s.message = fmt.Sprintf("Partition %d is already empty", s.selected+1)
			return
		}
		wasExtended := cfdiskExtendedType(s.partitions[s.selected].kind)
		removedLogical := len(s.logical)
		s.partitions[s.selected] = mbrPartition{}
		if wasExtended {
			s.logical = nil
		}
		s.dirty = true
		if wasExtended && removedLogical > 0 {
			s.message = fmt.Sprintf("Deleted extended partition %d and %d logical partition(s) in memory", s.selected+1, removedLogical)
		} else {
			s.message = fmt.Sprintf("Deleted partition %d in memory", s.selected+1)
		}
	}
}

func (s *cfdiskSession) resizePartition() error {
	if s.labelType == cfdiskLabelGPT {
		return s.resizeGPTPartition()
	}
	if s.readOnly("resize a partition") {
		return nil
	}
	row := s.selectedRow()
	if row.kind == cfdiskRowLogical {
		return s.resizeLogicalPartition(row.index)
	}
	if row.kind == cfdiskRowLogicalFree {
		s.message = "Select an existing partition first"
		return nil
	}
	partition := s.partitions[s.selected]
	if partition.size == 0 {
		s.message = "Select an existing partition first"
		return nil
	}
	value, ok, err := s.prompt(fmt.Sprintf("New size in sectors [%d]: ", partition.size))
	if err != nil || !ok {
		return err
	}
	if value == "" {
		return nil
	}
	size, parseErr := cfdiskParseSize(value, s.options.sectorSize)
	if parseErr != nil || size == 0 || size > uint64(^uint32(0)) {
		s.message = fmt.Sprintf("Invalid partition size %q", value)
		return nil
	}
	proposed := s.partitions
	proposed[s.selected].size = uint32(size) //nolint:gosec // The upper bound is checked above.
	if err := validateCfdiskPartitions(proposed, s.diskSectors); err != nil {
		s.message = "Cannot resize partition: " + err.Error()
		return nil
	}
	if cfdiskExtendedType(proposed[s.selected].kind) {
		if err := validateCfdiskLogicalPartitions(proposed[s.selected], s.logical); err != nil {
			s.message = "Cannot resize extended partition: " + err.Error()
			return nil
		}
	}
	if proposed == s.partitions {
		s.message = fmt.Sprintf("Partition %d size is unchanged", s.selected+1)
		return nil
	}
	s.partitions, s.dirty = proposed, true
	s.message = fmt.Sprintf("Resized partition %d to %d sectors", s.selected+1, size)
	return nil
}

// resizeLogicalPartition resizes a logical partition's data. Its boot
// record's own position is untouched, so this only ever needs to re-check
// bounds and overlap with the other logical partitions.
func (s *cfdiskSession) resizeLogicalPartition(index int) error {
	partition := s.logical[index].partition
	value, ok, err := s.prompt(fmt.Sprintf("New size in sectors [%d]: ", partition.size))
	if err != nil || !ok {
		return err
	}
	if value == "" {
		return nil
	}
	size, parseErr := cfdiskParseSize(value, s.options.sectorSize)
	if parseErr != nil || size == 0 || size > uint64(^uint32(0)) {
		s.message = fmt.Sprintf("Invalid partition size %q", value)
		return nil
	}
	proposed := make([]cfdiskLogical, len(s.logical))
	copy(proposed, s.logical)
	proposed[index].partition.size = uint32(size) //nolint:gosec // The upper bound is checked above.
	extIndex := cfdiskFindExtended(s.partitions)
	if err := validateCfdiskLogicalPartitions(s.partitions[extIndex], proposed); err != nil {
		s.message = "Cannot resize logical partition: " + err.Error()
		return nil
	}
	s.logical, s.dirty = proposed, true
	s.message = fmt.Sprintf("Resized logical partition %d to %d sectors", index+5, size)
	return nil
}

func (s *cfdiskSession) sortPartitions() {
	if s.labelType == cfdiskLabelGPT {
		s.sortGPTPartitions()
		return
	}
	if s.readOnly("sort partitions") {
		return
	}
	if s.selected >= len(s.partitions) {
		// A logical row, or the free-space-in-extended row: sorting only
		// reassigns which of the four *primary* slots holds which
		// partition, so a selection past those four needs no fix-up.
		sorted := cfdiskSortedPartitions(s.partitions)
		if sorted == s.partitions {
			s.message = "Partitions are already ordered by start sector"
			return
		}
		s.partitions, s.dirty = sorted, true
		s.message = "Sorted partition slots by start sector"
		return
	}
	selected := s.partitions[s.selected]
	sorted := cfdiskSortedPartitions(s.partitions)
	if sorted == s.partitions {
		s.message = "Partitions are already ordered by start sector"
		return
	}
	s.partitions = sorted
	s.selected = 0
	if selected.size != 0 {
		for index, partition := range sorted {
			if partition == selected {
				s.selected = index
				break
			}
		}
	}
	s.dirty = true
	s.message = "Sorted partition slots by start sector"
}

func cfdiskSortedPartitions(partitions [4]mbrPartition) [4]mbrPartition {
	ordered := make([]mbrPartition, 0, len(partitions))
	for _, partition := range partitions {
		if partition.size != 0 {
			ordered = append(ordered, partition)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].start < ordered[j].start })
	var sorted [4]mbrPartition
	copy(sorted[:], ordered)
	return sorted
}

func (s *cfdiskSession) dump() error {
	path, ok, err := s.prompt("Dump sfdisk script to file: ")
	if err != nil || !ok {
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		s.message = "Dump cancelled: no output file was given"
		return nil
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		answer, confirmed, promptErr := s.prompt("Overwrite existing dump? Type yes: ")
		if promptErr != nil || !confirmed {
			return promptErr
		}
		if !strings.EqualFold(strings.TrimSpace(answer), "yes") {
			s.message = "Dump cancelled"
			return nil
		}
	} else if !os.IsNotExist(statErr) {
		s.message = "Cannot inspect dump path: " + statErr.Error()
		return nil
	}
	dump := cfdiskDump(s.options.device, s.partitions, s.logical)
	if s.labelType == cfdiskLabelGPT {
		dump = cfdiskGPTDump(s.options.device, s.gpt)
	}
	if err := writeCfdiskDump(path, dump); err != nil {
		s.message = "Dump failed: " + err.Error()
		return nil
	}
	if s.labelType == cfdiskLabelGPT {
		s.message = "Wrote GPT sfdisk-style dump to " + path
	} else {
		s.message = "Wrote sfdisk-compatible dump to " + path
	}
	return nil
}

func cfdiskDump(device string, partitions [4]mbrPartition, logical []cfdiskLogical) string {
	var dump strings.Builder
	dump.WriteString("label: dos\nunit: sectors\n")
	for index, partition := range partitions {
		if partition.size == 0 {
			continue
		}
		fmt.Fprintf(&dump, "%s : start=%d, size=%d, type=%02x", partitionName(device, index+1), partition.start, partition.size, partition.kind)
		if partition.bootable {
			dump.WriteString(", bootable")
		}
		dump.WriteByte('\n')
	}
	for index, entry := range logical {
		partition := entry.partition
		fmt.Fprintf(&dump, "%s : start=%d, size=%d, type=%02x", partitionName(device, index+5), partition.start, partition.size, partition.kind)
		if partition.bootable {
			dump.WriteString(", bootable")
		}
		dump.WriteByte('\n')
	}
	return dump.String()
}

func writeCfdiskDump(path, dump string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.WriteString(file, dump)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (s *cfdiskSession) changeType() error {
	if s.labelType == cfdiskLabelGPT {
		return s.changeGPTType()
	}
	if s.readOnly("change a partition type") {
		return nil
	}
	row := s.selectedRow()
	if row.kind == cfdiskRowLogicalFree {
		s.message = "Select an existing partition first"
		return nil
	}
	if row.kind == cfdiskRowLogical {
		return s.changeLogicalType(row.index)
	}
	partition := &s.partitions[s.selected]
	if partition.size == 0 {
		s.message = "Select an existing partition first"
		return nil
	}
	value, ok, err := s.prompt(fmt.Sprintf("Hex type [%02x]: ", partition.kind))
	if err != nil || !ok {
		return err
	}
	if value == "" {
		return nil
	}
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x")
	kind, parseErr := strconv.ParseUint(value, 16, 8)
	if parseErr != nil || kind == 0 {
		s.message = fmt.Sprintf("Unsupported partition type %q", value)
		return nil
	}
	wasExtended, willBeExtended := cfdiskExtendedType(partition.kind), cfdiskExtendedType(byte(kind))
	if willBeExtended && !wasExtended && cfdiskFindExtended(s.partitions) >= 0 {
		s.message = "Partition table already has an extended partition"
		return nil
	}
	if wasExtended && !willBeExtended && len(s.logical) > 0 {
		s.message = fmt.Sprintf("Delete its %d logical partition(s) first", len(s.logical))
		return nil
	}
	partition.kind = byte(kind)
	s.dirty = true
	s.message = fmt.Sprintf("Changed partition %d type to %02x", s.selected+1, kind)
	return nil
}

// changeLogicalType sets a logical partition's type. It cannot itself become
// extended: DOS only nests one level of extended/logical partitions.
func (s *cfdiskSession) changeLogicalType(index int) error {
	partition := &s.logical[index].partition
	value, ok, err := s.prompt(fmt.Sprintf("Hex type [%02x]: ", partition.kind))
	if err != nil || !ok {
		return err
	}
	if value == "" {
		return nil
	}
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x")
	kind, parseErr := strconv.ParseUint(value, 16, 8)
	if parseErr != nil || kind == 0 || cfdiskExtendedType(byte(kind)) {
		s.message = fmt.Sprintf("Unsupported partition type %q", value)
		return nil
	}
	partition.kind = byte(kind)
	s.dirty = true
	s.message = fmt.Sprintf("Changed logical partition %d type to %02x", index+5, kind)
	return nil
}

func (s *cfdiskSession) toggleBootable() {
	if s.labelType == cfdiskLabelGPT {
		s.message = "GPT partitions do not have an MBR boot flag"
		return
	}
	if s.readOnly("toggle the boot flag") {
		return
	}
	switch row := s.selectedRow(); row.kind {
	case cfdiskRowLogical:
		partition := &s.logical[row.index].partition
		partition.bootable = !partition.bootable
		s.dirty = true
		s.message = fmt.Sprintf("Logical partition %d boot flag is now %v", row.index+5, partition.bootable)
	case cfdiskRowLogicalFree:
		s.message = "Select an existing partition first"
	default:
		partition := &s.partitions[s.selected]
		if partition.size == 0 {
			s.message = "Select an existing partition first"
			return
		}
		partition.bootable = !partition.bootable
		s.dirty = true
		s.message = fmt.Sprintf("Partition %d boot flag is now %v", s.selected+1, partition.bootable)
	}
}

func (s *cfdiskSession) write() error {
	if s.labelType == cfdiskLabelGPT {
		return s.writeGPT()
	}
	if s.readOnly("write the partition table") {
		return nil
	}
	if !s.dirty {
		s.message = "No changes to write"
		return nil
	}
	if err := validateCfdiskPartitions(s.partitions, s.diskSectors); err != nil {
		s.message = "Cannot write partition table: " + err.Error()
		return nil
	}
	if extIndex := cfdiskFindExtended(s.partitions); extIndex >= 0 {
		if err := validateCfdiskLogicalPartitions(s.partitions[extIndex], s.logical); err != nil {
			s.message = "Cannot write partition table: " + err.Error()
			return nil
		}
	}
	if err := s.verifyWriteTarget(); err != nil {
		s.message = "Cannot write partition table: " + err.Error()
		return nil
	}
	answer, ok, err := s.prompt("Write table to disk? Type yes: ")
	if err != nil || !ok {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		s.message = "Write cancelled"
		return nil
	}
	// The user can spend arbitrary time at the confirmation prompt, so check
	// again immediately before opening the write descriptor.
	if err := s.verifyWriteTarget(); err != nil {
		s.message = "Cannot write partition table: " + err.Error()
		return nil
	}
	if extIndex := cfdiskFindExtended(s.partitions); extIndex >= 0 {
		writes, chainErr := cfdiskBuildEBRChain(s.partitions[extIndex], s.logical)
		if chainErr != nil {
			s.message = "Cannot write partition table: " + chainErr.Error()
			return nil
		}
		// Boot records are written before the primary MBR: if a crash
		// interrupts the write, the extended partition's own entry (written
		// last, below) never points at a chain that wasn't fully committed.
		if err := writeCfdiskEBRChain(s.options.device, writes); err != nil {
			s.message = "Write failed: " + err.Error()
			return nil
		}
	}
	sector := cfdiskBuildMBR(s.sector, s.partitions)
	if err := writeCfdiskMBR(s.options.device, sector); err != nil {
		s.message = "Write failed: " + err.Error()
		return nil
	}
	s.sector, s.dirty = sector, false
	s.message = "Partition table written; ask the kernel to reread it if needed"
	return nil
}

// verifyWriteTarget prevents a staged table from overwriting a changed device.
// The advisory lock protects cooperative editors; the size and sector checks
// also protect against an uncooperative writer or a resized image.
func (s *cfdiskSession) verifyWriteTarget() error {
	if s.labelType == cfdiskLabelGPT {
		return s.verifyGPTWriteTarget()
	}
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
	current := make([]byte, 512)
	if _, err := s.file.ReadAt(current, 0); err != nil && err != io.EOF {
		return err
	}
	if !bytes.Equal(current, s.sector) {
		return fmt.Errorf("partition table changed; reopen the device")
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

func (s *cfdiskSession) quit() (bool, error) {
	if !s.dirty {
		return true, nil
	}
	answer, ok, err := s.prompt("Discard unsaved changes? Type yes: ")
	if err != nil || !ok {
		return false, err
	}
	if strings.EqualFold(strings.TrimSpace(answer), "yes") {
		return true, nil
	}
	s.message = "Changes kept in memory"
	return false, nil
}

func (s *cfdiskSession) readOnly(action string) bool {
	if !s.options.readOnly {
		return false
	}
	s.message = "Read-only mode cannot " + action
	return true
}

type cfdiskFreeRegion struct {
	start, size uint64
}

// cfdiskParseSize accepts sectors by default and the common binary size
// suffixes accepted by cfdisk's New and Resize prompts. Keeping conversion in
// one place ensures a requested byte size is exactly representable in sectors.
func cfdiskParseSize(value string, sectorSize uint64) (uint64, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "+")
	if value == "" || sectorSize == 0 {
		return 0, fmt.Errorf("invalid size")
	}
	lower := strings.ToLower(value)
	units := []struct {
		suffix string
		bytes  uint64
	}{
		{"tib", 1 << 40}, {"ti", 1 << 40}, {"tb", 1 << 40}, {"t", 1 << 40},
		{"gib", 1 << 30}, {"gi", 1 << 30}, {"gb", 1 << 30}, {"g", 1 << 30},
		{"mib", 1 << 20}, {"mi", 1 << 20}, {"mb", 1 << 20}, {"m", 1 << 20},
		{"kib", 1 << 10}, {"ki", 1 << 10}, {"kb", 1 << 10}, {"k", 1 << 10},
	}
	for _, unit := range units {
		if !strings.HasSuffix(lower, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(value[:len(value)-len(unit.suffix)])
		parsed, err := strconv.ParseUint(number, 0, 64)
		if err != nil || parsed > ^uint64(0)/unit.bytes {
			return 0, fmt.Errorf("invalid size")
		}
		bytes := parsed * unit.bytes
		if bytes%sectorSize != 0 {
			return 0, fmt.Errorf("size is not a whole sector")
		}
		return bytes / sectorSize, nil
	}
	return strconv.ParseUint(value, 0, 64)
}

func cfdiskSuggestedFreeRegion(partitions [4]mbrPartition, sectors uint64) (cfdiskFreeRegion, error) {
	regions, err := cfdiskFreeRegions(partitions, sectors)
	if err != nil {
		return cfdiskFreeRegion{}, err
	}
	best := cfdiskFreeRegion{}
	for _, region := range regions {
		start := region.start
		// Use 1 MiB alignment whenever it leaves usable space in the gap. The
		// fallback preserves small rescue images and tightly packed old tables.
		aligned := (start + 2047) &^ uint64(2047)
		if aligned >= 2048 && aligned < start+region.size {
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

func cfdiskFreeRegions(partitions [4]mbrPartition, sectors uint64) ([]cfdiskFreeRegion, error) {
	if err := validateCfdiskPartitions(partitions, sectors); err != nil {
		return nil, err
	}
	type usedRegion struct{ start, end uint64 }
	used := make([]usedRegion, 0, len(partitions))
	for _, partition := range partitions {
		if partition.size != 0 {
			used = append(used, usedRegion{start: uint64(partition.start), end: uint64(partition.start) + uint64(partition.size)})
		}
	}
	sort.Slice(used, func(i, j int) bool { return used[i].start < used[j].start })
	regions := make([]cfdiskFreeRegion, 0, len(used)+1)
	cursor := uint64(1) // LBA 0 contains the MBR itself.
	for _, region := range used {
		if cursor < region.start {
			regions = append(regions, cfdiskFreeRegion{start: cursor, size: region.start - cursor})
		}
		cursor = region.end
	}
	if cursor < sectors {
		regions = append(regions, cfdiskFreeRegion{start: cursor, size: sectors - cursor})
	}
	return regions, nil
}

func validateCfdiskPartitions(partitions [4]mbrPartition, sectors uint64) error {
	type rangeWithIndex struct {
		start, end uint64
		index      int
	}
	ranges := make([]rangeWithIndex, 0, len(partitions))
	extendedSeen := false
	for index, partition := range partitions {
		if partition.size == 0 {
			continue
		}
		if partition.start == 0 {
			return fmt.Errorf("partition %d starts at sector 0", index+1)
		}
		if partition.kind == 0 {
			return fmt.Errorf("partition %d has no type", index+1)
		}
		if cfdiskExtendedType(partition.kind) {
			if extendedSeen {
				return fmt.Errorf("partition %d is extended, but partition table already has one", index+1)
			}
			extendedSeen = true
		}
		end := uint64(partition.start) + uint64(partition.size)
		if end > sectors {
			return fmt.Errorf("partition %d lies outside the disk", index+1)
		}
		ranges = append(ranges, rangeWithIndex{start: uint64(partition.start), end: end, index: index})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	for index := 1; index < len(ranges); index++ {
		if ranges[index].start < ranges[index-1].end {
			return fmt.Errorf("partition %d overlaps partition %d", ranges[index].index+1, ranges[index-1].index+1)
		}
	}
	return nil
}

func cfdiskExtendedType(kind byte) bool {
	return kind == 0x05 || kind == 0x0f || kind == 0x85
}

func cfdiskBuildMBR(original []byte, partitions [4]mbrPartition) []byte {
	sector := make([]byte, 512)
	copy(sector, original)
	for index := 446; index < 510; index++ {
		sector[index] = 0
	}
	for index, partition := range partitions {
		if partition.size == 0 {
			continue
		}
		entry := sector[446+index*16 : 446+(index+1)*16]
		if partition.bootable {
			entry[0] = 0x80
		}
		// CHS is intentionally saturated. Linux and modern firmware use the
		// validated LBA fields, just as the sfdisk applet does.
		entry[1], entry[2], entry[3] = 0xfe, 0xff, 0xff
		entry[4] = partition.kind
		entry[5], entry[6], entry[7] = 0xfe, 0xff, 0xff
		binary.LittleEndian.PutUint32(entry[8:12], partition.start)
		binary.LittleEndian.PutUint32(entry[12:16], partition.size)
	}
	sector[510], sector[511] = 0x55, 0xaa
	return sector
}

func writeCfdiskMBR(device string, sector []byte) error {
	if len(sector) != 512 {
		return fmt.Errorf("internal error: MBR sector is %d bytes", len(sector))
	}
	file, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	count, err := file.WriteAt(sector, 0)
	if err == nil && count != len(sector) {
		err = io.ErrShortWrite
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

func (s *cfdiskSession) draw(prompt, input string) {
	rows, cols, ok := terminalDimensions(os.Stdout.Fd())
	if !ok {
		rows, cols = 24, 80
	}
	s.rows, s.cols = rows, cols
	lines := s.lines()
	if s.help {
		lines = cfdiskHelpLines()
	}
	normalScreen := prompt == "" && !s.help && !s.labelSelector
	footerRows := 1
	if normalScreen {
		footerRows = 2
	}
	bodyRows := rows - footerRows
	if bodyRows < 1 {
		bodyRows = 1
	}
	var screen strings.Builder
	screen.WriteString("\x1b[?25l\x1b[?7l\x1b[H\x1b[2J")
	for row := 0; row < bodyRows; row++ {
		line := ""
		if row < len(lines) {
			line = lines[row]
		}
		fmt.Fprintf(&screen, "\x1b[%d;1H%s", row+1, cfdiskPad(line, cols))
	}
	if prompt != "" {
		s.drawFooter(&screen, rows, cols, prompt+input, true)
	} else if s.help {
		s.drawFooter(&screen, rows, cols, "Press any key to return.", false)
	} else if s.labelSelector {
		footer := "Use Up/Down to select a label, Enter to choose, q to quit"
		if s.message != "" {
			footer = s.message + " — " + footer
		}
		s.drawFooter(&screen, rows, cols, footer, false)
		s.drawLabelSelector(&screen, rows, cols)
	} else {
		footer := s.message
		if footer == "" {
			footer = s.selectedMenuItem().description
		}
		s.drawFooter(&screen, rows-1, cols, footer, false)
		s.drawActionBar(&screen, rows, cols)
	}
	if prompt != "" {
		screen.WriteString("\x1b[?25h")
	}
	screen.WriteString("\x1b[?7h")
	fmt.Fprint(os.Stdout, screen.String())
}

func (s *cfdiskSession) drawFooter(screen *strings.Builder, row, cols int, text string, reverse bool) {
	text = cfdiskPad(text, cols)
	fmt.Fprintf(screen, "\x1b[%d;1H", row)
	if reverse && s.colorEnabled() {
		fmt.Fprintf(screen, "\x1b[7m%s\x1b[m", text)
		return
	}
	screen.WriteString(text)
}

func (s *cfdiskSession) selectedMenuItem() cfdiskMenuItem {
	if s.menuSelected < 0 || s.menuSelected >= len(cfdiskMenuItems) {
		return cfdiskMenuItems[0]
	}
	return cfdiskMenuItems[s.menuSelected]
}

func (s *cfdiskSession) drawActionBar(screen *strings.Builder, row, cols int) {
	fmt.Fprintf(screen, "\x1b[%d;1H%s", row, s.actionBar(cols))
}

func (s *cfdiskSession) actionBar(cols int) string {
	if cols <= 0 {
		return ""
	}
	buttons := make([]string, len(cfdiskMenuItems))
	visibleWidth := 2 * (len(cfdiskMenuItems) - 1)
	for index, item := range cfdiskMenuItems {
		button, width := s.menuButton(item.label, index == s.menuSelected)
		buttons[index] = button
		visibleWidth += width
	}
	if visibleWidth > cols {
		button, width := s.menuButton(s.selectedMenuItem().label, true)
		padding := (cols - width) / 2
		if padding < 0 {
			padding = 0
		}
		return strings.Repeat(" ", padding) + button
	}
	padding := (cols - visibleWidth) / 2
	return strings.Repeat(" ", padding) + strings.Join(buttons, "  ")
}

func (s *cfdiskSession) menuButton(label string, selected bool) (string, int) {
	plain := "[ " + label + " ]"
	if !selected {
		return plain, len(plain)
	}
	if s.colorEnabled() {
		return "\x1b[7m" + plain + "\x1b[m", len(plain)
	}
	plain = "> " + label + " <"
	return plain, len(plain)
}

func (s *cfdiskSession) drawLabelSelector(screen *strings.Builder, rows, cols int) {
	innerWidth := 34
	if cols-2 < innerWidth {
		innerWidth = cols - 2
	}
	if innerWidth < 8 || rows < len(cfdiskLabelTypes)+2 {
		return
	}
	height := len(cfdiskLabelTypes) + 2
	top := (rows-height)/2 + 1
	if top < 1 {
		top = 1
	}
	left := (cols - innerWidth - 2) / 2
	if left < 0 {
		left = 0
	}
	left++ // ANSI cursor columns are one-based.
	title := " Select label type "
	if len(title) > innerWidth {
		title = title[:innerWidth]
	}
	fmt.Fprintf(screen, "\x1b[%d;%dH+%s+", top, left, title+strings.Repeat("-", innerWidth-len(title)))
	for index, choice := range cfdiskLabelTypes {
		text := "  " + choice.name
		selected := index == s.labelSelected
		if selected && !s.colorEnabled() {
			text = "> " + strings.TrimPrefix(text, "  ")
		}
		text = cfdiskPad(text, innerWidth)
		fmt.Fprintf(screen, "\x1b[%d;%dH|", top+index+1, left)
		if selected && s.colorEnabled() {
			fmt.Fprintf(screen, "\x1b[7m%s\x1b[m", text)
		} else {
			screen.WriteString(text)
		}
		screen.WriteByte('|')
	}
	fmt.Fprintf(screen, "\x1b[%d;%dH+%s+", top+height-1, left, strings.Repeat("-", innerWidth))
}

func (s *cfdiskSession) colorEnabled() bool {
	return s.options.color != cfdiskColorNever
}

func (s *cfdiskSession) lines() []string {
	if s.labelType == cfdiskLabelGPT {
		return s.gptLines()
	}
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
		"",
		"    # Boot      Start        End    Sectors    Size Type",
	}
	rows := s.partitionRows()
	for index, partition := range s.partitions {
		marker := " "
		if index == s.selected {
			marker = ">"
		}
		if partition.size == 0 {
			lines = append(lines, fmt.Sprintf("%s %2d  <empty>", marker, index+1))
			continue
		}
		boot := " "
		if partition.bootable {
			boot = "*"
		}
		end := uint64(partition.start) + uint64(partition.size) - 1
		lines = append(lines, fmt.Sprintf("%s %2d   %s %10d %10d %10d %7s %02x %s", marker, index+1, boot,
			partition.start, end, partition.size, humanSizeUint64(uint64(partition.size)*512), partition.kind,
			cfdiskTypeName(partition.kind)))
	}
	for rowIndex := 4; rowIndex < len(rows); rowIndex++ {
		row := rows[rowIndex]
		marker := " "
		if rowIndex == s.selected {
			marker = ">"
		}
		if row.kind == cfdiskRowLogicalFree {
			lines = append(lines, fmt.Sprintf("%s  -   %10s %10s %10d %7s     Free space",
				marker, "", "", row.region.dataSize, humanSizeUint64(row.region.dataSize*512)))
			continue
		}
		partition := s.logical[row.index].partition
		boot := " "
		if partition.bootable {
			boot = "*"
		}
		end := uint64(partition.start) + uint64(partition.size) - 1
		lines = append(lines, fmt.Sprintf("%s %2d   %s %10d %10d %10d %7s %02x %s", marker, row.index+5, boot,
			partition.start, end, partition.size, humanSizeUint64(uint64(partition.size)*512), partition.kind,
			cfdiskTypeName(partition.kind)))
	}
	if regions, err := cfdiskFreeRegions(s.partitions, s.diskSectors); err == nil {
		var free uint64
		for _, region := range regions {
			free += region.size
		}
		lines = append(lines, "", fmt.Sprintf(" Free space: %s in %d region(s)", humanSizeUint64(free*512), len(regions)))
	} else {
		lines = append(lines, "", " Current layout is invalid: "+err.Error())
	}
	if s.extra {
		lines = append(lines, fmt.Sprintf(" MBR disk identifier: 0x%08x  Label: dos  Primary slots: 4",
			binary.LittleEndian.Uint32(s.sector[440:444])))
		selected := rows[0]
		if s.selected < len(rows) {
			selected = rows[s.selected]
		}
		switch selected.kind {
		case cfdiskRowPrimary:
			if partition := s.partitions[s.selected]; partition.size != 0 {
				end := uint64(partition.start) + uint64(partition.size) - 1
				lines = append(lines, fmt.Sprintf(" Selected %d: LBA %d-%d, %d sectors, type 0x%02x (%s)",
					s.selected+1, partition.start, end, partition.size, partition.kind, cfdiskTypeName(partition.kind)))
			}
		case cfdiskRowLogical:
			partition := s.logical[selected.index].partition
			end := uint64(partition.start) + uint64(partition.size) - 1
			lines = append(lines, fmt.Sprintf(" Selected %d: LBA %d-%d, %d sectors, type 0x%02x (%s)",
				selected.index+5, partition.start, end, partition.size, partition.kind, cfdiskTypeName(partition.kind)))
		}
	}
	return lines
}

func cfdiskHelpLines() []string {
	return []string{
		" cfdisk (ba6) help",
		"",
		" Up/Down or j/k  select an MBR slot or a visible GPT partition entry",
		" Left/Right      select New, Quit, Help, Write, or Dump on the action bar",
		" Enter           invoke the selected action-bar item",
		" n               create a Linux-filesystem partition in free space",
		" d               delete the selected partition from the in-memory table",
		" r               resize the selected partition with validated bounds",
		" s               sort partition slots by their start sector",
		" t               set an MBR hex type, or a GPT linux/swap/efi/GUID type",
		" b               toggle an MBR boot flag (GPT has no such flag)",
		" u               write the in-memory table as an sfdisk-style script",
		" x               toggle extra disk and selected-partition information",
		" W or w          validate and write after typing yes",
		" q               quit; modified tables require a discard confirmation",
		"",
		" An unlabeled disk opens a GPT/DOS selector with GPT selected. GPT uses",
		" 512-byte-sector, 128-entry layout; DOS supports four primary partitions",
		" plus one extended partition (t sets a primary's type to 5) holding any",
		" number of logical partitions. SGI and SUN labels are not supported.",
		" It never writes until the Write confirmation, and refuses mounted or",
		" active-swap disks. Commands may be entered in upper or lower case.",
		" K/M/G/T and KiB/MiB suffixes are accepted for New and Resize sizes.",
		"",
		" Press any key to return.",
	}
}

func cfdiskTypeName(kind byte) string {
	switch kind {
	case 0x01, 0x04, 0x06:
		return "FAT"
	case 0x07:
		return "HPFS/NTFS/exFAT"
	case 0x0b, 0x0c:
		return "W95 FAT32"
	case 0x05:
		return "Extended"
	case 0x0f:
		return "W95 Ext'd (LBA)"
	case 0x85:
		return "Linux extended"
	case 0x82:
		return "Linux swap"
	case 0x83:
		return "Linux filesystem"
	case 0x8e:
		return "Linux LVM"
	case 0xef:
		return "EFI (FAT-12/16/32)"
	default:
		return "Unknown"
	}
}

func (s *cfdiskSession) prompt(label string) (string, bool, error) {
	input := ""
	for {
		s.draw(label, input)
		key, err := readEditorKey()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", false, nil
			}
			return "", false, err
		}
		switch key {
		case '\r', '\n':
			return input, true, nil
		case 3, 27:
			return "", false, nil
		case 8, 127:
			if len(input) > 0 {
				input = input[:len(input)-1]
			}
		default:
			if key >= 32 && key < 127 && len(input) < 64 {
				input += string(rune(key))
			}
		}
	}
}

func cfdiskPad(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = cfdiskDisplay(text)
	if len(text) > width {
		return text[:width]
	}
	return text + strings.Repeat(" ", width-len(text))
}

// cfdiskDisplay prevents a disk pathname or kernel error from injecting
// terminal-control bytes into the full-screen interface.
func cfdiskDisplay(text string) string {
	return strings.Map(func(character rune) rune {
		if character < 32 || character == 127 {
			return '?'
		}
		return character
	}, text)
}
