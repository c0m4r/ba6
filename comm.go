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

// cmdComm implements comm(1): compare two sorted files line by line. Column 1
// holds lines only in the first file, column 2 only in the second, column 3
// common lines; -1/-2/-3 suppress columns, -z selects the NUL record
// terminator, and --total reports the counts on standard error. Input that is
// not sorted is still processed and reported, like the original.
func cmdComm(args []string) int {
	args = expandShortOptions(args, "")
	show1, show2, show3 := true, true, true
	zero := false
	total := false
	checkOrder := false
	noCheckOrder := false
	var files []string
	parsing := true
	for _, a := range args {
		switch {
		case parsing && a == "--":
			parsing = false
		case parsing && (a == "--check-order"):
			checkOrder = true
		case parsing && (a == "--nocheck-order"):
			noCheckOrder = true
		case parsing && a == "-1":
			show1 = false
		case parsing && a == "-2":
			show2 = false
		case parsing && a == "-3":
			show3 = false
		case parsing && (a == "-z" || a == "--zero-terminated"):
			zero = true
		case parsing && a == "--total":
			total = true
		case parsing && len(a) > 1 && a[0] == '-' && a != "-":
			fatalf("comm", "invalid option %q", a)
			return 1
		default:
			files = append(files, a)
		}
	}
	if len(files) != 2 {
		fatalf("comm", "missing operand")
		return 1
	}
	sep := byte('\n')
	if zero {
		sep = 0
	}

	var sides [2]commInput
	disordered := false
	halt := false
	for i := range sides {
		sides[i].number = i + 1
		if files[i] == "-" {
			sides[i].reader = bufio.NewReader(os.Stdin)
			sides[i].name = "standard input"
		} else {
			file, err := os.Open(files[i])
			if err != nil {
				fatalf("comm", "%s: %s", files[i], errText(err))
				return 1
			}
			defer file.Close()
			sides[i].reader = bufio.NewReader(file)
			sides[i].name = files[i]
		}
		sides[i].advance(sep, checkOrder, checkOrder, &disordered, &halt)
		if halt {
			return 1
		}
	}
	seenUnpairable := false
	checking := func() bool {
		return !noCheckOrder && (checkOrder || seenUnpairable)
	}

	out := os.Stdout
	count1, count2, count3 := 0, 0, 0
	for sides[0].has && sides[1].has {
		cmp := strings.Compare(string(sides[0].line), string(sides[1].line))
		switch {
		case cmp == 0:
			count3++
			if show3 {
				if show1 {
					_, _ = out.WriteString("\t")
				}
				if show2 {
					_, _ = out.WriteString("\t")
				}
				_, _ = out.Write(sides[0].line)
				_, _ = out.Write([]byte{sep})
			}
			sides[0].advance(sep, checking(), checkOrder, &disordered, &halt)
			if halt {
				return 1
			}
			sides[1].advance(sep, checking(), checkOrder, &disordered, &halt)
			if halt {
				return 1
			}
		case cmp < 0:
			count1++
			seenUnpairable = true
			if show1 {
				_, _ = out.Write(sides[0].line)
				_, _ = out.Write([]byte{sep})
			}
			sides[0].advance(sep, checking(), checkOrder, &disordered, &halt)
			if halt {
				return 1
			}
		default:
			count2++
			seenUnpairable = true
			if show2 {
				if show1 {
					_, _ = out.WriteString("\t")
				}
				_, _ = out.Write(sides[1].line)
				_, _ = out.Write([]byte{sep})
			}
			sides[1].advance(sep, checking(), checkOrder, &disordered, &halt)
			if halt {
				return 1
			}
		}
	}
	for ; sides[0].has; sides[0].advance(sep, checking(), checkOrder, &disordered, &halt) {
		if halt {
			return 1
		}
		seenUnpairable = true
		count1++
		if show1 {
			_, _ = out.Write(sides[0].line)
			_, _ = out.Write([]byte{sep})
		}
	}
	for ; sides[1].has; sides[1].advance(sep, checking(), checkOrder, &disordered, &halt) {
		if halt {
			return 1
		}
		seenUnpairable = true
		count2++
		if show2 {
			if show1 {
				_, _ = out.WriteString("\t")
			}
			_, _ = out.Write(sides[1].line)
			_, _ = out.Write([]byte{sep})
		}
	}
	if total {
		fmt.Fprintf(out, "%d\t%d\t%d\ttotal\n", count1, count2, count3)
	}
	if disordered {
		fatalf("comm", "input is not in sorted order")
		return 1
	}
	return 0
}

// commInput is one of the two compared streams, with the line the merge
// currently considers and enough of the previous one to detect disorder.
type commInput struct {
	reader   *bufio.Reader
	name     string
	number   int
	line     []byte
	has      bool
	last     []byte
	reported bool
}

// advance reads the next record. When order checking is active (always with
// --check-order, from the first unpairable line otherwise), a record that
// sorts before the previous one is still consumed; it is reported once per
// file as a warning, or halts processing with --check-order.
func (in *commInput) advance(sep byte, check, fatal bool, disordered, halt *bool) {
	data, err := in.reader.ReadBytes(sep)
	if err != nil && (err != io.EOF || len(data) == 0) {
		in.has = false
		return
	}
	if len(data) > 0 && data[len(data)-1] == sep {
		data = data[:len(data)-1]
	}
	// The bufio.Reader reuses its buffer between calls, so the record must be
	// copied before the next read overwrites it.
	record := append([]byte(nil), data...)
	if check && in.last != nil && strings.Compare(string(in.last), string(record)) > 0 {
		if fatal {
			fatalf("comm", "file %d is not in sorted order", in.number)
			*halt = true
			return
		}
		if !in.reported {
			*disordered = true
			fatalf("comm", "file %d is not in sorted order", in.number)
			in.reported = true
		}
	}
	in.last = record
	in.line = record
	in.has = true
}
