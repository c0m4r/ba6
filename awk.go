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
// needed in recovery scripts. It deliberately keeps the language bounded: no
// arrays, user functions, getline, redirection, or external command execution.

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

func (ctx *awkContext) matches(pattern string) (bool, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return true, nil
	}
	if len(pattern) >= 2 && pattern[0] == '/' && pattern[len(pattern)-1] == '/' {
		re, err := regexp.Compile(pattern[1 : len(pattern)-1])
		return err == nil && re.MatchString(ctx.record), err
	}
	value, err := ctx.eval(pattern)
	return err == nil && value.truth(), err
}

func (ctx *awkContext) runAction(action string) error {
	for _, statement := range splitAwkList(action, true) {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		switch {
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
			if err := ctx.awkPrint(strings.TrimSpace(strings.TrimPrefix(statement, "print"))); err != nil {
				return err
			}
		case strings.HasPrefix(statement, "printf "):
			if err := ctx.awkPrintf(strings.TrimSpace(strings.TrimPrefix(statement, "printf"))); err != nil {
				return err
			}
		default:
			if err := ctx.awkAssignment(statement); err != nil {
				return err
			}
		}
	}
	return nil
}

func (ctx *awkContext) awkPrint(expressions string) error {
	if expressions == "" {
		_, err := io.WriteString(os.Stdout, ctx.record+ctx.get("ORS").text)
		return err
	}
	parts := splitAwkList(expressions, false)
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
	parts := splitAwkList(expressions, false)
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
	_, err := writePrintf(os.Stdout, values[0], values[1:])
	return err
}

func (ctx *awkContext) awkAssignment(statement string) error {
	for _, operator := range []string{"+=", "-=", "*=", "/=", "="} {
		if position := findAwkOperator(statement, operator); position >= 0 {
			name := strings.TrimSpace(statement[:position])
			if !validAwkName(name) {
				return fmt.Errorf("unsupported statement %q", statement)
			}
			value, err := ctx.eval(statement[position+len(operator):])
			if err != nil {
				return err
			}
			current := ctx.get(name).asNumber()
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
			ctx.vars[name] = value
			return nil
		}
	}
	if strings.HasSuffix(statement, "++") && validAwkName(strings.TrimSpace(strings.TrimSuffix(statement, "++"))) {
		name := strings.TrimSpace(strings.TrimSuffix(statement, "++"))
		ctx.vars[name] = awkNumber(ctx.get(name).asNumber() + 1)
		return nil
	}
	return fmt.Errorf("unsupported statement %q", statement)
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
		if expression[i] == '/' {
			// A slash following ~ or !~ begins a regular-expression literal;
			// otherwise it is the division operator.
			if len(tokens) > 0 && (tokens[len(tokens)-1].text == "~" || tokens[len(tokens)-1].text == "!~") {
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
	right, err := p.parseAdd()
	if err != nil {
		return left, err
	}
	if op == "~" || op == "!~" {
		re, compileErr := regexp.Compile(right.text)
		if compileErr != nil {
			return left, compileErr
		}
		matched := re.MatchString(left.text)
		return awkBool(matched != (op == "!~")), nil
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
	case "string", "regex":
		return awkString(token.text), nil
	case "number":
		number, err := strconv.ParseFloat(token.text, 64)
		return awkNumber(number), err
	case "name":
		if p.take("(") {
			return p.call(token.text)
		}
		return p.ctx.get(token.text), nil
	}
	return awkValue{}, fmt.Errorf("unexpected token %q", token.text)
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

func (p *awkExpressionParser) take(value string) bool {
	if p.position < len(p.tokens) && p.tokens[p.position].text == value {
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
			pattern := strings.TrimSpace(program[position:])
			if pattern != "" {
				rules = append(rules, awkRule{pattern: pattern, action: "print", kind: "record"})
			}
			break
		}
		pattern := strings.TrimSpace(program[position:open])
		close, err := matchingAwkBrace(program, open)
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
	if len(rules) == 0 {
		return nil, fmt.Errorf("empty program")
	}
	return rules, nil
}

func findAwkBrace(value string, start int) int {
	quoted, regex := false, false
	for i := start; i < len(value); i++ {
		if value[i] == '\\' && (quoted || regex) {
			i++
			continue
		}
		switch value[i] {
		case '"':
			if !regex {
				quoted = !quoted
			}
		case '/':
			if !quoted {
				regex = !regex
			}
		case '{':
			if !quoted && !regex {
				return i
			}
		}
	}
	return -1
}

func matchingAwkBrace(value string, open int) (int, error) {
	quoted := false
	for i := open + 1; i < len(value); i++ {
		if value[i] == '\\' && quoted {
			i++
			continue
		}
		if value[i] == '"' {
			quoted = !quoted
		} else if value[i] == '}' && !quoted {
			return i, nil
		}
	}
	return 0, fmt.Errorf("missing }")
}

func splitAwkList(value string, statements bool) []string {
	var parts []string
	start, depth, quoted := 0, 0, false
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && quoted {
			i++
			continue
		}
		switch value[i] {
		case '"':
			quoted = !quoted
		case '(':
			if !quoted {
				depth++
			}
		case ')':
			if !quoted {
				depth--
			}
		case ',', ';', '\n':
			separator := value[i] == ',' && !statements || value[i] != ',' && statements
			if !quoted && depth == 0 && separator {
				parts = append(parts, value[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, value[start:])
	return parts
}

func findAwkOperator(value, operator string) int {
	quoted, depth := false, 0
	for i := 0; i+len(operator) <= len(value); i++ {
		if value[i] == '\\' && quoted {
			i++
			continue
		}
		if value[i] == '"' {
			quoted = !quoted
		} else if !quoted && value[i] == '(' {
			depth++
		} else if !quoted && value[i] == ')' {
			depth--
		} else if !quoted && depth == 0 && strings.HasPrefix(value[i:], operator) {
			if operator == "=" && i > 0 && strings.ContainsRune("!<>", rune(value[i-1])) {
				continue
			}
			return i
		}
	}
	return -1
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
