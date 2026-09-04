// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"bytes"
	"debug/elf"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

func cmdPrintf(args []string) int {
	if len(args) == 0 {
		fatalf("printf", "missing format operand")
		return 1
	}
	format := decodeEscapes(args[0], false)
	values := args[1:]
	status := 0
	for {
		used, bad, err := writePrintf(os.Stdout, format, values)
		if bad {
			status = 1
		}
		if err != nil {
			fatalf("printf", "%v", err)
			return 1
		}
		if len(values) == 0 || used == 0 || used >= len(values) {
			break
		}
		values = values[used:]
	}
	return status
}

// writePrintf expands one pass of format over args, returning how many operands
// it consumed and whether any of them failed to convert. A bad numeric operand
// is not fatal: like printf(1) it is reported, treated as zero, and the exit
// status becomes 1 once the whole format has been written.
func writePrintf(w io.Writer, format string, args []string) (int, bool, error) {
	used := 0
	bad := false
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			if _, err := io.WriteString(w, format[i:i+1]); err != nil {
				return used, bad, err
			}
			continue
		}
		i++
		if i >= len(format) {
			return used, bad, fmt.Errorf("invalid trailing %%")
		}
		if format[i] == '%' {
			_, err := io.WriteString(w, "%")
			if err != nil {
				return used, bad, err
			}
			continue
		}
		start := i
		for i < len(format) && strings.ContainsRune("-+ #0.123456789", rune(format[i])) {
			i++
		}
		if i >= len(format) {
			return used, bad, fmt.Errorf("incomplete conversion")
		}
		spec := format[i]
		directive := "%" + format[start:i+1]
		value := ""
		// A conversion with no operand left is a plain zero or empty string;
		// only an operand that is present and unconvertible is an error.
		present := used < len(args)
		if present {
			value = args[used]
		}
		used++
		var out string
		switch spec {
		case 's':
			out = fmt.Sprintf(directive, value)
		case 'b':
			out = decodeEscapes(value, true)
		case 'c':
			if value != "" {
				out = value[:1]
			}
		case 'd', 'i':
			n, failed := printfSigned(value, present)
			bad = bad || failed
			directive = directive[:len(directive)-1] + "d"
			out = fmt.Sprintf(directive, n)
		case 'u', 'o', 'x', 'X':
			n, failed := printfUnsigned(value, present)
			bad = bad || failed
			out = fmt.Sprintf(directive, n)
		case 'f', 'F', 'e', 'E', 'g', 'G':
			n, failed := printfFloat(value, present)
			bad = bad || failed
			out = fmt.Sprintf(directive, n)
		default:
			return used, bad, fmt.Errorf("unsupported conversion %%%c", spec)
		}
		if _, err := io.WriteString(w, out); err != nil {
			return used, bad, err
		}
	}
	return used, bad, nil
}

// charConstant decodes printf(1)'s character-constant operand: a leading quote
// makes "'A" mean 65. Trailing characters are ignored with a warning that does
// not affect the exit status.
func charConstant(value string) (int64, bool) {
	if len(value) < 2 || (value[0] != '\'' && value[0] != '"') {
		return 0, false
	}
	r, size := utf8.DecodeRuneInString(value[1:])
	if rest := value[1+size:]; rest != "" {
		fatalf("printf", "warning: %s: character(s) following character constant have been ignored", rest)
	}
	return int64(r), true
}

// numericPrefix returns the longest leading run of value that parse accepts,
// mirroring what strtol(3) consumes before printf(1) reports that a value was
// not completely converted.
func numericPrefix(value string, parse func(string) bool) string {
	for end := len(value); end > 0; end-- {
		if parse(value[:end]) {
			return value[:end]
		}
	}
	return ""
}

// reportOperand emits printf(1)'s diagnostic for an operand that did not
// convert, and reports that the exit status must become 1.
func reportOperand(value, prefix string) bool {
	if prefix == "" {
		fatalf("printf", "'%s': expected a numeric value", value)
	} else {
		fatalf("printf", "'%s': value not completely converted", value)
	}
	return true
}

func printfSigned(value string, present bool) (int64, bool) {
	if !present {
		return 0, false
	}
	if n, ok := charConstant(value); ok {
		return n, false
	}
	text := strings.TrimSpace(value)
	accepts := func(s string) bool { _, err := strconv.ParseInt(s, 0, 64); return err == nil }
	if accepts(text) {
		n, _ := strconv.ParseInt(text, 0, 64)
		return n, false
	}
	prefix := numericPrefix(text, accepts)
	n, _ := strconv.ParseInt(prefix, 0, 64)
	return n, reportOperand(value, prefix)
}

func printfUnsigned(value string, present bool) (uint64, bool) {
	if !present {
		return 0, false
	}
	if n, ok := charConstant(value); ok {
		return uint64(n), false //nolint:gosec // A character constant is a code point, never negative.
	}
	text := strings.TrimSpace(value)
	// A negative operand is accepted and wraps, as it does in C.
	accepts := func(s string) bool {
		if _, err := strconv.ParseUint(s, 0, 64); err == nil {
			return true
		}
		_, err := strconv.ParseInt(s, 0, 64)
		return err == nil
	}
	convert := func(s string) uint64 {
		if n, err := strconv.ParseUint(s, 0, 64); err == nil {
			return n
		}
		n, _ := strconv.ParseInt(s, 0, 64)
		return uint64(n) //nolint:gosec // Wrapping a negative operand is what C's %x does.
	}
	if accepts(text) {
		return convert(text), false
	}
	prefix := numericPrefix(text, accepts)
	return convert(prefix), reportOperand(value, prefix)
}

func printfFloat(value string, present bool) (float64, bool) {
	if !present {
		return 0, false
	}
	if n, ok := charConstant(value); ok {
		return float64(n), false
	}
	text := strings.TrimSpace(value)
	accepts := func(s string) bool { _, err := strconv.ParseFloat(s, 64); return err == nil }
	if accepts(text) {
		n, _ := strconv.ParseFloat(text, 64)
		return n, false
	}
	prefix := numericPrefix(text, accepts)
	n, _ := strconv.ParseFloat(prefix, 64)
	return n, reportOperand(value, prefix)
}

func decodeEscapes(value string, stop bool) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' || i+1 >= len(value) {
			out.WriteByte(value[i])
			continue
		}
		i++
		switch value[i] {
		case 'a':
			out.WriteByte('\a')
		case 'b':
			out.WriteByte('\b')
		case 'c':
			if stop {
				return out.String()
			}
			out.WriteString("\\c")
		case 'e':
			out.WriteByte(27)
		case 'f':
			out.WriteByte('\f')
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case 'v':
			out.WriteByte('\v')
		case '\\':
			out.WriteByte('\\')
		case 'x':
			end := i + 1
			for end < len(value) && end <= i+2 && isHex(value[end]) {
				end++
			}
			if end > i+1 {
				n, _ := strconv.ParseUint(value[i+1:end], 16, 8)
				out.WriteByte(byte(n))
				i = end - 1
			} else {
				out.WriteString("\\x")
			}
		default:
			if value[i] >= '0' && value[i] <= '7' {
				end := i + 1
				for end < len(value) && end <= i+2 && value[end] >= '0' && value[end] <= '7' {
					end++
				}
				n, _ := strconv.ParseUint(value[i:end], 8, 8)
				out.WriteByte(byte(n))
				i = end - 1
			} else {
				out.WriteByte('\\')
				out.WriteByte(value[i])
			}
		}
	}
	return out.String()
}

func cmdPrintenv(args []string) int {
	nullTerminate := false
	status := 0
	var names []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			names = append(names, args[i+1:]...)
			break
		}
		if arg == "-0" || arg == "--null" {
			nullTerminate = true
			continue
		}
		if len(arg) > 1 && arg[0] == '-' {
			fatalf("printenv", "unrecognized option '%s'", arg)
			fmt.Fprintln(os.Stderr, "Try 'printenv --help' for more information.")
			return 2
		}
		// Like GNU's getopt, the first operand ends option parsing: the
		// remaining arguments are names even when one starts with '-'.
		names = append(names, args[i:]...)
		break
	}
	terminator := "\n"
	if nullTerminate {
		terminator = "\x00"
	}
	if len(names) == 0 {
		for _, value := range os.Environ() {
			fmt.Fprint(os.Stdout, value+terminator)
		}
		return 0
	}
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok {
			fmt.Fprint(os.Stdout, value+terminator)
		} else {
			status = 1
		}
	}
	return status
}

func cmdSeq(args []string) int {
	separator, format := "\n", ""
	equalWidth := false
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-s":
			if len(args) < 2 {
				fatalf("seq", "-s requires an argument")
				return 1
			}
			separator, args = args[1], args[2:]
		case "-f":
			if len(args) < 2 {
				fatalf("seq", "-f requires an argument")
				return 1
			}
			format, args = args[1], args[2:]
		case "-w", "--equal-width":
			equalWidth, args = true, args[1:]
		case "--":
			args = args[1:]
			goto parsed
		default:
			goto parsed
		}
	}
parsed:
	if len(args) < 1 || len(args) > 3 {
		fatalf("seq", "expected LAST, FIRST LAST, or FIRST INCREMENT LAST")
		return 1
	}
	first, step, last := 1.0, 1.0, 0.0
	var err error
	if len(args) == 1 {
		last, err = strconv.ParseFloat(args[0], 64)
	} else {
		first, err = strconv.ParseFloat(args[0], 64)
		if err == nil {
			last, err = strconv.ParseFloat(args[len(args)-1], 64)
		}
		if len(args) == 3 && err == nil {
			step, err = strconv.ParseFloat(args[1], 64)
		}
	}
	if err != nil || step == 0 {
		fatalf("seq", "invalid numeric operand")
		return 1
	}

	// Every value is computed as first+i*step from an up-front count, never by
	// repeated addition: adding 0.1 three times gives 0.30000000000000004 and
	// would also decide the endpoint test wrongly.
	precision := 0
	for _, operand := range args {
		if places := decimalPlaces(operand); places > precision {
			precision = places
		}
	}
	span := (last - first) / step
	count := int64(0)
	if span >= 0 {
		count = int64(span+math.Abs(span)*1e-12+1e-9) + 1
	}
	width := 0
	if equalWidth && count > 0 {
		width = len(strconv.FormatFloat(first, 'f', precision, 64))
		if end := len(strconv.FormatFloat(first+float64(count-1)*step, 'f', precision, 64)); end > width {
			width = end
		}
	}

	for i := int64(0); i < count; i++ {
		if i > 0 {
			if _, err := io.WriteString(os.Stdout, separator); err != nil {
				fatalf("seq", "write error: %v", err)
				return 1
			}
		}
		value := first + float64(i)*step
		if format != "" {
			fmt.Fprintf(os.Stdout, format, value)
		} else {
			fmt.Fprint(os.Stdout, padNumber(strconv.FormatFloat(value, 'f', precision, 64), width))
		}
	}
	if count > 0 {
		fmt.Fprintln(os.Stdout)
	}
	return 0
}

// decimalPlaces reports how many digits after the point an operand asks seq(1)
// to print. The operand's spelling decides it, not its value: "0.10" means two
// places, and an exponent shifts the point ("1e2" means none).
func decimalPlaces(operand string) int {
	mantissa, exponent := operand, 0
	if marker := strings.IndexAny(operand, "eE"); marker >= 0 {
		value, err := strconv.Atoi(operand[marker+1:])
		if err != nil {
			return 0
		}
		mantissa, exponent = operand[:marker], value
	}
	places := 0
	if point := strings.IndexByte(mantissa, '.'); point >= 0 {
		places = len(mantissa) - point - 1
	}
	if places -= exponent; places < 0 {
		return 0
	}
	return places
}

// padNumber left-pads text with zeros to width for seq -w, keeping any sign in
// front of the padding.
func padNumber(text string, width int) string {
	if len(text) >= width {
		return text
	}
	sign := ""
	if strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+") {
		sign, text = text[:1], text[1:]
	}
	return sign + strings.Repeat("0", width-len(sign)-len(text)) + text
}

func cmdCmp(args []string) int {
	silent := false
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "-s" || args[0] == "--silent" || args[0] == "--quiet" {
			silent = true
			args = args[1:]
		} else if args[0] == "--" {
			args = args[1:]
			break
		} else {
			fatalf("cmp", "unsupported option %q", args[0])
			return 2
		}
	}
	if len(args) != 2 {
		fatalf("cmp", "expected two files")
		return 2
	}
	a, err := readInputBytes(args[0])
	if err != nil {
		fatalf("cmp", "%s: %v", args[0], err)
		return 2
	}
	b, err := readInputBytes(args[1])
	if err != nil {
		fatalf("cmp", "%s: %v", args[1], err)
		return 2
	}
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	line := 1
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			if !silent {
				// The unit is a byte, and so is the word: GNU cmp's own message
				// catalogue rewords the POSIX "char" of its untranslated string
				// to "byte", which is what the tool prints on any ordinary
				// system. The name is quoted only in the EOF message.
				fmt.Printf("%s %s differ: byte %d, line %d\n", args[0], args[1], i+1, line)
			}
			return 1
		}
		if a[i] == '\n' {
			line++
		}
	}
	if len(a) != len(b) {
		if !silent {
			shorter := args[0]
			if len(b) < len(a) {
				shorter = args[1]
			}
			switch {
			case limit == 0:
				fmt.Fprintf(os.Stderr, "cmp: EOF on '%s' which is empty\n", shorter)
			case a[limit-1] == '\n':
				// The file stopped on a line boundary, so count whole lines.
				fmt.Fprintf(os.Stderr, "cmp: EOF on '%s' after byte %d, line %d\n", shorter, limit, line-1)
			default:
				// It stopped part-way through a line, which the original says
				// differently: "in line N" names the incomplete line.
				fmt.Fprintf(os.Stderr, "cmp: EOF on '%s' after byte %d, in line %d\n", shorter, limit, line)
			}
		}
		return 1
	}
	return 0
}

func readInputBytes(name string) ([]byte, error) {
	r, err := openInput(name)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, 1<<30))
}

func cmdBase64(args []string) int {
	args = expandShortOptions(args, "w")
	decode, wrap := false, 76
	files := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--decode":
			decode = true
		case "-w", "--wrap":
			i++
			if i >= len(args) {
				fatalf("base64", "-w requires a number")
				return 1
			}
			wrap, _ = strconv.Atoi(args[i])
		case "--":
			files = append(files, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(args[i], "-") && args[i] != "-" {
				fatalf("base64", "unsupported option %q", args[i])
				return 1
			}
			files = append(files, args[i])
		}
	}
	if len(files) > 1 {
		fatalf("base64", "extra operand %q", files[1])
		return 1
	}
	name := "-"
	if len(files) == 1 {
		name = files[0]
	}
	data, err := readInputBytes(name)
	if err != nil {
		fatalf("base64", "%s: %v", name, err)
		return 1
	}
	if decode {
		clean := bytes.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, data)
		dst := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		n, err := base64.StdEncoding.Decode(dst, clean)
		if err != nil {
			fatalf("base64", "invalid input: %v", err)
			return 1
		}
		_, err = os.Stdout.Write(dst[:n])
		if err != nil {
			return 1
		}
		return 0
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	if wrap <= 0 {
		fmt.Fprint(os.Stdout, encoded)
		return 0
	}
	for len(encoded) > wrap {
		fmt.Fprintln(os.Stdout, encoded[:wrap])
		encoded = encoded[wrap:]
	}
	if encoded != "" {
		fmt.Fprintln(os.Stdout, encoded)
	}
	return 0
}

// stringsConfig is one strings(1) command line: the scan geometry (how long a
// run has to be and how wide a character is) plus how each run is printed.
type stringsConfig struct {
	minimum   int
	radix     byte   // 0, or 'o'/'d'/'x' for the -t address column
	printName bool   // -f
	dataOnly  bool   // -d, scan only the loaded non-code sections of an object
	allWhite  bool   // -w
	eightBit  bool   // -e S: bytes above 127 count as printable
	separator string // -s; printed after each run in place of the newline
	width     int    // bytes per character: 1, 2 or 4
	bigEndian bool   // for the 2- and 4-byte encodings
}

// stringsPrintable follows binutils' STRING_ISGRAPHIC: printable ASCII and tab
// always, every whitespace character under -w, and the whole upper half under
// the 8-bit encoding.
func (c *stringsConfig) printable(b byte) bool {
	switch {
	case b == '\t':
		return true
	case b >= 32 && b < 127:
		return true
	case c.allWhite && (b == '\n' || b == '\v' || b == '\f' || b == '\r'):
		return true
	case c.eightBit && b > 127:
		return true
	}
	return false
}

// setEncoding maps -e's letter onto a character width and byte order. 's' is
// binutils' default single-byte scan, 'S' the same width with the high half
// treated as printable, and b/l/B/L the 16- and 32-bit forms.
func (c *stringsConfig) setEncoding(name string) bool {
	if len(name) != 1 {
		return false
	}
	c.width, c.bigEndian, c.eightBit = 1, false, false
	switch name[0] {
	case 's':
	case 'S':
		c.eightBit = true
	case 'b':
		c.width, c.bigEndian = 2, true
	case 'l':
		c.width = 2
	case 'B':
		c.width, c.bigEndian = 4, true
	case 'L':
		c.width = 4
	default:
		return false
	}
	return true
}

func cmdStrings(args []string) int {
	cfg := stringsConfig{minimum: 4, separator: "\n", width: 1}
	names := []string{}
	// Argument of a short option: the rest of the cluster if there is one,
	// otherwise the next operand, as getopt hands it over.
	takeValue := func(rest string, i *int) (string, bool) {
		if rest != "" {
			return rest, true
		}
		*i++
		if *i >= len(args) {
			return "", false
		}
		return args[*i], true
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, "--"):
			name, value, hasValue := arg[2:], "", false
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name, value, hasValue = name[:eq], name[eq+1:], true
			}
			needValue := func() (string, bool) {
				if hasValue {
					return value, true
				}
				i++
				if i >= len(args) {
					return "", false
				}
				return args[i], true
			}
			switch name {
			case "all":
				cfg.dataOnly = false
			case "data":
				cfg.dataOnly = true
			case "print-file-name":
				cfg.printName = true
			case "include-all-whitespace":
				cfg.allWhite = true
			case "bytes":
				v, ok := needValue()
				if !ok {
					return stringsUsage()
				}
				n, err := strconv.Atoi(v)
				if err != nil {
					fatalf("strings", "invalid integer argument %s", v)
					return 1
				}
				cfg.minimum = n
			case "radix":
				v, ok := needValue()
				if !ok || len(v) != 1 || strings.IndexByte("odx", v[0]) < 0 {
					return stringsUsage()
				}
				cfg.radix = v[0]
			case "output-separator":
				v, ok := needValue()
				if !ok {
					return stringsUsage()
				}
				cfg.separator = v
			case "encoding":
				v, ok := needValue()
				if !ok || !cfg.setEncoding(v) {
					return stringsUsage()
				}
			case "target":
				if _, ok := needValue(); !ok {
					return stringsUsage()
				}
			case "help":
				return stringsHelp(os.Stdout, 0)
			default:
				fatalf("strings", "unsupported option %q", arg)
				return 1
			}
		case len(arg) > 1 && arg[0] == '-':
			cluster := arg[1:]
			for len(cluster) > 0 {
				letter := cluster[0]
				cluster = cluster[1:]
				switch letter {
				case 'a':
					cfg.dataOnly = false
				case 'd':
					cfg.dataOnly = true
				case 'f':
					cfg.printName = true
				case 'w':
					cfg.allWhite = true
				case 'o':
					cfg.radix = 'o'
				case 'n':
					v, ok := takeValue(cluster, &i)
					if !ok {
						return stringsUsage()
					}
					n, err := strconv.Atoi(v)
					if err != nil {
						fatalf("strings", "invalid integer argument %s", v)
						return 1
					}
					cfg.minimum, cluster = n, ""
				case 't':
					v, ok := takeValue(cluster, &i)
					if !ok || len(v) != 1 || strings.IndexByte("odx", v[0]) < 0 {
						return stringsUsage()
					}
					cfg.radix, cluster = v[0], ""
				case 's':
					v, ok := takeValue(cluster, &i)
					if !ok {
						return stringsUsage()
					}
					cfg.separator, cluster = v, ""
				case 'e':
					v, ok := takeValue(cluster, &i)
					if !ok || !cfg.setEncoding(v) {
						return stringsUsage()
					}
					cluster = ""
				case 'T':
					if _, ok := takeValue(cluster, &i); !ok {
						return stringsUsage()
					}
					cluster = ""
				case 'h':
					return stringsHelp(os.Stdout, 0)
				default:
					// A bare digit count, as in the historic "strings -8".
					if n, err := strconv.Atoi(arg[1:]); err == nil {
						cfg.minimum, cluster = n, ""
						continue
					}
					fatalf("strings", "unsupported option %q", arg)
					return 1
				}
			}
		default:
			names = append(names, arg)
		}
	}
	if cfg.minimum < 1 {
		fatalf("strings", "minimum string length is too small: %d", cfg.minimum)
		return 0
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush() //nolint:errcheck // a sticky write error is reported by the final Flush.
	if len(names) == 0 {
		data, err := readInputBytes("-")
		if err != nil {
			fatalf("strings", "%v", err)
			return 1
		}
		stringsScanAll(out, &cfg, "{standard input}", data)
		return 0
	}
	status := 0
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			out.Flush() //nolint:errcheck // ordering the diagnostic after pending output; errors surface at the deferred Flush.
			status = 1
			// binutils reports a missing file and a directory through BFD,
			// with its wording, and everything else through errno.
			switch {
			case errors.Is(err, os.ErrNotExist):
				fatalf("strings", "'%s': No such file", name)
			case errors.Is(err, syscall.EISDIR):
				fatalf("strings", "Warning: '%s' is a directory", name)
			default:
				fatalf("strings", "%s: %s", name, errText(err))
			}
			continue
		}
		stringsScanAll(out, &cfg, name, data)
	}
	return status
}

// stringsUsage reports a malformed command line the way binutils' strings
// does: the usage text, and a failing status.
func stringsUsage() int {
	return stringsHelp(os.Stderr, 1)
}

func stringsHelp(w io.Writer, status int) int {
	if err := writeAppletHelp(w, "strings"); err != nil {
		fatalf("strings", "%v", err)
		return 1
	}
	return status
}

// stringsScanAll scans one file: the whole image, or — under -d and only when
// the file really is an ELF object — the sections BFD calls loaded data, which
// is every allocated section that carries bytes. That includes .text: -d drops
// the sections a loader never maps (symbol tables, comments, debug info), not
// the code. A -d scan of anything else falls back to the whole file, as BFD's
// binary target does.
func stringsScanAll(out io.Writer, cfg *stringsConfig, name string, data []byte) {
	if cfg.dataOnly {
		if file, err := elf.NewFile(bytes.NewReader(data)); err == nil {
			for _, section := range file.Sections {
				if section.Flags&elf.SHF_ALLOC == 0 || section.Type == elf.SHT_NOBITS {
					continue
				}
				end := section.Offset + section.Size
				if section.Offset > uint64(len(data)) || end > uint64(len(data)) {
					continue
				}
				stringsScan(out, cfg, name, data[section.Offset:end], int64(section.Offset)) //nolint:gosec // section bounds were checked against the file length.
			}
			return
		}
	}
	stringsScan(out, cfg, name, data, 0)
}

// stringsScan walks one region looking for runs of printable characters, where
// a character is one, two or four bytes wide. base is the region's offset in
// the file, so -t reports file offsets even for a per-section scan.
//
// Multi-byte encodings are not scanned on character boundaries: binutils pushes
// back all but the first byte of a character that turns out not to be printable
// and retries one byte later, so a UTF-16 string that begins at an odd offset is
// still found. The scan therefore restarts at the byte after the character that
// ended a run, which for the single-byte encodings is the usual "resume after
// the terminator".
func stringsScan(out io.Writer, cfg *stringsConfig, name string, data []byte, base int64) {
	width := cfg.width
	// charAt decodes the character starting at byte i, reporting whether it
	// fits in a byte and is printable under the current encoding.
	charAt := func(i int) (byte, bool) {
		var value uint32
		for j := 0; j < width; j++ {
			if cfg.bigEndian {
				value = value<<8 | uint32(data[i+j])
			} else {
				value |= uint32(data[i+j]) << (8 * uint(j)) //nolint:gosec // j is bounded by the 4-byte character width.
			}
		}
		return byte(value), value <= 0xff && cfg.printable(byte(value))
	}
	for i := 0; i+width <= len(data); {
		b, ok := charAt(i)
		if !ok {
			i++
			continue
		}
		run := []byte{b}
		j := i + width
		for ; j+width <= len(data); j += width {
			b, ok := charAt(j)
			if !ok {
				break
			}
			run = append(run, b)
		}
		if len(run) >= cfg.minimum {
			if cfg.printName {
				fmt.Fprintf(out, "%s: ", name)
			}
			switch cfg.radix {
			case 'o':
				fmt.Fprintf(out, "%7o ", base+int64(i))
			case 'd':
				fmt.Fprintf(out, "%7d ", base+int64(i))
			case 'x':
				fmt.Fprintf(out, "%7x ", base+int64(i))
			}
			out.Write(run) //nolint:errcheck // buffered writer; the error surfaces at Flush.
			io.WriteString(out, cfg.separator)
		}
		i = j + 1
	}
}

// dumpWord reads size bytes little-endian starting at off, zero-extending
// past both the end of data and (for od's -j/-N and hexdump's -s/-n) the
// selected region, matching od/hexdump's treatment of a trailing partial
// word.
func dumpWord(data []byte, off, size int) uint64 {
	var v uint64
	for i := 0; i < size && off+i < len(data); i++ {
		v |= uint64(data[off+i]) << (8 * uint(i))
	}
	return v
}

// dumpEscapeC renders one byte the way od/hexdump -c do: the C escapes for
// the control characters that have one, a literal character for printable
// ASCII, backslash-escaped for a literal backslash, and 3-digit zero-padded
// octal for everything else.
func dumpEscapeC(b byte) string {
	switch b {
	case 0:
		return `\0`
	case 7:
		return `\a`
	case 8:
		return `\b`
	case 9:
		return `\t`
	case 10:
		return `\n`
	case 11:
		return `\v`
	case 12:
		return `\f`
	case 13:
		return `\r`
	case '\\':
		return `\\`
	}
	if b >= 32 && b < 127 {
		return string(rune(b))
	}
	return fmt.Sprintf("%03o", b)
}

var dumpControlMnemonics = [32]string{
	"nul", "soh", "stx", "etx", "eot", "enq", "ack", "bel",
	"bs", "ht", "nl", "vt", "ff", "cr", "so", "si",
	"dle", "dc1", "dc2", "dc3", "dc4", "nak", "syn", "etb",
	"can", "em", "sub", "esc", "fs", "gs", "rs", "us",
}

// dumpEscapeA renders one byte the way od -a does: it strips the high bit
// (od's traditional 7-bit "meta" convention) and then uses named mnemonics
// for the C0 control codes, "sp"/"del" for space and DEL, or the literal
// character for everything else printable.
func dumpEscapeA(b byte) string {
	b &= 0x7f
	switch {
	case b < 32:
		return dumpControlMnemonics[b]
	case b == 32:
		return "sp"
	case b == 127:
		return "del"
	default:
		return string(rune(b))
	}
}

// dumpFormatAddr zero-pads addr to at least width digits in the given radix,
// growing past that width for an address too large to fit (the same
// behaviour Go's own %0*o/%0*x verbs give for free). radix 0 means the
// address column is suppressed (od's -An).
func dumpFormatAddr(radix, width, addr int) string {
	switch radix {
	case 8:
		return fmt.Sprintf("%0*o", width, addr)
	case 10:
		return fmt.Sprintf("%0*d", width, addr)
	case 16:
		return fmt.Sprintf("%0*x", width, addr)
	default:
		return ""
	}
}

// dumpRenderer describes one od/hexdump output format: how many bytes make
// up a word, how a word (or, for byteField, a single byte) renders as a
// fixed-width field, and how the address column and any trailing padding or
// ASCII gutter are drawn.
type dumpRenderer struct {
	unitSize    int
	wordField   func(uint64) string
	byteField   func(byte) string
	blank       string
	addrRadix   int
	addrWidth   int
	trailingPad bool
	ascii       bool
	// suppressEmptyFinal skips the final address-only line when the selected
	// region is empty, matching hexdump (od always prints it, even at 0).
	suppressEmptyFinal bool
}

// runDump drives the shared od/hexdump display loop: 16 bytes per output
// line, a run of identical full lines collapsed to a single "*" (disabled by
// dedup=false, od's -v), and a final address-only line giving the end of the
// selected region.
func runDump(data []byte, skip, limit int, dedup bool, r dumpRenderer) {
	end := len(data)
	if limit >= 0 && skip+limit < end {
		end = skip + limit
	}
	if skip > end {
		skip = end
	}
	const perLine = 16
	var lastContent string
	haveLast, skippedStar := false, false
	for off := skip; off < end; off += perLine {
		lineEnd := off + perLine
		if lineEnd > end {
			lineEnd = end
		}
		var body strings.Builder
		if r.byteField != nil {
			i := off
			for ; i < lineEnd; i++ {
				body.WriteString(r.byteField(data[i]))
			}
			for ; r.trailingPad && i < off+perLine; i++ {
				body.WriteString(r.blank)
			}
		} else {
			i := off
			for ; i < lineEnd; i += r.unitSize {
				body.WriteString(r.wordField(dumpWord(data, i, r.unitSize)))
			}
			for ; r.trailingPad && i < off+perLine; i += r.unitSize {
				body.WriteString(r.blank)
			}
		}
		content := body.String()
		if r.ascii {
			var gutter strings.Builder
			for _, b := range data[off:lineEnd] {
				if b >= 32 && b < 127 {
					gutter.WriteByte(b)
				} else {
					gutter.WriteByte('.')
				}
			}
			content += " |" + gutter.String() + "|"
		}
		if dedup && lineEnd-off == perLine && haveLast && content == lastContent {
			if !skippedStar {
				fmt.Println("*")
				skippedStar = true
			}
			continue
		}
		skippedStar, haveLast, lastContent = false, true, content
		fmt.Println(dumpFormatAddr(r.addrRadix, r.addrWidth, off) + content)
	}
	suppressFinal := r.suppressEmptyFinal && (len(data) == 0 || limit == 0)
	if r.addrRadix != 0 && !suppressFinal {
		fmt.Println(dumpFormatAddr(r.addrRadix, r.addrWidth, end))
	}
}

// readDumpInputs concatenates every named operand (or stdin for "-"/none)
// into one byte stream, the way od and hexdump treat multiple file operands.
func readDumpInputs(prog string, names []string) ([]byte, int) {
	if len(names) == 0 {
		names = []string{"-"}
	}
	var data []byte
	for _, name := range names {
		b, err := readInputBytes(name)
		if err != nil {
			fatalf(prog, "%s: %v", name, errText(err))
			return nil, 1
		}
		data = append(data, b...)
	}
	return data, 0
}

func cmdOd(args []string) int {
	args = expandShortOptions(args, "AjN")
	addrRadix, addrWidth := 8, 7
	skip, limit := 0, -1
	dedup := true
	unitSize, signed := 2, false
	renderMode := "o" // o, x, d(unsigned), i(signed), c, a
	var files []string
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func(opt string) (string, bool) {
			i++
			if i >= len(args) {
				fatalf("od", "option '%s' requires an argument", opt)
				return "", false
			}
			return args[i], true
		}
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && arg == "-b":
			unitSize, signed, renderMode = 1, false, "o"
		case parsing && arg == "-c":
			renderMode = "c"
		case parsing && arg == "-a":
			renderMode = "a"
		case parsing && arg == "-d":
			unitSize, signed, renderMode = 2, false, "d"
		case parsing && arg == "-o":
			unitSize, signed, renderMode = 2, false, "o"
		case parsing && arg == "-x":
			unitSize, signed, renderMode = 2, false, "x"
		case parsing && arg == "-i":
			unitSize, signed, renderMode = 4, true, "d"
		case parsing && (arg == "-v" || arg == "--output-duplicates"):
			dedup = false
		case parsing && arg == "-A":
			v, ok := next("-A")
			if !ok {
				return 1
			}
			switch v {
			case "o", "octal":
				addrRadix, addrWidth = 8, 7
			case "d", "decimal":
				addrRadix, addrWidth = 10, 7
			case "x", "hex", "hexadecimal":
				addrRadix, addrWidth = 16, 6
			case "n", "none":
				addrRadix, addrWidth = 0, 0
			default:
				fatalf("od", "invalid address radix '%s'", v)
				return 1
			}
		case parsing && arg == "-j":
			v, ok := next("-j")
			if !ok {
				return 1
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				fatalf("od", "invalid skip count '%s'", v)
				return 1
			}
			skip = n
		case parsing && arg == "-N":
			v, ok := next("-N")
			if !ok {
				return 1
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				fatalf("od", "invalid byte count '%s'", v)
				return 1
			}
			limit = n
		case parsing && len(arg) > 1 && arg[0] == '-' && arg != "-":
			fatalf("od", "invalid option '%s'", arg)
			return 1
		default:
			files = append(files, arg)
		}
	}

	data, status := readDumpInputs("od", files)
	if status != 0 {
		return status
	}

	r := dumpRenderer{addrRadix: addrRadix, addrWidth: addrWidth}
	switch renderMode {
	case "c":
		r.byteField = func(b byte) string { return fmt.Sprintf(" %3s", dumpEscapeC(b)) }
		r.blank = "    "
	case "a":
		r.byteField = func(b byte) string { return fmt.Sprintf(" %3s", dumpEscapeA(b)) }
		r.blank = "    "
	case "d":
		r.unitSize = unitSize
		if signed {
			r.wordField = func(v uint64) string { return fmt.Sprintf(" %11d", int32(v)) } //nolint:gosec // G115: intentional truncation to the 4-byte od -i word
		} else {
			r.wordField = func(v uint64) string { return fmt.Sprintf(" %5d", v) }
		}
	case "x":
		r.unitSize = unitSize
		r.wordField = func(v uint64) string { return fmt.Sprintf(" %04x", v) }
	default: // "o"
		r.unitSize = unitSize
		if unitSize == 1 {
			r.wordField = func(v uint64) string { return fmt.Sprintf(" %03o", v) }
		} else {
			r.wordField = func(v uint64) string { return fmt.Sprintf(" %06o", v) }
		}
	}
	runDump(data, skip, limit, dedup, r)
	return 0
}

func cmdHexdump(args []string) int {
	args = expandShortOptions(args, "ns")
	skip, limit := 0, -1
	renderMode := "" // "" (default), C, c, b, d, o, x
	var files []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func(opt string) (string, bool) {
			i++
			if i >= len(args) {
				fatalf("hexdump", "option '%s' requires an argument", opt)
				return "", false
			}
			return args[i], true
		}
		switch arg {
		case "-C":
			renderMode = "C"
		case "-c":
			renderMode = "c"
		case "-b":
			renderMode = "b"
		case "-d":
			renderMode = "d"
		case "-o":
			renderMode = "o"
		case "-x":
			renderMode = "x"
		case "-n":
			v, ok := next("-n")
			if !ok {
				return 1
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				fatalf("hexdump", "invalid length '%s'", v)
				return 1
			}
			limit = n
		case "-s":
			v, ok := next("-s")
			if !ok {
				return 1
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				fatalf("hexdump", "invalid offset '%s'", v)
				return 1
			}
			skip = n
		default:
			if len(arg) > 1 && arg[0] == '-' && arg != "-" {
				fatalf("hexdump", "invalid option '%s'", arg)
				return 1
			}
			files = append(files, arg)
		}
	}

	data, status := readDumpInputs("hexdump", files)
	if status != 0 {
		return status
	}

	r := dumpRenderer{addrRadix: 16, addrWidth: 7, trailingPad: true, suppressEmptyFinal: true}
	switch renderMode {
	case "C":
		r.addrWidth, r.ascii = 8, true
		r.byteField = func(b byte) string { return fmt.Sprintf(" %02x", b) }
		r.blank = "   "
		// The 8th/9th byte gets an extra gap; runDump has no per-index hook
		// for that, so build this one format directly instead of through it.
		runDumpHexdumpCanonical(data, skip, limit)
		return 0
	case "c":
		r.byteField = func(b byte) string { return fmt.Sprintf(" %3s", dumpEscapeC(b)) }
		r.blank = "    "
	case "b":
		r.byteField = func(b byte) string { return fmt.Sprintf(" %03o", b) }
		r.blank = "    "
	case "d":
		r.unitSize = 2
		r.wordField = func(v uint64) string { return fmt.Sprintf("%8s", fmt.Sprintf("%05d", v)) }
		r.blank = "        "
	case "o":
		r.unitSize = 2
		r.wordField = func(v uint64) string { return fmt.Sprintf("%8s", fmt.Sprintf("%06o", v)) }
		r.blank = "        "
	case "x":
		r.unitSize = 2
		r.wordField = func(v uint64) string { return fmt.Sprintf("%8s", fmt.Sprintf("%04x", v)) }
		r.blank = "        "
	default:
		r.unitSize = 2
		r.wordField = func(v uint64) string { return fmt.Sprintf(" %04x", v) }
		r.blank = "     "
	}
	runDump(data, skip, limit, true, r)
	return 0
}

// runDumpHexdumpCanonical implements hexdump -C's fixed 16-bytes-per-line
// layout (two 8-byte hex groups separated by an extra space, then the ASCII
// gutter), which needs a mid-line gap runDump's uniform field loop cannot
// express.
func runDumpHexdumpCanonical(data []byte, skip, limit int) {
	end := len(data)
	if limit >= 0 && skip+limit < end {
		end = skip + limit
	}
	if skip > end {
		skip = end
	}
	const perLine = 16
	var lastLine string
	haveLast, skippedStar := false, false
	for off := skip; off < end; off += perLine {
		lineEnd := off + perLine
		if lineEnd > end {
			lineEnd = end
		}
		var body strings.Builder
		for i := 0; i < perLine; i++ {
			if i == 8 {
				body.WriteByte(' ')
			}
			if off+i < lineEnd {
				fmt.Fprintf(&body, "%02x ", data[off+i])
			} else {
				body.WriteString("   ")
			}
		}
		body.WriteString(" |")
		for _, b := range data[off:lineEnd] {
			if b >= 32 && b < 127 {
				body.WriteByte(b)
			} else {
				body.WriteByte('.')
			}
		}
		body.WriteByte('|')
		content := body.String()
		if lineEnd-off == perLine && haveLast && content == lastLine {
			if !skippedStar {
				fmt.Println("*")
				skippedStar = true
			}
			continue
		}
		skippedStar, haveLast, lastLine = false, true, content
		fmt.Printf("%08x  %s\n", off, content)
	}
	if len(data) != 0 && limit != 0 {
		fmt.Printf("%08x\n", end)
	}
}

func cmdDiff(args []string) int {
	unified, brief, reportSame := false, false, false
	ignoreCase, ignoreAllSpace, ignoreSpaceChange := false, false, false
	treatMissingAsEmpty := false
	var labels []string
	var files []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-u" || arg == "--unified":
			unified = true
		case arg == "-q" || arg == "--brief":
			brief = true
		case arg == "-s" || arg == "--report-identical-files":
			reportSame = true
		case arg == "-i" || arg == "--ignore-case":
			ignoreCase = true
		case arg == "-w" || arg == "--ignore-all-space":
			ignoreAllSpace = true
		case arg == "-b" || arg == "--ignore-space-change":
			ignoreSpaceChange = true
		case arg == "-N" || arg == "--new-file":
			treatMissingAsEmpty = true
		case arg == "--label":
			i++
			if i >= len(args) {
				fatalf("diff", "option '--label' requires an argument")
				return 2
			}
			labels = append(labels, args[i])
		case strings.HasPrefix(arg, "--label="):
			labels = append(labels, strings.TrimPrefix(arg, "--label="))
		case len(arg) > 1 && arg[0] == '-' && arg != "-":
			fatalf("diff", "unsupported option '%s'", arg)
			return 2
		default:
			files = append(files, arg)
		}
	}
	if len(files) != 2 {
		fatalf("diff", "expected two files")
		return 2
	}

	a, noNewlineA, e := readLines(files[0], treatMissingAsEmpty)
	if e != nil {
		fatalf("diff", "%s: %v", files[0], errText(e))
		return 2
	}
	b, noNewlineB, e := readLines(files[1], treatMissingAsEmpty)
	if e != nil {
		fatalf("diff", "%s: %v", files[1], errText(e))
		return 2
	}

	normalize := diffNormalizer(ignoreCase, ignoreAllSpace, ignoreSpaceChange)
	ops := diffLCS(diffKeyLines(a, noNewlineA), diffKeyLines(b, noNewlineB), a, b, normalize)
	changed := false
	for _, op := range ops {
		if op.kind != '=' {
			changed = true
			break
		}
	}

	if !changed {
		if reportSame {
			fmt.Printf("Files %s and %s are identical\n", files[0], files[1])
		}
		return 0
	}
	if brief {
		fmt.Printf("Files %s and %s differ\n", files[0], files[1])
		return 1
	}

	nl := diffNewlineInfo{noNewlineA, len(a) - 1, noNewlineB, len(b) - 1}
	if unified {
		headerA, headerB := files[0], files[1]
		if len(labels) > 0 {
			headerA = labels[0]
		} else {
			headerA += "\t" + diffFileTimestamp(files[0])
		}
		if len(labels) > 1 {
			headerB = labels[1]
		} else {
			headerB += "\t" + diffFileTimestamp(files[1])
		}
		printUnifiedDiff(headerA, headerB, ops, nl)
	} else {
		printNormalDiff(ops, nl)
	}
	return 1
}

// diffNewlineInfo records whether each file's last line lacked a trailing
// newline, and which line (by 0-indexed position) that was, so the
// renderers know where to print diff's "\ No newline at end of file" marker.
type diffNewlineInfo struct {
	noNewlineA bool
	lastA      int
	noNewlineB bool
	lastB      int
}

func (nl diffNewlineInfo) markAfter(op diffOp) bool {
	if nl.noNewlineA && op.kind != '+' && op.aPos == nl.lastA {
		return true
	}
	if nl.noNewlineB && op.kind != '-' && op.bPos == nl.lastB {
		return true
	}
	return false
}

// diffKeyLines returns the comparison keys for a set of lines: identical to
// the lines themselves, except the very last one gets a sentinel appended
// when it lacks a trailing newline. That makes two otherwise-identical last
// lines compare unequal whenever only one side is missing its newline,
// matching diff's own behaviour, while leaving every other line untouched.
func diffKeyLines(lines []string, noNewline bool) []string {
	if !noNewline || len(lines) == 0 {
		return lines
	}
	keyed := append([]string{}, lines...)
	keyed[len(keyed)-1] += "\x00no-newline"
	return keyed
}

// readLines splits a file into its lines, dropping exactly one trailing
// newline the way diff's line-oriented comparison does, and reports whether
// that trailing newline was actually present. With -N, a missing file reads
// as empty instead of failing, so a new/deleted file can still be diffed
// against its counterpart.
func readLines(name string, missingIsEmpty bool) ([]string, bool, error) {
	data, e := readInputBytes(name)
	if e != nil {
		if missingIsEmpty && errors.Is(e, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, e
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	noTrailingNewline := data[len(data)-1] != '\n'
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), noTrailingNewline, nil
}

// diffNormalizer builds the per-line comparison key -i/-w/-b apply before
// the LCS runs; the original line text is always what gets displayed.
func diffNormalizer(ignoreCase, ignoreAllSpace, ignoreSpaceChange bool) func(string) string {
	return func(s string) string {
		switch {
		case ignoreAllSpace:
			s = strings.NewReplacer(" ", "", "\t", "").Replace(s)
		case ignoreSpaceChange:
			s = diffCollapseSpace(strings.TrimRight(s, " \t"))
		}
		if ignoreCase {
			s = strings.ToLower(s)
		}
		return s
	}
}

// diffCollapseSpace replaces every run of spaces/tabs with a single space,
// matching diff -b's "ignore changes in the amount of white space" (a run of
// whitespace where the other side has none is still a difference; only the
// run's length is ignored).
func diffCollapseSpace(s string) string {
	var out strings.Builder
	inRun := false
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if !inRun {
				out.WriteByte(' ')
				inRun = true
			}
			continue
		}
		inRun = false
		out.WriteByte(s[i])
	}
	return out.String()
}

// diffOp is one line of an LCS edit script: '=' unchanged (aLine==bLine),
// '-' present only in a, '+' present only in b.
// diffOp is one line of an LCS edit script: '=' unchanged (present in both,
// at aPos/bPos), '-' present only in a (at aPos), '+' present only in b (at
// bPos, anchored to aPos, the a-position it was inserted before --
// consecutive inserts between two aPos-bearing ops share it).
type diffOp struct {
	kind  byte
	aLine string
	bLine string
	aPos  int
	bPos  int
}

// diffLCS aligns keyLinesA/keyLinesB with a classic O(n*m) LCS, comparing
// through norm (so -i/-w/-b, and diffKeyLines's no-trailing-newline
// sentinel, all affect equality) while recording the corresponding
// displayA/displayB text for output. Inputs too large for the O(n*m) table
// fall back to "everything changed" rather than exhausting memory.
func diffLCS(keyLinesA, keyLinesB, displayA, displayB []string, norm func(string) string) []diffOp {
	n, m := len(keyLinesA), len(keyLinesB)
	if n > 0 && m > 4_000_000/n {
		ops := make([]diffOp, 0, n+m)
		for i, line := range displayA {
			ops = append(ops, diffOp{'-', line, "", i, m})
		}
		for j, line := range displayB {
			ops = append(ops, diffOp{'+', "", line, n, j})
		}
		return ops
	}
	keyA := make([]string, n)
	for i, line := range keyLinesA {
		keyA[i] = norm(line)
	}
	keyB := make([]string, m)
	for j, line := range keyLinesB {
		keyB[j] = norm(line)
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if keyA[i] == keyB[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffOp
	for i, j := 0, 0; i < n || j < m; {
		if i < n && j < m && keyA[i] == keyB[j] {
			ops = append(ops, diffOp{'=', displayA[i], displayB[j], i, j})
			i++
			j++
		} else if i < n && (j == m || dp[i+1][j] >= dp[i][j+1]) {
			// On a tie, prefer deleting before inserting: a replaced block
			// then prints its "-old" lines before its "+new" lines, matching
			// GNU diff's convention for a change hunk.
			ops = append(ops, diffOp{'-', displayA[i], "", i, j})
			i++
		} else {
			ops = append(ops, diffOp{'+', "", displayB[j], i, j})
			j++
		}
	}
	return ops
}

// diffChange is one maximal run of non-equal ops, with the 0-indexed,
// half-open line ranges it spans in each file.
type diffChange struct {
	aStart, aEnd int
	bStart, bEnd int
	ops          []diffOp
}

// diffChanges groups an LCS edit script into the change regions diff's
// output formats are built from, one per contiguous run of '-'/'+' ops.
func diffChanges(ops []diffOp) []diffChange {
	var changes []diffChange
	aPos, bPos := 0, 0
	i := 0
	for i < len(ops) {
		if ops[i].kind == '=' {
			aPos++
			bPos++
			i++
			continue
		}
		start := i
		aStart, bStart := aPos, bPos
		for i < len(ops) && ops[i].kind != '=' {
			if ops[i].kind == '-' {
				aPos++
			} else {
				bPos++
			}
			i++
		}
		changes = append(changes, diffChange{aStart, aPos, bStart, bPos, ops[start:i]})
	}
	return changes
}

// diffRange formats a 1-indexed line range the way diff's normal and
// unified formats do: a bare number when it spans one line (or none, for
// unified's "N,0"-style empty side), "start,end" otherwise.
func diffRange(start, end int) string {
	if end-start <= 1 {
		return strconv.Itoa(start + 1)
	}
	return fmt.Sprintf("%d,%d", start+1, end)
}

// printNormalDiff renders diff's classic default format: one ed-style
// "NaN"/"NdN"/"NcN" header per change region, with no surrounding context.
func printNormalDiff(ops []diffOp, nl diffNewlineInfo) {
	for _, c := range diffChanges(ops) {
		switch {
		case c.aEnd == c.aStart:
			fmt.Printf("%da%s\n", c.aStart, diffRange(c.bStart, c.bEnd))
		case c.bEnd == c.bStart:
			fmt.Printf("%s%s%d\n", diffRange(c.aStart, c.aEnd), "d", c.bStart)
		default:
			fmt.Printf("%sc%s\n", diffRange(c.aStart, c.aEnd), diffRange(c.bStart, c.bEnd))
		}
		for _, op := range c.ops {
			if op.kind == '-' {
				fmt.Println("< " + op.aLine)
				if nl.markAfter(op) {
					fmt.Println(`\ No newline at end of file`)
				}
			}
		}
		if c.aEnd != c.aStart && c.bEnd != c.bStart {
			fmt.Println("---")
		}
		for _, op := range c.ops {
			if op.kind == '+' {
				fmt.Println("> " + op.bLine)
				if nl.markAfter(op) {
					fmt.Println(`\ No newline at end of file`)
				}
			}
		}
	}
}

const diffContext = 3

// printUnifiedDiff renders diff -u: a --- /+++ header line per file (the
// caller has already decided whether that's "name\ttimestamp" or a bare
// --label) followed by one or more @@ hunks, each showing up to diffContext
// lines of unchanged context around its changes; hunks within 2*diffContext
// lines of each other merge into one, matching GNU diff's hunk-splitting.
func printUnifiedDiff(headerA, headerB string, ops []diffOp, nl diffNewlineInfo) {
	fmt.Printf("--- %s\n", headerA)
	fmt.Printf("+++ %s\n", headerB)
	changes := diffChanges(ops)
	i := 0
	for i < len(changes) {
		aFrom := changes[i].aStart - diffContext
		j := i
		aTo := changes[i].aEnd
		for j+1 < len(changes) && changes[j+1].aStart-aTo <= 2*diffContext {
			j++
			aTo = changes[j].aEnd
		}
		aTo += diffContext
		if aFrom < 0 {
			aFrom = 0
		}

		var body strings.Builder
		aLen, bLen, bFrom := 0, 0, -1
		for _, op := range ops {
			var included bool
			if op.kind == '+' {
				included = op.aPos >= aFrom && op.aPos <= aTo
			} else {
				included = op.aPos >= aFrom && op.aPos < aTo
			}
			if !included {
				continue
			}
			if bFrom < 0 {
				bFrom = op.bPos
			}
			switch op.kind {
			case '=':
				body.WriteString(" " + op.aLine + "\n")
				aLen++
				bLen++
			case '-':
				body.WriteString("-" + op.aLine + "\n")
				aLen++
			case '+':
				body.WriteString("+" + op.bLine + "\n")
				bLen++
			}
			if nl.markAfter(op) {
				body.WriteString("\\ No newline at end of file\n")
			}
		}

		fmt.Printf("@@ -%s +%s @@\n", diffHunkRange(aFrom, aLen), diffHunkRange(bFrom, bLen))
		fmt.Print(body.String())
		i = j + 1
	}
}

func diffHunkRange(start, length int) string {
	if length == 1 {
		return strconv.Itoa(start + 1)
	}
	return fmt.Sprintf("%d,%d", start+1, length)
}

// diffFileTimestamp renders a file's modification time the way diff -u's
// header does: "YYYY-MM-DD HH:MM:SS.nnnnnnnnn +ZZZZ".
func diffFileTimestamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return info.ModTime().Format("2006-01-02 15:04:05.000000000 -0700")
}

func cmdExpr(args []string) int {
	if len(args) == 0 {
		fatalf("expr", "missing operand")
		return 2
	}
	p := &exprParser{tokens: args}
	v, err := p.parseOr()
	if err != nil || p.pos != len(args) {
		fatalf("expr", "syntax error")
		return 2
	}
	fmt.Println(v)
	if v == "" || v == "0" {
		return 1
	}
	return 0
}

type exprParser struct {
	tokens []string
	pos    int
}

func (p *exprParser) take(s string) bool {
	if p.pos < len(p.tokens) && p.tokens[p.pos] == s {
		p.pos++
		return true
	}
	return false
}
func (p *exprParser) parseOr() (string, error) {
	v, e := p.parseAnd()
	for e == nil && p.take("|") {
		r, x := p.parseAnd()
		e = x
		if v == "" || v == "0" {
			v = r
		}
	}
	return v, e
}
func (p *exprParser) parseAnd() (string, error) {
	v, e := p.parseCmp()
	for e == nil && p.take("&") {
		r, x := p.parseCmp()
		e = x
		if v == "" || v == "0" || r == "" || r == "0" {
			v = "0"
		}
	}
	return v, e
}
func (p *exprParser) parseCmp() (string, error) {
	v, e := p.parseAdd()
	if e != nil || p.pos >= len(p.tokens) {
		return v, e
	}
	op := p.tokens[p.pos]
	if !strings.ContainsRune("=<>!", rune(op[0])) {
		return v, nil
	}
	p.pos++
	r, e := p.parseAdd()
	if e != nil {
		return "", e
	}
	cmp := strings.Compare(v, r)
	if a, x := strconv.ParseInt(v, 10, 64); x == nil {
		if b, y := strconv.ParseInt(r, 10, 64); y == nil {
			cmp = 0
			if a < b {
				cmp = -1
			} else if a > b {
				cmp = 1
			}
		}
	}
	ok := op == "=" || op == "==" && cmp == 0
	switch op {
	case "=", "==":
		ok = cmp == 0
	case "!=":
		ok = cmp != 0
	case "<":
		ok = cmp < 0
	case "<=":
		ok = cmp <= 0
	case ">":
		ok = cmp > 0
	case ">=":
		ok = cmp >= 0
	}
	if ok {
		return "1", nil
	}
	return "0", nil
}
func (p *exprParser) parseAdd() (string, error) {
	v, e := p.parseMul()
	for e == nil && p.pos < len(p.tokens) && (p.tokens[p.pos] == "+" || p.tokens[p.pos] == "-") {
		op := p.tokens[p.pos]
		p.pos++
		r, x := p.parseMul()
		if x != nil {
			return "", x
		}
		a, x := strconv.ParseInt(v, 10, 64)
		if x != nil {
			return "", x
		}
		b, x := strconv.ParseInt(r, 10, 64)
		if x != nil {
			return "", x
		}
		if op == "+" {
			a += b
		} else {
			a -= b
		}
		v = strconv.FormatInt(a, 10)
	}
	return v, e
}
func (p *exprParser) parseMul() (string, error) {
	v, e := p.parsePrimary()
	for e == nil && p.pos < len(p.tokens) && len(p.tokens[p.pos]) == 1 && strings.ContainsRune("*/%", rune(p.tokens[p.pos][0])) {
		op := p.tokens[p.pos]
		p.pos++
		r, x := p.parsePrimary()
		if x != nil {
			return "", x
		}
		a, x := strconv.ParseInt(v, 10, 64)
		if x != nil {
			return "", x
		}
		b, x := strconv.ParseInt(r, 10, 64)
		if x != nil || b == 0 {
			return "", fmt.Errorf("invalid arithmetic")
		}
		switch op {
		case "*":
			a *= b
		case "/":
			a /= b
		case "%":
			a %= b
		}
		v = strconv.FormatInt(a, 10)
	}
	return v, e
}
func (p *exprParser) parsePrimary() (string, error) {
	if p.take("(") {
		v, e := p.parseOr()
		if !p.take(")") {
			return "", fmt.Errorf("missing )")
		}
		return v, e
	}
	if p.pos >= len(p.tokens) {
		return "", fmt.Errorf("missing")
	}
	v := p.tokens[p.pos]
	p.pos++
	return v, nil
}
