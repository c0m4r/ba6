// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// cmdUniq implements uniq(1): collapse adjacent duplicate lines. Flags: -c
// (prefix count), -d (only duplicated lines), -u (only unique lines), -i
// (ignore case), -f/-s/-w (skip fields, skip chars, compare at most N chars),
// -D/--all-repeated (print every line of duplicated groups), --group (blank
// lines around groups), -z (NUL-delimited). Operates on adjacent runs, like
// the real uniq.
func cmdUniq(args []string) int {
	var (
		count      bool
		onlyDup    bool
		onlyUniq   bool
		ignoreCase bool
		zeroTerm   bool
		skipFields int
		skipChars  int
		checkChars int
		allRepeat  string
		groupMode  string
		operands   []string
	)
	checkChars = -1

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "--count":
			count = true
		case a == "--repeated":
			onlyDup = true
		case a == "--unique":
			onlyUniq = true
		case a == "--ignore-case":
			ignoreCase = true
		case a == "--zero-terminated":
			zeroTerm = true
		case a == "--all-repeated":
			allRepeat = "none"
		case strings.HasPrefix(a, "--all-repeated="):
			allRepeat = strings.TrimPrefix(a, "--all-repeated=")
		case a == "--group":
			groupMode = "separate"
		case strings.HasPrefix(a, "--group="):
			groupMode = strings.TrimPrefix(a, "--group=")
		case strings.HasPrefix(a, "--skip-fields="):
			if n, ok := uniqNumber("fields to skip", strings.TrimPrefix(a, "--skip-fields=")); !ok {
				return 1
			} else {
				skipFields = n
			}
		case strings.HasPrefix(a, "--skip-chars="):
			if n, ok := uniqNumber("bytes to skip", strings.TrimPrefix(a, "--skip-chars=")); !ok {
				return 1
			} else {
				skipChars = n
			}
		case strings.HasPrefix(a, "--check-chars="):
			if n, ok := uniqNumber("bytes to compare", strings.TrimPrefix(a, "--check-chars=")); !ok {
				return 1
			} else {
				checkChars = n
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'c':
					count = true
				case 'd':
					onlyDup = true
				case 'u':
					onlyUniq = true
				case 'i':
					ignoreCase = true
				case 'z':
					zeroTerm = true
				case 'D':
					allRepeat = "none"
				case 'f', 's', 'w':
					text := a[j+1:]
					if text == "" {
						i++
						if i >= len(args) {
							fatalf("uniq", "option requires an argument -- '%c'", a[j])
							return 1
						}
						text = args[i]
					}
					what := map[byte]string{'f': "fields to skip", 's': "bytes to skip", 'w': "bytes to compare"}[a[j]]
					n, ok := uniqNumber(what, text)
					if !ok {
						return 1
					}
					switch a[j] {
					case 'f':
						skipFields = n
					case 's':
						skipChars = n
					case 'w':
						checkChars = n
					}
					j = len(a)
				default:
					fatalf("uniq", "invalid option -- '%c'", a[j])
					return 1
				}
			}
		default:
			operands = append(operands, a)
		}
	}
rest:
	operands = append(operands, args[i:]...)

	if allRepeat != "" && count {
		fatalf("uniq", "printing all duplicated lines and repeat counts is meaningless")
		return 1
	}
	if groupMode != "" && (count || onlyDup || onlyUniq || allRepeat != "") {
		fatalf("uniq", "--group is mutually exclusive with -c/-d/-D/-u")
		return 1
	}
	if allRepeat != "" && !uniqMethod(allRepeat, "all-repeated") {
		return 1
	}
	if groupMode != "" && !uniqMethod(groupMode, "group") {
		return 1
	}

	inName, outName := "-", ""
	if len(operands) >= 1 {
		inName = operands[0]
	}
	if len(operands) >= 2 {
		outName = operands[1]
	}
	if len(operands) > 2 {
		fatalf("uniq", "extra operand %q", operands[2])
		return 1
	}

	in, err := openInput(inName)
	if err != nil {
		fatalf("uniq", "%s: %v", inName, err)
		return 1
	}
	defer in.Close()

	var out *bufio.Writer
	var outFile *os.File
	if outName != "" {
		if inFile, ok := in.(*os.File); ok {
			inInfo, inErr := inFile.Stat()
			outInfo, outErr := os.Stat(outName)
			if inErr == nil && outErr == nil && os.SameFile(inInfo, outInfo) {
				fatalf("uniq", "%s: input and output files are the same", outName)
				return 1
			}
		}
		f, err := os.Create(outName)
		if err != nil {
			fatalf("uniq", "%s: %v", outName, err)
			return 1
		}
		outFile = f
		out = bufio.NewWriter(f)
	} else {
		out = bufio.NewWriter(os.Stdout)
	}

	term := "\n"
	if zeroTerm {
		term = "\x00"
	}

	key := func(s string) string {
		k := s
		if skipFields > 0 {
			k = uniqSkipFields(k, skipFields)
		}
		if skipChars > 0 {
			if len(k) > skipChars {
				k = k[skipChars:]
			} else {
				k = ""
			}
		}
		if checkChars >= 0 && len(k) > checkChars {
			k = k[:checkChars]
		}
		if ignoreCase {
			return strings.ToLower(k)
		}
		return k
	}

	// In the plain mode one representative line stands for the whole group;
	// -D and --group print every member instead. more tells a group whether
	// another group follows, which decides the "separate" blank line.
	printedAny := false
	emit := func(line string, n int, more bool) {
		if allRepeat != "" && n <= 1 {
			return
		}
		printIt := true
		if allRepeat == "" && groupMode == "" {
			isDup := n > 1
			if onlyDup && !isDup {
				printIt = false
			}
			if onlyUniq && isDup {
				printIt = false
			}
		} else if allRepeat != "" && onlyUniq {
			n = 1
		}
		if !printIt {
			return
		}
		method := groupMode
		if allRepeat != "" {
			method = allRepeat
		}
		// prepend: blank before every group; append: after every group;
		// separate: between groups only; both: before the first, between, and
		// after the last.
		sepBefore := method == "prepend" || (method == "both" && !printedAny)
		sepAfter := method == "append" || method == "both" || (method == "separate" && more)
		if sepBefore {
			if _, err := out.WriteString(term); err != nil {
				fatalf("uniq", "write error: %v", err)
			}
		}
		if count {
			fmt.Fprintf(out, "%7d %s%s", n, line, term)
		} else if allRepeat != "" || groupMode != "" {
			for j := 0; j < n; j++ {
				if _, err := out.WriteString(line + term); err != nil {
					fatalf("uniq", "write error: %v", err)
				}
			}
		} else {
			if _, err := out.WriteString(line + term); err != nil {
				fatalf("uniq", "write error: %v", err)
			}
		}
		if sepAfter {
			if _, err := out.WriteString(term); err != nil {
				fatalf("uniq", "write error: %v", err)
			}
		}
		printedAny = true
	}

	sc := newLineScanner(in)
	if zeroTerm {
		sc = newNulScanner(in)
	}
	var cur string
	var curKey string
	n := 0
	for sc.Scan() {
		line := sc.Text()
		k := key(line)
		if n == 0 {
			cur, curKey, n = line, k, 1
			continue
		}
		if k == curKey {
			n++
			continue
		}
		emit(cur, n, true)
		cur, curKey, n = line, k, 1
	}
	if n > 0 {
		emit(cur, n, false)
	}
	status := 0
	if scanErr("uniq", inName, sc) {
		status = 1
	}
	if err := out.Flush(); err != nil {
		fatalf("uniq", "write error: %v", err)
		status = 1
	}
	if outFile != nil {
		if err := outFile.Close(); err != nil {
			fatalf("uniq", "%s: %v", outName, err)
			status = 1
		}
	}
	if err := in.Close(); err != nil {
		fatalf("uniq", "%s: %v", inName, err)
		status = 1
	}
	return status
}

// uniqNumber parses a non-negative skip/compare count, reporting the
// original's wording on failure.
func uniqNumber(what, text string) (int, bool) {
	n, err := strconv.Atoi(text)
	if err != nil || n < 0 {
		fatalf("uniq", "invalid number of %s: %q", what, text)
		return 0, false
	}
	return n, true
}

// uniqMethod validates a --group/--all-repeated method argument.
func uniqMethod(method, option string) bool {
	switch method {
	case "none":
		return option == "all-repeated"
	case "prepend", "separate":
		return true
	case "append", "both":
		return option == "group"
	}
	fatalf("uniq", "invalid argument %q for %q", method, "--"+option)
	return false
}

// uniqSkipFields advances past n whitespace-separated fields the way GNU
// uniq's skipfield does: leading blanks, then the field's non-blank run, and
// the blanks that follow belong to the compared remainder.
func uniqSkipFields(s string, n int) string {
	i := 0
	for ; n > 0 && i < len(s); n-- {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		for i < len(s) && s[i] != ' ' && s[i] != '\t' {
			i++
		}
	}
	return s[i:]
}

// newNulScanner returns a line scanner that splits on NUL bytes instead of
// newlines, for -z.
func newNulScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxScanLine)
	sc.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		for i, b := range data {
			if b == 0 {
				return i + 1, data[:i], nil
			}
		}
		if !atEOF || len(data) == 0 {
			return 0, nil, nil
		}
		return len(data), data, nil
	})
	return sc
}
