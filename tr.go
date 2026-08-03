package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// cmdTr implements tr(1): translate, squeeze, or delete characters from stdin
// to stdout. Supports SET1/SET2 with ranges (a-z), -d (delete SET1), -s
// (squeeze repeats of the last operand set), -c (complement SET1), and the
// common escape sequences and a few classes ([:digit:], [:alpha:], etc.).
func cmdTr(args []string) int {
	var (
		del        bool
		squeeze    bool
		complement bool
		sets       []string
	)

	noMoreOptions := false
	for _, a := range args {
		switch {
		case !noMoreOptions && a == "--":
			noMoreOptions = true
		case !noMoreOptions && a == "--delete":
			del = true
		case !noMoreOptions && a == "--squeeze-repeats":
			squeeze = true
		case !noMoreOptions && a == "--complement":
			complement = true
		case !noMoreOptions && len(a) > 1 && a[0] == '-' && a != "-":
			handled := true
			for _, c := range a[1:] {
				switch c {
				case 'd':
					del = true
				case 's':
					squeeze = true
				case 'c', 'C':
					complement = true
				default:
					handled = false
				}
			}
			if !handled {
				fatalf("tr", "invalid option %q", a)
				return 1
			}
		default:
			sets = append(sets, a)
		}
	}

	if len(sets) == 0 {
		fatalf("tr", "missing operand")
		return 1
	}
	wantSets := 2
	if del && !squeeze || squeeze && !del && len(sets) == 1 {
		wantSets = 1
	}
	if len(sets) != wantSets {
		if len(sets) < wantSets {
			fatalf("tr", "missing operand")
		} else {
			fatalf("tr", "extra operand %q", sets[wantSets])
		}
		return 1
	}

	set1, err := expandSet(sets[0])
	if err != nil {
		fatalf("tr", "%v", err)
		return 1
	}
	if complement {
		set1 = complementSet(set1)
	}

	var set2 []byte
	if len(sets) >= 2 {
		set2, err = expandSet(sets[1])
		if err != nil {
			fatalf("tr", "%v", err)
			return 1
		}
	}

	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)

	switch {
	case del:
		err = runDelete(in, out, set1, squeeze, set2)
	case len(set2) > 0:
		err = runTranslate(in, out, set1, set2, squeeze)
	case squeeze:
		err = runSqueezeOnly(in, out, set1)
	default:
		fatalf("tr", "missing operand after %q", sets[0])
		return 1
	}
	if err != nil {
		fatalf("tr", "read error: %v", err)
		return 1
	}
	if err := out.Flush(); err != nil {
		fatalf("tr", "write error: %v", err)
		return 1
	}
	return 0
}

// inSet builds a 256-entry membership table for a byte set.
func inSet(set []byte) [256]bool {
	var t [256]bool
	for _, b := range set {
		t[b] = true
	}
	return t
}

func runDelete(in *bufio.Reader, out *bufio.Writer, set1 []byte, squeeze bool, set2 []byte) error {
	delTbl := inSet(set1)
	sqTbl := inSet(set2)
	last := -1
	for {
		b, err := in.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if delTbl[b] {
			continue
		}
		if squeeze && sqTbl[b] && int(b) == last {
			continue
		}
		if writeErr := out.WriteByte(b); writeErr != nil {
			return writeErr
		}
		last = int(b)
	}
}

func runTranslate(in *bufio.Reader, out *bufio.Writer, set1, set2 []byte, squeeze bool) error {
	// Build translation table; SET2's last char repeats to pad to len(SET1).
	var tbl [256]byte
	for i := range tbl {
		tbl[i] = byte(i)
	}
	mapped := inSet(set1)
	for i, b := range set1 {
		if i < len(set2) {
			tbl[b] = set2[i]
		} else {
			tbl[b] = set2[len(set2)-1]
		}
	}
	sqTbl := inSet(set2)
	last := -1
	for {
		b, err := in.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		o := b
		if mapped[b] {
			o = tbl[b]
		}
		if squeeze && sqTbl[o] && int(o) == last {
			continue
		}
		if writeErr := out.WriteByte(o); writeErr != nil {
			return writeErr
		}
		last = int(o)
	}
}

func runSqueezeOnly(in *bufio.Reader, out *bufio.Writer, set1 []byte) error {
	sqTbl := inSet(set1)
	last := -1
	for {
		b, err := in.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if sqTbl[b] && int(b) == last {
			continue
		}
		if writeErr := out.WriteByte(b); writeErr != nil {
			return writeErr
		}
		last = int(b)
	}
}

// expandSet turns a tr SET specification into the explicit byte sequence it
// denotes, handling ranges (a-z), escape sequences, and POSIX classes.
func expandSet(s string) ([]byte, error) {
	var out []byte
	runes := []byte(s)
	for i := 0; i < len(runes); i++ {
		// POSIX character class [:name:].
		if runes[i] == '[' && i+1 < len(runes) && runes[i+1] == ':' {
			if end := strings.Index(s[i:], ":]"); end >= 0 {
				class := s[i+2 : i+end]
				expanded, ok := expandClass(class)
				if !ok {
					return nil, fmt.Errorf("unknown character class %q", class)
				}
				out = append(out, expanded...)
				i += end + 1
				continue
			}
		}
		// Escape sequence.
		if runes[i] == '\\' && i+1 < len(runes) {
			i++
			if runes[i] >= '0' && runes[i] <= '7' {
				end := i + 1
				for end < len(runes) && end < i+3 && runes[end] >= '0' && runes[end] <= '7' {
					end++
				}
				v, _ := strconv.ParseUint(string(runes[i:end]), 8, 8)
				out = append(out, byte(v))
				i = end - 1
				continue
			}
			out = append(out, unescapeByte(runes[i]))
			continue
		}
		// Range a-z.
		if i+2 < len(runes) && runes[i+1] == '-' && runes[i+2] != '\\' {
			lo, hi := runes[i], runes[i+2]
			if lo <= hi {
				for c := int(lo); c <= int(hi); c++ {
					out = append(out, byte(c)) //nolint:gosec // G115: c <= hi <= 255
				}
				i += 2
				continue
			}
			return nil, fmt.Errorf("range-endpoints of %q are in reverse order", s[i:i+3])
		}
		out = append(out, runes[i])
	}
	return out, nil
}

func unescapeByte(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	case 'f':
		return '\f'
	case 'v':
		return '\v'
	case 'a':
		return '\a'
	case 'b':
		return '\b'
	case '\\':
		return '\\'
	default:
		return c
	}
}

// forEachByte invokes fn for every byte value 0x00..0xFF. Centralizing the
// loop keeps the (provably in-range) int-to-byte conversion in one audited spot.
func forEachByte(fn func(b byte)) {
	for c := 0; c < 256; c++ {
		fn(byte(c)) //nolint:gosec // G115: c is bounded to 0..255
	}
}

func expandClass(name string) ([]byte, bool) {
	var out []byte
	add := func(pred func(b byte) bool) {
		forEachByte(func(b byte) {
			if pred(b) {
				out = append(out, b)
			}
		})
	}
	switch name {
	case "digit":
		add(func(b byte) bool { return b >= '0' && b <= '9' })
	case "lower":
		add(func(b byte) bool { return b >= 'a' && b <= 'z' })
	case "upper":
		add(func(b byte) bool { return b >= 'A' && b <= 'Z' })
	case "alpha":
		add(func(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') })
	case "alnum":
		add(func(b byte) bool {
			return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
		})
	case "space":
		out = []byte(" \t\n\r\v\f")
	case "blank":
		out = []byte(" \t")
	case "punct":
		add(func(b byte) bool {
			return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", b) >= 0
		})
	default:
		return nil, false
	}
	return out, true
}

// complementSet returns all byte values not present in set, in ascending order.
func complementSet(set []byte) []byte {
	present := inSet(set)
	var out []byte
	forEachByte(func(b byte) {
		if !present[b] {
			out = append(out, b)
		}
	})
	return out
}
