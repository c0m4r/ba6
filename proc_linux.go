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
	"syscall"
)

type processInfo struct {
	pid, ppid int
	uid       uint32
	user      string
	state     string
	vsz, rss  uint64
	comm      string
	args      string
}

func cmdPs(args []string) int {
	full := false
	selected := map[int]bool{}
	var columns []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-p" || arg == "--pid":
			i++
			if i >= len(args) || parsePIDList(args[i], selected) != nil {
				fatalf("ps", "invalid PID list")
				return 1
			}
		case strings.HasPrefix(arg, "--pid="):
			if parsePIDList(strings.TrimPrefix(arg, "--pid="), selected) != nil {
				fatalf("ps", "invalid PID list")
				return 1
			}
		case arg == "-o" || arg == "--format":
			i++
			if i >= len(args) {
				fatalf("ps", "-o requires an argument")
				return 1
			}
			columns = splitPSColumns(args[i])
		case strings.HasPrefix(arg, "--format="):
			columns = splitPSColumns(strings.TrimPrefix(arg, "--format="))
		case len(arg) > 1 && arg[0] == '-':
			for _, flag := range arg[1:] {
				switch flag {
				case 'e', 'A':
				case 'f':
					full = true
				default:
					fatalf("ps", "invalid option -- '%c'", flag)
					return 1
				}
			}
		default:
			fatalf("ps", "unsupported operand %q", arg)
			return 1
		}
	}
	if len(columns) == 0 {
		if full {
			columns = []string{"user", "pid", "ppid", "vsz", "rss", "stat", "args"}
		} else {
			columns = []string{"pid", "stat", "args"}
		}
	}
	for _, column := range columns {
		if !validPSColumn(column) {
			fatalf("ps", "unknown output column %q", column)
			return 1
		}
	}
	processes, err := readProcesses(selected)
	if err != nil {
		fatalf("ps", "%v", err)
		return 1
	}
	writePS(processes, columns)
	return 0
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

func validPSColumn(value string) bool {
	switch value {
	case "pid", "ppid", "uid", "user", "stat", "vsz", "rss", "comm", "args":
		return true
	}
	return false
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

func writePS(processes []processInfo, columns []string) {
	headings := map[string]string{"pid": "PID", "ppid": "PPID", "uid": "UID", "user": "USER", "stat": "STAT", "vsz": "VSZ", "rss": "RSS", "comm": "COMMAND", "args": "COMMAND"}
	for i, column := range columns {
		if i > 0 {
			fmt.Fprint(os.Stdout, " ")
		}
		fmt.Fprint(os.Stdout, headings[column])
	}
	fmt.Fprintln(os.Stdout)
	for _, process := range processes {
		for i, column := range columns {
			if i > 0 {
				fmt.Fprint(os.Stdout, " ")
			}
			switch column {
			case "pid":
				fmt.Fprint(os.Stdout, process.pid)
			case "ppid":
				fmt.Fprint(os.Stdout, process.ppid)
			case "uid":
				fmt.Fprint(os.Stdout, process.uid)
			case "user":
				fmt.Fprint(os.Stdout, process.user)
			case "stat":
				fmt.Fprint(os.Stdout, process.state)
			case "vsz":
				fmt.Fprint(os.Stdout, (process.vsz+1023)/1024)
			case "rss":
				fmt.Fprint(os.Stdout, (process.rss+1023)/1024)
			case "comm":
				fmt.Fprint(os.Stdout, process.comm)
			case "args":
				fmt.Fprint(os.Stdout, process.args)
			}
		}
		fmt.Fprintln(os.Stdout)
	}
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
	used := uint64(0)
	if total > free+cache {
		used = total - free - cache
	}
	available := values["MemAvailable"]
	swapTotal, swapFree := values["SwapTotal"], values["SwapFree"]
	printMemory := func(label string, row []uint64) {
		fmt.Fprintf(os.Stdout, "%-7s", label)
		for _, value := range row {
			if human {
				fmt.Fprintf(os.Stdout, " %11s", humanSizeUint64(value))
			} else {
				fmt.Fprintf(os.Stdout, " %11d", value/unit)
			}
		}
		fmt.Fprintln(os.Stdout)
	}
	fmt.Fprintln(os.Stdout, "              total        used        free      shared  buff/cache   available")
	printMemory("Mem:", []uint64{total, used, free, values["Shmem"], cache, available})
	printMemory("Swap:", []uint64{swapTotal, swapTotal - swapFree, swapFree})
	return 0
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
