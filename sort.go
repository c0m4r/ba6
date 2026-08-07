// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// cmdSort implements a subset of sort(1): lexical sort by default, with -n
// (numeric), -r (reverse), -u (unique), -f (fold case), -b (ignore leading
// blanks), and -k/-t for field-based keys is intentionally omitted to keep the
// implementation compact. Reads all input into memory.
func cmdSort(args []string) int {
	var (
		numeric bool
		reverse bool
		unique  bool
		fold    bool
		ignoreB bool
		check   bool
		files   []string
	)

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "--numeric-sort":
			numeric = true
		case a == "--reverse":
			reverse = true
		case a == "--unique":
			unique = true
		case a == "--ignore-case":
			fold = true
		case a == "--check":
			check = true
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				switch c {
				case 'n':
					numeric = true
				case 'r':
					reverse = true
				case 'u':
					unique = true
				case 'f':
					fold = true
				case 'b':
					ignoreB = true
				case 'c':
					check = true
				default:
					fatalf("sort", "invalid option -- '%c'", c)
					return 2
				}
			}
		default:
			files = append(files, a)
		}
	}
rest:
	files = append(files, args[i:]...)
	if len(files) == 0 {
		files = []string{"-"}
	}

	var lines []string
	status := 0
	for _, f := range files {
		r, err := openInput(f)
		if err != nil {
			fatalf("sort", "cannot read: %s: %s", f, errText(err))
			status = 2
			continue
		}
		sc := newLineScanner(r)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		if scanErr("sort", f, sc) {
			status = 2
		}
		if closeErr := r.Close(); closeErr != nil {
			fatalf("sort", "%s: %s", f, errText(closeErr))
			status = 2
		}
	}

	key := func(s string) string {
		if ignoreB {
			s = strings.TrimLeft(s, " \t")
		}
		if fold {
			s = strings.ToLower(s)
		}
		return s
	}

	compare := func(aLine, bLine string) int {
		a, b := key(aLine), key(bLine)
		if numeric {
			fa, fb := leadingNumber(a), leadingNumber(b)
			if fa < fb {
				return -1
			}
			if fa > fb {
				return 1
			}
		}
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
	less := func(i, j int) bool {
		return compare(lines[i], lines[j]) < 0
	}

	if check {
		for k := 1; k < len(lines); k++ {
			disordered := less(k, k-1)
			if reverse {
				disordered = less(k-1, k)
			}
			if disordered {
				fatalf("sort", "-c: disorder at line %d: %s", k+1, lines[k])
				return 1
			}
		}
		return status
	}

	sort.SliceStable(lines, less)
	if reverse {
		for a, b := 0, len(lines)-1; a < b; a, b = a+1, b-1 {
			lines[a], lines[b] = lines[b], lines[a]
		}
	}

	out := bufio.NewWriter(os.Stdout)
	var prev string
	havePrev := false
	for _, ln := range lines {
		equal := key(ln) == key(prev)
		if numeric {
			equal = leadingNumber(key(ln)) == leadingNumber(key(prev))
		}
		if unique && havePrev && equal {
			continue
		}
		fmt.Fprintln(out, ln)
		prev, havePrev = ln, true
	}
	if err := out.Flush(); err != nil {
		fatalf("sort", "write error: %s", errText(err))
		return 1
	}
	return status
}

// leadingNumber parses a leading (optionally signed/decimal) number from s,
// returning 0 if there is none. Used for numeric sort comparisons.
func leadingNumber(s string) float64 {
	s = strings.TrimLeft(s, " \t")
	end := 0
	seenDot := false
	for end < len(s) {
		c := s[end]
		if c >= '0' && c <= '9' {
			end++
		} else if c == '-' && end == 0 {
			end++
		} else if c == '.' && !seenDot {
			seenDot = true
			end++
		} else {
			break
		}
	}
	var f float64
	if end == 0 {
		return 0
	}
	if _, err := fmt.Sscanf(s[:end], "%g", &f); err != nil {
		return 0
	}
	return f
}
