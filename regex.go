// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"regexp"
	"strings"
)

// posixRegexpSyntax identifies the syntax a command accepts from its user.
// RE2 implements an ERE-like language, but its extra operators would change
// POSIX behavior if user input were handed to it directly.
type posixRegexpSyntax uint8

const (
	posixBRE posixRegexpSyntax = iota
	posixERE
)

// compilePOSIXRegexp compiles a user-supplied POSIX BRE or ERE. The expression
// is first translated to an ERE accepted by RE2, then checked with
// regexp.CompilePOSIX so RE2-only syntax cannot leak into the command
// interface. Longest restores POSIX's leftmost-longest choice after the final
// RE2 compilation (which is needed to add case folding when requested).
func compilePOSIXRegexp(pattern string, syntax posixRegexpSyntax, ignoreCase bool) (*regexp.Regexp, error) {
	expression, err := translatePOSIXRegexp(pattern, syntax)
	if err != nil {
		return nil, err
	}
	return compilePOSIXERE(expression, ignoreCase)
}

// compilePOSIXERE compiles an already-translated POSIX ERE. It is also used
// for expressions assembled by an applet around independently translated
// user patterns.
// posixValidationForm hides the word-boundary escapes the translators emit
// from the POSIX syntax check, which knows only POSIX operators. RE2's own
// parser accepts them in the compile that follows.
func posixValidationForm(expression string) string {
	var b strings.Builder
	b.Grow(len(expression))
	for i := 0; i < len(expression); i++ {
		if expression[i] == '\\' && i+1 < len(expression) {
			if expression[i+1] == 'b' || expression[i+1] == 'B' {
				b.WriteByte('x')
			} else {
				b.WriteByte(expression[i])
				b.WriteByte(expression[i+1])
			}
			i++
			continue
		}
		b.WriteByte(expression[i])
	}
	return b.String()
}

func compilePOSIXERE(expression string, ignoreCase bool) (*regexp.Regexp, error) {
	if _, err := regexp.CompilePOSIX(posixValidationForm(expression)); err != nil {
		return nil, err
	}
	if ignoreCase {
		expression = "(?i:" + expression + ")"
	}
	re, err := regexp.Compile(expression)
	if err != nil {
		return nil, err
	}
	re.Longest()
	return re, nil
}

func translatePOSIXRegexp(pattern string, syntax posixRegexpSyntax) (string, error) {
	switch syntax {
	case posixBRE:
		return translatePOSIXBRE(pattern)
	case posixERE:
		return translatePOSIXERE(pattern)
	default:
		return "", fmt.Errorf("unknown regular-expression syntax")
	}
}

// translatePOSIXBRE changes the BRE operators that RE2 does not recognize:
// \( and \) become grouping and \{m,n\} becomes an interval. Conversely,
// unescaped |, +, ?, (, ), {, and } are quoted because they are literal in a
// BRE. GNU's \|, \+, and \? extensions remain available.
func translatePOSIXBRE(pattern string) (string, error) {
	var result strings.Builder
	result.Grow(len(pattern))
	atStart := true
	for i := 0; i < len(pattern); {
		character := pattern[i]
		if character == '[' {
			end, err := copyPOSIXBracketExpression(&result, pattern, i)
			if err != nil {
				return "", err
			}
			i = end
			atStart = false
			continue
		}
		if character == '\\' {
			if i+1 >= len(pattern) {
				return "", fmt.Errorf("trailing backslash in BRE")
			}
			next := pattern[i+1]
			if next >= '1' && next <= '9' {
				return "", unsupportedRegexpBackreference()
			}
			switch next {
			case '(':
				result.WriteByte('(')
				atStart = true
				i += 2
				continue
			case ')':
				result.WriteByte(')')
				atStart = false
				i += 2
				continue
			case '{':
				interval, end, err := parseBREInterval(pattern, i)
				if err != nil {
					return "", err
				}
				result.WriteByte('{')
				result.WriteString(interval)
				result.WriteByte('}')
				i = end
				atStart = false
				continue
			case '|', '+', '?':
				// These are GNU BRE extensions. They are common in shell
				// scripts and have unambiguous ERE equivalents.
				result.WriteByte(next)
				atStart = next == '|'
				i += 2
				continue
			case 'n', 't', 'r', 'f', 'v', 'a':
				// GNU grep/sed's own extension: these match the control
				// character, not the literal letter. RE2 gives the same
				// escape the same meaning, so it passes through unchanged.
				result.WriteByte('\\')
				result.WriteByte(next)
				atStart = false
				i += 2
				continue
			default:
				if expansion, ok := posixWordEscape(next); ok {
					result.WriteString(expansion)
					atStart = false
					i += 2
					continue
				}
				// A backslash quotes the following BRE character. Quote it
				// again for RE2 rather than admitting RE2-only escapes such
				// as \d.
				result.WriteString(regexp.QuoteMeta(string(next)))
				atStart = false
				i += 2
				continue
			}
		}

		switch character {
		case '^':
			if atStart {
				result.WriteByte(character)
			} else {
				result.WriteString("\\^")
			}
		case '$':
			if breAnchorAtEnd(pattern, i) {
				result.WriteByte(character)
			} else {
				result.WriteString("\\$")
			}
		case '|', '+', '?', '(', ')', '{', '}':
			result.WriteByte('\\')
			result.WriteByte(character)
		default:
			result.WriteByte(character)
		}
		atStart = false
		i++
	}
	return result.String(), nil
}

// translatePOSIXERE prevents RE2-specific syntax from changing the ERE
// interface. regexp.CompilePOSIX performs the final grammar check; this pass
// makes anchors that are ordinary in their current position literal and turns
// an otherwise cryptic RE2 error for \1 into an actionable one.
func translatePOSIXERE(pattern string) (string, error) {
	var result strings.Builder
	result.Grow(len(pattern))
	atStart := true
	for i := 0; i < len(pattern); {
		character := pattern[i]
		if character == '[' {
			end, err := copyPOSIXBracketExpression(&result, pattern, i)
			if err != nil {
				return "", err
			}
			i = end
			atStart = false
			continue
		}
		if character == '\\' {
			if i+1 < len(pattern) && pattern[i+1] >= '1' && pattern[i+1] <= '9' {
				return "", unsupportedRegexpBackreference()
			}
			if i+1 >= len(pattern) {
				return "", fmt.Errorf("trailing backslash in ERE")
			}
			if expansion, ok := posixWordEscape(pattern[i+1]); ok {
				result.WriteString(expansion)
			} else {
				result.WriteByte(character)
				result.WriteByte(pattern[i+1])
			}
			atStart = false
			i += 2
			continue
		}
		switch character {
		case '^':
			if atStart {
				result.WriteByte(character)
			} else {
				result.WriteString("\\^")
			}
		case '$':
			if ereAnchorAtEnd(pattern, i) {
				result.WriteByte(character)
			} else {
				result.WriteString("\\$")
			}
		default:
			result.WriteByte(character)
		}
		switch character {
		case '(', '|':
			atStart = true
		default:
			atStart = false
		}
		i++
	}
	return result.String(), nil
}

// posixWordEscape maps the GNU word and character-class escapes that both BREs
// and EREs accept onto their RE2 spellings. RE2 has only the two-sided \b where
// GNU has \< and \>, which agree everywhere a word actually begins or ends;
// they differ only for a pattern that anchors the wrong edge of a word, such as
// \>foo. The class escapes match GNU's own C-locale definitions.
func posixWordEscape(escaped byte) (string, bool) {
	switch escaped {
	case '<', '>', 'b':
		return `\b`, true
	case 'B':
		return `\B`, true
	case 'w':
		return "[[:alnum:]_]", true
	case 'W':
		return "[^[:alnum:]_]", true
	case 's':
		return "[[:space:]]", true
	case 'S':
		return "[^[:space:]]", true
	}
	return "", false
}

func unsupportedRegexpBackreference() error {
	return fmt.Errorf("regular-expression backreferences such as \\1 are not supported by RE2")
}

// parseBREInterval returns the contents of a \{m,n\} interval and the first
// byte after its closing \}. POSIX allows m, m,, and m,n, with decimal bounds.
func parseBREInterval(pattern string, start int) (string, int, error) {
	position := start + 2
	contentStart := position
	for position < len(pattern) {
		if pattern[position] == '\\' && position+1 < len(pattern) && pattern[position+1] == '}' {
			interval := pattern[contentStart:position]
			if !validBREInterval(interval) {
				return "", position, fmt.Errorf("invalid BRE interval \\{%s\\}", interval)
			}
			return interval, position + 2, nil
		}
		position++
	}
	return "", position, fmt.Errorf("unterminated BRE interval")
}

func validBREInterval(interval string) bool {
	if interval == "" {
		return false
	}
	comma := strings.IndexByte(interval, ',')
	if comma < 0 {
		return decimalDigits(interval)
	}
	if strings.Count(interval, ",") != 1 || !decimalDigits(interval[:comma]) {
		return false
	}
	return interval[comma+1:] == "" || decimalDigits(interval[comma+1:])
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func breAnchorAtEnd(pattern string, position int) bool {
	if position+1 == len(pattern) {
		return true
	}
	return position+2 < len(pattern) && pattern[position+1] == '\\' &&
		(pattern[position+2] == ')' || pattern[position+2] == '|')
}

func ereAnchorAtEnd(pattern string, position int) bool {
	if position+1 == len(pattern) {
		return true
	}
	return pattern[position+1] == ')' || pattern[position+1] == '|'
}

// copyPOSIXBracketExpression copies one bracket expression unchanged. RE2 and
// POSIX share the usual ranges and named character classes; the only parsing
// here is to avoid treating the ] which ends [[:alpha:]] as the end of the
// outer expression.
func copyPOSIXBracketExpression(result *strings.Builder, pattern string, start int) (int, error) {
	position := start
	result.WriteByte(pattern[position])
	position++
	if position < len(pattern) && pattern[position] == '^' {
		result.WriteByte(pattern[position])
		position++
	}
	if position < len(pattern) && pattern[position] == ']' {
		result.WriteByte(pattern[position])
		position++
	}
	for position < len(pattern) {
		if pattern[position] == '\\' && position+1 < len(pattern) {
			result.WriteByte(pattern[position])
			result.WriteByte(pattern[position+1])
			position += 2
			continue
		}
		if pattern[position] == '[' && position+1 < len(pattern) && strings.ContainsRune(".=:", rune(pattern[position+1])) {
			marker := pattern[position+1]
			end := strings.Index(pattern[position+2:], string([]byte{marker, ']'}))
			if end >= 0 {
				end += position + 4
				result.WriteString(pattern[position:end])
				position = end
				continue
			}
		}
		character := pattern[position]
		result.WriteByte(character)
		position++
		if character == ']' {
			return position, nil
		}
	}
	return position, fmt.Errorf("unterminated bracket expression")
}
