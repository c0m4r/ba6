// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
)

func cmdPrintf(args []string) int {
	if len(args) == 0 {
		fatalf("printf", "missing format operand")
		return 1
	}
	format := decodeEscapes(args[0], false)
	values := args[1:]
	for {
		used, err := writePrintf(os.Stdout, format, values)
		if err != nil {
			fatalf("printf", "%v", err)
			return 1
		}
		if len(values) == 0 || used == 0 || used >= len(values) {
			break
		}
		values = values[used:]
	}
	return 0
}

func writePrintf(w io.Writer, format string, args []string) (int, error) {
	used := 0
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			if _, err := io.WriteString(w, format[i:i+1]); err != nil {
				return used, err
			}
			continue
		}
		i++
		if i >= len(format) {
			return used, fmt.Errorf("invalid trailing %%")
		}
		if format[i] == '%' {
			_, err := io.WriteString(w, "%")
			if err != nil {
				return used, err
			}
			continue
		}
		start := i
		for i < len(format) && strings.ContainsRune("-+ #0.123456789", rune(format[i])) {
			i++
		}
		if i >= len(format) {
			return used, fmt.Errorf("incomplete conversion")
		}
		spec := format[i]
		directive := "%" + format[start:i+1]
		value := ""
		if used < len(args) {
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
			n, _ := strconv.ParseInt(value, 0, 64)
			directive = directive[:len(directive)-1] + "d"
			out = fmt.Sprintf(directive, n)
		case 'u', 'o', 'x', 'X':
			n, _ := strconv.ParseUint(value, 0, 64)
			out = fmt.Sprintf(directive, n)
		case 'f', 'e', 'E', 'g', 'G':
			n, _ := strconv.ParseFloat(value, 64)
			out = fmt.Sprintf(directive, n)
		default:
			return used, fmt.Errorf("unsupported conversion %%%c", spec)
		}
		if _, err := io.WriteString(w, out); err != nil {
			return used, err
		}
	}
	return used, nil
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
	status := 0
	if len(args) == 0 {
		for _, value := range os.Environ() {
			fmt.Fprintln(os.Stdout, value)
		}
		return 0
	}
	for _, name := range args {
		if value, ok := os.LookupEnv(name); ok {
			fmt.Fprintln(os.Stdout, value)
		} else {
			status = 1
		}
	}
	return status
}

func cmdSeq(args []string) int {
	separator, format := "\n", ""
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
	count := 0
	for v := first; count < 10000000 && (step > 0 && v <= last+step/1e12 || step < 0 && v >= last+step/1e12); v += step {
		if count > 0 {
			if _, err := io.WriteString(os.Stdout, separator); err != nil {
				fatalf("seq", "write error: %v", err)
				return 1
			}
		}
		if format != "" {
			fmt.Fprintf(os.Stdout, format, v)
		} else if v == float64(int64(v)) {
			fmt.Fprintf(os.Stdout, "%d", int64(v))
		} else {
			fmt.Fprintf(os.Stdout, "%g", v)
		}
		count++
	}
	if count > 0 {
		fmt.Fprintln(os.Stdout)
	}
	return 0
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
			fmt.Fprintf(os.Stderr, "cmp: EOF on %s after byte %d\n", shorter, limit)
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

func cmdStrings(args []string) int {
	minimum := 4
	names := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" {
			i++
			if i >= len(args) {
				return 1
			}
			minimum, _ = strconv.Atoi(args[i])
		} else if strings.HasPrefix(args[i], "-") && len(args[i]) > 1 {
			if n, e := strconv.Atoi(args[i][1:]); e == nil {
				minimum = n
			} else {
				return 1
			}
		} else {
			names = append(names, args[i])
		}
	}
	if len(names) == 0 {
		names = []string{"-"}
	}
	status := 0
	for _, name := range names {
		data, err := readInputBytes(name)
		if err != nil {
			fatalf("strings", "%s: %v", name, err)
			status = 1
			continue
		}
		start := -1
		for i, b := range data {
			printable := b >= 32 && b < 127 || b == '\t'
			if printable && start < 0 {
				start = i
			}
			if !printable && start >= 0 {
				if i-start >= minimum {
					fmt.Println(string(data[start:i]))
				}
				start = -1
			}
		}
		if start >= 0 && len(data)-start >= minimum {
			fmt.Println(string(data[start:]))
		}
	}
	return status
}

func cmdHexdump(args []string) int { return dumpBytes("hexdump", args, true) }
func cmdOd(args []string) int      { return dumpBytes("od", args, false) }
func dumpBytes(prog string, args []string, canonical bool) int {
	if len(args) > 0 && (args[0] == "-C" || args[0] == "-c") {
		canonical = true
		args = args[1:]
	}
	if len(args) > 1 {
		fatalf(prog, "too many operands")
		return 1
	}
	name := "-"
	if len(args) == 1 {
		name = args[0]
	}
	data, err := readInputBytes(name)
	if err != nil {
		fatalf(prog, "%s: %v", name, err)
		return 1
	}
	for off := 0; off < len(data); off += 16 {
		end := off + 16
		if end > len(data) {
			end = len(data)
		}
		fmt.Printf("%08x  ", off)
		for i := 0; i < 16; i++ {
			if i == 8 {
				fmt.Print(" ")
			}
			if off+i < end {
				fmt.Printf("%02x ", data[off+i])
			} else {
				fmt.Print("   ")
			}
		}
		if canonical {
			fmt.Print(" |")
			for _, b := range data[off:end] {
				if b >= 32 && b < 127 {
					fmt.Printf("%c", b)
				} else {
					fmt.Print(".")
				}
			}
			fmt.Print("|")
		}
		fmt.Println()
	}
	fmt.Printf("%08x\n", len(data))
	return 0
}

func cmdDiff(args []string) int {
	if len(args) > 0 && args[0] == "-u" {
		args = args[1:]
	}
	if len(args) != 2 {
		fatalf("diff", "expected two files")
		return 2
	}
	a, e := readLines(args[0])
	if e != nil {
		fatalf("diff", "%v", e)
		return 2
	}
	b, e := readLines(args[1])
	if e != nil {
		fatalf("diff", "%v", e)
		return 2
	}
	if strings.Join(a, "\n") == strings.Join(b, "\n") {
		return 0
	}
	fmt.Printf("--- %s\n+++ %s\n", args[0], args[1])
	for _, op := range lineDiff(a, b) {
		fmt.Println(op)
	}
	return 1
}
func readLines(name string) ([]string, error) {
	data, e := readInputBytes(name)
	if e != nil {
		return nil, e
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), nil
}
func lineDiff(a, b []string) []string {
	n, m := len(a), len(b)
	if n > 0 && m > 4_000_000/n {
		out := make([]string, 1, 1+n+m)
		out[0] = "@@ -1 +1 @@"
		for _, line := range a {
			out = append(out, "-"+line)
		}
		for _, line := range b {
			out = append(out, "+"+line)
		}
		return out
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	out := []string{"@@ -1 +1 @@"}
	for i, j := 0, 0; i < n || j < m; {
		if i < n && j < m && a[i] == b[j] {
			out = append(out, " "+a[i])
			i++
			j++
		} else if j < m && (i == n || dp[i][j+1] >= dp[i+1][j]) {
			out = append(out, "+"+b[j])
			j++
		} else {
			out = append(out, "-"+a[i])
			i++
		}
	}
	return out
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
