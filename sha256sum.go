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
			fatalf(spec.prog, "%s: %v", name, err)
			statusCode = 1
			continue
		}
		if _, err := fmt.Fprintf(os.Stdout, "%x %s%s\n", sum, marker, name); err != nil {
			fatalf(spec.prog, "write error: %v", err)
			return 1
		}
	}
	return statusCode
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

func checkChecksumFiles(spec checksumSpec, lists []string, quiet, statusOnly bool) int {
	statusCode := 0
	for _, list := range lists {
		input, err := openInput(list)
		if err != nil {
			if !statusOnly {
				fatalf(spec.prog, "%s: %v", list, err)
			}
			statusCode = 1
			continue
		}
		scanner := newLineScanner(input)
		lineNumber, validLines := 0, 0
		for scanner.Scan() {
			lineNumber++
			expected, name, parseErr := parseChecksumCheckLine(scanner.Text(), spec.size)
			if parseErr != nil {
				if !statusOnly {
					fatalf(spec.prog, "%s:%d: improperly formatted checksum line", list, lineNumber)
				}
				statusCode = 1
				continue
			}
			validLines++
			actual, hashErr := checksumFile(spec, name)
			matches := hashErr == nil && bytesEqual(actual, expected)
			if !statusOnly && (!matches || !quiet) {
				result := "OK"
				if !matches {
					result = "FAILED"
				}
				fmt.Fprintf(os.Stdout, "%s: %s\n", name, result)
			}
			if !matches {
				if hashErr != nil && !statusOnly {
					fatalf(spec.prog, "%s: %v", name, hashErr)
				}
				statusCode = 1
			}
		}
		if err := scanner.Err(); err != nil {
			if !statusOnly {
				fatalf(spec.prog, "%s: %v", list, err)
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

func parseChecksumCheckLine(line string, size int) ([]byte, string, error) {
	if len(line) < size*2+2 {
		return nil, "", fmt.Errorf("short checksum line")
	}
	digest, err := hex.DecodeString(line[:size*2])
	if err != nil || len(digest) != size || line[size*2] != ' ' ||
		line[size*2+1] != ' ' && line[size*2+1] != '*' {
		return nil, "", fmt.Errorf("invalid checksum line")
	}
	name := strings.TrimSuffix(line[size*2+2:], "\r")
	if name == "" {
		return nil, "", fmt.Errorf("missing filename")
	}
	return digest, name, nil
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
