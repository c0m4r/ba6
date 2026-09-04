// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// sortMods are the ordering options, which sort(1) accepts both globally and
// attached to one -k key.
type sortMods struct {
	numeric     bool // -n
	general     bool // -g
	human       bool // -h
	month       bool // -M
	fold        bool // -f
	dictionary  bool // -d
	ignoreCntrl bool // -i
	reverse     bool // -r
	blanksStart bool // -b at the key start
	blanksEnd   bool // -b at the key end
}

// ordering reports whether any option that changes the comparison is set. A key
// with none of them inherits the global ones, which is how the original decides
// what a bare -k means.
func (m sortMods) ordering() bool {
	return m.numeric || m.general || m.human || m.month || m.fold ||
		m.dictionary || m.ignoreCntrl || m.reverse || m.blanksStart || m.blanksEnd
}

// sortKeyDef is one -k KEYDEF: F[.C][OPTS][,F[.C][OPTS]].
type sortKeyDef struct {
	startField int
	startChar  int
	endField   int // 0 means "to the end of the line"
	endChar    int // 0 means "to the end of the end field"
	mods       sortMods
}

type sortOptions struct {
	keys     []sortKeyDef
	global   sortMods
	sep      byte
	sepSet   bool
	unique   bool
	stable   bool
	check    bool
	quiet    bool
	zeroTerm bool
	output   string
}

// cmdSort implements sort(1): lexical order by default, with -n/-g/-h/-M
// orderings, -k field keys with their own modifiers, -t separators, -u, -c/-C,
// -s, -o and -z.
func cmdSort(args []string) int {
	opts := sortOptions{sep: ' '}
	var files []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		next := func(name string) (string, bool) {
			i++
			if i >= len(args) {
				fatalf("sort", "option requires an argument -- '%s'", name)
				return "", false
			}
			return args[i], true
		}
		var value string
		var ok bool
		switch {
		case a == "--":
			i++
			goto rest
		case a == "--numeric-sort":
			opts.global.numeric = true
		case a == "--general-numeric-sort":
			opts.global.general = true
		case a == "--human-numeric-sort":
			opts.global.human = true
		case a == "--month-sort":
			opts.global.month = true
		case a == "--reverse":
			opts.global.reverse = true
		case a == "--unique":
			opts.unique = true
		case a == "--ignore-case":
			opts.global.fold = true
		case a == "--dictionary-order":
			opts.global.dictionary = true
		case a == "--ignore-nonprinting":
			opts.global.ignoreCntrl = true
		case a == "--ignore-leading-blanks":
			opts.global.blanksStart, opts.global.blanksEnd = true, true
		case a == "--stable":
			opts.stable = true
		case a == "--check" || a == "--check=diagnose-first":
			opts.check = true
		case a == "--check=quiet" || a == "--check=silent":
			opts.check, opts.quiet = true, true
		case a == "--zero-terminated":
			opts.zeroTerm = true
		case a == "--merge":
			// Every input is read and ordered anyway, so a merge of sorted
			// inputs produces exactly the same output.
		case a == "--key" || a == "-k":
			if value, ok = next("k"); !ok {
				return 2
			}
			if !opts.addKey(value) {
				return 2
			}
		case strings.HasPrefix(a, "--key="):
			if !opts.addKey(strings.TrimPrefix(a, "--key=")) {
				return 2
			}
		case strings.HasPrefix(a, "-k") && len(a) > 2:
			if !opts.addKey(a[2:]) {
				return 2
			}
		case a == "--field-separator" || a == "-t":
			if value, ok = next("t"); !ok {
				return 2
			}
			if !opts.setSeparator(value) {
				return 2
			}
		case strings.HasPrefix(a, "--field-separator="):
			if !opts.setSeparator(strings.TrimPrefix(a, "--field-separator=")) {
				return 2
			}
		case strings.HasPrefix(a, "-t") && len(a) > 2:
			if !opts.setSeparator(a[2:]) {
				return 2
			}
		case a == "--output" || a == "-o":
			if opts.output, ok = next("o"); !ok {
				return 2
			}
		case strings.HasPrefix(a, "--output="):
			opts.output = strings.TrimPrefix(a, "--output=")
		case strings.HasPrefix(a, "-o") && len(a) > 2:
			opts.output = a[2:]
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				c := a[j]
				switch c {
				case 'n':
					opts.global.numeric = true
				case 'g':
					opts.global.general = true
				case 'h':
					opts.global.human = true
				case 'M':
					opts.global.month = true
				case 'r':
					opts.global.reverse = true
				case 'u':
					opts.unique = true
				case 'f':
					opts.global.fold = true
				case 'd':
					opts.global.dictionary = true
				case 'i':
					opts.global.ignoreCntrl = true
				case 'b':
					opts.global.blanksStart, opts.global.blanksEnd = true, true
				case 's':
					opts.stable = true
				case 'm':
					// See --merge above.
				case 'z':
					opts.zeroTerm = true
				case 'c':
					opts.check = true
				case 'C':
					opts.check, opts.quiet = true, true
				case 'k', 't', 'o':
					// A value-taking option ends the bundle: -k2, -t: or -o out.
					value := a[j+1:]
					if value == "" {
						if value, ok = next(string(c)); !ok {
							return 2
						}
					}
					switch c {
					case 'k':
						if !opts.addKey(value) {
							return 2
						}
					case 't':
						if !opts.setSeparator(value) {
							return 2
						}
					case 'o':
						opts.output = value
					}
					j = len(a)
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
	// A key that carries no ordering option of its own uses the global ones.
	for k := range opts.keys {
		if !opts.keys[k].mods.ordering() {
			opts.keys[k].mods = opts.global
		}
	}
	return opts.run(files)
}

// setSeparator records -t. The original takes exactly one byte, with the
// two-character escape \0 for NUL.
func (o *sortOptions) setSeparator(value string) bool {
	if value == "\\0" {
		value = "\x00"
	}
	if len(value) != 1 {
		fatalf("sort", "multi-character tab %s", quoteLocaleName(value))
		return false
	}
	o.sep, o.sepSet = value[0], true
	return true
}

// addKey parses one -k KEYDEF.
func (o *sortOptions) addKey(spec string) bool {
	parts := strings.SplitN(spec, ",", 2)
	key := sortKeyDef{}
	startField, startChar, mods, err := parseKeyPart(parts[0], true)
	if err != nil {
		fatalf("sort", "invalid number at field start: invalid count at start of %q", spec)
		return false
	}
	if startField < 1 {
		fatalf("sort", "field number is zero: %q", spec)
		return false
	}
	key.startField, key.startChar, key.mods = startField, startChar, mods
	if len(parts) == 2 {
		endField, endChar, endMods, err := parseKeyPart(parts[1], false)
		if err != nil {
			fatalf("sort", "invalid number after ',': invalid count at start of %q", spec)
			return false
		}
		key.endField, key.endChar = endField, endChar
		// The end part carries its own b; every other modifier is shared.
		key.mods.blanksEnd = endMods.blanksEnd
		endMods.blanksEnd = false
		key.mods = mergeMods(key.mods, endMods)
	}
	o.keys = append(o.keys, key)
	return true
}

func mergeMods(a, b sortMods) sortMods {
	return sortMods{
		numeric:     a.numeric || b.numeric,
		general:     a.general || b.general,
		human:       a.human || b.human,
		month:       a.month || b.month,
		fold:        a.fold || b.fold,
		dictionary:  a.dictionary || b.dictionary,
		ignoreCntrl: a.ignoreCntrl || b.ignoreCntrl,
		reverse:     a.reverse || b.reverse,
		blanksStart: a.blanksStart || b.blanksStart,
		blanksEnd:   a.blanksEnd || b.blanksEnd,
	}
}

// parseKeyPart parses "F", "F.C" or either with trailing modifier letters. The
// start part's b means "skip blanks at the key start", the end part's b means
// the same for the key end.
func parseKeyPart(part string, isStart bool) (int, int, sortMods, error) {
	mods := sortMods{}
	digits := 0
	for digits < len(part) && (isDigitByte(part[digits]) || part[digits] == '.') {
		digits++
	}
	numbers, letters := part[:digits], part[digits:]
	field, char := 0, 0
	if dot := strings.IndexByte(numbers, '.'); dot >= 0 {
		var err error
		if field, err = strconv.Atoi(numbers[:dot]); err != nil {
			return 0, 0, mods, err
		}
		if char, err = strconv.Atoi(numbers[dot+1:]); err != nil {
			return 0, 0, mods, err
		}
	} else if numbers != "" {
		var err error
		if field, err = strconv.Atoi(numbers); err != nil {
			return 0, 0, mods, err
		}
	} else {
		return 0, 0, mods, fmt.Errorf("missing field number")
	}
	for i := 0; i < len(letters); i++ {
		switch letters[i] {
		case 'n':
			mods.numeric = true
		case 'g':
			mods.general = true
		case 'h':
			mods.human = true
		case 'M':
			mods.month = true
		case 'f':
			mods.fold = true
		case 'd':
			mods.dictionary = true
		case 'i':
			mods.ignoreCntrl = true
		case 'r':
			mods.reverse = true
		case 'b':
			if isStart {
				mods.blanksStart = true
			} else {
				mods.blanksEnd = true
			}
		default:
			return 0, 0, mods, fmt.Errorf("unknown key modifier %q", letters[i])
		}
	}
	return field, char, mods, nil
}

func (o *sortOptions) run(files []string) int {
	var lines []string
	var origins []lineOrigin
	status := 0
	for _, f := range files {
		r, err := openInput(f)
		if err != nil {
			fatalf("sort", "cannot read: %s: %s", shellQuoteName(f), errText(err))
			status = 2
			continue
		}
		sc := newLineScanner(r)
		if o.zeroTerm {
			sc.Split(scanNulLines)
		}
		number := 0
		for sc.Scan() {
			number++
			lines = append(lines, sc.Text())
			if o.check {
				origins = append(origins, lineOrigin{file: f, number: number})
			}
		}
		if scanErr("sort", f, sc) {
			status = 2
		}
		if closeErr := r.Close(); closeErr != nil {
			fatalf("sort", "%s: %s", f, errText(closeErr))
			status = 2
		}
	}
	if status != 0 {
		return status
	}
	if o.check {
		return o.runCheck(lines, origins)
	}
	sort.SliceStable(lines, func(a, b int) bool {
		return o.compare(lines[a], lines[b]) < 0
	})
	return o.write(lines)
}

// lineOrigin remembers where a line came from, for -c's disorder report.
type lineOrigin struct {
	file   string
	number int
}

// runCheck implements -c/-C: report the first line that breaks the order.
func (o *sortOptions) runCheck(lines []string, origins []lineOrigin) int {
	for k := 1; k < len(lines); k++ {
		cmp := o.compare(lines[k-1], lines[k])
		if cmp > 0 || (o.unique && cmp == 0) {
			if o.quiet {
				return 1
			}
			// The original reports a -u duplicate as a disorder too.
			fatalf("sort", "%s:%d: disorder: %s", origins[k].file, origins[k].number, lines[k])
			return 1
		}
	}
	return 0
}

func (o *sortOptions) write(lines []string) int {
	out := os.Stdout
	if o.output != "" {
		fh, err := os.Create(o.output)
		if err != nil {
			fatalf("sort", "open failed: %s: %s", o.output, errText(err))
			return 2
		}
		defer func() { _ = fh.Close() }()
		out = fh
	}
	terminator := byte('\n')
	if o.zeroTerm {
		terminator = 0
	}
	w := bufio.NewWriter(out)
	var prev string
	havePrev := false
	for _, ln := range lines {
		if o.unique && havePrev && o.compareKeys(prev, ln) == 0 {
			continue
		}
		_, _ = w.WriteString(ln)    // Flush reports the sticky error.
		_ = w.WriteByte(terminator) // Flush reports the sticky error.
		prev, havePrev = ln, true
	}
	if err := w.Flush(); err != nil {
		fatalf("sort", "write failed: %s", errText(err))
		return 2
	}
	return 0
}

// compare orders two lines: every key in turn, then the whole line as the
// last-resort comparison. -s and -u drop the last resort, and a global -r
// reverses the final answer, including that last resort - a key's own r does
// not.
func (o *sortOptions) compare(a, b string) int {
	if cmp := o.compareKeys(a, b); cmp != 0 {
		return cmp
	}
	// The original skips the last-resort comparison for -s and for -u, which
	// makes the sort stable and leaves the first line of an equal run first.
	if o.stable || o.unique {
		return 0
	}
	cmp := strings.Compare(a, b)
	if o.global.reverse {
		cmp = -cmp
	}
	return cmp
}

// compareKeys compares by the -k keys only (or the whole line when there are
// none), which is also the equality -u and -c use.
func (o *sortOptions) compareKeys(a, b string) int {
	if len(o.keys) == 0 {
		cmp := compareWithMods(a, b, o.global)
		if o.global.reverse {
			cmp = -cmp
		}
		return cmp
	}
	fieldsA, fieldsB := o.splitFields(a), o.splitFields(b)
	for _, key := range o.keys {
		keyA := extractKey(a, fieldsA, key)
		keyB := extractKey(b, fieldsB, key)
		cmp := compareWithMods(keyA, keyB, key.mods)
		if key.mods.reverse {
			cmp = -cmp
		}
		if cmp != 0 {
			return cmp
		}
	}
	return 0
}

// sortField is one field's byte range within a line.
type sortField struct{ start, end int }

// splitFields cuts a line into fields. With -t every separator byte ends a
// field and is not part of either neighbour; without it a field begins at the
// first blank of the run that precedes it, so the blanks belong to the field
// that follows them.
func (o *sortOptions) splitFields(line string) []sortField {
	var fields []sortField
	if o.sepSet {
		start := 0
		for i := 0; i < len(line); i++ {
			if line[i] == o.sep {
				fields = append(fields, sortField{start, i})
				start = i + 1
			}
		}
		return append(fields, sortField{start, len(line)})
	}
	start := 0
	for i := 1; i < len(line); i++ {
		if isSortBlank(line[i]) && !isSortBlank(line[i-1]) {
			fields = append(fields, sortField{start, i})
			start = i
		}
	}
	return append(fields, sortField{start, len(line)})
}

func isSortBlank(c byte) bool { return c == ' ' || c == '\t' }

// extractKey returns the substring of line the key selects.
func extractKey(line string, fields []sortField, key sortKeyDef) string {
	if key.startField > len(fields) {
		return ""
	}
	from := fields[key.startField-1]
	start := from.start
	if key.mods.blanksStart {
		for start < from.end && isSortBlank(line[start]) {
			start++
		}
	}
	if key.startChar > 1 {
		start += key.startChar - 1
	}
	if start > len(line) {
		start = len(line)
	}

	end := len(line)
	if key.endField > 0 && key.endField <= len(fields) {
		to := fields[key.endField-1]
		end = to.end
		if key.endChar > 0 {
			base := to.start
			if key.mods.blanksEnd {
				for base < to.end && isSortBlank(line[base]) {
					base++
				}
			}
			end = base + key.endChar
			if end > to.end {
				end = to.end
			}
		}
	}
	if end < start {
		return ""
	}
	return line[start:end]
}

// compareWithMods compares two key strings under one set of ordering options.
func compareWithMods(a, b string, mods sortMods) int {
	switch {
	case mods.numeric:
		return compareFloats(leadingNumber(a), leadingNumber(b))
	case mods.human:
		return compareFloats(humanNumber(a), humanNumber(b))
	case mods.general:
		return compareFloats(generalNumber(a), generalNumber(b))
	case mods.month:
		return compareInts(monthNumber(a), monthNumber(b))
	}
	return strings.Compare(sortTransform(a, mods), sortTransform(b, mods))
}

func compareFloats(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// sortTransform applies the options that rewrite a key before comparing it:
// -b (leading blanks), -d (keep blanks and alphanumerics), -i (drop
// unprintable bytes) and -f (fold case, to upper as in the original).
func sortTransform(s string, mods sortMods) string {
	if mods.blanksStart {
		s = strings.TrimLeft(s, " \t")
	}
	if !mods.dictionary && !mods.ignoreCntrl && !mods.fold {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if mods.dictionary && !isSortBlank(c) && !isSortAlnum(c) {
			continue
		}
		if mods.ignoreCntrl && (c < 0x20 || c == 0x7f) {
			continue
		}
		if mods.fold && c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

func isSortAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// monthNumber implements -M: an unknown month is 0, which sorts before January.
func monthNumber(s string) int {
	s = strings.TrimLeft(s, " \t")
	if len(s) < 3 {
		return 0
	}
	name := strings.ToLower(s[:3])
	months := []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	for i, m := range months {
		if name == m {
			return i + 1
		}
	}
	return 0
}

// generalNumber implements -g: a full strtod(3), where a key that is not a
// number at all sorts before every number.
func generalNumber(s string) float64 {
	s = strings.TrimLeft(s, " \t")
	end := 0
	for end < len(s) {
		c := s[end]
		switch {
		case isDigitByte(c) || c == '.':
			end++
			continue
		case (c == '+' || c == '-') && (end == 0 || s[end-1] == 'e' || s[end-1] == 'E'):
			end++
			continue
		case (c == 'e' || c == 'E') && end > 0:
			end++
			continue
		}
		break
	}
	value, err := strconv.ParseFloat(strings.TrimRight(s[:end], "eE+-"), 64)
	if err != nil {
		// Not a number: the original orders these before every number.
		return math.Inf(-1)
	}
	return value
}

// humanNumber implements -h: a number with an optional SI suffix.
func humanNumber(s string) float64 {
	s = strings.TrimLeft(s, " \t")
	value := leadingNumber(s)
	trimmed := strings.TrimLeft(s, "+-0123456789., \t")
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case 'K', 'k':
		return value * 1024
	case 'M':
		return value * 1024 * 1024
	case 'G':
		return value * 1024 * 1024 * 1024
	case 'T':
		return value * 1024 * 1024 * 1024 * 1024
	case 'P':
		return value * 1024 * 1024 * 1024 * 1024 * 1024
	}
	return value
}

// scanNulLines is a bufio.SplitFunc for -z: records end at a NUL byte.
func scanNulLines(data []byte, atEOF bool) (int, []byte, error) {
	for i, c := range data {
		if c == 0 {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
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
