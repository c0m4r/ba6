// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// cmdUniq implements uniq(1): collapse adjacent duplicate lines. Flags: -c
// (prefix count), -d (only duplicated lines), -u (only unique lines), -i
// (ignore case). Operates on adjacent runs, like the real uniq.
func cmdUniq(args []string) int {
	var (
		count      bool
		onlyDup    bool
		onlyUniq   bool
		ignoreCase bool
		operands   []string
	)

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
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				switch c {
				case 'c':
					count = true
				case 'd':
					onlyDup = true
				case 'u':
					onlyUniq = true
				case 'i':
					ignoreCase = true
				default:
					fatalf("uniq", "invalid option -- '%c'", c)
					return 1
				}
			}
		default:
			operands = append(operands, a)
		}
	}
rest:
	operands = append(operands, args[i:]...)

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

	key := func(s string) string {
		if ignoreCase {
			return strings.ToLower(s)
		}
		return s
	}

	emit := func(line string, n int) {
		isDup := n > 1
		if onlyDup && !isDup {
			return
		}
		if onlyUniq && isDup {
			return
		}
		if count {
			fmt.Fprintf(out, "%7d %s\n", n, line)
		} else {
			fmt.Fprintln(out, line)
		}
	}

	sc := newLineScanner(in)
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
		emit(cur, n)
		cur, curKey, n = line, k, 1
	}
	if n > 0 {
		emit(cur, n)
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
