// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"errors"
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
	interval    time.Duration
	noTitle     bool
	differences bool
	cumulative  bool
	chgexit     bool
	errexit     bool
	beep        bool
	execMode    bool
	noWrap      bool
	color       bool
	precise     bool
	equexit     int
	command     []string
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

//nolint:gocyclo // Command-line parser with option bundling.
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
		case arg == "-b" || arg == "--beep":
			opts.beep = true
		case arg == "-d" || arg == "--differences":
			opts.differences = true
		case strings.HasPrefix(arg, "--differences="):
			opts.differences = true
			if strings.TrimPrefix(arg, "--differences=") == "cumulative" {
				opts.cumulative = true
			}
		case arg == "-g" || arg == "--chgexit":
			opts.chgexit = true
		case arg == "-e" || arg == "--errexit":
			opts.errexit = true
		case arg == "-x" || arg == "--exec":
			opts.execMode = true
		case arg == "-w" || arg == "--no-wrap":
			opts.noWrap = true
		case arg == "-c" || arg == "--color":
			opts.color = true
		case arg == "-C" || arg == "--no-color":
			opts.color = false
		case arg == "-p" || arg == "--precise":
			opts.precise = true
		case arg == "-q" || arg == "--equexit":
			opts.equexit = 1
		case strings.HasPrefix(arg, "--equexit="):
			val, err := strconv.Atoi(strings.TrimPrefix(arg, "--equexit="))
			if err != nil || val <= 0 {
				return opts, fmt.Errorf("invalid equexit value")
			}
			opts.equexit = val
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
		case len(arg) > 1 && arg[0] == '-' && !strings.HasPrefix(arg, "--"):
			// Bundled short options (e.g. -te, -dg, -dn1)
			j := 1
			for j < len(arg) {
				switch arg[j] {
				case 't':
					opts.noTitle = true
				case 'b':
					opts.beep = true
				case 'd':
					opts.differences = true
					if j+1 < len(arg) && arg[j+1] == '=' {
						if arg[j+2:] == "cumulative" {
							opts.cumulative = true
						}
						j = len(arg)
						continue
					}
				case 'g':
					opts.chgexit = true
				case 'e':
					opts.errexit = true
				case 'x':
					opts.execMode = true
				case 'w':
					opts.noWrap = true
				case 'c':
					opts.color = true
				case 'C':
					opts.color = false
				case 'p':
					opts.precise = true
				case 'q':
					val := arg[j+1:]
					if val != "" {
						cnt, err := strconv.Atoi(val)
						if err != nil || cnt <= 0 {
							return opts, fmt.Errorf("invalid equexit value")
						}
						opts.equexit = cnt
					} else {
						opts.equexit = 1
					}
					j = len(arg)
					continue
				case 'n':
					val := arg[j+1:]
					if val == "" {
						i++
						if i >= len(args) {
							return opts, fmt.Errorf("option -n requires an argument")
						}
						val = args[i]
					}
					interval, err := parseWatchInterval(val)
					if err != nil {
						return opts, err
					}
					opts.interval = interval
					j = len(arg)
					continue
				default:
					return opts, fmt.Errorf("unsupported option %q", arg)
				}
				j++
			}
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

// highlightDifferences applies reverse-video ANSI sequences (\x1b[7m...\x1b[27m)
// to characters that changed relative to previous (or cumulative) iterations.
func highlightDifferences(current, previous []byte, cumulative bool, cumulativeDiff *[]bool) string {
	var sb strings.Builder
	inDiff := false

	if cumulative {
		if len(*cumulativeDiff) < len(current) {
			newDiff := make([]bool, len(current))
			copy(newDiff, *cumulativeDiff)
			*cumulativeDiff = newDiff
		}
	}

	for i := 0; i < len(current); i++ {
		diff := false
		if cumulative {
			if i >= len(previous) || current[i] != previous[i] {
				(*cumulativeDiff)[i] = true
			}
			diff = (*cumulativeDiff)[i]
		} else if i >= len(previous) || current[i] != previous[i] {
			diff = true
		}

		if diff && !inDiff {
			sb.WriteString("\x1b[7m")
			inDiff = true
		} else if !diff && inDiff {
			sb.WriteString("\x1b[27m")
			inDiff = false
		}
		sb.WriteByte(current[i])
	}
	if inDiff {
		sb.WriteString("\x1b[27m")
	}
	return sb.String()
}

//nolint:gocyclo // Main event loop for watch with exit condition checks.
func runWatch(opts watchOptions, signals <-chan os.Signal) int {
	if opts.noWrap && isTerminal(os.Stdout.Fd()) {
		fmt.Fprint(os.Stdout, "\x1b[?7l")
		defer fmt.Fprint(os.Stdout, "\x1b[?7h")
	}

	var previous []byte
	var cumulativeDiff []bool
	first := true
	equalCount := 0
	start := time.Now()
	iterations := 0

	for {
		iterations++
		var cmd *exec.Cmd
		if opts.execMode || len(opts.command) > 1 {
			cmd = exec.Command(opts.command[0], opts.command[1:]...) //nolint:gosec // watch deliberately executes the requested command repeatedly.
		} else {
			cmd = exec.Command("sh", "-c", opts.command[0]) //nolint:gosec // watch runs shell commands when requested as a single string.
		}
		var outputBuf bytes.Buffer
		cmd.Stdout = &outputBuf
		cmd.Stderr = &outputBuf
		cmdErr := cmd.Run()
		current := outputBuf.Bytes()

		hasChanged := false
		if first {
			first = false
			previous = append([]byte(nil), current...)
			if opts.cumulative {
				cumulativeDiff = make([]bool, len(current))
			}
		} else if !bytes.Equal(current, previous) {
			hasChanged = true
		}

		if hasChanged {
			equalCount = 0
			if opts.beep {
				fmt.Fprint(os.Stderr, "\a")
			}
		} else {
			equalCount++
		}

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

		if opts.differences {
			rendered := highlightDifferences(current, previous, opts.cumulative, &cumulativeDiff)
			os.Stdout.WriteString(rendered)
		} else {
			os.Stdout.Write(current)
		}
		if len(current) > 0 && current[len(current)-1] != '\n' {
			fmt.Println()
		}

		if cmdErr != nil {
			if opts.beep && !hasChanged {
				fmt.Fprint(os.Stderr, "\a")
			}
			if opts.errexit {
				var exitErr *exec.ExitError
				if errors.As(cmdErr, &exitErr) {
					return exitErr.ExitCode()
				}
				return 1
			}
		}

		if hasChanged && opts.chgexit {
			return 0
		}

		if opts.equexit > 0 && equalCount >= opts.equexit {
			return 0
		}

		if !opts.cumulative {
			previous = append([]byte(nil), current...)
		}

		var sleepDuration time.Duration
		if opts.precise {
			target := start.Add(time.Duration(iterations) * opts.interval)
			sleepDuration = time.Until(target)
			if sleepDuration < 0 {
				sleepDuration = 0
			}
		} else {
			sleepDuration = opts.interval
		}

		timer := time.NewTimer(sleepDuration)
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
