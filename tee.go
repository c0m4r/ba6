package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
)

func cmdTee(args []string) int {
	appendMode := false
	ignoreInterrupts := false
	var files []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-a" || arg == "--append"):
			appendMode = true
		case parsing && (arg == "-i" || arg == "--ignore-interrupts"):
			ignoreInterrupts = true
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
	}
	outputs := []output{{name: "standard output", file: os.Stdout, open: true}}
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
		outputs = append(outputs, output{name: name, file: file, open: true})
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
					fatalf("tee", "%s: %v", outputs[i].name, err)
					outputs[i].open = false
					status = 1
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
