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
)

// awk implements the small but useful record-processing subset most often
// needed in recovery scripts: patterns, actions, if/else, statement blocks,
// field assignment, and sub/gsub. It deliberately keeps the language bounded:
// no arrays, loops, user functions, getline, redirection, or external command
// execution.

// awkFieldLimit caps how far a field assignment may extend a record so a stray
// expression such as $999999999 = "" cannot exhaust memory.
const awkFieldLimit = 1 << 16

type awkRule struct {
	pattern string
	action  string
	kind    string
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
	vars          map[string]awkValue
	record        string
	fields        []string
	nr, fnr       int
	next, exiting bool
	exitStatus    int
}

func cmdAwk(args []string) int {
	ctx := &awkContext{vars: map[string]awkValue{
		"FS": awkString(" "), "OFS": awkString(" "), "ORS": awkString("\n"),
	}}
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
		ctx.splitRecord()
		ctx.next = false
		for _, rule := range rules {
			if rule.kind != "record" {
				continue
			}
			matches, err := ctx.matches(rule.pattern)
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

func (ctx *awkContext) splitRecord() {
	separator := ctx.get("FS").text
	if separator == " " || separator == "" {
		ctx.fields = strings.Fields(ctx.record)
	} else {
		re, err := regexp.Compile(separator)
		if err != nil {
			ctx.fields = strings.Split(ctx.record, separator)
		} else {
			ctx.fields = re.Split(ctx.record, -1)
		}
	}
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
		ctx.splitRecord()
		return nil
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
	name  string
	field int
	isSet bool
}

func (ctx *awkContext) read(target awkLvalue) awkValue {
	if target.name == "" {
		return ctx.field(target.field)
	}
	return ctx.get(target.name)
}

func (ctx *awkContext) assign(target awkLvalue, value awkValue) error {
	if target.name == "" {
		return ctx.setField(target.field, value.text)
	}
	ctx.vars[target.name] = value
	return nil
}

// target resolves the left-hand side of an assignment, which is either a plain
// variable name or a field reference such as $1 or $(NF - 1).
func (ctx *awkContext) target(text string) (awkLvalue, error) {
	text = strings.TrimSpace(text)
	if validAwkName(text) {
		return awkLvalue{name: text, isSet: true}, nil
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
		if ctx.next || ctx.exiting {
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
		if strings.ContainsRune("$()+-*/%<>!~,", rune(expression[i])) {
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
	left, err := p.parseCompare()
	for err == nil && p.take("&&") {
		var right awkValue
		right, err = p.parseCompare()
		left = awkBool(left.truth() && right.truth())
	}
	return left, err
}

func (p *awkExpressionParser) parseCompare() (awkValue, error) {
	left, err := p.parseAdd()
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
		re, err := regexp.Compile(pattern)
		if err != nil {
			return left, err
		}
		return awkBool(re.MatchString(left.text) != (op == "!~")), nil
	}
	right, err := p.parseAdd()
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
		re, err := regexp.Compile(token.text)
		if err != nil {
			return awkValue{}, err
		}
		return awkBool(re.MatchString(p.ctx.record)), nil
	case "number":
		number, err := strconv.ParseFloat(token.text, 64)
		return awkNumber(number), err
	case "name":
		if p.take("(") {
			if token.text == "sub" || token.text == "gsub" {
				return p.substitute(token.text == "gsub")
			}
			return p.call(token.text)
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
	re, err := regexp.Compile(pattern)
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
	case "length":
		if len(arguments) == 0 {
			return awkNumber(float64(len(p.ctx.record))), nil
		}
		return awkNumber(float64(len(arguments[0].text))), nil
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
				rules = append(rules, awkRule{pattern: pattern, action: "print", kind: "record"})
			}
			break
		}
		pattern := strings.TrimSpace(awkStripComments(program[position:open]))
		close, err := matchingAwkBracket(program, open)
		if err != nil {
			return nil, err
		}
		kind := "record"
		if pattern == "BEGIN" || pattern == "END" {
			kind, pattern = pattern, ""
		}
		rules = append(rules, awkRule{pattern: pattern, action: program[open+1 : close], kind: kind})
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
		case character == '(', character == '{':
			depth++
		case character == ')', character == '}':
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
	var parts []string
	start := 0
	awkScan(value, 0, func(i, depth int) bool {
		if value[i] == ',' && depth == 0 {
			parts = append(parts, value[start:i])
			start = i + 1
		}
		return true
	})
	return append(parts, value[start:])
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
