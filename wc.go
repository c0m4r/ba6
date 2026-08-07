// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"unicode"
)

// cmdWc implements wc(1): count lines (-l), words (-w), bytes (-c), and
// characters (-m). With no count flags it prints lines, words, and bytes.
func cmdWc(args []string) int {
	var wantLines, wantWords, wantBytes, wantChars bool
	var files []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "--lines":
			wantLines = true
		case a == "--words":
			wantWords = true
		case a == "--bytes":
			wantBytes = true
		case a == "--chars":
			wantChars = true
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				switch c {
				case 'l':
					wantLines = true
				case 'w':
					wantWords = true
				case 'c':
					wantBytes = true
				case 'm':
					wantChars = true
				default:
					fatalf("wc", "invalid option -- '%c'", c)
					return 1
				}
			}
		default:
			files = append(files, a)
		}
	}
rest:
	files = append(files, args[i:]...)

	if !wantLines && !wantWords && !wantBytes && !wantChars {
		wantLines, wantWords, wantBytes = true, true, true
	}
	sel := wcSel{wantLines, wantWords, wantBytes, wantChars}

	fromStdin := len(files) == 0
	if fromStdin {
		files = []string{"-"}
	}
	width := wcWidth(files, sel.count())

	out := bufio.NewWriter(os.Stdout)

	var total wcCount
	status := 0
	for _, f := range files {
		r, err := openInput(f)
		if err != nil {
			fatalf("wc", "%s: %s", f, errText(err))
			status = 1
			continue
		}
		cnt, readErr := countWc(r)
		if readErr != nil {
			fatalf("wc", "%s: %s", f, errText(readErr))
			status = 1
		}
		if closeErr := r.Close(); closeErr != nil {
			fatalf("wc", "%s: %s", f, errText(closeErr))
			status = 1
		}
		total.add(cnt)

		name := f
		if fromStdin {
			name = ""
		}
		writeWcLine(out, cnt, sel, name, width)
	}

	if len(files) > 1 {
		writeWcLine(out, total, sel, "total", width)
	}
	if err := out.Flush(); err != nil {
		fatalf("wc", "write error: %s", errText(err))
		status = 1
	}
	return status
}

type wcSel struct{ lines, words, bytes, chars bool }

// count reports how many columns this selection prints.
func (s wcSel) count() int {
	n := 0
	for _, selected := range []bool{s.lines, s.words, s.bytes, s.chars} {
		if selected {
			n++
		}
	}
	return n
}

// wcWidth returns the width every count column is padded to. wc sizes the
// columns from how large the counts could possibly be -- the number of bytes it
// is about to read -- not from the counts it ends up with, so the width is known
// before any counting happens. A single count for a single input is printed
// without padding, and an input whose size cannot be known in advance falls back
// to seven.
func wcWidth(files []string, fields int) int {
	if fields == 1 && len(files) <= 1 {
		return 1
	}
	total := int64(0)
	for _, name := range files {
		info, err := os.Stat(name)
		if name == "-" {
			// Standard input redirected from a file still has a known size.
			info, err = os.Stdin.Stat()
		}
		if err != nil || !info.Mode().IsRegular() {
			return 7
		}
		total += info.Size()
	}
	return len(strconv.FormatInt(total, 10))
}

type wcCount struct {
	lines, words, bytes, chars int64
}

func (c *wcCount) add(o wcCount) {
	c.lines += o.lines
	c.words += o.words
	c.bytes += o.bytes
	c.chars += o.chars
}

func countWc(r io.Reader) (wcCount, error) {
	var c wcCount
	br := bufio.NewReaderSize(r, 64*1024)
	inWord := false
	for {
		r, size, err := br.ReadRune()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return c, nil
			}
			return c, err
		}
		c.bytes += int64(size)
		c.chars++
		if r == '\n' {
			c.lines++
		}
		if unicode.IsSpace(r) {
			inWord = false
		} else if !inWord {
			inWord = true
			c.words++
		}
	}
}

func writeWcLine(out *bufio.Writer, c wcCount, sel wcSel, name string, width int) {
	var parts []int64
	if sel.lines {
		parts = append(parts, c.lines)
	}
	if sel.words {
		parts = append(parts, c.words)
	}
	if sel.bytes {
		parts = append(parts, c.bytes)
	}
	if sel.chars {
		parts = append(parts, c.chars)
	}
	for i, p := range parts {
		if i > 0 {
			_ = out.WriteByte(' ') // Flush reports the sticky error.
		}
		fmt.Fprintf(out, "%*d", width, p)
	}
	if name != "" {
		fmt.Fprintf(out, " %s", name)
	}
	_ = out.WriteByte('\n') // Flush reports the sticky error.
}
