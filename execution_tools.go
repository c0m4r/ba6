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
	replace := ""
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
		case strings.HasPrefix(arg, "-n") || strings.HasPrefix(arg, "--max-args"):
			text, next, ok := optionArgument(args, i, "-n", "--max-args")
			count, convErr := strconv.Atoi(text)
			if !ok || convErr != nil || count < 1 {
				fatalf("xargs", "invalid argument count %q", text)
				return 1
			}
			maxArgs, i = count, next
		case strings.HasPrefix(arg, "-I") || strings.HasPrefix(arg, "--replace"):
			text, next, ok := optionArgument(args, i, "-I", "--replace")
			if !ok || text == "" {
				fatalf("xargs", "option requires an argument -- 'I'")
				return 1
			}
			replace, i = text, next
		default:
			commandArgs = append(commandArgs, args[i:]...)
			i = len(args)
		}
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<30))
	if err != nil {
		return 1
	}
	var items []string
	switch {
	case null:
		for _, v := range strings.Split(string(data), "\x00") {
			if v != "" {
				items = append(items, v)
			}
		}
	case replace != "":
		// -I substitutes a whole line at a time, so blanks inside a line do not
		// separate items.
		for _, line := range strings.Split(string(data), "\n") {
			if trimmed := strings.Trim(line, " \t"); trimmed != "" {
				items = append(items, trimmed)
			}
		}
	default:
		items, err = splitXargsInput(string(data))
		if err != nil {
			fatalf("xargs", "%v", err)
			return 1
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
	if maxArgs <= 0 {
		maxArgs = len(items)
		if maxArgs == 0 {
			maxArgs = 1
		}
	}
	status := 0
	for start := 0; start < len(items) || start == 0 && len(items) == 0; start += maxArgs {
		end := start + maxArgs
		if end > len(items) {
			end = len(items)
		}
		current := append([]string{}, commandArgs...)
		if replace != "" {
			joined := strings.Join(items[start:end], " ")
			for i := range current {
				current[i] = strings.ReplaceAll(current[i], replace, joined)
			}
		} else {
			current = append(current, items[start:end]...)
		}
		cmd := exec.Command(current[0], current[1:]...) //nolint:gosec // G204: xargs intentionally executes user-selected commands.
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			status = commandStatus("xargs", err)
		}
		if len(items) == 0 {
			break
		}
	}
	return status
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
	status := 0
	exit := false
	segment := []shellToken{}
	connector := ";"
	run := func() {
		if len(segment) == 0 {
			return
		}
		if connector == "&&" && status != 0 || connector == "||" && status == 0 {
			return
		}
		status, exit = runShellPipeline(segment)
		shellStatus = status
	}
	for _, token := range tokens {
		if token.operator && (token.text == ";" || token.text == "\n" ||
			token.text == "&&" || token.text == "||") {
			run()
			if exit {
				return status, true
			}
			segment = nil
			connector = token.text
		} else {
			segment = append(segment, token)
		}
	}
	run()
	return status, exit
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
	case "echo", "printf", "read", "cd", "pwd", "export", "unset", "exit", ":":
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
	}
	return 0, false, false
}

// shellToken is one word or one operator of the source. The two are kept apart
// because an operator decides the shape of the script and a word never should:
// a ';' written inside quotes is an argument, not a command separator.
type shellToken struct {
	text     string
	operator bool
}

// shellTokens splits source into words and operators without expanding
// anything. Expansion is deliberately left to the moment each command runs --
// doing it here would freeze every variable at the value it had when the script
// was read, so a script could never see a variable it had just set.
func shellTokens(source string) ([]shellToken, error) {
	tokens := []shellToken{}
	var word strings.Builder
	quote := byte(0)
	wordStarted := false
	flush := func() {
		if wordStarted {
			tokens = append(tokens, shellToken{text: word.String()})
			word.Reset()
			wordStarted = false
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
			} else {
				word.WriteByte(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
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
func commandStatus(prog string, err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fatalf(prog, "%v", err)
	return 127
}
