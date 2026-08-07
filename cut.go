// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// cmdCut implements cut(1): extract sections from each line. Supports -f
// (fields, with -d delimiter, default TAB) and -c (character positions). Lists
// are comma-separated ranges like "1,3-5,7-". -s suppresses lines with no
// delimiter (field mode). --output-delimiter sets the field join string.
func cmdCut(args []string) int {
	args = expandShortOptions(args, "bcfd")
	var (
		fieldList string
		charList  string
		delim     = "\t"
		outDelim  string
		outSet    bool
		onlyDelim bool
		files     []string
	)

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-s" || a == "--only-delimited":
			onlyDelim = true
		case a == "-f" || a == "--fields":
			i++
			if i >= len(args) {
				fatalf("cut", "option requires an argument -- 'f'")
				return 1
			}
			fieldList = args[i]
		case strings.HasPrefix(a, "-f"):
			fieldList = a[2:]
		case a == "-c" || a == "--characters":
			i++
			if i >= len(args) {
				fatalf("cut", "option requires an argument -- 'c'")
				return 1
			}
			charList = args[i]
		case strings.HasPrefix(a, "-c"):
			charList = a[2:]
		case a == "-d" || a == "--delimiter":
			i++
			if i >= len(args) {
				fatalf("cut", "option requires an argument -- 'd'")
				return 1
			}
			delim = args[i]
		case strings.HasPrefix(a, "-d"):
			delim = a[2:]
		case strings.HasPrefix(a, "--output-delimiter="):
			outDelim, outSet = a[len("--output-delimiter="):], true
		case len(a) > 1 && a[0] == '-':
			fatalf("cut", "invalid option %q", a)
			return 1
		default:
			files = append(files, a)
		}
	}
rest:
	files = append(files, args[i:]...)

	if fieldList == "" && charList == "" {
		fatalf("cut", "you must specify a list of bytes, characters, or fields")
		return 1
	}
	if fieldList != "" && charList != "" {
		fatalf("cut", "only one type of list may be specified")
		return 1
	}
	if fieldList != "" && utf8.RuneCountInString(delim) != 1 {
		fatalf("cut", "the delimiter must be a single character")
		return 1
	}
	if charList != "" && onlyDelim {
		fatalf("cut", "suppressing non-delimited lines is only meaningful in field mode")
		return 1
	}

	listSpec := fieldList
	if charList != "" {
		listSpec = charList
	}
	ranges, err := parseRanges(listSpec)
	if err != nil {
		fatalf("cut", "%v", err)
		return 1
	}

	if len(files) == 0 {
		files = []string{"-"}
	}
	out := bufio.NewWriter(os.Stdout)

	status := 0
	for _, f := range files {
		r, err := openInput(f)
		if err != nil {
			fatalf("cut", "%s: %v", f, err)
			status = 1
			continue
		}
		sc := newLineScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if charList != "" {
				_, _ = out.WriteString(cutChars(line, ranges)) // Flush reports the sticky error.
			} else {
				joined := outDelim
				if !outSet {
					joined = delim
				}
				res, ok := cutFields(line, delim, joined, ranges, onlyDelim)
				if !ok {
					continue
				}
				_, _ = out.WriteString(res) // Flush reports the sticky error.
			}
			_ = out.WriteByte('\n') // Flush reports the sticky error.
		}
		if scanErr("cut", f, sc) {
			status = 1
		}
		if closeErr := r.Close(); closeErr != nil {
			fatalf("cut", "%s: %v", f, closeErr)
			status = 1
		}
	}
	if err := out.Flush(); err != nil {
		fatalf("cut", "write error: %v", err)
		status = 1
	}
	return status
}

// cutRange is an inclusive 1-based range; hi==0 means "to end of line".
type cutRange struct{ lo, hi int }

func parseRanges(spec string) ([]cutRange, error) {
	var ranges []cutRange
	for _, part := range strings.Split(spec, ",") {
		if part == "" {
			continue
		}
		dash := strings.IndexByte(part, '-')
		if dash < 0 {
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("invalid field value %q", part)
			}
			ranges = append(ranges, cutRange{n, n})
			continue
		}
		loStr, hiStr := part[:dash], part[dash+1:]
		lo, hi := 1, 0
		if loStr != "" {
			v, err := strconv.Atoi(loStr)
			if err != nil || v < 1 {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			lo = v
		}
		if hiStr != "" {
			v, err := strconv.Atoi(hiStr)
			if err != nil || v < 1 {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			hi = v
		}
		if hi != 0 && hi < lo {
			return nil, fmt.Errorf("invalid decreasing range %q", part)
		}
		ranges = append(ranges, cutRange{lo, hi})
	}
	if len(ranges) == 0 {
		return nil, fmt.Errorf("empty list")
	}
	return ranges, nil
}

// inRanges reports whether the 1-based position pos is selected.
func inRanges(pos int, ranges []cutRange) bool {
	for _, r := range ranges {
		if pos >= r.lo && (r.hi == 0 || pos <= r.hi) {
			return true
		}
	}
	return false
}

func cutChars(line string, ranges []cutRange) string {
	runes := []rune(line)
	var b strings.Builder
	for i, r := range runes {
		if inRanges(i+1, ranges) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cutFields selects fields from line split on delim. Returns (result, emit).
// When onlyDelim is set and the line has no delimiter, emit is false.
func cutFields(line, delim, join string, ranges []cutRange, onlyDelim bool) (string, bool) {
	if !strings.Contains(line, delim) {
		if onlyDelim {
			return "", false
		}
		return line, true
	}
	fields := strings.Split(line, delim)
	var selected []string
	for i, f := range fields {
		if inRanges(i+1, ranges) {
			selected = append(selected, f)
		}
	}
	return strings.Join(selected, join), true
}
