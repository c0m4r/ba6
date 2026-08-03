package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type findEntry struct {
	path  string
	info  os.FileInfo
	depth int
}

type findExpr interface {
	eval(findEntry) (bool, error)
}

type findExprFunc func(findEntry) (bool, error)

func (f findExprFunc) eval(entry findEntry) (bool, error) { return f(entry) }

type findAnd struct{ left, right findExpr }

func (a findAnd) eval(entry findEntry) (bool, error) {
	left, err := a.left.eval(entry)
	if err != nil || !left {
		return left, err
	}
	return a.right.eval(entry)
}

type findOr struct{ left, right findExpr }

func (o findOr) eval(entry findEntry) (bool, error) {
	left, err := o.left.eval(entry)
	if err != nil || left {
		return left, err
	}
	return o.right.eval(entry)
}

type findNot struct{ child findExpr }

func (n findNot) eval(entry findEntry) (bool, error) {
	value, err := n.child.eval(entry)
	return !value, err
}

type findParser struct {
	tokens    []string
	pos       int
	hasAction bool
}

func cmdFind(args []string) int {
	paths, expression := splitFindArguments(args)
	if len(paths) == 0 {
		paths = []string{"."}
	}
	minDepth, maxDepth := 0, -1
	filtered := make([]string, 0, len(expression))
	for i := 0; i < len(expression); i++ {
		if expression[i] == "-mindepth" || expression[i] == "-maxdepth" {
			if i+1 >= len(expression) {
				fatalf("find", "%s requires an argument", expression[i])
				return 1
			}
			depth, err := strconv.Atoi(expression[i+1])
			if err != nil || depth < 0 {
				fatalf("find", "invalid depth %q", expression[i+1])
				return 1
			}
			if expression[i] == "-mindepth" {
				minDepth = depth
			} else {
				maxDepth = depth
			}
			i++
			continue
		}
		filtered = append(filtered, expression[i])
	}
	parser := findParser{tokens: filtered}
	expr, err := parser.parseOr()
	if err != nil {
		fatalf("find", "%v", err)
		return 1
	}
	if parser.pos != len(parser.tokens) {
		fatalf("find", "unexpected expression token %q", parser.tokens[parser.pos])
		return 1
	}
	if expr == nil {
		expr = findExprFunc(func(findEntry) (bool, error) { return true, nil })
	}
	status := 0
	for _, root := range paths {
		cleanRoot := filepath.Clean(root)
		walkErr := filepath.Walk(cleanRoot, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				fatalf("find", "%s: %v", path, walkErr)
				status = 1
				if info != nil && info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			depth := findDepth(cleanRoot, path)
			if maxDepth >= 0 && depth > maxDepth {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if depth >= minDepth {
				matched, evalErr := expr.eval(findEntry{path: path, info: info, depth: depth})
				if evalErr != nil {
					return evalErr
				}
				if matched && !parser.hasAction {
					if _, err := fmt.Fprintln(os.Stdout, path); err != nil {
						return err
					}
				}
			}
			if maxDepth >= 0 && info.IsDir() && depth >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		})
		if walkErr != nil {
			fatalf("find", "%v", walkErr)
			status = 1
		}
	}
	return status
}

func splitFindArguments(args []string) ([]string, []string) {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	index := 0
	for index < len(args) && !isFindExpressionStart(args[index]) {
		index++
	}
	return args[:index], args[index:]
}

func isFindExpressionStart(value string) bool {
	return value == "!" || value == "(" || strings.HasPrefix(value, "-")
}

func findDepth(root, path string) int {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." {
		return 0
	}
	return strings.Count(relative, string(os.PathSeparator)) + 1
}

func (p *findParser) parseOr() (findExpr, error) {
	left, err := p.parseAnd()
	for err == nil && p.pos < len(p.tokens) && (p.tokens[p.pos] == "-o" || p.tokens[p.pos] == "-or") {
		p.pos++
		var right findExpr
		right, err = p.parseAnd()
		if right == nil && err == nil {
			err = fmt.Errorf("missing expression after -o")
		}
		left = findOr{left: left, right: right}
	}
	return left, err
}

func (p *findParser) parseAnd() (findExpr, error) {
	left, err := p.parseNot()
	for err == nil && p.pos < len(p.tokens) && p.tokens[p.pos] != ")" && p.tokens[p.pos] != "-o" && p.tokens[p.pos] != "-or" {
		if p.tokens[p.pos] == "-a" || p.tokens[p.pos] == "-and" {
			p.pos++
		}
		right, parseErr := p.parseNot()
		if parseErr != nil {
			return nil, parseErr
		}
		if right == nil {
			return nil, fmt.Errorf("missing expression")
		}
		if left == nil {
			left = right
		} else {
			left = findAnd{left: left, right: right}
		}
	}
	return left, err
}

func (p *findParser) parseNot() (findExpr, error) {
	if p.pos < len(p.tokens) && (p.tokens[p.pos] == "!" || p.tokens[p.pos] == "-not") {
		p.pos++
		child, err := p.parseNot()
		if child == nil && err == nil {
			err = fmt.Errorf("missing expression after ! operator")
		}
		return findNot{child: child}, err
	}
	return p.parsePrimary()
}

func (p *findParser) parsePrimary() (findExpr, error) {
	if p.pos >= len(p.tokens) {
		return nil, nil
	}
	if p.tokens[p.pos] == "(" {
		p.pos++
		expr, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.pos >= len(p.tokens) || p.tokens[p.pos] != ")" {
			return nil, fmt.Errorf("missing ')'")
		}
		p.pos++
		return expr, nil
	}
	token := p.tokens[p.pos]
	p.pos++
	switch token {
	case "-true":
		return findExprFunc(func(findEntry) (bool, error) { return true, nil }), nil
	case "-false":
		return findExprFunc(func(findEntry) (bool, error) { return false, nil }), nil
	case "-print", "-print0":
		p.hasAction = true
		nul := token == "-print0"
		return findExprFunc(func(entry findEntry) (bool, error) {
			ending := "\n"
			if nul {
				ending = "\x00"
			}
			_, err := fmt.Fprint(os.Stdout, entry.path, ending)
			return true, err
		}), nil
	case "-name", "-iname", "-path", "-ipath":
		value, err := p.argument(token)
		if err != nil {
			return nil, err
		}
		if _, err := filepath.Match(value, ""); err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", value, err)
		}
		ignoreCase := token == "-iname" || token == "-ipath"
		matchPath := token == "-path" || token == "-ipath"
		return findExprFunc(func(entry findEntry) (bool, error) {
			candidate := filepath.Base(entry.path)
			if matchPath {
				candidate = entry.path
			}
			pattern := value
			if ignoreCase {
				candidate, pattern = strings.ToLower(candidate), strings.ToLower(pattern)
			}
			return filepath.Match(pattern, candidate)
		}), nil
	case "-type":
		value, err := p.argument(token)
		if err != nil || len(value) != 1 || !strings.ContainsRune("fdlbcps", rune(value[0])) {
			return nil, fmt.Errorf("invalid argument to -type: %q", value)
		}
		return findExprFunc(func(entry findEntry) (bool, error) {
			return findType(entry.info.Mode()) == value[0], nil
		}), nil
	case "-empty":
		return findExprFunc(func(entry findEntry) (bool, error) {
			if entry.info.IsDir() {
				contents, err := os.ReadDir(entry.path)
				return err == nil && len(contents) == 0, err
			}
			return entry.info.Mode().IsRegular() && entry.info.Size() == 0, nil
		}), nil
	case "-size":
		value, err := p.argument(token)
		if err != nil {
			return nil, err
		}
		comparison, count, unit, err := parseFindSize(value)
		if err != nil {
			return nil, err
		}
		return findExprFunc(func(entry findEntry) (bool, error) {
			actual := entry.info.Size()
			if unit > 1 {
				actual = (actual + unit - 1) / unit
			}
			return compareFindNumber(actual, count, comparison), nil
		}), nil
	case "-mtime":
		value, err := p.argument(token)
		if err != nil {
			return nil, err
		}
		comparison, days, err := parseFindNumber(value)
		if err != nil {
			return nil, fmt.Errorf("invalid -mtime value %q", value)
		}
		return findExprFunc(func(entry findEntry) (bool, error) {
			age := int64(time.Since(entry.info.ModTime()).Hours() / 24)
			return compareFindNumber(age, days, comparison), nil
		}), nil
	case "-newer":
		value, err := p.argument(token)
		if err != nil {
			return nil, err
		}
		reference, err := os.Stat(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", value, err)
		}
		return findExprFunc(func(entry findEntry) (bool, error) {
			return entry.info.ModTime().After(reference.ModTime()), nil
		}), nil
	default:
		return nil, fmt.Errorf("unknown predicate %q", token)
	}
}

func (p *findParser) argument(option string) (string, error) {
	if p.pos >= len(p.tokens) {
		return "", fmt.Errorf("%s requires an argument", option)
	}
	value := p.tokens[p.pos]
	p.pos++
	return value, nil
}

func findType(mode os.FileMode) byte {
	switch {
	case mode.IsRegular():
		return 'f'
	case mode.IsDir():
		return 'd'
	case mode&os.ModeSymlink != 0:
		return 'l'
	case mode&os.ModeNamedPipe != 0:
		return 'p'
	case mode&os.ModeSocket != 0:
		return 's'
	case mode&os.ModeDevice != 0 && mode&os.ModeCharDevice != 0:
		return 'c'
	case mode&os.ModeDevice != 0:
		return 'b'
	}
	return 0
}

func parseFindSize(value string) (byte, int64, int64, error) {
	unit := int64(512)
	if len(value) > 0 {
		switch value[len(value)-1] {
		case 'c':
			unit, value = 1, value[:len(value)-1]
		case 'k':
			unit, value = 1024, value[:len(value)-1]
		case 'M':
			unit, value = 1024*1024, value[:len(value)-1]
		case 'G':
			unit, value = 1024*1024*1024, value[:len(value)-1]
		}
	}
	comparison, count, err := parseFindNumber(value)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid -size value %q", value)
	}
	return comparison, count, unit, nil
}

func parseFindNumber(value string) (byte, int64, error) {
	comparison := byte('=')
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		comparison, value = value[0], value[1:]
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number < 0 {
		return 0, 0, fmt.Errorf("invalid number")
	}
	return comparison, number, nil
}

func compareFindNumber(actual, expected int64, comparison byte) bool {
	switch comparison {
	case '+':
		return actual > expected
	case '-':
		return actual < expected
	default:
		return actual == expected
	}
}
