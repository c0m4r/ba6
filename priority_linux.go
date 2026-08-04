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

func cmdLosetup(args []string) int {
	find, show, readOnly := false, false, false
	offset, sizeLimit := uint64(0), uint64(0)
	var detach string
	var operands []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-a", "--all":
			return listLoopDevices()
		case "-f", "--find":
			find = true
		case "--show":
			show = true
		case "-r", "--read-only":
			readOnly = true
		case "-d", "--detach":
			i++
			if i >= len(args) {
				return 1
			}
			detach = args[i]
		case "-o", "--offset", "--sizelimit":
			option := args[i]
			i++
			if i >= len(args) {
				return 1
			}
			value, err := parseDDNumber(args[i])
			if err != nil || value < 0 {
				fatalf("losetup", "invalid value %q", args[i])
				return 1
			}
			if option == "--sizelimit" {
				sizeLimit = uint64(value)
			} else {
				offset = uint64(value)
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				fatalf("losetup", "unsupported option %q", args[i])
				return 1
			}
			operands = append(operands, args[i])
		}
	}
	if detach != "" {
		fd, err := syscall.Open(detach, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err == nil {
			err = loopIoctl(fd, loopClearFD, 0)
			_ = syscall.Close(fd)
		}
		if err != nil {
			fatalf("losetup", "%s: %v", detach, err)
			return 1
		}
		return 0
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
	return "", fmt.Errorf("no unused loop device")
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

func listLoopDevices() int {
	entries, err := filepath.Glob("/sys/class/block/loop*")
	if err != nil {
		return 1
	}
	for _, entry := range entries {
		backing := readTrimmed(filepath.Join(entry, "loop/backing_file"))
		if backing != "" {
			fmt.Printf("/dev/%s: (%s)\n", filepath.Base(entry), backing)
		}
	}
	return 0
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
