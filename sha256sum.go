// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

func cmdSha256sum(args []string) int {
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
			fatalf("sha256sum", "invalid option %q", arg)
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
				fatalf("sha256sum", "the %s option is meaningful only when verifying checksums", misplaced.name)
				fmt.Fprintln(os.Stderr, "Try 'sha256sum --help' for more information.")
				return 1
			}
		}
	} else if modeGiven {
		fatalf("sha256sum", "the --binary and --text options are meaningless when verifying checksums")
		fmt.Fprintln(os.Stderr, "Try 'sha256sum --help' for more information.")
		return 1
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	if check {
		return checkSHA256Files(files, quiet, statusOnly)
	}
	// Reading is identical either way on Linux; the marker still has to be
	// printed, because it is what tells the two modes apart in a checksum file.
	marker := " "
	if binary {
		marker = "*"
	}
	statusCode := 0
	for _, name := range files {
		sum, err := sha256File(name)
		if err != nil {
			fatalf("sha256sum", "%s: %v", name, err)
			statusCode = 1
			continue
		}
		if _, err := fmt.Fprintf(os.Stdout, "%x %s%s\n", sum, marker, name); err != nil {
			fatalf("sha256sum", "write error: %v", err)
			return 1
		}
	}
	return statusCode
}

func sha256File(name string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	input, err := openInput(name)
	if err != nil {
		return result, err
	}
	defer input.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, input); err != nil {
		return result, err
	}
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func checkSHA256Files(lists []string, quiet, statusOnly bool) int {
	statusCode := 0
	for _, list := range lists {
		input, err := openInput(list)
		if err != nil {
			if !statusOnly {
				fatalf("sha256sum", "%s: %v", list, err)
			}
			statusCode = 1
			continue
		}
		scanner := newLineScanner(input)
		lineNumber, validLines := 0, 0
		for scanner.Scan() {
			lineNumber++
			expected, name, parseErr := parseSHA256CheckLine(scanner.Text())
			if parseErr != nil {
				if !statusOnly {
					fatalf("sha256sum", "%s:%d: improperly formatted checksum line", list, lineNumber)
				}
				statusCode = 1
				continue
			}
			validLines++
			actual, hashErr := sha256File(name)
			matches := hashErr == nil && actual == expected
			if !statusOnly && (!matches || !quiet) {
				result := "OK"
				if !matches {
					result = "FAILED"
				}
				fmt.Fprintf(os.Stdout, "%s: %s\n", name, result)
			}
			if !matches {
				if hashErr != nil && !statusOnly {
					fatalf("sha256sum", "%s: %v", name, hashErr)
				}
				statusCode = 1
			}
		}
		if err := scanner.Err(); err != nil {
			if !statusOnly {
				fatalf("sha256sum", "%s: %v", list, err)
			}
			statusCode = 1
		}
		if closeErr := input.Close(); closeErr != nil {
			statusCode = 1
		}
		if validLines == 0 {
			statusCode = 1
		}
	}
	return statusCode
}

func parseSHA256CheckLine(line string) ([sha256.Size]byte, string, error) {
	var expected [sha256.Size]byte
	if len(line) < sha256.Size*2+2 {
		return expected, "", fmt.Errorf("short checksum line")
	}
	digest, err := hex.DecodeString(line[:sha256.Size*2])
	if err != nil || len(digest) != sha256.Size || line[sha256.Size*2] != ' ' ||
		line[sha256.Size*2+1] != ' ' && line[sha256.Size*2+1] != '*' {
		return expected, "", fmt.Errorf("invalid checksum line")
	}
	name := strings.TrimSuffix(line[sha256.Size*2+2:], "\r")
	if name == "" {
		return expected, "", fmt.Errorf("missing filename")
	}
	copy(expected[:], digest)
	return expected, name, nil
}
