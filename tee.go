// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
)

// teeOutputError selects how cmdTee reacts to a write error: diagnose and
// continue, or stop at once; either way optionally only for non-pipe outputs.
type teeOutputError int

const (
	teeWarn teeOutputError = iota
	teeWarnNoPipe
	teeExit
	teeExitNoPipe
)

func cmdTee(args []string) int {
	args = expandShortOptions(args, "")
	appendMode := false
	ignoreInterrupts := false
	outputError := teeWarn
	var files []string
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-a" || arg == "--append"):
			appendMode = true
		case parsing && (arg == "-i" || arg == "--ignore-interrupts"):
			ignoreInterrupts = true
		case parsing && arg == "-p":
			outputError = teeWarn
		case parsing && arg == "--output-error":
			outputError = teeWarn
		case parsing && strings.HasPrefix(arg, "--output-error="):
			mode := strings.TrimPrefix(arg, "--output-error=")
			switch mode {
			case "warn":
				outputError = teeWarn
			case "warn-nopipe":
				outputError = teeWarnNoPipe
			case "exit":
				outputError = teeExit
			case "exit-nopipe":
				outputError = teeExitNoPipe
			default:
				fatalf("tee", "invalid mode %q", mode)
				return 1
			}
		case parsing && len(arg) > 1 && arg[0] == '-':
			fatalf("tee", "invalid option %q", arg)
			return 1
		default:
			files = append(files, arg)
		}
	}
	if ignoreInterrupts {
		signal.Ignore(os.Interrupt)
		defer signal.Reset(os.Interrupt)
	}

	type output struct {
		name string
		file *os.File
		open bool
		pipe bool
	}
	outputs := []output{{name: "standard output", file: os.Stdout, open: true}}
	if info, err := os.Stdout.Stat(); err == nil {
		outputs[0].pipe = info.Mode()&os.ModeNamedPipe != 0
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	status := 0
	for _, name := range files {
		file, err := os.OpenFile(name, flags, 0o666) //nolint:gosec // tee follows the process umask like the standard utility.
		if err != nil {
			fatalf("tee", "%s: %v", name, err)
			status = 1
			continue
		}
		out := output{name: name, file: file, open: true}
		if info, statErr := file.Stat(); statErr == nil {
			out.pipe = info.Mode()&os.ModeNamedPipe != 0
		}
		outputs = append(outputs, out)
	}

	buf := make([]byte, 32*1024)
	for {
		n, readErr := os.Stdin.Read(buf)
		if n > 0 {
			for i := range outputs {
				if !outputs[i].open {
					continue
				}
				if err := writeTeeChunk(outputs[i].file, buf[:n]); err != nil {
					if !teeWriteErrorFatal(outputError, outputs[i].pipe) {
						continue
					}
					fatalf("tee", "%s: %v", outputs[i].name, err)
					status = 1
					if outputError == teeExit || outputError == teeExitNoPipe {
						return 1
					}
					outputs[i].open = false
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				fatalf("tee", "read error: %v", readErr)
				status = 1
			}
			break
		}
	}
	for i := 1; i < len(outputs); i++ {
		if err := outputs[i].file.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "tee: %s: %v\n", outputs[i].name, err)
			status = 1
		}
	}
	return status
}

// teeWriteErrorFatal reports whether a write error to a pipe/non-pipe output
// must be diagnosed and stop the loop for the given --output-error mode.
// Errors that are not fatal are skipped silently, as GNU's warn-nopipe and
// exit-nopipe modes skip pipe errors.
func teeWriteErrorFatal(mode teeOutputError, pipe bool) bool {
	switch mode {
	case teeWarn:
		return true
	case teeWarnNoPipe:
		return !pipe
	case teeExit:
		return true
	case teeExitNoPipe:
		return !pipe
	}
	return true
}

func writeTeeChunk(output *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := output.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
