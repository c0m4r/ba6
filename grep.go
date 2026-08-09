// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// grepOpts holds parsed grep flags.
type grepOpts struct {
	ignoreCase bool
	invert     bool
	lineNum    bool
	countOnly  bool
	filesOnly  bool // -l: only print names of matching files
	noFilename bool // -h
	withFn     bool // -H: force filename prefix
	wholeWord  bool // -w
	wholeLine  bool // -x
	fixed      bool // -F: pattern is a literal string
	extended   bool // -E: patterns use ERE rather than the default BRE
	recursive  bool // -r/-R
	quiet      bool // -q
	maxCount   int  // -m, -1 = unlimited
}

// cmdGrep implements a useful subset of grep(1). Its default patterns are
// POSIX BREs and -E selects POSIX EREs. RE2 supplies execution, after the
// syntax has been translated and validated by the shared regex layer.
func cmdGrep(args []string) int {
	opts := grepOpts{maxCount: -1}
	var pattern string
	patternSet := false
	var patterns []string
	var files []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) > 1 && a[0] == '-' && a != "-" {
			// Long options.
			switch {
			case a == "--ignore-case":
				opts.ignoreCase = true
				continue
			case a == "--invert-match":
				opts.invert = true
				continue
			case a == "--line-number":
				opts.lineNum = true
				continue
			case a == "--count":
				opts.countOnly = true
				continue
			case a == "--recursive":
				opts.recursive = true
				continue
			case a == "--word-regexp":
				opts.wholeWord = true
				continue
			case a == "--line-regexp":
				opts.wholeLine = true
				continue
			case a == "--fixed-strings":
				opts.fixed = true
				continue
			case a == "--extended-regexp":
				opts.extended = true
				continue
			case a == "--quiet" || a == "--silent":
				opts.quiet = true
				continue
			case strings.HasPrefix(a, "-e"):
				val := a[2:]
				if val == "" {
					i++
					if i >= len(args) {
						fatalf("grep", "option requires an argument -- 'e'")
						return 2
					}
					val = args[i]
				}
				patterns = append(patterns, val)
				patternSet = true
				continue
			case strings.HasPrefix(a, "-m"):
				val := a[2:]
				if val == "" {
					i++
					if i >= len(args) {
						fatalf("grep", "option requires an argument -- 'm'")
						return 2
					}
					val = args[i]
				}
				n, err := parseCount(val)
				maxInt := int64(^uint(0) >> 1)
				if err != nil || n > maxInt {
					fatalf("grep", "invalid max count %q", val)
					return 2
				}
				opts.maxCount = int(n)
				continue
			}
			// Short option bundle, e.g. -inv.
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'i':
					opts.ignoreCase = true
				case 'v':
					opts.invert = true
				case 'n':
					opts.lineNum = true
				case 'c':
					opts.countOnly = true
				case 'l':
					opts.filesOnly = true
				case 'h':
					opts.noFilename = true
				case 'H':
					opts.withFn = true
				case 'w':
					opts.wholeWord = true
				case 'x':
					opts.wholeLine = true
				case 'F':
					opts.fixed = true
				case 'E':
					opts.extended = true
				case 'r', 'R':
					opts.recursive = true
				case 'q':
					opts.quiet = true
				default:
					fatalf("grep", "invalid option -- '%c'", a[j])
					return 2
				}
			}
			continue
		}
		// First non-flag operand is the pattern (unless -e was used).
		if !patternSet && len(patterns) == 0 {
			pattern = a
			patternSet = true
		} else {
			files = append(files, a)
		}
	}
	if !patternSet && len(patterns) == 0 && i < len(args) {
		pattern, patternSet = args[i], true
		i++
	}
	files = append(files, args[i:]...)

	if len(patterns) == 0 {
		if !patternSet {
			fatalf("grep", "no pattern given")
			return 2
		}
		patterns = []string{pattern}
	}

	re, err := buildRegexp(patterns, opts)
	if err != nil {
		fatalf("grep", "%v", err)
		return 2
	}

	if len(files) == 0 {
		if opts.recursive {
			files = []string{"."}
		} else {
			files = []string{"-"}
		}
	}

	// Decide whether to prefix lines with filenames.
	showName := opts.withFn || opts.recursive || len(files) > 1
	if opts.noFilename {
		showName = false
	}

	g := &grepRun{opts: opts, re: re, showName: showName, out: bufio.NewWriter(os.Stdout)}

	if opts.recursive {
		for _, root := range files {
			g.walk(root)
			if g.done {
				break
			}
		}
	} else {
		for _, f := range files {
			g.scanFile(f)
			if g.done {
				break
			}
		}
	}
	if err := g.out.Flush(); err != nil {
		fatalf("grep", "write error: %v", err)
		g.fileError = true
	}

	if opts.quiet {
		if g.anyMatch {
			return 0
		}
		return 1
	}
	if g.fileError {
		return 2
	}
	if g.anyMatch {
		return 0
	}
	return 1
}

func buildRegexp(patterns []string, opts grepOpts) (*regexp.Regexp, error) {
	parts := make([]string, len(patterns))
	for i, p := range patterns {
		if opts.fixed {
			p = regexp.QuoteMeta(p)
		} else {
			var err error
			syntax := posixBRE
			if opts.extended {
				syntax = posixERE
			}
			p, err = translatePOSIXRegexp(p, syntax)
			if err != nil {
				return nil, err
			}
		}
		if opts.wholeWord {
			// RE2's \b is not a POSIX ERE operator. The explicit ASCII
			// boundary has the same definition grep uses for -w.
			p = `(^|[^[:alnum:]_])(` + p + `)($|[^[:alnum:]_])`
		}
		if opts.wholeLine {
			p = `^(` + p + `)$`
		}
		parts[i] = "(" + p + ")"
	}
	expr := strings.Join(parts, "|")
	return compilePOSIXERE(expr, opts.ignoreCase)
}

type grepRun struct {
	opts      grepOpts
	re        *regexp.Regexp
	showName  bool
	out       *bufio.Writer
	anyMatch  bool
	fileError bool
	done      bool
}

func (g *grepRun) walk(root string) {
	info, err := os.Stat(root)
	if err != nil {
		fatalf("grep", "%s: %v", root, err)
		g.fileError = true
		return
	}
	if !info.IsDir() {
		g.scanFile(root)
		return
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if g.done {
			return filepath.SkipAll
		}
		if err != nil {
			fatalf("grep", "%s: %v", path, err)
			g.fileError = true
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		g.scanFile(path)
		return nil
	})
}

func (g *grepRun) scanFile(fname string) {
	f, err := openInput(fname)
	if err != nil {
		fatalf("grep", "%s: %v", fname, err)
		g.fileError = true
		return
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			fatalf("grep", "%s: %v", fname, closeErr)
			g.fileError = true
		}
	}()

	displayName := fname
	if fname == "-" {
		displayName = "(standard input)"
	}

	sc := newLineScanner(f)

	var count int
	lineNo := 0
	if g.opts.maxCount == 0 {
		if g.opts.countOnly && !g.opts.quiet && !g.opts.filesOnly {
			if g.showName {
				_, _ = g.out.WriteString(displayName + ":") // Flush reports the sticky error.
			}
			fmt.Fprintln(g.out, 0)
		}
		return
	}
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		matched := g.re.MatchString(line)
		if g.opts.invert {
			matched = !matched
		}
		if !matched {
			continue
		}

		g.anyMatch = true
		count++

		if g.opts.quiet {
			g.done = true
			return
		}
		if g.opts.filesOnly {
			_, _ = g.out.WriteString(displayName + "\n") // Flush reports the sticky error.
			return
		}
		if g.opts.countOnly {
			if g.opts.maxCount > 0 && count >= g.opts.maxCount {
				break
			}
			continue
		}

		if g.showName {
			_, _ = g.out.WriteString(displayName + ":") // Flush reports the sticky error.
		}
		if g.opts.lineNum {
			fmt.Fprintf(g.out, "%d:", lineNo)
		}
		_, _ = g.out.WriteString(line) // Flush reports the sticky error.
		_ = g.out.WriteByte('\n')      // Flush reports the sticky error.

		if g.opts.maxCount > 0 && count >= g.opts.maxCount {
			break
		}
	}
	if scanErr("grep", displayName, sc) {
		g.fileError = true
	}

	if g.opts.countOnly && !g.opts.quiet && !g.opts.filesOnly {
		if g.showName {
			_, _ = g.out.WriteString(displayName + ":") // Flush reports the sticky error.
		}
		fmt.Fprintf(g.out, "%d\n", count)
	}
}
