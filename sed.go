// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type sedAddress struct {
	line  int
	last  bool
	regex *regexp.Regexp
}

func (a *sedAddress) matches(text string, line int, last bool) bool {
	if a == nil {
		return true
	}
	if a.line > 0 {
		return line == a.line
	}
	if a.last {
		return last
	}
	return a.regex != nil && a.regex.MatchString(text)
}

type sedCommand struct {
	first, second *sedAddress
	inRange       bool
	negated       bool
	kind          byte
	regex         *regexp.Regexp
	replacement   string
	global        bool
	printOnChange bool
}

// selected reports whether the command applies to this line, honouring an
// address negation such as "/TYPE := /,/}/!d".
func (c *sedCommand) selected(text string, line int, last bool) bool {
	return c.inAddress(text, line, last) != c.negated
}

func (c *sedCommand) inAddress(text string, line int, last bool) bool {
	if c.second == nil {
		return c.first.matches(text, line, last)
	}
	started := false
	if !c.inRange {
		if !c.first.matches(text, line, last) {
			return false
		}
		c.inRange = true
		started = true
	}
	ends := false
	switch {
	case c.second.line > 0:
		// A numeric second address at or before the first address makes a
		// one-line range; otherwise it ends when that line is reached.
		ends = line >= c.second.line
	case c.second.regex != nil:
		// sed starts testing a regular-expression second address on the line
		// after the first address matched.
		ends = !started && c.second.matches(text, line, last)
	default:
		ends = c.second.matches(text, line, last)
	}
	if ends {
		c.inRange = false
	}
	return true
}

type sedLine struct {
	text       string
	terminated bool
}

func cmdSed(args []string) int {
	noDefault := false
	extended := false
	var scripts, files []string
	parsingOptions := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if parsingOptions && arg == "--" {
			parsingOptions = false
			continue
		}
		if parsingOptions && (arg == "-n" || arg == "--quiet" || arg == "--silent") {
			noDefault = true
			continue
		}
		if parsingOptions && (arg == "-E" || arg == "-r" || arg == "--regexp-extended") {
			extended = true
			continue
		}
		if parsingOptions && (arg == "-e" || arg == "--expression") {
			i++
			if i >= len(args) {
				fatalf("sed", "option requires an argument -- 'e'")
				return 1
			}
			scripts = append(scripts, args[i])
			continue
		}
		if parsingOptions && strings.HasPrefix(arg, "-e") && len(arg) > 2 {
			scripts = append(scripts, arg[2:])
			continue
		}
		if parsingOptions && (arg == "-f" || arg == "--file") {
			i++
			if i >= len(args) {
				fatalf("sed", "option requires an argument -- 'f'")
				return 1
			}
			contents, err := os.ReadFile(args[i])
			if err != nil {
				fatalf("sed", "%s: %v", args[i], err)
				return 1
			}
			scripts = append(scripts, string(contents))
			continue
		}
		if parsingOptions && strings.HasPrefix(arg, "-f") && len(arg) > 2 {
			contents, err := os.ReadFile(arg[2:])
			if err != nil {
				fatalf("sed", "%s: %v", arg[2:], err)
				return 1
			}
			scripts = append(scripts, string(contents))
			continue
		}
		if parsingOptions && len(arg) > 1 && arg[0] == '-' {
			fatalf("sed", "invalid option %q", arg)
			return 1
		}
		if len(scripts) == 0 {
			scripts = append(scripts, arg)
			parsingOptions = false
		} else {
			files = append(files, arg)
		}
	}
	if len(scripts) == 0 {
		fatalf("sed", "missing script")
		return 1
	}
	var commands []sedCommand
	for _, script := range scripts {
		parsed, err := parseSedScript(script, extended)
		if err != nil {
			fatalf("sed", "%v", err)
			return 1
		}
		commands = append(commands, parsed...)
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	stream := &sedStream{files: files}
	defer stream.Close()
	inputLine, ok, err := stream.next()
	if err != nil {
		fatalf("sed", "%v", err)
		return 1
	}
	out := bufio.NewWriter(os.Stdout)
	quit := false
	lineNumber := 0
	needsLast := sedNeedsLastAddress(commands)
	for ok {
		var nextLine sedLine
		hasNext := false
		if needsLast {
			nextLine, hasNext, err = stream.next()
			if err != nil {
				fatalf("sed", "%v", err)
				return 1
			}
		}
		lineNumber++
		line := inputLine.text
		deleted := false
		for i := range commands {
			command := &commands[i]
			if !command.selected(line, lineNumber, needsLast && !hasNext) {
				continue
			}
			switch command.kind {
			case 's':
				changed := command.regex.MatchString(line)
				if changed {
					if command.global {
						line = command.regex.ReplaceAllString(line, command.replacement)
					} else {
						indices := command.regex.FindStringSubmatchIndex(line)
						replaced := command.regex.ExpandString(nil, command.replacement, line, indices)
						line = line[:indices[0]] + string(replaced) + line[indices[1]:]
					}
					if command.printOnChange {
						if err := writeSedLine(out, line, inputLine.terminated); err != nil {
							fatalf("sed", "write error: %v", err)
							return 1
						}
					}
				}
			case 'd':
				deleted = true
			case 'p':
				if err := writeSedLine(out, line, inputLine.terminated); err != nil {
					fatalf("sed", "write error: %v", err)
					return 1
				}
			case '=':
				fmt.Fprintln(out, lineNumber)
			case 'q':
				quit = true
			}
			if deleted || quit {
				break
			}
		}
		if !deleted && !noDefault {
			if err := writeSedLine(out, line, inputLine.terminated); err != nil {
				fatalf("sed", "write error: %v", err)
				return 1
			}
		}
		if quit {
			break
		}
		if needsLast {
			inputLine, ok = nextLine, hasNext
		} else {
			inputLine, ok, err = stream.next()
			if err != nil {
				fatalf("sed", "%v", err)
				return 1
			}
		}
	}
	if err := stream.Close(); err != nil {
		fatalf("sed", "%v", err)
		return 1
	}
	if err := out.Flush(); err != nil {
		fatalf("sed", "write error: %v", err)
		return 1
	}
	return 0
}

func sedNeedsLastAddress(commands []sedCommand) bool {
	for i := range commands {
		if commands[i].first != nil && commands[i].first.last || commands[i].second != nil && commands[i].second.last {
			return true
		}
	}
	return false
}

func writeSedLine(out *bufio.Writer, line string, terminated bool) error {
	if _, err := out.WriteString(line); err != nil {
		return err
	}
	if terminated {
		return out.WriteByte('\n')
	}
	return nil
}

type sedStream struct {
	files     []string
	fileIndex int
	name      string
	input     io.ReadCloser
	reader    *bufio.Reader
}

func (s *sedStream) next() (sedLine, bool, error) {
	for {
		if s.input == nil {
			if s.fileIndex >= len(s.files) {
				return sedLine{}, false, nil
			}
			s.name = s.files[s.fileIndex]
			s.fileIndex++
			input, err := openInput(s.name)
			if err != nil {
				return sedLine{}, false, fmt.Errorf("%s: %w", s.name, err)
			}
			s.input = input
			s.reader = bufio.NewReaderSize(input, 64*1024)
		}
		line, readErr := s.reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			_ = s.Close()
			return sedLine{}, false, fmt.Errorf("%s: %w", s.name, readErr)
		}
		if readErr == io.EOF {
			if err := s.Close(); err != nil {
				return sedLine{}, false, fmt.Errorf("%s: %w", s.name, err)
			}
		}
		if len(line) > 0 {
			terminated := strings.HasSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\n")
			return sedLine{text: line, terminated: terminated}, true, nil
		}
	}
}

func (s *sedStream) Close() error {
	if s.input == nil {
		return nil
	}
	err := s.input.Close()
	s.input = nil
	s.reader = nil
	return err
}

func parseSedScript(script string, extended bool) ([]sedCommand, error) {
	var commands []sedCommand
	position := 0
	for {
		for position < len(script) && (script[position] == ';' || script[position] == '\n' || script[position] == ' ' || script[position] == '\t') {
			position++
		}
		if position >= len(script) {
			break
		}
		if script[position] == '#' {
			for position < len(script) && script[position] != '\n' {
				position++
			}
			continue
		}
		var command sedCommand
		first, next, present, err := parseSedAddress(script, position, extended)
		if err != nil {
			return nil, err
		}
		if present {
			command.first, position = first, next
			for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
				position++
			}
			if position < len(script) && script[position] == ',' {
				position++
				second, after, secondPresent, addressErr := parseSedAddress(script, position, extended)
				if addressErr != nil || !secondPresent {
					return nil, fmt.Errorf("invalid second address")
				}
				command.second, position = second, after
			}
		}
		for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
			position++
		}
		if position < len(script) && script[position] == '!' {
			command.negated, position = true, position+1
			for position < len(script) && (script[position] == ' ' || script[position] == '\t') {
				position++
			}
			if position < len(script) && script[position] == '!' {
				return nil, fmt.Errorf("multiple '!'s")
			}
		}
		if position >= len(script) {
			return nil, fmt.Errorf("missing command")
		}
		command.kind = script[position]
		position++
		switch command.kind {
		case 'd', 'p', 'q', '=':
		case 's':
			if position >= len(script) || script[position] == '\n' {
				return nil, fmt.Errorf("missing substitution delimiter")
			}
			delimiter := script[position]
			position++
			pattern, after, err := readSedDelimited(script, position, delimiter)
			if err != nil {
				return nil, err
			}
			position = after
			replacement, after, err := readSedDelimited(script, position, delimiter)
			if err != nil {
				return nil, err
			}
			position = after
			ignoreCase := false
			for position < len(script) && script[position] != ';' && script[position] != '\n' {
				switch script[position] {
				case 'g':
					command.global = true
				case 'p':
					command.printOnChange = true
				case 'I', 'i':
					ignoreCase = true
				case ' ', '\t':
				default:
					return nil, fmt.Errorf("unsupported substitution flag %q", script[position])
				}
				position++
			}
			syntax := posixBRE
			if extended {
				syntax = posixERE
			}
			compiled, compileErr := compilePOSIXRegexp(pattern, syntax, ignoreCase)
			if compileErr != nil {
				return nil, fmt.Errorf("invalid regular expression: %w", compileErr)
			}
			command.regex = compiled
			command.replacement = sedReplacement(replacement)
		default:
			return nil, fmt.Errorf("unsupported command %q", command.kind)
		}
		commands = append(commands, command)
		if position < len(script) && script[position] != ';' && script[position] != '\n' {
			return nil, fmt.Errorf("extra characters after command %q", command.kind)
		}
	}
	return commands, nil
}

func parseSedAddress(script string, position int, extended bool) (*sedAddress, int, bool, error) {
	if position >= len(script) {
		return nil, position, false, nil
	}
	if script[position] >= '0' && script[position] <= '9' {
		start := position
		for position < len(script) && script[position] >= '0' && script[position] <= '9' {
			position++
		}
		line, err := strconv.Atoi(script[start:position])
		if err != nil || line < 1 {
			return nil, position, false, fmt.Errorf("invalid line address")
		}
		return &sedAddress{line: line}, position, true, nil
	}
	if script[position] == '$' {
		return &sedAddress{last: true}, position + 1, true, nil
	}
	if script[position] == '/' {
		pattern, after, err := readSedDelimited(script, position+1, '/')
		if err != nil {
			return nil, position, false, err
		}
		syntax := posixBRE
		if extended {
			syntax = posixERE
		}
		compiled, err := compilePOSIXRegexp(pattern, syntax, false)
		if err != nil {
			return nil, position, false, fmt.Errorf("invalid address regular expression: %w", err)
		}
		return &sedAddress{regex: compiled}, after, true, nil
	}
	return nil, position, false, nil
}

func readSedDelimited(script string, position int, delimiter byte) (string, int, error) {
	var value strings.Builder
	for position < len(script) {
		current := script[position]
		position++
		if current == delimiter {
			return value.String(), position, nil
		}
		if current == '\\' && position < len(script) {
			next := script[position]
			if next == delimiter {
				value.WriteByte(next)
				position++
				continue
			}
			value.WriteByte(current)
			continue
		}
		value.WriteByte(current)
	}
	return "", position, fmt.Errorf("unterminated expression")
}

func sedReplacement(value string) string {
	var result strings.Builder
	for i := 0; i < len(value); i++ {
		switch {
		case value[i] == '&':
			result.WriteString("${0}")
		case value[i] == '$':
			result.WriteString("$$")
		case value[i] == '\\' && i+1 < len(value) && value[i+1] >= '0' && value[i+1] <= '9':
			i++
			result.WriteString("${")
			result.WriteByte(value[i])
			result.WriteByte('}')
		case value[i] == '\\' && i+1 < len(value) && value[i+1] == '&':
			i++
			result.WriteByte('&')
		default:
			result.WriteByte(value[i])
		}
	}
	return result.String()
}
