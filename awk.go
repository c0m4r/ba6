// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// awk implements the record-processing subset most often needed in recovery
// scripts and one-liners: patterns (including /a/,/b/ ranges), actions,
// if/else, for/while/do loops, break/continue, associative arrays,
// statement blocks, field assignment, and the common string builtins. It
// deliberately keeps the language bounded: no user-defined functions,
// getline, output redirection, or external command execution.

// awkFieldLimit caps how far a field assignment may extend a record so a stray
// expression such as $999999999 = "" cannot exhaust memory.
const awkFieldLimit = 1 << 16

type awkRule struct {
	pattern  string
	pattern2 string // non-empty for a "/a/,/b/" range pattern
	inRange  bool   // range state, mutated as records are processed
	action   string
	kind     string
}

type awkValue struct {
	text    string
	number  float64
	numeric bool
}

func awkString(value string) awkValue { return awkValue{text: value} }
func awkNumber(value float64) awkValue {
	return awkValue{text: strconv.FormatFloat(value, 'g', -1, 64), number: value, numeric: true}
}

func (v awkValue) asNumber() float64 {
	if v.numeric {
		return v.number
	}
	number, _ := strconv.ParseFloat(strings.TrimSpace(v.text), 64)
	return number
}

func (v awkValue) truth() bool {
	if v.numeric {
		return v.number != 0
	}
	if v.text == "" {
		return false
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(v.text), 64)
	return err != nil || number != 0
}

type awkContext struct {
	vars                 map[string]awkValue
	arrays               map[string]map[string]awkValue
	record               string
	fields               []string
	nr, fnr              int
	next, exiting        bool
	breaking, continuing bool
	exitStatus           int
}

func cmdAwk(args []string) int {
	ctx := &awkContext{
		vars: map[string]awkValue{
			"FS": awkString(" "), "OFS": awkString(" "), "ORS": awkString("\n"), "SUBSEP": awkString("\x1c"),
		},
		arrays: map[string]map[string]awkValue{},
	}
	program := ""
	for len(args) > 0 {
		switch {
		case args[0] == "-F":
			if len(args) < 2 {
				fatalf("awk", "-F requires a separator")
				return 2
			}
			ctx.vars["FS"], args = awkString(args[1]), args[2:]
		case strings.HasPrefix(args[0], "-F") && len(args[0]) > 2:
			ctx.vars["FS"], args = awkString(args[0][2:]), args[1:]
		case args[0] == "-v":
			if len(args) < 2 || !strings.Contains(args[1], "=") {
				fatalf("awk", "-v requires NAME=VALUE")
				return 2
			}
			name, value, _ := strings.Cut(args[1], "=")
			if !validAwkName(name) {
				fatalf("awk", "invalid variable name %q", name)
				return 2
			}
			ctx.vars[name], args = awkLiteral(value), args[2:]
		case args[0] == "--":
			args = args[1:]
			goto optionsDone
		case strings.HasPrefix(args[0], "-"):
			fatalf("awk", "unsupported option %q", args[0])
			return 2
		default:
			goto optionsDone
		}
	}
optionsDone:
	if len(args) == 0 {
		fatalf("awk", "missing program")
		return 2
	}
	program, args = args[0], args[1:]
	for len(args) > 0 && validAwkAssignment(args[0]) {
		name, value, _ := strings.Cut(args[0], "=")
		ctx.vars[name], args = awkLiteral(value), args[1:]
	}
	rules, err := parseAwkProgram(program)
	if err != nil {
		fatalf("awk", "%v", err)
		return 2
	}
	for _, rule := range rules {
		if rule.kind == "BEGIN" {
			if err := ctx.runAction(rule.action); err != nil {
				fatalf("awk", "%v", err)
				return 2
			}
		}
	}
	if !ctx.exiting {
		if len(args) == 0 {
			args = []string{"-"}
		}
		for _, name := range args {
			if err := ctx.processAwkFile(name, rules); err != nil {
				fatalf("awk", "%s: %v", name, err)
				return 2
			}
			if ctx.exiting {
				break
			}
		}
	}
	for _, rule := range rules {
		if rule.kind == "END" {
			if err := ctx.runAction(rule.action); err != nil {
				fatalf("awk", "%v", err)
				return 2
			}
		}
	}
	return ctx.exitStatus
}

func (ctx *awkContext) processAwkFile(name string, rules []awkRule) error {
	reader, err := openInput(name)
	if err != nil {
		return err
	}
	defer reader.Close()
	ctx.fnr = 0
	scanner := newLineScanner(reader)
	for scanner.Scan() {
		ctx.nr++
		ctx.fnr++
		ctx.record = scanner.Text()
		if err := ctx.splitRecord(); err != nil {
			return err
		}
		ctx.next = false
		for i := range rules {
			rule := &rules[i]
			if rule.kind != "record" {
				continue
			}
			matches, err := ctx.matchesRule(rule)
			if err != nil {
				return err
			}
			if matches {
				if err := ctx.runAction(rule.action); err != nil {
					return err
				}
			}
			if ctx.next || ctx.exiting {
				break
			}
		}
		if ctx.exiting {
			break
		}
	}
	return scanner.Err()
}

func (ctx *awkContext) splitRecord() error {
	separator := ctx.get("FS").text
	if separator == " " || separator == "" {
		ctx.fields = strings.Fields(ctx.record)
		return nil
	}
	if literal, ok := awkSingleCharacterFS(separator); ok {
		ctx.fields = strings.Split(ctx.record, literal)
		return nil
	}
	re, err := compilePOSIXRegexp(separator, posixERE, false)
	if err != nil {
		return fmt.Errorf("invalid field separator: %w", err)
	}
	ctx.fields = re.Split(ctx.record, -1)
	return nil
}

// awkSingleCharacterFS applies awk's special single-character FS rule. The
// -F spelling commonly uses escaped single characters such as "\\t", so
// recognize those before deciding that a multi-character value is an ERE.
func awkSingleCharacterFS(separator string) (string, bool) {
	if utf8.RuneCountInString(separator) == 1 {
		return separator, true
	}
	if len(separator) < 2 || separator[0] != '\\' {
		return "", false
	}
	if len(separator) == 2 {
		switch separator[1] {
		case 'a':
			return "\a", true
		case 'b':
			return "\b", true
		case 'f':
			return "\f", true
		case 'n':
			return "\n", true
		case 'r':
			return "\r", true
		case 't':
			return "\t", true
		case 'v':
			return "\v", true
		default:
			// An escaped ordinary character is a literal separator too,
			// e.g. -F'\\|' and -F'\\.'.
			return string(separator[1]), true
		}
	}
	return "", false
}

func (ctx *awkContext) get(name string) awkValue {
	switch name {
	case "NR":
		return awkNumber(float64(ctx.nr))
	case "FNR":
		return awkNumber(float64(ctx.fnr))
	case "NF":
		return awkNumber(float64(len(ctx.fields)))
	}
	return ctx.vars[name]
}

func (ctx *awkContext) field(number int) awkValue {
	if number == 0 {
		return awkString(ctx.record)
	}
	if number < 0 || number > len(ctx.fields) {
		return awkString("")
	}
	return awkLiteral(ctx.fields[number-1])
}

// setField assigns a field, rebuilding the record from the fields joined by
// OFS the way awk does. Assigning $0 re-splits the record instead.
func (ctx *awkContext) setField(number int, value string) error {
	if number < 0 || number > awkFieldLimit {
		return fmt.Errorf("invalid field $%d", number)
	}
	if number == 0 {
		ctx.record = value
		return ctx.splitRecord()
	}
	for len(ctx.fields) < number {
		ctx.fields = append(ctx.fields, "")
	}
	ctx.fields[number-1] = value
	ctx.record = strings.Join(ctx.fields, ctx.get("OFS").text)
	return nil
}

// awkLvalue names an assignable location: either a variable or a field.
type awkLvalue struct {
	name      string
	field     int
	arrayName string
	arrayKey  string
	isArray   bool
	isSet     bool
}

func (ctx *awkContext) read(target awkLvalue) awkValue {
	if target.isArray {
		return ctx.arrays[target.arrayName][target.arrayKey]
	}
	if target.name == "" {
		return ctx.field(target.field)
	}
	return ctx.get(target.name)
}

func (ctx *awkContext) assign(target awkLvalue, value awkValue) error {
	if target.isArray {
		if ctx.arrays[target.arrayName] == nil {
			ctx.arrays[target.arrayName] = map[string]awkValue{}
		}
		ctx.arrays[target.arrayName][target.arrayKey] = value
		return nil
	}
	if target.name == "" {
		return ctx.setField(target.field, value.text)
	}
	ctx.vars[target.name] = value
	return nil
}

// target resolves the left-hand side of an assignment: a plain variable
// name, an array element such as a["x"], or a field reference such as $1
// or $(NF - 1).
func (ctx *awkContext) target(text string) (awkLvalue, error) {
	text = strings.TrimSpace(text)
	if validAwkName(text) {
		return awkLvalue{name: text, isSet: true}, nil
	}
	if name, keyExpr, ok := splitAwkArrayRef(text); ok {
		key, err := ctx.evalArrayKey(keyExpr)
		if err != nil {
			return awkLvalue{}, err
		}
		return awkLvalue{arrayName: name, arrayKey: key, isArray: true, isSet: true}, nil
	}
	if strings.HasPrefix(text, "$") {
		index, err := ctx.eval(text[1:])
		if err != nil {
			return awkLvalue{}, err
		}
		return awkLvalue{field: int(index.asNumber()), isSet: true}, nil
	}
	return awkLvalue{}, fmt.Errorf("invalid assignment target %q", text)
}

// evalArrayKey evaluates a subscript expression, joining a comma-separated
// multi-dimensional subscript such as "i, j" with SUBSEP the way real awk
// simulates multi-dimensional arrays with a single string-keyed map.
func (ctx *awkContext) evalArrayKey(keyExpr string) (string, error) {
	parts := splitAwkTopLevel(keyExpr, ',')
	if len(parts) == 1 {
		value, err := ctx.eval(parts[0])
		return value.text, err
	}
	keys := make([]string, len(parts))
	for i, part := range parts {
		value, err := ctx.eval(part)
		if err != nil {
			return "", err
		}
		keys[i] = value.text
	}
	return strings.Join(keys, ctx.get("SUBSEP").text), nil
}

// splitAwkArrayRef recognizes "name[key]" and returns the array name and the
// (unevaluated) key expression text.
func splitAwkArrayRef(text string) (name, keyExpr string, ok bool) {
	if !strings.HasSuffix(text, "]") {
		return "", "", false
	}
	open := strings.IndexByte(text, '[')
	if open <= 0 {
		return "", "", false
	}
	name = text[:open]
	if !validAwkName(name) {
		return "", "", false
	}
	return name, text[open+1 : len(text)-1], true
}

func (ctx *awkContext) matches(pattern string) (bool, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return true, nil
	}
	value, err := ctx.eval(pattern)
	if err != nil {
		return false, err
	}
	return value.truth(), nil
}

// matchesRule evaluates a rule's pattern against the current record,
// including a "/a/,/b/" range pattern's state: once the first pattern has
// matched, every record matches up to and including the one the second
// pattern matches.
func (ctx *awkContext) matchesRule(rule *awkRule) (bool, error) {
	if rule.pattern2 == "" {
		return ctx.matches(rule.pattern)
	}
	if !rule.inRange {
		started, err := ctx.matches(rule.pattern)
		if err != nil || !started {
			return false, err
		}
		rule.inRange = true
	}
	ended, err := ctx.matches(rule.pattern2)
	if err != nil {
		return false, err
	}
	if ended {
		rule.inRange = false
	}
	return true, nil
}

// splitAwkRangePattern recognizes a "/a/,/b/"-style range pattern (any two
// comma-separated patterns, not just regex literals) and splits it in two;
// an ordinary pattern is returned unchanged with pattern2 empty.
func splitAwkRangePattern(pattern string) (string, string) {
	parts := splitAwkTopLevel(pattern, ',')
	if len(parts) != 2 {
		return pattern, ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func (ctx *awkContext) runAction(action string) error {
	for position := 0; ; {
		position = skipAwkSpace(action, position)
		if position >= len(action) {
			return nil
		}
		end := awkStatementEnd(action, position)
		if err := ctx.runStatement(action[position:end]); err != nil {
			return err
		}
		if ctx.next || ctx.exiting || ctx.breaking || ctx.continuing {
			return nil
		}
		position = end
	}
}

func (ctx *awkContext) runStatement(statement string) error {
	statement = strings.TrimSpace(statement)
	switch {
	case statement == "":
		return nil
	case statement[0] == '{':
		close, err := matchingAwkBracket(statement, 0)
		if err != nil {
			return err
		}
		if rest := strings.TrimSpace(statement[close+1:]); rest != "" {
			return fmt.Errorf("unexpected text after block %q", rest)
		}
		return ctx.runAction(statement[1:close])
	case awkKeyword(statement, "if"):
		return ctx.runIf(statement)
	case awkKeyword(statement, "for"):
		return ctx.runFor(statement)
	case awkKeyword(statement, "while"):
		return ctx.runWhile(statement)
	case awkKeyword(statement, "do"):
		return ctx.runDo(statement)
	case awkKeyword(statement, "delete"):
		return ctx.runDelete(statement)
	case statement == "break":
		ctx.breaking = true
		return nil
	case statement == "continue":
		ctx.continuing = true
		return nil
	case statement == "next":
		ctx.next = true
		return nil
	case statement == "exit" || strings.HasPrefix(statement, "exit "):
		ctx.exiting = true
		if expression := strings.TrimSpace(strings.TrimPrefix(statement, "exit")); expression != "" {
			value, err := ctx.eval(expression)
			if err != nil {
				return err
			}
			ctx.exitStatus = int(value.asNumber())
		}
		return nil
	case statement == "print" || strings.HasPrefix(statement, "print "):
		return ctx.awkPrint(strings.TrimSpace(strings.TrimPrefix(statement, "print")))
	case strings.HasPrefix(statement, "printf "):
		return ctx.awkPrintf(strings.TrimSpace(strings.TrimPrefix(statement, "printf")))
	default:
		return ctx.awkAssignment(statement)
	}
}

func (ctx *awkContext) runIf(statement string) error {
	condition, thenPart, elsePart, err := splitAwkIf(statement)
	if err != nil {
		return err
	}
	value, err := ctx.eval(condition)
	if err != nil {
		return err
	}
	if value.truth() {
		return ctx.runStatement(thenPart)
	}
	return ctx.runStatement(elsePart)
}

// splitAwkIf breaks "if (condition) statement [else statement]" apart. The
// statement must already be a single statement as delimited by awkStatementEnd.
func splitAwkIf(statement string) (condition, thenPart, elsePart string, err error) {
	rest := strings.TrimSpace(statement[len("if"):])
	if !strings.HasPrefix(rest, "(") {
		return "", "", "", fmt.Errorf("if requires a condition")
	}
	close, err := matchingAwkBracket(rest, 0)
	if err != nil {
		return "", "", "", err
	}
	condition = rest[1:close]
	body := rest[close+1:]
	end := awkStatementEnd(body, 0)
	thenPart = body[:end]
	tail := skipAwkSpace(body, end)
	if awkKeyword(body[tail:], "else") {
		elsePart = body[tail+len("else"):]
	} else if remainder := strings.TrimSpace(body[tail:]); remainder != "" {
		return "", "", "", fmt.Errorf("unexpected text after if %q", remainder)
	}
	return condition, thenPart, elsePart, nil
}

// loopBody runs one loop iteration's body statement, translating break and
// continue into a signal the caller's loop can act on: (stop, err) where
// stop means "leave the loop now" (true for break, and for next/exit/a
// propagated error, which the caller must also check for itself).
func (ctx *awkContext) loopBody(body string) (stop bool, err error) {
	if err := ctx.runStatement(body); err != nil {
		return true, err
	}
	if ctx.breaking {
		ctx.breaking = false
		return true, nil
	}
	ctx.continuing = false
	if ctx.next || ctx.exiting {
		return true, nil
	}
	return false, nil
}

// runWhile implements "while (condition) body".
func (ctx *awkContext) runWhile(statement string) error {
	rest := strings.TrimSpace(statement[len("while"):])
	if !strings.HasPrefix(rest, "(") {
		return fmt.Errorf("while requires a condition")
	}
	close, err := matchingAwkBracket(rest, 0)
	if err != nil {
		return err
	}
	condition, body := rest[1:close], rest[close+1:]
	for {
		value, err := ctx.eval(condition)
		if err != nil {
			return err
		}
		if !value.truth() {
			return nil
		}
		if stop, err := ctx.loopBody(body); stop {
			return err
		}
	}
}

// runDo implements "do body while (condition)".
func (ctx *awkContext) runDo(statement string) error {
	body := strings.TrimSpace(statement[len("do"):])
	bodyEnd := awkStatementEnd(body, 0)
	bodyStatement := body[:bodyEnd]
	tail := skipAwkSpace(body, bodyEnd)
	if !awkKeyword(body[tail:], "while") {
		return fmt.Errorf("do requires a trailing while")
	}
	rest := strings.TrimSpace(body[tail+len("while"):])
	if !strings.HasPrefix(rest, "(") {
		return fmt.Errorf("while requires a condition")
	}
	close, err := matchingAwkBracket(rest, 0)
	if err != nil {
		return err
	}
	condition := rest[1:close]
	for {
		if stop, err := ctx.loopBody(bodyStatement); stop {
			return err
		}
		value, err := ctx.eval(condition)
		if err != nil {
			return err
		}
		if !value.truth() {
			return nil
		}
	}
}

// runFor implements both "for (init; condition; post) body" and
// "for (key in array) body".
func (ctx *awkContext) runFor(statement string) error {
	rest := strings.TrimSpace(statement[len("for"):])
	if !strings.HasPrefix(rest, "(") {
		return fmt.Errorf("for requires a condition")
	}
	close, err := matchingAwkBracket(rest, 0)
	if err != nil {
		return err
	}
	clause, body := rest[1:close], rest[close+1:]

	if idx := findAwkTopLevelWord(clause, "in"); idx >= 0 {
		keyName := strings.TrimSpace(clause[:idx])
		arrayName := strings.TrimSpace(clause[idx+len("in"):])
		if !validAwkName(keyName) || !validAwkName(arrayName) {
			return fmt.Errorf("invalid for-in clause %q", clause)
		}
		for key := range ctx.arrays[arrayName] {
			ctx.vars[keyName] = awkLiteral(key)
			if stop, err := ctx.loopBody(body); stop {
				return err
			}
		}
		return nil
	}

	parts := splitAwkTopLevel(clause, ';')
	if len(parts) != 3 {
		return fmt.Errorf("for requires init; condition; post")
	}
	if init := strings.TrimSpace(parts[0]); init != "" {
		if err := ctx.runStatement(init); err != nil {
			return err
		}
	}
	condition, post := strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	for {
		if condition != "" {
			value, err := ctx.eval(condition)
			if err != nil {
				return err
			}
			if !value.truth() {
				return nil
			}
		}
		if stop, err := ctx.loopBody(body); stop {
			return err
		}
		if post != "" {
			if err := ctx.runStatement(post); err != nil {
				return err
			}
		}
	}
}

// runDelete implements "delete array[key]" and whole-array "delete array".
func (ctx *awkContext) runDelete(statement string) error {
	rest := strings.TrimSpace(statement[len("delete"):])
	if name, keyExpr, ok := splitAwkArrayRef(rest); ok {
		key, err := ctx.eval(keyExpr)
		if err != nil {
			return err
		}
		delete(ctx.arrays[name], key.text)
		return nil
	}
	if validAwkName(rest) {
		delete(ctx.arrays, rest)
		return nil
	}
	return fmt.Errorf("invalid delete target %q", rest)
}

func (ctx *awkContext) awkPrint(expressions string) error {
	if expressions == "" {
		_, err := io.WriteString(os.Stdout, ctx.record+ctx.get("ORS").text)
		return err
	}
	parts := splitAwkArguments(expressions)
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value, err := ctx.eval(part)
		if err != nil {
			return err
		}
		values = append(values, value.text)
	}
	_, err := io.WriteString(os.Stdout, strings.Join(values, ctx.get("OFS").text)+ctx.get("ORS").text)
	return err
}

func (ctx *awkContext) awkPrintf(expressions string) error {
	parts := splitAwkArguments(expressions)
	if len(parts) == 0 {
		return fmt.Errorf("printf requires a format")
	}
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value, err := ctx.eval(part)
		if err != nil {
			return err
		}
		values = append(values, value.text)
	}
	_, _, err := writePrintf(os.Stdout, values[0], values[1:])
	return err
}

func (ctx *awkContext) awkAssignment(statement string) error {
	for _, operator := range []string{"+=", "-=", "*=", "/=", "="} {
		position := findAwkOperator(statement, operator)
		if position < 0 {
			continue
		}
		target, err := ctx.target(statement[:position])
		if err != nil {
			return err
		}
		value, err := ctx.eval(statement[position+len(operator):])
		if err != nil {
			return err
		}
		current := ctx.read(target).asNumber()
		switch operator {
		case "+=":
			value = awkNumber(current + value.asNumber())
		case "-=":
			value = awkNumber(current - value.asNumber())
		case "*=":
			value = awkNumber(current * value.asNumber())
		case "/=":
			if value.asNumber() == 0 {
				return fmt.Errorf("division by zero")
			}
			value = awkNumber(current / value.asNumber())
		}
		return ctx.assign(target, value)
	}
	if strings.HasSuffix(statement, "++") {
		target, err := ctx.target(strings.TrimSuffix(statement, "++"))
		if err == nil {
			return ctx.assign(target, awkNumber(ctx.read(target).asNumber()+1))
		}
	}
	if strings.HasSuffix(statement, "--") {
		target, err := ctx.target(strings.TrimSuffix(statement, "--"))
		if err == nil {
			return ctx.assign(target, awkNumber(ctx.read(target).asNumber()-1))
		}
	}
	if strings.HasPrefix(statement, "++") {
		target, err := ctx.target(strings.TrimPrefix(statement, "++"))
		if err == nil {
			return ctx.assign(target, awkNumber(ctx.read(target).asNumber()+1))
		}
	}
	if strings.HasPrefix(statement, "--") {
		target, err := ctx.target(strings.TrimPrefix(statement, "--"))
		if err == nil {
			return ctx.assign(target, awkNumber(ctx.read(target).asNumber()-1))
		}
	}
	// Anything else is an expression statement, evaluated for its side effects
	// such as sub() and gsub().
	_, err := ctx.eval(statement)
	return err
}

func (ctx *awkContext) eval(expression string) (awkValue, error) {
	tokens, err := tokenizeAwk(expression)
	if err != nil {
		return awkValue{}, err
	}
	parser := awkExpressionParser{tokens: tokens, ctx: ctx}
	value, err := parser.parseOr()
	if err == nil && parser.position != len(tokens) {
		err = fmt.Errorf("unexpected token %q", tokens[parser.position].text)
	}
	return value, err
}

type awkToken struct {
	kind, text string
}

func tokenizeAwk(expression string) ([]awkToken, error) {
	var tokens []awkToken
	for i := 0; i < len(expression); {
		if strings.ContainsRune(" \t\r\n", rune(expression[i])) {
			i++
			continue
		}
		if expression[i] == '#' {
			for i < len(expression) && expression[i] != '\n' {
				i++
			}
			continue
		}
		if expression[i] == '"' {
			start := i
			i++
			for i < len(expression) && expression[i] != '"' {
				if expression[i] == '\\' && i+1 < len(expression) {
					i += 2
				} else {
					i++
				}
			}
			if i >= len(expression) {
				return nil, fmt.Errorf("unterminated string")
			}
			i++
			value, err := strconv.Unquote(expression[start:i])
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, awkToken{kind: "string", text: value})
			continue
		}
		if expression[i] == '/' && awkRegexOperand(tokens) {
			// A slash where a value is expected begins a regular-expression
			// literal; after a value it is the division operator.
			start := i + 1
			i++
			for i < len(expression) && expression[i] != '/' {
				if expression[i] == '\\' && i+1 < len(expression) {
					i += 2
				} else {
					i++
				}
			}
			if i >= len(expression) {
				return nil, fmt.Errorf("unterminated regular expression")
			}
			tokens = append(tokens, awkToken{kind: "regex", text: expression[start:i]})
			i++
			continue
		}
		if (expression[i] >= '0' && expression[i] <= '9') || expression[i] == '.' && i+1 < len(expression) && expression[i+1] >= '0' && expression[i+1] <= '9' {
			start := i
			for i < len(expression) && (expression[i] >= '0' && expression[i] <= '9' || strings.ContainsRune(".eE+-", rune(expression[i])) && i > start) {
				if (expression[i] == '+' || expression[i] == '-') && expression[i-1] != 'e' && expression[i-1] != 'E' {
					break
				}
				i++
			}
			tokens = append(tokens, awkToken{kind: "number", text: expression[start:i]})
			continue
		}
		if isAwkNameStart(expression[i]) {
			start := i
			for i < len(expression) && (isAwkNameStart(expression[i]) || expression[i] >= '0' && expression[i] <= '9') {
				i++
			}
			tokens = append(tokens, awkToken{kind: "name", text: expression[start:i]})
			continue
		}
		matched := false
		for _, operator := range []string{"==", "!=", "<=", ">=", "&&", "||", "!~"} {
			if strings.HasPrefix(expression[i:], operator) {
				tokens = append(tokens, awkToken{kind: "operator", text: operator})
				i += len(operator)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if strings.ContainsRune("$()+-*/%<>!~,[]", rune(expression[i])) {
			tokens = append(tokens, awkToken{kind: "operator", text: expression[i : i+1]})
			i++
			continue
		}
		return nil, fmt.Errorf("invalid character %q", expression[i])
	}
	return tokens, nil
}

// awkRegexOperand reports whether a slash following the tokens scanned so far
// starts a regular-expression literal rather than the division operator.
func awkRegexOperand(tokens []awkToken) bool {
	if len(tokens) == 0 {
		return true
	}
	last := tokens[len(tokens)-1]
	return last.kind == "operator" && last.text != ")"
}

type awkExpressionParser struct {
	tokens   []awkToken
	position int
	ctx      *awkContext
}

func (p *awkExpressionParser) parseOr() (awkValue, error) {
	left, err := p.parseAnd()
	for err == nil && p.take("||") {
		var right awkValue
		right, err = p.parseAnd()
		left = awkBool(left.truth() || right.truth())
	}
	return left, err
}

func (p *awkExpressionParser) parseAnd() (awkValue, error) {
	left, err := p.parseIn()
	for err == nil && p.take("&&") {
		var right awkValue
		right, err = p.parseIn()
		left = awkBool(left.truth() && right.truth())
	}
	return left, err
}

// parseArrayKey parses a subscript expression up to its closing "]",
// joining a comma-separated multi-dimensional subscript ("i, j") with
// SUBSEP the way real awk simulates multi-dimensional arrays with a single
// string-keyed map.
func (p *awkExpressionParser) parseArrayKey() (string, error) {
	first, err := p.parseOr()
	if err != nil {
		return "", err
	}
	keys := []string{first.text}
	for p.take(",") {
		next, err := p.parseOr()
		if err != nil {
			return "", err
		}
		keys = append(keys, next.text)
	}
	if !p.take("]") {
		return "", fmt.Errorf("missing ]")
	}
	if len(keys) == 1 {
		return keys[0], nil
	}
	return strings.Join(keys, p.ctx.get("SUBSEP").text), nil
}

// parseIn handles "key in array", awk's array-membership test.
func (p *awkExpressionParser) parseIn() (awkValue, error) {
	left, err := p.parseCompare()
	if err != nil {
		return left, err
	}
	for p.position < len(p.tokens) && p.tokens[p.position].kind == "name" && p.tokens[p.position].text == "in" {
		p.position++
		if p.position >= len(p.tokens) || p.tokens[p.position].kind != "name" {
			return awkValue{}, fmt.Errorf("in requires an array name")
		}
		arrayName := p.tokens[p.position].text
		p.position++
		_, ok := p.ctx.arrays[arrayName][left.text]
		left = awkBool(ok)
	}
	return left, nil
}

// parseConcat implements awk's string concatenation: two expressions with no
// operator between them, as in "print \"x=\" x". By the time parseAdd
// returns, it has already consumed every +/- it can reach, so a term here
// can only follow if it starts with something other than +/- -- which is
// why unary +/- are deliberately not among the tokens that start one.
func (p *awkExpressionParser) parseConcat() (awkValue, error) {
	left, err := p.parseAdd()
	if err != nil {
		return left, err
	}
	for p.canStartTerm() {
		right, err := p.parseAdd()
		if err != nil {
			return left, err
		}
		left = awkString(left.text + right.text)
	}
	return left, nil
}

// canStartTerm reports whether the next token could begin another
// concatenation operand, without consuming it.
func (p *awkExpressionParser) canStartTerm() bool {
	if p.position >= len(p.tokens) {
		return false
	}
	switch p.tokens[p.position].kind {
	case "string", "regex", "number":
		return true
	case "name":
		// "in" is a contextual keyword (parseIn handles "key in array"
		// above this level); it can never itself start a concat operand.
		return p.tokens[p.position].text != "in"
	case "operator":
		switch p.tokens[p.position].text {
		case "(", "$", "!":
			return true
		}
	}
	return false
}

func (p *awkExpressionParser) parseCompare() (awkValue, error) {
	left, err := p.parseConcat()
	if err != nil || p.position >= len(p.tokens) {
		return left, err
	}
	op := p.tokens[p.position].text
	switch op {
	case "==", "!=", "<", "<=", ">", ">=", "~", "!~":
	default:
		return left, nil
	}
	p.position++
	if op == "~" || op == "!~" {
		pattern, err := p.parseRegexOperand()
		if err != nil {
			return left, err
		}
		re, err := compilePOSIXRegexp(pattern, posixERE, false)
		if err != nil {
			return left, err
		}
		return awkBool(re.MatchString(left.text) != (op == "!~")), nil
	}
	right, err := p.parseConcat()
	if err != nil {
		return left, err
	}
	numeric := left.numeric && right.numeric
	var comparison int
	if numeric {
		if left.asNumber() < right.asNumber() {
			comparison = -1
		} else if left.asNumber() > right.asNumber() {
			comparison = 1
		}
	} else {
		comparison = strings.Compare(left.text, right.text)
	}
	switch op {
	case "==":
		return awkBool(comparison == 0), nil
	case "!=":
		return awkBool(comparison != 0), nil
	case "<":
		return awkBool(comparison < 0), nil
	case "<=":
		return awkBool(comparison <= 0), nil
	case ">":
		return awkBool(comparison > 0), nil
	default:
		return awkBool(comparison >= 0), nil
	}
}

func (p *awkExpressionParser) parseAdd() (awkValue, error) {
	left, err := p.parseMultiply()
	for err == nil && p.position < len(p.tokens) && (p.tokens[p.position].text == "+" || p.tokens[p.position].text == "-") {
		op := p.tokens[p.position].text
		p.position++
		var right awkValue
		right, err = p.parseMultiply()
		if op == "+" {
			left = awkNumber(left.asNumber() + right.asNumber())
		} else {
			left = awkNumber(left.asNumber() - right.asNumber())
		}
	}
	return left, err
}

func (p *awkExpressionParser) parseMultiply() (awkValue, error) {
	left, err := p.parseUnary()
	for err == nil && p.position < len(p.tokens) && isAwkMultiplicative(p.tokens[p.position].text) {
		op := p.tokens[p.position].text
		p.position++
		var right awkValue
		right, err = p.parseUnary()
		if err != nil {
			break
		}
		if right.asNumber() == 0 && op != "*" {
			return left, fmt.Errorf("division by zero")
		}
		switch op {
		case "*":
			left = awkNumber(left.asNumber() * right.asNumber())
		case "/":
			left = awkNumber(left.asNumber() / right.asNumber())
		case "%":
			left = awkNumber(float64(int64(left.asNumber()) % int64(right.asNumber())))
		}
	}
	return left, err
}

func isAwkMultiplicative(operator string) bool {
	return operator == "*" || operator == "/" || operator == "%"
}

func (p *awkExpressionParser) parseUnary() (awkValue, error) {
	if p.take("!") {
		value, err := p.parseUnary()
		return awkBool(!value.truth()), err
	}
	if p.take("-") {
		value, err := p.parseUnary()
		return awkNumber(-value.asNumber()), err
	}
	if p.take("+") {
		return p.parseUnary()
	}
	if p.take("$") {
		value, err := p.parseUnary()
		return p.ctx.field(int(value.asNumber())), err
	}
	return p.parsePrimary()
}

func (p *awkExpressionParser) parsePrimary() (awkValue, error) {
	if p.take("(") {
		value, err := p.parseOr()
		if !p.take(")") && err == nil {
			err = fmt.Errorf("missing )")
		}
		return value, err
	}
	if p.position >= len(p.tokens) {
		return awkValue{}, fmt.Errorf("missing expression")
	}
	token := p.tokens[p.position]
	p.position++
	switch token.kind {
	case "string":
		return awkString(token.text), nil
	case "regex":
		// A bare regular expression is shorthand for matching it against $0.
		re, err := compilePOSIXRegexp(token.text, posixERE, false)
		if err != nil {
			return awkValue{}, err
		}
		return awkBool(re.MatchString(p.ctx.record)), nil
	case "number":
		number, err := strconv.ParseFloat(token.text, 64)
		return awkNumber(number), err
	case "name":
		if p.take("(") {
			switch token.text {
			case "sub", "gsub":
				return p.substitute(token.text == "gsub")
			case "split":
				return p.splitCall()
			case "match":
				return p.matchCall()
			case "length":
				return p.lengthCall()
			}
			return p.call(token.text)
		}
		if token.text == "length" {
			// "length" alone, with no parentheses, is a GNU extension for
			// the current record's length.
			return awkNumber(float64(len(p.ctx.record))), nil
		}
		if p.take("[") {
			key, err := p.parseArrayKey()
			if err != nil {
				return awkValue{}, err
			}
			return p.ctx.arrays[token.text][key], nil
		}
		return p.ctx.get(token.text), nil
	}
	return awkValue{}, fmt.Errorf("unexpected token %q", token.text)
}

// parseRegexOperand reads a pattern given either as a /regex/ literal or as an
// expression producing the pattern text.
func (p *awkExpressionParser) parseRegexOperand() (string, error) {
	if p.position < len(p.tokens) && p.tokens[p.position].kind == "regex" {
		pattern := p.tokens[p.position].text
		p.position++
		return pattern, nil
	}
	value, err := p.parseAdd()
	return value.text, err
}

// parseLvalue reads an assignable target: a field such as $2 or a variable.
func (p *awkExpressionParser) parseLvalue() (awkLvalue, error) {
	if p.take("$") {
		value, err := p.parseUnary()
		if err != nil {
			return awkLvalue{}, err
		}
		return awkLvalue{field: int(value.asNumber()), isSet: true}, nil
	}
	if p.position < len(p.tokens) && p.tokens[p.position].kind == "name" {
		name := p.tokens[p.position].text
		p.position++
		if p.take("[") {
			key, err := p.parseArrayKey()
			if err != nil {
				return awkLvalue{}, err
			}
			return awkLvalue{arrayName: name, arrayKey: key, isArray: true, isSet: true}, nil
		}
		return awkLvalue{name: name, isSet: true}, nil
	}
	return awkLvalue{}, fmt.Errorf("substitution target must be a field or variable")
}

// substitute implements sub(regex, replacement [, target]) and its gsub
// variant, returning the number of replacements made.
func (p *awkExpressionParser) substitute(global bool) (awkValue, error) {
	pattern, err := p.parseRegexOperand()
	if err != nil {
		return awkValue{}, err
	}
	if !p.take(",") {
		return awkValue{}, fmt.Errorf("sub requires a replacement")
	}
	replacement, err := p.parseOr()
	if err != nil {
		return awkValue{}, err
	}
	target := awkLvalue{isSet: true}
	if p.take(",") {
		if target, err = p.parseLvalue(); err != nil {
			return awkValue{}, err
		}
	}
	if !p.take(")") {
		return awkValue{}, fmt.Errorf("missing )")
	}
	re, err := compilePOSIXRegexp(pattern, posixERE, false)
	if err != nil {
		return awkValue{}, err
	}
	text, count := awkSubstitute(re, p.ctx.read(target).text, replacement.text, global)
	if count > 0 {
		if err := p.ctx.assign(target, awkString(text)); err != nil {
			return awkValue{}, err
		}
	}
	return awkNumber(float64(count)), nil
}

// awkSubstitute replaces the first match, or every match when global is set,
// expanding an unescaped & in the replacement to the matched text.
func awkSubstitute(re *regexp.Regexp, text, replacement string, global bool) (string, int) {
	var builder strings.Builder
	count, position, previous := 0, 0, -1
	for position <= len(text) {
		match := re.FindStringIndex(text[position:])
		if match == nil {
			break
		}
		start, end := position+match[0], position+match[1]
		empty := start == end
		builder.WriteString(text[position:start])
		if empty && start == previous {
			// An empty match directly after a replacement is not a match.
			if start < len(text) {
				builder.WriteByte(text[start])
			}
			position = start + 1
			continue
		}
		previous = end
		builder.WriteString(awkReplacement(replacement, text[start:end]))
		count++
		position = end
		if empty {
			// An empty match must still make progress.
			if end < len(text) {
				builder.WriteByte(text[end])
			}
			position = end + 1
		}
		if !global {
			break
		}
	}
	if position < len(text) {
		builder.WriteString(text[position:])
	}
	return builder.String(), count
}

func awkReplacement(replacement, matched string) string {
	var builder strings.Builder
	for i := 0; i < len(replacement); i++ {
		switch {
		case replacement[i] == '\\' && i+1 < len(replacement) && (replacement[i+1] == '&' || replacement[i+1] == '\\'):
			builder.WriteByte(replacement[i+1])
			i++
		case replacement[i] == '&':
			builder.WriteString(matched)
		default:
			builder.WriteByte(replacement[i])
		}
	}
	return builder.String()
}

// splitCall implements split(string, array [, fs]), populating array with
// the string's fields (1-indexed, awk's own convention) and returning the
// count. The array argument is a bare name, not a general expression, since
// arrays cannot appear as ordinary values in this implementation.
func (p *awkExpressionParser) splitCall() (awkValue, error) {
	str, err := p.parseOr()
	if err != nil {
		return awkValue{}, err
	}
	if !p.take(",") {
		return awkValue{}, fmt.Errorf("split requires an array")
	}
	if p.position >= len(p.tokens) || p.tokens[p.position].kind != "name" {
		return awkValue{}, fmt.Errorf("split requires an array name")
	}
	arrayName := p.tokens[p.position].text
	p.position++
	separator := p.ctx.get("FS").text
	if p.take(",") {
		sepValue, err := p.parseRegexOperand()
		if err != nil {
			return awkValue{}, err
		}
		separator = sepValue
	}
	if !p.take(")") {
		return awkValue{}, fmt.Errorf("missing )")
	}
	var parts []string
	if separator == " " || separator == "" {
		parts = strings.Fields(str.text)
	} else if literal, ok := awkSingleCharacterFS(separator); ok {
		parts = strings.Split(str.text, literal)
	} else {
		re, err := compilePOSIXRegexp(separator, posixERE, false)
		if err != nil {
			return awkValue{}, fmt.Errorf("invalid field separator: %w", err)
		}
		parts = re.Split(str.text, -1)
	}
	if str.text == "" {
		parts = nil
	}
	array := map[string]awkValue{}
	for i, part := range parts {
		array[strconv.Itoa(i+1)] = awkLiteral(part)
	}
	p.ctx.arrays[arrayName] = array
	return awkNumber(float64(len(parts))), nil
}

// matchCall implements match(string, regex), setting RSTART/RLENGTH and
// returning the 1-indexed match position (0 if none). Its regex argument
// needs the pattern text itself, not the "matched against $0" boolean a
// bare /regex/ literal evaluates to elsewhere, so it is parsed the same way
// sub/gsub's pattern argument is.
func (p *awkExpressionParser) matchCall() (awkValue, error) {
	str, err := p.parseOr()
	if err != nil {
		return awkValue{}, err
	}
	if !p.take(",") {
		return awkValue{}, fmt.Errorf("match requires a pattern")
	}
	pattern, err := p.parseRegexOperand()
	if err != nil {
		return awkValue{}, err
	}
	if !p.take(")") {
		return awkValue{}, fmt.Errorf("missing )")
	}
	re, err := compilePOSIXRegexp(pattern, posixERE, false)
	if err != nil {
		return awkValue{}, err
	}
	loc := re.FindStringIndex(str.text)
	if loc == nil {
		p.ctx.vars["RSTART"], p.ctx.vars["RLENGTH"] = awkNumber(0), awkNumber(-1)
		return awkNumber(0), nil
	}
	p.ctx.vars["RSTART"] = awkNumber(float64(loc[0] + 1))
	p.ctx.vars["RLENGTH"] = awkNumber(float64(loc[1] - loc[0]))
	return awkNumber(float64(loc[0] + 1)), nil
}

// lengthCall implements length() (the record), length(string), and
// length(array) (its element count). The array form needs special-casing
// because a bare array name isn't an evaluable value anywhere else in this
// implementation.
func (p *awkExpressionParser) lengthCall() (awkValue, error) {
	if p.take(")") {
		return awkNumber(float64(len(p.ctx.record))), nil
	}
	if p.position+1 < len(p.tokens) && p.tokens[p.position].kind == "name" &&
		p.tokens[p.position+1].kind == "operator" && p.tokens[p.position+1].text == ")" {
		if array, ok := p.ctx.arrays[p.tokens[p.position].text]; ok {
			p.position += 2
			return awkNumber(float64(len(array))), nil
		}
	}
	value, err := p.parseOr()
	if err != nil {
		return awkValue{}, err
	}
	if !p.take(")") {
		return awkValue{}, fmt.Errorf("missing )")
	}
	return awkNumber(float64(len(value.text))), nil
}

func (p *awkExpressionParser) call(name string) (awkValue, error) {
	var arguments []awkValue
	if !p.take(")") {
		for {
			argument, err := p.parseOr()
			if err != nil {
				return awkValue{}, err
			}
			arguments = append(arguments, argument)
			if p.take(")") {
				break
			}
			if !p.take(",") {
				return awkValue{}, fmt.Errorf("missing , or )")
			}
		}
	}
	switch name {
	case "int":
		if len(arguments) != 1 {
			return awkValue{}, fmt.Errorf("int requires one argument")
		}
		return awkNumber(float64(int64(arguments[0].asNumber()))), nil
	case "tolower":
		if len(arguments) == 1 {
			return awkString(strings.ToLower(arguments[0].text)), nil
		}
	case "toupper":
		if len(arguments) == 1 {
			return awkString(strings.ToUpper(arguments[0].text)), nil
		}
	case "substr":
		if len(arguments) == 2 || len(arguments) == 3 {
			start := int(arguments[1].asNumber()) - 1
			if start < 0 {
				start = 0
			}
			if start > len(arguments[0].text) {
				start = len(arguments[0].text)
			}
			end := len(arguments[0].text)
			if len(arguments) == 3 && start+int(arguments[2].asNumber()) < end {
				end = start + int(arguments[2].asNumber())
			}
			return awkString(arguments[0].text[start:end]), nil
		}
	case "index":
		if len(arguments) == 2 {
			return awkNumber(float64(strings.Index(arguments[0].text, arguments[1].text) + 1)), nil
		}
	case "sprintf":
		if len(arguments) == 0 {
			return awkValue{}, fmt.Errorf("sprintf requires a format")
		}
		values := make([]string, len(arguments)-1)
		for i, argument := range arguments[1:] {
			values[i] = argument.text
		}
		var out strings.Builder
		if _, _, err := writePrintf(&out, arguments[0].text, values); err != nil {
			return awkValue{}, err
		}
		return awkString(out.String()), nil
	}
	return awkValue{}, fmt.Errorf("unsupported function %s", name)
}

// take consumes the named operator. It matches operator tokens only, so that a
// string literal such as "-" is never mistaken for one.
func (p *awkExpressionParser) take(value string) bool {
	if p.position < len(p.tokens) && p.tokens[p.position].kind == "operator" && p.tokens[p.position].text == value {
		p.position++
		return true
	}
	return false
}

func awkBool(value bool) awkValue {
	if value {
		return awkNumber(1)
	}
	return awkNumber(0)
}

func parseAwkProgram(program string) ([]awkRule, error) {
	var rules []awkRule
	for position := 0; ; {
		position = skipAwkSpace(program, position)
		if position >= len(program) {
			break
		}
		open := findAwkBrace(program, position)
		if open < 0 {
			pattern := strings.TrimSpace(awkStripComments(program[position:]))
			if pattern != "" {
				pattern, pattern2 := splitAwkRangePattern(pattern)
				rules = append(rules, awkRule{pattern: pattern, pattern2: pattern2, action: "print", kind: "record"})
			}
			break
		}
		pattern := strings.TrimSpace(awkStripComments(program[position:open]))
		close, err := matchingAwkBracket(program, open)
		if err != nil {
			return nil, err
		}
		kind := "record"
		var pattern2 string
		if pattern == "BEGIN" || pattern == "END" {
			kind, pattern = pattern, ""
		} else {
			pattern, pattern2 = splitAwkRangePattern(pattern)
		}
		rules = append(rules, awkRule{pattern: pattern, pattern2: pattern2, action: program[open+1 : close], kind: kind})
		position = close + 1
	}
	return rules, nil
}

// awkScan walks value from start, skipping string literals, regular
// expressions and comments so that structural characters inside them are
// ignored. visit receives each remaining position together with the current
// bracket nesting depth, counted after an opener and after its closer, and
// stops the scan by returning false.
func awkScan(value string, start int, visit func(i, depth int) bool) {
	previous := byte(0)
	depth := 0
	for i := start; i < len(value); {
		character := value[i]
		switch {
		case character == '"', character == '/' && awkRegexStart(previous):
			i = awkSkipLiteral(value, i)
			previous = character
			continue
		case character == '#':
			if !visit(i, depth) {
				return
			}
			for i < len(value) && value[i] != '\n' {
				i++
			}
			previous = 0
			continue
		case character == '(', character == '{', character == '[':
			depth++
		case character == ')', character == '}', character == ']':
			depth--
		}
		if !visit(i, depth) {
			return
		}
		if !strings.ContainsRune(" \t\r\n", rune(character)) {
			previous = character
		}
		i++
	}
}

// awkRegexStart reports whether a slash preceded by the given character begins
// a regular-expression literal rather than a division.
func awkRegexStart(previous byte) bool {
	return previous == 0 || strings.IndexByte("(,{}~!&|=<>+-*%;", previous) >= 0
}

// awkStripComments removes # comments that lie outside string and regular
// expression literals.
func awkStripComments(value string) string {
	var builder strings.Builder
	kept := 0
	awkScan(value, 0, func(i, depth int) bool {
		if value[i] != '#' {
			return true
		}
		builder.WriteString(value[kept:i])
		kept = len(value)
		if end := strings.IndexByte(value[i:], '\n'); end >= 0 {
			kept = i + end
		}
		return true
	})
	if kept == 0 {
		return value
	}
	builder.WriteString(value[kept:])
	return builder.String()
}

// awkSkipLiteral returns the index just past the string or regular expression
// literal beginning at i.
func awkSkipLiteral(value string, i int) int {
	quote := value[i]
	for i++; i < len(value); i++ {
		if value[i] == '\\' {
			i++
			continue
		}
		if value[i] == quote {
			return i + 1
		}
	}
	return len(value)
}

func findAwkBrace(value string, start int) int {
	brace := -1
	awkScan(value, start, func(i, depth int) bool {
		if value[i] == '{' && depth == 1 {
			brace = i
			return false
		}
		return true
	})
	return brace
}

// matchingAwkBracket returns the index of the bracket closing the ( or { at
// open, honouring nesting, literals and comments.
func matchingAwkBracket(value string, open int) (int, error) {
	closer := byte(')')
	if value[open] == '{' {
		closer = '}'
	}
	match := -1
	awkScan(value, open, func(i, depth int) bool {
		if value[i] == closer && depth == 0 {
			match = i
			return false
		}
		return true
	})
	if match < 0 {
		return 0, fmt.Errorf("missing %c", closer)
	}
	return match, nil
}

// awkStatementEnd returns the index just past the single statement beginning at
// start. An if statement takes its trailing else branch with it, so that a
// semicolon before the else does not terminate the statement.
func awkStatementEnd(value string, start int) int {
	start = skipAwkSpace(value, start)
	if start >= len(value) {
		return len(value)
	}
	if value[start] == '{' {
		close, err := matchingAwkBracket(value, start)
		if err != nil {
			return len(value)
		}
		return close + 1
	}
	if awkKeyword(value[start:], "if") {
		open := skipAwkSpace(value, start+len("if"))
		if open >= len(value) || value[open] != '(' {
			return len(value)
		}
		close, err := matchingAwkBracket(value, open)
		if err != nil {
			return len(value)
		}
		end := awkStatementEnd(value, close+1)
		if tail := skipAwkSpace(value, end); awkKeyword(value[tail:], "else") {
			return awkStatementEnd(value, tail+len("else"))
		}
		return end
	}
	for _, keyword := range []string{"for", "while"} {
		if !awkKeyword(value[start:], keyword) {
			continue
		}
		open := skipAwkSpace(value, start+len(keyword))
		if open >= len(value) || value[open] != '(' {
			return len(value)
		}
		close, err := matchingAwkBracket(value, open)
		if err != nil {
			return len(value)
		}
		return awkStatementEnd(value, close+1)
	}
	if awkKeyword(value[start:], "do") {
		bodyEnd := awkStatementEnd(value, start+len("do"))
		tail := skipAwkSpace(value, bodyEnd)
		if !awkKeyword(value[tail:], "while") {
			return bodyEnd
		}
		open := skipAwkSpace(value, tail+len("while"))
		if open >= len(value) || value[open] != '(' {
			return bodyEnd
		}
		close, err := matchingAwkBracket(value, open)
		if err != nil {
			return bodyEnd
		}
		after := skipAwkSpace(value, close+1)
		if after < len(value) && value[after] == ';' {
			after++
		}
		return after
	}
	end := len(value)
	awkScan(value, start, func(i, depth int) bool {
		if depth == 0 && (value[i] == ';' || value[i] == '\n') {
			end = i
			return false
		}
		return true
	})
	return end
}

// awkKeyword reports whether value begins with the keyword as a whole word.
func awkKeyword(value, keyword string) bool {
	if !strings.HasPrefix(value, keyword) {
		return false
	}
	rest := value[len(keyword):]
	return rest == "" || !isAwkNameStart(rest[0]) && (rest[0] < '0' || rest[0] > '9')
}

// splitAwkArguments splits a comma-separated expression list, ignoring commas
// nested inside brackets or literals.
func splitAwkArguments(value string) []string {
	return splitAwkTopLevel(value, ',')
}

// splitAwkTopLevel splits value on sep, ignoring occurrences nested inside
// brackets, strings, or regular expressions.
func splitAwkTopLevel(value string, sep byte) []string {
	var parts []string
	start := 0
	awkScan(value, 0, func(i, depth int) bool {
		if value[i] == sep && depth == 0 {
			parts = append(parts, value[start:i])
			start = i + 1
		}
		return true
	})
	return append(parts, value[start:])
}

// findAwkTopLevelWord returns the index of word's first occurrence in value
// as a whole word at bracket depth 0 (not inside a string, regex, or a
// nested pair), or -1 if there is none. It is used to find the "in" keyword
// in a "for (key in array)" clause.
func findAwkTopLevelWord(value, word string) int {
	found := -1
	awkScan(value, 0, func(i, depth int) bool {
		if depth != 0 || !strings.HasPrefix(value[i:], word) {
			return true
		}
		before := i == 0 || !isAwkNameStart(value[i-1]) && (value[i-1] < '0' || value[i-1] > '9')
		after := i+len(word) >= len(value) ||
			!isAwkNameStart(value[i+len(word)]) && (value[i+len(word)] < '0' || value[i+len(word)] > '9')
		if before && after {
			found = i
			return false
		}
		return true
	})
	return found
}

func findAwkOperator(value, operator string) int {
	position := -1
	awkScan(value, 0, func(i, depth int) bool {
		if depth != 0 || !strings.HasPrefix(value[i:], operator) {
			return true
		}
		if operator == "=" {
			// Skip the = of ==, !=, <= and >=, which are comparisons.
			if i > 0 && strings.ContainsRune("!<>=", rune(value[i-1])) || strings.HasPrefix(value[i:], "==") {
				return true
			}
		}
		position = i
		return false
	})
	return position
}

func skipAwkSpace(value string, position int) int {
	for position < len(value) && strings.ContainsRune(" \t\r\n;", rune(value[position])) {
		position++
	}
	return position
}

func validAwkAssignment(value string) bool {
	name, _, ok := strings.Cut(value, "=")
	return ok && validAwkName(name)
}

func validAwkName(value string) bool {
	if value == "" || !isAwkNameStart(value[0]) {
		return false
	}
	for i := 1; i < len(value); i++ {
		if !isAwkNameStart(value[i]) && (value[i] < '0' || value[i] > '9') {
			return false
		}
	}
	return true
}

func isAwkNameStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func awkLiteral(value string) awkValue {
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err == nil {
		return awkValue{text: value, number: number, numeric: true}
	}
	return awkString(value)
}
