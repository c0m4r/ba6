// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	//nolint:gosec // These compatibility applets intentionally implement MD5 and SHA-1.
	"crypto/md5"
	//nolint:gosec // These compatibility applets intentionally implement MD5 and SHA-1.
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

type checksumSpec struct {
	prog string
	new  func() hash.Hash
	size int
}

var (
	md5Checksum    = checksumSpec{prog: "md5sum", new: md5.New, size: md5.Size}
	sha1Checksum   = checksumSpec{prog: "sha1sum", new: sha1.New, size: sha1.Size}
	sha256Checksum = checksumSpec{prog: "sha256sum", new: sha256.New, size: sha256.Size}
	sha512Checksum = checksumSpec{prog: "sha512sum", new: sha512.New, size: sha512.Size}
)

func cmdMd5sum(args []string) int    { return cmdChecksum(md5Checksum, args) }
func cmdSha1sum(args []string) int   { return cmdChecksum(sha1Checksum, args) }
func cmdSha256sum(args []string) int { return cmdChecksum(sha256Checksum, args) }
func cmdSha512sum(args []string) int { return cmdChecksum(sha512Checksum, args) }

func cmdChecksum(spec checksumSpec, args []string) int {
	args = expandShortOptions(args, "")
	check, quiet, statusOnly := false, false, false
	binary, modeGiven := false, false
	var files []string
	parsing := true
	for _, arg := range args {
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-c" || arg == "--check"):
			check = true
		case parsing && arg == "--quiet":
			quiet = true
		case parsing && arg == "--status":
			statusOnly = true
		case parsing && (arg == "-b" || arg == "--binary"):
			binary, modeGiven = true, true
		case parsing && (arg == "-t" || arg == "--text"):
			binary, modeGiven = false, true
		case parsing && len(arg) > 1 && arg[0] == '-':
			fatalf(spec.prog, "invalid option %q", arg)
			return 1
		default:
			files = append(files, arg)
		}
	}
	// The originals reject the options that only make sense on the other side
	// of -c rather than silently ignoring them.
	if !check {
		for _, misplaced := range []struct {
			given bool
			name  string
		}{{quiet, "--quiet"}, {statusOnly, "--status"}} {
			if misplaced.given {
				fatalf(spec.prog, "the %s option is meaningful only when verifying checksums", misplaced.name)
				fmt.Fprintf(os.Stderr, "Try '%s --help' for more information.\n", spec.prog)
				return 1
			}
		}
	} else if modeGiven {
		fatalf(spec.prog, "the --binary and --text options are meaningless when verifying checksums")
		fmt.Fprintf(os.Stderr, "Try '%s --help' for more information.\n", spec.prog)
		return 1
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	if check {
		return checkChecksumFiles(spec, files, quiet, statusOnly)
	}
	// Reading is identical either way on Linux; the marker still has to be
	// printed, because it is what tells the two modes apart in a checksum file.
	marker := " "
	if binary {
		marker = "*"
	}
	statusCode := 0
	for _, name := range files {
		sum, err := checksumFile(spec, name)
		if err != nil {
			fatalf(spec.prog, "%s: %s", name, errText(err))
			statusCode = 1
			continue
		}
		// A name holding a backslash, newline or carriage return would otherwise
		// be ambiguous in this line-oriented format, so the originals escape it
		// and flag the line with a leading backslash for -c to reverse.
		line, escaped := escapeChecksumName(name)
		prefix := ""
		if escaped {
			prefix = `\`
		}
		if _, err := fmt.Fprintf(os.Stdout, "%s%x %s%s\n", prefix, sum, marker, line); err != nil {
			fatalf(spec.prog, "write error: %v", err)
			return 1
		}
	}
	return statusCode
}

// escapeChecksumName renders name for a checksum line. Only the three
// characters that would break the format are escaped -- the backslash that
// does the escaping, plus newline and carriage return; tabs and other control
// characters are written through untouched, as in the originals. The second
// result reports whether anything was escaped, which is what tells the caller
// to mark the line.
func escapeChecksumName(name string) (string, bool) {
	if !strings.ContainsAny(name, "\\\n\r") {
		return name, false
	}
	var out strings.Builder
	for index := 0; index < len(name); index++ {
		switch name[index] {
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		default:
			out.WriteByte(name[index])
		}
	}
	return out.String(), true
}

// unescapeChecksumName reverses escapeChecksumName for a line that carried the
// leading backslash marker. An unknown escape means the line is malformed.
func unescapeChecksumName(name string) (string, bool) {
	var out strings.Builder
	for index := 0; index < len(name); index++ {
		if name[index] != '\\' {
			out.WriteByte(name[index])
			continue
		}
		index++
		if index >= len(name) {
			return "", false
		}
		switch name[index] {
		case '\\':
			out.WriteByte('\\')
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		default:
			return "", false
		}
	}
	return out.String(), true
}

func checksumFile(spec checksumSpec, name string) ([]byte, error) {
	input, err := openInput(name)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	digest := spec.new()
	if _, err := io.Copy(digest, input); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}

// checksumTally counts the three things that can go wrong while verifying one
// checksum list. The originals report each as a single summary warning rather
// than a diagnostic per line.
type checksumTally struct {
	badFormat  int
	unreadable int
	mismatched int
}

func checkChecksumFiles(spec checksumSpec, lists []string, quiet, statusOnly bool) int {
	statusCode := 0
	for _, list := range lists {
		input, err := openInput(list)
		if err != nil {
			if !statusOnly {
				fatalf(spec.prog, "%s: %s", list, errText(err))
			}
			statusCode = 1
			continue
		}
		var tally checksumTally
		scanner := newLineScanner(input)
		validLines := 0
		for scanner.Scan() {
			expected, name, ok := parseChecksumCheckLine(scanner.Text(), spec.size)
			if !ok {
				tally.badFormat++
				continue
			}
			validLines++
			actual, hashErr := checksumFile(spec, name)
			switch {
			case hashErr != nil:
				// The originals report the underlying error on stderr and still
				// mark the entry on stdout, so a reader of either stream alone
				// sees the failure.
				if !statusOnly {
					fatalf(spec.prog, "%s: %s", name, errText(hashErr))
					fmt.Fprintf(os.Stdout, "%s: FAILED open or read\n", shellQuoteName(name))
				}
				tally.unreadable++
				statusCode = 1
			case !bytesEqual(actual, expected):
				if !statusOnly {
					fmt.Fprintf(os.Stdout, "%s: FAILED\n", shellQuoteName(name))
				}
				tally.mismatched++
				statusCode = 1
			default:
				if !statusOnly && !quiet {
					fmt.Fprintf(os.Stdout, "%s: OK\n", shellQuoteName(name))
				}
			}
		}
		if err := scanner.Err(); err != nil {
			if !statusOnly {
				fatalf(spec.prog, "%s: %s", list, errText(err))
			}
			statusCode = 1
		}
		if closeErr := input.Close(); closeErr != nil {
			statusCode = 1
		}
		// A list with nothing usable in it is an error in its own right, and
		// replaces the per-category warnings. Malformed lines alongside valid
		// ones are only ever a warning: they do not change the exit status.
		if validLines == 0 {
			if !statusOnly {
				fatalf(spec.prog, "%s: no properly formatted checksum lines found", list)
			}
			statusCode = 1
			continue
		}
		if !statusOnly {
			reportChecksumTally(spec.prog, tally)
		}
	}
	return statusCode
}

func reportChecksumTally(prog string, tally checksumTally) {
	if tally.badFormat > 0 {
		noun, verb := "lines", "are"
		if tally.badFormat == 1 {
			noun, verb = "line", "is"
		}
		fmt.Fprintf(os.Stderr, "%s: WARNING: %d %s %s improperly formatted\n", prog, tally.badFormat, noun, verb)
	}
	if tally.unreadable > 0 {
		noun := "files"
		if tally.unreadable == 1 {
			noun = "file"
		}
		fmt.Fprintf(os.Stderr, "%s: WARNING: %d listed %s could not be read\n", prog, tally.unreadable, noun)
	}
	if tally.mismatched > 0 {
		noun := "checksums"
		if tally.mismatched == 1 {
			noun = "checksum"
		}
		fmt.Fprintf(os.Stderr, "%s: WARNING: %d computed %s did NOT match\n", prog, tally.mismatched, noun)
	}
}

func parseChecksumCheckLine(line string, size int) ([]byte, string, bool) {
	// A leading backslash marks a line whose name was escaped when written.
	escaped := strings.HasPrefix(line, `\`)
	if escaped {
		line = line[1:]
	}
	if len(line) < size*2+2 {
		return nil, "", false
	}
	digest, err := hex.DecodeString(line[:size*2])
	if err != nil || len(digest) != size || line[size*2] != ' ' ||
		line[size*2+1] != ' ' && line[size*2+1] != '*' {
		return nil, "", false
	}
	name := strings.TrimSuffix(line[size*2+2:], "\r")
	if name == "" {
		return nil, "", false
	}
	if escaped {
		unescaped, ok := unescapeChecksumName(name)
		if !ok {
			return nil, "", false
		}
		name = unescaped
	}
	return digest, name, true
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
