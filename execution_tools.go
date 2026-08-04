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
		default:
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
		switch args[i] {
		case "-0", "--null":
			null = true
		case "-r", "--no-run-if-empty":
			noRun = true
		case "-n", "--max-args":
			i++
			if i >= len(args) {
				return 1
			}
			maxArgs, _ = strconv.Atoi(args[i])
		case "-I":
			i++
			if i >= len(args) {
				return 1
			}
			replace = args[i]
		case "--":
			commandArgs = append(commandArgs, args[i+1:]...)
			i = len(args)
		default:
			commandArgs = append(commandArgs, args[i:]...)
			i = len(args)
		}
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<30))
	if err != nil {
		return 1
	}
	items := []string{}
	if null {
		for _, v := range strings.Split(string(data), "\x00") {
			if v != "" {
				items = append(items, v)
			}
		}
	} else {
		items, err = shellWords(string(data))
		if err != nil {
			fatalf("xargs", "%v", err)
			return 1
		}
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
	for i, value := range append([]string{name}, scriptArgs...) {
		os.Setenv(strconv.Itoa(i), value)
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
	segment := []string{}
	connector := ";"
	run := func() {
		if len(segment) == 0 {
			return
		}
		if connector == "&&" && status != 0 || connector == "||" && status == 0 {
			return
		}
		status, exit = runShellPipeline(segment)
	}
	for _, token := range tokens {
		if token == ";" || token == "\n" || token == "&&" || token == "||" {
			run()
			if exit {
				return status, true
			}
			segment = nil
			connector = token
		} else {
			segment = append(segment, token)
		}
	}
	run()
	return status, exit
}

func runShellPipeline(tokens []string) (int, bool) {
	parts := [][]string{{}}
	for _, token := range tokens {
		if token == "|" {
			parts = append(parts, []string{})
		} else {
			parts[len(parts)-1] = append(parts[len(parts)-1], token)
		}
	}
	if len(parts) == 1 {
		if status, ok, exit := runShellBuiltin(parts[0]); ok {
			return status, exit
		}
	}
	commands := make([]*exec.Cmd, 0, len(parts))
	var previous io.ReadCloser
	for index, part := range parts {
		argv, input, output, appendMode, err := shellRedirections(part)
		if err != nil || len(argv) == 0 {
			fatalf("sh", "invalid command")
			return 2, false
		}
		cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // G204: command execution is the shell's explicit purpose.
		cmd.Stderr = os.Stderr
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
			fatalf("sh", "%v", err)
			return 127, false
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
func shellRedirections(tokens []string) ([]string, string, string, bool, error) {
	argv := []string{}
	input, output := "", ""
	appendMode := false
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "<", ">", ">>":
			if i+1 >= len(tokens) {
				return nil, "", "", false, errors.New("missing redirection target")
			}
			i++
			if tokens[i-1] == "<" {
				input = tokens[i]
			} else {
				output = tokens[i]
				appendMode = tokens[i-1] == ">>"
			}
		default:
			argv = append(argv, tokens[i])
		}
	}
	return argv, input, output, appendMode, nil
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
		_ = os.Setenv(name, value.String())
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
		for _, v := range args[1:] {
			p := strings.SplitN(v, "=", 2)
			if len(p) == 2 {
				os.Setenv(p[0], p[1])
			}
		}
		return 0, true, false
	case "unset":
		for _, v := range args[1:] {
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

func shellWords(source string) ([]string, error) {
	tokens, err := shellTokens(source)
	if err != nil {
		return nil, err
	}
	for _, t := range tokens {
		if len(t) == 1 && strings.ContainsRune(";|<>", rune(t[0])) {
			return nil, fmt.Errorf("operators are not valid in input")
		}
	}
	return tokens, nil
}
func shellTokens(source string) ([]string, error) {
	tokens := []string{}
	var word strings.Builder
	quote := byte(0)
	wordStarted := false
	flush := func() {
		if wordStarted {
			tokens = append(tokens, expandShellWord(word.String()))
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
			} else if c == '\\' && quote == '"' && i+1 < len(source) {
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
			tokens = append(tokens, "\n")
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
			tokens = append(tokens, op)
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
		out.WriteString(os.Getenv(name))
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
