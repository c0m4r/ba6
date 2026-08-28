// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"io"
	"os"
	"regexp"
	"strings"
)

// cmdTac implements tac(1): print each file with its records in reverse
// order (last "line" first). -s changes the separator from a newline, -b
// attaches it to the front of the record that follows instead of the back of
// the one before, and -r treats it as a regular expression. Each file's own
// records are reversed independently; multiple files are still written in
// the order given, matching the original.
func cmdTac(args []string) int {
	args = expandShortOptions(args, "s")
	before := false
	useRegex := false
	separator := "\n"
	var files []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-b" || a == "--before":
			before = true
		case a == "-r" || a == "--regex":
			useRegex = true
		case a == "-s" || a == "--separator":
			i++
			if i >= len(args) {
				fatalf("tac", "option requires an argument -- 's'")
				return 1
			}
			separator = args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
		case strings.HasPrefix(a, "--separator="):
			separator = strings.TrimPrefix(a, "--separator=")
		case len(a) > 1 && a[0] == '-':
			fatalf("tac", "invalid option %q", a)
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

	var sepRe *regexp.Regexp
	if separator != "" {
		var pattern string
		if useRegex {
			translated, err := translatePOSIXRegexp(separator, posixBRE)
			if err != nil {
				fatalf("tac", "invalid separator: %v", err)
				return 1
			}
			pattern = translated
		} else {
			pattern = regexp.QuoteMeta(separator)
		}
		re, err := compilePOSIXERE(pattern, false)
		if err != nil {
			fatalf("tac", "invalid separator: %v", err)
			return 1
		}
		sepRe = re
	}

	status := 0
	for _, name := range files {
		input, err := openInput(name)
		if err != nil {
			fatalf("tac", "failed to open '%s' for reading: %s", name, errText(err))
			status = 1
			continue
		}
		data, err := io.ReadAll(input)
		input.Close()
		if err != nil {
			fatalf("tac", "%s: %s", name, errText(err))
			status = 1
			continue
		}
		if err := writeTacRecords(os.Stdout, string(data), sepRe, before); err != nil {
			fatalf("tac", "write error: %v", err)
			return 1
		}
	}
	return status
}

// writeTacRecords writes data's records (split by splitTacRecords) to w in
// reverse order.
func writeTacRecords(w io.Writer, data string, sepRe *regexp.Regexp, before bool) error {
	records := splitTacRecords(data, sepRe, before)
	for i := len(records) - 1; i >= 0; i-- {
		if _, err := io.WriteString(w, records[i]); err != nil {
			return err
		}
	}
	return nil
}

// splitTacRecords splits data at each match of sepRe, keeping the separator
// attached to the record that follows it when before is true (-b) or the one
// that precedes it otherwise (the default) -- so the cut falls at a match's
// start or its end, respectively.
func splitTacRecords(data string, sepRe *regexp.Regexp, before bool) []string {
	if data == "" {
		return nil
	}
	if sepRe == nil {
		return []string{data}
	}
	matches := sepRe.FindAllStringIndex(data, -1)
	if len(matches) == 0 {
		return []string{data}
	}
	boundaries := make([]int, 0, len(matches)+2)
	boundaries = append(boundaries, 0)
	for _, m := range matches {
		if m[0] == m[1] {
			continue
		}
		if before {
			boundaries = append(boundaries, m[0])
		} else {
			boundaries = append(boundaries, m[1])
		}
	}
	boundaries = append(boundaries, len(data))
	records := make([]string, 0, len(boundaries)-1)
	for i := 1; i < len(boundaries); i++ {
		records = append(records, data[boundaries[i-1]:boundaries[i]])
	}
	return records
}
