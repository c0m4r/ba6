// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
)

// joinFieldSpec is one element of a -o FORMAT: field FIELD of FILE (0 for the
// join field itself).
type joinFieldSpec struct {
	file  int
	field int
}

// cmdJoin implements join(1): write a line for each pair of lines in two
// sorted files whose join fields (the first by default, -1/-2 to select) are
// equal. Without -t fields are split on blank runs and re-joined with single
// spaces; -a/-v select unpairable lines, -o an explicit field list, and -e
// fills fields missing from unpairable lines.
func cmdJoin(args []string) int {
	field1, field2 := 1, 1
	with1, with2 := false, false
	only1, only2 := false, false
	caseInsensitive := false
	header := false
	zero := false
	checkOrder := false
	noCheckOrder := false
	sep := byte(0)
	empty := ""
	var format []joinFieldSpec
	formatSet := false
	var operands []string
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			operands = append(operands, args[i:]...)
			i = len(args)
		case a == "-1":
			i++
			if i >= len(args) {
				fatalf("join", "option requires an argument -- '1'")
				return 1
			}
			value, err := strconv.Atoi(args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
			if err != nil || value < 1 {
				fatalf("join", "invalid field number: %q", args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
				return 1
			}
			field1 = value
		case a == "-2":
			i++
			if i >= len(args) {
				fatalf("join", "option requires an argument -- '2'")
				return 1
			}
			value, err := strconv.Atoi(args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
			if err != nil || value < 1 {
				fatalf("join", "invalid field number: %q", args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
				return 1
			}
			field2 = value
		case strings.HasPrefix(a, "--j1="):
			value, err := strconv.Atoi(strings.TrimPrefix(a, "--j1="))
			if err != nil || value < 1 {
				fatalf("join", "invalid field number: %q", strings.TrimPrefix(a, "--j1="))
				return 1
			}
			field1 = value
		case strings.HasPrefix(a, "--j2="):
			value, err := strconv.Atoi(strings.TrimPrefix(a, "--j2="))
			if err != nil || value < 1 {
				fatalf("join", "invalid field number: %q", strings.TrimPrefix(a, "--j2="))
				return 1
			}
			field2 = value
		case a == "-a":
			i++
			if i >= len(args) {
				fatalf("join", "option requires an argument -- 'a'")
				return 1
			}
			switch args[i] { //nolint:gosec // G602: i is checked against len(args) immediately above.
			case "1":
				with1 = true
			case "2":
				with2 = true
			default:
				fatalf("join", "invalid field number: %q", args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
				return 1
			}
		case a == "-v":
			i++
			if i >= len(args) {
				fatalf("join", "option requires an argument -- 'v'")
				return 1
			}
			switch args[i] { //nolint:gosec // G602: i is checked against len(args) immediately above.
			case "1":
				only1 = true
			case "2":
				only2 = true
			default:
				fatalf("join", "invalid field number: %q", args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
				return 1
			}
		case a == "-o" || a == "--format":
			i++
			if i >= len(args) {
				fatalf("join", "option requires an argument -- 'o'")
				return 1
			}
			specs, ok := parseJoinFormat(args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
			if !ok {
				return 1
			}
			format, formatSet = specs, true
		case strings.HasPrefix(a, "--format="):
			specs, ok := parseJoinFormat(strings.TrimPrefix(a, "--format="))
			if !ok {
				return 1
			}
			format, formatSet = specs, true
		case a == "-t" || a == "--field-separator":
			i++
			if i >= len(args) {
				fatalf("join", "option requires an argument -- 't'")
				return 1
			}
			if len(args[i]) > 1 { //nolint:gosec // G602: i is checked against len(args) immediately above.
				fatalf("join", "multi-character tab %q", args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
				return 1
			}
			sep = args[i][0] //nolint:gosec // G602: i is checked against len(args) immediately above.
		case a == "-e":
			i++
			if i >= len(args) {
				fatalf("join", "option requires an argument -- 'e'")
				return 1
			}
			empty = args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
		case a == "-i" || a == "--ignore-case":
			caseInsensitive = true
		case a == "--header":
			header = true
		case a == "--check-order":
			checkOrder = true
		case a == "--nocheck-order":
			noCheckOrder = true
		case a == "-z" || a == "--zero-terminated":
			zero = true
		case len(a) > 1 && a[0] == '-' && a != "-":
			fatalf("join", "invalid option %q", a)
			return 1
		default:
			operands = append(operands, a)
		}
	}
	if len(operands) != 2 {
		fatalf("join", "missing operand")
		return 1
	}
	if operands[0] == "-" && operands[1] == "-" {
		fatalf("join", "both files cannot be standard input")
		return 1
	}
	terminator := byte('\n')
	if zero {
		terminator = 0
	}

	readFile := func(name string) ([]joinRecord, int) {
		var reader io.Reader
		if name == "-" {
			reader = os.Stdin
		} else {
			file, err := os.Open(name)
			if err != nil {
				fatalf("join", "%s: %s", name, errText(err))
				return nil, 1
			}
			defer file.Close()
			reader = file
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			fatalf("join", "%s: %s", name, errText(err))
			return nil, 1
		}
		return splitJoinRecords(data, terminator, sep), 0
	}
	records1, status := readFile(operands[0])
	if status != 0 {
		return status
	}
	records2, status := readFile(operands[1])
	if status != 0 {
		return status
	}

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	outSep := []byte{' '}
	if sep != 0 {
		outSep = []byte{sep}
	}
	fill := []byte(empty)

	fieldValue := func(rec joinRecord, n int) []byte {
		if n >= 1 && n <= len(rec.fields) {
			return rec.fields[n-1]
		}
		return nil
	}
	// Attach each file's join key and the fields printed around it.
	attach := func(records []joinRecord, field int) {
		for r := range records {
			key := []byte(nil)
			var rest [][]byte
			if field >= 1 && field <= len(records[r].fields) {
				key = records[r].fields[field-1]
				rest = append(append([][]byte{}, records[r].fields[:field-1]...), records[r].fields[field:]...)
			} else {
				rest = records[r].fields
			}
			records[r].key = key
			records[r].rest = rest
		}
	}
	attach(records1, field1)
	attach(records2, field2)
	writeFormatted := func(key []byte, r1, r2 *joinRecord) {
		if formatSet {
			first := true
			for _, spec := range format {
				if !first {
					_, _ = out.Write(outSep)
				}
				first = false
				var value []byte
				switch spec.file {
				case 0:
					value = key
				case 1:
					if r1 != nil {
						value = fieldValue(*r1, spec.field)
					}
				case 2:
					if r2 != nil {
						value = fieldValue(*r2, spec.field)
					}
				}
				if value == nil && empty != "" {
					_, _ = out.Write(fill)
				} else {
					_, _ = out.Write(value)
				}
			}
		} else {
			_, _ = out.Write(key)
			writeRest := func(rec *joinRecord) {
				for _, field := range rec.rest {
					_, _ = out.Write(outSep)
					_, _ = out.Write(field)
				}
			}
			if r1 != nil {
				writeRest(r1)
			}
			if r2 != nil {
				writeRest(r2)
			}
		}
		_, _ = out.Write([]byte{terminator})
	}

	var prev1, prev2 []byte
	warned1, warned2 := false, false
	seenUnpairable := false
	halt := false
	// checkLine mirrors GNU's get_line: the whole line is compared with the
	// previous one, but only when order checking is active at read time.
	checkLine := func(rec joinRecord, prev *[]byte, fileNum int, warned *bool, active bool) {
		if active && *prev != nil && bytes.Compare(*prev, rec.raw) > 0 {
			if checkOrder {
				fatalf("join", "%s:%d: is not sorted: %s", operands[fileNum-1], rec.line, string(rec.raw))
				halt = true
				return
			}
			if !*warned {
				fatalf("join", "%s:%d: is not sorted: %s", operands[fileNum-1], rec.line, string(rec.raw))
				*warned = true
			}
		}
		*prev = append((*prev)[:0], rec.raw...)
	}
	active := func() bool {
		return !noCheckOrder && (checkOrder || seenUnpairable)
	}

	i1, i2 := 0, 0
	if header {
		if len(records1) > 0 && len(records2) > 0 {
			writeFormatted(records1[0].key, &records1[0], &records2[0])
		}
		// The header is not part of the compared sequence.
		prev1, prev2 = nil, nil
		i1, i2 = 1, 1
	}
	if i1 < len(records1) {
		checkLine(records1[i1], &prev1, 1, &warned1, active())
		if halt {
			return 1
		}
	}
	if i2 < len(records2) {
		checkLine(records2[i2], &prev2, 2, &warned2, active())
		if halt {
			return 1
		}
	}
	for i1 < len(records1) && i2 < len(records2) {
		key1 := records1[i1].key
		key2 := records2[i2].key
		cmp := joinKeyCompare(key1, key2, caseInsensitive)
		switch {
		case cmp == 0:
			// Consecutive lines with the same key form a group; every pair
			// of one line from each group is printed, like the original.
			// Each line read here is also order-checked, including the
			// first one past the group that becomes the next current line.
			end1 := i1 + 1
			for end1 < len(records1) {
				checkLine(records1[end1], &prev1, 1, &warned1, active())
				if halt {
					return 1
				}
				if joinKeyCompare(records1[end1].key, key1, caseInsensitive) != 0 {
					break
				}
				end1++
			}
			end2 := i2 + 1
			for end2 < len(records2) {
				checkLine(records2[end2], &prev2, 2, &warned2, active())
				if halt {
					return 1
				}
				if joinKeyCompare(records2[end2].key, key2, caseInsensitive) != 0 {
					break
				}
				end2++
			}
			if !only1 && !only2 {
				for a := i1; a < end1; a++ {
					for b := i2; b < end2; b++ {
						writeFormatted(key1, &records1[a], &records2[b])
					}
				}
			}
			i1, i2 = end1, end2
		case cmp < 0:
			if only1 || with1 {
				writeFormatted(key1, &records1[i1], nil)
			}
			i1++
			// The line after an unpairable one is read (and checked) before
			// the unpairable is known, so its check sees the old flag.
			if i1 < len(records1) {
				checkLine(records1[i1], &prev1, 1, &warned1, active())
				if halt {
					return 1
				}
			}
			seenUnpairable = true
		default:
			if only2 || with2 {
				writeFormatted(key2, nil, &records2[i2])
			}
			i2++
			if i2 < len(records2) {
				checkLine(records2[i2], &prev2, 2, &warned2, active())
				if halt {
					return 1
				}
			}
			seenUnpairable = true
		}
	}
	// The tails: lines the merge never reached still count for order checks,
	// and -a/-v prints the unpairable ones.
	for ; i1 < len(records1); i1++ {
		checkLine(records1[i1], &prev1, 1, &warned1, active())
		if halt {
			return 1
		}
		if only1 || with1 {
			writeFormatted(records1[i1].key, &records1[i1], nil)
		} else if warned1 {
			break
		}
	}
	for ; i2 < len(records2); i2++ {
		checkLine(records2[i2], &prev2, 2, &warned2, active())
		if halt {
			return 1
		}
		if only2 || with2 {
			writeFormatted(records2[i2].key, nil, &records2[i2])
		} else if warned2 {
			break
		}
	}
	if err := out.Flush(); err != nil {
		fatalf("join", "write error: %v", err)
		return 1
	}
	if warned1 || warned2 {
		fatalf("join", "input is not in sorted order")
		return 1
	}
	return 0
}

func joinKeyCompare(a, b []byte, insensitive bool) int {
	if insensitive {
		return bytes.Compare(bytes.ToLower(a), bytes.ToLower(b))
	}
	return bytes.Compare(a, b)
}

// joinRecord is one parsed input line: the key fields for file 1 and 2
// (field N of each), the remaining fields, and the raw line for reporting.
type joinRecord struct {
	key    []byte
	fields [][]byte
	rest   [][]byte
	raw    []byte
	line   int
}

// splitJoinRecords splits data into join records on the terminator byte. With
// sep set, fields split on that byte; otherwise on runs of blanks with
// leading blanks skipped.
func splitJoinRecords(data []byte, terminator, sep byte) []joinRecord {
	var records []joinRecord
	lines := bytes.Split(data, []byte{terminator})
	number := 0
	for i, raw := range lines {
		if i == len(lines)-1 && len(raw) == 0 {
			break
		}
		number++
		var fields [][]byte
		if sep != 0 {
			fields = append(fields, bytes.Split(raw, []byte{sep})...)
		} else {
			rest := bytes.TrimLeft(raw, " \t")
			for len(rest) > 0 {
				end := bytes.IndexAny(rest, " \t")
				if end < 0 {
					fields = append(fields, rest)
					break
				}
				fields = append(fields, rest[:end])
				rest = bytes.TrimLeft(rest[end:], " \t")
			}
		}
		records = append(records, joinRecord{fields: fields, raw: raw, line: number})
	}
	return records
}

// parseJoinFormat parses a -o FORMAT list of "0", "N" or "N.M" specs.
func parseJoinFormat(spec string) ([]joinFieldSpec, bool) {
	var out []joinFieldSpec
	for _, part := range strings.Split(spec, ",") {
		if part == "" {
			fatalf("join", "invalid field specifier: %q", spec)
			return nil, false
		}
		if part == "0" {
			out = append(out, joinFieldSpec{file: 0, field: 0})
			continue
		}
		dot := strings.IndexByte(part, '.')
		if dot < 1 || dot == len(part)-1 {
			fatalf("join", "invalid field specifier: %q", part)
			return nil, false
		}
		file, err := strconv.Atoi(part[:dot])
		field, err2 := strconv.Atoi(part[dot+1:])
		if err != nil || err2 != nil || file < 1 || file > 2 || field < 1 {
			fatalf("join", "invalid field specifier: %q", part)
			return nil, false
		}
		out = append(out, joinFieldSpec{file: file, field: field})
	}
	return out, true
}
