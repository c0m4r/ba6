package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func cmdBracket(args []string) int {
	if len(args) == 0 || args[len(args)-1] != "]" {
		fatalf("[", "missing ']'")
		return 2
	}
	return runTestExpression("[", args[:len(args)-1])
}

func cmdTest(args []string) int { return runTestExpression("test", args) }

func runTestExpression(prog string, args []string) int {
	if len(args) == 0 {
		return 1
	}
	if len(args) == 1 {
		if args[0] != "" {
			return 0
		}
		return 1
	}
	if len(args) == 2 && args[0] == "!" {
		if args[1] == "" {
			return 0
		}
		return 1
	}
	if len(args) == 3 && isTestBinary(args[1]) {
		value, err := evalTestBinary(args[0], args[1], args[2])
		if err != nil {
			fatalf(prog, "%v", err)
			return 2
		}
		if value {
			return 0
		}
		return 1
	}
	p := testParser{args: args}
	value, err := p.parseOr()
	if err == nil && p.pos != len(args) {
		err = fmt.Errorf("unexpected operand %q", args[p.pos])
	}
	if err != nil {
		fatalf(prog, "%v", err)
		return 2
	}
	if value {
		return 0
	}
	return 1
}

type testParser struct {
	args []string
	pos  int
}

func (p *testParser) parseOr() (bool, error) {
	left, err := p.parseAnd()
	for err == nil && p.take("-o") {
		var right bool
		right, err = p.parseAnd()
		left = left || right
	}
	return left, err
}

func (p *testParser) parseAnd() (bool, error) {
	left, err := p.parseNot()
	for err == nil && p.take("-a") {
		var right bool
		right, err = p.parseNot()
		left = left && right
	}
	return left, err
}

func (p *testParser) parseNot() (bool, error) {
	if p.take("!") {
		value, err := p.parseNot()
		return !value, err
	}
	return p.parsePrimary()
}

func (p *testParser) parsePrimary() (bool, error) {
	if p.take("(") {
		value, err := p.parseOr()
		if err != nil {
			return false, err
		}
		if !p.take(")") {
			return false, fmt.Errorf("missing ')'")
		}
		return value, nil
	}
	if p.pos >= len(p.args) {
		return false, fmt.Errorf("missing expression")
	}
	if isTestUnary(p.args[p.pos]) {
		op := p.args[p.pos]
		p.pos++
		if p.pos >= len(p.args) {
			return false, fmt.Errorf("%s requires an operand", op)
		}
		value := p.args[p.pos]
		p.pos++
		return evalTestUnary(op, value)
	}
	left := p.args[p.pos]
	p.pos++
	if p.pos < len(p.args) && isTestBinary(p.args[p.pos]) {
		op := p.args[p.pos]
		p.pos++
		if p.pos >= len(p.args) {
			return false, fmt.Errorf("%s requires a right operand", op)
		}
		right := p.args[p.pos]
		p.pos++
		return evalTestBinary(left, op, right)
	}
	return left != "", nil
}

func (p *testParser) take(value string) bool {
	if p.pos < len(p.args) && p.args[p.pos] == value {
		p.pos++
		return true
	}
	return false
}

func isTestUnary(op string) bool {
	switch op {
	case "-n", "-z", "-e", "-f", "-d", "-L", "-h", "-r", "-w", "-x", "-s", "-b", "-c", "-p", "-S", "-t":
		return true
	}
	return false
}

func evalTestUnary(op, value string) (bool, error) {
	switch op {
	case "-n":
		return value != "", nil
	case "-z":
		return value == "", nil
	case "-t":
		fd, err := strconv.Atoi(value)
		if err != nil || fd < 0 {
			return false, fmt.Errorf("invalid file descriptor %q", value)
		}
		_, err = unixWinsize(uintptr(fd))
		return err == nil, nil
	case "-L", "-h":
		info, err := os.Lstat(value)
		return err == nil && info.Mode()&os.ModeSymlink != 0, nil
	case "-r":
		return syscall.Access(value, 4) == nil, nil
	case "-w":
		return syscall.Access(value, 2) == nil, nil
	case "-x":
		return syscall.Access(value, 1) == nil, nil
	}
	info, err := os.Stat(value)
	if err != nil {
		return false, nil
	}
	switch op {
	case "-e":
		return true, nil
	case "-f":
		return info.Mode().IsRegular(), nil
	case "-d":
		return info.IsDir(), nil
	case "-s":
		return info.Size() > 0, nil
	case "-b":
		return info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0, nil
	case "-c":
		return info.Mode()&os.ModeCharDevice != 0, nil
	case "-p":
		return info.Mode()&os.ModeNamedPipe != 0, nil
	case "-S":
		return info.Mode()&os.ModeSocket != 0, nil
	}
	return false, fmt.Errorf("unknown unary operator %q", op)
}

func isTestBinary(op string) bool {
	switch op {
	case "=", "==", "!=", "<", ">", "-eq", "-ne", "-lt", "-le", "-gt", "-ge", "-nt", "-ot", "-ef":
		return true
	}
	return false
}

func evalTestBinary(left, op, right string) (bool, error) {
	switch op {
	case "=", "==":
		return left == right, nil
	case "!=":
		return left != right, nil
	case "<":
		return left < right, nil
	case ">":
		return left > right, nil
	case "-nt", "-ot", "-ef":
		leftInfo, leftErr := os.Stat(left)
		rightInfo, rightErr := os.Stat(right)
		if op == "-ef" {
			return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo), nil
		}
		if op == "-nt" {
			if leftErr == nil && rightErr != nil {
				return true, nil
			}
			if leftErr != nil || rightErr != nil {
				return false, nil
			}
			return leftInfo.ModTime().After(rightInfo.ModTime()), nil
		}
		if leftErr != nil && rightErr == nil {
			return true, nil
		}
		if leftErr != nil || rightErr != nil {
			return false, nil
		}
		return leftInfo.ModTime().Before(rightInfo.ModTime()), nil
	}
	a, err := strconv.ParseInt(left, 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid integer %q", left)
	}
	b, err := strconv.ParseInt(right, 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid integer %q", right)
	}
	switch op {
	case "-eq":
		return a == b, nil
	case "-ne":
		return a != b, nil
	case "-lt":
		return a < b, nil
	case "-le":
		return a <= b, nil
	case "-gt":
		return a > b, nil
	case "-ge":
		return a >= b, nil
	}
	return false, fmt.Errorf("unknown binary operator %q", op)
}
