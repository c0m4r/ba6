//go:build linux

package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	blkROSet     = 0x125d
	blkROGet     = 0x125e
	blkRereadPT  = 0x125f
	blkFlushBuf  = 0x1261
	blkRASet     = 0x1262
	blkRAGet     = 0x1263
	blkSSZGet    = 0x1268
	blkBSZGet    = 0x80081270
	blkGetSize64 = 0x80081272
)

func ioctlPointer(fd uintptr, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value)) //nolint:gosec // Linux ioctl ABI requires a typed userspace pointer.
	if errno != 0 {
		return errno
	}
	return nil
}

func ioctlNoArg(fd uintptr, request uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func cmdBlockdev(args []string) int {
	if len(args) == 0 {
		fatalf("blockdev", "missing operation and device")
		return 1
	}
	if args[0] == "--report" {
		if len(args) < 2 {
			fatalf("blockdev", "--report requires a device")
			return 1
		}
		fmt.Fprintln(os.Stdout, "RO    RA   SSZ   BSZ        SIZE DEVICE")
		status := 0
		for _, path := range args[1:] {
			file, err := os.Open(path)
			if err != nil {
				fatalf("blockdev", "%s: %v", path, err)
				status = 1
				continue
			}
			ro, ra, ssz, bsz, size, err := blockDeviceValues(file)
			_ = file.Close()
			if err != nil {
				fatalf("blockdev", "%s: %v", path, err)
				status = 1
				continue
			}
			fmt.Fprintf(os.Stdout, "%2d %5d %5d %5d %11d %s\n", ro, ra, ssz, bsz, size, path)
		}
		return status
	}

	operation := args[0]
	value := ""
	deviceIndex := 1
	if operation == "--setra" {
		if len(args) < 3 {
			fatalf("blockdev", "--setra requires a value and device")
			return 1
		}
		value, deviceIndex = args[1], 2
	}
	if len(args) != deviceIndex+1 {
		fatalf("blockdev", "expected exactly one device")
		return 1
	}
	path := args[deviceIndex]
	write := operation == "--setro" || operation == "--setrw" || operation == "--setra" || operation == "--flushbufs" || operation == "--rereadpt"
	flags := os.O_RDONLY
	if write {
		flags = os.O_RDWR
	}
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		fatalf("blockdev", "%s: %v", path, err)
		return 1
	}
	defer file.Close()

	fd := file.Fd()
	switch operation {
	case "--getro", "--getra", "--getss", "--getbsz", "--getsz", "--getsize64":
		var result uint64
		switch operation {
		case "--getro":
			var number int32
			err = ioctlPointer(fd, blkROGet, unsafe.Pointer(&number)) //nolint:gosec // Fixed-width Linux ioctl result.
			result = uint64(number)                                   //nolint:gosec // Kernel returns zero or one.
		case "--getra":
			var number uintptr
			err = ioctlPointer(fd, blkRAGet, unsafe.Pointer(&number)) //nolint:gosec // Native-word Linux ioctl result.
			result = uint64(number)
		case "--getss":
			var number int32
			err = ioctlPointer(fd, blkSSZGet, unsafe.Pointer(&number)) //nolint:gosec // Fixed-width Linux ioctl result.
			result = uint64(number)                                    //nolint:gosec // Kernel sector sizes are positive.
		case "--getbsz":
			var number uintptr
			err = ioctlPointer(fd, blkBSZGet, unsafe.Pointer(&number)) //nolint:gosec // Native-word Linux ioctl result.
			result = uint64(number)
		case "--getsz", "--getsize64":
			result, err = deviceSize(file)
			if operation == "--getsz" {
				result /= 512
			}
		}
		if err != nil {
			fatalf("blockdev", "%s: %v", path, err)
			return 1
		}
		fmt.Fprintln(os.Stdout, result)
	case "--setro", "--setrw":
		number := int32(0)
		if operation == "--setro" {
			number = 1
		}
		err = ioctlPointer(fd, blkROSet, unsafe.Pointer(&number)) //nolint:gosec // Fixed-width Linux ioctl argument.
	case "--setra":
		parsed, parseErr := strconv.ParseUint(value, 10, 64)
		if parseErr != nil || parsed > uint64(^uintptr(0)) {
			fatalf("blockdev", "invalid readahead value %q", value)
			return 1
		}
		number := uintptr(parsed)
		err = ioctlPointer(fd, blkRASet, unsafe.Pointer(&number)) //nolint:gosec // Native-word Linux ioctl argument.
	case "--flushbufs":
		err = ioctlNoArg(fd, blkFlushBuf)
	case "--rereadpt":
		err = ioctlNoArg(fd, blkRereadPT)
	default:
		fatalf("blockdev", "unsupported operation %q", operation)
		return 1
	}
	if err != nil {
		fatalf("blockdev", "%s: %v", path, err)
		return 1
	}
	return 0
}

func blockDeviceValues(file *os.File) (int32, uintptr, int32, uintptr, uint64, error) {
	var ro, ssz int32
	var ra, bsz uintptr
	if err := ioctlPointer(file.Fd(), blkROGet, unsafe.Pointer(&ro)); err != nil { //nolint:gosec // Fixed-width Linux ioctl result.
		return 0, 0, 0, 0, 0, err
	}
	if err := ioctlPointer(file.Fd(), blkRAGet, unsafe.Pointer(&ra)); err != nil { //nolint:gosec // Native-word Linux ioctl result.
		return 0, 0, 0, 0, 0, err
	}
	if err := ioctlPointer(file.Fd(), blkSSZGet, unsafe.Pointer(&ssz)); err != nil { //nolint:gosec // Fixed-width Linux ioctl result.
		return 0, 0, 0, 0, 0, err
	}
	if err := ioctlPointer(file.Fd(), blkBSZGet, unsafe.Pointer(&bsz)); err != nil { //nolint:gosec // Native-word Linux ioctl result.
		return 0, 0, 0, 0, 0, err
	}
	size, err := deviceSize(file)
	return ro, ra, ssz, bsz, size, err
}

func deviceSize(file *os.File) (uint64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Mode().IsRegular() {
		return uint64(info.Size()), nil //nolint:gosec // Regular file sizes are nonnegative.
	}
	var size uint64
	if err := ioctlPointer(file.Fd(), blkGetSize64, unsafe.Pointer(&size)); err != nil { //nolint:gosec // Fixed-width Linux ioctl result.
		return 0, err
	}
	return size, nil
}

func isBlockDevice(mode os.FileMode) bool {
	return mode&os.ModeDevice != 0 && mode&os.ModeCharDevice == 0
}

func cmdFdisk(args []string) int {
	list := false
	var devices []string
	for _, arg := range args {
		switch arg {
		case "-l", "--list":
			list = true
		case "--":
			// A following path is handled like any other operand.
		default:
			if strings.HasPrefix(arg, "-") {
				fatalf("fdisk", "only read-only -l/--list mode is supported")
				return 1
			}
			devices = append(devices, arg)
		}
	}
	if !list {
		fatalf("fdisk", "interactive editing is unsupported; use fdisk -l [DEVICE]")
		return 1
	}
	if len(devices) == 0 {
		entries, err := os.ReadDir("/sys/class/block")
		if err != nil {
			fatalf("fdisk", "%v", err)
			return 1
		}
		for _, entry := range entries {
			if _, err := os.Stat(filepath.Join("/sys/class/block", entry.Name(), "partition")); err == nil {
				continue
			}
			device := filepath.Join("/dev", entry.Name())
			if _, err := os.Stat(device); err == nil {
				devices = append(devices, device)
			}
		}
	}
	status := 0
	for index, device := range devices {
		if index > 0 {
			fmt.Fprintln(os.Stdout)
		}
		if err := listFDiskDevice(device); err != nil {
			fatalf("fdisk", "%s: %v", device, err)
			status = 1
		}
	}
	return status
}

func listFDiskDevice(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	size, err := deviceSize(file)
	if err != nil {
		return err
	}
	sectorSize := uint64(512)
	if info, statErr := file.Stat(); statErr == nil && isBlockDevice(info.Mode()) {
		var logical int32
		if ioctlErr := ioctlPointer(file.Fd(), blkSSZGet, unsafe.Pointer(&logical)); ioctlErr == nil && logical >= 512 { //nolint:gosec // Fixed-width Linux ioctl result.
			sectorSize = uint64(logical) //nolint:gosec // Positive logical sector size checked above.
		}
	}
	if sectorSize > 65536 || size < sectorSize*2 {
		return fmt.Errorf("unsupported disk geometry")
	}
	first := make([]byte, sectorSize)
	if _, err := file.ReadAt(first, 0); err != nil {
		return err
	}
	if len(first) < 512 || first[510] != 0x55 || first[511] != 0xaa {
		return fmt.Errorf("no recognized partition table")
	}
	protectiveGPT := false
	for index := 0; index < 4; index++ {
		if first[446+index*16+4] == 0xee {
			protectiveGPT = true
			break
		}
	}
	if protectiveGPT {
		return printGPT(file, path, size, sectorSize)
	}
	_ = printSfdisk(path, size, first[:512], false)
	return nil
}

func printGPT(file *os.File, path string, size, sectorSize uint64) error {
	headerSector := make([]byte, sectorSize)
	if _, err := file.ReadAt(headerSector, int64(sectorSize)); err != nil { //nolint:gosec // Sector size is bounded above.
		return fmt.Errorf("read GPT header: %w", err)
	}
	if string(headerSector[:8]) != "EFI PART" {
		return fmt.Errorf("protective MBR has no GPT header")
	}
	headerSize := binary.LittleEndian.Uint32(headerSector[12:16])
	if headerSize < 92 || uint64(headerSize) > sectorSize {
		return fmt.Errorf("invalid GPT header size %d", headerSize)
	}
	header := append([]byte(nil), headerSector[:headerSize]...)
	wantHeaderCRC := binary.LittleEndian.Uint32(header[16:20])
	for index := 16; index < 20; index++ {
		header[index] = 0
	}
	if crc32.ChecksumIEEE(header) != wantHeaderCRC {
		return fmt.Errorf("invalid GPT header checksum")
	}
	diskSectors := size / sectorSize
	currentLBA := binary.LittleEndian.Uint64(headerSector[24:32])
	backupLBA := binary.LittleEndian.Uint64(headerSector[32:40])
	firstUsable := binary.LittleEndian.Uint64(headerSector[40:48])
	lastUsable := binary.LittleEndian.Uint64(headerSector[48:56])
	if currentLBA != 1 || backupLBA <= currentLBA || backupLBA >= diskSectors ||
		firstUsable < 2 || lastUsable < firstUsable || lastUsable >= diskSectors {
		return fmt.Errorf("invalid GPT usable-sector geometry")
	}
	entriesLBA := binary.LittleEndian.Uint64(headerSector[72:80])
	entryCount := binary.LittleEndian.Uint32(headerSector[80:84])
	entrySize := binary.LittleEndian.Uint32(headerSector[84:88])
	if entryCount == 0 || entryCount > 16384 || entrySize < 128 || entrySize > 4096 || entrySize%8 != 0 {
		return fmt.Errorf("invalid GPT entry geometry")
	}
	arrayBytes := uint64(entryCount) * uint64(entrySize)
	const maxSignedOffset = ^uint64(0) >> 1
	if entriesLBA > (^uint64(0))/sectorSize || entriesLBA*sectorSize > size || entriesLBA*sectorSize > maxSignedOffset ||
		arrayBytes > size-entriesLBA*sectorSize {
		return fmt.Errorf("GPT entry array lies outside the disk")
	}
	entries := make([]byte, arrayBytes)
	if _, err := file.ReadAt(entries, int64(entriesLBA*sectorSize)); err != nil { //nolint:gosec // Entry array was checked against device size and int64-backed file offsets.
		return fmt.Errorf("read GPT entries: %w", err)
	}
	if crc32.ChecksumIEEE(entries) != binary.LittleEndian.Uint32(headerSector[88:92]) {
		return fmt.Errorf("invalid GPT entry-array checksum")
	}
	fmt.Fprintf(os.Stdout, "Disk %s: %d bytes, %d sectors\n", path, size, size/sectorSize)
	fmt.Fprintf(os.Stdout, "Disklabel type: gpt\nDisk identifier: %s\n", formatGPTGUID(headerSector[56:72]))
	fmt.Fprintln(os.Stdout, "Device Start End Sectors Size Type Name")
	type partitionRange struct{ start, end uint64 }
	var ranges []partitionRange
	for index := uint32(0); index < entryCount; index++ {
		entry := entries[uint64(index)*uint64(entrySize) : uint64(index+1)*uint64(entrySize)]
		if allZero(entry[:16]) {
			continue
		}
		start := binary.LittleEndian.Uint64(entry[32:40])
		end := binary.LittleEndian.Uint64(entry[40:48])
		if allZero(entry[16:32]) {
			return fmt.Errorf("partition %d has no unique GUID", index+1)
		}
		if start < firstUsable || end < start || end > lastUsable {
			return fmt.Errorf("partition %d lies outside the disk", index+1)
		}
		for _, previous := range ranges {
			if start <= previous.end && previous.start <= end {
				return fmt.Errorf("partition %d overlaps another partition", index+1)
			}
		}
		ranges = append(ranges, partitionRange{start: start, end: end})
		name := decodeGPTName(entry[56:])
		fmt.Fprintf(os.Stdout, "%s %d %d %d %s %s %s\n", partitionName(path, int(index+1)), start, end,
			end-start+1, humanSizeUint64((end-start+1)*sectorSize), gptTypeName(entry[:16]), name)
	}
	return nil
}

func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func formatGPTGUID(raw []byte) string {
	if len(raw) < 16 {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%x", binary.LittleEndian.Uint32(raw[0:4]),
		binary.LittleEndian.Uint16(raw[4:6]), binary.LittleEndian.Uint16(raw[6:8]), raw[8], raw[9], raw[10:16])
}

func gptTypeName(raw []byte) string {
	guid := strings.ToLower(formatGPTGUID(raw))
	switch guid {
	case "0fc63daf-8483-4772-8e79-3d69d8477de4":
		return "Linux filesystem"
	case "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f":
		return "Linux swap"
	case "c12a7328-f81f-11d2-ba4b-00a0c93ec93b":
		return "EFI System"
	}
	return guid
}

func decodeGPTName(raw []byte) string {
	units := make([]uint16, 0, len(raw)/2)
	for index := 0; index+1 < len(raw); index += 2 {
		unit := binary.LittleEndian.Uint16(raw[index : index+2])
		if unit == 0 {
			break
		}
		units = append(units, unit)
	}
	return string(utf16.Decode(units))
}

type mbrPartition struct {
	start, size uint32
	kind        byte
	bootable    bool
}

func cmdSfdisk(args []string) int {
	dump, list, force, noReread := false, false, false, false
	var device string
	for _, arg := range args {
		switch arg {
		case "-d", "--dump":
			dump = true
		case "-l", "--list":
			list = true
		case "-f", "--force":
			force = true
		case "--no-reread":
			noReread = true
		default:
			if strings.HasPrefix(arg, "-") {
				fatalf("sfdisk", "unsupported option %q", arg)
				return 1
			}
			if device != "" {
				fatalf("sfdisk", "expected exactly one device")
				return 1
			}
			device = arg
		}
	}
	if device == "" {
		fatalf("sfdisk", "missing device")
		return 1
	}
	file, err := os.Open(device)
	if err != nil {
		fatalf("sfdisk", "%s: %v", device, err)
		return 1
	}
	size, err := deviceSize(file)
	if err != nil {
		_ = file.Close()
		fatalf("sfdisk", "%s: %v", device, err)
		return 1
	}
	sector := make([]byte, 512)
	_, readErr := file.ReadAt(sector, 0)
	_ = file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		fatalf("sfdisk", "%s: %v", device, readErr)
		return 1
	}
	if dump || list {
		return printSfdisk(device, size, sector, dump)
	}
	if size < 1024 || size/512 > uint64(^uint32(0)) {
		fatalf("sfdisk", "%s: DOS partition tables require 1 KiB to 2 TiB devices", device)
		return 1
	}
	if !force {
		mounted, mountErr := pathIsInUse(device)
		if mountErr != nil {
			fatalf("sfdisk", "cannot verify mount status: %v", mountErr)
			return 1
		}
		if mounted {
			fatalf("sfdisk", "%s is mounted or active swap; --force bypasses this safeguard", device)
			return 1
		}
	}
	partitions, err := parseSfdiskScript(os.Stdin, uint32(size/512))
	if err != nil {
		fatalf("sfdisk", "%v", err)
		return 1
	}
	for i := 446; i < 510; i++ {
		sector[i] = 0
	}
	for index, partition := range partitions {
		entry := sector[446+index*16 : 446+(index+1)*16]
		if partition.bootable {
			entry[0] = 0x80
		}
		// CHS is intentionally saturated; modern Linux uses the LBA fields.
		entry[1], entry[2], entry[3] = 0xfe, 0xff, 0xff
		entry[4] = partition.kind
		entry[5], entry[6], entry[7] = 0xfe, 0xff, 0xff
		binary.LittleEndian.PutUint32(entry[8:12], partition.start)
		binary.LittleEndian.PutUint32(entry[12:16], partition.size)
	}
	sector[510], sector[511] = 0x55, 0xaa
	out, err := os.OpenFile(device, os.O_RDWR, 0)
	if err == nil {
		_, err = out.WriteAt(sector, 0)
	}
	if err == nil {
		err = out.Sync()
	}
	if out != nil {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		fatalf("sfdisk", "%s: %v", device, err)
		return 1
	}
	if !noReread {
		info, statErr := os.Stat(device)
		if statErr != nil || !isBlockDevice(info.Mode()) {
			return 0
		}
		if fd, openErr := syscall.Open(device, syscall.O_RDONLY|syscall.O_CLOEXEC, 0); openErr == nil {
			_ = ioctlNoArg(uintptr(fd), blkRereadPT)
			_ = syscall.Close(fd)
		}
	}
	return 0
}

func parseSfdiskScript(reader io.Reader, sectors uint32) ([]mbrPartition, error) {
	scanner := bufio.NewScanner(reader)
	partitions := make([]mbrPartition, 0, 4)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "label:") {
			if strings.TrimSpace(strings.TrimPrefix(lower, "label:")) != "dos" {
				return nil, fmt.Errorf("line %d: only a DOS/MBR label is supported", lineNumber)
			}
			continue
		}
		if strings.HasPrefix(lower, "unit:") {
			if strings.TrimSpace(strings.TrimPrefix(lower, "unit:")) != "sectors" {
				return nil, fmt.Errorf("line %d: unit must be sectors", lineNumber)
			}
			continue
		}
		if strings.HasPrefix(lower, "device:") || strings.HasPrefix(lower, "label-id:") || strings.HasPrefix(lower, "sector-size:") {
			continue
		}
		if colon := strings.Index(line, ":"); colon >= 0 {
			line = strings.TrimSpace(line[colon+1:])
		}
		if len(partitions) == 4 {
			return nil, fmt.Errorf("line %d: DOS labels support at most four primary partitions", lineNumber)
		}
		partition := mbrPartition{kind: 0x83}
		seenStart, seenSize := false, false
		fields := strings.Split(line, ",")
		for position, raw := range fields {
			field := strings.TrimSpace(raw)
			if field == "" {
				continue
			}
			key, value, keyed := strings.Cut(field, "=")
			if keyed {
				key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
			} else {
				value = field
				switch position {
				case 0:
					key = "start"
				case 1:
					key = "size"
				case 2:
					key = "type"
				case 3:
					key = "bootable"
				default:
					return nil, fmt.Errorf("line %d: too many fields", lineNumber)
				}
			}
			switch key {
			case "start":
				number, err := parseSectorNumber(value)
				if err != nil || number > uint64(^uint32(0)) {
					return nil, fmt.Errorf("line %d: invalid start %q", lineNumber, value)
				}
				partition.start, seenStart = uint32(number), true
			case "size":
				number, err := parseSectorNumber(value)
				if err != nil || number == 0 || number > uint64(^uint32(0)) {
					return nil, fmt.Errorf("line %d: invalid size %q", lineNumber, value)
				}
				partition.size, seenSize = uint32(number), true
			case "type":
				value = strings.TrimPrefix(strings.ToLower(value), "0x")
				number, err := strconv.ParseUint(value, 16, 8)
				if err != nil || number == 0 {
					return nil, fmt.Errorf("line %d: invalid partition type %q", lineNumber, value)
				}
				partition.kind = byte(number)
			case "bootable", "boot":
				if keyed && value != "" && value != "1" && !strings.EqualFold(value, "yes") {
					return nil, fmt.Errorf("line %d: invalid bootable value %q", lineNumber, value)
				}
				partition.bootable = true
			default:
				return nil, fmt.Errorf("line %d: unsupported field %q", lineNumber, key)
			}
		}
		if !seenStart || !seenSize {
			return nil, fmt.Errorf("line %d: start and size are required", lineNumber)
		}
		end := uint64(partition.start) + uint64(partition.size)
		if partition.start == 0 || end > uint64(sectors) {
			return nil, fmt.Errorf("line %d: partition lies outside the device", lineNumber)
		}
		for _, previous := range partitions {
			previousEnd := uint64(previous.start) + uint64(previous.size)
			if uint64(partition.start) < previousEnd && uint64(previous.start) < end {
				return nil, fmt.Errorf("line %d: partition overlaps an earlier partition", lineNumber)
			}
		}
		partitions = append(partitions, partition)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(partitions) == 0 {
		return nil, fmt.Errorf("no partitions specified")
	}
	return partitions, nil
}

func parseSectorNumber(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	multiplier := uint64(1)
	if len(value) > 0 {
		switch value[len(value)-1] {
		case 'K', 'k':
			multiplier, value = 2, value[:len(value)-1]
		case 'M', 'm':
			multiplier, value = 2048, value[:len(value)-1]
		case 'G', 'g':
			multiplier, value = 2097152, value[:len(value)-1]
		}
	}
	number, err := strconv.ParseUint(value, 0, 64)
	if err != nil || number > ^uint64(0)/multiplier {
		return 0, fmt.Errorf("invalid sector number")
	}
	return number * multiplier, nil
}

func printSfdisk(device string, size uint64, sector []byte, dump bool) int {
	if sector[510] != 0x55 || sector[511] != 0xaa {
		fatalf("sfdisk", "%s: no valid DOS partition table", device)
		return 1
	}
	if dump {
		fmt.Fprintln(os.Stdout, "label: dos")
		fmt.Fprintln(os.Stdout, "unit: sectors")
	} else {
		fmt.Fprintf(os.Stdout, "Disk %s: %d bytes, %d sectors\n", device, size, size/512)
		fmt.Fprintln(os.Stdout, "Device Start End Sectors Type Boot")
	}
	for index := 0; index < 4; index++ {
		entry := sector[446+index*16 : 446+(index+1)*16]
		start := binary.LittleEndian.Uint32(entry[8:12])
		count := binary.LittleEndian.Uint32(entry[12:16])
		if count == 0 {
			continue
		}
		boot := ""
		if entry[0] == 0x80 {
			boot = "*"
		}
		name := partitionName(device, index+1)
		if dump {
			fmt.Fprintf(os.Stdout, "%s : start=%10d, size=%10d, type=%02x", name, start, count, entry[4])
			if boot != "" {
				fmt.Fprint(os.Stdout, ", bootable")
			}
			fmt.Fprintln(os.Stdout)
		} else {
			fmt.Fprintf(os.Stdout, "%s %d %d %d %02x %s\n", name, start, uint64(start)+uint64(count)-1, count, entry[4], boot)
		}
	}
	return 0
}

func partitionName(device string, number int) string {
	separator := ""
	if len(device) > 0 && device[len(device)-1] >= '0' && device[len(device)-1] <= '9' {
		separator = "p"
	}
	return device + separator + strconv.Itoa(number)
}

func pathIsInUse(path string) (bool, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = filepath.Clean(path)
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 0 || separator+2 >= len(fields) {
			continue
		}
		source := unescapeMountField(fields[separator+2])
		sourceResolved, resolveErr := filepath.EvalSymlinks(source)
		if resolveErr != nil {
			sourceResolved = filepath.Clean(source)
		}
		if sourceResolved == resolved {
			return true, nil
		}
	}
	swaps, err := activeSwapPaths()
	if err != nil {
		return false, err
	}
	for _, source := range swaps {
		sourceResolved, resolveErr := filepath.EvalSymlinks(source)
		if resolveErr != nil {
			sourceResolved = filepath.Clean(source)
		}
		if sourceResolved == resolved {
			return true, nil
		}
	}
	return false, nil
}

// The ext formatter and checker below deliberately support one conservative
// geometry: a 4 KiB block, single-group, revision-1 filesystem. Keeping the
// written format narrow makes every metadata offset independently verifiable.
// Only the feature set varies between ext2, ext3, and ext4.
const (
	ext2BlockSize    = 4096
	ext2InodeSize    = 128
	ext4InodeSize    = 256
	ext2Inodes       = 1024
	extReservedInode = 11
	// extJournalBlocks is JBD2's minimum journal length, and also the size
	// mke2fs picks for every filesystem this formatter can create.
	extJournalBlocks   = 1024
	extJournalMagic    = 0xc03b3998
	extExtentMagic     = 0xf30a
	extInodeExtentsFlg = 0x80000
	extExtraIsize      = 32
)

// extProfile names one of the supported ext-family on-disk formats. ext3 adds a
// journal to ext2, and ext4 additionally maps file data with extent trees and
// widens inodes so that later kernels can store the extra timestamp fields.
type extProfile struct {
	name      string
	inodeSize uint32
	journal   bool
	extents   bool
	minBlocks uint32
}

var extProfiles = map[string]extProfile{
	"ext2": {name: "ext2", inodeSize: ext2InodeSize, minBlocks: 256},
	"ext3": {name: "ext3", inodeSize: ext2InodeSize, journal: true, minBlocks: 2048},
	"ext4": {name: "ext4", inodeSize: ext4InodeSize, journal: true, extents: true, minBlocks: 2048},
}

// extLayout is the block assignment derived from a profile. Every block below
// firstFree is metadata written by the formatter; everything above it is free.
type extLayout struct {
	inodeTable      uint32
	inodeTableBlks  uint32
	root            uint32
	lostFound       uint32
	journal         uint32
	journalIndirect uint32
	firstFree       uint32
}

func (p extProfile) layout() extLayout {
	// Blocks 0 through 3 hold the superblock, the group descriptor table, the
	// block bitmap, and the inode bitmap respectively.
	layout := extLayout{inodeTable: 4, inodeTableBlks: ext2Inodes * p.inodeSize / ext2BlockSize}
	layout.root = layout.inodeTable + layout.inodeTableBlks
	layout.lostFound = layout.root + 1
	layout.firstFree = layout.lostFound + 1
	if p.journal {
		layout.journal = layout.firstFree
		layout.firstFree = layout.journal + extJournalBlocks
		if !p.extents {
			// A block-mapped journal needs one indirect block for the
			// blocks that do not fit in the twelve direct pointers.
			layout.journalIndirect = layout.firstFree
			layout.firstFree++
		}
	}
	return layout
}

// journalBlockCount is the number of blocks the journal occupies on disk,
// including the indirect block a block-mapped journal needs.
func (p extProfile) journalBlockCount() uint32 {
	if !p.journal {
		return 0
	}
	if p.extents {
		return extJournalBlocks
	}
	return extJournalBlocks + 1
}

func cmdMkfs(args []string) int {
	filesystem := ""
	forward := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if args[index] == "-t" || args[index] == "--type" {
			index++
			if index >= len(args) {
				fatalf("mkfs", "-t requires a filesystem type")
				return 1
			}
			filesystem = strings.ToLower(args[index])
		} else {
			forward = append(forward, args[index])
		}
	}
	if filesystem == "" {
		fatalf("mkfs", "filesystem type is required (supported: %s)", mkfsSupportedTypes)
		return 1
	}
	switch filesystem {
	case "ext2", "ext3", "ext4":
		return mkfsExt("mkfs."+filesystem, forward, extProfiles[filesystem])
	case "xfs":
		return cmdMkfsXfs(forward)
	case "btrfs":
		return cmdMkfsBtrfs(forward)
	}
	fatalf("mkfs", "unsupported filesystem type %q (supported: %s)", filesystem, mkfsSupportedTypes)
	return 1
}

const mkfsSupportedTypes = "ext2, ext3, ext4, xfs, btrfs"

func cmdMkfsExt2(args []string) int { return mkfsExt("mkfs.ext2", args, extProfiles["ext2"]) }
func cmdMkfsExt3(args []string) int { return mkfsExt("mkfs.ext3", args, extProfiles["ext3"]) }
func cmdMkfsExt4(args []string) int { return mkfsExt("mkfs.ext4", args, extProfiles["ext4"]) }

func mkfsExt(prog string, args []string, profile extProfile) int {
	request, ok := parseFormatArgs(prog, args, 16)
	if !ok {
		return 1
	}
	file, bytesAvailable, status := openFormatTarget(prog, request.device, request.force)
	if file == nil {
		return status
	}
	defer file.Close()
	blocks := bytesAvailable / ext2BlockSize
	if request.kibibytes != 0 {
		if request.kibibytes%4 != 0 || request.kibibytes > bytesAvailable/1024 {
			fatalf(prog, "invalid 1 KiB block count %d", request.kibibytes)
			return 1
		}
		blocks = request.kibibytes * 1024 / ext2BlockSize
	}
	if blocks < uint64(profile.minBlocks) || blocks > ext2BlockSize*8 {
		fatalf(prog, "supported %s size is %s through 128 MiB", profile.name,
			humanSizeUint64(uint64(profile.minBlocks)*ext2BlockSize))
		return 1
	}
	if err := writeExtFilesystem(file, uint32(blocks), request.label, profile); err != nil {
		fatalf(prog, "%s: %v", request.device, err)
		return 1
	}
	return 0
}

// formatRequest is the option set every mkfs applet accepts. kibibytes is the
// optional explicit size operand, expressed in 1 KiB units like mke2fs, and is
// zero when the whole target should be used.
type formatRequest struct {
	force     bool
	label     string
	device    string
	kibibytes uint64
}

func parseFormatArgs(prog string, args []string, labelMax int) (formatRequest, bool) {
	var request formatRequest
	var operands []string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-F", "-f", "--force":
			request.force = true
		case "-L", "--label":
			flag := args[index]
			index++
			if index >= len(args) {
				fatalf(prog, "%s requires a label", flag)
				return request, false
			}
			request.label = args[index]
		default:
			if strings.HasPrefix(args[index], "-") {
				fatalf(prog, "unsupported option %q", args[index])
				return request, false
			}
			operands = append(operands, args[index])
		}
	}
	if len(operands) < 1 || len(operands) > 2 {
		fatalf(prog, "expected DEVICE [BLOCKS]")
		return request, false
	}
	if len(request.label) > labelMax || strings.IndexByte(request.label, 0) >= 0 {
		fatalf(prog, "label must contain at most %d non-NUL bytes", labelMax)
		return request, false
	}
	request.device = operands[0]
	if len(operands) == 2 {
		kibibytes, err := strconv.ParseUint(operands[1], 10, 64)
		if err != nil || kibibytes == 0 {
			fatalf(prog, "invalid 1 KiB block count %q", operands[1])
			return request, false
		}
		request.kibibytes = kibibytes
	}
	return request, true
}

// openFormatTarget performs the checks every formatter shares: the target must
// be a block device or an explicitly forced regular file, and it must not be
// mounted or in use as swap. A nil file means the returned status should be
// used as the applet exit code.
func openFormatTarget(prog, path string, force bool) (*os.File, uint64, int) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		fatalf(prog, "%s: %v", path, err)
		return nil, 0, 1
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		fatalf(prog, "%s: %v", path, err)
		return nil, 0, 1
	}
	if !info.Mode().IsRegular() && !isBlockDevice(info.Mode()) {
		file.Close()
		fatalf(prog, "%s is not a regular file or block device", path)
		return nil, 0, 1
	}
	if info.Mode().IsRegular() && !force {
		file.Close()
		fatalf(prog, "%s is a regular file; use -F to confirm formatting", path)
		return nil, 0, 1
	}
	if !force {
		mounted, mountErr := pathIsInUse(path)
		if mountErr != nil {
			file.Close()
			fatalf(prog, "cannot verify mount status: %v", mountErr)
			return nil, 0, 1
		}
		if mounted {
			file.Close()
			fatalf(prog, "%s is mounted or active swap", path)
			return nil, 0, 1
		}
	}
	size, err := deviceSize(file)
	if err != nil {
		file.Close()
		fatalf(prog, "%s: %v", path, err)
		return nil, 0, 1
	}
	return file, size, 0
}

func writeExtFilesystem(file *os.File, blocks uint32, label string, profile extProfile) error {
	layout := profile.layout()
	if blocks <= layout.firstFree {
		return fmt.Errorf("device is too small")
	}
	metadata := make([]byte, uint64(layout.firstFree)*ext2BlockSize)
	now := uint32(time.Now().Unix()) //nolint:gosec // ext2 revision 1 uses unsigned 32-bit timestamps.
	uuid := make([]byte, 16)
	if _, err := rand.Read(uuid); err != nil {
		return fmt.Errorf("generate UUID: %w", err)
	}
	uuid[6] = uuid[6]&0x0f | 0x40
	uuid[8] = uuid[8]&0x3f | 0x80

	inodeTable := metadata[uint64(layout.inodeTable)*ext2BlockSize : uint64(layout.root)*ext2BlockSize]
	inodeAt := func(number uint32) []byte {
		start := uint64(number-1) * uint64(profile.inodeSize)
		return inodeTable[start : start+uint64(profile.inodeSize)]
	}
	writeExtDirectoryInode(inodeAt(2), layout.root, 3, now, profile)
	writeExtDirectoryInode(inodeAt(extReservedInode), layout.lostFound, 2, now, profile)
	journalInode := []byte(nil)
	if profile.journal {
		journalInode = inodeAt(8)
		writeExtJournalInode(journalInode, layout, profile, now)
		writeExtJournalSuperblock(metadata[uint64(layout.journal)*ext2BlockSize:], uuid)
		if !profile.extents {
			indirect := metadata[uint64(layout.journalIndirect)*ext2BlockSize:]
			for index := uint32(12); index < extJournalBlocks; index++ {
				binary.LittleEndian.PutUint32(indirect[(index-12)*4:(index-12)*4+4], layout.journal+index)
			}
		}
	}

	super := metadata[1024:2048]
	put32 := func(offset int, value uint32) { binary.LittleEndian.PutUint32(super[offset:offset+4], value) }
	put16 := func(offset int, value uint16) { binary.LittleEndian.PutUint16(super[offset:offset+2], value) }
	freeBlocks := blocks - layout.firstFree
	freeInodes := uint32(ext2Inodes - extReservedInode)
	compat, incompat, roCompat := uint32(0), uint32(0x2), uint32(0x1)
	if profile.journal {
		compat |= 0x4
	}
	if profile.extents {
		incompat |= 0x40
	}
	put32(0, ext2Inodes)
	put32(4, blocks)
	put32(8, blocks/20)
	put32(12, freeBlocks)
	put32(16, freeInodes)
	put32(20, 0)
	put32(24, 2)
	put32(28, 2)
	put32(32, ext2BlockSize*8)
	put32(36, ext2BlockSize*8)
	put32(40, ext2Inodes)
	put32(44, 0)
	put32(48, now)
	put16(52, 0)
	put16(54, 20)
	put16(56, 0xef53)
	put16(58, 1)
	put16(60, 1)
	put32(64, now)
	put32(68, 180*24*60*60)
	put32(72, 0)
	put32(76, 1)
	put32(84, extReservedInode)
	put16(88, uint16(profile.inodeSize)) //nolint:gosec // Profile inode sizes are 128 or 256.
	put32(92, compat)
	put32(96, incompat) // Directory entries carry a file-type byte.
	put32(100, roCompat)
	copy(super[104:120], uuid)
	copy(super[120:136], label)
	put32(264, now) // s_mkfs_time
	if profile.journal {
		put32(224, 8) // s_journal_inum
		super[253] = 1
		// s_jnl_blocks backs up the journal inode's block map and size so
		// that a checker can find the journal if the inode is damaged.
		copy(super[268:328], journalInode[40:100])
		put32(332, extJournalBlocks*ext2BlockSize)
	}
	if profile.inodeSize > ext2InodeSize {
		put16(348, extExtraIsize) // s_min_extra_isize
		put16(350, extExtraIsize) // s_want_extra_isize
	}

	group := metadata[ext2BlockSize : ext2BlockSize+32]
	binary.LittleEndian.PutUint32(group[0:4], 2)
	binary.LittleEndian.PutUint32(group[4:8], 3)
	binary.LittleEndian.PutUint32(group[8:12], layout.inodeTable)
	binary.LittleEndian.PutUint16(group[12:14], uint16(freeBlocks)) //nolint:gosec // Single group has at most 32768 blocks.
	binary.LittleEndian.PutUint16(group[14:16], uint16(freeInodes))
	binary.LittleEndian.PutUint16(group[16:18], 2)

	blockBitmap := metadata[2*ext2BlockSize : 3*ext2BlockSize]
	for block := uint32(0); block < layout.firstFree; block++ {
		setBitmapBit(blockBitmap, block)
	}
	for block := blocks; block < ext2BlockSize*8; block++ {
		setBitmapBit(blockBitmap, block)
	}
	inodeBitmap := metadata[3*ext2BlockSize : 4*ext2BlockSize]
	for inode := uint32(0); inode < extReservedInode; inode++ {
		setBitmapBit(inodeBitmap, inode)
	}
	for inode := uint32(ext2Inodes); inode < ext2BlockSize*8; inode++ {
		setBitmapBit(inodeBitmap, inode)
	}

	root := metadata[uint64(layout.root)*ext2BlockSize : uint64(layout.root+1)*ext2BlockSize]
	writeExt2Dirent(root[0:12], 2, 12, ".")
	writeExt2Dirent(root[12:24], 2, 12, "..")
	writeExt2Dirent(root[24:], extReservedInode, ext2BlockSize-24, "lost+found")
	lostFound := metadata[uint64(layout.lostFound)*ext2BlockSize : uint64(layout.lostFound+1)*ext2BlockSize]
	writeExt2Dirent(lostFound[0:12], extReservedInode, 12, ".")
	writeExt2Dirent(lostFound[12:], 2, ext2BlockSize-12, "..")

	if _, err := file.WriteAt(metadata, 0); err != nil {
		return err
	}
	return file.Sync()
}

func setBitmapBit(bitmap []byte, bit uint32) {
	bitmap[bit/8] |= 1 << (bit % 8)
}

func writeExtDirectoryInode(inode []byte, block uint32, links uint16, now uint32, profile extProfile) {
	binary.LittleEndian.PutUint16(inode[0:2], 0x41ed)
	binary.LittleEndian.PutUint32(inode[4:8], ext2BlockSize)
	binary.LittleEndian.PutUint32(inode[8:12], now)
	binary.LittleEndian.PutUint32(inode[12:16], now)
	binary.LittleEndian.PutUint32(inode[16:20], now)
	binary.LittleEndian.PutUint16(inode[26:28], links)
	binary.LittleEndian.PutUint32(inode[28:32], ext2BlockSize/512)
	if profile.extents {
		binary.LittleEndian.PutUint32(inode[32:36], extInodeExtentsFlg)
		writeExtExtentMap(inode[40:100], 1, block)
	} else {
		binary.LittleEndian.PutUint32(inode[40:44], block)
	}
	if profile.inodeSize > ext2InodeSize {
		binary.LittleEndian.PutUint16(inode[128:130], extExtraIsize)
	}
}

// writeExtJournalInode fills reserved inode 8, whose data blocks are the
// journal itself. The journal is always laid out as one contiguous run, so
// extent profiles need a single extent and block-mapped profiles need the
// twelve direct pointers plus one indirect block.
func writeExtJournalInode(inode []byte, layout extLayout, profile extProfile, now uint32) {
	binary.LittleEndian.PutUint16(inode[0:2], 0x8180) // regular file, mode 0600
	binary.LittleEndian.PutUint32(inode[4:8], extJournalBlocks*ext2BlockSize)
	binary.LittleEndian.PutUint32(inode[8:12], now)
	binary.LittleEndian.PutUint32(inode[12:16], now)
	binary.LittleEndian.PutUint32(inode[16:20], now)
	binary.LittleEndian.PutUint16(inode[26:28], 1)
	binary.LittleEndian.PutUint32(inode[28:32], profile.journalBlockCount()*(ext2BlockSize/512))
	if profile.extents {
		binary.LittleEndian.PutUint32(inode[32:36], extInodeExtentsFlg)
		writeExtExtentMap(inode[40:100], extJournalBlocks, layout.journal)
	} else {
		for index := uint32(0); index < 12; index++ {
			binary.LittleEndian.PutUint32(inode[40+index*4:44+index*4], layout.journal+index)
		}
		binary.LittleEndian.PutUint32(inode[88:92], layout.journalIndirect)
	}
	if profile.inodeSize > ext2InodeSize {
		binary.LittleEndian.PutUint16(inode[128:130], extExtraIsize)
	}
}

// writeExtJournalSuperblock writes the JBD2 V2 superblock that marks an empty,
// fully recovered journal. Every field in it is big-endian.
func writeExtJournalSuperblock(journal []byte, uuid []byte) {
	be32 := func(offset int, value uint32) { binary.BigEndian.PutUint32(journal[offset:offset+4], value) }
	be32(0, extJournalMagic)
	be32(4, 4) // JBD2 V2 superblock block type
	be32(12, ext2BlockSize)
	be32(16, extJournalBlocks)
	be32(20, 1) // first block of the log
	be32(24, 1) // first expected commit sequence
	be32(28, 0) // no outstanding log start
	copy(journal[48:64], uuid)
	be32(64, 1) // one filesystem shares this journal
}

// writeExtExtentMap fills an inode's inline block area with a depth-zero extent
// tree mapping length logical blocks from logical block zero onto start.
func writeExtExtentMap(target []byte, length uint16, start uint32) {
	binary.LittleEndian.PutUint16(target[0:2], extExtentMagic)
	binary.LittleEndian.PutUint16(target[2:4], 1) // one extent
	binary.LittleEndian.PutUint16(target[4:6], 4) // the inline area holds four
	binary.LittleEndian.PutUint16(target[6:8], 0) // depth zero: entries are leaves
	binary.LittleEndian.PutUint32(target[12:16], 0)
	binary.LittleEndian.PutUint16(target[16:18], length)
	binary.LittleEndian.PutUint16(target[18:20], 0)
	binary.LittleEndian.PutUint32(target[20:24], start)
}

func writeExt2Dirent(target []byte, inode uint32, recordLength int, name string) {
	binary.LittleEndian.PutUint32(target[0:4], inode)
	binary.LittleEndian.PutUint16(target[4:6], uint16(recordLength)) //nolint:gosec // One directory block is 4096 bytes.
	target[6], target[7] = byte(len(name)), 2                        //nolint:gosec // Names here are fixed and no longer than lost+found.
	copy(target[8:], name)
}

func cmdFsck(args []string) int {
	filesystem := ""
	forward := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if args[index] == "-t" || args[index] == "--type" {
			index++
			if index >= len(args) {
				fatalf("fsck", "-t requires a filesystem type")
				return 8
			}
			filesystem = strings.ToLower(args[index])
		} else {
			forward = append(forward, args[index])
		}
	}
	if filesystem != "" && filesystem != "ext2" && filesystem != "ext3" && filesystem != "ext4" {
		fatalf("fsck", "unsupported filesystem type %q (supported: ext2/ext3/ext4 validation)", filesystem)
		return 8
	}
	return fsckExt("fsck", forward, filesystem)
}

func cmdFsckExt(args []string) int {
	return fsckExt("fsck.ext2", args, "")
}

func fsckExt(prog string, args []string, requestedType string) int {
	var devices []string
	for _, arg := range args {
		switch arg {
		case "-n", "-f", "-p", "-a", "-v":
			// Validation is always read-only; accepted common modes cannot weaken it.
		default:
			if strings.HasPrefix(arg, "-") {
				fatalf(prog, "unsupported option %q", arg)
				return 8
			}
			devices = append(devices, arg)
		}
	}
	if len(devices) == 0 {
		fatalf(prog, "missing device")
		return 8
	}
	status := 0
	for _, device := range devices {
		err := checkExtFilesystem(device, requestedType)
		if err != nil {
			fatalf(prog, "%s: %v", device, err)
			status |= 4
			continue
		}
		fmt.Fprintf(os.Stdout, "%s: clean\n", device)
	}
	return status
}

func checkExtFilesystem(path, requestedType string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	super := make([]byte, 1024)
	if _, err := file.ReadAt(super, 1024); err != nil {
		return fmt.Errorf("read superblock: %w", err)
	}
	if binary.LittleEndian.Uint16(super[56:58]) != 0xef53 {
		return fmt.Errorf("invalid ext superblock magic")
	}
	state := binary.LittleEndian.Uint16(super[58:60])
	if state&1 == 0 || state&2 != 0 {
		return fmt.Errorf("filesystem is not marked clean")
	}
	revision := binary.LittleEndian.Uint32(super[76:80])
	if revision > 1 {
		return fmt.Errorf("unsupported ext revision %d", revision)
	}
	logBlock := binary.LittleEndian.Uint32(super[24:28])
	if logBlock > 6 {
		return fmt.Errorf("invalid block size shift %d", logBlock)
	}
	blockSize := uint64(1024) << logBlock
	blocks := uint64(binary.LittleEndian.Uint32(super[4:8]))
	inodes := binary.LittleEndian.Uint32(super[0:4])
	blocksPerGroup := binary.LittleEndian.Uint32(super[32:36])
	inodesPerGroup := binary.LittleEndian.Uint32(super[40:44])
	firstDataBlock := binary.LittleEndian.Uint32(super[20:24])
	incompat := binary.LittleEndian.Uint32(super[96:100])
	if incompat&0x80 != 0 {
		blocks |= uint64(binary.LittleEndian.Uint32(super[336:340])) << 32
	}
	if blocks == 0 || inodes == 0 || blocksPerGroup == 0 || inodesPerGroup == 0 || uint64(firstDataBlock) >= blocks {
		return fmt.Errorf("invalid zero or out-of-range geometry")
	}
	if uint64(blocksPerGroup) > blockSize*8 || uint64(inodesPerGroup) > blockSize*8 ||
		uint64(binary.LittleEndian.Uint32(super[12:16])) > blocks || binary.LittleEndian.Uint32(super[16:20]) > inodes {
		return fmt.Errorf("invalid bitmap or free-space geometry")
	}
	const maxFileOffset = ^uint64(0) >> 1
	if blocks > maxFileOffset/blockSize {
		return fmt.Errorf("filesystem exceeds supported file offsets")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() && uint64(info.Size()) < blocks*blockSize { //nolint:gosec // File size is nonnegative.
		return fmt.Errorf("filesystem extends beyond device")
	}
	const knownIncompat = uint32(0x2 | 0x4 | 0x40 | 0x80 | 0x100 | 0x200 | 0x2000 | 0x4000 | 0x8000 | 0x10000)
	if incompat & ^knownIncompat != 0 {
		return fmt.Errorf("unsupported incompatible feature bits 0x%x", incompat & ^knownIncompat)
	}
	if incompat&0x4 != 0 || binary.LittleEndian.Uint32(super[232:236]) != 0 {
		return fmt.Errorf("journal replay or orphan cleanup is required")
	}
	hasJournal := binary.LittleEndian.Uint32(super[92:96])&0x4 != 0
	usesExtents := incompat&0x40 != 0
	detectedType := "ext2"
	if hasJournal {
		detectedType = "ext3"
	}
	if usesExtents {
		detectedType = "ext4"
	}
	if requestedType == "ext2" && detectedType != "ext2" {
		return fmt.Errorf("filesystem is %s, not ext2", detectedType)
	}
	if requestedType == "ext3" && detectedType != "ext3" {
		return fmt.Errorf("filesystem is %s, not ext3", detectedType)
	}
	if requestedType == "ext4" && detectedType != "ext4" {
		return fmt.Errorf("filesystem does not use ext4 extents")
	}
	groupDescriptorOffset := int64(blockSize)
	if blockSize == 1024 {
		groupDescriptorOffset = 2048
	}
	descriptorSize := uint16(32)
	if incompat&0x80 != 0 {
		descriptorSize = binary.LittleEndian.Uint16(super[254:256])
		if descriptorSize < 64 {
			return fmt.Errorf("64-bit filesystem has a short group descriptor")
		}
	}
	blockGroups := (blocks - uint64(firstDataBlock) + uint64(blocksPerGroup) - 1) / uint64(blocksPerGroup)
	inodeGroups := (uint64(inodes) + uint64(inodesPerGroup) - 1) / uint64(inodesPerGroup)
	if blockGroups == 0 || blockGroups != inodeGroups ||
		blockGroups > (maxFileOffset-uint64(groupDescriptorOffset))/uint64(descriptorSize) { //nolint:gosec // Offset is a small positive block boundary.
		return fmt.Errorf("inconsistent block and inode group counts")
	}
	var descriptor []byte
	for groupNumber := uint64(0); groupNumber < blockGroups; groupNumber++ {
		current := make([]byte, descriptorSize)
		offset := groupDescriptorOffset + int64(groupNumber*uint64(descriptorSize)) //nolint:gosec // Descriptor table length was bounded above.
		if _, err := file.ReadAt(current, offset); err != nil {
			return fmt.Errorf("read group %d descriptor: %w", groupNumber, err)
		}
		if groupNumber == 0 {
			descriptor = current
		}
		for _, field := range []struct {
			name   string
			offset int
			hi     int
		}{
			{"block bitmap", 0, 32}, {"inode bitmap", 4, 36}, {"inode table", 8, 40},
		} {
			block := uint64(binary.LittleEndian.Uint32(current[field.offset : field.offset+4]))
			if descriptorSize >= 64 {
				block |= uint64(binary.LittleEndian.Uint32(current[field.hi:field.hi+4])) << 32
			}
			if block < uint64(firstDataBlock) || block >= blocks {
				return fmt.Errorf("group %d %s block is outside the filesystem", groupNumber, field.name)
			}
		}
		groupBlocks := blocks - uint64(firstDataBlock) - groupNumber*uint64(blocksPerGroup)
		if groupBlocks > uint64(blocksPerGroup) {
			groupBlocks = uint64(blocksPerGroup)
		}
		groupInodes := uint64(inodes) - groupNumber*uint64(inodesPerGroup)
		if groupInodes > uint64(inodesPerGroup) {
			groupInodes = uint64(inodesPerGroup)
		}
		freeBlocks := uint64(binary.LittleEndian.Uint16(current[12:14]))
		freeInodes := uint64(binary.LittleEndian.Uint16(current[14:16]))
		if descriptorSize >= 64 {
			freeBlocks |= uint64(binary.LittleEndian.Uint16(current[44:46])) << 16
			freeInodes |= uint64(binary.LittleEndian.Uint16(current[46:48])) << 16
		}
		if freeBlocks > groupBlocks || freeInodes > groupInodes {
			return fmt.Errorf("group %d has invalid free-space counts", groupNumber)
		}
	}
	inodeSize := uint16(128)
	if revision == 1 {
		inodeSize = binary.LittleEndian.Uint16(super[88:90])
	}
	if inodeSize < 128 || inodeSize > uint16(blockSize) || inodeSize&(inodeSize-1) != 0 {
		return fmt.Errorf("invalid inode size %d", inodeSize)
	}
	inodeTable := uint64(binary.LittleEndian.Uint32(descriptor[8:12]))
	if descriptorSize >= 64 {
		inodeTable |= uint64(binary.LittleEndian.Uint32(descriptor[40:44])) << 32
	}
	if inodeTable > (maxFileOffset-uint64(inodeSize))/blockSize {
		return fmt.Errorf("root inode offset overflows")
	}
	rootOffset := int64(inodeTable*blockSize) + int64(inodeSize) //nolint:gosec // Bounds checked immediately above.
	root := make([]byte, inodeSize)
	if _, err := file.ReadAt(root, rootOffset); err != nil {
		return fmt.Errorf("read root inode: %w", err)
	}
	if binary.LittleEndian.Uint16(root[0:2])&0xf000 != 0x4000 || binary.LittleEndian.Uint16(root[26:28]) < 2 {
		return fmt.Errorf("root inode is not a linked directory")
	}
	rootBlock, err := extRootDirectoryBlock(file, root, blockSize, blocks, uint64(firstDataBlock), usesExtents)
	if err != nil {
		return err
	}
	directory := make([]byte, blockSize)
	if _, err := file.ReadAt(directory, int64(rootBlock*blockSize)); err != nil { //nolint:gosec // Filesystem size was bounded to the signed file-offset range.
		return fmt.Errorf("read root directory: %w", err)
	}
	if err := validateRootDirectory(directory); err != nil {
		return err
	}
	return nil
}

func extRootDirectoryBlock(file *os.File, inode []byte, blockSize, blocks, firstDataBlock uint64, usesExtents bool) (uint64, error) {
	const extentsFlag = uint32(0x80000)
	flags := binary.LittleEndian.Uint32(inode[32:36])
	if !usesExtents || flags&extentsFlag == 0 {
		block := uint64(binary.LittleEndian.Uint32(inode[40:44]))
		if block < firstDataBlock || block >= blocks {
			return 0, fmt.Errorf("root directory block is outside the filesystem")
		}
		return block, nil
	}
	node := inode[40:]
	for depthLimit := 0; depthLimit < 6; depthLimit++ {
		if len(node) < 12 || binary.LittleEndian.Uint16(node[0:2]) != 0xf30a {
			return 0, fmt.Errorf("invalid root extent header")
		}
		entries := int(binary.LittleEndian.Uint16(node[2:4]))
		maximum := int(binary.LittleEndian.Uint16(node[4:6]))
		depth := binary.LittleEndian.Uint16(node[6:8])
		if entries < 1 || entries > maximum || 12+entries*12 > len(node) {
			return 0, fmt.Errorf("invalid root extent count")
		}
		entry := node[12:24]
		if binary.LittleEndian.Uint32(entry[0:4]) != 0 {
			return 0, fmt.Errorf("root extent tree does not begin at logical block zero")
		}
		if depth == 0 {
			length := binary.LittleEndian.Uint16(entry[4:6])
			if length == 0 || length&0x8000 != 0 {
				return 0, fmt.Errorf("root directory begins with an uninitialized extent")
			}
			block := uint64(binary.LittleEndian.Uint32(entry[8:12])) | uint64(binary.LittleEndian.Uint16(entry[6:8]))<<32
			if block < firstDataBlock || block >= blocks {
				return 0, fmt.Errorf("root extent is outside the filesystem")
			}
			return block, nil
		}
		leaf := uint64(binary.LittleEndian.Uint32(entry[4:8])) | uint64(binary.LittleEndian.Uint16(entry[8:10]))<<32
		if leaf < firstDataBlock || leaf >= blocks || leaf > (^uint64(0)>>1)/blockSize {
			return 0, fmt.Errorf("root extent index is outside the filesystem")
		}
		node = make([]byte, blockSize)
		if _, err := file.ReadAt(node, int64(leaf*blockSize)); err != nil { //nolint:gosec // Extent block offset was bounded above.
			return 0, fmt.Errorf("read root extent node: %w", err)
		}
	}
	return 0, fmt.Errorf("root extent tree is too deep")
}

func validateRootDirectory(block []byte) error {
	offset := 0
	seenDot, seenDotDot := false, false
	for offset < len(block) {
		if len(block)-offset < 8 {
			return fmt.Errorf("truncated root directory entry")
		}
		inode := binary.LittleEndian.Uint32(block[offset : offset+4])
		recordLength := int(binary.LittleEndian.Uint16(block[offset+4 : offset+6]))
		nameLength := int(block[offset+6])
		if recordLength < 8 || recordLength%4 != 0 || offset+recordLength > len(block) || nameLength > recordLength-8 {
			return fmt.Errorf("invalid root directory entry")
		}
		if inode != 0 {
			name := string(block[offset+8 : offset+8+nameLength])
			seenDot = seenDot || name == "." && inode == 2
			seenDotDot = seenDotDot || name == ".." && inode == 2
		}
		offset += recordLength
	}
	if !seenDot || !seenDotDot {
		return fmt.Errorf("root directory lacks dot entries")
	}
	return nil
}

func cmdMkswap(args []string) int {
	force := false
	label := ""
	var operands []string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-f", "--force":
			force = true
		case "-L", "--label":
			index++
			if index >= len(args) {
				fatalf("mkswap", "-L requires a label")
				return 1
			}
			label = args[index]
		default:
			if strings.HasPrefix(args[index], "-") {
				fatalf("mkswap", "unsupported option %q", args[index])
				return 1
			}
			operands = append(operands, args[index])
		}
	}
	if len(operands) != 1 {
		fatalf("mkswap", "expected exactly one device or file")
		return 1
	}
	if len(label) > 16 || strings.IndexByte(label, 0) >= 0 {
		fatalf("mkswap", "label must contain at most 16 non-NUL bytes")
		return 1
	}
	path := operands[0]
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		fatalf("mkswap", "%s: %v", path, err)
		return 1
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		fatalf("mkswap", "%s: %v", path, err)
		return 1
	}
	if !info.Mode().IsRegular() && !isBlockDevice(info.Mode()) {
		fatalf("mkswap", "%s is not a regular file or block device", path)
		return 1
	}
	if !force {
		mounted, mountErr := pathIsInUse(path)
		if mountErr != nil {
			fatalf("mkswap", "cannot verify mount status: %v", mountErr)
			return 1
		}
		if mounted {
			fatalf("mkswap", "%s is mounted or active swap", path)
			return 1
		}
	}
	size, err := deviceSize(file)
	if err != nil || size < 2*4096 || size/4096-1 > uint64(^uint32(0)) {
		fatalf("mkswap", "%s has an unsupported size", path)
		return 1
	}
	header := make([]byte, 4096)
	binary.LittleEndian.PutUint32(header[1024:1028], 1)
	binary.LittleEndian.PutUint32(header[1028:1032], uint32(size/4096-1)) //nolint:gosec // Size was bounded to uint32 pages above.
	uuid := header[1036:1052]
	if _, err := rand.Read(uuid); err != nil {
		fatalf("mkswap", "generate UUID: %v", err)
		return 1
	}
	uuid[6] = uuid[6]&0x0f | 0x40
	uuid[8] = uuid[8]&0x3f | 0x80
	copy(header[1052:1068], label)
	copy(header[len(header)-10:], "SWAPSPACE2")
	if _, err := file.WriteAt(header, 0); err != nil {
		fatalf("mkswap", "%s: %v", path, err)
		return 1
	}
	if err := file.Sync(); err != nil {
		fatalf("mkswap", "%s: %v", path, err)
		return 1
	}
	return 0
}

func cmdSwapon(args []string) int {
	return swapCommand("swapon", args, true)
}

func cmdSwapoff(args []string) int {
	return swapCommand("swapoff", args, false)
}

func swapCommand(prog string, args []string, enable bool) int {
	all, priority := false, -1
	var paths []string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-a", "--all":
			all = true
		case "-p", "--priority":
			if !enable {
				fatalf(prog, "priority is only valid for swapon")
				return 1
			}
			index++
			if index >= len(args) {
				fatalf(prog, "-p requires a priority")
				return 1
			}
			parsed, err := strconv.Atoi(args[index])
			if err != nil || parsed < 0 || parsed > 32767 {
				fatalf(prog, "invalid priority %q", args[index])
				return 1
			}
			priority = parsed
		default:
			if strings.HasPrefix(args[index], "-") {
				fatalf(prog, "unsupported option %q", args[index])
				return 1
			}
			paths = append(paths, args[index])
		}
	}
	if all {
		if len(paths) != 0 {
			fatalf(prog, "cannot combine --all with explicit devices")
			return 1
		}
		var err error
		if enable {
			paths, err = swapPathsFromFstab()
		} else {
			paths, err = activeSwapPaths()
		}
		if err != nil {
			fatalf(prog, "%v", err)
			return 1
		}
	}
	if len(paths) == 0 && all {
		return 0
	}
	if len(paths) == 0 {
		fatalf(prog, "missing swap device")
		return 1
	}
	status := 0
	for _, path := range paths {
		pathPointer, pointerErr := syscall.BytePtrFromString(path)
		if pointerErr != nil {
			fatalf(prog, "%q: %v", path, pointerErr)
			status = 1
			continue
		}
		var errno syscall.Errno
		if enable {
			flags := 0
			if priority >= 0 {
				flags = 0x8000 | priority
			}
			_, _, errno = syscall.Syscall(syscall.SYS_SWAPON, uintptr(unsafe.Pointer(pathPointer)), uintptr(flags), 0) //nolint:gosec // Direct Linux swapon ABI with a NUL-checked path.
		} else {
			_, _, errno = syscall.Syscall(syscall.SYS_SWAPOFF, uintptr(unsafe.Pointer(pathPointer)), 0, 0) //nolint:gosec // Direct Linux swapoff ABI with a NUL-checked path.
		}
		if errno != 0 {
			fatalf(prog, "%s: %v", path, errno)
			status = 1
		}
	}
	return status
}

func swapPathsFromFstab() ([]string, error) {
	file, err := os.Open("/etc/fstab")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var paths []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[2] == "swap" && !strings.Contains(","+fields[3]+",", ",noauto,") {
			paths = append(paths, fields[0])
		}
	}
	return paths, scanner.Err()
}

func activeSwapPaths() ([]string, error) {
	data, err := os.ReadFile("/proc/swaps")
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(data, []byte{'\n'})
	paths := make([]string, 0, len(lines))
	for _, line := range lines[1:] {
		fields := bytes.Fields(line)
		if len(fields) > 0 {
			paths = append(paths, string(fields[0]))
		}
	}
	return paths, nil
}
