// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// foldUnit is one wrappable piece of a line: a byte (-b) or a rune (default,
// -c). r is retained separately from its byte span so the column-advance
// rules below can inspect it without re-decoding.
type foldUnit struct {
	start, end int
	r          rune
	blank      bool
}

// cmdFold implements fold(1): wrap each line of input to a fixed width. The
// default and -c modes track the same "column" a terminal would show,
// including tab stops (every 8th column), backspace (moves back one column)
// and carriage return (returns to column 0); -b counts raw bytes instead,
// with no such adjustments; -s breaks after the last blank in the wrapped
// segment instead of mid-word, when one is present.
func cmdFold(args []string) int {
	args = expandShortOptions(args, "w")
	byteMode := false
	spaces := false
	width := 80
	var files []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-b" || a == "--bytes":
			byteMode = true
		case a == "-c" || a == "--characters":
			byteMode = false
		case a == "-s" || a == "--spaces":
			spaces = true
		case a == "-w" || a == "--width":
			i++
			if i >= len(args) {
				fatalf("fold", "option requires an argument -- 'w'")
				return 1
			}
			value := args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				fatalf("fold", "invalid number of columns: %q", value)
				return 1
			}
			width = n
		case strings.HasPrefix(a, "--width="):
			value := strings.TrimPrefix(a, "--width=")
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				fatalf("fold", "invalid number of columns: %q", value)
				return 1
			}
			width = n
		case len(a) > 1 && a[0] == '-':
			fatalf("fold", "invalid option %q", a)
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

	status := 0
	for _, name := range files {
		input, err := openInput(name)
		if err != nil {
			fatalf("fold", "cannot open %s for reading: %s", name, errText(err))
			status = 1
			continue
		}
		data, err := io.ReadAll(input)
		input.Close()
		if err != nil {
			fatalf("fold", "%s: %s", name, errText(err))
			status = 1
			continue
		}
		if err := foldFile(os.Stdout, string(data), width, byteMode, spaces); err != nil {
			fatalf("fold", "write error: %v", err)
			return 1
		}
	}
	return status
}

// foldFile wraps every line of data and writes the result to w, preserving
// data's own line terminators exactly (including a missing final newline).
func foldFile(w io.Writer, data string, width int, byteMode, spaces bool) error {
	if data == "" {
		return nil
	}
	lines := strings.Split(data, "\n")
	for i, line := range lines {
		last := i == len(lines)-1
		if last && line == "" {
			break // the split's trailing empty element is not a real line
		}
		if err := wrapFoldLine(w, line, byteMode, spaces, width); err != nil {
			return err
		}
		if !last {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

// foldLineUnits splits line into wrappable units: one per byte in byte mode,
// one per rune otherwise.
func foldLineUnits(line string, byteMode bool) []foldUnit {
	if byteMode {
		units := make([]foldUnit, len(line))
		for i := 0; i < len(line); i++ {
			b := line[i]
			units[i] = foldUnit{i, i + 1, rune(b), b == ' ' || b == '\t'}
		}
		return units
	}
	units := make([]foldUnit, 0, len(line))
	for i, r := range line {
		units = append(units, foldUnit{i, i + utf8.RuneLen(r), r, r == ' ' || r == '\t'})
	}
	return units
}

// foldColumnAdvance reports how many columns r adds when it appears at the
// given column, following the same tab/backspace/carriage-return handling
// fold's default and -c modes give a terminal-displayed line. -b mode never
// calls this: every byte there is worth exactly one column.
func foldColumnAdvance(col int, r rune) int {
	switch r {
	case '\t':
		return ((col/8)+1)*8 - col
	case '\b':
		if col > 0 {
			return -1
		}
		return 0
	case '\r':
		return -col
	default:
		return 1
	}
}

// wrapFoldLine writes one input line (no trailing newline) to w, splitting it
// into width-limited segments separated by "\n"; the final segment is
// written without a trailing newline, left to the caller.
func wrapFoldLine(w io.Writer, line string, byteMode, spaces bool, width int) error {
	units := foldLineUnits(line, byteMode)
	n := len(units)
	segStart, col, idx := 0, 0, 0
	for idx < n {
		advance := 1
		if !byteMode {
			advance = foldColumnAdvance(col, units[idx].r)
		}
		if col > 0 && col+advance > width {
			// A tab (the only way advance can exceed 1) would overshoot the
			// width: break before placing it.
			breakIdx := foldBackoffPoint(units, segStart, idx, spaces)
			if err := writeFoldSegment(w, line, units, segStart, breakIdx, true); err != nil {
				return err
			}
			segStart, col, idx = breakIdx, 0, breakIdx
			continue
		}
		col += advance
		idx++
		// Reaching the width exactly on the line's very last unit is just the
		// ordinary end of the line, not a wrap point: only break here if
		// something is still left to defer to the next segment.
		if col >= width && idx < n {
			breakIdx := foldBackoffPoint(units, segStart, idx, spaces)
			if err := writeFoldSegment(w, line, units, segStart, breakIdx, true); err != nil {
				return err
			}
			segStart, col, idx = breakIdx, 0, breakIdx
		}
	}
	return writeFoldSegment(w, line, units, segStart, n, false)
}

// foldBackoffPoint implements -s: if the segment [segStart, breakIdx) would
// otherwise end mid-word, it looks backward for the last blank unit and
// returns just past it instead, so that blank stays with this segment and
// the word moves to the next one. It falls back to breakIdx unchanged when
// -s is off, the segment already ends on a blank, or no blank is found.
func foldBackoffPoint(units []foldUnit, segStart, breakIdx int, spaces bool) int {
	if !spaces || breakIdx <= segStart || units[breakIdx-1].blank {
		return breakIdx
	}
	for j := breakIdx - 1; j > segStart; {
		j--
		if units[j].blank {
			return j + 1
		}
	}
	return breakIdx
}

func writeFoldSegment(w io.Writer, line string, units []foldUnit, from, to int, newline bool) error {
	if from < to {
		if _, err := io.WriteString(w, line[units[from].start:units[to-1].end]); err != nil {
			return err
		}
	}
	if newline {
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	return nil
}
