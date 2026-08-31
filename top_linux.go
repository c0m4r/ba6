// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// top deliberately keeps no ~/.toprc state. That makes its batch output
// repeatable in a rescue environment while still covering the useful command
// line and live-display controls from procps top.
type topOptions struct {
	batch, commandLine, threads, idle, cumulative, singleCPU bool
	iterations                                               int // zero means unlimited
	delay                                                    time.Duration
	summaryUnit, taskUnit                                    topMemoryUnit
	sort                                                     topSort
	descending                                               bool
	pids                                                     map[int]bool
	pidFilter                                                bool
	userFilter                                               *topUserFilter
	width                                                    int
	autoWidth                                                bool
	listFields, version, help, applyDefaults                 bool
}

type topUserFilter struct {
	uid    uint32
	any    bool
	invert bool
}

type topMemoryUnit uint8

const (
	topMemKiB topMemoryUnit = iota
	topMemMiB
	topMemGiB
	topMemTiB
	topMemPiB
	topMemEiB
)

func (unit topMemoryUnit) label() string {
	return [...]string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}[unit]
}

func (unit topMemoryUnit) divisor() float64 {
	return float64(uint64(1) << (10 * (unit + 1)))
}

func (unit topMemoryUnit) suffix() string {
	return strings.ToLower(unit.label()[:1])
}

func parseTopMemoryUnit(value string, summary bool) (topMemoryUnit, error) {
	if len(value) != 1 {
		return 0, fmt.Errorf("invalid memory scale %q", value)
	}
	var unit topMemoryUnit
	switch strings.ToLower(value) {
	case "k":
		unit = topMemKiB
	case "m":
		unit = topMemMiB
	case "g":
		unit = topMemGiB
	case "t":
		unit = topMemTiB
	case "p":
		unit = topMemPiB
	case "e":
		unit = topMemEiB
	default:
		return 0, fmt.Errorf("invalid memory scale %q", value)
	}
	if !summary && unit == topMemEiB {
		return 0, fmt.Errorf("invalid task memory scale %q", value)
	}
	return unit, nil
}

type topSort uint8

const (
	topSortCPU topSort = iota
	topSortPID
	topSortPPID
	topSortUID
	topSortUser
	topSortPriority
	topSortNice
	topSortThreads
	topSortTime
	topSortMemory
	topSortVirtual
	topSortResident
	topSortShared
	topSortState
	topSortCommand
)

// topSortFieldNames is both the useful, implemented subset of top -O and a
// single source of truth for -o spelling. The default screen only shows a
// subset of these fields, but every listed one can order a batch report.
var topSortFieldNames = []string{
	"PID", "PPID", "UID", "USER", "PR", "NI", "nTH", "%CPU", "TIME", "TIME+",
	"%MEM", "VIRT", "RES", "SHR", "S", "COMMAND",
}

func parseTopSort(value string) (topSort, bool, error) {
	if value == "" {
		return 0, false, fmt.Errorf("missing sort field")
	}
	descending := true
	switch value[0] {
	case '+':
		value = value[1:]
	case '-':
		descending, value = false, value[1:]
	}
	name := strings.ToUpper(value)
	switch name {
	case "PID":
		return topSortPID, descending, nil
	case "PPID":
		return topSortPPID, descending, nil
	case "UID":
		return topSortUID, descending, nil
	case "USER":
		return topSortUser, descending, nil
	case "PR", "PRIORITY":
		return topSortPriority, descending, nil
	case "NI", "NICE":
		return topSortNice, descending, nil
	case "NTH", "THREADS":
		return topSortThreads, descending, nil
	case "%CPU", "CPU", "PCPU":
		return topSortCPU, descending, nil
	case "TIME", "TIME+":
		return topSortTime, descending, nil
	case "%MEM", "MEM", "PMEM":
		return topSortMemory, descending, nil
	case "VIRT", "VSZ":
		return topSortVirtual, descending, nil
	case "RES", "RSS":
		return topSortResident, descending, nil
	case "SHR", "SHARED":
		return topSortShared, descending, nil
	case "S", "STATE", "STAT":
		return topSortState, descending, nil
	case "COMMAND", "CMD", "COMM", "ARGS":
		return topSortCommand, descending, nil
	default:
		return 0, false, fmt.Errorf("unrecognized field name %q", value)
	}
}

func cmdTop(args []string) int {
	options, err := parseTopOptions(args)
	if err != nil {
		fatalf("top", "%v", err)
		return 1
	}
	if options.help {
		if err := writeAppletHelp(os.Stdout, "top"); err != nil {
			fatalf("top", "write error: %v", err)
			return 1
		}
		return 0
	}
	if options.version {
		fmt.Fprintln(os.Stdout, "top from ba6")
		return 0
	}
	if options.listFields {
		for _, field := range topSortFieldNames {
			fmt.Fprintln(os.Stdout, field)
		}
		return 0
	}
	interactive := !options.batch && isTerminal(os.Stdin.Fd()) && isTerminal(os.Stdout.Fd())
	if interactive {
		return runTopInteractive(options)
	}
	// A redirected bare invocation used to produce one useful report. Preserve
	// that safe behaviour rather than leaving a pipeline running forever; -b
	// retains procps's unlimited batch semantics unless -n is given.
	if !options.batch && options.iterations == 0 {
		options.iterations = 1
	}
	return runTopBatch(options)
}

func parseTopOptions(args []string) (topOptions, error) {
	options := topOptions{
		delay:       3 * time.Second,
		summaryUnit: topMemMiB,
		taskUnit:    topMemKiB,
		sort:        topSortCPU,
		descending:  true,
		pids:        map[int]bool{},
	}
	args = expandTopShortOptions(args)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("option %s requires an argument", arg)
			}
			return args[index], nil
		}
		switch {
		case arg == "--":
			if index+1 < len(args) {
				return options, fmt.Errorf("unexpected operand %q", args[index+1])
			}
			index = len(args)
		case arg == "-A" || arg == "--apply-defaults":
			options.applyDefaults = true
		case arg == "-b" || arg == "--batch" || arg == "--batch-mode":
			options.batch = true
		case arg == "-c" || arg == "--cmdline-toggle":
			options.commandLine = !options.commandLine
		case arg == "-d" || arg == "--delay":
			parsed, parseErr := value()
			if parseErr != nil {
				return options, parseErr
			}
			delay, parseErr := parseTopDelay(parsed)
			if parseErr != nil {
				return options, parseErr
			}
			options.delay = delay
		case strings.HasPrefix(arg, "--delay="):
			delay, parseErr := parseTopDelay(strings.TrimPrefix(arg, "--delay="))
			if parseErr != nil {
				return options, parseErr
			}
			options.delay = delay
		case arg == "-E" || arg == "--scale-summary-mem":
			parsed, parseErr := value()
			if parseErr != nil {
				return options, parseErr
			}
			unit, parseErr := parseTopMemoryUnit(parsed, true)
			if parseErr != nil {
				return options, parseErr
			}
			options.summaryUnit = unit
		case strings.HasPrefix(arg, "--scale-summary-mem="):
			unit, parseErr := parseTopMemoryUnit(strings.TrimPrefix(arg, "--scale-summary-mem="), true)
			if parseErr != nil {
				return options, parseErr
			}
			options.summaryUnit = unit
		case arg == "-e" || arg == "--scale-task-mem":
			parsed, parseErr := value()
			if parseErr != nil {
				return options, parseErr
			}
			unit, parseErr := parseTopMemoryUnit(parsed, false)
			if parseErr != nil {
				return options, parseErr
			}
			options.taskUnit = unit
		case strings.HasPrefix(arg, "--scale-task-mem="):
			unit, parseErr := parseTopMemoryUnit(strings.TrimPrefix(arg, "--scale-task-mem="), false)
			if parseErr != nil {
				return options, parseErr
			}
			options.taskUnit = unit
		case arg == "-H" || arg == "--threads-show":
			options.threads = !options.threads
		case arg == "-i" || arg == "--idle-toggle":
			options.idle = !options.idle
		case arg == "-n" || arg == "--iterations":
			parsed, parseErr := value()
			if parseErr != nil {
				return options, parseErr
			}
			iterations, parseErr := parseTopIterations(parsed)
			if parseErr != nil {
				return options, parseErr
			}
			options.iterations = iterations
		case strings.HasPrefix(arg, "--iterations="):
			iterations, parseErr := parseTopIterations(strings.TrimPrefix(arg, "--iterations="))
			if parseErr != nil {
				return options, parseErr
			}
			options.iterations = iterations
		case arg == "-O" || arg == "--list-fields":
			options.listFields = true
		case arg == "-o" || arg == "--sort-override":
			parsed, parseErr := value()
			if parseErr != nil {
				return options, parseErr
			}
			sortField, descending, parseErr := parseTopSort(parsed)
			if parseErr != nil {
				return options, parseErr
			}
			options.sort, options.descending = sortField, descending
		case strings.HasPrefix(arg, "--sort-override="):
			sortField, descending, parseErr := parseTopSort(strings.TrimPrefix(arg, "--sort-override="))
			if parseErr != nil {
				return options, parseErr
			}
			options.sort, options.descending = sortField, descending
		case arg == "-p" || arg == "--pid":
			parsed, parseErr := value()
			if parseErr != nil {
				return options, parseErr
			}
			if parseErr := addTopPIDs(options.pids, parsed); parseErr != nil {
				return options, parseErr
			}
			options.pidFilter = true
		case strings.HasPrefix(arg, "--pid="):
			if parseErr := addTopPIDs(options.pids, strings.TrimPrefix(arg, "--pid=")); parseErr != nil {
				return options, parseErr
			}
			options.pidFilter = true
		case arg == "-S" || arg == "--accum-time-toggle":
			options.cumulative = !options.cumulative
		case arg == "-s" || arg == "--secure-mode":
			// ba6 keeps no editable runtime configuration and implements no
			// signal-sending commands, so its display is always secure.
		case arg == "-U" || arg == "--filter-any-user":
			parsed, parseErr := value()
			if parseErr != nil {
				return options, parseErr
			}
			filter, parseErr := parseTopUserFilter(parsed, true)
			if parseErr != nil {
				return options, parseErr
			}
			options.userFilter = &filter
		case strings.HasPrefix(arg, "--filter-any-user="):
			filter, parseErr := parseTopUserFilter(strings.TrimPrefix(arg, "--filter-any-user="), true)
			if parseErr != nil {
				return options, parseErr
			}
			options.userFilter = &filter
		case arg == "-u" || arg == "--filter-only-euser":
			parsed, parseErr := value()
			if parseErr != nil {
				return options, parseErr
			}
			filter, parseErr := parseTopUserFilter(parsed, false)
			if parseErr != nil {
				return options, parseErr
			}
			options.userFilter = &filter
		case strings.HasPrefix(arg, "--filter-only-euser="):
			filter, parseErr := parseTopUserFilter(strings.TrimPrefix(arg, "--filter-only-euser="), false)
			if parseErr != nil {
				return options, parseErr
			}
			options.userFilter = &filter
		case arg == "-w" || arg == "--width":
			options.autoWidth = true
			if index+1 < len(args) && !strings.HasPrefix(args[index+1], "-") {
				index++
				width, parseErr := parseTopWidth(args[index])
				if parseErr != nil {
					return options, parseErr
				}
				options.width = width
			}
		case strings.HasPrefix(arg, "--width="):
			width, parseErr := parseTopWidth(strings.TrimPrefix(arg, "--width="))
			if parseErr != nil {
				return options, parseErr
			}
			options.width, options.autoWidth = width, true
		case arg == "-1" || arg == "--single-cpu-toggle":
			options.singleCPU = !options.singleCPU
		case arg == "-V" || arg == "--version":
			options.version = true
		case arg == "-h" || arg == "--help" || arg == "-?":
			options.help = true
		case strings.HasPrefix(arg, "-"):
			return options, fmt.Errorf("unsupported option %q", arg)
		default:
			return options, fmt.Errorf("unexpected operand %q", arg)
		}
	}
	if options.applyDefaults && len(args) != 1 {
		return options, fmt.Errorf("-A must be used by itself")
	}
	if options.pidFilter && options.userFilter != nil {
		return options, fmt.Errorf("conflicting process selections (-p and -u/-U)")
	}
	return options, nil
}

// expandTopShortOptions implements getopt-style short option clusters while
// retaining -w's optional argument. The common forms -bn2 and -d0.2 therefore
// work just like their spaced equivalents.
func expandTopShortOptions(args []string) []string {
	result := make([]string, 0, len(args))
	expectsValue := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if expectsValue {
			result = append(result, arg)
			expectsValue = false
			continue
		}
		if arg == "--" {
			return append(result, args[index:]...)
		}
		if topLongOptionNeedsValue(arg) {
			result = append(result, arg)
			expectsValue = true
			continue
		}
		if len(arg) < 3 || arg[0] != '-' || arg[1] == '-' {
			result = append(result, arg)
			if len(arg) == 2 && topShortOptionNeedsValue(arg[1]) {
				expectsValue = true
			}
			continue
		}
		remaining := arg[1:]
		for len(remaining) > 0 {
			option := remaining[0]
			remaining = remaining[1:]
			result = append(result, "-"+string(option))
			switch option {
			case 'n', 'd', 'E', 'e', 'o', 'p', 'U', 'u', 'w':
				if len(remaining) > 0 {
					if remaining[0] == '=' {
						remaining = remaining[1:]
					}
					result = append(result, remaining)
				} else if option != 'w' {
					expectsValue = true
				}
				remaining = ""
			}
		}
	}
	return result
}

func topShortOptionNeedsValue(option byte) bool {
	return strings.ContainsRune("ndEeopUu", rune(option))
}

func topLongOptionNeedsValue(option string) bool {
	switch option {
	case "--delay", "--scale-summary-mem", "--scale-task-mem", "--iterations", "--sort-override", "--pid", "--filter-any-user", "--filter-only-euser":
		return true
	}
	return false
}

func parseTopDelay(value string) (time.Duration, error) {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 ||
		seconds > float64(math.MaxInt64)/float64(time.Second) {
		return 0, fmt.Errorf("invalid delay %q", value)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func parseTopIterations(value string) (int, error) {
	iterations, err := strconv.Atoi(value)
	if err != nil || iterations < 1 {
		return 0, fmt.Errorf("invalid iteration count %q", value)
	}
	return iterations, nil
}

func parseTopWidth(value string) (int, error) {
	width, err := strconv.Atoi(value)
	if err != nil || width < 1 || width > 512 {
		return 0, fmt.Errorf("invalid width %q", value)
	}
	return width, nil
}

func addTopPIDs(pids map[int]bool, value string) error {
	if value == "" {
		return fmt.Errorf("invalid PID list")
	}
	for _, part := range strings.Split(value, ",") {
		pid, err := strconv.Atoi(part)
		if err != nil || pid < 0 {
			return fmt.Errorf("invalid PID %q", part)
		}
		if pid == 0 {
			pid = os.Getpid()
		}
		pids[pid] = true
	}
	return nil
}

func parseTopUserFilter(value string, any bool) (topUserFilter, error) {
	filter := topUserFilter{any: any}
	if strings.HasPrefix(value, "!") {
		filter.invert, value = true, strings.TrimPrefix(value, "!")
	}
	if value == "" {
		return filter, fmt.Errorf("invalid user")
	}
	if numeric, err := strconv.ParseUint(value, 10, 32); err == nil {
		filter.uid = uint32(numeric) //nolint:gosec // limited to Linux's 32-bit uid_t.
		return filter, nil
	}
	account, err := user.Lookup(value)
	if err != nil {
		return filter, fmt.Errorf("invalid user %q", value)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return filter, fmt.Errorf("invalid user %q", value)
	}
	filter.uid = uint32(uid) //nolint:gosec // limited to Linux's 32-bit uid_t.
	return filter, nil
}

func (filter topUserFilter) matches(process processInfo) bool {
	matched := process.uid == filter.uid
	if filter.any {
		matched = matched || process.realUID == filter.uid || process.savedUID == filter.uid || process.fsUID == filter.uid
	}
	if filter.invert {
		return !matched
	}
	return matched
}

type topCPUTime struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (times topCPUTime) total() uint64 {
	return times.user + times.nice + times.system + times.idle + times.iowait + times.irq + times.softirq + times.steal
}

type topCPUStats struct {
	all   topCPUTime
	cores []topCPUTime
}

func readTopCPUStats() (topCPUStats, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return topCPUStats{}, err
	}
	return parseTopCPUStats(string(data))
}

func parseTopCPUStats(data string) (topCPUStats, error) {
	var stats topCPUStats
	found := false
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		name := fields[0]
		if name != "cpu" {
			if _, err := strconv.Atoi(strings.TrimPrefix(name, "cpu")); err != nil {
				continue
			}
		}
		values, err := parseTopCPUTimes(fields[1:])
		if err != nil {
			return topCPUStats{}, err
		}
		if name == "cpu" {
			stats.all, found = values, true
		} else {
			stats.cores = append(stats.cores, values)
		}
	}
	if !found {
		return topCPUStats{}, fmt.Errorf("invalid /proc/stat CPU data")
	}
	return stats, nil
}

func parseTopCPUTimes(values []string) (topCPUTime, error) {
	var result topCPUTime
	fields := []*uint64{&result.user, &result.nice, &result.system, &result.idle, &result.iowait, &result.irq, &result.softirq, &result.steal}
	for index, target := range fields {
		if index >= len(values) {
			break
		}
		value, err := strconv.ParseUint(values[index], 10, 64)
		if err != nil {
			return result, fmt.Errorf("invalid /proc/stat CPU counter")
		}
		*target = value
	}
	return result, nil
}

type topCPUUsage struct {
	user, nice, system, idle, iowait, irq, softirq, steal float64
}

func topCPUPercentages(current, previous topCPUTime, hasPrevious bool) topCPUUsage {
	if hasPrevious {
		if current.user < previous.user || current.nice < previous.nice || current.system < previous.system ||
			current.idle < previous.idle || current.iowait < previous.iowait || current.irq < previous.irq ||
			current.softirq < previous.softirq || current.steal < previous.steal {
			return topCPUUsage{}
		}
		current = topCPUTime{
			user: current.user - previous.user, nice: current.nice - previous.nice,
			system: current.system - previous.system, idle: current.idle - previous.idle,
			iowait: current.iowait - previous.iowait, irq: current.irq - previous.irq,
			softirq: current.softirq - previous.softirq, steal: current.steal - previous.steal,
		}
	}
	total := current.total()
	if total == 0 {
		return topCPUUsage{}
	}
	percent := func(value uint64) float64 { return float64(value) * 100 / float64(total) }
	return topCPUUsage{
		user: percent(current.user), nice: percent(current.nice), system: percent(current.system), idle: percent(current.idle),
		iowait: percent(current.iowait), irq: percent(current.irq), softirq: percent(current.softirq), steal: percent(current.steal),
	}
}

type topMemory struct {
	total, free, used, buffCache  uint64
	swapTotal, swapFree, swapUsed uint64
	available                     uint64
}

func topMemoryFromMeminfo(values map[string]uint64) topMemory {
	// readMeminfo normalises the kernel's KiB values to bytes for every
	// caller, including free(1), so keep that unit here as well.
	add := func(values ...uint64) uint64 {
		total := uint64(0)
		for _, value := range values {
			if value > ^uint64(0)-total {
				return ^uint64(0)
			}
			total += value
		}
		return total
	}
	memory := topMemory{
		total:     values["MemTotal"],
		free:      values["MemFree"],
		available: values["MemAvailable"],
		swapTotal: values["SwapTotal"],
		swapFree:  values["SwapFree"],
		buffCache: add(values["Buffers"], values["Cached"], values["SReclaimable"]),
	}
	if memory.available > 0 && memory.total >= memory.available {
		memory.used = memory.total - memory.available
	} else if memory.total >= memory.free+memory.buffCache {
		memory.used = memory.total - memory.free - memory.buffCache
	}
	if memory.swapTotal >= memory.swapFree {
		memory.swapUsed = memory.swapTotal - memory.swapFree
	}
	return memory
}

type topSnapshot struct {
	now       time.Time
	uptime    float64
	loads     [3]string
	users     int
	cpu       topCPUStats
	memory    topMemory
	processes []processInfo
}

func readTopSnapshot(options topOptions) (topSnapshot, error) {
	var snapshot topSnapshot
	uptimeData, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return snapshot, err
	}
	uptimeFields := strings.Fields(string(uptimeData))
	if len(uptimeFields) == 0 {
		return snapshot, fmt.Errorf("invalid /proc/uptime")
	}
	snapshot.uptime, err = strconv.ParseFloat(uptimeFields[0], 64)
	if err != nil || snapshot.uptime < 0 {
		return snapshot, fmt.Errorf("invalid /proc/uptime")
	}
	snapshot.now = time.Now()
	if loadData, readErr := os.ReadFile("/proc/loadavg"); readErr == nil {
		fields := strings.Fields(string(loadData))
		for index := range snapshot.loads {
			if index < len(fields) {
				snapshot.loads[index] = fields[index]
			} else {
				snapshot.loads[index] = "?"
			}
		}
	} else {
		snapshot.loads = [3]string{"?", "?", "?"}
	}
	snapshot.cpu, err = readTopCPUStats()
	if err != nil {
		return snapshot, err
	}
	if values, readErr := readMeminfo(); readErr == nil {
		snapshot.memory = topMemoryFromMeminfo(values)
	}
	snapshot.users = topUserCount()
	snapshot.processes, err = readTopProcesses(options)
	if err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

var systemdSessionsDir = "/run/systemd/sessions"

// topUserCount counts logged-in users the way procps does: systemd sessions
// first (only active ones whose class starts with "user"), falling back to
// Linux's fixed-size utmp records when systemd is not running. It avoids a
// child process and is deliberately best-effort: minimal recovery images often
// do not create an utmp file at all.
func topUserCount() int {
	if sessions, err := os.ReadDir(systemdSessionsDir); err == nil && len(sessions) > 0 {
		count := 0
		for _, entry := range sessions {
			// The directory also holds "<id>.ref" FIFOs that systemd keeps
			// open for reference counting. Reading one blocks until systemd
			// drops the session, so only plain session files are parsed.
			if !entry.Type().IsRegular() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(systemdSessionsDir, entry.Name()))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "CLASS=user") {
					count++
				}
			}
		}
		return count
	}
	const (
		topUtmpRecordSize  = 384
		topUtmpUserProcess = 7
		topUtmpTypeOffset  = 0
		topUtmpIDOffset    = 40
		topUtmpIDSize      = 4
		topUtmpUserOffset  = 44
		topUtmpUserSize    = 32
	)
	for _, name := range []string{"/var/run/utmp", "/run/utmp"} {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		count := 0
		for offset := 0; offset+topUtmpRecordSize <= len(data); offset += topUtmpRecordSize {
			if binary.LittleEndian.Uint16(data[offset+topUtmpTypeOffset:]) != topUtmpUserProcess {
				continue
			}
			// A USER_PROCESS slot without its terminal ID is stale or
			// incomplete. glibc's session readers ignore those records too.
			id := data[offset+topUtmpIDOffset : offset+topUtmpIDOffset+topUtmpIDSize]
			if strings.TrimRight(string(id), "\x00") == "" {
				continue
			}
			name := data[offset+topUtmpUserOffset : offset+topUtmpUserOffset+topUtmpUserSize]
			if strings.TrimRight(string(name), "\x00") != "" {
				count++
			}
		}
		return count
	}
	return 0
}

func readTopProcesses(options topOptions) ([]processInfo, error) {
	var selected map[int]bool
	if options.pidFilter {
		selected = options.pids
	}
	processes, err := readProcesses(selected)
	if err != nil || !options.threads {
		return filterTopProcesses(processes, options.userFilter), err
	}
	threads := make([]processInfo, 0, len(processes))
	for _, process := range processes {
		entries, readErr := os.ReadDir(filepath.Join("/proc", strconv.Itoa(process.pid), "task"))
		if readErr != nil {
			// A process can exit after readProcesses has seen it. Keep the
			// leader's still-valid snapshot instead of losing it entirely.
			threads = append(threads, process)
			continue
		}
		for _, entry := range entries {
			tid, parseErr := strconv.Atoi(entry.Name())
			if parseErr != nil {
				continue
			}
			if tid == process.pid {
				threads = append(threads, process)
				continue
			}
			thread, readErr := readTopThread(process, tid)
			if readErr == nil {
				threads = append(threads, thread)
			}
		}
	}
	sort.Slice(threads, func(left, right int) bool { return threads[left].pid < threads[right].pid })
	return filterTopProcesses(threads, options.userFilter), nil
}

func filterTopProcesses(processes []processInfo, filter *topUserFilter) []processInfo {
	if filter == nil {
		return processes
	}
	filtered := make([]processInfo, 0, len(processes))
	for _, process := range processes {
		if filter.matches(process) {
			filtered = append(filtered, process)
		}
	}
	return filtered
}

// readTopThread starts from the leader's metadata (UID, resident accounting and
// command line are process-wide), then replaces fields that Linux reports per
// thread in /proc/PID/task/TID/stat.
func readTopThread(leader processInfo, tid int) (processInfo, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(leader.pid), "task", strconv.Itoa(tid), "stat"))
	if err != nil {
		return processInfo{}, err
	}
	thread := leader
	text := string(data)
	left, right := strings.IndexByte(text, '('), strings.LastIndex(text, ") ")
	if left < 0 || right < left {
		return processInfo{}, fmt.Errorf("invalid task stat data")
	}
	fields := strings.Fields(text[right+2:])
	if len(fields) < 22 {
		return processInfo{}, fmt.Errorf("short task stat data")
	}
	thread.pid, thread.comm = tid, text[left+1:right]
	thread.state = fields[0]
	thread.ppid, _ = strconv.Atoi(fields[1])
	thread.utime, _ = strconv.ParseUint(fields[11], 10, 64)
	thread.stime, _ = strconv.ParseUint(fields[12], 10, 64)
	thread.cutime, _ = strconv.ParseUint(fields[13], 10, 64)
	thread.cstime, _ = strconv.ParseUint(fields[14], 10, 64)
	thread.priority, _ = strconv.Atoi(fields[15])
	thread.nice, _ = strconv.Atoi(fields[16])
	thread.threads, _ = strconv.Atoi(fields[17])
	thread.startTicks, _ = strconv.ParseUint(fields[19], 10, 64)
	return thread, nil
}

type topSample struct {
	at    time.Time
	cpu   topCPUStats
	ticks map[int]uint64
}

type topMonitor struct {
	options  topOptions
	previous topSample
	hasPrev  bool
}

func newTopMonitor(options topOptions) *topMonitor {
	return &topMonitor{options: options}
}

func (monitor *topMonitor) resetCPUHistory() {
	monitor.previous, monitor.hasPrev = topSample{}, false
}

type topRow struct {
	process processInfo
	cpu     float64
	memory  float64
	ticks   uint64
}

type topDisplay struct {
	snapshot topSnapshot
	cpu      topCPUUsage
	coreCPU  []topCPUUsage
	rows     []topRow
}

func (monitor *topMonitor) sample() (topDisplay, error) {
	snapshot, err := readTopSnapshot(monitor.options)
	if err != nil {
		return topDisplay{}, err
	}
	display := topDisplay{snapshot: snapshot, cpu: topCPUPercentages(snapshot.cpu.all, monitor.previous.cpu.all, monitor.hasPrev)}
	for index, core := range snapshot.cpu.cores {
		previous, hasPrevious := topCPUTime{}, false
		if monitor.hasPrev && index < len(monitor.previous.cpu.cores) {
			previous, hasPrevious = monitor.previous.cpu.cores[index], true
		}
		display.coreCPU = append(display.coreCPU, topCPUPercentages(core, previous, hasPrevious))
	}
	elapsed := snapshot.now.Sub(monitor.previous.at).Seconds()
	current := topSample{at: snapshot.now, cpu: snapshot.cpu, ticks: make(map[int]uint64, len(snapshot.processes))}
	for _, process := range snapshot.processes {
		ticks := topProcessTicks(process, monitor.options.cumulative)
		current.ticks[process.pid] = ticks
		row := topRow{process: process, ticks: ticks}
		if monitor.hasPrev && elapsed > 0 {
			if previous, ok := monitor.previous.ticks[process.pid]; ok && ticks >= previous {
				row.cpu = float64(ticks-previous) * 100 / float64(clockTicks) / elapsed
			}
		}
		if snapshot.memory.total > 0 {
			row.memory = float64(process.rss) * 100 / float64(snapshot.memory.total)
		}
		// top keeps the first idle-filtered sample visible because it has no
		// interval from which to decide whether a task was idle.
		if !monitor.options.idle || !monitor.hasPrev || row.cpu != 0 || process.state == "R" {
			display.rows = append(display.rows, row)
		}
	}
	monitor.previous, monitor.hasPrev = current, true
	sortTopRows(display.rows, monitor.options)
	return display, nil
}

func topProcessTicks(process processInfo, cumulative bool) uint64 {
	ticks := process.utime + process.stime
	if cumulative {
		ticks += process.cutime + process.cstime
	}
	return ticks
}

func sortTopRows(rows []topRow, options topOptions) {
	sort.SliceStable(rows, func(left, right int) bool {
		comparison := compareTopRows(rows[left], rows[right], options.sort)
		if comparison == 0 {
			return rows[left].process.pid < rows[right].process.pid
		}
		if options.descending {
			return comparison > 0
		}
		return comparison < 0
	})
}

func compareTopRows(left, right topRow, field topSort) int {
	compareInt := func(a, b int) int {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	}
	compareUint := func(a, b uint64) int {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	}
	compareFloat := func(a, b float64) int {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	}
	switch field {
	case topSortPID:
		return compareInt(left.process.pid, right.process.pid)
	case topSortPPID:
		return compareInt(left.process.ppid, right.process.ppid)
	case topSortUID:
		return compareUint(uint64(left.process.uid), uint64(right.process.uid))
	case topSortUser:
		return strings.Compare(left.process.user, right.process.user)
	case topSortPriority:
		return compareInt(left.process.priority, right.process.priority)
	case topSortNice:
		return compareInt(left.process.nice, right.process.nice)
	case topSortThreads:
		return compareInt(left.process.threads, right.process.threads)
	case topSortTime:
		return compareUint(left.ticks, right.ticks)
	case topSortMemory:
		return compareFloat(left.memory, right.memory)
	case topSortVirtual:
		return compareUint(left.process.vsz, right.process.vsz)
	case topSortResident:
		return compareUint(left.process.rss, right.process.rss)
	case topSortShared:
		return compareUint(left.process.shared, right.process.shared)
	case topSortState:
		return strings.Compare(left.process.state, right.process.state)
	case topSortCommand:
		return strings.Compare(left.process.args, right.process.args)
	default:
		return compareFloat(left.cpu, right.cpu)
	}
}

func runTopBatch(options topOptions) int {
	monitor := newTopMonitor(options)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	for iteration := 0; options.iterations == 0 || iteration < options.iterations; iteration++ {
		if iteration > 0 {
			if !waitTopDelay(options.delay, options.iterations == 0, signals) {
				return 0
			}
			fmt.Fprintln(os.Stdout)
		}
		width := topOutputWidth(options, 0)
		output, err := monitor.render(width, 0, "")
		if err != nil {
			fatalf("top", "%v", err)
			return 1
		}
		if _, err := fmt.Fprint(os.Stdout, output); err != nil {
			fatalf("top", "write error: %v", err)
			return 1
		}
	}
	return 0
}

func waitTopDelay(delay time.Duration, unlimited bool, signals <-chan os.Signal) bool {
	if delay <= 0 {
		select {
		case <-signals:
			return false
		default:
		}
		if unlimited {
			// procps accepts zero as a delay. A tiny yield keeps an unlimited
			// ba6 batch report from becoming an accidental busy loop.
			time.Sleep(10 * time.Millisecond)
		}
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-signals:
		return false
	case <-timer.C:
		return true
	}
}

func runTopInteractive(options topOptions) int {
	fd := os.Stdin.Fd()
	original, err := terminalRaw(fd)
	if err != nil {
		fatalf("top", "stdin is not a terminal: %v", err)
		return 1
	}
	defer restoreTerminal(fd, original)
	monitor := newTopMonitor(options)
	message := ""
	paint := func() bool {
		rows, columns := 24, 80
		if terminalRows, terminalColumns, ok := terminalDimensions(fd); ok {
			rows, columns = terminalRows, terminalColumns
		}
		width := topOutputWidth(monitor.options, columns)
		output, renderErr := monitor.render(width, rows, message)
		if renderErr != nil {
			fatalf("top", "%v", renderErr)
			return false
		}
		if _, renderErr = fmt.Fprint(os.Stdout, topInteractiveScreen(output)); renderErr != nil {
			fatalf("top", "write error: %v", renderErr)
			return false
		}
		return true
	}
	if !paint() {
		return 1
	}
	updates := 1
	if options.iterations > 0 && updates >= options.iterations {
		return 0
	}
	keys := make(chan byte, 16)
	go readTopKeys(keys)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	delay := options.delay
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	ticker := time.NewTicker(delay)
	defer ticker.Stop()
	for {
		select {
		case <-signals:
			return 0
		case <-ticker.C:
			message = ""
			if !paint() {
				return 1
			}
			updates++
			if options.iterations > 0 && updates >= options.iterations {
				return 0
			}
		case key := <-keys:
			if key == 'q' || key == 'Q' || key == 3 {
				return 0
			}
			changed := true
			switch key {
			case ' ':
				message = ""
			case 'c':
				monitor.options.commandLine = !monitor.options.commandLine
				message = "COMMAND now shows command lines"
			case 'i':
				monitor.options.idle = !monitor.options.idle
				message = "idle-task filter toggled"
			case 'S':
				monitor.options.cumulative = !monitor.options.cumulative
				monitor.resetCPUHistory()
				message = "cumulative CPU time toggled"
			case '1':
				monitor.options.singleCPU = !monitor.options.singleCPU
				message = "per-CPU summary toggled"
			case 'P':
				monitor.options.sort, monitor.options.descending = topSortCPU, true
				message = "sorting by CPU"
			case 'M':
				monitor.options.sort, monitor.options.descending = topSortMemory, true
				message = "sorting by memory"
			case 'N':
				monitor.options.sort, monitor.options.descending = topSortPID, true
				message = "sorting by PID"
			case 'T':
				monitor.options.sort, monitor.options.descending = topSortTime, true
				message = "sorting by CPU time"
			case 'R':
				monitor.options.descending = !monitor.options.descending
				message = "sort direction reversed"
			case 'h', '?':
				message = "q quit  SPACE refresh  c command  i idle  P/M/N/T sort  R reverse  S cumulative  1 CPUs"
			default:
				changed = false
			}
			if changed && !paint() {
				return 1
			}
		}
	}
}

// terminalRaw disables OPOST, so a bare '\n' advances only to the next row;
// it does not return to column zero. The batch renderer intentionally emits
// LF-only text for pipes, while the live terminal needs CRLF to keep every
// process row aligned at its first column.
func topInteractiveScreen(output string) string {
	return "\x1b[H\x1b[2J" + strings.ReplaceAll(output, "\n", "\r\n")
}

func readTopKeys(keys chan<- byte) {
	buffer := []byte{0}
	for {
		if _, err := os.Stdin.Read(buffer); err != nil {
			return
		}
		keys <- buffer[0]
	}
}

func topOutputWidth(options topOptions, terminalWidth int) int {
	if options.width > 0 {
		return options.width
	}
	if options.autoWidth {
		if columns, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && columns >= 1 && columns <= 512 {
			return columns
		}
	}
	if terminalWidth > 0 {
		return minInt(terminalWidth, 512)
	}
	return 512
}

func (monitor *topMonitor) render(width, screenRows int, message string) (string, error) {
	display, err := monitor.sample()
	if err != nil {
		return "", err
	}
	return formatTopDisplay(display, monitor.options, width, screenRows, message), nil
}

func formatTopDisplay(display topDisplay, options topOptions, width, screenRows int, message string) string {
	var output strings.Builder
	writeLine := func(line string) {
		if width > 0 {
			line = topClipLine(line, width)
		}
		output.WriteString(line)
		output.WriteByte('\n')
	}
	users := "users"
	if display.snapshot.users == 1 {
		users = "user"
	}
	writeLine(fmt.Sprintf("top - %s up %5s,  %d %s,  load average: %s, %s, %s", display.snapshot.now.Format("15:04:05"),
		topFormatUptime(display.snapshot.uptime), display.snapshot.users, users,
		display.snapshot.loads[0], display.snapshot.loads[1], display.snapshot.loads[2]))
	writeLine(topTaskLine(display.rows, options.threads))
	if options.singleCPU && len(display.snapshot.cpu.cores) > 0 {
		for index, usage := range display.coreCPU {
			writeLine(topCPULine(fmt.Sprintf("%%Cpu%d(s):", index), usage))
		}
	} else {
		writeLine(topCPULine("%Cpu(s):", display.cpu))
	}
	writeLine(topMemoryLine(options.summaryUnit, display.snapshot.memory, false))
	writeLine(topMemoryLine(options.summaryUnit, display.snapshot.memory, true))
	writeLine("")
	writeLine("    PID USER      PR  NI    VIRT    RES    SHR S  %CPU  %MEM     TIME+ COMMAND")
	limit := len(display.rows)
	if screenRows > 0 {
		cpuLines := 1
		if options.singleCPU && len(display.snapshot.cpu.cores) > 0 {
			cpuLines = len(display.snapshot.cpu.cores)
		}
		headerLines := 6 + cpuLines // summary, blank line, and table heading
		if message != "" {
			headerLines++
		}
		limit = minInt(limit, maxInt(0, screenRows-headerLines))
	}
	for _, row := range display.rows[:limit] {
		writeLine(topProcessLine(row, options))
	}
	if message != "" {
		writeLine(message)
	}
	return output.String()
}

func topTaskLine(rows []topRow, threads bool) string {
	running, sleeping, dSleep, stopped, zombie := 0, 0, 0, 0, 0
	for _, row := range rows {
		switch row.process.state {
		case "R":
			running++
		case "D":
			dSleep++
		case "T", "t":
			stopped++
		case "Z":
			zombie++
		default:
			sleeping++
		}
	}
	label := "Tasks"
	if threads {
		label = "Threads"
	}
	return fmt.Sprintf("%s: %d total, %d running, %d sleep, %d d-sleep, %d stopped, %d zombie",
		label, len(rows), running, sleeping, dSleep, stopped, zombie)
}

func topCPULine(label string, usage topCPUUsage) string {
	return fmt.Sprintf("%-8s %4.1f us, %4.1f sy, %4.1f ni, %4.1f id, %4.1f wa, %4.1f hi, %4.1f si, %4.1f st ",
		label, usage.user, usage.system, usage.nice, usage.idle, usage.iowait, usage.irq, usage.softirq, usage.steal)
}

func topMemoryLine(unit topMemoryUnit, memory topMemory, swap bool) string {
	if swap {
		return fmt.Sprintf("%s Swap:%9s total,%9s free,%9s used.%9s avail Mem",
			unit.label(), topSummaryMemory(memory.swapTotal, unit), topSummaryMemory(memory.swapFree, unit),
			topSummaryMemory(memory.swapUsed, unit), topSummaryMemory(memory.available, unit))
	}
	return fmt.Sprintf("%s Mem :%9s total,%9s free,%9s used,%9s buff/cache",
		unit.label(), topSummaryMemory(memory.total, unit), topSummaryMemory(memory.free, unit),
		topSummaryMemory(memory.used, unit), topSummaryMemory(memory.buffCache, unit))
}

func topSummaryMemory(value uint64, unit topMemoryUnit) string {
	if unit == topMemKiB {
		return strconv.FormatUint(value/1024, 10)
	}
	return fmt.Sprintf("%.1f", float64(value)/unit.divisor())
}

func topProcessLine(row topRow, options topOptions) string {
	process := row.process
	command := process.comm
	if options.commandLine {
		command = process.args
	}
	command = topSafeText(command)
	state := process.state
	if state == "" {
		state = "?"
	}
	return fmt.Sprintf("%7d %-8s %3s %3d %7s %6s %6s %.1s %5.1f %5.1f %9s %s",
		process.pid, topClipLine(process.user, 8), topPriority(process.priority), process.nice,
		topTaskMemory(process.vsz, options.taskUnit), topTaskMemory(process.rss, options.taskUnit),
		topTaskMemory(process.shared, options.taskUnit), state, row.cpu, row.memory, topCPUTimeString(row.ticks), command)
}

func topPriority(priority int) string {
	if priority < 0 {
		return "rt"
	}
	return strconv.Itoa(priority)
}

func topTaskMemory(value uint64, unit topMemoryUnit) string {
	if unit == topMemKiB {
		return strconv.FormatUint(value/1024, 10)
	}
	return fmt.Sprintf("%.1f%s", float64(value)/unit.divisor(), unit.suffix())
}

func topCPUTimeString(ticks uint64) string {
	seconds := ticks / clockTicks
	fraction := (ticks % clockTicks) * 100 / clockTicks
	minutes := seconds / 60
	return fmt.Sprintf("%d:%02d.%02d", minutes, seconds%60, fraction)
}

func topFormatUptime(seconds float64) string {
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return "?"
	}
	totalMinutes := uint64(seconds) / 60
	days, hours, minutes := totalMinutes/(24*60), totalMinutes/60%24, totalMinutes%60
	if days == 0 {
		return fmt.Sprintf("%d:%02d", hours, minutes)
	}
	dayLabel := "days"
	if days == 1 {
		dayLabel = "day"
	}
	return fmt.Sprintf("%d %s,  %d:%02d", days, dayLabel, hours, minutes)
}

func topClipLine(value string, width int) string {
	if width < 1 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:width])
}

// Command lines belong to other processes and can contain terminal control
// bytes. A monitor must never replay those bytes into its own live display.
func topSafeText(value string) string {
	return strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return '?'
		}
		return character
	}, value)
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
