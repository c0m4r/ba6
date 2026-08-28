// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// nlOptions holds nl(1)'s numbering configuration. Only a single logical
// section is supported (there is no -d/-h/-f header/footer distinction);
// every file is numbered as one continuous body, which is what nl's default
// invocation on ordinary text does.
type nlOptions struct {
	style     byte // 'a' all, 't' non-empty only (default), 'n' none, 'p' pattern
	pattern   *regexp.Regexp
	format    string // "ln", "rn" (default), "rz"
	separator string
	start     int64
	increment int64
	joinBlank int64
	width     int
}

// cmdNl implements nl(1): number the lines of one or more files (or standard
// input), written as one continuous sequence. -b selects which lines are
// numbered, -n how the number is formatted, -s/-w/-v/-i its separator, field
// width and starting value/step, and -l groups runs of blank lines so only
// every Nth one counts.
func cmdNl(args []string) int {
	args = expandShortOptions(args, "bnsvilw")
	opts := nlOptions{style: 't', format: "rn", separator: "\t", start: 1, increment: 1, joinBlank: 1, width: 6}
	var files []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		var value string
		switch {
		case a == "--":
			i++
			goto rest
		case strings.HasPrefix(a, "--body-numbering="):
			value = strings.TrimPrefix(a, "--body-numbering=")
			a = "-b"
		case strings.HasPrefix(a, "--number-format="):
			value = strings.TrimPrefix(a, "--number-format=")
			a = "-n"
		case strings.HasPrefix(a, "--number-separator="):
			value = strings.TrimPrefix(a, "--number-separator=")
			a = "-s"
		case strings.HasPrefix(a, "--starting-line-number="):
			value = strings.TrimPrefix(a, "--starting-line-number=")
			a = "-v"
		case strings.HasPrefix(a, "--line-increment="):
			value = strings.TrimPrefix(a, "--line-increment=")
			a = "-i"
		case strings.HasPrefix(a, "--join-blank-lines="):
			value = strings.TrimPrefix(a, "--join-blank-lines=")
			a = "-l"
		case strings.HasPrefix(a, "--number-width="):
			value = strings.TrimPrefix(a, "--number-width=")
			a = "-w"
		case a == "-b" || a == "--body-numbering" ||
			a == "-n" || a == "--number-format" ||
			a == "-s" || a == "--number-separator" ||
			a == "-v" || a == "--starting-line-number" ||
			a == "-i" || a == "--line-increment" ||
			a == "-l" || a == "--join-blank-lines" ||
			a == "-w" || a == "--number-width":
			i++
			if i >= len(args) {
				fatalf("nl", "option requires an argument -- '%c'", a[1])
				return 1
			}
			value = args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
			a = a[:2]
		case a == "-d" || a == "--section-delimiter" || a == "-h" || a == "--header-numbering" ||
			a == "-f" || a == "--footer-numbering" || a == "-p" || a == "--no-renumber":
			fatalf("nl", "unsupported option %q (only a single body section is supported)", a)
			return 1
		case len(a) > 1 && a[0] == '-':
			fatalf("nl", "invalid option %q", a)
			return 1
		default:
			files = append(files, a)
			continue
		}
		switch a {
		case "-b":
			if value == "a" || value == "t" || value == "n" {
				opts.style = value[0]
			} else if strings.HasPrefix(value, "p") {
				re, err := compilePOSIXRegexp(value[1:], posixBRE, false)
				if err != nil {
					fatalf("nl", "invalid regular expression %q: %v", value[1:], err)
					return 1
				}
				opts.style, opts.pattern = 'p', re
			} else {
				fatalf("nl", "invalid body numbering style %q", value)
				return 1
			}
		case "-n":
			if value != "ln" && value != "rn" && value != "rz" {
				fatalf("nl", "invalid line number format %q", value)
				return 1
			}
			opts.format = value
		case "-s":
			opts.separator = value
		case "-v":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				fatalf("nl", "invalid line number: %q", value)
				return 1
			}
			opts.start = n
		case "-i":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				fatalf("nl", "invalid line number increment: %q", value)
				return 1
			}
			opts.increment = n
		case "-l":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 1 {
				fatalf("nl", "invalid line number of blank lines: %q", value)
				return 1
			}
			opts.joinBlank = n
		case "-w":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				fatalf("nl", "invalid line number field width: %q", value)
				return 1
			}
			opts.width = n
		}
	}
rest:
	files = append(files, args[i:]...)
	if len(files) == 0 {
		files = []string{"-"}
	}

	counter := opts.start
	blankRun := int64(0)
	status := 0
	blankField := strings.Repeat(" ", opts.width+len(opts.separator))
	for _, name := range files {
		input, err := openInput(name)
		if err != nil {
			fatalf("nl", "cannot open %s for reading: %s", name, errText(err))
			status = 1
			continue
		}
		sc := newLineScanner(input)
		for sc.Scan() {
			line := sc.Text()
			numbered := nlLineNumbered(opts, line, &blankRun)
			var writeErr error
			if numbered {
				numStr := formatNlNumber(opts.format, opts.width, counter)
				_, writeErr = fmt.Fprintf(os.Stdout, "%s%s%s\n", numStr, opts.separator, line)
				counter += opts.increment
			} else {
				_, writeErr = fmt.Fprintf(os.Stdout, "%s%s\n", blankField, line)
			}
			if writeErr != nil {
				fatalf("nl", "write error: %v", writeErr)
				input.Close()
				return 1
			}
		}
		if scanErr("nl", name, sc) {
			status = 1
		}
		input.Close()
	}
	return status
}

// nlLineNumbered reports whether line should be numbered under opts, and
// advances blankRun so a run of consecutive blank lines is grouped by -l.
func nlLineNumbered(opts nlOptions, line string, blankRun *int64) bool {
	if line != "" {
		*blankRun = 0
		switch opts.style {
		case 'a':
			return true
		case 't':
			return true
		case 'n':
			return false
		case 'p':
			return opts.pattern.MatchString(line)
		}
		return false
	}
	// Blank line: style 't' never numbers it; style 'n' never does either;
	// style 'p' only if the pattern matches an empty string; style 'a' numbers
	// every -l'th one in the current run.
	eligible := opts.style == 'a' || (opts.style == 'p' && opts.pattern.MatchString(""))
	if !eligible {
		*blankRun = 0
		return false
	}
	*blankRun++
	return *blankRun%opts.joinBlank == 0
}

// formatNlNumber renders n under format ("ln", "rn", or "rz") in a field of
// the given width, matching nl's own printf-style behavior of simply growing
// past the requested width rather than truncating.
func formatNlNumber(format string, width int, n int64) string {
	switch format {
	case "ln":
		return fmt.Sprintf("%-*d", width, n)
	case "rz":
		return fmt.Sprintf("%0*d", width, n)
	default: // "rn"
		return fmt.Sprintf("%*d", width, n)
	}
}
