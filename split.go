// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// splitSuffix generates output file names: the prefix followed by a
// fixed-length suffix over an alphabet (a-z, digits, or hex). The suffix
// never starts with the alphabet's last character; when a carry would make
// it, that character is absorbed into the prefix and the suffix grows by one,
// the same sequence GNU split produces (xaa..xyz, xzaaa, ...).
type splitSuffix struct {
	alphabet string
	prefix   string
	length   int
	auto     bool
	index    []int
	init     bool
	extra    string
}

func newSplitSuffix(alphabet, prefix, extra string, length int, auto bool) *splitSuffix {
	return &splitSuffix{
		alphabet: alphabet,
		prefix:   prefix,
		length:   length,
		auto:     auto,
		index:    make([]int, length),
		extra:    extra,
	}
}

// setStart fast-forwards a numeric suffix to a given start value.
func (s *splitSuffix) setStart(start string) bool {
	if len(start) > s.length {
		return false
	}
	for i := 0; i < len(start); i++ {
		s.index[s.length-len(start)+i] = strings.IndexByte(s.alphabet, start[i])
	}
	s.init = true
	return true
}

// next returns the next file name, widening the suffix when the carry would
// make the first character the alphabet's last one. ok is false when the
// alphabet is exhausted and widening is disabled.
func (s *splitSuffix) next() (name string, ok bool) {
	if !s.init {
		s.init = true
		return s.build(), true
	}
	for i := s.length - 1; i >= 0; i-- {
		s.index[i]++
		if s.index[i] < len(s.alphabet) {
			if s.auto && i == 0 && s.index[i] == len(s.alphabet)-1 {
				s.widen()
				return s.build(), true
			}
			return s.build(), true
		}
		if s.auto && i == 0 {
			s.widen()
			return s.build(), true
		}
		s.index[i] = 0
	}
	return "", false
}

// widen absorbs the alphabet's last character into the prefix and grows the
// suffix by one, mirroring GNU split's file-name sequence.
func (s *splitSuffix) widen() {
	s.prefix += string(s.alphabet[len(s.alphabet)-1])
	s.length++
	s.index = make([]int, s.length)
}

func (s *splitSuffix) build() string {
	var b strings.Builder
	b.WriteString(s.prefix)
	for _, i := range s.index {
		b.WriteByte(s.alphabet[i])
	}
	b.WriteString(s.extra)
	return b.String()
}

// parseSplitSize parses a byte size with GNU split's multipliers: b=512,
// K/M/G/T/P/E/Z/Y powers of 1024, and KB/MB/... powers of 1000.
func parseSplitSize(spec string) (int64, bool) {
	if spec == "" {
		return 0, false
	}
	rest, mult := spec, int64(1)
	if len(spec) > 1 && spec[len(spec)-1] == 'b' && spec[len(spec)-2] != 'B' {
		rest, mult = spec[:len(spec)-1], 512
	} else {
		upper := strings.ToUpper(spec)
		suffixes := []struct {
			suffix string
			value  int64
		}{
			{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000},
			{"TB", 1000 * 1000 * 1000 * 1000}, {"PB", 1000 * 1000 * 1000 * 1000 * 1000},
			{"EB", 1000 * 1000 * 1000 * 1000 * 1000 * 1000},
			{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"T", 1 << 40},
			{"P", 1 << 50}, {"E", 1 << 60},
		}
		for _, s := range suffixes {
			if strings.HasSuffix(upper, s.suffix) {
				rest, mult = spec[:len(spec)-len(s.suffix)], s.value
				break
			}
		}
	}
	value, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || value < 1 {
		return 0, false
	}
	if value > (1<<62)/mult {
		return 0, false
	}
	return value * mult, true
}

type splitMode int

const (
	splitLines splitMode = iota
	splitBytes
	splitChunkBytes
	splitChunkLines
	splitRoundRobin
	splitLineBytes
)

// cmdSplit implements split(1): write the input to pieces of a fixed size
// (lines with -l, bytes with -b, or a chunk count with -n) named with a
// rotating suffix. -n N makes N byte-sized pieces, K/N prints one of them,
// l/N makes line-aligned pieces, and r/N distributes records round-robin.
func cmdSplit(args []string) int {
	mode := splitLines
	size := int64(1000)
	chunkK, chunkN := int64(0), int64(0)
	suffixLength := 0
	suffixLengthSet := false
	alphabet := "abcdefghijklmnopqrstuvwxyz"
	numericStart := ""
	elideEmpty := false
	verbose := false
	sep := byte('\n')
	additional := ""
	var operands []string
	modeSet := false

	parseChunkSpec := func(spec string) bool {
		spec = strings.TrimLeft(spec, " \t")
		var newMode splitMode
		rest := spec
		switch {
		case strings.HasPrefix(rest, "r/"):
			newMode, rest = splitRoundRobin, rest[2:]
		case strings.HasPrefix(rest, "l/"):
			newMode, rest = splitChunkLines, rest[2:]
		default:
			newMode = splitChunkBytes
		}
		if modeSet && mode != newMode {
			fatalf("split", "cannot split in more than one way")
			return false
		}
		mode, modeSet = newMode, true
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			k64, err := strconv.ParseInt(rest[:slash], 10, 64)
			n64, err2 := strconv.ParseInt(rest[slash+1:], 10, 64)
			if err != nil || err2 != nil || k64 < 1 || n64 < 1 || k64 > n64 {
				fatalf("split", "invalid chunk number: %q", spec)
				return false
			}
			chunkK, chunkN = k64, n64
			return true
		}
		n64, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || n64 < 1 {
			fatalf("split", "invalid number of chunks: %q", spec)
			return false
		}
		chunkN = n64
		return true
	}

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			operands = append(operands, args[i:]...)
			i = len(args)
		case a == "-b" || a == "--bytes":
			if modeSet && mode != splitBytes {
				fatalf("split", "cannot split in more than one way")
				return 1
			}
			mode, modeSet = splitBytes, true
			i++
			if i >= len(args) {
				fatalf("split", "option requires an argument -- 'b'")
				return 1
			}
			value, ok := parseSplitSize(args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
			if !ok {
				fatalf("split", "invalid number of bytes: %q", args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
				return 1
			}
			size = value
		case strings.HasPrefix(a, "--bytes="):
			if modeSet && mode != splitBytes {
				fatalf("split", "cannot split in more than one way")
				return 1
			}
			mode, modeSet = splitBytes, true
			value, ok := parseSplitSize(strings.TrimPrefix(a, "--bytes="))
			if !ok {
				fatalf("split", "invalid number of bytes: %q", strings.TrimPrefix(a, "--bytes="))
				return 1
			}
			size = value
		case a == "-l" || a == "--lines":
			if modeSet && mode != splitLines {
				fatalf("split", "cannot split in more than one way")
				return 1
			}
			mode, modeSet = splitLines, true
			i++
			if i >= len(args) {
				fatalf("split", "option requires an argument -- 'l'")
				return 1
			}
			value, err := strconv.ParseInt(args[i], 10, 64) //nolint:gosec // G602: i is checked against len(args) immediately above.
			if err != nil || value < 1 {
				fatalf("split", "invalid number of lines: %q", args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
				return 1
			}
			size = value
		case strings.HasPrefix(a, "--lines="):
			if modeSet && mode != splitLines {
				fatalf("split", "cannot split in more than one way")
				return 1
			}
			mode, modeSet = splitLines, true
			value, err := strconv.ParseInt(strings.TrimPrefix(a, "--lines="), 10, 64)
			if err != nil || value < 1 {
				fatalf("split", "invalid number of lines: %q", strings.TrimPrefix(a, "--lines="))
				return 1
			}
			size = value
		case a == "-C" || a == "--line-bytes":
			if modeSet && mode != splitLineBytes {
				fatalf("split", "cannot split in more than one way")
				return 1
			}
			mode, modeSet = splitLineBytes, true
			i++
			if i >= len(args) {
				fatalf("split", "option requires an argument -- 'C'")
				return 1
			}
			value, ok := parseSplitSize(args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
			if !ok {
				fatalf("split", "invalid number of bytes: %q", args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
				return 1
			}
			size = value
		case a == "-n" || a == "--number":
			i++
			if i >= len(args) {
				fatalf("split", "option requires an argument -- 'n'")
				return 1
			}
			if !parseChunkSpec(args[i]) { //nolint:gosec // G602: i is checked against len(args) immediately above.
				return 1
			}
		case strings.HasPrefix(a, "--number="):
			if !parseChunkSpec(strings.TrimPrefix(a, "--number=")) {
				return 1
			}
		case a == "-a" || a == "--suffix-length":
			i++
			if i >= len(args) {
				fatalf("split", "option requires an argument -- 'a'")
				return 1
			}
			value, err := strconv.Atoi(args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
			if err != nil || value < 0 {
				fatalf("split", "invalid suffix length: %q", args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
				return 1
			}
			suffixLength, suffixLengthSet = value, true
		case a == "-d" || a == "--numeric-suffixes":
			alphabet = "0123456789"
		case a == "-x" || a == "--hex-suffixes":
			alphabet = "0123456789abcdef"
		case strings.HasPrefix(a, "--numeric-suffixes="):
			alphabet = "0123456789"
			numericStart = strings.TrimPrefix(a, "--numeric-suffixes=")
			for strings.HasPrefix(numericStart, "0") && len(numericStart) > 1 {
				numericStart = numericStart[1:]
			}
			for _, c := range numericStart {
				if c < '0' || c > '9' {
					fatalf("split", "%q: invalid start value for numerical suffix", numericStart)
					return 1
				}
			}
		case strings.HasPrefix(a, "--hex-suffixes="):
			alphabet = "0123456789abcdef"
			numericStart = strings.TrimPrefix(a, "--hex-suffixes=")
			for strings.HasPrefix(numericStart, "0") && len(numericStart) > 1 {
				numericStart = numericStart[1:]
			}
			for _, c := range numericStart {
				if !strings.ContainsRune("0123456789abcdef", c) {
					fatalf("split", "%q: invalid start value for hexadecimal suffix", numericStart)
					return 1
				}
			}
		case strings.HasPrefix(a, "--additional-suffix="):
			additional = strings.TrimPrefix(a, "--additional-suffix=")
		case a == "-e" || a == "--elide-empty-files":
			elideEmpty = true
		case a == "--verbose":
			verbose = true
		case a == "-u" || a == "--unbuffered":
			// Output buffering choice; ba6 always buffers.
		case a == "-t" || a == "--separator":
			i++
			if i >= len(args) {
				fatalf("split", "option requires an argument -- 't'")
				return 1
			}
			spec := args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
			if spec == "" {
				fatalf("split", "empty record separator")
				return 1
			}
			if spec == "\\0" {
				sep = 0
			} else if len(spec) > 1 {
				fatalf("split", "multi-character separator %q", spec)
				return 1
			} else {
				sep = spec[0]
			}
		case len(a) > 1 && a[0] == '-' && a[1] >= '0' && a[1] <= '9':
			if modeSet && mode != splitLines {
				fatalf("split", "cannot split in more than one way")
				return 1
			}
			mode, modeSet = splitLines, true
			value, err := strconv.ParseInt(a[1:], 10, 64)
			if err != nil || value < 1 {
				fatalf("split", "invalid number of lines: %q", a[1:])
				return 1
			}
			size = value
		case len(a) > 1 && a[0] == '-':
			fatalf("split", "invalid option %q", a)
			return 1
		default:
			operands = append(operands, a)
		}
	}
	if len(operands) > 2 {
		fatalf("split", "extra operand %q", operands[2])
		return 1
	}
	input, prefix := "-", "x"
	if len(operands) > 0 {
		input = operands[0]
	}
	if len(operands) > 1 {
		prefix = operands[1]
	}
	if numericStart != "" {
		if start, err := strconv.ParseInt(numericStart, 10, 64); err == nil && start >= chunkN {
			numericStart = ""
		}
	}
	if suffixLengthSet {
		needed := splitSuffixLength(chunkN, len(alphabet), numericStart)
		if chunkN > 0 && suffixLength < needed {
			fatalf("split", "the suffix length needs to be at least %d", needed)
			return 1
		}
	} else if chunkN > 0 {
		suffixLength = splitSuffixLength(chunkN, len(alphabet), numericStart)
		if suffixLength < 2 {
			suffixLength = 2
		}
	} else {
		suffixLength = 2
	}
	if numericStart != "" && len(numericStart) > suffixLength {
		fatalf("split", "numerical suffix start value is too large for the suffix length")
		return 1
	}

	data, status := readSplitInput(input)
	if status != 0 {
		return status
	}
	var inputInfo os.FileInfo
	if input != "-" {
		if info, err := os.Stat(input); err == nil {
			inputInfo = info
		}
	}
	suffix := newSplitSuffix(alphabet, prefix, additional, suffixLength,
		numericStart == "" && chunkN == 0 && !suffixLengthSet)
	if numericStart != "" && !suffix.setStart(numericStart) {
		fatalf("split", "numerical suffix start value is too large for the suffix length")
		return 1
	}

	write := func(name string, chunk []byte) int {
		if inputInfo != nil {
			if info, err := os.Stat(name); err == nil && os.SameFile(inputInfo, info) {
				fatalf("split", "%s would overwrite input; aborting", name)
				return 1
			}
		}
		if verbose {
			fmt.Fprintf(os.Stdout, "creating file %s\n", name)
		}
		out, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666) //nolint:gosec // split follows the process umask like the standard utility.
		if err != nil {
			fatalf("split", "%s: %s", name, errText(err))
			return 1
		}
		_, werr := out.Write(chunk)
		cerr := out.Close()
		if werr != nil || cerr != nil {
			fatalf("split", "%s: %s", name, errText(firstErr(werr, cerr)))
			return 1
		}
		return 0
	}
	nextName := func() (string, bool) {
		name, ok := suffix.next()
		if !ok {
			fatalf("split", "output file suffixes exhausted")
			return "", false
		}
		return name, true
	}

	switch mode {
	case splitLines:
		return splitByLines(data, sep, size, nextName, write)
	case splitBytes:
		return splitByBytes(data, size, nextName, write)
	case splitLineBytes:
		return splitByLineBytes(data, sep, size, nextName, write)
	case splitChunkBytes:
		if chunkK > 0 {
			return writeChunkToStdout(byteChunks(data, chunkK, chunkN)[0])
		}
		return writeChunks(byteChunks(data, 0, chunkN), nextName, write, elideEmpty)
	case splitChunkLines:
		if chunkK > 0 {
			return writeChunkToStdout(lineAlignedChunks(data, sep, chunkK, chunkN)[0])
		}
		return writeChunks(lineAlignedChunks(data, sep, 0, chunkN), nextName, write, elideEmpty)
	case splitRoundRobin:
		return splitRoundRobinLines(data, sep, chunkK, chunkN, nextName, write, elideEmpty)
	}
	return 0
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func splitSuffixLength(units int64, alphabetLen int, start string) int {
	if units == 0 {
		return 0
	}
	end := units - 1
	if start != "" {
		if value, err := strconv.ParseInt(start, 10, 64); err == nil && value < units {
			end += value
		}
	}
	needed := 0
	for {
		needed++
		end /= int64(alphabetLen)
		if end == 0 {
			break
		}
	}
	return needed
}

func readSplitInput(input string) ([]byte, int) {
	var reader io.Reader
	if input == "-" {
		reader = os.Stdin
	} else {
		file, err := os.Open(input)
		if err != nil {
			fatalf("split", "cannot open %s for reading: %s", input, errText(err))
			return nil, 1
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		fatalf("split", "%s: %s", input, errText(err))
		return nil, 1
	}
	return data, 0
}

// splitByLines writes pieces of exactly perFile records.
func splitByLines(data []byte, sep byte, perFile int64, nextName func() (string, bool), write func(string, []byte) int) int {
	count := int64(0)
	var current []byte
	flush := func() int {
		if len(current) == 0 {
			return 0
		}
		name, ok := nextName()
		if !ok {
			return 1
		}
		status := write(name, current)
		current = nil
		return status
	}
	for len(data) > 0 {
		idx := bytes.IndexByte(data, sep)
		if idx < 0 {
			current = append(current, data...)
			data = nil
			break
		}
		current = append(current, data[:idx+1]...)
		data = data[idx+1:]
		count++
		if count >= perFile {
			if status := flush(); status != 0 {
				return status
			}
			count = 0
		}
	}
	return flush()
}

// splitByBytes writes pieces of exactly size bytes.
func splitByBytes(data []byte, size int64, nextName func() (string, bool), write func(string, []byte) int) int {
	for pos := int64(0); pos < int64(len(data)); {
		end := pos + size
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		name, ok := nextName()
		if !ok {
			return 1
		}
		if status := write(name, data[pos:end]); status != 0 {
			return status
		}
		pos = end
	}
	return 0
}

// byteChunks partitions data into n byte-sized pieces for -n N (and selects
// piece k for K/N), the first rem pieces one byte longer.
func byteChunks(data []byte, k, n int64) [][]byte {
	size := int64(len(data)) / n
	rem := int64(len(data)) % n
	chunks := make([][]byte, n)
	pos := int64(0)
	for i := int64(0); i < n; i++ {
		toWrite := size
		if i < rem {
			toWrite++
		}
		end := pos + toWrite
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		chunks[i] = data[pos:end]
		pos = end
	}
	if k > 0 {
		return [][]byte{chunks[k-1]}
	}
	return chunks
}

// lineAlignedChunks partitions data into n byte-sized pieces whose boundaries
// snap to record ends, for -n l/N; a record longer than a piece swallows the
// following pieces, which come out empty. GNU's lines_chunk_split algorithm.
func lineAlignedChunks(data []byte, sep byte, k, n int64) [][]byte {
	size := int64(len(data)) / n
	rem := int64(len(data)) % n
	chunks := make([][]byte, n)
	pos := int64(0)
	for i := int64(0); i < n; i++ {
		if pos >= int64(len(data)) {
			break
		}
		target := (i+1)*size + min64(i+1, rem)
		if target > int64(len(data)) {
			target = int64(len(data))
		}
		from := target - 1
		if from < pos {
			from = pos
		}
		idx := bytes.IndexByte(data[from:], sep)
		end := int64(len(data))
		if idx >= 0 {
			end = from + int64(idx) + 1
		}
		chunks[i] = data[pos:end]
		pos = end
		for i+1 < n {
			nextTarget := (i+2)*size + min64(i+2, rem)
			if nextTarget <= pos {
				i++
			} else {
				break
			}
		}
	}
	if k > 0 {
		return [][]byte{chunks[k-1]}
	}
	return chunks
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// writeChunks writes the pieces to suffix-named files, skipping empty ones
// with -e.
func writeChunks(chunks [][]byte, nextName func() (string, bool), write func(string, []byte) int, elide bool) int {
	for _, chunk := range chunks {
		if elide && len(chunk) == 0 {
			continue
		}
		name, ok := nextName()
		if !ok {
			return 1
		}
		if status := write(name, chunk); status != 0 {
			return status
		}
	}
	return 0
}

// writeChunkToStdout implements -n K/N: the selected piece goes to stdout.
func writeChunkToStdout(chunk []byte) int {
	if _, err := os.Stdout.Write(chunk); err != nil {
		fatalf("split", "write error: %v", err)
		return 1
	}
	return 0
}

// splitRoundRobinLines distributes records over n files round-robin; for K/N
// only the records at position K of each cycle go to standard output.
func splitRoundRobinLines(data []byte, sep byte, k, n int64, nextName func() (string, bool), write func(string, []byte) int, elide bool) int {
	if k > 0 {
		line := int64(1)
		for len(data) > 0 {
			idx := bytes.IndexByte(data, sep)
			end := len(data)
			if idx >= 0 {
				end = idx + 1
			}
			if line == k {
				if _, err := os.Stdout.Write(data[:end]); err != nil {
					fatalf("split", "write error: %v", err)
					return 1
				}
			}
			if idx >= 0 {
				line++
				if line > n {
					line = 1
				}
			}
			data = data[end:]
		}
		return 0
	}
	files := make([][]byte, n)
	i := int64(0)
	for len(data) > 0 {
		idx := bytes.IndexByte(data, sep)
		end := len(data)
		if idx >= 0 {
			end = idx + 1
		}
		files[i] = append(files[i], data[:end]...)
		data = data[end:]
		if idx >= 0 {
			i = (i + 1) % n
		}
	}
	for _, chunk := range files {
		if elide && len(chunk) == 0 {
			continue
		}
		name, ok := nextName()
		if !ok {
			return 1
		}
		if status := write(name, chunk); status != 0 {
			return status
		}
	}
	return 0
}

// splitByLineBytes writes pieces of at most size bytes (-C). A piece ends at
// the last record boundary inside the size window; a record longer than the
// window is cut exactly at the window edge and its remainder (held, like
// GNU's hold buffer) starts the next piece.
func splitByLineBytes(data []byte, sep byte, size int64, nextName func() (string, bool), write func(string, []byte) int) int {
	var current []byte
	flush := func() int {
		if len(current) == 0 {
			return 0
		}
		name, ok := nextName()
		if !ok {
			return 1
		}
		status := write(name, current)
		current = nil
		return status
	}
	var hold []byte
	splitLine := false
	for pos := int64(0); pos < int64(len(data)); {
		remaining := int64(len(data)) - pos
		splitRest := int64(0)
		eocSet := false
		var eol int64 = -1
		if size-int64(len(current))-int64(len(hold)) <= remaining {
			splitRest = size - int64(len(current)) - int64(len(hold))
			eocSet = true
			if idx := bytes.LastIndexByte(data[pos:pos+splitRest], sep); idx >= 0 {
				eol = int64(idx)
			}
		} else if idx := bytes.LastIndexByte(data[pos:], sep); idx >= 0 {
			eol = int64(idx)
		}
		if len(hold) > 0 && (eol >= 0 || len(current) == 0) {
			current = append(current, hold...)
			hold = nil
		}
		if eol >= 0 {
			splitLine = true
			current = append(current, data[pos:pos+eol+1]...)
			pos += eol + 1
			if eocSet {
				splitRest -= eol + 1
			}
		}
		if pos < int64(len(data)) && !splitLine {
			n := splitRest
			if !eocSet {
				n = int64(len(data)) - pos
			}
			current = append(current, data[pos:pos+n]...)
			pos += n
			if eocSet {
				splitRest -= n
			}
		}
		if (eocSet && splitRest > 0) || (!eocSet && pos < int64(len(data))) {
			n := splitRest
			if !eocSet {
				n = int64(len(data)) - pos
			}
			hold = append(hold, data[pos:pos+n]...)
			pos += n
		}
		if eocSet {
			if status := flush(); status != 0 {
				return status
			}
			splitLine = false
		}
	}
	if len(hold) > 0 {
		current = append(current, hold...)
	}
	return flush()
}
