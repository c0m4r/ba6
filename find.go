// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// errFindQuit unwinds the expression when -quit fires. The original stops the
// whole run at that point, before the implicit -print can write anything.
var errFindQuit = errors.New("find: -quit")

// findQuote renders a name the way find quotes one in a diagnostic, which is
// the older `x' style rather than the 'x' the other tools use.
func findQuote(text string) string { return "`" + text + "'" }

type findEntry struct {
	path  string
	info  os.FileInfo
	depth int
}

// stat returns the entry's raw stat data, which most predicates need.
func (e findEntry) stat() (*syscall.Stat_t, bool) {
	st, ok := e.info.Sys().(*syscall.Stat_t)
	return st, ok
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

// findRun carries the traversal options and the state the actions change.
type findRun struct {
	follow     byte // 'P' (default), 'L' or 'H'
	xdev       bool
	depthFirst bool
	minDepth   int
	maxDepth   int
	out        *bufio.Writer
	status     int
	quit       bool
	prune      bool
	rootDev    uint64
	haveRoot   bool
	root       string // the operand the current walk started from, for %P
	uids       map[uint32]string
	gids       map[uint32]string
	mounts     []mountInfo
	haveMounts bool
}

type findParser struct {
	tokens    []string
	pos       int
	hasAction bool
	run       *findRun
}

func cmdFind(args []string) int {
	run := &findRun{
		follow:   'P',
		maxDepth: -1,
		out:      bufio.NewWriter(os.Stdout),
		uids:     map[uint32]string{},
		gids:     map[uint32]string{},
	}
	// -H, -L and -P precede the paths, as in the original.
	for len(args) > 0 && (args[0] == "-H" || args[0] == "-L" || args[0] == "-P") {
		run.follow = args[0][1]
		args = args[1:]
	}
	paths, expression := splitFindArguments(args)
	if len(paths) == 0 {
		paths = []string{"."}
	}
	// The global options may appear anywhere in the expression but apply to
	// the whole run, so they are lifted out before it is parsed.
	filtered := make([]string, 0, len(expression))
	for i := 0; i < len(expression); i++ {
		switch expression[i] {
		case "-mindepth", "-maxdepth":
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
				run.minDepth = depth
			} else {
				run.maxDepth = depth
			}
			i++
		case "-depth", "-d":
			run.depthFirst = true
		case "-xdev", "-mount":
			run.xdev = true
		case "-follow":
			run.follow = 'L'
		case "-H", "-L", "-P":
			run.follow = expression[i][1]
		case "-nowarn", "-noleaf", "-warn", "-ignore_readdir_race", "-noignore_readdir_race":
			// Accepted and without effect here.
		default:
			filtered = append(filtered, expression[i])
		}
	}
	parser := findParser{tokens: filtered, run: run}
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
	if !parser.hasAction {
		// The expression carries an implicit -print unless it acts itself.
		printer := findExprFunc(func(entry findEntry) (bool, error) {
			run.print(entry.path, "\n")
			return true, nil
		})
		expr = findAnd{left: expr, right: printer}
	}

	for _, root := range paths {
		// The operand is used exactly as written, trailing slashes included,
		// because everything the original prints is built from it.
		run.root = root
		run.walk(root, 0, true, expr)
		if run.quit {
			break
		}
	}
	if err := run.out.Flush(); err != nil {
		fatalf("find", "write error: %s", errText(err))
		run.status = 1
	}
	return run.status
}

func (r *findRun) print(text, ending string) {
	_, _ = r.out.WriteString(text)   // Flush reports the sticky error.
	_, _ = r.out.WriteString(ending) // Flush reports the sticky error.
}

// statOf follows symlinks according to -L and -H, falling back to the link
// itself when what it points at cannot be reached, the way the original does.
func (r *findRun) statOf(path string, isRoot bool) (os.FileInfo, error) {
	if r.follow == 'L' || (r.follow == 'H' && isRoot) {
		if info, err := os.Stat(path); err == nil {
			return info, nil
		}
	}
	return os.Lstat(path)
}

// walk visits one path, evaluating expr for it and descending when it is a
// directory the options allow.
func (r *findRun) walk(path string, depth int, isRoot bool, expr findExpr) {
	if r.quit {
		return
	}
	info, err := r.statOf(path, isRoot)
	if err != nil {
		fatalf("find", "%s: %s", quoteForceName(path), errText(err))
		r.status = 1
		return
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if isRoot && !r.haveRoot {
			r.rootDev, r.haveRoot = st.Dev, true
		}
	}

	visit := func() {
		if depth < r.minDepth || (r.maxDepth >= 0 && depth > r.maxDepth) {
			return
		}
		r.prune = false
		if _, evalErr := expr.eval(findEntry{path: path, info: info, depth: depth}); evalErr != nil && !errors.Is(evalErr, errFindQuit) {
			fatalf("find", "%v", evalErr)
			r.status = 1
		}
	}

	descend := func() {
		if !info.IsDir() || r.quit {
			return
		}
		if r.maxDepth >= 0 && depth >= r.maxDepth {
			return
		}
		if r.xdev && !isRoot {
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Dev != r.rootDev {
				return
			}
		}
		names, err := readDirRaw(path)
		if err != nil {
			fatalf("find", "%s: %s", quoteForceName(path), errText(err))
			r.status = 1
			return
		}
		for _, name := range names {
			r.walk(findJoin(path, name), depth+1, false, expr)
			if r.quit {
				return
			}
		}
	}

	if r.depthFirst {
		descend()
		visit()
		return
	}
	visit()
	// -prune stops the descent into the directory just visited.
	if !r.prune {
		descend()
	}
}

// findJoin appends a name to a directory path without cleaning away the "./"
// of the default operand, which the original keeps in what it prints.
func findJoin(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
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
	if expr, handled, err := p.parseNameTest(token); handled {
		return expr, err
	}
	if expr, handled, err := p.parseAttributeTest(token); handled {
		return expr, err
	}
	if expr, handled, err := p.parseTimeTest(token); handled {
		return expr, err
	}
	if expr, handled, err := p.parseAction(token); handled {
		return expr, err
	}
	return nil, fmt.Errorf("unknown predicate %s", findQuote(token))
}

// parseNameTest handles the predicates that look at a path or a link target.
func (p *findParser) parseNameTest(token string) (findExpr, bool, error) {
	switch token {
	case "-name", "-iname", "-path", "-ipath", "-wholename", "-iwholename", "-lname", "-ilname":
		value, err := p.argument(token)
		if err != nil {
			return nil, true, err
		}
		ignoreCase := strings.HasPrefix(token, "-i")
		wholePath := strings.Contains(token, "path") || strings.Contains(token, "wholename")
		linkTarget := strings.Contains(token, "lname")
		return findExprFunc(func(entry findEntry) (bool, error) {
			candidate := filepath.Base(entry.path)
			switch {
			case linkTarget:
				if entry.info.Mode()&os.ModeSymlink == 0 {
					return false, nil
				}
				target, err := os.Readlink(entry.path)
				if err != nil {
					return false, nil
				}
				candidate = target
			case wholePath:
				candidate = entry.path
			}
			pattern := value
			if ignoreCase {
				candidate, pattern = strings.ToLower(candidate), strings.ToLower(pattern)
			}
			return globMatch(pattern, candidate), nil
		}), true, nil
	case "-regex", "-iregex":
		value, err := p.argument(token)
		if err != nil {
			return nil, true, err
		}
		// The original anchors the expression at both ends and matches it
		// against the whole path as it was written.
		expression := "^(?:" + value + ")$"
		if token == "-iregex" {
			expression = "(?i)" + expression
		}
		re, err := regexp.Compile(expression)
		if err != nil {
			return nil, true, fmt.Errorf("invalid regular expression %q", value)
		}
		return findExprFunc(func(entry findEntry) (bool, error) {
			return re.MatchString(entry.path), nil
		}), true, nil
	case "-samefile":
		value, err := p.argument(token)
		if err != nil {
			return nil, true, err
		}
		reference, err := os.Stat(value)
		if err != nil {
			return nil, true, fmt.Errorf("%s: %s", value, errText(err))
		}
		want, ok := reference.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, true, fmt.Errorf("cannot read %s", value)
		}
		return findExprFunc(func(entry findEntry) (bool, error) {
			st, ok := entry.stat()
			return ok && st.Dev == want.Dev && st.Ino == want.Ino, nil
		}), true, nil
	case "-fstype":
		value, err := p.argument(token)
		if err != nil {
			return nil, true, err
		}
		return findExprFunc(func(entry findEntry) (bool, error) {
			return p.run.filesystemType(entry.path) == value, nil
		}), true, nil
	}
	return nil, false, nil
}

// parseAttributeTest handles the predicates that read the inode's own fields.
func (p *findParser) parseAttributeTest(token string) (findExpr, bool, error) {
	switch token {
	case "-true":
		return findExprFunc(func(findEntry) (bool, error) { return true, nil }), true, nil
	case "-false":
		return findExprFunc(func(findEntry) (bool, error) { return false, nil }), true, nil
	case "-type", "-xtype":
		value, err := p.argument(token)
		if err != nil || len(value) != 1 || !strings.ContainsRune("fdlbcps", rune(value[0])) {
			return nil, true, fmt.Errorf("invalid argument to -type: %q", value)
		}
		crossed := token == "-xtype"
		run := p.run
		return findExprFunc(func(entry findEntry) (bool, error) {
			mode := entry.info.Mode()
			// -xtype asks about the end of a symlink the walk is not already
			// looking at: what the link points to by default, and the link
			// itself under -L. A link that leads nowhere stays a link.
			if crossed {
				if run.follow == 'L' {
					if link, err := os.Lstat(entry.path); err == nil {
						mode = link.Mode()
					}
				} else if mode&os.ModeSymlink != 0 {
					if target, err := os.Stat(entry.path); err == nil {
						mode = target.Mode()
					}
				}
			}
			return findType(mode) == value[0], nil
		}), true, nil
	case "-empty":
		return findExprFunc(func(entry findEntry) (bool, error) {
			if entry.info.IsDir() {
				contents, err := os.ReadDir(entry.path)
				return err == nil && len(contents) == 0, err
			}
			return entry.info.Mode().IsRegular() && entry.info.Size() == 0, nil
		}), true, nil
	case "-size":
		value, err := p.argument(token)
		if err != nil {
			return nil, true, err
		}
		comparison, count, unit, err := parseFindSize(value)
		if err != nil {
			return nil, true, err
		}
		return findExprFunc(func(entry findEntry) (bool, error) {
			actual := entry.info.Size()
			if unit > 1 {
				actual = (actual + unit - 1) / unit
			}
			return compareFindNumber(actual, count, comparison), nil
		}), true, nil
	case "-perm":
		value, err := p.argument(token)
		if err != nil {
			return nil, true, err
		}
		return parseFindPerm(value)
	case "-links", "-inum":
		value, err := p.argument(token)
		if err != nil {
			return nil, true, err
		}
		comparison, count, err := parseFindNumber(value)
		if err != nil {
			return nil, true, fmt.Errorf("invalid %s value %q", token, value)
		}
		wantLinks := token == "-links"
		return findExprFunc(func(entry findEntry) (bool, error) {
			st, ok := entry.stat()
			if !ok {
				return false, nil
			}
			actual := int64(st.Ino) //nolint:gosec // G115: an inode number fits in an int64 on Linux.
			if wantLinks {
				actual = int64(st.Nlink) //nolint:gosec // G115: a link count fits in an int64.
			}
			return compareFindNumber(actual, count, comparison), nil
		}), true, nil
	case "-user", "-group", "-uid", "-gid", "-nouser", "-nogroup":
		return p.parseOwnerTest(token)
	case "-readable", "-writable", "-executable":
		mode := uint32(4)
		switch token {
		case "-writable":
			mode = 2
		case "-executable":
			mode = 1
		}
		return findExprFunc(func(entry findEntry) (bool, error) {
			return syscall.Access(entry.path, mode) == nil, nil
		}), true, nil
	}
	return nil, false, nil
}

// parseOwnerTest handles -user, -group, their numeric forms and the two
// "orphaned id" tests.
func (p *findParser) parseOwnerTest(token string) (findExpr, bool, error) {
	if token == "-nouser" || token == "-nogroup" {
		wantUser := token == "-nouser"
		return findExprFunc(func(entry findEntry) (bool, error) {
			st, ok := entry.stat()
			if !ok {
				return false, nil
			}
			if wantUser {
				_, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
				return err != nil, nil
			}
			_, err := user.LookupGroupId(strconv.FormatUint(uint64(st.Gid), 10))
			return err != nil, nil
		}), true, nil
	}
	value, err := p.argument(token)
	if err != nil {
		return nil, true, err
	}
	wantUser := token == "-user" || token == "-uid"
	comparison := byte('=')
	var want int64
	if token == "-uid" || token == "-gid" {
		comparison, want, err = parseFindNumber(value)
		if err != nil {
			return nil, true, fmt.Errorf("invalid %s value %q", token, value)
		}
	} else if id, convErr := strconv.ParseInt(value, 10, 64); convErr == nil {
		want = id
	} else if wantUser {
		account, lookupErr := user.Lookup(value)
		if lookupErr != nil {
			return nil, true, fmt.Errorf("%s is not the name of a known user", findQuote(value))
		}
		want, _ = strconv.ParseInt(account.Uid, 10, 64)
	} else {
		group, lookupErr := user.LookupGroup(value)
		if lookupErr != nil {
			return nil, true, fmt.Errorf("%s is not the name of an existing group", findQuote(value))
		}
		want, _ = strconv.ParseInt(group.Gid, 10, 64)
	}
	return findExprFunc(func(entry findEntry) (bool, error) {
		st, ok := entry.stat()
		if !ok {
			return false, nil
		}
		actual := int64(st.Gid)
		if wantUser {
			actual = int64(st.Uid)
		}
		return compareFindNumber(actual, want, comparison), nil
	}), true, nil
}

// parseTimeTest handles the age and comparison predicates on the three stamps.
func (p *findParser) parseTimeTest(token string) (findExpr, bool, error) {
	unit := time.Hour * 24
	var stamp byte
	switch token {
	case "-mtime":
		stamp = 'm'
	case "-atime":
		stamp = 'a'
	case "-ctime":
		stamp = 'c'
	case "-mmin":
		stamp, unit = 'm', time.Minute
	case "-amin":
		stamp, unit = 'a', time.Minute
	case "-cmin":
		stamp, unit = 'c', time.Minute
	case "-newer", "-anewer", "-cnewer":
		value, err := p.argument(token)
		if err != nil {
			return nil, true, err
		}
		reference, err := os.Stat(value)
		if err != nil {
			return nil, true, fmt.Errorf("%s: %s", value, errText(err))
		}
		want := findStampOf(reference, map[string]byte{"-newer": 'm', "-anewer": 'a', "-cnewer": 'c'}[token])
		return findExprFunc(func(entry findEntry) (bool, error) {
			return findStampOf(entry.info, 'm').After(want), nil
		}), true, nil
	default:
		return nil, false, nil
	}
	value, err := p.argument(token)
	if err != nil {
		return nil, true, err
	}
	comparison, count, err := parseFindNumber(value)
	if err != nil {
		return nil, true, fmt.Errorf("invalid %s value %q", token, value)
	}
	return findExprFunc(func(entry findEntry) (bool, error) {
		// The original divides the age by the unit and drops the remainder.
		age := int64(time.Since(findStampOf(entry.info, stamp)) / unit)
		return compareFindNumber(age, count, comparison), nil
	}), true, nil
}

// parseAction handles the predicates that produce output or change the tree.
func (p *findParser) parseAction(token string) (findExpr, bool, error) {
	run := p.run
	switch token {
	case "-print", "-print0":
		p.hasAction = true
		ending := "\n"
		if token == "-print0" {
			ending = "\x00"
		}
		return findExprFunc(func(entry findEntry) (bool, error) {
			run.print(entry.path, ending)
			return true, nil
		}), true, nil
	case "-printf":
		value, err := p.argument(token)
		if err != nil {
			return nil, true, err
		}
		p.hasAction = true
		// The original reports an unusable directive once, while parsing, and
		// then prints it verbatim for every entry.
		warnUnknownFindDirectives(value)
		return findExprFunc(func(entry findEntry) (bool, error) {
			run.print(run.formatEntry(value, entry), "")
			return true, nil
		}), true, nil
	case "-ls":
		p.hasAction = true
		return findExprFunc(func(entry findEntry) (bool, error) {
			run.print(run.longListing(entry), "\n")
			return true, nil
		}), true, nil
	case "-delete":
		p.hasAction = true
		// -delete works bottom-up, so it turns on -depth as the original does.
		run.depthFirst = true
		return findExprFunc(func(entry findEntry) (bool, error) {
			if entry.path == "." {
				return true, nil
			}
			// A directory is only removable once it is empty, which the
			// bottom-up walk has already taken care of.
			if err := os.Remove(entry.path); err != nil {
				fatalf("find", "cannot delete %s: %s", quoteForceName(entry.path), errText(err))
				run.status = 1
				return false, nil
			}
			return true, nil
		}), true, nil
	case "-prune":
		return findExprFunc(func(entry findEntry) (bool, error) {
			// With -depth the descent has already happened, and the original
			// makes -prune do nothing at all in that case.
			if !run.depthFirst && entry.info.IsDir() {
				run.prune = true
			}
			return true, nil
		}), true, nil
	case "-quit":
		return findExprFunc(func(findEntry) (bool, error) {
			run.quit = true
			return true, errFindQuit
		}), true, nil
	}
	return nil, false, nil
}

func (p *findParser) argument(option string) (string, error) {
	if p.pos >= len(p.tokens) {
		return "", fmt.Errorf("missing argument to %s", findQuote(option))
	}
	value := p.tokens[p.pos]
	p.pos++
	return value, nil
}

// parseFindPerm builds -perm's three forms: an exact mode, "-MODE" for "all of
// these bits" and "/MODE" for "any of them".
func parseFindPerm(value string) (findExpr, bool, error) {
	kind := byte('=')
	spec := value
	if len(spec) > 0 && (spec[0] == '-' || spec[0] == '/' || spec[0] == '+') {
		kind, spec = spec[0], spec[1:]
		if kind == '+' {
			// The obsolete "+MODE" spelling of "/MODE".
			kind = '/'
		}
	}
	var want uint64
	if parsed, err := strconv.ParseUint(spec, 8, 32); err == nil {
		want = parsed
	} else {
		clauses, symErr := parseSymbolicMode(spec)
		if symErr != nil {
			return nil, true, fmt.Errorf("invalid mode %s", findQuote(value))
		}
		// A symbolic mode is applied to an all-zero mode, as the original does.
		for _, clause := range clauses {
			want = applyChmodClause(want, false, clause, 0)
		}
	}
	return findExprFunc(func(entry findEntry) (bool, error) {
		bits := octalFromFileMode(entry.info.Mode())
		switch kind {
		case '-':
			return bits&want == want, nil
		case '/':
			return want == 0 || bits&want != 0, nil
		}
		return bits == want, nil
	}), true, nil
}

// findStampOf returns one of the three timestamps.
func findStampOf(info os.FileInfo, stamp byte) time.Time {
	switch stamp {
	case 'a':
		return atimeOf(info)
	case 'c':
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			return time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
		}
	}
	return info.ModTime()
}

// filesystemType names the filesystem a path lives on, for -fstype.
func (r *findRun) filesystemType(path string) string {
	if !r.haveMounts {
		mounts, err := readMountInfo()
		if err == nil {
			r.mounts = mounts
		}
		r.haveMounts = true
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	return findMount(absolute, r.mounts).fstype
}

func (r *findRun) ownerName(st *syscall.Stat_t) string {
	if name, cached := r.uids[st.Uid]; cached {
		return name
	}
	name := strconv.FormatUint(uint64(st.Uid), 10)
	if account, err := user.LookupId(name); err == nil {
		name = account.Username
	}
	r.uids[st.Uid] = name
	return name
}

func (r *findRun) groupName(st *syscall.Stat_t) string {
	if name, cached := r.gids[st.Gid]; cached {
		return name
	}
	name := strconv.FormatUint(uint64(st.Gid), 10)
	if group, err := user.LookupGroupId(name); err == nil {
		name = group.Name
	}
	r.gids[st.Gid] = name
	return name
}

// longListing renders one entry the way -ls does: the fields of "ls -dils",
// with the inode and 1K block count in front.
func (r *findRun) longListing(entry findEntry) string {
	st, ok := entry.stat()
	if !ok {
		return entry.path
	}
	blocks := uint64(st.Blocks) / 2 //nolint:gosec // G115: st_blocks is nonnegative.
	name := entry.path
	if entry.info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(entry.path); err == nil {
			name += " -> " + target
		}
	}
	return fmt.Sprintf("%9d %6d %s %3d %-8s %-8s %8d %s %s",
		st.Ino, blocks, modeString(entry.info.Mode()), st.Nlink,
		r.ownerName(st), r.groupName(st), entry.info.Size(),
		formatLsTime(entry.info.ModTime()), name)
}

// formatEntry renders one -printf format string for an entry.
func (r *findRun) formatEntry(format string, entry findEntry) string {
	var b strings.Builder
	st, haveStat := entry.stat()
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c == '\\' && i+1 < len(format) {
			i++
			switch format[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'f':
				b.WriteByte('\f')
			case 'v':
				b.WriteByte('\v')
			case 'b':
				b.WriteByte('\b')
			case 'a':
				b.WriteByte('\a')
			case '0':
				b.WriteByte(0)
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte('\\')
				b.WriteByte(format[i])
			}
			continue
		}
		if c != '%' || i+1 >= len(format) {
			b.WriteByte(c)
			continue
		}
		i++
		directive := format[i]
		// The time directives take a second letter naming the field.
		if directive == 'T' || directive == 'A' || directive == 'C' {
			if i+1 >= len(format) {
				b.WriteByte('%')
				b.WriteByte(directive)
				continue
			}
			i++
			stamp := map[byte]byte{'T': 'm', 'A': 'a', 'C': 'c'}[directive]
			b.WriteString(findTimeField(findStampOf(entry.info, stamp), format[i]))
			continue
		}
		b.WriteString(r.formatDirective(directive, entry, st, haveStat))
	}
	return b.String()
}

func (r *findRun) formatDirective(directive byte, entry findEntry, st *syscall.Stat_t, haveStat bool) string {
	switch directive {
	case '%':
		return "%"
	case 'p':
		return entry.path
	case 'f':
		return filepath.Base(entry.path)
	case 'h':
		// Everything before the last slash of the path as it was written; a
		// name with no slash at all reports the current directory.
		slash := strings.LastIndexByte(entry.path, '/')
		if slash < 0 {
			return "."
		}
		return entry.path[:slash]
	case 'P':
		// The path with the command-line operand it was found under removed.
		return strings.TrimPrefix(strings.TrimPrefix(entry.path, r.root), "/")
	case 's':
		return strconv.FormatInt(entry.info.Size(), 10)
	case 'm':
		return fmt.Sprintf("%o", octalFromFileMode(entry.info.Mode()))
	case 'M':
		return modeString(entry.info.Mode())
	case 'y':
		kind := findType(entry.info.Mode())
		if kind == 0 {
			return "U"
		}
		return string(kind)
	case 'd':
		return strconv.Itoa(entry.depth)
	case 'l':
		if entry.info.Mode()&os.ModeSymlink == 0 {
			return ""
		}
		target, err := os.Readlink(entry.path)
		if err != nil {
			return ""
		}
		return target
	case 't':
		return findCtimeText(entry.info.ModTime())
	case 'a':
		return findCtimeText(findStampOf(entry.info, 'a'))
	case 'c':
		return findCtimeText(findStampOf(entry.info, 'c'))
	}
	if !haveStat {
		return "%" + string(directive)
	}
	switch directive {
	case 'i':
		return strconv.FormatUint(st.Ino, 10)
	case 'n':
		return strconv.FormatUint(st.Nlink, 10)
	case 'u':
		return r.ownerName(st)
	case 'g':
		return r.groupName(st)
	case 'U':
		return strconv.FormatUint(uint64(st.Uid), 10)
	case 'G':
		return strconv.FormatUint(uint64(st.Gid), 10)
	case 'b':
		return strconv.FormatInt(st.Blocks, 10)
	case 'k':
		return strconv.FormatInt((st.Blocks+1)/2, 10)
	case 'D':
		return strconv.FormatUint(st.Dev, 10)
	}
	return "%" + string(directive)
}

// findFormatDirectives are the %-directives -printf understands here; the time
// ones (%A, %C, %T) take a second letter and are handled separately.
const findFormatDirectives = "%pfhPsmMyndltacigUGbkDi"

// warnUnknownFindDirectives reports the directives in a -printf format that
// this applet cannot render, which are then written out as they were typed.
func warnUnknownFindDirectives(format string) {
	for i := 0; i < len(format); i++ {
		if format[i] == '\\' && i+1 < len(format) {
			i++
			continue
		}
		if format[i] != '%' || i+1 >= len(format) {
			continue
		}
		i++
		if format[i] == 'T' || format[i] == 'A' || format[i] == 'C' {
			i++
			continue
		}
		if strings.IndexByte(findFormatDirectives, format[i]) < 0 {
			fatalf("find", "warning: unrecognized format directive %s", findQuote("%"+string(format[i])))
		}
	}
}

// findTimeField renders one %T/%A/%C field. "@" is the epoch with the
// fractional part the original prints; the rest reuse date's directives.
func findTimeField(value time.Time, letter byte) string {
	if letter == '@' {
		return fmt.Sprintf("%d.%010d", value.Unix(), value.Nanosecond()*10)
	}
	// A letter formatDate does not know is written out as it was typed.
	text, err := formatDate(value, "%"+string(letter))
	if err != nil {
		return "%" + string(letter)
	}
	return text
}

// findCtimeText is the ctime(3)-like stamp -printf's %t writes, with the
// fractional seconds the original inserts before the year.
func findCtimeText(value time.Time) string {
	return fmt.Sprintf("%s %s %2d %02d:%02d:%02d.%010d %d",
		value.Format("Mon"), value.Format("Jan"), value.Day(),
		value.Hour(), value.Minute(), value.Second(), value.Nanosecond()*10, value.Year())
}

// globMatch implements fnmatch(3) without FNM_PATHNAME: "*" and "?" match any
// byte, "/" included, which is what find's -name, -path and -lname patterns
// mean. filepath.Match cannot express that.
func globMatch(pattern, name string) bool {
	p, n := 0, 0
	star, mark := -1, 0
	for n < len(name) {
		if p < len(pattern) {
			switch pattern[p] {
			case '*':
				star, mark = p, n
				p++
				continue
			case '?':
				p++
				n++
				continue
			case '[':
				if next, matched, ok := globBracket(pattern, p, name[n]); ok {
					if matched {
						p, n = next, n+1
						continue
					}
					break
				}
				if pattern[p] == name[n] {
					p++
					n++
					continue
				}
			case '\\':
				if p+1 < len(pattern) && pattern[p+1] == name[n] {
					p += 2
					n++
					continue
				}
			default:
				if pattern[p] == name[n] {
					p++
					n++
					continue
				}
			}
		}
		if star < 0 {
			return false
		}
		// Backtrack: let the last "*" swallow one more byte.
		p = star + 1
		mark++
		n = mark
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// globBracket matches one [...] set against c. It returns the index just past
// the set, whether c matched, and whether the set was well formed at all.
func globBracket(pattern string, start int, c byte) (int, bool, bool) {
	i := start + 1
	negated := false
	if i < len(pattern) && (pattern[i] == '!' || pattern[i] == '^') {
		negated = true
		i++
	}
	matched := false
	first := true
	for i < len(pattern) {
		if pattern[i] == ']' && !first {
			return i + 1, matched != negated, true
		}
		first = false
		if pattern[i] == '\\' && i+1 < len(pattern) {
			i++
			if pattern[i] == c {
				matched = true
			}
			i++
			continue
		}
		if i+2 < len(pattern) && pattern[i+1] == '-' && pattern[i+2] != ']' {
			if c >= pattern[i] && c <= pattern[i+2] {
				matched = true
			}
			i += 3
			continue
		}
		if pattern[i] == c {
			matched = true
		}
		i++
	}
	// No closing bracket: the "[" was a literal.
	return start, false, false
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
		case 'b':
			unit, value = 512, value[:len(value)-1]
		case 'c':
			unit, value = 1, value[:len(value)-1]
		case 'w':
			unit, value = 2, value[:len(value)-1]
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
