// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

type blockDevice struct {
	name, majorMinor, kind, mountpoint string
	parent                             string
	removable, readOnly                bool
	bytes                              uint64
}

func cmdLsblk(args []string) int {
	bytesOutput, noHeadings := false, false
	columns := []string{"NAME", "MAJ:MIN", "RM", "SIZE", "RO", "TYPE", "MOUNTPOINTS"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-b", "--bytes":
			bytesOutput = true
		case "-n", "--noheadings":
			noHeadings = true
		case "-o", "--output":
			i++
			if i >= len(args) {
				fatalf("lsblk", "-o requires a column list")
				return 1
			}
			columns = strings.Split(strings.ToUpper(args[i]), ",")
		case "-a", "--all":
		default:
			fatalf("lsblk", "unsupported option or operand %q", args[i])
			return 1
		}
	}
	valid := map[string]bool{"NAME": true, "KNAME": true, "MAJ:MIN": true, "RM": true, "SIZE": true, "RO": true, "TYPE": true, "MOUNTPOINT": true, "MOUNTPOINTS": true}
	for _, column := range columns {
		if !valid[column] {
			fatalf("lsblk", "unknown column %q", column)
			return 1
		}
	}
	devices, err := readBlockDevices("/sys/class/block", "/proc/self/mountinfo")
	if err != nil {
		fatalf("lsblk", "%v", err)
		return 1
	}
	if !noHeadings {
		fmt.Println(strings.Join(columns, " "))
	}
	children := make(map[string][]blockDevice)
	var roots []blockDevice
	for _, device := range devices {
		if device.parent == "" {
			roots = append(roots, device)
		} else {
			children[device.parent] = append(children[device.parent], device)
		}
	}
	var printDevice func(blockDevice, string)
	printDevice = func(device blockDevice, prefix string) {
		values := make([]string, 0, len(columns))
		for _, column := range columns {
			switch column {
			case "NAME":
				values = append(values, prefix+device.name)
			case "KNAME":
				values = append(values, device.name)
			case "MAJ:MIN":
				values = append(values, device.majorMinor)
			case "RM":
				values = append(values, boolDigit(device.removable))
			case "SIZE":
				if bytesOutput {
					values = append(values, strconv.FormatUint(device.bytes, 10))
				} else {
					values = append(values, humanSizeUint64(device.bytes))
				}
			case "RO":
				values = append(values, boolDigit(device.readOnly))
			case "TYPE":
				values = append(values, device.kind)
			case "MOUNTPOINT", "MOUNTPOINTS":
				values = append(values, device.mountpoint)
			}
		}
		fmt.Println(strings.Join(values, " "))
		for _, child := range children[device.name] {
			printDevice(child, prefix+"  ")
		}
	}
	for _, root := range roots {
		printDevice(root, "")
	}
	return 0
}

func boolDigit(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func readBlockDevices(sysRoot, mountInfoPath string) ([]blockDevice, error) {
	entries, err := os.ReadDir(sysRoot)
	if err != nil {
		return nil, err
	}
	mountsByName, mountsByDevice := map[string][]string{}, map[string][]string{}
	if data, readErr := os.ReadFile(mountInfoPath); readErr == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			separator := -1
			for i, field := range fields {
				if field == "-" {
					separator = i
					break
				}
			}
			if separator > 0 && separator+2 < len(fields) && len(fields) > 4 {
				mountpoint := unescapeMountField(fields[4])
				name := filepath.Base(unescapeMountField(fields[separator+2]))
				mountsByName[name] = append(mountsByName[name], mountpoint)
				if len(fields) > 2 {
					mountsByDevice[fields[2]] = append(mountsByDevice[fields[2]], mountpoint)
				}
			}
		}
	}
	devices := make([]blockDevice, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(sysRoot, name)
		device := blockDevice{name: name, kind: "disk"}
		device.majorMinor = readTrimmed(filepath.Join(path, "dev"))
		mountpoints := mountsByDevice[device.majorMinor]
		if len(mountpoints) == 0 {
			mountpoints = mountsByName[name]
		}
		device.mountpoint = strings.Join(mountpoints, ",")
		sectors, _ := strconv.ParseUint(readTrimmed(filepath.Join(path, "size")), 10, 64)
		device.bytes = sectors * 512
		device.removable = readTrimmed(filepath.Join(path, "removable")) == "1"
		device.readOnly = readTrimmed(filepath.Join(path, "ro")) == "1"
		if _, statErr := os.Stat(filepath.Join(path, "partition")); statErr == nil {
			device.kind = "part"
			if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
				device.parent = filepath.Base(filepath.Dir(resolved))
			}
			if device.parent == name || device.parent == "block" {
				device.parent = inferBlockParent(name)
			}
		} else if strings.HasPrefix(name, "loop") {
			device.kind = "loop"
		} else if strings.HasPrefix(name, "sr") {
			device.kind = "rom"
		} else if strings.HasPrefix(name, "dm-") {
			device.kind = "dm"
			uuid := readTrimmed(filepath.Join(path, "dm/uuid"))
			switch {
			case strings.HasPrefix(uuid, "LVM-"):
				device.kind = "lvm"
			case strings.HasPrefix(uuid, "CRYPT-"):
				device.kind = "crypt"
			}
		} else if level := readTrimmed(filepath.Join(path, "md/level")); level != "" {
			device.kind = level
		}
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].name < devices[j].name })
	return devices, nil
}

func inferBlockParent(name string) string {
	if position := strings.LastIndex(name, "p"); position > 0 && position+1 < len(name) {
		if _, err := strconv.Atoi(name[position+1:]); err == nil {
			return name[:position]
		}
	}
	return strings.TrimRight(name, "0123456789")
}

func readTrimmed(path string) string {
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}

const (
	loopSetFD      = 0x4c00
	loopClearFD    = 0x4c01
	loopSetStatus  = 0x4c04
	loopGetStatus  = 0x4c05
	loopCtlGetFree = 0x4c82
	loopReadOnly   = 1
)

type loopInfo64 struct {
	Device, Inode, Rdevice, Offset, SizeLimit  uint64
	Number, EncryptType, EncryptKeySize, Flags uint32
	FileName, CryptName                        [64]uint8
	EncryptKey                                 [32]uint8
	Init                                       [2]uint64
}

// loopDevice is what sysfs knows about one loop device.
type loopDevice struct {
	name        string
	backingFile string
	offset      string
	sizeLimit   string
	autoclear   string
	readOnly    string
	dio         string
	logSector   string
}

// readLoopDevices lists the configured loop devices in the order sysfs gives
// them, which is the order the original lists them in.
func readLoopDevices() []loopDevice {
	names, err := readDirRaw("/sys/block")
	if err != nil {
		return nil
	}
	var devices []loopDevice
	for _, name := range names {
		if !strings.HasPrefix(name, "loop") {
			continue
		}
		base := filepath.Join("/sys/block", name)
		backing := readTrimmed(filepath.Join(base, "loop/backing_file"))
		if backing == "" {
			continue
		}
		devices = append(devices, loopDevice{
			name:        "/dev/" + name,
			backingFile: backing,
			offset:      readTrimmedOr(filepath.Join(base, "loop/offset"), "0"),
			sizeLimit:   readTrimmedOr(filepath.Join(base, "loop/sizelimit"), "0"),
			autoclear:   readTrimmedOr(filepath.Join(base, "loop/autoclear"), "0"),
			readOnly:    readTrimmedOr(filepath.Join(base, "ro"), "0"),
			dio:         readTrimmedOr(filepath.Join(base, "loop/dio"), "0"),
			logSector:   readTrimmedOr(filepath.Join(base, "queue/logical_block_size"), "512"),
		})
	}
	return devices
}

func readTrimmedOr(path, fallback string) string {
	if value := readTrimmed(path); value != "" {
		return value
	}
	return fallback
}

// loopColumns are the columns the table knows, in the order the default
// listing prints them.
var loopColumns = []string{"NAME", "SIZELIMIT", "OFFSET", "AUTOCLEAR", "RO", "BACK-FILE", "DIO", "LOG-SEC"}

func loopColumnValue(device loopDevice, column string) string {
	switch column {
	case "NAME":
		return device.name
	case "SIZELIMIT":
		return device.sizeLimit
	case "OFFSET":
		return device.offset
	case "AUTOCLEAR":
		return device.autoclear
	case "RO":
		return device.readOnly
	case "BACK-FILE":
		return device.backingFile
	case "DIO":
		return device.dio
	case "LOG-SEC":
		return device.logSector
	}
	return ""
}

// loopColumnLeft says which columns hold text and are left-aligned.
func loopColumnLeft(column string) bool { return column == "NAME" || column == "BACK-FILE" }

// showLoopTable prints the table form, whose columns are sized from the widest
// value in each and whose last left-aligned column carries no padding.
func showLoopTable(devices []loopDevice, columns []string, headings, raw bool) int {
	rows := make([][]string, 0, len(devices))
	for _, device := range devices {
		row := make([]string, 0, len(columns))
		for _, column := range columns {
			row = append(row, loopColumnValue(device, column))
		}
		rows = append(rows, row)
	}
	widths := make([]int, len(columns))
	for i, column := range columns {
		if headings {
			widths[i] = len(column)
		}
		for _, row := range rows {
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
	}
	write := func(row []string) {
		fields := make([]string, 0, len(row))
		for i, value := range row {
			if raw || (i == len(row)-1 && loopColumnLeft(columns[i])) {
				fields = append(fields, value)
				continue
			}
			if loopColumnLeft(columns[i]) {
				fields = append(fields, fmt.Sprintf("%-*s", widths[i], value))
			} else {
				fields = append(fields, fmt.Sprintf("%*s", widths[i], value))
			}
		}
		fmt.Println(strings.Join(fields, " "))
	}
	if headings && len(rows) > 0 {
		write(columns)
	}
	for _, row := range rows {
		write(row)
	}
	return 0
}

func losetupUsage(format string, a ...interface{}) int {
	fatalf("losetup", format, a...)
	fmt.Fprintln(os.Stderr, "Try 'losetup --help' for more information.")
	return 1
}

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func cmdLosetup(args []string) int {
	args = expandShortOptions(args, "odjObv")
	find, show, readOnly := false, false, false
	list, all, headings, raw := false, false, true, false
	// --noheadings only qualifies a listing someone asked for; on its own, or
	// beside --raw alone, it leaves the command with nothing to do, which the
	// original calls a missing device.
	sawNoHeadings, rawOnly := false, false
	partscan, directIO := false, false
	offset, sizeLimit := uint64(0), uint64(0)
	sectorSize := uint64(0)
	columns := loopColumns
	var detach []string
	var associated string
	var operands []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
		}
		needValue := func() (string, bool) {
			if hasValue {
				return value, true
			}
			i++
			if i >= len(args) {
				losetupUsage("option '%s' requires an argument", name)
				return "", false
			}
			return args[i], true
		}
		switch name {
		case "-a", "--all":
			all = true
		case "-l", "--list":
			list = true
		case "-n", "--noheadings":
			headings, sawNoHeadings = false, true
		case "--raw":
			// --raw asks for the table, as -l and -O do; only --noheadings on
			// its own leaves the command without anything to do.
			raw, rawOnly = true, true
		case "-f", "--find":
			find = true
		case "--show":
			show = true
		case "-r", "--read-only":
			readOnly = true
		case "-P", "--partscan":
			partscan = true
		case "--direct-io":
			directIO = true
			if hasValue && value == "off" {
				directIO = false
			}
		case "-v", "--verbose", "-c", "--set-capacity", "--nooverlap", "-L", "--loop-ref":
			// Nothing here caches a capacity or keeps a reference name.
		case "-D", "--detach-all":
			for _, device := range readLoopDevices() {
				detach = append(detach, device.name)
			}
		case "-d", "--detach":
			text, ok := needValue()
			if !ok {
				return 1
			}
			detach = append(detach, text)
		case "-j", "--associated":
			text, ok := needValue()
			if !ok {
				return 1
			}
			associated = text
		case "-O", "--output":
			text, ok := needValue()
			if !ok {
				return 1
			}
			columns = nil
			for _, column := range strings.Split(strings.ToUpper(text), ",") {
				known := false
				for _, candidate := range loopColumns {
					if candidate == column {
						known = true
					}
				}
				if !known {
					fatalf("losetup", "unknown column: %s", column)
					return 1
				}
				columns = append(columns, column)
			}
			list = true
		case "-b", "--sector-size":
			text, ok := needValue()
			if !ok {
				return 1
			}
			parsed, err := parseByteSize(text)
			if err != nil {
				fatalf("losetup", "invalid value %q", text)
				return 1
			}
			sectorSize = parsed
		case "-o", "--offset", "--sizelimit":
			text, ok := needValue()
			if !ok {
				return 1
			}
			parsed, err := parseDDNumber(text)
			if err != nil || parsed < 0 {
				fatalf("losetup", "invalid value %q", text)
				return 1
			}
			if name == "--sizelimit" {
				sizeLimit = uint64(parsed) //nolint:gosec // G115: guarded by the sign test.
			} else {
				offset = uint64(parsed) //nolint:gosec // G115: same.
			}
		default:
			if strings.HasPrefix(arg, "-") && arg != "-" {
				if strings.HasPrefix(arg, "--") {
					return losetupUsage("unrecognized option '%s'", arg)
				}
				return losetupUsage("invalid option -- '%c'", arg[1])
			}
			operands = append(operands, arg)
		}
	}
	// -P, --direct-io and -b change a device after it is attached, which needs
	// privileges this build cannot exercise anywhere it is tested, so they are
	// accepted and left to the kernel's defaults.
	_, _, _ = partscan, directIO, sectorSize
	if len(detach) > 0 {
		status := 0
		for _, device := range detach {
			fd, err := syscall.Open(device, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
			if err == nil {
				err = loopIoctl(fd, loopClearFD, 0)
				_ = syscall.Close(fd)
			}
			if err != nil {
				// util-linux words this one against the operation it tried.
				fatalf("losetup", "%s: detach failed: %s", device, errText(err))
				status = 1
			}
		}
		return status
	}
	if rawOnly && !sawNoHeadings {
		// --raw on its own asks for the table.
		list = true
	}
	devices := readLoopDevices()
	if associated != "" {
		// -j keeps only the devices backed by that file.
		resolved := associated
		if absolute, err := filepath.Abs(associated); err == nil {
			resolved = absolute
		}
		filtered := devices[:0]
		for _, device := range devices {
			if device.backingFile == resolved || device.backingFile == associated {
				filtered = append(filtered, device)
			}
		}
		devices = filtered
		if !list {
			return listLoopDevices(devices)
		}
	}
	switch {
	case all:
		return listLoopDevices(devices)
	case list:
		return showLoopTable(devices, columns, headings, raw)
	case len(operands) == 0 && !find && sawNoHeadings && !list:
		fatalf("losetup", "no loop device specified")
		return 1
	case len(operands) == 0 && !find:
		return showLoopTable(devices, columns, headings, raw)
	}
	if find {
		device, err := findFreeLoop()
		if err != nil {
			fatalf("losetup", "%v", err)
			return 1
		}
		if len(operands) == 0 {
			fmt.Println(device)
			return 0
		}
		operands = append([]string{device}, operands...)
	}
	if len(operands) == 1 && !find {
		return showLoopDevice(operands[0])
	}
	if len(operands) != 2 {
		fatalf("losetup", "expected LOOPDEV FILE")
		return 1
	}
	if err := attachLoop(operands[0], operands[1], offset, sizeLimit, readOnly); err != nil {
		fatalf("losetup", "%v", err)
		return 1
	}
	if show {
		fmt.Println(operands[0])
	}
	return 0
}

func findFreeLoop() (string, error) {
	if fd, err := syscall.Open("/dev/loop-control", syscall.O_RDWR|syscall.O_CLOEXEC, 0); err == nil {
		number, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), loopCtlGetFree, 0)
		_ = syscall.Close(fd)
		if errno == 0 {
			return fmt.Sprintf("/dev/loop%d", number), nil
		}
	}
	for number := 0; number < 256; number++ {
		path := fmt.Sprintf("/dev/loop%d", number)
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		var info loopInfo64
		err = loopIoctl(fd, loopGetStatus, uintptr(unsafe.Pointer(&info))) //nolint:gosec // G103: fixed kernel ABI struct.
		_ = syscall.Close(fd)
		if errors.Is(err, syscall.ENXIO) {
			return path, nil
		}
	}
	// util-linux words this one against the reason the search failed, which
	// for an unprivileged caller is the refusal to open the control device.
	if _, err := os.Stat("/dev/loop-control"); err == nil {
		if fd, openErr := syscall.Open("/dev/loop-control", syscall.O_RDWR|syscall.O_CLOEXEC, 0); openErr != nil {
			return "", fmt.Errorf("cannot find an unused loop device: %s", errText(openErr))
		} else if closeErr := syscall.Close(fd); closeErr != nil {
			return "", fmt.Errorf("cannot find an unused loop device: %s", errText(closeErr))
		}
	}
	return "", fmt.Errorf("cannot find an unused loop device")
}

func attachLoop(device, backing string, offset, sizeLimit uint64, readOnly bool) error {
	fileFlags, loopFlags := syscall.O_RDWR|syscall.O_CLOEXEC, syscall.O_RDWR|syscall.O_CLOEXEC
	if readOnly {
		fileFlags, loopFlags = syscall.O_RDONLY|syscall.O_CLOEXEC, syscall.O_RDONLY|syscall.O_CLOEXEC
	}
	backingFD, err := syscall.Open(backing, fileFlags, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", backing, err)
	}
	defer syscall.Close(backingFD)
	loopFD, err := syscall.Open(device, loopFlags, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", device, err)
	}
	defer syscall.Close(loopFD)
	if err := loopIoctl(loopFD, loopSetFD, uintptr(backingFD)); err != nil {
		return err
	}
	info := loopInfo64{Offset: offset, SizeLimit: sizeLimit}
	if readOnly {
		info.Flags |= loopReadOnly
	}
	copy(info.FileName[:], backing)
	if err := loopIoctl(loopFD, loopSetStatus, uintptr(unsafe.Pointer(&info))); err != nil { //nolint:gosec // G103: fixed kernel ABI struct.
		_ = loopIoctl(loopFD, loopClearFD, 0)
		return err
	}
	return nil
}

func loopIoctl(fd int, request, argument uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, argument)
	if errno != 0 {
		return errno
	}
	return nil
}

// listLoopDevices prints the -a form: the device, the backing file's device
// and inode in brackets — which stay empty when the ioctl that carries them is
// refused, as it is for an unprivileged caller — and the file itself.
func listLoopDevices(devices []loopDevice) int {
	for _, device := range devices {
		deviceNumber, inode := loopBackingIdentity(device.name)
		line := fmt.Sprintf("%s: [%s]:%s (%s)", device.name, deviceNumber, inode, device.backingFile)
		if device.offset != "0" {
			line += ", offset " + device.offset
		}
		if device.sizeLimit != "0" {
			line += ", sizelimit " + device.sizeLimit
		}
		fmt.Println(line)
	}
	return 0
}

// loopBackingIdentity asks the kernel which file backs a loop device. The
// answer needs the device open, so an unprivileged run comes back empty and
// the brackets are printed empty, exactly as the original prints them.
func loopBackingIdentity(device string) (string, string) {
	fd, err := syscall.Open(device, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", ""
	}
	defer syscall.Close(fd) //nolint:errcheck // the descriptor is only read from.
	var info loopInfo64
	if err := loopIoctl(fd, loopGetStatus, uintptr(unsafe.Pointer(&info))); err != nil { //nolint:gosec // G103: fixed kernel ABI struct.
		return "", ""
	}
	return fmt.Sprintf("%4d", info.Device), strconv.FormatUint(info.Inode, 10)
}

func showLoopDevice(device string) int {
	backing := readTrimmed(filepath.Join("/sys/class/block", filepath.Base(device), "loop/backing_file"))
	if backing == "" {
		fatalf("losetup", "%s: not configured", device)
		return 1
	}
	fmt.Printf("%s: (%s)\n", device, backing)
	return 0
}

func cmdChroot(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fatalf("chroot", "missing new root")
		return 1
	}
	root, command := args[0], args[1:]
	if len(command) == 0 {
		command = []string{"/bin/sh", "-i"}
	}
	if err := syscall.Chroot(root); err != nil {
		fatalf("chroot", "%s: %v", root, err)
		return 1
	}
	if err := os.Chdir("/"); err != nil {
		fatalf("chroot", "%v", err)
		return 1
	}
	path, err := exec.LookPath(command[0])
	if err != nil {
		fatalf("chroot", "%v", err)
		return 127
	}
	if err := syscall.Exec(path, command, os.Environ()); err != nil { //nolint:gosec // G204: chroot intentionally executes the selected command.
		fatalf("chroot", "%v", err)
		return 126
	}
	return 0
}

// cmdPivotRoot is a thin wrapper around the pivot_root(2) syscall, matching
// util-linux's pivot_root(8): it moves the root filesystem to PUT_OLD and
// makes NEW_ROOT the new root. It does not chdir or exec anything; callers
// are responsible for the chroot/chdir dance the syscall's man page
// describes.
func cmdPivotRoot(args []string) int {
	if len(args) != 2 {
		fatalf("pivot_root", "expected NEW_ROOT PUT_OLD")
		return 1
	}
	newRoot, putOld := args[0], args[1]
	if err := syscall.PivotRoot(newRoot, putOld); err != nil {
		fatalf("pivot_root", "failed to change root from %q to %q: %v", newRoot, putOld, err)
		return 1
	}
	return 0
}

func cmdSwitchRoot(args []string) int {
	if len(args) < 2 {
		fatalf("switch_root", "expected NEW_ROOT NEW_INIT [ARG]...")
		return 1
	}
	if os.Getpid() != 1 {
		fatalf("switch_root", "must be run as PID 1")
		return 1
	}
	newRoot, initArgs := filepath.Clean(args[0]), args[1:]
	info, err := os.Stat(newRoot)
	if err != nil || !info.IsDir() {
		fatalf("switch_root", "%s is not a directory", newRoot)
		return 1
	}
	if err := syscall.Mount(newRoot, newRoot, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		fatalf("switch_root", "make new root a mount point: %v", err)
		return 1
	}
	putOld := filepath.Join(newRoot, ".ba6-old-root")
	if err := os.Mkdir(putOld, 0o700); err != nil {
		fatalf("switch_root", "%v", err)
		return 1
	}
	if err := syscall.PivotRoot(newRoot, putOld); err != nil {
		fatalf("switch_root", "pivot root: %v", err)
		return 1
	}
	if err := os.Chdir("/"); err != nil {
		fatalf("switch_root", "%v", err)
		return 1
	}
	oldRoot := "/.ba6-old-root"
	for _, name := range []string{"dev", "proc", "sys", "run"} {
		source, target := filepath.Join(oldRoot, name), filepath.Join("/", name)
		if _, err := os.Stat(source); err == nil {
			_ = os.MkdirAll(target, 0o755) //nolint:gosec // API filesystem mountpoints must be traversable system-wide.
			_ = syscall.Mount(source, target, "", syscall.MS_MOVE, "")
		}
	}
	if err := syscall.Unmount(oldRoot, syscall.MNT_DETACH); err != nil {
		fatalf("switch_root", "detach old root: %v", err)
		return 1
	}
	_ = os.Remove(oldRoot)
	path, err := exec.LookPath(initArgs[0])
	if err != nil {
		fatalf("switch_root", "%v", err)
		return 127
	}
	if err := syscall.Exec(path, initArgs, os.Environ()); err != nil { //nolint:gosec // G204: switch_root intentionally launches init.
		fatalf("switch_root", "%v", err)
		return 126
	}
	return 0
}
