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

// dmesgFacilityNames and dmesgLevelNames are the syslog facility/priority
// tables dmesg -x decodes and -f/-l match against, in the fixed numeric order
// the kernel's <facility*8+level> priority value encodes them.
var dmesgFacilityNames = []string{
	"kern", "user", "mail", "daemon", "auth", "syslog", "lpr", "news",
	"uucp", "cron", "authpriv", "ftp", "res0", "res1", "res2", "res3",
	"local0", "local1", "local2", "local3", "local4", "local5", "local6", "local7",
}
var dmesgLevelNames = []string{"emerg", "alert", "crit", "err", "warn", "notice", "info", "debug"}

func dmesgFacilityName(n int) string {
	if n >= 0 && n < len(dmesgFacilityNames) {
		return dmesgFacilityNames[n]
	}
	return strconv.Itoa(n)
}

func dmesgLevelName(n int) string {
	if n >= 0 && n < len(dmesgLevelNames) {
		return dmesgLevelNames[n]
	}
	return strconv.Itoa(n)
}

func dmesgLevelNum(name string) (int, bool) {
	for i, n := range dmesgLevelNames {
		if n == name {
			return i, true
		}
	}
	if v, err := strconv.Atoi(name); err == nil && v >= 0 && v < len(dmesgLevelNames) {
		return v, true
	}
	return 0, false
}

func dmesgFacilityNum(name string) (int, bool) {
	for i, n := range dmesgFacilityNames {
		if n == name {
			return i, true
		}
	}
	if v, err := strconv.Atoi(name); err == nil && v >= 0 {
		return v, true
	}
	return 0, false
}

// parseDmesgLevels parses a comma-separated -l/--level spec. A trailing '+'
// on a level name widens the match to that level and everything more severe
// (lower-numbered), matching dmesg(1)'s documented "err+" form.
func parseDmesgLevels(spec string) (map[int]bool, error) {
	allowed := map[int]bool{}
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		widen := strings.HasSuffix(tok, "+")
		name := strings.TrimSuffix(tok, "+")
		lvl, ok := dmesgLevelNum(name)
		if !ok {
			return nil, fmt.Errorf("unknown level '%s'", name)
		}
		if widen {
			for i := 0; i <= lvl; i++ {
				allowed[i] = true
			}
		} else {
			allowed[lvl] = true
		}
	}
	return allowed, nil
}

func parseDmesgFacilities(spec string) (map[int]bool, error) {
	allowed := map[int]bool{}
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		fac, ok := dmesgFacilityNum(tok)
		if !ok {
			return nil, fmt.Errorf("unknown facility '%s'", tok)
		}
		allowed[fac] = true
	}
	return allowed, nil
}

type dmesgLine struct {
	facility int
	level    int
	sec      int64
	usec     int64
	hasTS    bool
	text     string
	raw      string
}

// parseDmesgLine splits one syslog(2)-format record, "<PRI>[sec.usec] text",
// into its priority, monotonic timestamp and message. Lines that do not
// match (as from an arbitrary -F file) pass through as plain text at the
// kern/info default priority.
func parseDmesgLine(line string) dmesgLine {
	l := dmesgLine{facility: 0, level: 6, hasTS: true, raw: line, text: line}
	if len(line) < 2 || line[0] != '<' {
		return l
	}
	end := strings.IndexByte(line, '>')
	if end < 1 {
		return l
	}
	pri, err := strconv.Atoi(line[1:end])
	if err != nil {
		return l
	}
	l.facility, l.level = pri/8, pri%8
	rest := line[end+1:]
	l.text = rest
	if rest == "" || rest[0] != '[' {
		return l
	}
	closeIdx := strings.IndexByte(rest, ']')
	if closeIdx < 0 {
		return l
	}
	secStr, usecStr, ok := strings.Cut(strings.TrimSpace(rest[1:closeIdx]), ".")
	if !ok {
		return l
	}
	sec, err1 := strconv.ParseInt(strings.TrimSpace(secStr), 10, 64)
	usec, err2 := strconv.ParseInt(usecStr, 10, 64)
	if err1 != nil || err2 != nil {
		return l
	}
	l.sec, l.usec, l.hasTS = sec, usec, true
	l.text = strings.TrimPrefix(rest[closeIdx+1:], " ")
	return l
}

// dmesgBootInstant estimates the wall-clock instant CLOCK_MONOTONIC's zero
// point corresponds to, the same delta dmesg(1) documents using for -T/-e:
// "adjusted according to current delta between boottime and monotonic
// clocks, this works only for messages printed after last resume." Unlike
// /proc/uptime (CLOCK_BOOTTIME, which keeps ticking across a suspend),
// CLOCK_MONOTONIC freezes during suspend just as kernel log timestamps do,
// so this offset stays consistent with them until the next resume shifts it.
func dmesgBootInstant() (time.Time, bool) {
	var ts syscall.Timespec
	const clockMonotonic = 1
	if _, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockMonotonic, uintptr(unsafe.Pointer(&ts)), 0); errno != 0 { //nolint:gosec // G103: fixed-size Timespec, kernel writes only within it
		return time.Time{}, false
	}
	monotonic := time.Duration(ts.Sec)*time.Second + time.Duration(ts.Nsec)*time.Nanosecond
	return time.Now().Add(-monotonic), true
}

func parseDmesgTime(spec string) (time.Time, error) {
	spec = strings.TrimSpace(spec)
	if fields := strings.Fields(spec); len(fields) == 3 && fields[2] == "ago" {
		n, err := strconv.ParseFloat(fields[0], 64)
		if err == nil {
			var unit time.Duration
			switch strings.TrimSuffix(fields[1], "s") {
			case "second":
				unit = time.Second
			case "minute":
				unit = time.Minute
			case "hour":
				unit = time.Hour
			case "day":
				unit = 24 * time.Hour
			case "week":
				unit = 7 * 24 * time.Hour
			default:
				return time.Time{}, fmt.Errorf("unknown time unit '%s'", fields[1])
			}
			return time.Now().Add(-time.Duration(n * float64(unit))), nil
		}
	}
	layouts := []string{
		"2006-01-02 15:04:05.999999", "2006-01-02T15:04:05.999999",
		"2006-01-02 15:04:05", "2006-01-02T15:04:05",
		"2006-01-02", "15:04:05",
	}
	now := time.Now()
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, spec, time.Local)
		if err != nil {
			continue
		}
		if layout == "15:04:05" {
			t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid time '%s'", spec)
}

func dmesgSyslog(action, size uintptr) ([]byte, error) {
	if size == 0 {
		n, _, errno := syscall.Syscall(syscall.SYS_SYSLOG, 10, 0, 0)
		if errno != 0 {
			return nil, errno
		}
		size = n + 1
	}
	buf := make([]byte, size)
	if len(buf) == 0 {
		return nil, nil
	}
	n, _, errno := syscall.Syscall(syscall.SYS_SYSLOG, action, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))) //nolint:gosec // G103: kernel writes into this size-bounded byte slice.
	if errno != 0 {
		return nil, errno
	}
	return buf[:n], nil
}

func cmdDmesg(args []string) int {
	args = expandShortOptions(args, "lfnsF")
	var (
		clearAfter, clearOnly, raw, notime, decode, ctimeFmt, isoFmt bool
		kernelOnly, userspaceOnly                                    bool
		consoleOff, consoleOn                                        bool
		consoleLevel                                                 = -1
		bufSize                                                      uintptr
		fromFile                                                     string
		levels, facilities                                           map[int]bool
		since, until                                                 *time.Time
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, bool) {
			i++
			if i >= len(args) {
				return "", false
			}
			return args[i], true
		}
		switch {
		case arg == "-c" || arg == "--read-clear":
			clearAfter = true
		case arg == "-C" || arg == "--clear":
			clearOnly = true
		case arg == "-r" || arg == "--raw":
			raw = true
		case arg == "-t" || arg == "--notime":
			notime = true
		case arg == "-x" || arg == "--decode":
			decode = true
		case arg == "-T" || arg == "--ctime":
			ctimeFmt = true
		case arg == "-k" || arg == "--kernel":
			kernelOnly = true
		case arg == "-u" || arg == "--userspace":
			userspaceOnly = true
		case arg == "-D" || arg == "--console-off":
			consoleOff = true
		case arg == "-E" || arg == "--console-on":
			consoleOn = true
		case arg == "-S" || arg == "--syslog", arg == "-P" || arg == "--nopager",
			arg == "-p" || arg == "--force-prefix", arg == "--noescape":
			// no-op: ba6 always uses syslog(2), never pages, never colours,
			// and single-line records need no multi-line prefix repair.
		case arg == "-n" || arg == "--console-level":
			v, ok := next()
			if !ok {
				fatalf("dmesg", "option '%s' requires an argument", arg)
				return 1
			}
			lvl, lok := dmesgLevelNum(v)
			if !lok {
				fatalf("dmesg", "unknown level '%s'", v)
				return 1
			}
			consoleLevel = lvl
		case arg == "-l" || arg == "--level":
			v, ok := next()
			if !ok {
				fatalf("dmesg", "option '%s' requires an argument", arg)
				return 1
			}
			m, err := parseDmesgLevels(v)
			if err != nil {
				fatalf("dmesg", "%v", err)
				return 1
			}
			levels = m
		case arg == "-f" || arg == "--facility":
			v, ok := next()
			if !ok {
				fatalf("dmesg", "option '%s' requires an argument", arg)
				return 1
			}
			m, err := parseDmesgFacilities(v)
			if err != nil {
				fatalf("dmesg", "%v", err)
				return 1
			}
			facilities = m
		case arg == "-s" || arg == "--buffer-size":
			v, ok := next()
			if !ok {
				fatalf("dmesg", "option '%s' requires an argument", arg)
				return 1
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				fatalf("dmesg", "invalid buffer size '%s'", v)
				return 1
			}
			bufSize = uintptr(n) + 1
		case arg == "-F" || arg == "--file":
			v, ok := next()
			if !ok {
				fatalf("dmesg", "option '%s' requires an argument", arg)
				return 1
			}
			fromFile = v
		case strings.HasPrefix(arg, "--time-format"):
			v := strings.TrimPrefix(arg, "--time-format=")
			if v == arg {
				var ok bool
				v, ok = next()
				if !ok {
					fatalf("dmesg", "option '--time-format' requires an argument")
					return 1
				}
			}
			switch v {
			case "ctime":
				ctimeFmt = true
			case "iso":
				isoFmt = true
			case "notime":
				notime = true
			case "raw":
				// default timestamp format; nothing to set
			default:
				fatalf("dmesg", "unsupported time format '%s'", v)
				return 1
			}
		case strings.HasPrefix(arg, "--since") || strings.HasPrefix(arg, "--until"):
			isUntil := strings.HasPrefix(arg, "--until")
			v := ""
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				v = arg[eq+1:]
			} else {
				var ok bool
				v, ok = next()
				if !ok {
					fatalf("dmesg", "option '%s' requires an argument", arg)
					return 1
				}
			}
			t, err := parseDmesgTime(v)
			if err != nil {
				fatalf("dmesg", "%v", err)
				return 1
			}
			if isUntil {
				until = &t
			} else {
				since = &t
			}
		default:
			fatalf("dmesg", "unsupported option '%s'", arg)
			return 1
		}
	}

	if consoleOff || consoleOn || consoleLevel >= 0 {
		action, level := uintptr(6), uintptr(0)
		switch {
		case consoleOn:
			action = 7
		case consoleLevel >= 0:
			action, level = 8, uintptr(consoleLevel)
		}
		if _, _, errno := syscall.Syscall(syscall.SYS_SYSLOG, action, 0, level); errno != 0 {
			fatalf("dmesg", "%v", errno)
			return 1
		}
		return 0
	}

	var data []byte
	if fromFile != "" {
		b, err := os.ReadFile(fromFile)
		if err != nil {
			fatalf("dmesg", "cannot open %s: %v", fromFile, errText(err))
			return 1
		}
		data = b
	} else if clearOnly {
		if _, err := dmesgSyslog(5, 0); err != nil {
			fatalf("dmesg", "%v", err)
			return 1
		}
		return 0
	} else {
		action := uintptr(3)
		if clearAfter {
			action = 4
		}
		b, err := dmesgSyslog(action, bufSize)
		if err != nil {
			fatalf("dmesg", "%v", err)
			return 1
		}
		data = b
	}

	boot, haveBoot := dmesgBootInstant()
	var out strings.Builder
	for _, raw0 := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if raw0 == "" {
			continue
		}
		line := parseDmesgLine(raw0)
		if kernelOnly && line.facility != 0 {
			continue
		}
		if userspaceOnly && line.facility == 0 {
			continue
		}
		if levels != nil && !levels[line.level] {
			continue
		}
		if facilities != nil && !facilities[line.facility] {
			continue
		}
		var realTime time.Time
		if haveBoot && line.hasTS {
			realTime = boot.Add(time.Duration(line.sec)*time.Second + time.Duration(line.usec)*time.Microsecond)
		}
		if (since != nil || until != nil) && line.hasTS && haveBoot {
			if since != nil && realTime.Before(*since) {
				continue
			}
			if until != nil && realTime.After(*until) {
				continue
			}
		}
		if raw {
			out.WriteString(line.raw)
			out.WriteByte('\n')
			continue
		}
		if decode {
			fmt.Fprintf(&out, "%-6s:%-6s: ", dmesgFacilityName(line.facility), dmesgLevelName(line.level))
		}
		if !notime && line.hasTS {
			switch {
			case isoFmt && haveBoot:
				fmt.Fprintf(&out, "%s,%06d%s ", realTime.Format("2006-01-02T15:04:05"),
					realTime.Nanosecond()/1000, realTime.Format("-07:00"))
			case ctimeFmt && haveBoot:
				fmt.Fprintf(&out, "[%s] ", realTime.Format("Mon Jan _2 15:04:05 2006"))
			default:
				fmt.Fprintf(&out, "[%5d.%06d] ", line.sec, line.usec)
			}
		}
		out.WriteString(line.text)
		out.WriteByte('\n')
	}
	if _, err := os.Stdout.WriteString(out.String()); err != nil {
		return 1
	}
	return 0
}

func cmdPgrep(args []string) int { return processMatchCommand("pgrep", args, false) }
func cmdPkill(args []string) int { return processMatchCommand("pkill", args, true) }
func cmdPidof(args []string) int {
	for _, arg := range args {
		if len(arg) > 1 && arg[0] == '-' {
			fatalf("pidof", "unrecognized option '%s'", arg)
			return 1
		}
	}
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
	optionFlags, data := parseMountOptions(options)
	flags |= optionFlags
	if err := syscall.Mount(operands[0], operands[1], fstype, flags, data); err != nil {
		fatalf("mount", "%v", err)
		return 1
	}
	return 0
}

// MS_NOSYMFOLLOW is available in Linux, but syscall does not expose it on all
// supported Go architectures.
const msNoSymFollow = uintptr(0x100)

// parseMountOptions separates VFS mount flags from filesystem-specific data.
// Only valueless, recognized flag options are consumed; options with values
// remain filesystem data verbatim so a future filesystem option cannot be
// mistaken for a mount flag.
func parseMountOptions(options string) (uintptr, string) {
	flags := uintptr(0)
	data := []string{}
	for _, option := range strings.Split(options, ",") {
		key, _, hasValue := strings.Cut(option, "=")
		if hasValue {
			data = append(data, option)
			continue
		}
		switch key {
		case "", "defaults":
		case "ro":
			flags |= syscall.MS_RDONLY
		case "rw":
			flags &^= syscall.MS_RDONLY
		case "bind":
			flags |= syscall.MS_BIND
		case "rbind":
			flags |= syscall.MS_BIND | syscall.MS_REC
		case "remount":
			flags |= syscall.MS_REMOUNT
		case "nosuid":
			flags |= syscall.MS_NOSUID
		case "suid":
			flags &^= syscall.MS_NOSUID
		case "nodev":
			flags |= syscall.MS_NODEV
		case "dev":
			flags &^= syscall.MS_NODEV
		case "noexec":
			flags |= syscall.MS_NOEXEC
		case "exec":
			flags &^= syscall.MS_NOEXEC
		case "sync":
			flags |= syscall.MS_SYNCHRONOUS
		case "dirsync":
			flags |= syscall.MS_DIRSYNC
		case "noatime":
			flags |= syscall.MS_NOATIME
		case "atime":
			flags &^= syscall.MS_NOATIME
		case "nodiratime":
			flags |= syscall.MS_NODIRATIME
		case "diratime":
			flags &^= syscall.MS_NODIRATIME
		case "relatime":
			flags |= syscall.MS_RELATIME
		case "strictatime":
			flags |= syscall.MS_STRICTATIME
		case "nosymfollow":
			flags |= msNoSymFollow
		case "silent":
			flags |= syscall.MS_SILENT
		case "private":
			flags |= syscall.MS_PRIVATE
		case "rprivate":
			flags |= syscall.MS_PRIVATE | syscall.MS_REC
		case "shared":
			flags |= syscall.MS_SHARED
		case "rshared":
			flags |= syscall.MS_SHARED | syscall.MS_REC
		case "slave":
			flags |= syscall.MS_SLAVE
		case "rslave":
			flags |= syscall.MS_SLAVE | syscall.MS_REC
		case "unbindable":
			flags |= syscall.MS_UNBINDABLE
		case "runbindable":
			flags |= syscall.MS_UNBINDABLE | syscall.MS_REC
		default:
			data = append(data, option)
		}
	}
	return flags, strings.Join(data, ",")
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

// linuxDeviceMajorMinor is the inverse of linuxMakeDevice: it splits a
// glibc-style 64-bit dev_t back into its major and minor numbers.
func linuxDeviceMajorMinor(dev uint64) (uint32, uint32) {
	major := uint32(dev>>8&0xfff) | uint32(dev>>32&^uint64(0xfff)) //nolint:gosec // G115: masked to 32 bits before conversion
	minor := uint32(dev&0xff) | uint32(dev>>12&^uint64(0xff))      //nolint:gosec // G115: masked to 32 bits before conversion
	return major, minor
}

func cmdBlkid(args []string) int {
	for _, arg := range args {
		if len(arg) > 1 && arg[0] == '-' {
			fatalf("blkid", "unrecognized option '%s'", arg)
			fmt.Fprintln(os.Stderr, "Try 'blkid --help' for more information.")
			return 1
		}
	}
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
