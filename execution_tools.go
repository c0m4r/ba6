// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

func cmdEnv(args []string) int {
	clear := false
	unset := []string{}
	for len(args) > 0 {
		switch args[0] {
		case "-i", "--ignore-environment":
			clear = true
			args = args[1:]
		case "-u", "--unset":
			if len(args) < 2 {
				return 125
			}
			unset = append(unset, args[1])
			args = args[2:]
		case "--":
			args = args[1:]
			goto optionsDone
		default:
			// The first operand ends option parsing; anything that still looks
			// like an option at that point is one env does not have, and env
			// reserves 125 for its own failures.
			if len(args[0]) > 1 && args[0][0] == '-' {
				fatalf("env", "unrecognized option '%s'", args[0])
				fmt.Fprintln(os.Stderr, "Try 'env --help' for more information.")
				return 125
			}
			goto optionsDone
		}
	}
optionsDone:
	environment := os.Environ()
	if clear {
		environment = nil
	}
	for _, name := range unset {
		environment = removeEnvironment(environment, name)
	}
	for len(args) > 0 && strings.Contains(args[0], "=") {
		environment = setEnvironment(environment, args[0])
		args = args[1:]
	}
	if len(args) == 0 {
		for _, value := range environment {
			fmt.Println(value)
		}
		return 0
	}
	command := exec.Command(args[0], args[1:]...) //nolint:gosec // G204: env exists to execute the user-specified command.
	command.Env = environment
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return commandStatus("env", err)
	}
	return 0
}

func cmdWhich(args []string) int {
	all := false
	if len(args) > 0 && args[0] == "-a" {
		all = true
		args = args[1:]
	}
	if len(args) == 0 {
		fatalf("which", "missing command name")
		return 1
	}
	status := 0
	for _, name := range args {
		// which warns about an option it does not know and carries on with the
		// remaining names rather than failing, so the exit status still
		// reflects only whether the commands were found.
		if len(name) > 1 && name[0] == '-' {
			fatalf("which", "unrecognized option '%s'", name)
			continue
		}
		matches := []string{}
		if strings.ContainsRune(name, '/') {
			if executableFile(name) {
				matches = append(matches, name)
			}
		} else {
			for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
				if dir == "" {
					dir = "."
				}
				candidate := filepath.Join(dir, name)
				if executableFile(candidate) {
					matches = append(matches, candidate)
					if !all {
						break
					}
				}
			}
		}
		if len(matches) == 0 {
			status = 1
			continue
		}
		for _, match := range matches {
			fmt.Println(match)
		}
	}
	return status
}
func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}
func removeEnvironment(env []string, name string) []string {
	prefix := name + "="
	out := env[:0]
	for _, v := range env {
		if !strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	return out
}
func setEnvironment(env []string, value string) []string {
	name := strings.SplitN(value, "=", 2)[0]
	env = removeEnvironment(env, name)
	return append(env, value)
}

func cmdXargs(args []string) int {
	null, maxArgs, noRun := false, 0, false
	replace, delim, eofStr, argFile := "", "", "", ""
	haveDelim := false
	verbose, interactive, exitOnOverflow := false, false, false
	maxChars, maxProcs := 0, 1
	maxLines, slotVar := 0, ""
	commandArgs := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			commandArgs = append(commandArgs, args[i+1:]...)
			i = len(args)
		case arg == "-0" || arg == "--null":
			null = true
		case arg == "-r" || arg == "--no-run-if-empty":
			noRun = true
		case arg == "-t" || arg == "--verbose":
			verbose = true
		case arg == "-p" || arg == "--interactive":
			verbose, interactive = true, true
		case arg == "-x" || arg == "--exit":
			exitOnOverflow = true
		case strings.HasPrefix(arg, "-n") || strings.HasPrefix(arg, "--max-args"):
			text, next, ok := optionArgument(args, i, "-n", "--max-args")
			count, convErr := strconv.Atoi(text)
			if !ok || convErr != nil || count < 1 {
				fatalf("xargs", "invalid argument count %q", text)
				return 1
			}
			if maxLines > 0 {
				fatalf("xargs", "warning: options --max-lines and --max-args/-n are mutually exclusive, ignoring previous --max-lines value")
				maxLines = 0
			}
			if replace != "" {
				fatalf("xargs", "warning: options --replace and --max-args/-n are mutually exclusive, ignoring previous --replace value")
				replace = ""
			}
			maxArgs, i = count, next
		case strings.HasPrefix(arg, "-L") || strings.HasPrefix(arg, "--max-lines"):
			text, next, ok := optionArgument(args, i, "-L", "--max-lines")
			count, convErr := strconv.Atoi(text)
			if !ok || convErr != nil || count < 1 {
				fatalf("xargs", "invalid max-lines count %q", text)
				return 1
			}
			if maxArgs > 0 {
				fatalf("xargs", "warning: options --max-args and -L are mutually exclusive, ignoring previous --max-args value")
				maxArgs = 0
			}
			if replace != "" {
				fatalf("xargs", "warning: options --max-lines and --replace/-I/-i are mutually exclusive, ignoring previous --replace value")
				replace = ""
			}
			maxLines, i = count, next
		case strings.HasPrefix(arg, "--process-slot-var"):
			text, next, ok := optionArgument(args, i, "--process-slot-var")
			if !ok || text == "" {
				fatalf("xargs", "option requires an argument -- 'process-slot-var'")
				return 1
			}
			slotVar, i = text, next
		case strings.HasPrefix(arg, "-P") || strings.HasPrefix(arg, "--max-procs"):
			text, next, ok := optionArgument(args, i, "-P", "--max-procs")
			count, convErr := strconv.Atoi(text)
			if !ok || convErr != nil || count < 0 {
				fatalf("xargs", "invalid max-procs count %q", text)
				return 1
			}
			maxProcs, i = count, next
		case strings.HasPrefix(arg, "-s") || strings.HasPrefix(arg, "--max-chars"):
			text, next, ok := optionArgument(args, i, "-s", "--max-chars")
			count, convErr := strconv.Atoi(text)
			if !ok || convErr != nil || count < 1 {
				fatalf("xargs", "invalid max-chars count %q", text)
				return 1
			}
			maxChars, i = count, next
		case strings.HasPrefix(arg, "-I") || strings.HasPrefix(arg, "--replace"):
			text, next, ok := optionArgument(args, i, "-I", "--replace")
			if !ok || text == "" {
				fatalf("xargs", "option requires an argument -- 'I'")
				return 1
			}
			if maxArgs > 0 {
				fatalf("xargs", "warning: options --replace and --max-args/-n are mutually exclusive, ignoring previous --max-args value")
				maxArgs = 0
			}
			if maxLines > 0 {
				fatalf("xargs", "warning: options --replace and --max-lines/-l are mutually exclusive, ignoring previous --max-lines value")
				maxLines = 0
			}
			replace, i = text, next
		case strings.HasPrefix(arg, "-a") || strings.HasPrefix(arg, "--arg-file"):
			text, next, ok := optionArgument(args, i, "-a", "--arg-file")
			if !ok || text == "" {
				fatalf("xargs", "option requires an argument -- 'a'")
				return 1
			}
			argFile, i = text, next
		case strings.HasPrefix(arg, "-d") || strings.HasPrefix(arg, "--delimiter"):
			text, next, ok := optionArgument(args, i, "-d", "--delimiter")
			if !ok {
				fatalf("xargs", "option requires an argument -- 'd'")
				return 1
			}
			decoded := decodeEscapes(text, false)
			if len(decoded) != 1 {
				fatalf("xargs", "the argument to -d must be a single character")
				return 1
			}
			delim, haveDelim, i = decoded, true, next
		case strings.HasPrefix(arg, "-E"):
			text, next, ok := optionArgument(args, i, "-E")
			if !ok {
				fatalf("xargs", "option requires an argument -- 'E'")
				return 1
			}
			eofStr, i = text, next
		case arg == "-e" || arg == "--eof":
			// The EOF string is optional here (unlike -E) and never consumes
			// the next argument: bare -e/--eof defaults to "_".
			eofStr = "_"
		case strings.HasPrefix(arg, "--eof="):
			eofStr = strings.TrimPrefix(arg, "--eof=")
		case strings.HasPrefix(arg, "-e"):
			eofStr = strings.TrimPrefix(arg, "-e")
		default:
			commandArgs = append(commandArgs, args[i:]...)
			i = len(args)
		}
	}

	var data []byte
	var err error
	if argFile != "" {
		data, err = os.ReadFile(argFile)
	} else {
		data, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<30))
	}
	if err != nil {
		fatalf("xargs", "%v", errText(err))
		return 1
	}
	var items []string
	var lineEnds []bool
	switch {
	case haveDelim:
		items = strings.Split(string(data), delim)
		if len(items) > 0 && items[len(items)-1] == "" {
			items = items[:len(items)-1]
		}
	case null:
		items = strings.Split(string(data), "\x00")
		if len(items) > 0 && items[len(items)-1] == "" {
			items = items[:len(items)-1]
		}
	case replace != "":
		// -I substitutes a whole line at a time, so blanks inside a line do not
		// separate items.
		for _, line := range strings.Split(string(data), "\n") {
			if trimmed := strings.Trim(line, " \t"); trimmed != "" {
				items = append(items, trimmed)
			}
		}
	case maxLines > 0:
		// -L groups input into logical lines: a line whose last character is a
		// blank continues on the next physical line, blank lines are skipped,
		// and quoting still applies within a line. lineEnds marks the last item
		// of each logical line so the batching step can split between lines.
		var logical strings.Builder
		for _, line := range strings.Split(string(data), "\n") {
			logical.WriteString(line)
			if strings.HasSuffix(line, " ") || strings.HasSuffix(line, "\t") {
				continue
			}
			if strings.TrimSpace(logical.String()) != "" {
				lineItems, err := splitXargsInput(logical.String())
				if err != nil {
					fatalf("xargs", "%v", err)
					return 1
				}
				for _, item := range lineItems {
					items = append(items, item)
					lineEnds = append(lineEnds, false)
				}
				if len(lineItems) > 0 {
					lineEnds[len(lineEnds)-1] = true
				}
			}
			logical.Reset()
		}
	default:
		items, err = splitXargsInput(string(data))
		if err != nil {
			fatalf("xargs", "%v", err)
			return 1
		}
	}
	if eofStr != "" {
		for idx, item := range items {
			if item == eofStr {
				items = items[:idx]
				break
			}
		}
	}
	if replace != "" {
		// One command per line, which is what -I means.
		maxArgs = 1
	}
	if len(commandArgs) == 0 {
		commandArgs = []string{"echo"}
	}
	if len(items) == 0 && noRun {
		return 0
	}

	batches := xargsBatches(items, maxArgs, maxChars, maxLines, lineEnds, commandArgs)
	if exitOnOverflow && maxChars > 0 {
		prefixLen := xargsLineLen(commandArgs)
		for _, item := range items {
			if prefixLen+len(item)+1 > maxChars {
				fatalf("xargs", "argument line too long")
				return 1
			}
		}
	}

	run := func(batch []string, slot int) int {
		current := append([]string{}, commandArgs...)
		if replace != "" {
			joined := strings.Join(batch, " ")
			for i := range current {
				current[i] = strings.ReplaceAll(current[i], replace, joined)
			}
		} else {
			current = append(current, batch...)
		}
		if verbose {
			fmt.Fprintln(os.Stderr, xargsQuoteLine(current))
		}
		if interactive {
			if !xargsConfirm() {
				return 0
			}
		}
		cmd := exec.Command(current[0], current[1:]...) //nolint:gosec // G204: xargs intentionally executes user-selected commands.
		if slotVar != "" {
			cmd.Env = setEnvironment(os.Environ(), fmt.Sprintf("%s=%d", slotVar, slot))
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return commandStatus("xargs", err)
		}
		return 0
	}

	if maxProcs == 1 || len(batches) <= 1 {
		status := 0
		for _, batch := range batches {
			if s := run(batch, 0); s != 0 {
				status = s
			}
		}
		return status
	}

	limit := maxProcs
	if limit <= 0 {
		limit = len(batches)
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	status := 0
	for slot, batch := range batches {
		batch := batch
		slot := slot % limit
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if s := run(batch, slot); s != 0 {
				mu.Lock()
				status = s
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return status
}

// xargsBatches groups items into command invocations honouring both -n's
// item-count limit and -s's total command-line-length limit (0 means
// unlimited). A single item that alone exceeds maxChars still gets its own
// batch rather than being dropped; -x turns that case into a hard error
// before any command runs. With -L (maxLines > 0), a batch additionally
// breaks between input lines: lineEnds[i] marks the last item of a logical
// line, and a batch never spans more than maxLines of them.
func xargsBatches(items []string, maxArgs, maxChars, maxLines int, lineEnds []bool, prefixArgs []string) [][]string {
	if len(items) == 0 {
		return [][]string{{}}
	}
	if maxArgs <= 0 {
		maxArgs = len(items)
	}
	prefixLen := xargsLineLen(prefixArgs)
	var batches [][]string
	var current []string
	curLen := prefixLen
	linesUsed := 0
	for i, item := range items {
		addLen := len(item) + 1
		lineBreak := maxLines > 0 && i > 0 && lineEnds[i-1] && linesUsed >= maxLines
		if len(current) > 0 && (len(current) >= maxArgs || (maxChars > 0 && curLen+addLen > maxChars) || lineBreak) {
			batches = append(batches, current)
			current, curLen, linesUsed = nil, prefixLen, 0
		}
		current = append(current, item)
		curLen += addLen
		if i < len(lineEnds) && lineEnds[i] {
			linesUsed++
		}
	}
	batches = append(batches, current)
	return batches
}

func xargsLineLen(args []string) int {
	n := 0
	for _, a := range args {
		n += len(a) + 1
	}
	return n
}

// xargsQuoteLine renders a command line the way -t/-p echo it: space-joined,
// with any argument containing whitespace wrapped in single quotes.
func xargsQuoteLine(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\n") {
			parts[i] = "'" + a + "'"
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// xargsConfirm implements -p: read a yes/no answer from the controlling
// terminal (never from stdin, which may be the item source) and run the
// command only if it starts with 'y' or 'Y'.
func xargsConfirm() bool {
	fmt.Fprint(os.Stderr, "?...")
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		fatalf("xargs", "failed to open /dev/tty for reading: %v", errText(err))
		return false
	}
	defer tty.Close()
	scanner := bufio.NewScanner(tty)
	if !scanner.Scan() {
		return false
	}
	answer := strings.TrimSpace(scanner.Text())
	return len(answer) > 0 && (answer[0] == 'y' || answer[0] == 'Y')
}

// optionArgument returns the value belonging to an option, which may be written
// attached to it (-n1), as the next argument (-n 1), or after an equals sign
// (--max-args=1). It also reports the index the caller should continue from.
func optionArgument(args []string, index int, forms ...string) (string, int, bool) {
	arg := args[index]
	for _, form := range forms {
		switch {
		case arg == form:
			if index+1 < len(args) {
				return args[index+1], index + 1, true
			}
			return "", index, false
		case strings.HasPrefix(form, "--"):
			if value, found := strings.CutPrefix(arg, form+"="); found {
				return value, index, true
			}
		default:
			if value, found := strings.CutPrefix(arg, form); found {
				return value, index, true
			}
		}
	}
	return "", index, false
}

// splitXargsInput splits standard input the way xargs does: on blanks and
// newlines alike, honouring quotes and backslash escapes. It is deliberately
// not the shell's splitter, because xargs performs no expansion -- a literal
// $HOME stays a literal $HOME.
func splitXargsInput(data string) ([]string, error) {
	var items []string
	var word strings.Builder
	started := false
	flush := func() {
		if started {
			items = append(items, word.String())
			word.Reset()
			started = false
		}
	}
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '\'' || c == '"':
			started = true
			quote := c
			for i++; i < len(data) && data[i] != quote; i++ {
				word.WriteByte(data[i])
			}
			if i >= len(data) {
				return nil, fmt.Errorf("unterminated %c quote in input; use -0 for data that contains them", quote)
			}
		case c == '\\' && i+1 < len(data):
			i++
			word.WriteByte(data[i])
			started = true
		default:
			word.WriteByte(c)
			started = true
		}
	}
	flush()
	return items, nil
}

func cmdSh(args []string) int {
	interactive := false
	var source, name string
	scriptArgs := []string{}
	if len(args) > 0 && args[0] == "-c" {
		if len(args) < 2 {
			fatalf("sh", "-c requires a command")
			return 2
		}
		source = args[1]
		name = "-c"
		scriptArgs = args[2:]
	} else if len(args) > 0 {
		data, err := os.ReadFile(args[0])
		if err != nil {
			fatalf("sh", "%v", err)
			return 127
		}
		source = string(data)
		name = args[0]
		scriptArgs = args[1:]
	} else {
		if info, e := os.Stdin.Stat(); e == nil && info.Mode()&os.ModeCharDevice != 0 {
			interactive = true
			name = "sh"
		} else {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return 1
			}
			source = string(data)
			name = "sh"
		}
	}
	// Positional parameters are shell variables, not environment entries: a
	// child process must not inherit $0 and $1 from its caller.
	shellVariables = map[string]string{}
	shellStatus = 0
	shellBreaking, shellContinuing = false, false
	for i, value := range append([]string{name}, scriptArgs...) {
		shellVariables[strconv.Itoa(i)] = value
	}
	if interactive {
		return runInteractiveShell()
	}
	return runShellSource(source)
}
func runInteractiveShell() int {
	scanner := bufio.NewScanner(os.Stdin)
	status := 0
	for {
		fmt.Fprint(os.Stderr, "$ ")
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var exit bool
		status, exit = runShellSourceControl(line)
		if exit {
			return status
		}
	}
	return status
}
func runShellSource(source string) int {
	status, _ := runShellSourceControl(source)
	return status
}
func runShellSourceControl(source string) (int, bool) {
	tokens, err := shellTokens(source)
	if err != nil {
		fatalf("sh", "%v", err)
		return 2, false
	}
	statements, pos, err := parseShellList(tokens, 0, shellStopAt())
	if err != nil {
		fatalf("sh", "%v", err)
		return 2, false
	}
	if pos != len(tokens) {
		fatalf("sh", "unexpected token %q", tokens[pos].text)
		return 2, false
	}
	return runShellStatements(statements)
}

// shellStatement is one parsed unit of a shellList: a simple pipeline, or a
// compound if/for/while command. guard records the connector (";"/"\n" for
// unconditional, "&&"/"||" for short-circuit) that preceded it in its list,
// which is what decides whether it runs at all.
type shellStatement struct {
	kind   string // "pipeline", "if", "for", "while"
	guard  string
	tokens []shellToken // kind == "pipeline"

	ifClauses []shellIfClause // kind == "if"
	elseBody  []shellStatement

	forVar  string // kind == "for"
	forList []shellToken
	forBody []shellStatement

	whileCond []shellStatement // kind == "while"
	whileBody []shellStatement
}

type shellIfClause struct {
	cond []shellStatement
	body []shellStatement
}

// shellBreaking and shellContinuing are break/continue's signal to the
// nearest enclosing for/while, mirroring shellStatus as script-wide state:
// runShellStatements stops a list early when either is set (the same way it
// does for a real exit), and the loop that owns the flag clears it.
var shellBreaking, shellContinuing bool

// shellStopAt builds a predicate matching any of the given bare words --
// the keywords (then, fi, done, ...) that end a parsed statement list.
func shellStopAt(words ...string) func(string) bool {
	return func(word string) bool {
		for _, w := range words {
			if word == w {
				return true
			}
		}
		return false
	}
}

func skipShellSeparators(tokens []shellToken, pos int) int {
	for pos < len(tokens) && tokens[pos].operator && (tokens[pos].text == ";" || tokens[pos].text == "\n") {
		pos++
	}
	return pos
}

func shellIsWord(tokens []shellToken, pos int, word string) bool {
	return pos < len(tokens) && !tokens[pos].operator && tokens[pos].text == word
}

// parseShellList parses a ";"/"\n"/"&&"/"||"-connected sequence of
// statements, stopping at end of input or at a bare word stop accepts (a
// keyword belonging to whichever compound command is calling it).
func parseShellList(tokens []shellToken, pos int, stop func(string) bool) ([]shellStatement, int, error) {
	var statements []shellStatement
	guard := ""
	for {
		pos = skipShellSeparators(tokens, pos)
		if pos >= len(tokens) || (!tokens[pos].operator && stop(tokens[pos].text)) {
			return statements, pos, nil
		}
		stmt, next, err := parseShellStatement(tokens, pos)
		if err != nil {
			return nil, 0, err
		}
		stmt.guard = guard
		statements = append(statements, stmt)
		pos = next
		guard = ""
		if pos < len(tokens) && tokens[pos].operator {
			switch tokens[pos].text {
			case ";", "\n":
				pos++
				continue
			case "&&", "||":
				guard = tokens[pos].text
				pos++
				continue
			}
		}
		return statements, pos, nil
	}
}

func parseShellStatement(tokens []shellToken, pos int) (shellStatement, int, error) {
	if pos < len(tokens) && !tokens[pos].operator {
		switch tokens[pos].text {
		case "if":
			return parseShellIf(tokens, pos)
		case "for":
			return parseShellFor(tokens, pos)
		case "while":
			return parseShellWhile(tokens, pos)
		}
	}
	start := pos
	for pos < len(tokens) {
		if tokens[pos].operator {
			switch tokens[pos].text {
			case ";", "\n", "&&", "||":
				return shellStatement{kind: "pipeline", tokens: tokens[start:pos]}, pos, nil
			}
		}
		pos++
	}
	return shellStatement{kind: "pipeline", tokens: tokens[start:pos]}, pos, nil
}

func parseShellIf(tokens []shellToken, pos int) (shellStatement, int, error) {
	stmt := shellStatement{kind: "if"}
	pos++ // consume "if"
	for {
		cond, next, err := parseShellList(tokens, pos, shellStopAt("then"))
		if err != nil {
			return stmt, 0, err
		}
		if !shellIsWord(tokens, next, "then") {
			return stmt, 0, fmt.Errorf("expected 'then'")
		}
		pos = next + 1
		body, next2, err := parseShellList(tokens, pos, shellStopAt("elif", "else", "fi"))
		if err != nil {
			return stmt, 0, err
		}
		stmt.ifClauses = append(stmt.ifClauses, shellIfClause{cond: cond, body: body})
		pos = next2
		if shellIsWord(tokens, pos, "elif") {
			pos++
			continue
		}
		break
	}
	if shellIsWord(tokens, pos, "else") {
		elseBody, next, err := parseShellList(tokens, pos+1, shellStopAt("fi"))
		if err != nil {
			return stmt, 0, err
		}
		stmt.elseBody = elseBody
		pos = next
	}
	if !shellIsWord(tokens, pos, "fi") {
		return stmt, 0, fmt.Errorf("expected 'fi'")
	}
	return stmt, pos + 1, nil
}

func parseShellFor(tokens []shellToken, pos int) (shellStatement, int, error) {
	pos++ // consume "for"
	if pos >= len(tokens) || tokens[pos].operator {
		return shellStatement{}, 0, fmt.Errorf("expected name after 'for'")
	}
	stmt := shellStatement{kind: "for", forVar: tokens[pos].text}
	pos++
	pos = skipShellSeparators(tokens, pos)
	if shellIsWord(tokens, pos, "in") {
		pos++
		for pos < len(tokens) && (!tokens[pos].operator || tokens[pos].text != ";" && tokens[pos].text != "\n") {
			stmt.forList = append(stmt.forList, tokens[pos])
			pos++
		}
		pos = skipShellSeparators(tokens, pos)
	}
	if !shellIsWord(tokens, pos, "do") {
		return stmt, 0, fmt.Errorf("expected 'do'")
	}
	body, next, err := parseShellList(tokens, pos+1, shellStopAt("done"))
	if err != nil {
		return stmt, 0, err
	}
	stmt.forBody = body
	if !shellIsWord(tokens, next, "done") {
		return stmt, 0, fmt.Errorf("expected 'done'")
	}
	return stmt, next + 1, nil
}

func parseShellWhile(tokens []shellToken, pos int) (shellStatement, int, error) {
	pos++ // consume "while"
	cond, next, err := parseShellList(tokens, pos, shellStopAt("do"))
	if err != nil {
		return shellStatement{}, 0, err
	}
	if !shellIsWord(tokens, next, "do") {
		return shellStatement{}, 0, fmt.Errorf("expected 'do'")
	}
	body, next2, err := parseShellList(tokens, next+1, shellStopAt("done"))
	if err != nil {
		return shellStatement{}, 0, err
	}
	if !shellIsWord(tokens, next2, "done") {
		return shellStatement{}, 0, fmt.Errorf("expected 'done'")
	}
	return shellStatement{kind: "while", whileCond: cond, whileBody: body}, next2 + 1, nil
}

// runShellStatements runs a parsed list in order, honouring each
// statement's short-circuit guard and stopping early on exit, break, or
// continue so the signal can reach the construct that owns it.
func runShellStatements(statements []shellStatement) (int, bool) {
	status := 0
	for _, stmt := range statements {
		if stmt.guard == "&&" && status != 0 || stmt.guard == "||" && status == 0 {
			continue
		}
		var exit bool
		status, exit = runShellStatement(stmt)
		shellStatus = status
		if exit || shellBreaking || shellContinuing {
			return status, exit
		}
	}
	return status, false
}

func runShellStatement(stmt shellStatement) (int, bool) {
	switch stmt.kind {
	case "pipeline":
		return runShellPipeline(stmt.tokens)
	case "if":
		for _, clause := range stmt.ifClauses {
			condStatus, exit := runShellStatements(clause.cond)
			if exit {
				return condStatus, true
			}
			if condStatus == 0 {
				return runShellStatements(clause.body)
			}
		}
		if stmt.elseBody != nil {
			return runShellStatements(stmt.elseBody)
		}
		return 0, false
	case "for":
		status := 0
		var values []string
		for _, item := range stmt.forList {
			expanded := expandShellWord(item.text)
			if item.quoted {
				values = append(values, expanded)
			} else {
				// An unquoted word's expansion is field-split, the same as
				// an unquoted command's arguments: "for f in $(cmd)" must
				// see each of cmd's output words as a separate iteration.
				values = append(values, strings.Fields(expanded)...)
			}
		}
		for _, value := range values {
			setShellVariable(stmt.forVar, value)
			var exit bool
			status, exit = runShellStatements(stmt.forBody)
			if exit {
				return status, true
			}
			if shellBreaking {
				shellBreaking = false
				break
			}
			shellContinuing = false
		}
		return status, false
	case "while":
		status := 0
		for {
			condStatus, exit := runShellStatements(stmt.whileCond)
			if exit {
				return condStatus, true
			}
			if condStatus != 0 {
				break
			}
			var bodyExit bool
			status, bodyExit = runShellStatements(stmt.whileBody)
			if bodyExit {
				return status, true
			}
			if shellBreaking {
				shellBreaking = false
				break
			}
			shellContinuing = false
		}
		return status, false
	}
	return 0, false
}

func runShellPipeline(tokens []shellToken) (int, bool) {
	parts := [][]shellToken{{}}
	for _, token := range tokens {
		if token.operator && token.text == "|" {
			parts = append(parts, []shellToken{})
		} else {
			parts[len(parts)-1] = append(parts[len(parts)-1], token)
		}
	}
	// A builtin runs in this process, so its redirections have to be applied
	// here. Passing the raw tokens straight to the builtin would make "echo hi
	// > f" print "hi > f" and create no file.
	if len(parts) == 1 {
		command, err := shellCommand(parts[0])
		if err != nil {
			fatalf("sh", "%v", err)
			return 2, false
		}
		// A command that is nothing but assignments sets shell variables and
		// succeeds: "x=1" is a complete statement, not a program to look up.
		if len(command.argv) == 0 && len(command.assignments) > 0 {
			for _, assignment := range command.assignments {
				setShellVariable(assignment.name, assignment.value)
			}
			return 0, false
		}
		if len(command.argv) > 0 && isShellBuiltin(command.argv[0]) {
			restore, redirectErr := redirectStandardFiles(command.input, command.output, command.appendMode)
			if redirectErr != nil {
				fatalf("sh", "%v", redirectErr)
				return 1, false
			}
			status, _, exit := runShellBuiltin(command.argv)
			restore()
			return status, exit
		}
	}
	commands := make([]*exec.Cmd, 0, len(parts))
	var previous io.ReadCloser
	for index, part := range parts {
		command, err := shellCommand(part)
		if err != nil || len(command.argv) == 0 {
			fatalf("sh", "invalid command")
			return 2, false
		}
		argv, input, output, appendMode := command.argv, command.input, command.output, command.appendMode
		cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // G204: command execution is the shell's explicit purpose.
		cmd.Stderr = os.Stderr
		// Assignments written in front of a command belong to that command
		// alone: "x=1 env" must not leave x set in the shell afterwards.
		if len(command.assignments) > 0 {
			cmd.Env = os.Environ()
			for _, assignment := range command.assignments {
				cmd.Env = append(cmd.Env, assignment.name+"="+assignment.value)
			}
		}
		if previous != nil {
			cmd.Stdin = previous
		} else {
			cmd.Stdin = os.Stdin
		}
		if input != "" {
			file, e := os.Open(input)
			if e != nil {
				fatalf("sh", "%v", e)
				return 1, false
			}
			defer file.Close()
			cmd.Stdin = file
		}
		if index < len(parts)-1 {
			pipe, e := cmd.StdoutPipe()
			if e != nil {
				return 1, false
			}
			previous = pipe
		} else if output != "" {
			flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
			if appendMode {
				flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
			}
			file, e := os.OpenFile(output, flag, 0o666) //nolint:gosec // G302: shell redirection follows the process umask.
			if e != nil {
				return 1, false
			}
			defer file.Close()
			cmd.Stdout = file
		} else {
			cmd.Stdout = os.Stdout
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Start(); err != nil {
			// A shell reports the command that could not be run, not the Go
			// call that failed, and distinguishes "no such command" (127) from
			// "found but not runnable" (126).
			if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
				fatalf("sh", "%s: not found", cmd.Args[0])
				return 127, false
			}
			fatalf("sh", "%s: %s", cmd.Args[0], errText(err))
			return 126, false
		}
	}
	status := 0
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			status = commandStatus("sh", err)
		}
	}
	return status, false
}

// shellAssignmentPair is one NAME=VALUE written in front of a command.
type shellAssignmentPair struct{ name, value string }

// shellCommandParts is a single command: its leading assignments, its expanded
// argument vector, and where its standard input and output go.
type shellCommandParts struct {
	assignments []shellAssignmentPair
	argv        []string
	input       string
	output      string
	appendMode  bool
}

// shellCommand turns one command's tokens into something runnable. This is
// where expansion happens -- as late as possible, so every word sees the
// variables and the exit status as they are at this moment.
func shellCommand(tokens []shellToken) (shellCommandParts, error) {
	var command shellCommandParts
	leading := true
	for i := 0; i < len(tokens); i++ {
		if tokens[i].operator {
			switch tokens[i].text {
			case "<", ">", ">>":
				if i+1 >= len(tokens) {
					return command, errors.New("missing redirection target")
				}
				target := expandShellWord(tokens[i+1].text)
				if tokens[i].text == "<" {
					command.input = target
				} else {
					command.output = target
					command.appendMode = tokens[i].text == ">>"
				}
				i++
				continue
			default:
				return command, fmt.Errorf("unexpected %q", tokens[i].text)
			}
		}
		if leading {
			if name, value, ok := shellAssignment(tokens[i].text); ok {
				command.assignments = append(command.assignments,
					shellAssignmentPair{name: name, value: expandShellWord(value)})
				continue
			}
			leading = false
		}
		command.argv = append(command.argv, expandShellWord(tokens[i].text))
	}
	return command, nil
}

// isShellBuiltin reports whether a command runs inside the shell itself rather
// than as a separate process. The list has to agree with runShellBuiltin.
func isShellBuiltin(name string) bool {
	switch name {
	case "echo", "printf", "read", "cd", "pwd", "export", "unset", "exit", ":", "break", "continue":
		return true
	}
	return false
}

// redirectStandardFiles points os.Stdin and os.Stdout at the redirection
// targets for the duration of a builtin, and returns the function that puts
// them back. The builtins write through those variables, so swapping them is
// this shell's equivalent of the dup2 a real one performs.
func redirectStandardFiles(input, output string, appendMode bool) (func(), error) {
	savedIn, savedOut := os.Stdin, os.Stdout
	var opened []*os.File
	restore := func() {
		os.Stdin, os.Stdout = savedIn, savedOut
		for _, file := range opened {
			_ = file.Close()
		}
	}
	if input != "" {
		file, err := os.Open(input)
		if err != nil {
			restore()
			return nil, err
		}
		opened = append(opened, file)
		os.Stdin = file
	}
	if output != "" {
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendMode {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		file, err := os.OpenFile(output, flag, 0o666) //nolint:gosec // G302: shell redirection follows the process umask.
		if err != nil {
			restore()
			return nil, err
		}
		opened = append(opened, file)
		os.Stdout = file
	}
	return restore, nil
}

func runShellBuiltin(args []string) (int, bool, bool) {
	if len(args) == 0 {
		return 0, true, false
	}
	switch args[0] {
	case "echo":
		return cmdEcho(args[1:]), true, false
	case "printf":
		return cmdPrintf(args[1:]), true, false
	case "read":
		name := "REPLY"
		if len(args) > 1 {
			name = args[1]
		}
		var value strings.Builder
		one := []byte{0}
		for {
			n, err := os.Stdin.Read(one)
			if n == 1 && one[0] != '\n' && one[0] != '\r' {
				value.WriteByte(one[0])
			}
			if n == 1 && (one[0] == '\n' || one[0] == '\r') {
				break
			}
			if err != nil {
				if errors.Is(err, io.EOF) && value.Len() > 0 {
					break
				}
				return 1, true, false
			}
		}
		setShellVariable(name, value.String())
		return 0, true, false
	case "cd":
		dir := ""
		if len(args) > 1 {
			dir = args[1]
		} else {
			dir = os.Getenv("HOME")
		}
		if err := os.Chdir(dir); err != nil {
			fatalf("sh", "cd: %v", err)
			return 1, true, false
		}
		return 0, true, false
	case "pwd":
		dir, e := os.Getwd()
		if e != nil {
			return 1, true, false
		}
		fmt.Println(dir)
		return 0, true, false
	case "export":
		// "export NAME=value" assigns and exports; "export NAME" promotes a
		// variable the script already set, which is why the two are separate.
		for _, v := range args[1:] {
			if name, value, ok := shellAssignment(v); ok {
				delete(shellVariables, name)
				os.Setenv(name, value)
				continue
			}
			if value, ok := shellVariables[v]; ok {
				delete(shellVariables, v)
				os.Setenv(v, value)
			}
		}
		return 0, true, false
	case "unset":
		for _, v := range args[1:] {
			delete(shellVariables, v)
			os.Unsetenv(v)
		}
		return 0, true, false
	case "exit":
		code := 0
		if len(args) > 1 {
			code, _ = strconv.Atoi(args[1])
		}
		return code, true, true
	case ":":
		return 0, true, false
	case "break":
		shellBreaking = true
		return 0, true, false
	case "continue":
		shellContinuing = true
		return 0, true, false
	}
	return 0, false, false
}

// shellToken is one word or one operator of the source. The two are kept apart
// because an operator decides the shape of the script and a word never should:
// a ';' written inside quotes is an argument, not a command separator.
type shellToken struct {
	text     string
	operator bool
	// quoted marks a word that had any part of it inside '...' or "...",
	// which protects the whole word from the field-splitting an unquoted
	// expansion's result undergoes (as in "for f in $(cmd)").
	quoted bool
}

// shellTokens splits source into words and operators without expanding
// anything. Expansion is deliberately left to the moment each command runs --
// doing it here would freeze every variable at the value it had when the script
// was read, so a script could never see a variable it had just set.
func shellTokens(source string) ([]shellToken, error) {
	tokens := []shellToken{}
	var word strings.Builder
	quote := byte(0)
	wordStarted, wordQuoted := false, false
	flush := func() {
		if wordStarted {
			tokens = append(tokens, shellToken{text: word.String(), quoted: wordQuoted})
			word.Reset()
			wordStarted, wordQuoted = false, false
		}
	}
	for i := 0; i < len(source); i++ {
		c := source[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else if quote == '\'' && c == '$' {
				word.WriteString("\x00dollar\x00")
			} else if c == '\\' && quote == '"' && i+1 < len(source) &&
				strings.IndexByte("$`\"\\\n", source[i+1]) >= 0 {
				// Inside double quotes a backslash escapes only these; before
				// anything else it stays literal, so "%s\n" reaches printf with
				// its backslash intact.
				i++
				word.WriteByte(source[i])
			} else if quote == '"' && c == '$' {
				if end, ok := shellSubstitutionSpan(source, i); ok {
					word.WriteString(source[i : end+1])
					i = end
				} else {
					word.WriteByte(c)
				}
			} else {
				word.WriteByte(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			wordStarted, wordQuoted = true, true
		case '$':
			if end, ok := shellSubstitutionSpan(source, i); ok {
				word.WriteString(source[i : end+1])
				i = end
			} else {
				word.WriteByte(c)
			}
			wordStarted = true
		case '\\':
			if i+1 < len(source) {
				i++
				if source[i] == '$' {
					word.WriteString("\x00dollar\x00")
				} else {
					word.WriteByte(source[i])
				}
				wordStarted = true
			}
		case ' ', '\t', '\r':
			flush()
		case '\n':
			flush()
			tokens = append(tokens, shellToken{text: "\n", operator: true})
		case '#':
			if word.Len() == 0 {
				for i < len(source) && source[i] != '\n' {
					i++
				}
				i--
			} else {
				word.WriteByte(c)
			}
		case ';', '|', '&', '<', '>':
			flush()
			op := string(c)
			if i+1 < len(source) && source[i+1] == c && (c == '&' || c == '|' || c == '>') {
				op += string(c)
				i++
			}
			tokens = append(tokens, shellToken{text: op, operator: true})
		default:
			word.WriteByte(c)
			wordStarted = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	return tokens, nil
}

// shellSubstitutionSpan recognizes a "$(...)" command substitution or
// "$((...))" arithmetic expansion starting at source[dollar] (which must be
// '$'), and returns the index of its final ')' so the tokenizer can copy the
// whole span into the current word without splitting it on internal
// whitespace or quotes. It reports ok=false for a bare '$' that isn't
// followed by '(' at all, leaving that case to the caller.
func shellSubstitutionSpan(source string, dollar int) (int, bool) {
	if dollar+1 >= len(source) || source[dollar+1] != '(' {
		return 0, false
	}
	if dollar+2 < len(source) && source[dollar+2] == '(' {
		end, err := shellScanParens(source, dollar+3, 2)
		if err != nil {
			return 0, false
		}
		return end, true
	}
	end, err := shellScanParens(source, dollar+2, 1)
	if err != nil {
		return 0, false
	}
	return end, true
}

// shellScanParens scans forward from start, which is just past initialDepth
// parens already considered open, until nesting returns to zero, honouring
// quotes so an unbalanced paren inside a quoted string doesn't end the scan
// early. It returns the index of the final matching ')'.
func shellScanParens(source string, start, initialDepth int) (int, error) {
	depth := initialDepth
	quote := byte(0)
	for i := start; i < len(source); i++ {
		c := source[i]
		if quote != 0 {
			if c == '\\' && quote == '"' && i+1 < len(source) {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unterminated substitution")
}

// shellVariables holds the variables a script assigns without exporting them.
// A real shell keeps these out of the environment its children inherit, so they
// live here rather than in os.Setenv, and expansion consults them first.
var shellVariables = map[string]string{}

// shellStatus is the exit status of the most recent command, which is what $?
// expands to.
var shellStatus int

// shellVariable resolves a name for expansion: the special parameters first,
// then the script's own variables, then the environment.
func shellVariable(name string) string {
	switch name {
	case "?":
		return strconv.Itoa(shellStatus)
	case "$":
		return strconv.Itoa(os.Getpid())
	}
	if value, ok := shellVariables[name]; ok {
		return value
	}
	return os.Getenv(name)
}

// setShellVariable assigns a variable. A name that is already exported keeps its
// place in the environment so child processes see the new value; anything else
// stays a shell variable, invisible to them.
func setShellVariable(name, value string) {
	if _, exported := os.LookupEnv(name); exported {
		_ = os.Setenv(name, value)
		return
	}
	shellVariables[name] = value
}

// shellAssignment splits a NAME=VALUE word. The decision is made on the
// unexpanded word, as a shell does: a value that happens to contain '=' after
// expansion is still an ordinary argument.
func shellAssignment(word string) (string, string, bool) {
	equals := strings.IndexByte(word, '=')
	if equals <= 0 {
		return "", "", false
	}
	for i := 0; i < equals; i++ {
		c := word[i]
		switch {
		case c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return "", "", false
		}
	}
	return word[:equals], word[equals+1:], true
}

func expandShellWord(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '$' {
			out.WriteByte(value[i])
			continue
		}
		i++
		if i >= len(value) {
			out.WriteByte('$')
			break
		}
		if value[i] == '?' || value[i] == '$' {
			out.WriteString(shellVariable(value[i : i+1]))
			continue
		}
		if value[i] == '(' {
			if end, ok := shellSubstitutionSpan(value, i-1); ok {
				if value[i+1] == '(' {
					body := strings.TrimSuffix(value[i+2:end], ")")
					result, err := shellArithEval(body)
					if err != nil {
						fatalf("sh", "%v", err)
					} else {
						out.WriteString(strconv.FormatInt(result, 10))
					}
				} else {
					captured, status := shellCommandSubstitution(value[i+1 : end])
					shellStatus = status
					out.WriteString(captured)
				}
				i = end
				continue
			}
		}
		name := ""
		if value[i] == '{' {
			end := strings.IndexByte(value[i+1:], '}')
			if end < 0 {
				out.WriteString("${")
				continue
			}
			name = value[i+1 : i+1+end]
			i += end + 1
		} else {
			start := i
			for i < len(value) && (value[i] == '_' || value[i] >= 'A' && value[i] <= 'Z' || value[i] >= 'a' && value[i] <= 'z' || value[i] >= '0' && value[i] <= '9') {
				i++
			}
			name = value[start:i]
			i--
		}
		out.WriteString(shellVariable(name))
	}
	return strings.ReplaceAll(out.String(), "\x00dollar\x00", "$")
}

// shellCommandSubstitution runs source through the same interpreter as the
// top-level script, capturing what it writes to stdout (trailing newlines
// stripped, as $(...) always does) instead of letting it reach the real
// terminal. Variable assignments made inside still land in the single
// shared shellVariables map, unlike a real subshell's isolated copy -- an
// accepted simplification, since the common uses of $(...) (command output,
// not stateful scripts) never depend on that isolation.
func shellCommandSubstitution(source string) (string, int) {
	read, write, err := os.Pipe()
	if err != nil {
		return "", 1
	}
	old := os.Stdout
	os.Stdout = write
	captured := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(read)
		captured <- data
	}()
	status, _ := runShellSourceControl(source)
	os.Stdout = old
	write.Close()
	data := <-captured
	read.Close()
	return strings.TrimRight(string(data), "\n"), status
}

// shellArithToken and shellArithParser implement $((...)) arithmetic
// expansion: signed 64-bit integers, +-*/%, unary +/-, parens, and bare
// names read as shell variables (so both "i+1" and "$i+1" work, matching
// real shells' arithmetic context). Comparison and assignment operators are
// not supported -- a documented gap, since i=$((i+1)) already covers the
// common loop-counter idiom without them.
type shellArithToken struct{ kind, text string }

func shellArithTokenize(expression string) ([]shellArithToken, error) {
	var tokens []shellArithToken
	for i := 0; i < len(expression); i++ {
		c := expression[i]
		switch {
		case c == ' ' || c == '\t':
		case c == '$':
			// "$i" and "i" are equivalent inside arithmetic; the $ is just skipped.
		case c >= '0' && c <= '9':
			start := i
			for i < len(expression) && expression[i] >= '0' && expression[i] <= '9' {
				i++
			}
			tokens = append(tokens, shellArithToken{"number", expression[start:i]})
			i--
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			start := i
			for i < len(expression) && (expression[i] == '_' || expression[i] >= 'a' && expression[i] <= 'z' ||
				expression[i] >= 'A' && expression[i] <= 'Z' || expression[i] >= '0' && expression[i] <= '9') {
				i++
			}
			tokens = append(tokens, shellArithToken{"name", expression[start:i]})
			i--
		case strings.ContainsRune("+-*/%()", rune(c)):
			tokens = append(tokens, shellArithToken{"op", string(c)})
		default:
			return nil, fmt.Errorf("invalid character %q in arithmetic expression", c)
		}
	}
	return tokens, nil
}

type shellArithParser struct {
	tokens []shellArithToken
	pos    int
}

func shellArithEval(expression string) (int64, error) {
	tokens, err := shellArithTokenize(expression)
	if err != nil {
		return 0, err
	}
	p := &shellArithParser{tokens: tokens}
	value, err := p.parseAdd()
	if err != nil {
		return 0, err
	}
	if p.pos != len(p.tokens) {
		return 0, fmt.Errorf("unexpected token %q in arithmetic expression", p.tokens[p.pos].text)
	}
	return value, nil
}

func (p *shellArithParser) parseAdd() (int64, error) {
	left, err := p.parseMultiply()
	if err != nil {
		return 0, err
	}
	for p.opIs("+") || p.opIs("-") {
		op := p.tokens[p.pos].text
		p.pos++
		right, err := p.parseMultiply()
		if err != nil {
			return 0, err
		}
		if op == "+" {
			left += right
		} else {
			left -= right
		}
	}
	return left, nil
}

func (p *shellArithParser) parseMultiply() (int64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for p.opIs("*") || p.opIs("/") || p.opIs("%") {
		op := p.tokens[p.pos].text
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		switch op {
		case "*":
			left *= right
		case "/", "%":
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			if op == "/" {
				left /= right
			} else {
				left %= right
			}
		}
	}
	return left, nil
}

func (p *shellArithParser) parseUnary() (int64, error) {
	if p.opIs("-") {
		p.pos++
		value, err := p.parseUnary()
		return -value, err
	}
	if p.opIs("+") {
		p.pos++
		return p.parseUnary()
	}
	return p.parsePrimary()
}

func (p *shellArithParser) parsePrimary() (int64, error) {
	if p.opIs("(") {
		p.pos++
		value, err := p.parseAdd()
		if err != nil {
			return 0, err
		}
		if !p.opIs(")") {
			return 0, fmt.Errorf("missing ) in arithmetic expression")
		}
		p.pos++
		return value, nil
	}
	if p.pos >= len(p.tokens) {
		return 0, fmt.Errorf("expected an arithmetic expression")
	}
	token := p.tokens[p.pos]
	p.pos++
	switch token.kind {
	case "number":
		return strconv.ParseInt(token.text, 10, 64)
	case "name":
		value := strings.TrimSpace(shellVariable(token.text))
		if value == "" {
			return 0, nil
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, nil // a non-numeric variable reads as 0, as real shells do
		}
		return n, nil
	}
	return 0, fmt.Errorf("unexpected token %q in arithmetic expression", token.text)
}

func (p *shellArithParser) opIs(text string) bool {
	return p.pos < len(p.tokens) && p.tokens[p.pos].kind == "op" && p.tokens[p.pos].text == text
}

func commandStatus(prog string, err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fatalf(prog, "%v", err)
	return 127
}
