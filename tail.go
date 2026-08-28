// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// cmdTail implements a subset of tail(1): -n N (last N lines), -n +N (lines
// starting at line N), -c N (last N bytes), and -f (follow appended data).
// Multiple files get coreutils-style headers unless -q is given.
func cmdTail(args []string) int {
	args = expandShortOptions(args, "nc")
	count := int64(10)
	byMode := false // true => -c bytes
	fromStart := false
	follow := false
	quiet := false
	verbose := false
	var files []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-f" || a == "--follow":
			follow = true
		case a == "-q" || a == "--quiet" || a == "--silent":
			quiet = true
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "-n" || a == "-c":
			byMode = a == "-c"
			if i+1 >= len(args) {
				fatalf("tail", "option requires an argument -- '%c'", a[1])
				return 1
			}
			i++
			c, fs, err := parseTailCount(args[i])
			if err != nil {
				fatalf("tail", "invalid number: %q", args[i])
				return 1
			}
			count, fromStart = c, fs
		case strings.HasPrefix(a, "-n"):
			c, fs, err := parseTailCount(a[2:])
			if err != nil {
				fatalf("tail", "invalid number: %q", a[2:])
				return 1
			}
			count, fromStart, byMode = c, fs, false
		case strings.HasPrefix(a, "-c"):
			c, fs, err := parseTailCount(a[2:])
			if err != nil {
				fatalf("tail", "invalid number: %q", a[2:])
				return 1
			}
			count, fromStart, byMode = c, fs, true
		case len(a) > 1 && a[0] == '-':
			fatalf("tail", "invalid option -- %q", a)
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
	if follow && len(files) > 1 {
		fatalf("tail", "following multiple files is not supported")
		return 1
	}

	showHeaders := (len(files) > 1 || verbose) && !quiet
	out := bufio.NewWriter(os.Stdout)

	status := 0
	for idx, fname := range files {
		f, err := openInput(fname)
		if err != nil {
			fatalf("tail", "cannot open '%s' for reading: %s", fname, errText(err))
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
			if readErr := emitLastBytes(out, f, count, fromStart); readErr != nil {
				fatalf("tail", "error reading '%s': %s", fname, errText(readErr))
				status = 1
			}
		} else {
			if readErr := emitLastLines(out, f, count, fromStart); readErr != nil {
				fatalf("tail", "error reading '%s': %s", fname, errText(readErr))
				status = 1
			}
		}
		if flushErr := out.Flush(); flushErr != nil {
			fatalf("tail", "write error: %s", errText(flushErr))
			_ = f.Close()
			return 1
		}

		// -f: keep following the last named file (coreutils follows all, but
		// following a single growing file is the common case and simplest).
		if follow && fname != "-" {
			if osf, ok := f.(*os.File); ok && idx == len(files)-1 {
				// followFile only returns once it hits an error; it never
				// completes with a nil error, so there is nothing to guard here.
				followErr := followFile(out, osf)
				fatalf("tail", "error reading '%s': %s", fname, errText(followErr))
				status = 1
			}
		}
		if closeErr := f.Close(); closeErr != nil {
			fatalf("tail", "error reading '%s': %s", fname, errText(closeErr))
			status = 1
		}
	}
	if err := out.Flush(); err != nil {
		fatalf("tail", "write error: %s", errText(err))
		status = 1
	}
	return status
}

// emitLastLines writes the trailing n lines of r. If fromStart, it writes from
// line n onward (1-indexed). Uses a ring buffer so streams need no seeking.
func emitLastLines(w *bufio.Writer, r io.Reader, n int64, fromStart bool) error {
	br := bufio.NewReader(r)
	if fromStart {
		lineNo := int64(1)
		for {
			line, err := br.ReadString('\n')
			if lineNo >= n && len(line) > 0 {
				if _, writeErr := w.WriteString(line); writeErr != nil {
					return writeErr
				}
			}
			lineNo++
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	}
	if n <= 0 {
		// Drain so a pipe doesn't get SIGPIPE surprises; emit nothing.
		_, err := io.Copy(io.Discard, br)
		return err
	}
	// Grow the ring lazily so a huge -n (e.g. tail -n 1e18) bounds memory by
	// the actual line count rather than panicking on make([]string, n).
	var ring []string
	head := 0 // index of the oldest retained line once the ring is full
	full := false
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if !full {
				ring = append(ring, line)
				if int64(len(ring)) == n {
					full = true
					head = 0
				}
			} else {
				ring[head] = line
				head = (head + 1) % len(ring)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			break
		}
	}
	size := len(ring)
	for k := 0; k < size; k++ {
		if _, err := w.WriteString(ring[(head+k)%size]); err != nil {
			return err
		}
	}
	return nil
}

// emitLastBytes writes the trailing n bytes of r (or from byte n if fromStart).
func emitLastBytes(w *bufio.Writer, r io.Reader, n int64, fromStart bool) error {
	if fromStart {
		br := bufio.NewReader(r)
		skip := n - 1
		if skip > 0 {
			if _, err := io.CopyN(io.Discard, br, skip); err != nil && !errors.Is(err, io.EOF) {
				return err
			}
		}
		_, err := io.Copy(w, br)
		return err
	}
	if n <= 0 {
		_, err := io.Copy(io.Discard, r)
		return err
	}
	// Keep only the trailing n bytes, growing the buffer lazily so a huge -c
	// is bounded by the actual data read rather than preallocating n bytes.
	var buf []byte
	tmp := make([]byte, 32*1024)
	for {
		m, err := r.Read(tmp)
		if m > 0 {
			chunk := tmp[:m]
			if int64(m) >= n {
				// This chunk alone covers the window; keep its last n bytes.
				buf = append(buf[:0], chunk[int64(m)-n:]...)
			} else {
				buf = append(buf, chunk...)
				if int64(len(buf)) > n {
					// Compact in place to the last n bytes (bounded memory).
					buf = append(buf[:0], buf[int64(len(buf))-n:]...)
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			break
		}
	}
	_, err := w.Write(buf)
	return err
}

// followFile watches f for appended data and streams it until interrupted.
func followFile(w *bufio.Writer, f *os.File) error {
	buf := make([]byte, 32*1024)
	for {
		for {
			m, err := f.Read(buf)
			if m > 0 {
				if _, writeErr := w.Write(buf[:m]); writeErr != nil {
					return writeErr
				}
				if flushErr := w.Flush(); flushErr != nil {
					return flushErr
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// parseTailCount parses a count that may carry a leading '+' (count from start)
// and the same size suffixes as head. Returns (count, fromStart, error).
func parseTailCount(s string) (int64, bool, error) {
	s = strings.TrimSpace(s)
	fromStart := false
	if strings.HasPrefix(s, "+") {
		fromStart = true
		s = s[1:]
	} else if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	v, err := parseCount(s)
	if err != nil {
		return 0, false, err
	}
	return v, fromStart, nil
}
