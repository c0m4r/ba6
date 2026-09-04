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

// cutMode is which kind of list was given: -b, -c or -f.
type cutMode int

const (
	cutModeNone cutMode = iota
	cutModeBytes
	cutModeChars
	cutModeFields
)

type cutOptions struct {
	mode       cutMode
	list       string
	delim      string
	outDelim   string
	outSet     bool
	onlyDelim  bool
	complement bool
	zeroTerm   bool
	ranges     []cutRange
}

// cmdCut implements cut(1): extract sections from each line. -f selects fields
// (with -d, default TAB), -c character positions and -b byte positions; lists
// are comma-separated ranges like "1,3-5,7-". -s suppresses lines with no
// delimiter, --complement inverts the selection and --output-delimiter sets the
// string written between the pieces that are kept.
func cmdCut(args []string) int {
	opts := cutOptions{delim: "\t"}
	var files []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		next := func(name string) (string, bool) {
			i++
			if i >= len(args) {
				cutUsageError("option requires an argument -- '%s'", name)
				return "", false
			}
			return args[i], true
		}
		var value string
		var ok bool
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-s" || a == "--only-delimited":
			opts.onlyDelim = true
		case a == "--complement":
			opts.complement = true
		case a == "-z" || a == "--zero-terminated":
			opts.zeroTerm = true
		case a == "-n":
			// POSIX's "do not split multibyte characters"; -b already keeps
			// whole bytes, so the original ignores it too.
		case a == "-f" || a == "--fields" || a == "-c" || a == "--characters" || a == "-b" || a == "--bytes":
			name := strings.TrimLeft(a, "-")[:1]
			if value, ok = next(name); !ok {
				return 1
			}
			if !opts.setList(name[0], value) {
				return 1
			}
		case strings.HasPrefix(a, "--fields=") || strings.HasPrefix(a, "--characters=") || strings.HasPrefix(a, "--bytes="):
			name := a[2]
			if !opts.setList(name, a[strings.IndexByte(a, '=')+1:]) {
				return 1
			}
		case len(a) > 2 && a[0] == '-' && (a[1] == 'f' || a[1] == 'c' || a[1] == 'b'):
			if !opts.setList(a[1], a[2:]) {
				return 1
			}
		case a == "-d" || a == "--delimiter":
			if opts.delim, ok = next("d"); !ok {
				return 1
			}
		case strings.HasPrefix(a, "--delimiter="):
			opts.delim = strings.TrimPrefix(a, "--delimiter=")
		case strings.HasPrefix(a, "-d") && len(a) > 2:
			opts.delim = a[2:]
		case a == "--output-delimiter":
			if opts.outDelim, ok = next("-output-delimiter"); !ok {
				return 1
			}
			opts.outSet = true
		case strings.HasPrefix(a, "--output-delimiter="):
			opts.outDelim, opts.outSet = a[len("--output-delimiter="):], true
		case len(a) > 1 && a[0] == '-':
			// A bundle such as -sf3 or -zd:.
			if !opts.parseCluster(a, args, &i) {
				return 1
			}
		default:
			files = append(files, a)
		}
	}
rest:
	files = append(files, args[i:]...)

	if opts.mode == cutModeNone {
		cutUsageError("you must specify a list of bytes, characters, or fields")
		return 1
	}
	if opts.mode == cutModeFields && utf8.RuneCountInString(opts.delim) != 1 {
		cutUsageError("the delimiter must be a single character")
		return 1
	}
	if opts.mode != cutModeFields && opts.onlyDelim {
		cutUsageError("suppressing non-delimited lines makes sense\n\tonly when operating on fields")
		return 1
	}
	ranges, err := parseRanges(opts.list, opts.mode)
	if err != nil {
		cutUsageError("%v", err)
		return 1
	}
	opts.ranges = mergeRanges(ranges)
	if opts.complement {
		opts.ranges = complementRanges(opts.ranges)
	}

	if len(files) == 0 {
		files = []string{"-"}
	}
	return opts.run(files)
}

// setList records one of -b/-c/-f; the original refuses more than one.
func (o *cutOptions) setList(kind byte, list string) bool {
	mode := cutModeFields
	switch kind {
	case 'c':
		mode = cutModeChars
	case 'b':
		mode = cutModeBytes
	}
	if o.mode != cutModeNone && o.mode != mode {
		cutUsageError("only one list may be specified")
		return false
	}
	o.mode, o.list = mode, list
	return true
}

// parseCluster handles bundled short options, where a list or delimiter option
// takes the rest of the word (or the next argument).
func (o *cutOptions) parseCluster(arg string, args []string, i *int) bool {
	for j := 1; j < len(arg); j++ {
		c := arg[j]
		switch c {
		case 's':
			o.onlyDelim = true
		case 'z':
			o.zeroTerm = true
		case 'n':
		case 'b', 'c', 'f', 'd':
			value := arg[j+1:]
			if value == "" {
				*i++
				if *i >= len(args) {
					cutUsageError("option requires an argument -- '%c'", c)
					return false
				}
				value = args[*i]
			}
			if c == 'd' {
				o.delim = value
				return true
			}
			return o.setList(c, value)
		default:
			cutUsageError("invalid option -- '%c'", c)
			return false
		}
	}
	return true
}

// cutUsageError prints a command-line complaint the way the original does,
// including the "Try ... --help" line.
func cutUsageError(format string, args ...any) {
	fatalf("cut", format, args...)
	fmt.Fprintln(os.Stderr, "Try 'cut --help' for more information.")
}

func (o *cutOptions) run(files []string) int {
	out := bufio.NewWriter(os.Stdout)
	terminator := byte('\n')
	if o.zeroTerm {
		terminator = 0
	}
	status := 0
	for _, f := range files {
		r, err := openInput(f)
		if err != nil {
			fatalf("cut", "%s: %s", f, errText(err))
			status = 1
			continue
		}
		sc := newLineScanner(r)
		if o.zeroTerm {
			sc.Split(scanNulLines)
		}
		for sc.Scan() {
			line := sc.Text()
			var text string
			if o.mode == cutModeFields {
				selected, emit := o.cutFields(line)
				if !emit {
					continue
				}
				text = selected
			} else {
				text = o.cutPositions(line)
			}
			_, _ = out.WriteString(text)  // Flush reports the sticky error.
			_ = out.WriteByte(terminator) // Flush reports the sticky error.
		}
		if scanErr("cut", f, sc) {
			status = 1
		}
		if closeErr := r.Close(); closeErr != nil {
			fatalf("cut", "%s: %s", f, errText(closeErr))
			status = 1
		}
	}
	if err := out.Flush(); err != nil {
		fatalf("cut", "write error: %s", errText(err))
		status = 1
	}
	return status
}

// cutRange is an inclusive 1-based range; hi==0 means "to end of line".
type cutRange struct{ lo, hi int }

func parseRanges(spec string, mode cutMode) ([]cutRange, error) {
	zeroText := "fields are numbered from 1"
	valueText := "invalid field value"
	if mode != cutModeFields {
		zeroText = "byte/character positions are numbered from 1"
		valueText = "invalid byte/character position"
	}
	var ranges []cutRange
	for _, part := range strings.Split(spec, ",") {
		if part == "" {
			continue
		}
		number := func(text string) (int, error) {
			digits := 0
			for digits < len(text) && isDigitByte(text[digits]) {
				digits++
			}
			if digits != len(text) || digits == 0 {
				return 0, fmt.Errorf("%s %s", valueText, quoteLocaleName(text[digits:]))
			}
			n, err := strconv.Atoi(text)
			if err != nil {
				return 0, fmt.Errorf("%s %s", valueText, quoteLocaleName(text))
			}
			if n < 1 {
				return 0, fmt.Errorf("%s", zeroText)
			}
			return n, nil
		}
		dash := strings.IndexByte(part, '-')
		if dash < 0 {
			n, err := number(part)
			if err != nil {
				return nil, err
			}
			ranges = append(ranges, cutRange{n, n})
			continue
		}
		loText, hiText := part[:dash], part[dash+1:]
		lo, hi := 1, 0
		if loText != "" {
			v, err := number(loText)
			if err != nil {
				return nil, err
			}
			lo = v
		}
		if hiText != "" {
			v, err := number(hiText)
			if err != nil {
				return nil, err
			}
			hi = v
		}
		if hi != 0 && hi < lo {
			return nil, fmt.Errorf("invalid decreasing range")
		}
		ranges = append(ranges, cutRange{lo, hi})
	}
	if len(ranges) == 0 {
		return nil, fmt.Errorf("%s", zeroText)
	}
	return ranges, nil
}

// mergeRanges sorts the ranges and folds overlapping or adjacent ones together,
// so a list like "1-1,1-2" selects one run rather than two.
func mergeRanges(ranges []cutRange) []cutRange {
	sorted := make([]cutRange, len(ranges))
	copy(sorted, ranges)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].lo < sorted[j-1].lo; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	var merged []cutRange
	for _, r := range sorted {
		if len(merged) == 0 {
			merged = append(merged, r)
			continue
		}
		last := &merged[len(merged)-1]
		if last.hi == 0 {
			continue // Already open to the end of the line.
		}
		if r.lo > last.hi+1 {
			merged = append(merged, r)
			continue
		}
		if r.hi == 0 || r.hi > last.hi {
			last.hi = r.hi
		}
	}
	return merged
}

// complementRanges returns the ranges that the merged input does not cover.
func complementRanges(ranges []cutRange) []cutRange {
	var out []cutRange
	next := 1
	for _, r := range ranges {
		if r.lo > next {
			out = append(out, cutRange{next, r.lo - 1})
		}
		if r.hi == 0 {
			return out
		}
		next = r.hi + 1
	}
	return append(out, cutRange{next, 0})
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

// cutPositions implements -b and -c: each selected run of the line is emitted,
// with --output-delimiter written between runs that both produced text.
func (o *cutOptions) cutPositions(line string) string {
	units := []string{}
	if o.mode == cutModeBytes {
		for i := 0; i < len(line); i++ {
			units = append(units, line[i:i+1])
		}
	} else {
		for _, r := range line {
			units = append(units, string(r))
		}
	}
	var pieces []string
	for _, r := range o.ranges {
		lo, hi := r.lo, r.hi
		if hi == 0 || hi > len(units) {
			hi = len(units)
		}
		if lo > len(units) || lo > hi {
			continue
		}
		pieces = append(pieces, strings.Join(units[lo-1:hi], ""))
	}
	return strings.Join(pieces, o.outDelim)
}

// cutFields implements -f. Returns (result, emit); emit is false when -s
// suppresses a line that holds no delimiter.
func (o *cutOptions) cutFields(line string) (string, bool) {
	if !strings.Contains(line, o.delim) {
		if o.onlyDelim {
			return "", false
		}
		return line, true
	}
	join := o.delim
	if o.outSet {
		join = o.outDelim
	}
	fields := strings.Split(line, o.delim)
	var selected []string
	for i, f := range fields {
		if inRanges(i+1, o.ranges) {
			selected = append(selected, f)
		}
	}
	return strings.Join(selected, join), true
}
