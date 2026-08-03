//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// cmdInit is a deliberately small PID-1/container supervisor. The main child
// receives its own process group so signals and shutdown cleanup reach the
// complete foreground workload rather than only its first process.
func cmdInit(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		args = []string{"/bin/sh"}
	}

	signals := make(chan os.Signal, 32)
	signal.Notify(signals,
		syscall.SIGCHLD, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT,
		syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGWINCH,
	)
	defer signal.Stop(signals)

	command := exec.Command(args[0], args[1:]...) //nolint:gosec // G204: init exists to supervise the user-selected command.
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		fatalf("init", "%v", err)
		return 127
	}
	mainPID := command.Process.Pid

	// SIGCHLD normally drives reaping. The ticker closes the small race between
	// a child exit and signal registration and also copes with coalesced signals.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case received := <-signals:
			sig, ok := received.(syscall.Signal)
			if !ok {
				continue
			}
			if sig != syscall.SIGCHLD {
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

// reapInitChildren drains all immediately waitable children. PID 1 becomes the
// parent of orphaned descendants, so this also prevents zombie accumulation.
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
	for attempts := 0; attempts < 25; attempts++ {
		_, _ = reapInitChildren(-1)
		if err := syscall.Kill(-mainPID, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
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
