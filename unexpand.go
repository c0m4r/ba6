// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"io"
	"os"
)

// cmdUnexpand implements unexpand(1): convert runs of blanks that cross a tab
// stop into tabs. By default only the leading run of each line is converted;
// -a (also implied by -t) converts every run. The conversion is a faithful
// port of GNU's own pending-blank state machine, so multi-tab runs, blanks
// around existing tabs, and a run ending exactly on a stop all match.
func cmdUnexpand(args []string) int {
	entireLine := false
	firstOnly := false
	extra := func(a string) (bool, int) {
		switch {
		case a == "-a" || a == "--all":
			entireLine = true
			return true, 0
		case a == "--first-only":
			firstOnly = true
			return true, 0
		case a == "-t" || a == "--tabs" || (len(a) > 2 && a[0] == '-' && a[1] == 't') || (len(a) > 7 && a[:7] == "--tabs="):
			// -t selects tab stops and, like the original, also enables -a.
			entireLine = true
			return false, 0
		}
		return false, 0
	}
	stops, files, status := parseTabArgs("unexpand", args, extra)
	if status != 0 {
		return status
	}
	if firstOnly {
		entireLine = false
	}
	if !stops.finalize("unexpand") {
		return 1
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	var state = unexpandState{convert: true, prevBlank: true}
	status = forEachInput("unexpand", files, func(_ string, r *bufio.Reader) error {
		return state.run(out, r, stops, entireLine)
	})
	if err := state.flushPending(out); err != nil {
		fatalf("unexpand", "write error: %v", err)
		return 1
	}
	if err := out.Flush(); err != nil {
		fatalf("unexpand", "write error: %v", err)
		return 1
	}
	return status
}

// unexpandState carries the pending-blank run and column bookkeeping across
// the file-to-file stream handoff, mirroring GNU's single input stream.
type unexpandState struct {
	convert        bool
	column         int
	tabIndex       int
	nextTab        int
	oneBlankBefore bool
	prevBlank      bool
	pending        []byte
}

// flushPending writes the held-back blank run, converting its first blank to
// a tab when the run crosses a stop boundary, and resets the tracking.
func (s *unexpandState) flushPending(w *bufio.Writer) error {
	if len(s.pending) > 0 {
		if len(s.pending) > 1 && s.oneBlankBefore {
			s.pending[0] = '\t'
		}
		if _, err := w.Write(s.pending); err != nil {
			return err
		}
		s.pending = s.pending[:0]
		s.oneBlankBefore = false
	}
	return nil
}

// run writes r to w with blank runs converted to tabs. The run of blanks
// since the last non-blank is held back until either a blank reaches a tab
// stop (replace it with a tab) or a non-blank arrives (emit it verbatim).
func (s *unexpandState) run(w *bufio.Writer, r *bufio.Reader, stops tabStops, entireLine bool) error {
	for {
		b, err := r.ReadByte()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		writeChar := true
		if s.convert {
			blank := b == ' ' || b == '\t'
			if blank {
				var last bool
				s.nextTab, last = stops.next(s.column, &s.tabIndex)
				if last {
					s.convert = false
				}
				if s.convert {
					if b == '\t' {
						s.column = s.nextTab
						if len(s.pending) > 0 {
							s.pending[0] = '\t'
						}
					} else {
						s.column++
						if !s.prevBlank || s.column < s.nextTab {
							if s.column == s.nextTab {
								s.oneBlankBefore = true
							}
							s.pending = append(s.pending, b)
							s.prevBlank = true
							continue
						}
						writeChar = false
						if werr := w.WriteByte('\t'); werr != nil {
							return werr
						}
						s.pending = s.pending[:0]
						s.pending = append(s.pending, '\t')
					}
					if s.oneBlankBefore && len(s.pending) > 0 {
						s.pending = s.pending[:1]
					} else {
						s.pending = s.pending[:0]
					}
				}
			} else if b == '\b' {
				if s.column > 0 {
					s.column--
				}
				s.nextTab = s.column
				if s.tabIndex > 0 {
					s.tabIndex--
				}
			} else {
				s.column++
			}
			if err := s.flushPending(w); err != nil {
				return err
			}
			s.prevBlank = blank
			s.convert = s.convert && (entireLine || blank)
		}
		if writeChar {
			if werr := w.WriteByte(b); werr != nil {
				return werr
			}
		}
		if b == '\n' {
			s.convert = true
			s.column = 0
			s.tabIndex = 0
			s.nextTab = 0
			s.oneBlankBefore = false
			s.prevBlank = true
		}
	}
}
