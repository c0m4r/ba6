// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type ddOptions struct {
	input, output        string
	inputBlock, outBlock int64
	count, skip, seek    int64
	countSet             bool
	noTrunc, sync, noErr bool
	status               string
}

func cmdDd(args []string) int {
	opts, err := parseDDOptions(args)
	if err != nil {
		fatalf("dd", "%v", err)
		return 1
	}
	in, closeIn, err := ddInput(opts.input)
	if err != nil {
		fatalf("dd", "%v", err)
		return 1
	}
	defer closeIn()
	out, closeOut, err := ddOutput(opts)
	if err != nil {
		fatalf("dd", "%v", err)
		return 1
	}
	defer closeOut()

	if opts.skip > 0 {
		if err := skipInput(in, opts.skip*opts.inputBlock); err != nil {
			fatalf("dd", "skip: %v", err)
			return 1
		}
	}
	writer := bufio.NewWriterSize(out, int(opts.outBlock))
	buffer := make([]byte, int(opts.inputBlock))
	var fullIn, partialIn, fullOut, partialOut, total int64
	status := 0
	for block := int64(0); !opts.countSet || block < opts.count; block++ {
		n, readErr := io.ReadFull(in, buffer)
		if n == 0 && (errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)) {
			break
		}
		if n == len(buffer) {
			fullIn++
		} else if n > 0 {
			partialIn++
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			fatalf("dd", "read error: %v", readErr)
			status = 1
			if !opts.noErr {
				break
			}
			if n == 0 {
				break
			}
		}
		writeLength := n
		if opts.sync && n < len(buffer) {
			clear(buffer[n:])
			writeLength = len(buffer)
		}
		if writeLength > 0 {
			written, writeErr := writer.Write(buffer[:writeLength])
			total += int64(written)
			if writeErr != nil {
				fatalf("dd", "write error: %v", writeErr)
				status = 1
				break
			}
		}
		if readErr != nil && (errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)) {
			break
		}
	}
	if err := writer.Flush(); err != nil {
		fatalf("dd", "write error: %v", err)
		status = 1
	}
	fullOut, partialOut = total/opts.outBlock, 0
	if total%opts.outBlock != 0 {
		partialOut = 1
	}
	if opts.status != "none" {
		fmt.Fprintf(os.Stderr, "%d+%d records in\n%d+%d records out\n",
			fullIn, partialIn, fullOut, partialOut)
		if opts.status != "noxfer" {
			fmt.Fprintf(os.Stderr, "%d bytes copied\n", total)
		}
	}
	return status
}

func parseDDOptions(args []string) (ddOptions, error) {
	opts := ddOptions{input: "-", output: "-", inputBlock: 512, outBlock: 512}
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return opts, fmt.Errorf("unrecognized operand %q", arg)
		}
		switch key {
		case "if":
			opts.input = value
		case "of":
			opts.output = value
		case "bs", "ibs", "obs":
			n, err := parseDDNumber(value)
			if err != nil || n < 1 || n > 64<<20 {
				return opts, fmt.Errorf("invalid %s %q", key, value)
			}
			if key == "bs" || key == "ibs" {
				opts.inputBlock = n
			}
			if key == "bs" || key == "obs" {
				opts.outBlock = n
			}
		case "count", "skip", "seek":
			n, err := parseDDNumber(value)
			if err != nil || n < 0 {
				return opts, fmt.Errorf("invalid %s %q", key, value)
			}
			switch key {
			case "count":
				opts.count, opts.countSet = n, true
			case "skip":
				opts.skip = n
			case "seek":
				opts.seek = n
			}
		case "conv":
			for _, conversion := range strings.Split(value, ",") {
				switch conversion {
				case "notrunc":
					opts.noTrunc = true
				case "sync":
					opts.sync = true
				case "noerror":
					opts.noErr = true
				default:
					return opts, fmt.Errorf("unsupported conversion %q", conversion)
				}
			}
		case "status":
			if value != "none" && value != "noxfer" {
				return opts, fmt.Errorf("unsupported status %q", value)
			}
			opts.status = value
		default:
			return opts, fmt.Errorf("unrecognized operand %q", arg)
		}
	}
	if opts.skip > math.MaxInt64/opts.inputBlock || opts.seek > math.MaxInt64/opts.outBlock {
		return opts, fmt.Errorf("skip or seek offset is out of range")
	}
	return opts, nil
}

func parseDDNumber(value string) (int64, error) {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == 'x' || r == '*' })
	if len(parts) == 0 {
		return 0, fmt.Errorf("invalid number")
	}
	result := int64(1)
	for _, part := range parts {
		multiplier := int64(1)
		for _, suffix := range []struct {
			name  string
			value int64
		}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"GB", 1_000_000_000}, {"MB", 1_000_000}, {"kB", 1_000}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"k", 1 << 10}, {"b", 512}, {"w", 2}, {"c", 1}} {
			if strings.HasSuffix(part, suffix.name) {
				part = strings.TrimSuffix(part, suffix.name)
				multiplier = suffix.value
				break
			}
		}
		n, err := strconv.ParseInt(part, 0, 64)
		if err != nil || n < 0 || n > math.MaxInt64/multiplier {
			return 0, fmt.Errorf("invalid number")
		}
		factor := n * multiplier
		if factor != 0 && result > math.MaxInt64/factor {
			return 0, fmt.Errorf("invalid number")
		}
		result *= factor
	}
	return result, nil
}

func ddInput(name string) (io.Reader, func(), error) {
	if name == "-" {
		return os.Stdin, func() {}, nil
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, func() {}, fmt.Errorf("%s: %w", name, err)
	}
	return file, func() { _ = file.Close() }, nil
}

func ddOutput(opts ddOptions) (io.Writer, func(), error) {
	if opts.output == "-" {
		if opts.seek != 0 {
			return nil, func() {}, fmt.Errorf("cannot seek standard output")
		}
		return os.Stdout, func() {}, nil
	}
	flags := os.O_CREATE | os.O_WRONLY
	if !opts.noTrunc && opts.seek == 0 {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(opts.output, flags, 0o666) //nolint:gosec // dd follows the standard umask-controlled output mode.
	if err != nil {
		return nil, func() {}, fmt.Errorf("%s: %w", opts.output, err)
	}
	if opts.seek != 0 {
		if _, err := file.Seek(opts.seek*opts.outBlock, io.SeekStart); err != nil {
			_ = file.Close()
			return nil, func() {}, err
		}
	}
	return file, func() { _ = file.Close() }, nil
}

func skipInput(reader io.Reader, bytesToSkip int64) error {
	if seeker, ok := reader.(io.Seeker); ok {
		_, err := seeker.Seek(bytesToSkip, io.SeekCurrent)
		return err
	}
	_, err := io.CopyN(io.Discard, reader, bytesToSkip)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func cmdFile(args []string) int {
	brief := false
	if len(args) > 0 && (args[0] == "-b" || args[0] == "--brief") {
		brief, args = true, args[1:]
	}
	if len(args) == 0 {
		fatalf("file", "missing file operand")
		return 1
	}
	status := 0
	for _, name := range args {
		description, err := describeFile(name)
		if err != nil {
			description, status = "cannot open: "+err.Error(), 1
		}
		if brief {
			fmt.Println(description)
		} else {
			fmt.Printf("%s: %s\n", name, description)
		}
	}
	return status
}

func describeFile(name string) (string, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return "", err
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(name)
		if err != nil {
			return "symbolic link", nil
		}
		return "symbolic link to " + target, nil
	case mode.IsDir():
		return "directory", nil
	case mode&os.ModeNamedPipe != 0:
		return "fifo (named pipe)", nil
	case mode&os.ModeSocket != 0:
		return "socket", nil
	case mode&os.ModeDevice != 0 && mode&os.ModeCharDevice != 0:
		return "character special", nil
	case mode&os.ModeDevice != 0:
		return "block special", nil
	case !mode.IsRegular():
		return "special file", nil
	}
	if kind, label, _, probeErr := probeFilesystem(name); probeErr == nil && kind != "" {
		description := kind + " filesystem data"
		if label != "" {
			description += ", label " + strconv.Quote(label)
		}
		return description, nil
	}
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data := make([]byte, 8192)
	n, err := file.Read(data)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return describeData(data[:n], mode), nil
}

func describeData(data []byte, mode os.FileMode) string {
	switch {
	case len(data) == 0:
		return "empty"
	case bytes.HasPrefix(data, []byte{0x7f, 'E', 'L', 'F'}):
		bits := "32-bit"
		if len(data) > 4 && data[4] == 2 {
			bits = "64-bit"
		}
		return "ELF " + bits + " executable or object"
	case bytes.HasPrefix(data, []byte("#!")):
		line := strings.TrimSpace(strings.SplitN(string(data[2:]), "\n", 2)[0])
		return "script, interpreter " + line
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		return "gzip compressed data"
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return "Zip archive data"
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "PNG image data"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "JPEG image data"
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return "PDF document"
	case len(data) > 262 && string(data[257:262]) == "ustar":
		return "POSIX tar archive"
	}
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		if mode&0o111 != 0 {
			return "Unicode text, executable"
		}
		return "Unicode text"
	}
	return "data"
}

func cmdMktemp(args []string) int {
	directory, baseDirectory := false, ""
	var template string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--directory":
			directory = true
		case "-p", "--tmpdir":
			i++
			if i >= len(args) {
				fatalf("mktemp", "-p requires a directory")
				return 1
			}
			baseDirectory = args[i]
		case "-t":
			if baseDirectory == "" {
				baseDirectory = os.TempDir()
			}
		case "--":
			if i+1 < len(args) {
				template = args[i+1]
				i++
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				fatalf("mktemp", "unsupported option %q", args[i])
				return 1
			}
			if template != "" {
				fatalf("mktemp", "extra operand %q", args[i])
				return 1
			}
			template = args[i]
		}
	}
	if template == "" {
		template = "tmp.XXXXXXXXXX"
		if baseDirectory == "" {
			baseDirectory = os.TempDir()
		}
	}
	dir, pattern, err := mktempPattern(baseDirectory, template)
	if err != nil {
		fatalf("mktemp", "%v", err)
		return 1
	}
	var name string
	if directory {
		name, err = os.MkdirTemp(dir, pattern)
	} else {
		var file *os.File
		file, err = os.CreateTemp(dir, pattern)
		if err == nil {
			name = file.Name()
			err = file.Close()
		}
	}
	if err != nil {
		fatalf("mktemp", "%v", err)
		return 1
	}
	fmt.Println(name)
	return 0
}

func mktempPattern(baseDirectory, template string) (string, string, error) {
	dir, name := filepath.Dir(template), filepath.Base(template)
	if dir == "." && baseDirectory != "" {
		dir = baseDirectory
	}
	end := strings.LastIndex(name, "X") + 1
	start := end
	for start > 0 && name[start-1] == 'X' {
		start--
	}
	if end-start < 3 {
		return "", "", fmt.Errorf("template must end with at least 3 consecutive Xs")
	}
	return dir, name[:start] + "*" + name[end:], nil
}

func cmdTimeout(args []string) int {
	signal, killAfter := syscall.SIGTERM, time.Duration(0)
	for len(args) > 0 {
		switch {
		case args[0] == "-s" || args[0] == "--signal":
			if len(args) < 2 {
				fatalf("timeout", "%s requires an argument", args[0])
				return 125
			}
			parsed, err := parseSignal(args[1])
			if err != nil {
				fatalf("timeout", "%v", err)
				return 125
			}
			signal, args = parsed, args[2:]
		case args[0] == "-k" || args[0] == "--kill-after":
			if len(args) < 2 {
				return 125
			}
			parsed, err := commandDuration(args[1])
			if err != nil {
				fatalf("timeout", "invalid duration %q", args[1])
				return 125
			}
			killAfter, args = parsed, args[2:]
		case args[0] == "--":
			args = args[1:]
			goto parsed
		case strings.HasPrefix(args[0], "-"):
			fatalf("timeout", "unsupported option %q", args[0])
			return 125
		default:
			goto parsed
		}
	}
parsed:
	if len(args) < 2 {
		fatalf("timeout", "missing duration or command")
		return 125
	}
	duration, err := commandDuration(args[0])
	if err != nil {
		fatalf("timeout", "invalid duration %q", args[0])
		return 125
	}
	command := exec.Command(args[1], args[2:]...) //nolint:gosec // G204: timeout intentionally runs the selected command.
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		fatalf("timeout", "%v", err)
		if errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	if duration == 0 {
		if err := <-done; err != nil {
			return commandStatus("timeout", err)
		}
		return 0
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return commandStatus("timeout", err)
		}
		return 0
	case <-timer.C:
		_ = syscall.Kill(-command.Process.Pid, signal)
	}
	if killAfter > 0 {
		killTimer := time.NewTimer(killAfter)
		select {
		case <-done:
			killTimer.Stop()
		case <-killTimer.C:
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			<-done
		}
	} else {
		<-done
	}
	return 124
}

func commandDuration(value string) (time.Duration, error) {
	seconds, err := parseSleepDuration(value)
	if err != nil || seconds > float64(math.MaxInt64)/float64(time.Second) {
		return 0, fmt.Errorf("invalid duration")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func cmdTop(args []string) int {
	iterations, delay := 1, 3*time.Second
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-b":
		case "-n":
			i++
			if i >= len(args) {
				return 1
			}
			iterations, _ = strconv.Atoi(args[i])
		case "-d":
			i++
			if i >= len(args) {
				return 1
			}
			parsed, err := commandDuration(args[i])
			if err != nil {
				return 1
			}
			delay = parsed
		default:
			fatalf("top", "unsupported option %q", args[i])
			return 1
		}
	}
	if iterations < 1 {
		fatalf("top", "iteration count must be positive")
		return 1
	}
	for iteration := 0; iteration < iterations; iteration++ {
		if iteration > 0 {
			time.Sleep(delay)
			fmt.Println()
		}
		if err := writeTopSnapshot(); err != nil {
			fatalf("top", "%v", err)
			return 1
		}
	}
	return 0
}

type topProcess struct {
	processInfo
	cpu float64
}

func writeTopSnapshot() error {
	uptimeData, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return err
	}
	uptimeFields := strings.Fields(string(uptimeData))
	if len(uptimeFields) == 0 {
		return fmt.Errorf("invalid /proc/uptime")
	}
	uptime, err := strconv.ParseFloat(uptimeFields[0], 64)
	if err != nil {
		return err
	}
	load, _ := os.ReadFile("/proc/loadavg")
	loads := strings.Fields(string(load))
	if len(loads) < 3 {
		loads = []string{"?", "?", "?"}
	}
	processes, err := readProcesses(nil)
	if err != nil {
		return err
	}
	topProcesses := make([]topProcess, 0, len(processes))
	for _, process := range processes {
		topProcesses = append(topProcesses, topProcess{processInfo: process, cpu: averageProcessCPU(process.pid, uptime)})
	}
	sort.Slice(topProcesses, func(i, j int) bool {
		if topProcesses[i].cpu == topProcesses[j].cpu {
			return topProcesses[i].rss > topProcesses[j].rss
		}
		return topProcesses[i].cpu > topProcesses[j].cpu
	})
	memory, _ := readMemorySummary()
	fmt.Printf("top - %s up %s, load average: %s, %s, %s\n", time.Now().Format("15:04:05"),
		formatUptime(time.Duration(uptime*float64(time.Second))), loads[0], loads[1], loads[2])
	fmt.Printf("Tasks: %d total\n", len(processes))
	if memory != "" {
		fmt.Println(memory)
	}
	fmt.Println("  PID USER       %CPU    VSZ    RSS STAT COMMAND")
	limit := len(topProcesses)
	if limit > 25 {
		limit = 25
	}
	for _, process := range topProcesses[:limit] {
		fmt.Printf("%5d %-10.10s %5.1f %6d %6d %-4s %s\n", process.pid, process.user,
			process.cpu, (process.vsz+1023)/1024, (process.rss+1023)/1024, process.state, process.comm)
	}
	return nil
}

func averageProcessCPU(pid int, uptime float64) float64 {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	text := string(data)
	right := strings.LastIndex(text, ") ")
	if right < 0 {
		return 0
	}
	fields := strings.Fields(text[right+2:])
	if len(fields) < 20 {
		return 0
	}
	userTicks, _ := strconv.ParseFloat(fields[11], 64)
	systemTicks, _ := strconv.ParseFloat(fields[12], 64)
	startTicks, _ := strconv.ParseFloat(fields[19], 64)
	lifetime := uptime - startTicks/100
	if lifetime <= 0 {
		return 0
	}
	return (userTicks + systemTicks) / 100 / lifetime * 100
}

func readMemorySummary() (string, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "", err
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			values[strings.TrimSuffix(fields[0], ":")], _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	used := uint64(0)
	if values["MemTotal"] >= values["MemAvailable"] {
		used = values["MemTotal"] - values["MemAvailable"]
	}
	return fmt.Sprintf("MiB Mem: %d total, %d used, %d available", values["MemTotal"]/1024,
		used/1024, values["MemAvailable"]/1024), nil
}
