// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	defaultInittab      = "/etc/inittab"
	defaultHostnameFile = "/etc/hostname"
	kernelHostnameFile  = "/proc/sys/kernel/hostname"
	maxHostnameLength   = 64
)

type initAction string

const (
	initSysinit     initAction = "sysinit"
	initWait        initAction = "wait"
	initOnce        initAction = "once"
	initRespawn     initAction = "respawn"
	initAskFirst    initAction = "askfirst"
	initShutdown    initAction = "shutdown"
	initCtrlAltDel  initAction = "ctrlaltdel"
	initPowerFail   initAction = "powerfail"
	initPowerWait   initAction = "powerwait"
	initPowerOKWait initAction = "powerokwait"
)

var validInitActions = map[initAction]bool{
	initSysinit: true, initWait: true, initOnce: true,
	initRespawn: true, initAskFirst: true, initShutdown: true,
	initCtrlAltDel: true, initPowerFail: true, initPowerWait: true,
	initPowerOKWait: true,
}

type inittabEntry struct {
	id, runlevels string
	action        initAction
	command       string
	line          int
}

type initService struct {
	entry     inittabEntry
	pid       int
	started   time.Time
	restartAt time.Time
	failures  int
	disabled  bool
}

func cmdInit(args []string) int {
	if os.Getpid() == 1 {
		inittab, command, err := parseSystemInitArgs(args)
		if err != nil {
			fatalf("init", "%v", err)
			pauseInitForever()
		}
		if len(command) != 0 {
			status := runCommandSupervisor(command)
			logInit("supervised command exited with status %d; powering off", status)
			shutdownSystem(nil, syscall.LINUX_REBOOT_CMD_POWER_OFF)
		}
		runSystemInit(inittab)
		pauseInitForever()
	}

	// Outside PID 1, retain the useful container/subreaper-style command
	// supervisor. System inittab mode is intentionally reserved for PID 1.
	if len(args) > 0 && (args[0] == "-f" || args[0] == "--inittab") {
		fatalf("init", "inittab mode requires PID 1")
		return 1
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		args = []string{"/bin/sh"}
	}
	return runCommandSupervisor(args)
}

func parseSystemInitArgs(args []string) (string, []string, error) {
	path := defaultInittab
	if len(args) == 0 {
		return path, nil, nil
	}
	if args[0] == "-f" || args[0] == "--inittab" {
		if len(args) != 2 {
			return "", nil, fmt.Errorf("%s requires exactly one path", args[0])
		}
		return args[1], nil, nil
	}
	if args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return "", nil, fmt.Errorf("missing command after --")
	}
	return "", args, nil
}

func parseInittab(reader io.Reader) ([]inittabEntry, error) {
	scanner := newLineScanner(reader)
	entries := make([]inittabEntry, 0)
	problems := make([]error, 0)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 4)
		if len(parts) != 4 {
			problems = append(problems, fmt.Errorf("line %d: expected id:runlevels:action:process", lineNumber))
			continue
		}
		action := initAction(strings.TrimSpace(parts[2]))
		command := strings.TrimSpace(parts[3])
		if !validInitActions[action] {
			problems = append(problems, fmt.Errorf("line %d: unsupported action %q", lineNumber, action))
			continue
		}
		if command == "" {
			problems = append(problems, fmt.Errorf("line %d: empty process", lineNumber))
			continue
		}
		entries = append(entries, inittabEntry{
			id: strings.TrimSpace(parts[0]), runlevels: strings.TrimSpace(parts[1]),
			action: action, command: command, line: lineNumber,
		})
	}
	if err := scanner.Err(); err != nil {
		problems = append(problems, err)
	}
	return entries, errors.Join(problems...)
}

func readInittab(path string) ([]inittabEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseInittab(file)
}

func runSystemInit(path string) {
	establishInitEnvironment()
	entries, err := readInittab(path)
	if err != nil {
		logInit("%s: %v", path, err)
	}
	if len(entries) == 0 {
		logInit("no usable inittab entries; installing emergency console shell")
		entries = []inittabEntry{{action: initRespawn, command: "/bin/sh"}}
	}

	signals := initSignalChannel()
	defer signal.Stop(signals)
	runInitBootActions(entries, defaultHostnameFile, kernelHostnameFile)
	for _, entry := range entriesForAction(entries, initOnce) {
		if _, err := startInitEntry(entry); err != nil {
			logInit("line %d: %v", entry.line, err)
		}
	}

	services := make(map[string]*initService)
	servicesByPID := make(map[int]*initService)
	reconcileInitServices(entries, services, servicesByPID)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case received := <-signals:
			sig, ok := received.(syscall.Signal)
			if !ok {
				continue
			}
			switch sig {
			case syscall.SIGCHLD:
			case syscall.SIGHUP:
				reloaded, reloadErr := readInittab(path)
				if reloadErr != nil {
					logInit("reload %s: %v", path, reloadErr)
				}
				if len(reloaded) != 0 {
					entries = reloaded
					reconcileInitServices(entries, services, servicesByPID)
				}
			case syscall.SIGINT:
				runInitActions(entries, initCtrlAltDel)
				shutdownSystem(entries, syscall.LINUX_REBOOT_CMD_RESTART)
			case syscall.SIGPWR:
				if initPowerRestored() {
					runInitActions(entries, initPowerOKWait)
				} else {
					runInitActions(entries, initPowerFail)
					runInitActions(entries, initPowerWait)
					shutdownSystem(entries, syscall.LINUX_REBOOT_CMD_POWER_OFF)
				}
			case syscall.SIGUSR1:
				shutdownSystem(entries, syscall.LINUX_REBOOT_CMD_HALT)
			case syscall.SIGUSR2:
				shutdownSystem(entries, syscall.LINUX_REBOOT_CMD_POWER_OFF)
			case syscall.SIGTERM:
				shutdownSystem(entries, syscall.LINUX_REBOOT_CMD_RESTART)
			}
		case <-ticker.C:
		}
		reapSystemChildren(servicesByPID)
		startDueInitServices(services, servicesByPID)
	}
}

func establishInitEnvironment() {
	_ = os.Chdir("/")
	syscall.Umask(0o022)
	defaults := map[string]string{
		"PATH": "/sbin:/bin:/usr/sbin:/usr/bin", "HOME": "/root",
		"USER": "root", "LOGNAME": "root", "SHELL": "/bin/sh",
		"TERM": "linux",
	}
	for name, value := range defaults {
		if name == "PATH" || os.Getenv(name) == "" {
			_ = os.Setenv(name, value)
		}
	}
	console, err := os.OpenFile("/dev/console", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return
	}
	defer console.Close()
	for fd := 0; fd <= 2; fd++ {
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); errors.Is(err, syscall.EBADF) {
			_ = syscall.Dup2(int(console.Fd()), fd)
		}
	}
}

func initSignalChannel() chan os.Signal {
	signals := make(chan os.Signal, 32)
	signal.Notify(signals,
		syscall.SIGCHLD, syscall.SIGHUP, syscall.SIGINT, syscall.SIGPWR,
		syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2,
	)
	return signals
}

func entriesForAction(entries []inittabEntry, action initAction) []inittabEntry {
	selected := make([]inittabEntry, 0)
	for _, entry := range entries {
		if entry.action == action {
			selected = append(selected, entry)
		}
	}
	return selected
}

func runInitActions(entries []inittabEntry, action initAction) {
	for _, entry := range entriesForAction(entries, action) {
		runInitEntryAndWait(entry)
	}
}

func runInitBootActions(entries []inittabEntry, hostnamePath, kernelPath string) {
	runInitActions(entries, initSysinit)
	if err := setInitHostname(hostnamePath, kernelPath); err != nil {
		logInit("hostname: %v", err)
	}
	runInitActions(entries, initWait)
}

func setInitHostname(hostnamePath, kernelPath string) error {
	data, err := os.ReadFile(hostnamePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", hostnamePath, err)
	}
	hostname := strings.TrimSpace(string(data))
	if hostname == "" {
		return fmt.Errorf("%s is empty", hostnamePath)
	}
	if len(hostname) > maxHostnameLength {
		return fmt.Errorf("%s exceeds the %d-byte kernel limit", hostnamePath, maxHostnameLength)
	}
	for _, character := range []byte(hostname) {
		if character <= ' ' || character == 0x7f || character == '/' {
			return fmt.Errorf("%s contains an invalid hostname character", hostnamePath)
		}
	}

	file, err := os.OpenFile(kernelPath, os.O_WRONLY|os.O_TRUNC|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", kernelPath, err)
	}
	if _, err := io.WriteString(file, hostname+"\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", kernelPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", kernelPath, err)
	}
	return nil
}

func runInitEntryAndWait(entry inittabEntry) {
	command, err := startInitEntry(entry)
	if err != nil {
		logInit("line %d: %v", entry.line, err)
		return
	}
	if err := command.Wait(); err != nil {
		logInit("line %d (%s): %v", entry.line, entry.action, err)
	}
}

func startInitEntry(entry inittabEntry) (*exec.Cmd, error) {
	commandText := entry.command
	if strings.HasPrefix(commandText, "-/") {
		// BusyBox-style inittabs commonly prefix a console shell with '-' to
		// request login-shell semantics. ba6 sh gets its login environment from
		// init, so execute the same absolute path without the marker.
		commandText = strings.TrimPrefix(commandText, "-")
	}
	if entry.action == initAskFirst {
		commandText = "echo 'Please press Enter to activate this console.'; read _; " + commandText
	}
	command := exec.Command("/bin/sh", "-c", commandText) //nolint:gosec // G204: inittab explicitly defines commands executed by init.
	command.Env = os.Environ()
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var terminal *os.File
	if entry.id != "" {
		path := entry.id
		if !strings.HasPrefix(path, "/") {
			path = filepath.Join("/dev", path)
		}
		opened, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
		if err != nil {
			return nil, fmt.Errorf("open terminal %s: %w", path, err)
		}
		terminal = opened
		command.Stdin, command.Stdout, command.Stderr = terminal, terminal, terminal
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	}
	if err := command.Start(); err != nil {
		if terminal != nil {
			_ = terminal.Close()
		}
		return nil, err
	}
	if terminal != nil {
		_ = terminal.Close()
	}
	return command, nil
}

func serviceKey(entry inittabEntry) string {
	return entry.id + "\x00" + string(entry.action) + "\x00" + entry.command
}

func reconcileInitServices(entries []inittabEntry, services map[string]*initService, byPID map[int]*initService) {
	desired := make(map[string]inittabEntry)
	for _, entry := range entries {
		if entry.action == initRespawn || entry.action == initAskFirst {
			desired[serviceKey(entry)] = entry
		}
	}
	for key, service := range services {
		entry, keep := desired[key]
		if keep {
			service.entry = entry
			delete(desired, key)
			continue
		}
		service.disabled = true
		if service.pid > 0 {
			_ = syscall.Kill(-service.pid, syscall.SIGTERM)
		}
		delete(services, key)
	}
	for key, entry := range desired {
		services[key] = &initService{entry: entry}
	}
	startDueInitServices(services, byPID)
}

func startDueInitServices(services map[string]*initService, byPID map[int]*initService) {
	now := time.Now()
	for _, service := range services {
		if service.disabled || service.pid != 0 || now.Before(service.restartAt) {
			continue
		}
		if service.entry.action == initAskFirst {
			logInit("press Enter on %s to activate %s", service.entry.id, service.entry.command)
		}
		command, err := startInitEntry(service.entry)
		if err != nil {
			service.failures++
			service.restartAt = now.Add(respawnDelay(service.failures))
			logInit("line %d: %v; retrying in %s", service.entry.line, err, time.Until(service.restartAt).Round(time.Second))
			continue
		}
		service.pid, service.started = command.Process.Pid, now
		byPID[service.pid] = service
	}
}

func reapSystemChildren(byPID map[int]*initService) {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
		service := byPID[pid]
		if service == nil {
			continue
		}
		delete(byPID, pid)
		runtime := time.Since(service.started)
		service.pid = 0
		if service.disabled {
			continue
		}
		if runtime >= 5*time.Second {
			service.failures = 0
			service.restartAt = time.Now()
		} else {
			service.failures++
			service.restartAt = time.Now().Add(respawnDelay(service.failures))
			logInit("%s exited after %s; respawning in %s", service.entry.command,
				runtime.Round(time.Millisecond), time.Until(service.restartAt).Round(time.Second))
		}
	}
}

func respawnDelay(failures int) time.Duration {
	if failures < 1 {
		return 0
	}
	shift := failures - 1
	if shift > 5 {
		shift = 5
	}
	return time.Second * time.Duration(1<<shift)
}

func runCommandSupervisor(args []string) int {
	signals := make(chan os.Signal, 32)
	signal.Notify(signals,
		syscall.SIGCHLD, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT,
		syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGWINCH,
	)
	defer signal.Stop(signals)

	command := exec.Command(args[0], args[1:]...) //nolint:gosec // G204: command supervision is init's explicit command mode.
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		fatalf("init", "%v", err)
		return 127
	}
	mainPID := command.Process.Pid
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case received := <-signals:
			sig, ok := received.(syscall.Signal)
			if ok && sig != syscall.SIGCHLD {
				forwardInitSignal(mainPID, sig)
			}
		case <-ticker.C:
		}
		if exited, status := reapInitChildren(mainPID); exited {
			terminateInitGroup(mainPID)
			return waitStatusExitCode(status)
		}
	}
}

func forwardInitSignal(mainPID int, signal syscall.Signal) {
	if err := syscall.Kill(-mainPID, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		fatalf("init", "forward signal %s: %v", signalName(signal), err)
	}
}

func reapInitChildren(mainPID int) (bool, syscall.WaitStatus) {
	mainExited := false
	var mainStatus syscall.WaitStatus
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			break
		}
		if pid == mainPID {
			mainExited, mainStatus = true, status
		}
	}
	return mainExited, mainStatus
}

func terminateInitGroup(mainPID int) {
	if err := syscall.Kill(-mainPID, 0); errors.Is(err, syscall.ESRCH) {
		return
	}
	_ = syscall.Kill(-mainPID, syscall.SIGTERM)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = reapInitChildren(-1)
		if err := syscall.Kill(-mainPID, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-mainPID, syscall.SIGKILL)
}

func shutdownSystem(entries []inittabEntry, action int) {
	logInit("system shutdown requested")
	runInitActions(entries, initShutdown)
	syscall.Sync()
	_ = syscall.Kill(-1, syscall.SIGTERM)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = reapInitChildren(-1)
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(-1, syscall.SIGKILL)
	for attempts := 0; attempts < 20; attempts++ {
		_, _ = reapInitChildren(-1)
		time.Sleep(10 * time.Millisecond)
	}
	unmountForShutdown()
	syscall.Sync()
	if err := syscall.Reboot(action); err != nil {
		logInit("reboot syscall failed: %v; PID 1 will remain alive", err)
	}
	pauseInitForever()
}

func initPowerRestored() bool {
	data, err := os.ReadFile("/etc/powerstatus")
	if err != nil {
		return false
	}
	return powerStatusRestored(string(data))
}

func powerStatusRestored(value string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value)), "O")
}

func unmountForShutdown() {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return
	}
	mounts := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] != "/" {
			mounts = append(mounts, decodeMountField(fields[1]))
		}
	}
	sort.SliceStable(mounts, func(i, j int) bool { return len(mounts[i]) > len(mounts[j]) })
	for _, mount := range mounts {
		if err := syscall.Unmount(mount, 0); err != nil {
			_ = syscall.Unmount(mount, syscall.MNT_DETACH)
		}
	}
	_ = syscall.Mount("", "/", "", syscall.MS_REMOUNT|syscall.MS_RDONLY, "")
}

func decodeMountField(value string) string {
	replacer := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	return replacer.Replace(value)
}

func waitStatusExitCode(status syscall.WaitStatus) int {
	if status.Exited() {
		return status.ExitStatus()
	}
	if status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}

func logInit(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "init: "+format+"\n", args...)
}

func pauseInitForever() {
	for {
		_ = syscall.Pause()
	}
}
