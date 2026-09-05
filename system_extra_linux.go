// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
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
	pretty, raw, since, container := false, false, false, false
	for _, arg := range args {
		switch arg {
		case "-p", "--pretty":
			pretty = true
		case "-r", "--raw":
			raw = true
		case "-s", "--since":
			since = true
		case "-c", "--container":
			container = true
		default:
			fatalf("uptime", "unsupported option %q", arg)
			return 1
		}
	}
	if os.Getenv("PROCPS_CONTAINER") != "" {
		container = true
	}
	if raw {
		seconds, err := uptimeSeconds(false)
		if err != nil {
			fatalf("uptime", "cannot get system uptime: %v", err)
			return 1
		}
		load, _ := os.ReadFile("/proc/loadavg")
		loadFields := strings.Fields(string(load))
		av := [3]float64{}
		for i := 0; i < 3 && i < len(loadFields); i++ {
			av[i], _ = strconv.ParseFloat(loadFields[i], 64)
		}
		fmt.Printf("%d %f %d %.2f %.2f %.2f\n", time.Now().Unix(), seconds, topUserCount(), av[0], av[1], av[2])
		return 0
	}
	seconds, err := uptimeSeconds(container)
	if err != nil {
		fatalf("uptime", "cannot get system uptime: %v", err)
		return 1
	}
	if since {
		boot := float64(time.Now().Unix()) - seconds
		fmt.Println(time.Unix(int64(boot), 0).Format("2006-01-02 15:04:05"))
		return 0
	}
	load, _ := os.ReadFile("/proc/loadavg")
	loadFields := strings.Fields(string(load))
	loads := "?"
	if len(loadFields) >= 3 {
		loads = strings.Join(loadFields[:3], ", ")
	}
	if pretty {
		fmt.Println("up " + formatUptimePretty(seconds))
		return 0
	}
	now := time.Now()
	users := topUserCount()
	plural := "users"
	if users == 1 {
		plural = "user"
	}
	fmt.Printf(" %02d:%02d:%02d up %s, %2d %s,  load average: %s\n",
		now.Hour(), now.Minute(), now.Second(), formatUptimeShort(seconds), users, plural, loads)
	return 0
}

// uptimeSeconds reads /proc/uptime, or derives a container's uptime from the
// difference between CLOCK_BOOTTIME and pid 1's start time the way procps -c
// does.
func uptimeSeconds(container bool) (float64, error) {
	if !container {
		data, err := os.ReadFile("/proc/uptime")
		if err != nil {
			return 0, err
		}
		fields := strings.Fields(string(data))
		if len(fields) < 1 {
			return 0, fmt.Errorf("empty /proc/uptime")
		}
		return strconv.ParseFloat(fields[0], 64)
	}
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0, err
	}
	pid1, err := readProcess(1)
	if err != nil {
		return 0, fmt.Errorf("cannot get container uptime: %w", err)
	}
	start := float64(pid1.startTicks) / clockTicks
	boot := float64(info.Uptime)
	if boot > start {
		return boot - start, nil
	}
	return 0, nil
}

// formatUptimeShort renders the uptime component of the default line:
// "5 days,  2:10", " 1:23" or "42 min", matching procps' %2d padding.
func formatUptimeShort(seconds float64) string {
	days := int(seconds) / (24 * 60 * 60)
	hours := int(seconds) / (60 * 60) % 24
	mins := int(seconds) / 60 % 60
	part := ""
	if days > 0 {
		suffix := "days"
		if days == 1 {
			suffix = "day"
		}
		part = fmt.Sprintf("%d %s", days, suffix)
	}
	if hours > 0 {
		comma := ""
		if days > 0 {
			comma = ", "
		}
		part += fmt.Sprintf("%s%2d:%02d", comma, hours, mins)
	} else {
		comma := ""
		if days > 0 {
			comma = ", "
		}
		part += fmt.Sprintf("%s%d min", comma, mins)
	}
	return part
}

// formatUptimePretty renders the -p form: decades, years, weeks, days, hours
// and minutes, joined by ", ", each with its singular or plural unit.
func formatUptimePretty(seconds float64) string {
	const (
		year   = 365 * 24 * 60 * 60
		decade = 10 * year
		week   = 7 * 24 * 60 * 60
	)
	parts := []string{}
	add := func(value int, unit string) {
		if value <= 0 {
			return
		}
		if value > 1 {
			unit += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", value, unit))
	}
	rem := int(seconds)
	add(rem/decade, "decade")
	rem %= decade
	add(rem/year, "year")
	rem %= year
	add(rem/week, "week")
	rem %= week
	days := rem / (24 * 60 * 60)
	rem %= 24 * 60 * 60
	hours := rem / (60 * 60)
	rem %= 60 * 60
	mins := rem / 60
	add(days, "day")
	add(hours, "hour")
	add(mins, "minute")
	return strings.Join(parts, ", ")
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
	singleShot, checkRoot, quiet, scriptsToo, workers, threads := false, false, false, false, false, false
	separator := " "
	var omit []int
	var names []string
	i := 0
	for ; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-s" || arg == "--single-shot":
			singleShot = true
		case arg == "-q" || arg == "--quiet":
			// -q implies -s in procps: only the exit status is wanted.
			quiet, singleShot = true, true
		case arg == "-c" || arg == "--check-root":
			checkRoot = true
		case arg == "-x":
			scriptsToo = true
		case arg == "-w" || arg == "--with-workers":
			workers = true
		case arg == "-t" || arg == "--lightweight":
			threads = true
		case arg == "-o" || arg == "--omit-pid":
			i++
			if i >= len(args) {
				fatalf("pidof", "option requires an argument -- 'o'")
				return 1
			}
			omit = append(omit, pidofOmitList(args[i])...)
		case strings.HasPrefix(arg, "--omit-pid="):
			omit = append(omit, pidofOmitList(strings.TrimPrefix(arg, "--omit-pid="))...)
		case arg == "-S" || arg == "-d":
			i++
			if i >= len(args) {
				fatalf("pidof", "option requires an argument -- '%s'", arg)
				return 1
			}
			separator = args[i]
		case strings.HasPrefix(arg, "-S") || strings.HasPrefix(arg, "-d"):
			if len(arg) > 2 {
				separator = arg[2:]
			}
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			fatalf("pidof", "unrecognized option '%s'", arg)
			return 1
		default:
			names = append(names, arg)
		}
	}
	if len(names) == 0 {
		fatalf("pidof", "missing program name")
		return 1
	}
	omitted := map[int]bool{}
	for _, pid := range omit {
		omitted[pid] = true
	}
	myRoot := ""
	if checkRoot && os.Geteuid() == 0 {
		myRoot, _ = os.Readlink("/proc/self/root")
	}

	processes, err := readProcesses(nil)
	if err != nil {
		fatalf("pidof", "%v", err)
		return 1
	}
	ids := make([]int, 0, len(processes))
	for _, p := range processes {
		ids = append(ids, p.pid)
	}
	if threads {
		for _, p := range processes {
			entries, readErr := os.ReadDir(filepath.Join("/proc", strconv.Itoa(p.pid), "task"))
			if readErr != nil {
				continue
			}
			for _, entry := range entries {
				if tid, convErr := strconv.Atoi(entry.Name()); convErr == nil && tid != p.pid {
					ids = append(ids, tid)
				}
			}
		}
	}
	found := false
	var output []string
	for _, name := range names {
		if name == "" {
			continue
		}
		programBase := filepath.Base(name)
		matches := []int{}
		for _, id := range ids {
			if omitted[id] {
				continue
			}
			proc := pidofRead(id)
			if checkRoot {
				if root, linkErr := os.Readlink(filepath.Join("/proc", strconv.Itoa(id), "root")); linkErr != nil || root != myRoot {
					continue
				}
			}
			if len(proc.argv) == 0 && !workers {
				continue
			}
			argv0 := ""
			argv1 := ""
			if len(proc.argv) > 0 {
				argv0 = proc.argv[0]
			}
			if len(proc.argv) > 1 {
				argv1 = proc.argv[1]
			}
			// Processes whose argv0 starts with '-' are login shells.
			argv0 = strings.TrimPrefix(argv0, "-")
			argv0base := filepath.Base(argv0)
			exeBase := filepath.Base(proc.exe)
			match := name == argv0base || programBase == argv0 || name == argv0 ||
				(workers && name == proc.comm) || name == exeBase || name == proc.exe
			// A space inside argv0 means the title was rewritten; the
			// process comm is then the only reliable name.
			if !match && strings.Contains(argv0, " ") {
				match = name == proc.comm
			}
			// -x finds interpreters running a named script: the comm is the
			// script name and the name matches argv1.
			if !match && scriptsToo && argv1 != "" {
				argv1base := filepath.Base(argv1)
				if strings.HasPrefix(argv1base, proc.comm) &&
					(name == argv1base || programBase == argv1 || name == argv1) {
					match = true
				}
			}
			if match {
				matches = append(matches, id)
			}
		}
		if len(matches) > 0 {
			found = true
		}
		sort.Sort(sort.Reverse(sort.IntSlice(matches)))
		for idx, pid := range matches {
			if singleShot && idx > 0 {
				break
			}
			output = append(output, strconv.Itoa(pid))
		}
	}
	if !quiet && found {
		fmt.Fprintln(os.Stdout, strings.Join(output, separator))
	}
	if found {
		return 0
	}
	return 1
}

// pidofOmitList parses -o's comma/colon/semicolon-separated PID list,
// honouring the historic %PPID token.
func pidofOmitList(text string) []int {
	var list []int
	for _, token := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' || r == ':' }) {
		if token == "%PPID" {
			list = append(list, os.Getppid())
			continue
		}
		pid, err := strconv.Atoi(token)
		if err != nil {
			fatalf("pidof", "illegal omit pid value (%s)!", token)
			continue
		}
		list = append(list, pid)
	}
	return list
}

// pidofRead captures one task's argv, comm and executable link.
func pidofRead(id int) struct {
	argv []string
	comm string
	exe  string
} {
	base := filepath.Join("/proc", strconv.Itoa(id))
	var proc struct {
		argv []string
		comm string
		exe  string
	}
	if cmdline, err := os.ReadFile(filepath.Join(base, "cmdline")); err == nil {
		for _, part := range strings.Split(string(cmdline), "\x00") {
			if part != "" {
				proc.argv = append(proc.argv, part)
			}
		}
	}
	if comm, err := os.ReadFile(filepath.Join(base, "comm")); err == nil {
		proc.comm = strings.TrimSuffix(string(comm), "\n")
	}
	proc.exe, _ = os.Readlink(filepath.Join(base, "exe"))
	return proc
}

// procMatch is one pgrep or pkill command line: every selection filter, and
// how the matches are reported once they are found.
type procMatch struct {
	full, exact, invert, ignoreCase bool
	newest, oldest                  bool
	count, listName, listFull       bool
	quiet, echo, threads            bool
	ignoreAncestors, requireHandler bool
	delimiter                       string
	older                           uint64
	haveOlder                       bool
	states                          string
	signal                          syscall.Signal
	pids, ppids, pgroups, sessions  map[int]bool
	euids, uids, gids               map[uint32]bool
	terminals                       map[string]bool
	// criteria records whether any option that selects processes was given,
	// which is what makes the pattern operand optional.
	criteria bool
}

// procMatchIDs parses one comma-separated list of numeric ids for -p, -P, -g
// and -s.
func procMatchIDs(value string, into map[int]bool) error {
	for _, part := range strings.Split(value, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return fmt.Errorf("not a number: %s", part)
		}
		into[n] = true
	}
	return nil
}

// procMatchNames parses a comma-separated list of user or group names and ids,
// as -u, -U and -G take them. lookup turns one name into its numeric id.
func procMatchNames(value string, into map[uint32]bool, lookup func(string) (uint32, bool)) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if n, err := strconv.ParseUint(part, 10, 32); err == nil {
			into[uint32(n)] = true
			continue
		}
		id, ok := lookup(part)
		if !ok {
			return fmt.Errorf("%s", part)
		}
		into[id] = true
	}
	return nil
}

func procMatchLookupUser(name string) (uint32, bool) {
	account, err := user.Lookup(name)
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseUint(account.Uid, 10, 32)
	return uint32(id), err == nil
}

func procMatchLookupGroup(name string) (uint32, bool) {
	group, err := user.LookupGroup(name)
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseUint(group.Gid, 10, 32)
	return uint32(id), err == nil
}

// procps has three shapes of command-line failure, and scripts see all three:
// a missing selection prints the diagnostic and the "Try ..." line, a bad
// option value prints the diagnostic alone, and an unusable option prints the
// diagnostic and then the whole usage text. All three exit 2.
func procMatchUsage(prog, format string, a ...interface{}) int {
	fatalf(prog, format, a...)
	fmt.Fprintf(os.Stderr, "Try `%s --help' for more information.\n", prog)
	return 2
}

func procMatchError(prog, format string, a ...interface{}) int {
	fatalf(prog, format, a...)
	return 2
}

func procMatchOptionError(prog, format string, a ...interface{}) int {
	if format != "" {
		fatalf(prog, format, a...)
	}
	fmt.Fprintln(os.Stderr)
	if err := writeAppletHelp(os.Stderr, prog); err != nil {
		fatalf(prog, "%v", err)
	}
	return 2
}

// procMatchSignalArgument pulls pkill's "-SIGNAL" out of the command line
// before the options are parsed, the way procps' own signal_option does: the
// first argument whose body names a signal is consumed, so that "-o" and the
// other option letters keep their meaning.
func procMatchSignalArgument(args []string) ([]string, syscall.Signal, bool) {
	for i, arg := range args {
		if len(arg) < 2 || arg[0] != '-' || arg[1] == '-' {
			continue
		}
		body := arg[1:]
		if _, err := strconv.Atoi(body); err != nil && signalNames[strings.TrimPrefix(strings.ToUpper(body), "SIG")] == 0 {
			continue
		}
		signal, err := parseSignal(body)
		if err != nil {
			continue
		}
		return append(append([]string{}, args[:i]...), args[i+1:]...), signal, true
	}
	return args, 0, false
}

// procMatchAncestors is the chain of parents above our own process, which -A
// removes from the result.
func procMatchAncestors(processes []processInfo) map[int]bool {
	parent := map[int]int{}
	for _, p := range processes {
		parent[p.pid] = p.ppid
	}
	ancestors := map[int]bool{}
	for pid := os.Getpid(); pid > 0; {
		next, ok := parent[pid]
		if !ok || ancestors[next] {
			break
		}
		ancestors[next] = true
		pid = next
	}
	return ancestors
}

// procMatchCatches reports whether the process has a handler installed for the
// signal, which pkill -H requires before it will send one.
func procMatchCatches(pid int, signal syscall.Signal) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		value, found := strings.CutPrefix(line, "SigCgt:")
		if !found {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil || signal < 1 || signal > 64 {
			return false
		}
		return mask&(1<<uint(signal-1)) != 0
	}
	return false
}

// procMatchThreads lists a process's task ids, which -w reports in place of the
// single process id.
func procMatchThreads(pid int) []int {
	entries, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "task"))
	if err != nil {
		return []int{pid}
	}
	var tids []int
	for _, entry := range entries {
		if tid, err := strconv.Atoi(entry.Name()); err == nil {
			tids = append(tids, tid)
		}
	}
	if len(tids) == 0 {
		return []int{pid}
	}
	sort.Ints(tids)
	return tids
}

//nolint:gocyclo // one option table and one filter chain; splitting them apart would only scatter the command line.
func processMatchCommand(prog string, args []string, send bool) int {
	options := procMatch{
		delimiter: "\n",
		signal:    syscall.SIGTERM,
		pids:      map[int]bool{}, ppids: map[int]bool{},
		pgroups: map[int]bool{}, sessions: map[int]bool{},
		euids: map[uint32]bool{}, uids: map[uint32]bool{}, gids: map[uint32]bool{},
		terminals: map[string]bool{},
	}
	if send {
		if rest, signal, found := procMatchSignalArgument(args); found {
			args, options.signal = rest, signal
		}
	}
	var pattern string
	havePattern := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			for _, operand := range args[i+1:] {
				if havePattern {
					return procMatchUsage(prog, "only one pattern can be provided")
				}
				pattern, havePattern = operand, true
			}
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if havePattern {
				return procMatchUsage(prog, "only one pattern can be provided")
			}
			pattern, havePattern = arg, true
			continue
		}
		// Long options first; the short ones cluster, and the last letter of a
		// cluster may carry its argument attached or as the next operand.
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := arg[2:], "", false
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name, value, hasValue = name[:eq], name[eq+1:], true
			}
			letters := map[string]string{
				"delimiter": "d", "list-name": "l", "list-full": "a", "inverse": "v",
				"lightweight": "w", "count": "c", "full": "f", "pgroup": "g", "group": "G",
				"ignore-case": "i", "newest": "n", "oldest": "o", "older": "O", "pid": "p",
				"parent": "P", "session": "s", "terminal": "t", "euid": "u", "uid": "U",
				"exact": "x", "pidfile": "F", "logpidfile": "L", "runstates": "r",
				"ignore-ancestors": "A", "echo": "e", "require-handler": "H",
			}
			switch name {
			case "quiet":
				options.quiet = true
				continue
			case "signal":
				if !hasValue {
					i++
					if i >= len(args) {
						return procMatchOptionError(prog, "option '--signal' requires an argument")
					}
					value = args[i]
				}
				signal, err := parseSignal(value)
				if err != nil {
					return procMatchError(prog, "%v", err)
				}
				options.signal = signal
				continue
			}
			letter, ok := letters[name]
			if !ok {
				return procMatchOptionError(prog, "unrecognized option '--%s'", name)
			}
			arg = "-" + letter
			if hasValue {
				arg += value
			}
		}
		cluster := arg[1:]
		for len(cluster) > 0 {
			letter := cluster[0]
			cluster = cluster[1:]
			// takeValue returns the option's argument: the rest of the cluster
			// when there is one, otherwise the following operand.
			takeValue := func() (string, bool) {
				if cluster != "" {
					value := cluster
					cluster = ""
					return value, true
				}
				i++
				if i >= len(args) {
					return "", false
				}
				return args[i], true
			}
			var err error
			switch letter {
			case 'f':
				options.full = true
			case 'x':
				options.exact = true
			case 'v':
				options.invert = true
			case 'i':
				options.ignoreCase = true
			case 'c':
				options.count = true
			case 'l':
				options.listName = true
			case 'a':
				options.listFull = true
			case 'w':
				options.threads = true
			case 'n':
				options.newest, options.criteria = true, true
			case 'o':
				options.oldest, options.criteria = true, true
			case 'A':
				options.ignoreAncestors = true
			case 'e':
				options.echo = true
			case 'H':
				options.requireHandler = true
			case 'L':
				// Only meaningful beside -F, whose lock this build does not check.
			case 'd', 'g', 'G', 'O', 'p', 'P', 's', 't', 'u', 'U', 'F', 'r':
				value, ok := takeValue()
				if !ok {
					return procMatchOptionError(prog, "option requires an argument -- '%c'", letter)
				}
				switch letter {
				case 'd':
					options.delimiter = value
				case 'g':
					err, options.criteria = procMatchIDs(value, options.pgroups), true
				case 'p':
					err, options.criteria = procMatchIDs(value, options.pids), true
				case 'P':
					err, options.criteria = procMatchIDs(value, options.ppids), true
				case 's':
					err, options.criteria = procMatchIDs(value, options.sessions), true
				case 'u':
					if err = procMatchNames(value, options.euids, procMatchLookupUser); err != nil {
						return procMatchError(prog, "invalid user name: %v", err)
					}
					options.criteria = true
				case 'U':
					if err = procMatchNames(value, options.uids, procMatchLookupUser); err != nil {
						return procMatchError(prog, "invalid user name: %v", err)
					}
					options.criteria = true
				case 'G':
					if err = procMatchNames(value, options.gids, procMatchLookupGroup); err != nil {
						return procMatchError(prog, "invalid group name: %v", err)
					}
					options.criteria = true
				case 't':
					for _, name := range strings.Split(value, ",") {
						options.terminals[strings.TrimPrefix(strings.TrimSpace(name), "/dev/")] = true
					}
					options.criteria = true
				case 'r':
					options.states, options.criteria = value, true
				case 'O':
					seconds, convErr := strconv.ParseUint(value, 10, 64)
					if convErr != nil {
						return procMatchError(prog, "not a number: %s", value)
					}
					options.older, options.haveOlder, options.criteria = seconds, true, true
				case 'F':
					data, readErr := os.ReadFile(value)
					if readErr != nil {
						fatalf(prog, "cannot open pidfile %s: %s", value, errText(readErr))
						return 2
					}
					field := strings.Fields(string(data))
					if len(field) == 0 {
						fatalf(prog, "pidfile not valid")
						return 2
					}
					err, options.criteria = procMatchIDs(field[0], options.pids), true
				}
			default:
				return procMatchOptionError(prog, "invalid option -- '%c'", letter)
			}
			if err != nil {
				return procMatchError(prog, "%v", err)
			}
		}
	}
	if options.newest && options.oldest {
		// procps prints its usage here with no diagnostic at all.
		return procMatchOptionError(prog, "")
	}
	if !havePattern && !options.criteria {
		return procMatchUsage(prog, "no matching criteria specified")
	}
	// procps reads a 0 in a session or process-group list as "my own", so that
	// a script can ask for its own session without looking the number up.
	if options.sessions[0] {
		delete(options.sessions, 0)
		if sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, 0, 0, 0); errno == 0 {
			options.sessions[int(sid)] = true
		}
	}
	if options.pgroups[0] {
		delete(options.pgroups, 0)
		options.pgroups[syscall.Getpgrp()] = true
	}
	var re *regexp.Regexp
	if havePattern {
		// procps anchors the pattern itself under -x rather than comparing the
		// whole match, so an alternation is anchored as a whole.
		expression := pattern
		if options.exact {
			expression = "^(" + expression + ")$"
		}
		if options.ignoreCase {
			expression = "(?i)" + expression
		}
		compiled, err := regexp.Compile(expression)
		if err != nil {
			return procMatchError(prog, "%v", err)
		}
		re = compiled
		// A name in /proc/PID/stat is truncated to 15 characters, so a longer
		// pattern can never match one; procps warns and carries on.
		if !options.full && len(pattern) > 15 {
			fatalf(prog, "pattern that searches for process name longer than 15 characters will result in zero matches")
			fmt.Fprintf(os.Stderr, "Try `%s -f' option to match against the complete command line.\n", prog)
		}
	}
	processes, err := readProcesses(nil)
	if err != nil {
		return 2
	}
	ancestors := map[int]bool{}
	if options.ignoreAncestors {
		ancestors = procMatchAncestors(processes)
	}
	runtime := newPSRuntime()
	var matches []processInfo
	for _, p := range processes {
		if p.pid == os.Getpid() || ancestors[p.pid] {
			continue
		}
		if !procMatchSelects(&options, runtime, p, re) {
			continue
		}
		if send && options.requireHandler && !procMatchCatches(p.pid, options.signal) {
			continue
		}
		matches = append(matches, p)
	}
	// -n and -o narrow the result to one process once every other filter has
	// had its say.
	if (options.newest || options.oldest) && len(matches) > 0 {
		best := matches[0]
		for _, p := range matches[1:] {
			if options.newest && p.startTicks >= best.startTicks || options.oldest && p.startTicks < best.startTicks {
				best = p
			}
		}
		matches = []processInfo{best}
	}
	return procMatchReport(prog, &options, send, matches)
}

// procMatchSelects applies every filter to one process. procps combines them
// all — the pattern and the id, terminal, state and age restrictions — into one
// verdict and lets -v invert that verdict as a whole, so "pgrep -v -u root"
// really does list the processes root does not own.
func procMatchSelects(options *procMatch, runtime psRuntime, p processInfo, re *regexp.Regexp) bool {
	match := true
	if re != nil {
		target := p.comm
		if options.full {
			target = p.args
		}
		match = re.MatchString(target)
	}
	switch {
	case len(options.pids) > 0 && !options.pids[p.pid]:
		match = false
	case len(options.ppids) > 0 && !options.ppids[p.ppid]:
		match = false
	case len(options.pgroups) > 0 && !options.pgroups[p.pgrp]:
		match = false
	case len(options.sessions) > 0 && !options.sessions[p.session]:
		match = false
	case len(options.euids) > 0 && !options.euids[p.uid]:
		match = false
	case len(options.uids) > 0 && !options.uids[p.realUID]:
		match = false
	case len(options.gids) > 0 && !options.gids[p.realGID]:
		match = false
	case len(options.terminals) > 0 && !options.terminals[ttyName(p.tty)]:
		match = false
	case options.states != "" && !strings.Contains(options.states, p.state):
		match = false
	case options.haveOlder && runtime.elapsedSeconds(p) < options.older:
		match = false
	}
	return match != options.invert
}

// procMatchReport prints or signals the matches and returns pgrep's status: 0
// when something matched, 1 when nothing did.
func procMatchReport(prog string, options *procMatch, send bool, matches []processInfo) int {
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush() //nolint:errcheck // a sticky write error is reported by the final Flush.
	status := 1
	if len(matches) > 0 {
		status = 0
	}
	if send {
		for _, p := range matches {
			if err := syscall.Kill(p.pid, options.signal); err != nil {
				fatalf(prog, "killing pid %d failed: %s", p.pid, errText(err))
				status = 1
				continue
			}
			if options.echo && !options.quiet {
				fmt.Fprintf(out, "%s killed (pid %d)\n", p.comm, p.pid)
			}
		}
	}
	if options.quiet {
		return status
	}
	if options.count {
		fmt.Fprintf(out, "%d\n", len(matches))
		return status
	}
	if send {
		return status
	}
	written := false
	for _, p := range matches {
		for _, pid := range procMatchPIDs(options, p) {
			if written {
				io.WriteString(out, options.delimiter) //nolint:errcheck // buffered writer; the error surfaces at Flush.
			}
			written = true
			switch {
			case options.listFull:
				text := p.args
				if text == "" {
					text = "[" + p.comm + "]"
				}
				fmt.Fprintf(out, "%d %s", pid, text)
			case options.listName:
				fmt.Fprintf(out, "%d %s", pid, p.comm)
			default:
				fmt.Fprintf(out, "%d", pid)
			}
		}
	}
	if written {
		io.WriteString(out, "\n") //nolint:errcheck // buffered writer; the error surfaces at Flush.
	}
	return status
}

// procMatchPIDs is what one match contributes to the output: its thread ids
// under -w, and otherwise the process id alone.
func procMatchPIDs(options *procMatch, p processInfo) []int {
	if options.threads {
		return procMatchThreads(p.pid)
	}
	return []int{p.pid}
}

// MS_NOSYMFOLLOW is available in Linux, but syscall does not expose it on all
// supported Go architectures.
const msNoSymFollow = uintptr(0x100)

// mountEntry is one line of the kernel's mount table.
type mountEntry struct {
	source  string
	target  string
	fstype  string
	options string
}

// readMountTable parses /proc/self/mounts, decoding the escapes the kernel
// writes for spaces and tabs in a path.
func readMountTable() ([]mountEntry, error) {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	var entries []mountEntry
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		entries = append(entries, mountEntry{
			source:  decodeMountField(fields[0]),
			target:  decodeMountField(fields[1]),
			fstype:  fields[2],
			options: decodeMountField(fields[3]),
		})
	}
	return entries, nil
}

// readUserMountOptions reads the options only userspace knows about, which the
// mount helpers record in /run/mount/utab under the target they belong to.
func readUserMountOptions() map[string]string {
	options := map[string]string{}
	data, err := os.ReadFile("/run/mount/utab")
	if err != nil {
		return options
	}
	for _, line := range strings.Split(string(data), "\n") {
		target, extra := "", ""
		for _, field := range strings.Fields(line) {
			if value, found := strings.CutPrefix(field, "TARGET="); found {
				target = decodeMountField(value)
			}
			if value, found := strings.CutPrefix(field, "OPTS="); found {
				extra = decodeMountField(value)
			}
		}
		if target != "" && extra != "" {
			options[target] = extra
		}
	}
	return options
}

// loopBackingFile is the file a loop device stands for, which the original
// shows in place of the device node itself.
func loopBackingFile(device string) string {
	name, found := strings.CutPrefix(device, "/dev/")
	if !found || !strings.HasPrefix(name, "loop") {
		return device
	}
	data, err := os.ReadFile("/sys/block/" + name + "/loop/backing_file")
	if err != nil {
		return device
	}
	backing := strings.TrimSuffix(strings.TrimSpace(string(data)), " (deleted)")
	if backing == "" {
		return device
	}
	return backing
}

// fstabEntry is one line of /etc/fstab.
type fstabEntry struct {
	source  string
	target  string
	fstype  string
	options string
}

func readFstab(path string) ([]fstabEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []fstabEntry
	for _, line := range strings.Split(string(data), "\n") {
		if cut := strings.IndexByte(line, '#'); cut >= 0 {
			line = line[:cut]
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		entry := fstabEntry{
			source: decodeMountField(fields[0]),
			target: decodeMountField(fields[1]),
			fstype: fields[2],
		}
		if len(fields) > 3 {
			entry.options = fields[3]
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// resolveMountSource turns the LABEL= and UUID= spellings fstab uses, and the
// -L and -U options, into the device they name.
func resolveMountSource(source string) string {
	for _, form := range []struct{ prefix, directory string }{
		{"LABEL=", "/dev/disk/by-label/"},
		{"UUID=", "/dev/disk/by-uuid/"},
		{"PARTLABEL=", "/dev/disk/by-partlabel/"},
		{"PARTUUID=", "/dev/disk/by-partuuid/"},
	} {
		if value, found := strings.CutPrefix(source, form.prefix); found {
			if resolved, err := filepath.EvalSymlinks(form.directory + value); err == nil {
				return resolved
			}
			return form.directory + value
		}
	}
	return source
}

// mountOptions is one mount(8) command line.
type mountOptions struct {
	fstype       string
	options      string
	flags        uintptr
	all          bool
	verbose      bool
	fake         bool
	filterTypes  string
	filterOpts   string
	fstab        string
	source       string
	target       string
	haveSource   bool
	haveTarget   bool
	showLabels   bool
	propagations uintptr
	// sourceSpec is how -L or -U named the device, which the original quotes
	// back when nothing carries that label.
	sourceSpec string
}

// mountUsage reports a command-line mistake the way util-linux does.
func mountUsage(format string, a ...interface{}) int {
	fatalf("mount", format, a...)
	fmt.Fprintln(os.Stderr, "Try 'mount --help' for more information.")
	return 1
}

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func cmdMount(args []string) int {
	options := mountOptions{fstab: "/etc/fstab"}
	operands := []string{}
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !parsing || !strings.HasPrefix(arg, "-") || arg == "-" {
			operands = append(operands, arg)
			continue
		}
		if arg == "--" {
			parsing = false
			continue
		}
		name, value, hasValue := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
		}
		needValue := func(what string) (string, bool) {
			if hasValue {
				return value, true
			}
			i++
			if i >= len(args) {
				mountUsage("option '%s' requires an argument", what)
				return "", false
			}
			return args[i], true
		}
		switch name {
		case "-t", "--types":
			text, ok := needValue(name)
			if !ok {
				return 1
			}
			options.fstype = text
		case "-o", "--options":
			text, ok := needValue(name)
			if !ok {
				return 1
			}
			if options.options == "" {
				options.options = text
			} else {
				options.options += "," + text
			}
		case "-r", "--read-only":
			options.flags |= syscall.MS_RDONLY
		case "-w", "--rw", "--read-write":
			options.flags &^= syscall.MS_RDONLY
		case "-a", "--all":
			options.all = true
		case "-v", "--verbose":
			options.verbose = true
		case "-n", "--no-mtab":
			// There is no mtab to write; the kernel table is the only one.
		case "-l", "--show-labels":
			options.showLabels = true
		case "-B", "--bind":
			options.flags |= syscall.MS_BIND
		case "-R", "--rbind":
			options.flags |= syscall.MS_BIND | syscall.MS_REC
		case "-M", "--move":
			options.flags |= syscall.MS_MOVE
		case "--make-shared":
			options.propagations = syscall.MS_SHARED
		case "--make-private":
			options.propagations = syscall.MS_PRIVATE
		case "--make-slave":
			options.propagations = syscall.MS_SLAVE
		case "--make-unbindable":
			options.propagations = syscall.MS_UNBINDABLE
		case "--make-rshared":
			options.propagations = syscall.MS_SHARED | syscall.MS_REC
		case "--make-rprivate":
			options.propagations = syscall.MS_PRIVATE | syscall.MS_REC
		case "--make-rslave":
			options.propagations = syscall.MS_SLAVE | syscall.MS_REC
		case "--make-runbindable":
			options.propagations = syscall.MS_UNBINDABLE | syscall.MS_REC
		case "-f", "--fake":
			options.fake = true
		case "-L", "--label", "-U", "--uuid":
			text, ok := needValue(name)
			if !ok {
				return 1
			}
			kind := "LABEL"
			if name == "-U" || name == "--uuid" {
				kind = "UUID"
			}
			options.source, options.haveSource = resolveMountSource(kind+"="+text), true
			options.sourceSpec = kind + `="` + text + `"`
		case "--source":
			text, ok := needValue(name)
			if !ok {
				return 1
			}
			options.source, options.haveSource = text, true
		case "--target":
			text, ok := needValue(name)
			if !ok {
				return 1
			}
			options.target, options.haveTarget = text, true
		case "-T", "--fstab":
			text, ok := needValue(name)
			if !ok {
				return 1
			}
			options.fstab = text
		case "-O", "--test-opts":
			text, ok := needValue(name)
			if !ok {
				return 1
			}
			options.filterOpts = text
		default:
			if strings.HasPrefix(arg, "--") {
				return mountUsage("unrecognized option '%s'", arg)
			}
			return mountUsage("invalid option -- '%c'", arg[1])
		}
	}
	switch {
	case options.haveSource && !options.haveTarget && len(operands) == 1:
		options.target, options.haveTarget = operands[0], true
		operands = nil
	case options.haveTarget && !options.haveSource && len(operands) == 1:
		options.source, options.haveSource = operands[0], true
		operands = nil
	}
	options.filterTypes = options.fstype

	if options.all {
		return mountAll(&options)
	}
	if len(operands) == 0 && !options.haveTarget && !options.haveSource {
		return listMounts(options.filterTypes)
	}
	switch len(operands) {
	case 0:
	case 1:
		// One operand names either the device or the mount point; which one it
		// is comes from fstab, as it does in the original.
		if !options.haveTarget && !options.haveSource {
			return mountFromFstab(&options, operands[0])
		}
		if options.haveTarget {
			options.source, options.haveSource = operands[0], true
		} else {
			options.target, options.haveTarget = operands[0], true
		}
	case 2:
		options.source, options.haveSource = operands[0], true
		options.target, options.haveTarget = operands[1], true
	default:
		return mountUsage("only root can use \"--options\" option (effective UID is %d)", os.Geteuid())
	}
	return mountOne(&options, options.source, options.target, options.fstype, options.options)
}

// mountFromFstab handles the one-operand form: the operand is looked up in
// fstab as either a mount point or a device, and the entry supplies the rest.
func mountFromFstab(options *mountOptions, operand string) int {
	entries, err := readFstab(options.fstab)
	if err != nil {
		fatalf("mount", "%s: %s", options.fstab, errText(err))
		return 32
	}
	// The operand may be written relative to the current directory while fstab
	// spells everything absolutely, so both sides are made absolute first.
	clean := mountAbsolute(operand)
	for _, entry := range entries {
		if mountAbsolute(entry.target) != clean && mountAbsolute(entry.source) != clean {
			continue
		}
		combined := entry.options
		if options.options != "" {
			combined = strings.TrimPrefix(combined+","+options.options, ",")
		}
		fstype := entry.fstype
		if options.fstype != "" {
			fstype = options.fstype
		}
		return mountOne(options, resolveMountSource(entry.source), entry.target, fstype, combined)
	}
	fatalf("mount", "%s: can't find in %s.", operand, options.fstab)
	return 1
}

// mountAbsolute is a path made absolute without resolving anything, which is
// enough to compare an operand against an fstab entry.
func mountAbsolute(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return filepath.Clean(path)
}

// mountAll mounts every fstab entry that is not already mounted, honouring the
// -t and -O filters and skipping the ones marked noauto.
func mountAll(options *mountOptions) int {
	entries, err := readFstab(options.fstab)
	if err != nil {
		fatalf("mount", "%s: %s", options.fstab, errText(err))
		return 32
	}
	mounted := map[string]bool{}
	if table, tableErr := readMountTable(); tableErr == nil {
		for _, entry := range table {
			mounted[filepath.Clean(entry.target)] = true
		}
	}
	status := 0
	for _, entry := range entries {
		switch {
		case entry.fstype == "swap" || entry.target == "none":
			continue
		case mounted[filepath.Clean(entry.target)]:
			continue
		case mountHasOption(entry.options, "noauto"):
			continue
		case !mountTypeSelected(options.filterTypes, entry.fstype):
			continue
		case options.filterOpts != "" && !mountOptionsSelected(options.filterOpts, entry.options):
			continue
		}
		combined := entry.options
		if options.options != "" {
			combined = strings.TrimPrefix(combined+","+options.options, ",")
		}
		if code := mountOne(options, resolveMountSource(entry.source), entry.target, entry.fstype, combined); code != 0 {
			status = code
		}
	}
	return status
}

// mountOne performs one mount, or one propagation change, and reports the
// failure the way the original does — including its exit status 32.
func mountOne(options *mountOptions, source, target, fstype, optionText string) int {
	flags, data := parseMountOptions(optionText)
	flags |= options.flags
	if options.propagations != 0 {
		if err := syscall.Mount("", target, "", options.propagations, ""); err != nil {
			fatalf("mount", "%s: %s.", target, errText(err))
			return 32
		}
		return 0
	}
	if options.fake {
		if options.verbose {
			fmt.Printf("mount: %s would be mounted on %s\n", source, target)
		}
		return 0
	}
	// A device named by label or uuid that nothing answers to is reported
	// against the target, before privilege comes into it.
	if options.sourceSpec != "" {
		if _, err := os.Stat(source); err != nil {
			fatalf("mount", "%s: can't find %s.", target, options.sourceSpec)
			return 1
		}
	}
	// The original checks the caller's privilege before the kernel gets a say,
	// so an unprivileged mount is refused by that name whatever else is wrong.
	if os.Geteuid() != 0 {
		fatalf("mount", "%s: must be superuser to use mount.", target)
		fmt.Fprintln(os.Stderr, "       dmesg(1) may have more information after failed mount system call.")
		return 32
	}
	if err := syscall.Mount(source, target, fstype, flags, data); err != nil {
		fatalf("mount", "%s: %s.", target, errText(err))
		fmt.Fprintln(os.Stderr, "       dmesg(1) may have more information after failed mount system call.")
		return 32
	}
	// A bind mount cannot carry its own flags in one call, so read-only and
	// the other per-mount flags are applied in the remount the original makes
	// for exactly this reason.
	const perMount = syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV |
		syscall.MS_NOEXEC | syscall.MS_NOATIME | syscall.MS_NODIRATIME | syscall.MS_RELATIME
	if flags&syscall.MS_BIND != 0 && flags&perMount != 0 {
		remount := flags&^uintptr(syscall.MS_REC) | syscall.MS_REMOUNT | syscall.MS_BIND
		if err := syscall.Mount("", target, "", remount, ""); err != nil {
			fatalf("mount", "%s: %s.", target, errText(err))
			return 32
		}
	}
	if options.verbose {
		fmt.Printf("mount: %s mounted on %s.\n", source, target)
	}
	return 0
}

// mountHasOption reports whether a comma-separated option list contains name.
func mountHasOption(options, name string) bool {
	for _, option := range strings.Split(options, ",") {
		if key, _, _ := strings.Cut(option, "="); key == name {
			return true
		}
	}
	return false
}

// mountTypeSelected applies a -t filter, which may be a comma-separated list
// and may be negated as a whole with "no".
func mountTypeSelected(filter, fstype string) bool {
	if filter == "" {
		return true
	}
	if negated, found := strings.CutPrefix(filter, "no"); found {
		for _, name := range strings.Split(negated, ",") {
			if name == fstype {
				return false
			}
		}
		return true
	}
	for _, name := range strings.Split(filter, ",") {
		if name == fstype {
			return true
		}
	}
	return false
}

// mountOptionsSelected applies a -O filter the same way.
func mountOptionsSelected(filter, options string) bool {
	for _, name := range strings.Split(filter, ",") {
		if negated, found := strings.CutPrefix(name, "no"); found {
			if mountHasOption(options, negated) {
				return false
			}
			continue
		}
		if !mountHasOption(options, name) {
			return false
		}
	}
	return true
}

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

// listMounts prints the mount table in the original's own form rather than
// the kernel's: "SOURCE on TARGET type FSTYPE (options)".
func listMounts(filterTypes string) int {
	entries, err := readMountTable()
	if err != nil {
		fatalf("mount", "%v", err)
		return 1
	}
	userOptions := readUserMountOptions()
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush() //nolint:errcheck // a sticky write error is reported by the final Flush.
	for _, entry := range entries {
		if !mountTypeSelected(filterTypes, entry.fstype) {
			continue
		}
		options := entry.options
		if extra := userOptions[entry.target]; extra != "" {
			options += "," + extra
		}
		fmt.Fprintf(out, "%s on %s type %s (%s)\n",
			loopBackingFile(entry.source), entry.target, entry.fstype, options)
	}
	return 0
}

// umountOptions is one umount(8) command line.
type umountOptions struct {
	flags       int
	all         bool
	allTargets  bool
	recursive   bool
	readOnly    bool
	verbose     bool
	quiet       bool
	fake        bool
	filterTypes string
	filterOpts  string
}

func umountUsage(format string, a ...interface{}) int {
	fatalf("umount", format, a...)
	fmt.Fprintln(os.Stderr, "Try 'umount --help' for more information.")
	return 1
}

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func cmdUmount(args []string) int {
	options := umountOptions{}
	targets := []string{}
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !parsing || !strings.HasPrefix(arg, "-") || arg == "-" {
			targets = append(targets, arg)
			continue
		}
		if arg == "--" {
			parsing = false
			continue
		}
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
				umountUsage("option '%s' requires an argument", name)
				return "", false
			}
			return args[i], true
		}
		// The short options cluster, so each letter is handled on its own.
		letters := []string{name}
		if !strings.HasPrefix(arg, "--") && len(arg) > 2 {
			letters = nil
			for _, letter := range arg[1:] {
				letters = append(letters, "-"+string(letter))
			}
		}
		for _, letter := range letters {
			switch letter {
			case "-l", "--lazy":
				options.flags |= syscall.MNT_DETACH
			case "-f", "--force":
				options.flags |= syscall.MNT_FORCE
			case "-a", "--all":
				options.all = true
			case "-A", "--all-targets":
				options.allTargets = true
			case "-R", "--recursive":
				options.recursive = true
			case "-r", "--read-only":
				options.readOnly = true
			case "-v", "--verbose":
				options.verbose = true
			case "-q", "--quiet":
				options.quiet = true
			case "--fake":
				options.fake = true
			case "-n", "--no-mtab", "-c", "--no-canonicalize", "-d", "--detach-loop", "-i", "--internal-only":
				// There is no mtab and no helper to call; a loop device is
				// freed by the kernel when its last mount goes.
			case "-t", "--types":
				text, ok := needValue()
				if !ok {
					return 1
				}
				options.filterTypes = text
			case "-O", "--test-opts":
				text, ok := needValue()
				if !ok {
					return 1
				}
				options.filterOpts = text
			default:
				if strings.HasPrefix(letter, "--") {
					return umountUsage("unrecognized option '%s'", letter)
				}
				return umountUsage("invalid option -- '%c'", letter[1])
			}
		}
	}

	table, err := readMountTable()
	if err != nil {
		fatalf("umount", "%v", err)
		return 1
	}
	if options.all {
		// Everything but the root, deepest first, so a mount point is free by
		// the time its parent is unmounted.
		for _, entry := range table {
			if entry.target == "/" || !mountTypeSelected(options.filterTypes, entry.fstype) {
				continue
			}
			if options.filterOpts != "" && !mountOptionsSelected(options.filterOpts, entry.options) {
				continue
			}
			targets = append(targets, entry.target)
		}
		sort.SliceStable(targets, func(i, j int) bool { return len(targets[i]) > len(targets[j]) })
	}
	if len(targets) == 0 {
		fatalf("umount", "bad usage")
		fmt.Fprintln(os.Stderr, "Try 'umount --help' for more information.")
		return 1
	}

	var resolved []string
	for _, target := range targets {
		found, ok := umountResolve(&options, table, target)
		if !ok {
			return 1
		}
		resolved = append(resolved, found...)
	}
	status := 0
	for _, target := range resolved {
		if code := umountOne(&options, target); code != 0 {
			status = code
		}
	}
	if options.all && options.readOnly {
		if err := syscall.Mount("", "/", "", syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
			fatalf("umount", "/: %s", errText(err))
			status = 32
		}
	}
	return status
}

// umountResolve turns one operand into the mount points it names. The original
// resolves it against the mount table before the kernel sees it, so a path that
// carries no mount — or a device whose mount point it is — is reported from
// there rather than as an errno. -R adds everything mounted beneath it.
func umountResolve(options *umountOptions, table []mountEntry, target string) ([]string, bool) {
	canonical := target
	if absolute, err := filepath.Abs(target); err == nil {
		canonical = absolute
	}
	if evaluated, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = evaluated
	}
	var found []string
	for _, entry := range table {
		switch {
		case entry.target == canonical:
			found = append(found, entry.target)
		case entry.source == canonical || loopBackingFile(entry.source) == canonical:
			// A device names every mount point it is mounted at, though only
			// -A asks for more than the first.
			found = append(found, entry.target)
		}
	}
	if len(found) > 1 && !options.allTargets {
		found = found[:1]
	}
	if options.recursive {
		for _, entry := range table {
			if strings.HasPrefix(entry.target, canonical+"/") {
				found = append(found, entry.target)
			}
		}
		// The deepest mounts have to go first, or their parents are busy.
		sort.SliceStable(found, func(i, j int) bool { return len(found[i]) > len(found[j]) })
	}
	if len(found) == 0 {
		if options.quiet {
			return nil, true
		}
		if _, err := os.Lstat(canonical); err != nil {
			fatalf("umount", "%s: %s", target, errText(err))
			return nil, false
		}
		fatalf("umount", "%s: not mounted.", canonical)
		return nil, false
	}
	return found, true
}

// umountOne unmounts one target and reports the failure the way the original
// does, including which of its exit statuses the failure earns.
func umountOne(options *umountOptions, target string) int {
	if options.fake {
		if options.verbose {
			fmt.Printf("umount: %s (fake)\n", target)
		}
		return 0
	}
	if err := syscall.Unmount(target, options.flags); err != nil {
		if options.readOnly && syscall.Mount("", target, "", syscall.MS_REMOUNT|syscall.MS_RDONLY, "") == nil {
			if options.verbose {
				fmt.Printf("umount: %s busy - remounted read-only\n", target)
			}
			return 0
		}
		if options.quiet {
			return 32
		}
		// The original keeps status 32 for an unmount the kernel refused
		// rather than one the caller got wrong.
		switch {
		case errors.Is(err, syscall.EPERM) && os.Geteuid() != 0:
			fatalf("umount", "%s: must be superuser to unmount.", target)
		case errors.Is(err, syscall.EINVAL):
			fatalf("umount", "%s: not mounted.", target)
			return 1
		case errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ENOTDIR):
			fatalf("umount", "%s: %s", target, errText(err))
			return 1
		default:
			fatalf("umount", "%s: %s", target, errText(err))
		}
		return 32
	}
	if options.verbose {
		fmt.Printf("umount: %s unmounted\n", target)
	}
	return 0
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
