// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// clockTicks is the kernel's USER_HZ, the unit of the times in /proc/PID/stat.
// A static binary cannot call sysconf(_SC_CLK_TCK), and Linux has used 100 on
// every architecture ba6 targets.
const clockTicks = 100

type processInfo struct {
	pid, ppid, pgrp int
	session, tpgid  int
	tty             int
	uid             uint32
	user            string
	state           string
	nice            int
	threads         int
	vsz, rss        uint64
	utime, stime    uint64 // clock ticks of user and system time
	startTicks      uint64 // clock ticks between boot and process start
	comm            string
	args            string
}

// psOptions holds one command line. Selection follows ps(1): the BSD options
// "a" and "x" together lift every restriction, "a" alone keeps the processes
// that hold a terminal, and "x" alone keeps the caller's own.
type psOptions struct {
	full                bool
	userFormat          bool
	bsdTerminal, bsdOwn bool
	selected            map[int]bool
	columns             []string
}

func cmdPs(args []string) int {
	options := psOptions{selected: map[int]bool{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-p" || arg == "--pid":
			i++
			if i >= len(args) || parsePIDList(args[i], options.selected) != nil {
				fatalf("ps", "invalid PID list")
				return 1
			}
		case strings.HasPrefix(arg, "--pid="):
			if parsePIDList(strings.TrimPrefix(arg, "--pid="), options.selected) != nil {
				fatalf("ps", "invalid PID list")
				return 1
			}
		case arg == "-o" || arg == "--format":
			i++
			if i >= len(args) {
				fatalf("ps", "-o requires an argument")
				return 1
			}
			options.columns = splitPSColumns(args[i])
		case strings.HasPrefix(arg, "--format="):
			options.columns = splitPSColumns(strings.TrimPrefix(arg, "--format="))
		case len(arg) > 1 && arg[0] == '-':
			for _, flag := range arg[1:] {
				switch flag {
				case 'e', 'A':
				case 'f':
					options.full = true
				default:
					fatalf("ps", "invalid option -- '%c'", flag)
					return 1
				}
			}
		default:
			// BSD options carry no dash, so "ps axu" is a single operand
			// naming three of them.
			if err := options.parseBSD(arg); err != nil {
				fatalf("ps", "%v", err)
				return 1
			}
		}
	}
	options.applyDefaultColumns()
	for _, column := range options.columns {
		if _, ok := psColumns[psColumnName(column)]; !ok {
			fatalf("ps", "unknown output column %q", column)
			return 1
		}
	}
	processes, err := readProcesses(options.selected)
	if err != nil {
		fatalf("ps", "%v", err)
		return 1
	}
	writePS(options.filter(processes), options.columns, newPSRuntime())
	return 0
}

// parseBSD reads one dashless operand: either a PID list, as in "ps 1 2", or a
// group of BSD option letters, as in "ps axu".
func (o *psOptions) parseBSD(arg string) error {
	if arg != "" && strings.IndexFunc(arg, func(r rune) bool { return r != ',' && (r < '0' || r > '9') }) < 0 {
		if parsePIDList(arg, o.selected) != nil {
			return fmt.Errorf("invalid PID list")
		}
		return nil
	}
	for _, flag := range arg {
		switch flag {
		case 'a':
			o.bsdTerminal = true
		case 'x':
			o.bsdOwn = true
		case 'u':
			o.userFormat = true
		case 'A':
			// The BSD "A" is the same selection as -e; the dashless "e" means
			// something else entirely (show the environment) and is not
			// supported here.
			o.bsdTerminal, o.bsdOwn = true, true
		case 'w':
			// Wide output. Command lines are never truncated here.
		default:
			return fmt.Errorf("unsupported operand %q", arg)
		}
	}
	return nil
}

func (o *psOptions) applyDefaultColumns() {
	if len(o.columns) > 0 {
		return
	}
	switch {
	case o.userFormat:
		o.columns = []string{"user", "pid", "pcpu", "pmem", "vsz", "rss", "tty", "stat", "start", "time", "args"}
	case o.full:
		o.columns = []string{"user", "pid", "ppid", "vsz", "rss", "stat", "args"}
	case o.bsdTerminal || o.bsdOwn:
		o.columns = []string{"pid", "tty", "stat", "time", "args"}
	default:
		o.columns = []string{"pid", "stat", "args"}
	}
}

// filter applies BSD selection. Without -p and without a or x, every process is
// listed, which is what a rescue shell wants and what ba6 has always done.
func (o *psOptions) filter(processes []processInfo) []processInfo {
	if len(o.selected) > 0 || o.bsdTerminal && o.bsdOwn || !o.bsdTerminal && !o.bsdOwn {
		return processes
	}
	//nolint:gosec // G115: a Linux user ID is a 32-bit unsigned value.
	euid := uint32(os.Geteuid())
	kept := make([]processInfo, 0, len(processes))
	for _, process := range processes {
		if o.bsdTerminal && process.tty != 0 || o.bsdOwn && process.uid == euid {
			kept = append(kept, process)
		}
	}
	return kept
}

func parsePIDList(value string, result map[int]bool) error {
	for _, part := range strings.Split(value, ",") {
		pid, err := strconv.Atoi(part)
		if err != nil || pid < 1 {
			return fmt.Errorf("invalid pid")
		}
		result[pid] = true
	}
	return nil
}

func splitPSColumns(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return r == ',' || r == ' ' })
}

// psColumn describes one output column. ps(1) reserves a fixed width for most
// columns: a numeric value wider than its column simply overflows and shifts
// the rest of that row, while a name is cut short and marked with a trailing
// "+". Only the PID and command columns are sized from the data.
type psColumn struct {
	heading string
	right   bool
	width   int
	grow    bool
	value   func(psRuntime, processInfo) string
}

var psColumns = map[string]psColumn{
	"pid":   {"PID", true, 0, true, func(_ psRuntime, p processInfo) string { return strconv.Itoa(p.pid) }},
	"ppid":  {"PPID", true, 0, true, func(_ psRuntime, p processInfo) string { return strconv.Itoa(p.ppid) }},
	"uid":   {"UID", true, 5, true, func(_ psRuntime, p processInfo) string { return strconv.FormatUint(uint64(p.uid), 10) }},
	"user":  {"USER", false, 8, false, func(_ psRuntime, p processInfo) string { return p.user }},
	"stat":  {"STAT", false, 4, false, func(_ psRuntime, p processInfo) string { return psState(p) }},
	"vsz":   {"VSZ", true, 6, false, func(_ psRuntime, p processInfo) string { return strconv.FormatUint((p.vsz+1023)/1024, 10) }},
	"rss":   {"RSS", true, 5, false, func(_ psRuntime, p processInfo) string { return strconv.FormatUint((p.rss+1023)/1024, 10) }},
	"pcpu":  {"%CPU", true, 4, false, func(r psRuntime, p processInfo) string { return fmt.Sprintf("%.1f", r.cpuPercent(p)) }},
	"pmem":  {"%MEM", true, 4, false, func(r psRuntime, p processInfo) string { return fmt.Sprintf("%.1f", r.memoryPercent(p)) }},
	"tty":   {"TTY", false, 8, false, func(_ psRuntime, p processInfo) string { return ttyName(p.tty) }},
	"start": {"START", true, 5, false, func(r psRuntime, p processInfo) string { return r.startTime(p) }},
	"time":  {"TIME", true, 6, false, func(_ psRuntime, p processInfo) string { return psCPUTime(p) }},
	"comm":  {"COMMAND", false, 0, true, func(_ psRuntime, p processInfo) string { return p.comm }},
	"args":  {"COMMAND", false, 0, true, func(_ psRuntime, p processInfo) string { return p.args }},
}

// psColumnName resolves the spellings ps(1) accepts for the same column.
func psColumnName(column string) string {
	switch column {
	case "%cpu", "pcpu":
		return "pcpu"
	case "%mem", "pmem":
		return "pmem"
	case "cmd", "command", "args":
		return "args"
	case "tt", "tty":
		return "tty"
	case "cputime", "time":
		return "time"
	case "s", "state", "stat":
		return "stat"
	}
	return column
}

// psState builds the STAT field: the run state followed by the modifiers ps
// documents, in its order -- priority, session leader, multithreaded, and
// foreground process group.
func psState(p processInfo) string {
	state := p.state
	switch {
	case p.nice < 0:
		state += "<"
	case p.nice > 0:
		state += "N"
	}
	if p.session == p.pid {
		state += "s"
	}
	if p.threads > 1 {
		state += "l"
	}
	if p.tty != 0 && p.tpgid == p.pgrp {
		state += "+"
	}
	return state
}

func psCPUTime(p processInfo) string {
	seconds := (p.utime + p.stime) / clockTicks
	if hours := seconds / 3600; hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

// psRuntime holds the system-wide values the computed columns need, so that
// every row is rendered against one consistent snapshot.
type psRuntime struct {
	uptime   float64
	memTotal uint64
	boot     time.Time
	now      time.Time
	pidWidth int
}

func newPSRuntime() psRuntime {
	runtime := psRuntime{now: time.Now(), pidWidth: 5}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		if fields := strings.Fields(string(data)); len(fields) > 0 {
			runtime.uptime, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	runtime.boot = runtime.now.Add(-time.Duration(runtime.uptime * float64(time.Second)))
	if values, err := readMeminfo(); err == nil {
		runtime.memTotal = values["MemTotal"]
	}
	// ps sizes its PID columns from the largest PID the kernel will hand out.
	if data, err := os.ReadFile("/proc/sys/kernel/pid_max"); err == nil {
		if width := len(strings.TrimSpace(string(data))); width > runtime.pidWidth {
			runtime.pidWidth = width
		}
	}
	return runtime
}

func (r psRuntime) cpuPercent(p processInfo) float64 {
	lifetime := r.uptime - float64(p.startTicks)/clockTicks
	if lifetime <= 0 {
		return 0
	}
	return float64(p.utime+p.stime) / clockTicks / lifetime * 100
}

func (r psRuntime) memoryPercent(p processInfo) float64 {
	if r.memTotal == 0 {
		return 0
	}
	return float64(p.rss) / float64(r.memTotal) * 100
}

// startTime prints the wall-clock start of a process the way ps does: the time
// of day for processes started today, the date within this year, and otherwise
// the year alone.
func (r psRuntime) startTime(p processInfo) string {
	start := r.boot.Add(time.Duration(float64(p.startTicks) / clockTicks * float64(time.Second)))
	switch {
	case start.Year() == r.now.Year() && start.YearDay() == r.now.YearDay():
		return start.Format("15:04")
	case start.Year() == r.now.Year():
		return start.Format("Jan02")
	}
	return start.Format("2006")
}

// ttyDevices maps a terminal's device number to its name under /dev. It is
// built once, and only for a device the fixed table below does not cover.
var ttyDevices struct {
	once  sync.Once
	names map[uint64]string
}

// ttyName renders the tty_nr field of /proc/PID/stat. The common terminals have
// fixed device numbers; anything else (a hypervisor or USB console, say) is
// looked up in /dev.
func ttyName(device int) string {
	if device == 0 {
		return "?"
	}
	major, minor := (device>>8)&0xfff, (device&0xff)|((device>>12)&0xfff00)
	switch {
	case major == 4 && minor < 64:
		return "tty" + strconv.Itoa(minor)
	case major == 4:
		return "ttyS" + strconv.Itoa(minor-64)
	case major == 5 && minor == 1:
		return "console"
	case major >= 136 && major <= 143:
		return "pts/" + strconv.Itoa((major-136)*256+minor)
	}
	ttyDevices.once.Do(readTTYDevices)
	if name, ok := ttyDevices.names[uint64(device)]; ok { //nolint:gosec // G115: a device number is a nonnegative 32-bit value.
		return name
	}
	return "?"
}

func readTTYDevices() {
	ttyDevices.names = map[uint64]string{}
	for _, directory := range []string{"/dev", "/dev/pts"} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil || info.Mode()&os.ModeCharDevice == 0 {
				continue
			}
			if status, ok := info.Sys().(*syscall.Stat_t); ok {
				ttyDevices.names[status.Rdev] = strings.TrimPrefix(filepath.Join(directory, entry.Name()), "/dev/")
			}
		}
	}
}

func readProcesses(selected map[int]bool) ([]processInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var processes []processInfo
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || len(selected) > 0 && !selected[pid] {
			continue
		}
		process, err := readProcess(pid)
		if err == nil {
			processes = append(processes, process)
		}
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].pid < processes[j].pid })
	return processes, nil
}

func readProcess(pid int) (processInfo, error) {
	var process processInfo
	statData, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return process, err
	}
	text := string(statData)
	left, right := strings.IndexByte(text, '('), strings.LastIndex(text, ") ")
	if left < 0 || right < left {
		return process, fmt.Errorf("invalid stat data")
	}
	process.pid, process.comm = pid, text[left+1:right]
	fields := strings.Fields(text[right+2:])
	if len(fields) < 22 {
		return process, fmt.Errorf("short stat data")
	}
	process.state = fields[0]
	process.ppid, _ = strconv.Atoi(fields[1])
	process.pgrp, _ = strconv.Atoi(fields[2])
	process.session, _ = strconv.Atoi(fields[3])
	process.tty, _ = strconv.Atoi(fields[4])
	process.tpgid, _ = strconv.Atoi(fields[5])
	process.utime, _ = strconv.ParseUint(fields[11], 10, 64)
	process.stime, _ = strconv.ParseUint(fields[12], 10, 64)
	process.nice, _ = strconv.Atoi(fields[16])
	process.threads, _ = strconv.Atoi(fields[17])
	process.startTicks, _ = strconv.ParseUint(fields[19], 10, 64)
	process.vsz, _ = strconv.ParseUint(fields[20], 10, 64)
	rssPages, _ := strconv.ParseUint(fields[21], 10, 64)
	process.rss = rssPages * uint64(os.Getpagesize()) //nolint:gosec // page size and RSS page count are nonnegative.
	status, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			parts := strings.Fields(line)
			if len(parts) > 1 {
				value, _ := strconv.ParseUint(parts[1], 10, 32)
				process.uid = uint32(value) //nolint:gosec // parsed with a 32-bit limit.
			}
			break
		}
	}
	process.user = strconv.FormatUint(uint64(process.uid), 10)
	if account, lookupErr := user.LookupId(process.user); lookupErr == nil {
		process.user = account.Username
	}
	cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	process.args = strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
	if process.args == "" {
		process.args = "[" + process.comm + "]"
	}
	return process, nil
}

// writePS lays the table out the way ps does: every column is as wide as its
// heading, its reserved width, and its widest value, values are right-aligned
// for the numeric columns, and the last column is never padded.
func writePS(processes []processInfo, columns []string, runtime psRuntime) {
	specs := make([]psColumn, len(columns))
	widths := make([]int, len(columns))
	for i, column := range columns {
		name := psColumnName(column)
		specs[i] = psColumns[name]
		if name == "pid" || name == "ppid" {
			specs[i].width = runtime.pidWidth
		}
		widths[i] = maxInt(specs[i].width, len(specs[i].heading))
	}
	rows := make([][]string, 0, len(processes))
	for _, process := range processes {
		row := make([]string, len(columns))
		for i, spec := range specs {
			row[i] = spec.value(runtime, process)
			switch {
			case spec.grow:
				widths[i] = maxInt(widths[i], len(row[i]))
			case !spec.right && len(row[i]) > widths[i]:
				row[i] = row[i][:widths[i]-1] + "+"
			}
		}
		rows = append(rows, row)
	}
	writePSRow(specs, widths, psHeadings(specs))
	for _, row := range rows {
		writePSRow(specs, widths, row)
	}
}

func psHeadings(specs []psColumn) []string {
	headings := make([]string, len(specs))
	for i, spec := range specs {
		headings[i] = spec.heading
	}
	return headings
}

func writePSRow(specs []psColumn, widths []int, fields []string) {
	var line strings.Builder
	for i, field := range fields {
		if i > 0 {
			line.WriteByte(' ')
		}
		padding := widths[i] - len(field)
		switch {
		case i == len(fields)-1 && !specs[i].right:
			line.WriteString(field)
		case specs[i].right:
			line.WriteString(strings.Repeat(" ", maxInt(0, padding)) + field)
		default:
			line.WriteString(field + strings.Repeat(" ", maxInt(0, padding)))
		}
	}
	fmt.Fprintln(os.Stdout, line.String())
}

var signalNames = map[string]syscall.Signal{
	"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT,
	"ILL": syscall.SIGILL, "ABRT": syscall.SIGABRT, "FPE": syscall.SIGFPE,
	"KILL": syscall.SIGKILL, "SEGV": syscall.SIGSEGV, "PIPE": syscall.SIGPIPE,
	"ALRM": syscall.SIGALRM, "TERM": syscall.SIGTERM, "USR1": syscall.SIGUSR1,
	"USR2": syscall.SIGUSR2, "CHLD": syscall.SIGCHLD, "CONT": syscall.SIGCONT,
	"STOP": syscall.SIGSTOP, "TSTP": syscall.SIGTSTP, "TTIN": syscall.SIGTTIN,
	"TTOU": syscall.SIGTTOU,
}

func cmdKill(args []string) int {
	signal := syscall.SIGTERM
	list := false
	for len(args) > 0 {
		arg := args[0]
		switch {
		case arg == "--":
			args = args[1:]
			goto send
		case arg == "-l" || arg == "--list":
			list, args = true, args[1:]
			goto send
		case arg == "-s" || arg == "--signal":
			if len(args) < 2 {
				fatalf("kill", "-s requires a signal")
				return 1
			}
			parsed, err := parseSignal(args[1])
			if err != nil {
				fatalf("kill", "%v", err)
				return 1
			}
			signal, args = parsed, args[2:]
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			parsed, err := parseSignal(arg[1:])
			if err != nil {
				goto send
			}
			signal, args = parsed, args[1:]
		default:
			goto send
		}
	}
send:
	if list {
		if len(args) == 1 {
			parsed, err := parseSignal(args[0])
			if err != nil {
				fatalf("kill", "%v", err)
				return 1
			}
			fmt.Fprintln(os.Stdout, signalName(parsed))
			return 0
		}
		if len(args) != 0 {
			fatalf("kill", "too many arguments for -l")
			return 1
		}
		names := make([]string, 0, len(signalNames))
		for name := range signalNames {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintln(os.Stdout, strings.Join(names, " "))
		return 0
	}
	if len(args) == 0 {
		fatalf("kill", "missing process ID")
		return 1
	}
	status := 0
	for _, value := range args {
		pid, err := strconv.Atoi(value)
		if err != nil {
			fatalf("kill", "invalid process ID %q", value)
			status = 1
			continue
		}
		if err := syscall.Kill(pid, signal); err != nil {
			fatalf("kill", "%s: %v", value, err)
			status = 1
		}
	}
	return status
}

func parseSignal(value string) (syscall.Signal, error) {
	value = strings.TrimPrefix(strings.ToUpper(value), "SIG")
	if number, err := strconv.Atoi(value); err == nil && number >= 0 && number < 128 {
		return syscall.Signal(number), nil
	}
	if signal, ok := signalNames[value]; ok {
		return signal, nil
	}
	return 0, fmt.Errorf("unknown signal %q", value)
}

func signalName(signal syscall.Signal) string {
	for name, value := range signalNames {
		if value == signal {
			return name
		}
	}
	return strconv.Itoa(int(signal))
}

func cmdFree(args []string) int {
	unit, human := uint64(1024), false
	for _, arg := range args {
		switch arg {
		case "-h", "--human":
			human = true
		case "-b":
			unit = 1
		case "-k":
			unit = 1024
		case "-m":
			unit = 1024 * 1024
		case "-g":
			unit = 1024 * 1024 * 1024
		default:
			fatalf("free", "invalid option %q", arg)
			return 1
		}
	}
	values, err := readMeminfo()
	if err != nil {
		fatalf("free", "%v", err)
		return 1
	}
	total, free := values["MemTotal"], values["MemFree"]
	cache := values["Buffers"] + values["Cached"] + values["SReclaimable"]
	available := values["MemAvailable"]
	// "used" is what people read off free, so it has to be the same subtraction
	// the original makes: everything the kernel says is not available, not just
	// what is neither free nor reclaimable cache.
	used := uint64(0)
	switch {
	case available > 0 && total > available:
		used = total - available
	case total > free+cache:
		used = total - free - cache
	}
	swapTotal, swapFree := values["SwapTotal"], values["SwapFree"]
	// Each column is twelve wide and the first ends at column twenty, which is
	// what lines the header up with the numbers underneath it.
	printMemory := func(label string, row []uint64) {
		fmt.Fprintf(os.Stdout, "%-8s", label)
		for _, value := range row {
			if human {
				fmt.Fprintf(os.Stdout, "%12s", scaleMemory(value))
			} else {
				fmt.Fprintf(os.Stdout, "%12d", value/unit)
			}
		}
		fmt.Fprintln(os.Stdout)
	}
	fmt.Fprintf(os.Stdout, "%-8s%12s%12s%12s%12s%12s%12s\n", "",
		"total", "used", "free", "shared", "buff/cache", "available")
	printMemory("Mem:", []uint64{total, used, free, values["Shmem"], cache, available})
	printMemory("Swap:", []uint64{swapTotal, swapTotal - swapFree, swapFree})
	return 0
}

// scaleMemory formats a byte count for free -h. It deliberately does not use
// humanSize: free labels its units IEC-style (Ki, Mi, Gi) and truncates whole
// numbers where the coreutils tools round up, so 11.7 GiB reads as 11Gi.
func scaleMemory(value uint64) string {
	suffixes := []string{"B", "Ki", "Mi", "Gi", "Ti", "Pi", "Ei"}
	divisor := uint64(1)
	index := 0
	for index < len(suffixes)-1 && value/divisor >= 1024 {
		divisor *= 1024
		index++
	}
	if whole := value / divisor; whole < 10 && index > 0 {
		return fmt.Sprintf("%.1f%s", float64(value)/float64(divisor), suffixes[index])
	}
	return fmt.Sprintf("%d%s", value/divisor, suffixes[index])
}

func readMeminfo() (map[string]uint64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := make(map[string]uint64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			values[strings.TrimSuffix(fields[0], ":")] = value * 1024
		}
	}
	return values, scanner.Err()
}
