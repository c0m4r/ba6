// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func cmdUptime(args []string) int {
	pretty := false
	for _, arg := range args {
		if arg == "-p" || arg == "--pretty" {
			pretty = true
		} else {
			fatalf("uptime", "unsupported option %q", arg)
			return 1
		}
	}
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		fatalf("uptime", "%v", err)
		return 1
	}
	fields := strings.Fields(string(data))
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 1
	}
	d := time.Duration(seconds) * time.Second
	if pretty {
		days := int(d / (24 * time.Hour))
		hours := int(d/time.Hour) % 24
		mins := int(d/time.Minute) % 60
		parts := []string{}
		if days > 0 {
			parts = append(parts, fmt.Sprintf("%d day(s)", days))
		}
		if hours > 0 {
			parts = append(parts, fmt.Sprintf("%d hour(s)", hours))
		}
		parts = append(parts, fmt.Sprintf("%d minute(s)", mins))
		fmt.Println("up " + strings.Join(parts, ", "))
		return 0
	}
	load, _ := os.ReadFile("/proc/loadavg")
	loadFields := strings.Fields(string(load))
	loads := "?"
	if len(loadFields) >= 3 {
		loads = strings.Join(loadFields[:3], ", ")
	}
	fmt.Printf(" %s up %s, load average: %s\n", time.Now().Format("15:04:05"), formatUptime(d), loads)
	return 0
}
func formatUptime(d time.Duration) string {
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	mins := int(d/time.Minute) % 60
	if days > 0 {
		return fmt.Sprintf("%d days, %02d:%02d", days, hours, mins)
	}
	return fmt.Sprintf("%02d:%02d", hours, mins)
}

func cmdSync(args []string) int {
	if len(args) != 0 {
		fatalf("sync", "unexpected operand %q", args[0])
		return 1
	}
	syscall.Sync()
	return 0
}

func cmdReboot(args []string) int {
	return powerControl("reboot", args, syscall.LINUX_REBOOT_CMD_RESTART, syscall.SIGTERM)
}
func cmdPoweroff(args []string) int {
	return powerControl("poweroff", args, syscall.LINUX_REBOOT_CMD_POWER_OFF, syscall.SIGUSR2)
}
func cmdHalt(args []string) int {
	return powerControl("halt", args, syscall.LINUX_REBOOT_CMD_HALT, syscall.SIGUSR1)
}
func powerControl(prog string, args []string, action int, initSignal syscall.Signal) int {
	skipSync, force := false, false
	for _, arg := range args {
		switch arg {
		case "-n", "--no-sync":
			skipSync = true
		case "-f", "--force":
			force = true
		default:
			fatalf(prog, "unsupported option %q", arg)
			return 1
		}
	}
	if !force && os.Getpid() != 1 {
		if err := syscall.Kill(1, initSignal); err != nil {
			fatalf(prog, "signal init: %v", err)
			return 1
		}
		return 0
	}
	if !skipSync {
		syscall.Sync()
	}
	if err := syscall.Reboot(action); err != nil {
		fatalf(prog, "%v", err)
		return 1
	}
	return 0
}

func cmdDmesg(args []string) int {
	clear := false
	for _, a := range args {
		if a == "-c" || a == "--read-clear" {
			clear = true
		} else {
			fatalf("dmesg", "unsupported option %q", a)
			return 1
		}
	}
	size, _, errno := syscall.Syscall(syscall.SYS_SYSLOG, 10, 0, 0)
	if errno != 0 {
		fatalf("dmesg", "%v", errno)
		return 1
	}
	if size == 0 {
		return 0
	}
	buf := make([]byte, int(size)+1)
	action := uintptr(3)
	if clear {
		action = 4
	}
	n, _, errno := syscall.Syscall(syscall.SYS_SYSLOG, action, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))) //nolint:gosec // G103: kernel writes into this size-bounded byte slice.
	if errno != 0 {
		fatalf("dmesg", "%v", errno)
		return 1
	}
	_, err := os.Stdout.Write(buf[:n])
	if err != nil {
		return 1
	}
	return 0
}

func cmdPgrep(args []string) int { return processMatchCommand("pgrep", args, false) }
func cmdPkill(args []string) int { return processMatchCommand("pkill", args, true) }
func cmdPidof(args []string) int {
	if len(args) == 0 {
		fatalf("pidof", "missing program name")
		return 1
	}
	processes, err := readProcesses(nil)
	if err != nil {
		fatalf("pidof", "%v", err)
		return 1
	}
	status := 1
	for _, name := range args {
		matches := []string{}
		// readProcesses returns ascending PIDs; pidof prints the newest match
		// first, so walk the list backwards.
		for i := len(processes) - 1; i >= 0; i-- {
			p := processes[i]
			if p.comm == name || filepath.Base(strings.Fields(p.args)[0]) == name {
				matches = append(matches, strconv.Itoa(p.pid))
			}
		}
		if len(matches) > 0 {
			fmt.Println(strings.Join(matches, " "))
			status = 0
		}
	}
	return status
}
func processMatchCommand(prog string, args []string, send bool) int {
	full, exact, invert := false, false, false
	signal := syscall.SIGTERM
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		a := args[0]
		switch a {
		case "-f", "--full":
			full = true
		case "-x", "--exact":
			exact = true
		case "-v", "--inverse":
			invert = true
		case "-signal":
			if !send || len(args) < 2 {
				return 2
			}
			s, e := parseSignal(args[1])
			if e != nil {
				return 2
			}
			signal = s
			args = args[1:]
		default:
			if send {
				s, e := parseSignal(strings.TrimPrefix(a, "-"))
				if e == nil {
					signal = s
					args = args[1:]
					continue
				}
			}
			fatalf(prog, "unsupported option %q", a)
			return 2
		}
		args = args[1:]
	}
	if len(args) != 1 {
		fatalf(prog, "expected one pattern")
		return 2
	}
	re, err := regexp.Compile(args[0])
	if err != nil {
		fatalf(prog, "%v", err)
		return 2
	}
	processes, err := readProcesses(nil)
	if err != nil {
		return 2
	}
	matched := false
	for _, p := range processes {
		target := p.comm
		if full {
			target = p.args
		}
		ok := re.MatchString(target)
		if exact {
			ok = ok && re.FindString(target) == target
		}
		if invert {
			ok = !ok
		}
		if !ok || p.pid == os.Getpid() {
			continue
		}
		matched = true
		if send {
			if err := syscall.Kill(p.pid, signal); err != nil {
				fatalf(prog, "%d: %v", p.pid, err)
			}
		} else {
			fmt.Println(p.pid)
		}
	}
	if matched {
		return 0
	}
	return 1
}

func cmdMount(args []string) int {
	fstype, options := "", ""
	flags := uintptr(0)
	operands := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			i++
			if i >= len(args) {
				return 1
			}
			fstype = args[i]
		case "-o":
			i++
			if i >= len(args) {
				return 1
			}
			options = args[i]
		case "-r":
			flags |= syscall.MS_RDONLY
		case "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(args[i], "-") {
				fatalf("mount", "unsupported option %q", args[i])
				return 1
			}
			operands = append(operands, args[i])
		}
	}
	if len(operands) == 0 {
		return listMounts()
	}
	if len(operands) != 2 {
		fatalf("mount", "expected DEVICE DIRECTORY")
		return 1
	}
	for _, o := range strings.Split(options, ",") {
		switch o {
		case "ro":
			flags |= syscall.MS_RDONLY
		case "rw", "defaults", "":
		case "bind":
			flags |= syscall.MS_BIND
		case "remount":
			flags |= syscall.MS_REMOUNT
		case "nosuid":
			flags |= syscall.MS_NOSUID
		case "nodev":
			flags |= syscall.MS_NODEV
		case "noexec":
			flags |= syscall.MS_NOEXEC
		}
	}
	if err := syscall.Mount(operands[0], operands[1], fstype, flags, options); err != nil {
		fatalf("mount", "%v", err)
		return 1
	}
	return 0
}
func listMounts() int {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		fatalf("mount", "%v", err)
		return 1
	}
	os.Stdout.Write(data)
	return 0
}
func cmdUmount(args []string) int {
	flags, all, remountReadonly := 0, false, false
	targets := []string{}
	for _, a := range args {
		switch a {
		case "-l", "--lazy":
			flags |= syscall.MNT_DETACH
		case "-f", "--force":
			flags |= syscall.MNT_FORCE
		case "-a", "--all":
			all = true
		case "-r", "--read-only":
			remountReadonly = true
		default:
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
				for _, option := range strings.TrimPrefix(a, "-") {
					switch option {
					case 'l':
						flags |= syscall.MNT_DETACH
					case 'f':
						flags |= syscall.MNT_FORCE
					case 'a':
						all = true
					case 'r':
						remountReadonly = true
					default:
						fatalf("umount", "unsupported option -%c", option)
						return 1
					}
				}
				continue
			}
			if strings.HasPrefix(a, "-") {
				fatalf("umount", "unsupported option %q", a)
				return 1
			}
			targets = append(targets, a)
		}
	}
	if all {
		data, err := os.ReadFile("/proc/self/mounts")
		if err != nil {
			fatalf("umount", "%v", err)
			return 1
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] != "/" {
				targets = append(targets, decodeMountField(fields[1]))
			}
		}
		sort.SliceStable(targets, func(i, j int) bool { return len(targets[i]) > len(targets[j]) })
	}
	if len(targets) == 0 {
		fatalf("umount", "missing operand")
		return 1
	}
	status := 0
	for _, t := range targets {
		if err := syscall.Unmount(t, flags); err != nil {
			if remountReadonly && syscall.Mount("", t, "", syscall.MS_REMOUNT|syscall.MS_RDONLY, "") == nil {
				continue
			}
			fatalf("umount", "%s: %v", t, err)
			status = 1
		}
	}
	if all && remountReadonly {
		if err := syscall.Mount("", "/", "", syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
			fatalf("umount", "/: %v", err)
			status = 1
		}
	}
	return status
}

func cmdMknod(args []string) int {
	mode := uint32(0o666)
	if len(args) > 0 && args[0] == "-m" {
		if len(args) < 3 {
			return 1
		}
		n, e := strconv.ParseUint(args[1], 8, 32)
		if e != nil {
			return 1
		}
		mode = uint32(n)
		args = args[2:]
	}
	if len(args) < 2 {
		fatalf("mknod", "missing operand")
		return 1
	}
	name, kind := args[0], args[1]
	var bits uint32
	var dev int
	switch kind {
	case "p":
		bits = syscall.S_IFIFO
		if len(args) != 2 {
			return 1
		}
	case "b", "c", "u":
		if len(args) != 4 {
			return 1
		}
		major, e1 := strconv.ParseUint(args[2], 10, 32)
		minor, e2 := strconv.ParseUint(args[3], 10, 32)
		if e1 != nil || e2 != nil {
			return 1
		}
		dev = int(linuxMakeDevice(uint32(major), uint32(minor))) //nolint:gosec // G115: ParseUint bounded both values to 32 bits.
		if kind == "b" {
			bits = syscall.S_IFBLK
		} else {
			bits = syscall.S_IFCHR
		}
	default:
		fatalf("mknod", "invalid type %q", kind)
		return 1
	}
	if err := syscall.Mknod(name, bits|mode, dev); err != nil {
		fatalf("mknod", "%v", err)
		return 1
	}
	return 0
}
func linuxMakeDevice(major, minor uint32) uint64 {
	return uint64(minor&0xff) | uint64(major&0xfff)<<8 |
		uint64(minor&^0xff)<<12 | uint64(major&^0xfff)<<32
}

func cmdBlkid(args []string) int {
	devices := args
	if len(devices) == 0 {
		matches, _ := filepath.Glob("/dev/*")
		for _, p := range matches {
			if info, e := os.Stat(p); e == nil && info.Mode()&os.ModeDevice != 0 {
				devices = append(devices, p)
			}
		}
	}
	status := 0
	for _, path := range devices {
		kind, label, uuid, err := probeFilesystem(path)
		if err != nil || kind == "" {
			status = 2
			continue
		}
		fmt.Printf("%s:", path)
		if label != "" {
			fmt.Printf(" LABEL=%q", label)
		}
		if uuid != "" {
			fmt.Printf(" UUID=%q", uuid)
		}
		fmt.Printf(" TYPE=%q\n", kind)
	}
	return status
}
func probeFilesystem(path string) (string, string, string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", "", "", e
	}
	defer f.Close()
	buf := make([]byte, 4096)
	_, e = f.ReadAt(buf, 0)
	if e != nil && len(buf) == 0 {
		return "", "", "", e
	}
	if len(buf) >= 1160 && buf[1080] == 0x53 && buf[1081] == 0xef {
		uuid := formatUUID(buf[1128:1144])
		label := strings.TrimRight(string(buf[1144:1160]), "\x00 ")
		compat := binary.LittleEndian.Uint32(buf[1116:1120])
		incompat := binary.LittleEndian.Uint32(buf[1120:1124])
		kind := "ext2"
		if compat&0x4 != 0 {
			kind = "ext3"
		}
		if incompat&0x40 != 0 {
			kind = "ext4"
		}
		return kind, label, uuid, nil
	}
	if string(buf[:4]) == "XFSB" {
		return "xfs", strings.TrimRight(string(buf[0x6c:0x78]), "\x00 "), formatUUID(buf[32:48]), nil
	}
	// btrfs keeps its superblock at 64 KiB so that a boot loader can live
	// in front of it, so it needs a second read.
	super := make([]byte, 4096)
	if _, e := f.ReadAt(super, 65536); e == nil && string(super[64:72]) == "_BHRfS_M" {
		return "btrfs", strings.TrimRight(string(super[299:555]), "\x00 "), formatUUID(super[32:48]), nil
	}
	if string(buf[3:11]) == "NTFS    " {
		return "ntfs", "", "", nil
	}
	if string(buf[54:62]) == "FAT16   " || string(buf[82:90]) == "FAT32   " {
		return "vfat", "", "", nil
	}
	if len(buf) >= 10 && string(buf[:6]) == "LUKS\xba\xbe" {
		return "crypto_LUKS", "", "", nil
	}
	tail := make([]byte, 10)
	if st, e := f.Stat(); e == nil && st.Size() >= 10 {
		_, _ = f.ReadAt(tail, st.Size()-10)
		if string(tail) == "SWAPSPACE2" {
			return "swap", "", "", nil
		}
	}
	return "", "", "", nil
}
func formatUUID(b []byte) string {
	if len(b) < 16 {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", binary.BigEndian.Uint32(b[:4]), binary.BigEndian.Uint16(b[4:6]), binary.BigEndian.Uint16(b[6:8]), binary.BigEndian.Uint16(b[8:10]), b[10:16])
}
