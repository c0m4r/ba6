// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// tabStops is the tab-stop model expand(1) and unexpand(1) share. size is a
// single repeating stop distance; stops is an explicit list of columns
// (0-based); extend ('/N') is a repeating size used after the last explicit
// stop, and increment ('+N') repeats stops relative to the last explicit one.
// The empty model means the default: stops every 8 columns.
type tabStops struct {
	size      int
	stops     []int
	extend    int
	increment int
}

// next returns the tab stop after column, advancing the explicit-stop scan
// index when the list is in use. last reports that the stop list is exhausted
// without a repeat rule, where a tab collapses to a single space.
func (t *tabStops) next(column int, index *int) (stop int, last bool) {
	if t.size > 0 {
		return column + (t.size - column%t.size), false
	}
	for *index < len(t.stops) {
		if column < t.stops[*index] {
			return t.stops[*index], false
		}
		*index++
	}
	if t.extend > 0 {
		return column + (t.extend - column%t.extend), false
	}
	if t.increment > 0 {
		end := t.stops[len(t.stops)-1]
		return column + (t.increment - (column-end)%t.increment), false
	}
	return column + 1, true
}

// addTabSpec parses one -t/--tabs argument — a size, or a comma- or
// blank-separated stop list whose last entry may be prefixed with '/'
// (repeating size) or '+' (increment) — and accumulates it into t, mirroring
// GNU's parse_tab_stops including its diagnostics.
func (t *tabStops) addTabSpec(spec string) error {
	haveValue := false
	value := 0
	extendFlag := false
	incrementFlag := false
	add := func() error {
		if !haveValue {
			return nil
		}
		if extendFlag {
			if t.extend != 0 {
				return fmt.Errorf("'/' specifier only allowed with the last value")
			}
			t.extend = value
		} else if incrementFlag {
			if t.increment != 0 {
				return fmt.Errorf("'+' specifier only allowed with the last value")
			}
			t.increment = value
		} else {
			t.stops = append(t.stops, value)
		}
		haveValue = false
		return nil
	}
	for i := 0; i < len(spec); i++ {
		ch := spec[i]
		switch {
		case ch == ',' || ch == ' ' || ch == '\t':
			if err := add(); err != nil {
				return err
			}
		case ch == '/':
			if haveValue {
				return fmt.Errorf("'/' specifier not at start of number: %q", spec[i:])
			}
			extendFlag = true
			incrementFlag = false
		case ch == '+':
			if haveValue {
				return fmt.Errorf("'+' specifier not at start of number: %q", spec[i:])
			}
			incrementFlag = true
			extendFlag = false
		case ch >= '0' && ch <= '9':
			if !haveValue {
				value = 0
				haveValue = true
			}
			value = value*10 + int(ch-'0')
			if value > 1<<31 {
				return fmt.Errorf("tab stop is too large")
			}
		default:
			return fmt.Errorf("tab size contains invalid character(s): %q", spec[i:])
		}
	}
	return add()
}

// finalize applies GNU's finalize_tab_stops: a single bare stop becomes a
// repeating size, an empty spec defaults to 8 (or the /N or +N value), and
// the explicit list is validated.
func (t *tabStops) finalize(prog string) bool {
	for _, s := range t.stops {
		if s == 0 {
			fatalf(prog, "tab size cannot be 0")
			return false
		}
	}
	for i := 1; i < len(t.stops); i++ {
		if t.stops[i] <= t.stops[i-1] {
			fatalf(prog, "tab sizes must be ascending")
			return false
		}
	}
	if t.extend != 0 && t.increment != 0 {
		fatalf(prog, "'/' specifier is mutually exclusive with '+'")
		return false
	}
	if len(t.stops) == 0 {
		switch {
		case t.extend != 0:
			t.size = t.extend
		case t.increment != 0:
			t.size = t.increment
		default:
			t.size = 8
		}
	} else if len(t.stops) == 1 && t.extend == 0 && t.increment == 0 {
		t.size = t.stops[0]
	}
	return true
}

// parseTabArgs parses the options expand and unexpand share: -t/--tabs (the
// attached, spaced and = forms), the legacy bare -N[,N...] stop form, and --
// as the operand terminator. extra handles the applet-specific flags; it
// receives the current argument and returns whether it consumed it.
func parseTabArgs(prog string, args []string, extra func(a string) (bool, int)) (tabStops, []string, int) {
	var stops tabStops
	var files []string
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if extra != nil {
			if handled, status := extra(a); handled {
				if status != 0 {
					return stops, nil, status
				}
				continue
			}
		}
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-t" || a == "--tabs":
			i++
			if i >= len(args) {
				fatalf(prog, "option requires an argument -- 't'")
				return stops, nil, 1
			}
			if err := stops.addTabSpec(args[i]); err != nil { //nolint:gosec // G602: i is checked against len(args) immediately above.
				fatalf(prog, "%v", err)
				return stops, nil, 1
			}
		case strings.HasPrefix(a, "--tabs="):
			if err := stops.addTabSpec(strings.TrimPrefix(a, "--tabs=")); err != nil {
				fatalf(prog, "%v", err)
				return stops, nil, 1
			}
		case len(a) > 2 && a[0] == '-' && a[1] == 't':
			if err := stops.addTabSpec(a[2:]); err != nil {
				fatalf(prog, "%v", err)
				return stops, nil, 1
			}
		case len(a) > 1 && a[0] == '-' && a[1] >= '0' && a[1] <= '9':
			if err := stops.addTabSpec(a[1:]); err != nil {
				fatalf(prog, "%v", err)
				return stops, nil, 1
			}
		case len(a) > 1 && a[0] == '-':
			fatalf(prog, "invalid option %q", a)
			return stops, nil, 1
		default:
			files = append(files, a)
		}
	}
rest:
	files = append(files, args[i:]...)
	return stops, files, 0
}

// forEachInput opens each named file (or standard input for "-" and for an
// empty list) in order and calls fn on a buffered reader of it, so a line
// split across files keeps its column state like GNU's single stream.
func forEachInput(prog string, files []string, fn func(name string, r *bufio.Reader) error) (status int) {
	if len(files) == 0 {
		files = []string{"-"}
	}
	for _, name := range files {
		input, err := openInput(name)
		if err != nil {
			fatalf(prog, "%s: %s", name, errText(err))
			status = 1
			continue
		}
		err = fn(name, bufio.NewReader(input))
		input.Close()
		if err != nil {
			fatalf(prog, "%s: %s", name, errText(err))
			status = 1
		}
	}
	return status
}

// expandState carries the line-state that expandStream keeps across the
// file-to-file stream handoff: a line that ends a file without a newline
// continues in the next file with the same column.
type expandState struct {
	convert  bool
	column   int
	tabIndex int
}

// cmdExpand implements expand(1): convert tabs to spaces, tracking columns
// the way a terminal would (tab stops, backspace). -i restricts conversion to
// leading blanks; -t sets the tab stops. The files are processed as one
// continuous line stream, matching the original's column accounting.
func cmdExpand(args []string) int {
	initialOnly := false
	stops, files, status := parseTabArgs("expand", args, func(a string) (bool, int) {
		if a == "-i" || a == "--initial" {
			initialOnly = true
			return true, 0
		}
		return false, 0
	})
	if status != 0 {
		return status
	}
	if !stops.finalize("expand") {
		return 1
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	var state = expandState{convert: true}
	status = forEachInput("expand", files, func(_ string, r *bufio.Reader) error {
		return state.run(out, r, stops, initialOnly)
	})
	if err := out.Flush(); err != nil {
		fatalf("expand", "write error: %v", err)
		return 1
	}
	return status
}

// run writes r to w with each tab replaced by the spaces that reach the next
// tab stop, keeping the column state that -i and backspace depend on.
func (s *expandState) run(w *bufio.Writer, r *bufio.Reader, stops tabStops, initialOnly bool) error {
	for {
		b, err := r.ReadByte()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if s.convert {
			s.convert = !initialOnly || b == ' ' || b == '\t'
			switch b {
			case '\t':
				next, _ := stops.next(s.column, &s.tabIndex)
				for s.column < next {
					if werr := w.WriteByte(' '); werr != nil {
						return werr
					}
					s.column++
				}
				continue
			case '\b':
				if s.column > 0 {
					s.column--
				}
				if s.tabIndex > 0 {
					s.tabIndex--
				}
			default:
				s.column++
			}
		}
		if werr := w.WriteByte(b); werr != nil {
			return werr
		}
		if b == '\n' {
			s.convert = true
			s.column = 0
			s.tabIndex = 0
		}
	}
}
