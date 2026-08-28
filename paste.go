// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// cmdPaste implements paste(1): write lines of the given files side by side,
// separated by the characters of a cycled delimiter list (-d, default TAB).
// -s serializes each file onto one output line and -z selects the NUL record
// terminator. A file that ends contributes empty cells to the remaining rows.
func cmdPaste(args []string) int {
	delimiters := "\t"
	serial := false
	zero := false
	var files []string
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-d" || a == "--delimiters":
			i++
			if i >= len(args) {
				fatalf("paste", "option requires an argument -- 'd'")
				return 1
			}
			delimiters = args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
		case strings.HasPrefix(a, "--delimiters="):
			delimiters = strings.TrimPrefix(a, "--delimiters=")
		case len(a) > 2 && a[0] == '-' && a[1] == 'd':
			delimiters = a[2:]
		case a == "-s" || a == "--serial":
			serial = true
		case a == "-z" || a == "--zero-terminated":
			zero = true
		case len(a) > 1 && a[0] == '-':
			fatalf("paste", "invalid option %q", a)
			return 1
		default:
			files = append(files, a)
		}
	}
rest:
	files = append(files, args[i:]...)
	if len(files) == 0 {
		files = []string{"-"}
	}
	delimList, ok := parsePasteDelimiters(delimiters)
	if !ok {
		return 1
	}
	if len(delimList) == 0 && len(files) > 1 {
		fatalf("paste", "no delimiters specified")
		return 1
	}

	readers := make([]*bufio.Reader, len(files))
	closers := make([]io.Closer, len(files))
	status := 0
	for idx, name := range files {
		input, err := openInput(name)
		if err != nil {
			fatalf("paste", "%s: %s", name, errText(err))
			return 1
		}
		readers[idx] = bufio.NewReader(input)
		closers[idx] = input
	}
	defer func() {
		for _, c := range closers {
			if c != nil {
				c.Close()
			}
		}
	}()

	terminator := byte('\n')
	if zero {
		terminator = 0
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	if serial {
		for idx, r := range readers {
			var lines [][]byte
			for {
				line, err := readPasteRecord(r, zero)
				if err != nil {
					break
				}
				lines = append(lines, line)
			}
			if idx > 0 {
				_ = out.WriteByte(terminator)
			}
			for j, line := range lines {
				if j > 0 {
					_ = out.WriteByte(delimList[(j-1)%len(delimList)])
				}
				_, _ = out.Write(line)
			}
		}
		_ = out.WriteByte(terminator)
		if err := out.Flush(); err != nil {
			fatalf("paste", "write error: %v", err)
			return 1
		}
		return status
	}

	lineBuffer := make([][]byte, len(readers))
	for {
		any := false
		for idx, r := range readers {
			if lineBuffer[idx] != nil {
				any = true
				continue
			}
			line, err := readPasteRecord(r, zero)
			if err != nil {
				continue
			}
			lineBuffer[idx] = line
			any = true
		}
		if !any {
			break
		}
		first := true
		for idx, line := range lineBuffer {
			if !first {
				_ = out.WriteByte(delimList[(idx-1)%len(delimList)])
			}
			first = false
			if line != nil {
				_, _ = out.Write(line)
				lineBuffer[idx] = nil
			}
		}
		_ = out.WriteByte(terminator)
	}
	if err := out.Flush(); err != nil {
		fatalf("paste", "write error: %v", err)
		return 1
	}
	return status
}

// readPasteRecord reads one record terminated by '\n' (or NUL with zero) and
// returns it without the terminator. A final unterminated record is returned
// as-is, so a last line without its terminator still contributes a cell.
func readPasteRecord(r *bufio.Reader, zero bool) ([]byte, error) {
	sep := byte('\n')
	if zero {
		sep = 0
	}
	data, err := r.ReadBytes(sep)
	if err != nil {
		if err == io.EOF && len(data) > 0 {
			return data, nil
		}
		return nil, err
	}
	return data[:len(data)-1], nil
}

// parsePasteDelimiters decodes the -d list, honoring the backslash escapes
// GNU paste understands: \n \t \\ \0 and \NNN octal. An unescaped trailing
// backslash is reported as an error.
func parsePasteDelimiters(spec string) ([]byte, bool) {
	var out []byte
	for i := 0; i < len(spec); i++ {
		ch := spec[i]
		if ch != '\\' {
			out = append(out, ch)
			continue
		}
		i++
		if i >= len(spec) {
			fatalf("paste", "delimiter list ends with an unescaped backslash: %q", spec)
			return nil, false
		}
		ch = spec[i]
		switch ch {
		case 'n':
			out = append(out, '\n')
		case 't':
			out = append(out, '\t')
		case '\\':
			out = append(out, '\\')
		case '0':
			out = append(out, 0)
		default:
			if ch >= '0' && ch <= '7' {
				value := int(ch - '0')
				for i+1 < len(spec) && spec[i+1] >= '0' && spec[i+1] <= '7' && value*8 < 256 {
					i++
					value = value*8 + int(spec[i]-'0')
				}
				if value > 255 {
					fatalf("paste", "invalid octal escape in delimiter list: %q", spec)
					return nil, false
				}
				out = append(out, byte(value))
				continue
			}
			fatalf("paste", "unknown escape sequence in delimiter list: %q", spec)
			return nil, false
		}
	}
	return out, true
}
