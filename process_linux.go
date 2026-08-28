// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// execResolved looks PATH up for command and replaces the current process
// with it, mapping a failed lookup onto the 127/126 convention of the
// original tools.
func execResolved(prog, command string, args []string) int {
	path, err := exec.LookPath(command)
	if err != nil {
		fatalf(prog, "'%s': %s", command, errText(err))
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
			return 127
		}
		return 126
	}
	if err := syscall.Exec(path, args, os.Environ()); err != nil { //nolint:gosec // G204: these applets intentionally execute the selected command.
		fatalf(prog, "failed to execute '%s': %s", command, errText(err))
		return 126
	}
	return 0
}

// cmdSetsid implements setsid(1): run COMMAND in a new session. Without -f the
// session is created in place; with -f a child is forked into its own session
// and, with -w, waited for. -c makes the current terminal the controlling one.
func cmdSetsid(args []string) int {
	ctty := false
	forkChild := false
	wait := false
	for len(args) > 0 {
		switch {
		case args[0] == "-c" || args[0] == "--ctty":
			ctty = true
			args = args[1:]
		case args[0] == "-f" || args[0] == "--fork":
			forkChild = true
			args = args[1:]
		case args[0] == "-w" || args[0] == "--wait":
			wait = true
			args = args[1:]
		case args[0] == "--":
			args = args[1:]
			goto parsed
		case strings.HasPrefix(args[0], "-"):
			fatalf("setsid", "invalid option %q", args[0])
			return 1
		default:
			goto parsed
		}
	}
parsed:
	if len(args) == 0 {
		fatalf("setsid", "no command specified")
		return 1
	}
	if forkChild {
		cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // G204: setsid intentionally executes the selected command.
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if ctty {
			cmd.SysProcAttr.Setctty = true
			cmd.SysProcAttr.Ctty = 0
		}
		if err := cmd.Start(); err != nil {
			fatalf("setsid", "%v", err)
			if errors.Is(err, exec.ErrNotFound) {
				return 127
			}
			return 126
		}
		if wait {
			if err := cmd.Wait(); err != nil {
				return commandStatus("setsid", err)
			}
			return 0
		}
		return 0
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_SETSID, 0, 0, 0); errno != 0 {
		fatalf("setsid", "failed to setsid: %s", errText(errno))
		return 1
	}
	if ctty {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, 0, syscall.TIOCSCTTY, 0)
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		fatalf("setsid", "failed to execute %s: %s", args[0], errText(err))
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
			return 127
		}
		return 126
	}
	if err := syscall.Exec(path, args, os.Environ()); err != nil { //nolint:gosec // G204: setsid intentionally executes the selected command.
		fatalf("setsid", "failed to execute %s: %s", args[0], errText(err))
		return 126
	}
	return 0
}

// cmdNohup implements nohup(1): run COMMAND immune to hangups. Standard input
// that is a terminal is replaced by /dev/null, and terminal output is
// appended to nohup.out (or $HOME/nohup.out) with a note on standard error.
func cmdNohup(args []string) int {
	for len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fatalf("nohup", "missing operand")
		return 125
	}
	signal.Ignore(syscall.SIGHUP)
	stdinTTY := isTTY(0)
	if stdinTTY {
		null, err := os.Open("/dev/null")
		if err != nil {
			fatalf("nohup", "failed to redirect standard input: %s", errText(err))
			return 125
		}
		_ = syscall.Dup2(int(null.Fd()), 0)
		null.Close()
	}
	outputTTY := isTTY(1)
	if outputTTY {
		target := "nohup.out"
		fd, err := syscall.Open("nohup.out", syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT, 0o600)
		if err != nil {
			if home := os.Getenv("HOME"); home != "" {
				path := filepath.Join(home, "nohup.out")
				if fd2, err2 := syscall.Open(path, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT, 0o600); err2 == nil {
					fd, target, err = fd2, path, nil
				}
			}
			if err != nil {
				fatalf("nohup", "failed to run command '%s': %s", args[0], errText(err))
				return 125
			}
		}
		_ = syscall.Dup2(fd, 1)
		syscall.Close(fd)
		if stdinTTY {
			fmt.Fprintf(os.Stderr, "nohup: ignoring input and appending output to '%s'\n", target)
		} else {
			fmt.Fprintf(os.Stderr, "nohup: appending output to '%s'\n", target)
		}
	} else if stdinTTY {
		fmt.Fprintln(os.Stderr, "nohup: ignoring input")
	}
	if isTTY(2) {
		_ = syscall.Dup2(1, 2)
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		fatalf("nohup", "failed to run command '%s': %s", args[0], errText(err))
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
			return 127
		}
		return 126
	}
	if err := syscall.Exec(path, args, os.Environ()); err != nil { //nolint:gosec // G204: nohup intentionally executes the selected command.
		fatalf("nohup", "failed to run command '%s': %s", args[0], errText(err))
		return 126
	}
	return 0
}

// isTTY reports whether the descriptor is a terminal, using the TCGETS probe
// the other interactive applets share.
func isTTY(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&termios))) //nolint:gosec // G103: fixed Termios buffer for TCGETS.
	return errno == 0
}

// cmdNice implements nice(1): run COMMAND at an adjusted niceness. The
// adjustment is applied to this process, which exec replaces, so it covers
// the command's entire run like the original's pre-exec setpriority.
func cmdNice(args []string) int {
	adjustment := 10
	for len(args) > 0 {
		switch {
		case args[0] == "-n" || args[0] == "--adjustment":
			if len(args) < 2 {
				fatalf("nice", "option requires an argument -- 'n'")
				return 125
			}
			value, err := strconv.Atoi(args[1])
			if err != nil {
				fatalf("nice", "invalid adjustment %q", args[1])
				return 125
			}
			adjustment = value
			args = args[2:]
		case strings.HasPrefix(args[0], "--adjustment="):
			value, err := strconv.Atoi(strings.TrimPrefix(args[0], "--adjustment="))
			if err != nil {
				fatalf("nice", "invalid adjustment %q", strings.TrimPrefix(args[0], "--adjustment="))
				return 125
			}
			adjustment = value
			args = args[1:]
		case len(args[0]) > 1 && args[0][0] == '-' && args[0][1] >= '0' && args[0][1] <= '9':
			value, err := strconv.Atoi(args[0][1:])
			if err != nil {
				fatalf("nice", "invalid adjustment %q", args[0][1:])
				return 125
			}
			adjustment = value
			args = args[1:]
		case args[0] == "--":
			args = args[1:]
			goto parsed
		case strings.HasPrefix(args[0], "-"):
			fatalf("nice", "invalid option %q", args[0])
			return 125
		default:
			goto parsed
		}
	}
parsed:
	if len(args) == 0 {
		return 0
	}
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, 0, adjustment); err != nil {
		fatalf("nice", "cannot set niceness: %s", errText(err))
		return 125
	}
	return execResolved("nice", args[0], args)
}

// cmdRenice implements renice(1): change the niceness of running processes,
// process groups, or every process of a user, reporting each old and new
// priority on standard output.
func cmdRenice(args []string) int {
	priority := int64(0)
	type target struct {
		kind  int // PRIO_PROCESS or PRIO_PGRP
		who   int64
		label string
	}
	var targets []target
	var users []string

	// gather collects the operands following a -p/-g/-u selector.
	i := 0
	nextValue := func() (string, bool) {
		i++
		if i >= len(args) {
			return "", false
		}
		return args[i], true
	}
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-n" || a == "--priority":
			value, ok := nextValue()
			if !ok {
				fatalf("renice", "option requires an argument -- 'n'")
				return 1
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				fatalf("renice", "invalid priority %q", value)
				return 1
			}
			priority = parsed
		case a == "-p" || a == "--pid":
			value, ok := nextValue()
			if !ok {
				fatalf("renice", "option requires an argument -- 'p'")
				return 1
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				fatalf("renice", "invalid process ID: %q", value)
				return 1
			}
			targets = append(targets, target{kind: syscall.PRIO_PROCESS, who: parsed, label: "process ID"})
		case a == "-g" || a == "--pgrp":
			value, ok := nextValue()
			if !ok {
				fatalf("renice", "option requires an argument -- 'g'")
				return 1
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				fatalf("renice", "invalid process group ID: %q", value)
				return 1
			}
			targets = append(targets, target{kind: syscall.PRIO_PGRP, who: parsed, label: "process group ID"})
		case a == "-u" || a == "--user":
			value, ok := nextValue()
			if !ok {
				fatalf("renice", "option requires an argument -- 'u'")
				return 1
			}
			users = append(users, value)
		case len(a) > 1 && a[0] == '-':
			fatalf("renice", "invalid option %q", a)
			return 1
		default:
			i--
			if i < 0 {
				i = 0
			}
			goto rest
		}
	}
rest:
	// The legacy form: an optional bare priority, then an optional selector
	// and targets.
	remaining := args[i:]
	if len(remaining) > 0 {
		if value, err := strconv.ParseInt(remaining[0], 10, 64); err == nil {
			priority = value
			remaining = remaining[1:]
		}
		if len(remaining) > 0 && (remaining[0] == "-p" || remaining[0] == "-g" || remaining[0] == "-u") {
			remaining = remaining[1:]
		}
		for _, value := range remaining {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				fatalf("renice", "invalid process ID: %q", value)
				return 1
			}
			targets = append(targets, target{kind: syscall.PRIO_PROCESS, who: parsed, label: "process ID"})
		}
	}
	if len(targets) == 0 && len(users) == 0 {
		fatalf("renice", "no process specified")
		return 1
	}
	status := 0
	for _, t := range targets {
		if !reniceOne(t.who, t.kind, priority, t.label) {
			status = 1
		}
	}
	for _, user := range users {
		account, err := lookupUser(user)
		if err != nil {
			fatalf("renice", "unknown user %q", user)
			status = 1
			continue
		}
		uid, err := strconv.ParseInt(account.Uid, 10, 64)
		if err != nil {
			fatalf("renice", "unknown user %q", user)
			status = 1
			continue
		}
		for _, pid := range userProcesses(uid) {
			if !reniceOne(pid, syscall.PRIO_PROCESS, priority, "process ID") {
				status = 1
			}
		}
	}
	return status
}

// reniceOne changes one target's priority and reports the old and new values.
func reniceOne(who int64, which int, priority int64, label string) bool {
	old, err := syscall.Getpriority(which, int(who)) //nolint:gosec // G115: process IDs fit in an int on Linux.
	if err != nil {
		fatalf("renice", "failed to get priority for %d (%s): %s", who, label, errText(err))
		return false
	}
	if err := syscall.Setpriority(which, int(who), int(priority)); err != nil { //nolint:gosec // G115: see above.
		fatalf("renice", "failed to set priority for %d (%s): %s", who, label, errText(err))
		return false
	}
	// The kernel clamps out-of-range requests, so report the resulting value.
	// Linux getpriority returns 20 - nice, unlike setpriority's nice scale.
	result, err := syscall.Getpriority(which, int(who)) //nolint:gosec // G115: see above.
	if err != nil {
		fatalf("renice", "failed to get priority for %d (%s): %s", who, label, errText(err))
		return false
	}
	fmt.Fprintf(os.Stdout, "%d (%s) old priority %d, new priority %d\n", who, label, 20-old, 20-result)
	return true
}

// userProcesses lists the PIDs whose real UID matches, from /proc.
func userProcesses(uid int64) []int64 {
	var pids []int64
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		pid, err := strconv.ParseInt(entry.Name(), 10, 64)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(data))
		if len(fields) < 4 {
			continue
		}
		processUID, err := strconv.ParseInt(fields[3], 10, 64)
		if err == nil && processUID == uid {
			pids = append(pids, pid)
		}
	}
	return pids
}
