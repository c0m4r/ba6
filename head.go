// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"errors"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// cmdHead implements a subset of head(1): -n N (first N lines) and -c N (first
// N bytes). A leading '-' on the count (e.g. -n -5) is not supported; use the
// common positive form. With multiple files, each is preceded by a header
// (unless -q), matching coreutils.
func cmdHead(args []string) int {
	count := int64(10)
	byMode := false // true => -c bytes, false => -n lines
	var files []string
	quiet := false
	verbose := false

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-q" || a == "--quiet" || a == "--silent":
			quiet = true
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "-n" || a == "-c":
			byMode = a == "-c"
			if i+1 >= len(args) {
				fatalf("head", "option requires an argument -- '%c'", a[1])
				return 1
			}
			i++
			v, err := parseCount(args[i])
			if err != nil {
				fatalf("head", "invalid number: %q", args[i])
				return 1
			}
			count = v
		case strings.HasPrefix(a, "-n"):
			v, err := parseCount(a[2:])
			if err != nil {
				fatalf("head", "invalid number: %q", a[2:])
				return 1
			}
			count, byMode = v, false
		case strings.HasPrefix(a, "-c"):
			v, err := parseCount(a[2:])
			if err != nil {
				fatalf("head", "invalid number: %q", a[2:])
				return 1
			}
			count, byMode = v, true
		case len(a) > 1 && a[0] == '-':
			fatalf("head", "invalid option -- %q", a)
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

	showHeaders := (len(files) > 1 || verbose) && !quiet
	out := bufio.NewWriter(os.Stdout)

	status := 0
	for idx, fname := range files {
		f, err := openInput(fname)
		if err != nil {
			fatalf("head", "cannot open '%s' for reading: %v", fname, err)
			status = 1
			continue
		}
		if showHeaders {
			if idx > 0 {
				_ = out.WriteByte('\n') // Flush reports the sticky error.
			}
			name := fname
			if name == "-" {
				name = "standard input"
			}
			_, _ = out.WriteString("==> " + name + " <==\n") // Flush reports the sticky error.
		}
		if byMode {
			if _, copyErr := io.CopyN(out, f, count); copyErr != nil && !errors.Is(copyErr, io.EOF) {
				fatalf("head", "%s: %v", fname, copyErr)
				status = 1
			}
		} else {
			if readErr := emitFirstLines(out, f, count); readErr != nil {
				fatalf("head", "%s: %v", fname, readErr)
				status = 1
			}
		}
		if closeErr := f.Close(); closeErr != nil {
			fatalf("head", "%s: %v", fname, closeErr)
			status = 1
		}
	}
	if err := out.Flush(); err != nil {
		fatalf("head", "write error: %v", err)
		status = 1
	}
	return status
}

// emitFirstLines writes the first n lines from r to w.
func emitFirstLines(w *bufio.Writer, r io.Reader, n int64) error {
	br := bufio.NewReader(r)
	for written := int64(0); written < n; written++ {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if _, writeErr := w.WriteString(line); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	return nil
}

// parseCount parses a nonnegative numeric count with the supported one-byte
// suffixes: b=512 and k/K, m/M, g/G as binary powers of 1024.
func parseCount(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, strconv.ErrSyntax
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "k") || strings.HasSuffix(s, "K"):
		mult, s = 1024, s[:len(s)-1]
	case strings.HasSuffix(s, "m") || strings.HasSuffix(s, "M"):
		mult, s = 1024*1024, s[:len(s)-1]
	case strings.HasSuffix(s, "g") || strings.HasSuffix(s, "G"):
		mult, s = 1024*1024*1024, s[:len(s)-1]
	case strings.HasSuffix(s, "b"):
		mult, s = 512, s[:len(s)-1]
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 || v > math.MaxInt64/mult {
		return 0, strconv.ErrRange
	}
	return v * mult, nil
}
