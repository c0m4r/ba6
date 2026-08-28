// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type watchOptions struct {
	interval time.Duration
	noTitle  bool
	command  []string
}

func cmdWatch(args []string) int {
	opts, err := parseWatchOptions(args)
	if err != nil {
		fatalf("watch", "%v", err)
		return 1
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	return runWatch(opts, signals)
}

func parseWatchOptions(args []string) (watchOptions, error) {
	opts := watchOptions{interval: 2 * time.Second}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			opts.command = append(opts.command, args[i+1:]...)
			break
		}
		switch {
		case arg == "-t" || arg == "--no-title":
			opts.noTitle = true
		case arg == "-n" || arg == "--interval":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("option %s requires an argument", arg)
			}
			interval, err := parseWatchInterval(args[i])
			if err != nil {
				return opts, err
			}
			opts.interval = interval
		case strings.HasPrefix(arg, "--interval="):
			interval, err := parseWatchInterval(strings.TrimPrefix(arg, "--interval="))
			if err != nil {
				return opts, err
			}
			opts.interval = interval
		case len(arg) > 2 && strings.HasPrefix(arg, "-n"):
			interval, err := parseWatchInterval(arg[2:])
			if err != nil {
				return opts, err
			}
			opts.interval = interval
		case strings.HasPrefix(arg, "-"):
			return opts, fmt.Errorf("unsupported option %q", arg)
		default:
			opts.command = append(opts.command, args[i:]...)
			i = len(args)
		}
	}
	if len(opts.command) == 0 {
		return opts, fmt.Errorf("missing command")
	}
	return opts, nil
}

func parseWatchInterval(value string) (time.Duration, error) {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0.1 {
		return 0, fmt.Errorf("interval must be at least 0.1 seconds")
	}
	interval := time.Duration(seconds * float64(time.Second))
	if interval <= 0 {
		return 0, fmt.Errorf("invalid interval %q", value)
	}
	return interval, nil
}

func runWatch(opts watchOptions, signals <-chan os.Signal) int {
	for {
		if isTerminal(os.Stdout.Fd()) {
			fmt.Fprint(os.Stdout, "\x1b[H\x1b[2J")
		}
		if !opts.noTitle {
			hostname, err := os.Hostname()
			if err != nil {
				hostname = "?"
			}
			fmt.Fprint(os.Stdout, watchTitle(opts, hostname, time.Now())+"\n\n")
		}
		command := exec.Command(opts.command[0], opts.command[1:]...) //nolint:gosec // watch deliberately executes the requested command repeatedly.
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "watch: %s\n", errText(err))
		}
		timer := time.NewTimer(opts.interval)
		select {
		case <-signals:
			if !timer.Stop() {
				<-timer.C
			}
			return 0
		case <-timer.C:
		}
	}
}

// watchTitle lays the header out the way the original does: the interval and
// command on the left, and "host: date" pushed against the right edge of the
// terminal, with the left side clipped rather than wrapped when the two would
// collide. The date is in the ctime(3) form the original prints.
func watchTitle(opts watchOptions, hostname string, now time.Time) string {
	left := fmt.Sprintf("Every %s: %s", formatWatchInterval(opts.interval), strings.Join(opts.command, " "))
	right := hostname + ": " + now.Format(time.ANSIC)
	width, err := unixWinsize(os.Stdout.Fd())
	if err != nil || width <= 0 {
		width = 80
	}
	if room := width - len(right) - 1; room < len(left) {
		if room < 0 {
			room = 0
		}
		left = left[:room]
	}
	padding := width - len(left) - len(right)
	if padding < 1 {
		padding = 1
	}
	return left + strings.Repeat(" ", padding) + right
}

func formatWatchInterval(interval time.Duration) string {
	seconds := float64(interval) / float64(time.Second)
	return strconv.FormatFloat(seconds, 'f', -1, 64) + "s"
}
