// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type ddOptions struct {
	input, output        string
	inputBlock, outBlock int64
	count, skip, seek    int64
	countSet             bool
	noTrunc, sync, noErr bool
	status               string
}

func cmdDd(args []string) int {
	opts, err := parseDDOptions(args)
	if err != nil {
		fatalf("dd", "%v", err)
		return 1
	}
	in, closeIn, err := ddInput(opts.input)
	if err != nil {
		fatalf("dd", "%v", err)
		return 1
	}
	defer closeIn()
	out, closeOut, err := ddOutput(opts)
	if err != nil {
		fatalf("dd", "%v", err)
		return 1
	}
	defer closeOut()

	if opts.skip > 0 {
		if err := skipInput(in, opts.skip*opts.inputBlock); err != nil {
			fatalf("dd", "skip: %v", err)
			return 1
		}
	}
	writer := bufio.NewWriterSize(out, int(opts.outBlock))
	buffer := make([]byte, int(opts.inputBlock))
	var fullIn, partialIn, fullOut, partialOut, total int64
	status := 0
	for block := int64(0); !opts.countSet || block < opts.count; block++ {
		n, readErr := io.ReadFull(in, buffer)
		if n == 0 && (errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)) {
			break
		}
		if n == len(buffer) {
			fullIn++
		} else if n > 0 {
			partialIn++
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			fatalf("dd", "read error: %v", readErr)
			status = 1
			if !opts.noErr {
				break
			}
			if n == 0 {
				break
			}
		}
		writeLength := n
		if opts.sync && n < len(buffer) {
			clear(buffer[n:])
			writeLength = len(buffer)
		}
		if writeLength > 0 {
			written, writeErr := writer.Write(buffer[:writeLength])
			total += int64(written)
			if writeErr != nil {
				fatalf("dd", "write error: %v", writeErr)
				status = 1
				break
			}
		}
		if readErr != nil && (errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)) {
			break
		}
	}
	if err := writer.Flush(); err != nil {
		fatalf("dd", "write error: %v", err)
		status = 1
	}
	fullOut, partialOut = total/opts.outBlock, 0
	if total%opts.outBlock != 0 {
		partialOut = 1
	}
	if opts.status != "none" {
		fmt.Fprintf(os.Stderr, "%d+%d records in\n%d+%d records out\n",
			fullIn, partialIn, fullOut, partialOut)
		if opts.status != "noxfer" {
			fmt.Fprintf(os.Stderr, "%d bytes copied\n", total)
		}
	}
	return status
}

func parseDDOptions(args []string) (ddOptions, error) {
	opts := ddOptions{input: "-", output: "-", inputBlock: 512, outBlock: 512}
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return opts, fmt.Errorf("unrecognized operand %q", arg)
		}
		switch key {
		case "if":
			opts.input = value
		case "of":
			opts.output = value
		case "bs", "ibs", "obs":
			n, err := parseDDNumber(value)
			if err != nil || n < 1 || n > 64<<20 {
				return opts, fmt.Errorf("invalid %s %q", key, value)
			}
			if key == "bs" || key == "ibs" {
				opts.inputBlock = n
			}
			if key == "bs" || key == "obs" {
				opts.outBlock = n
			}
		case "count", "skip", "seek":
			n, err := parseDDNumber(value)
			if err != nil || n < 0 {
				return opts, fmt.Errorf("invalid %s %q", key, value)
			}
			switch key {
			case "count":
				opts.count, opts.countSet = n, true
			case "skip":
				opts.skip = n
			case "seek":
				opts.seek = n
			}
		case "conv":
			for _, conversion := range strings.Split(value, ",") {
				switch conversion {
				case "notrunc":
					opts.noTrunc = true
				case "sync":
					opts.sync = true
				case "noerror":
					opts.noErr = true
				default:
					return opts, fmt.Errorf("unsupported conversion %q", conversion)
				}
			}
		case "status":
			if value != "none" && value != "noxfer" {
				return opts, fmt.Errorf("unsupported status %q", value)
			}
			opts.status = value
		default:
			return opts, fmt.Errorf("unrecognized operand %q", arg)
		}
	}
	if opts.skip > math.MaxInt64/opts.inputBlock || opts.seek > math.MaxInt64/opts.outBlock {
		return opts, fmt.Errorf("skip or seek offset is out of range")
	}
	return opts, nil
}

func parseDDNumber(value string) (int64, error) {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == 'x' || r == '*' })
	if len(parts) == 0 {
		return 0, fmt.Errorf("invalid number")
	}
	result := int64(1)
	for _, part := range parts {
		multiplier := int64(1)
		for _, suffix := range []struct {
			name  string
			value int64
		}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"GB", 1_000_000_000}, {"MB", 1_000_000}, {"kB", 1_000}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"k", 1 << 10}, {"b", 512}, {"w", 2}, {"c", 1}} {
			if strings.HasSuffix(part, suffix.name) {
				part = strings.TrimSuffix(part, suffix.name)
				multiplier = suffix.value
				break
			}
		}
		n, err := strconv.ParseInt(part, 0, 64)
		if err != nil || n < 0 || n > math.MaxInt64/multiplier {
			return 0, fmt.Errorf("invalid number")
		}
		factor := n * multiplier
		if factor != 0 && result > math.MaxInt64/factor {
			return 0, fmt.Errorf("invalid number")
		}
		result *= factor
	}
	return result, nil
}

func ddInput(name string) (io.Reader, func(), error) {
	if name == "-" {
		return os.Stdin, func() {}, nil
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, func() {}, fmt.Errorf("%s: %w", name, err)
	}
	return file, func() { _ = file.Close() }, nil
}

func ddOutput(opts ddOptions) (io.Writer, func(), error) {
	if opts.output == "-" {
		if opts.seek != 0 {
			return nil, func() {}, fmt.Errorf("cannot seek standard output")
		}
		return os.Stdout, func() {}, nil
	}
	flags := os.O_CREATE | os.O_WRONLY
	if !opts.noTrunc && opts.seek == 0 {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(opts.output, flags, 0o666) //nolint:gosec // dd follows the standard umask-controlled output mode.
	if err != nil {
		return nil, func() {}, fmt.Errorf("%s: %w", opts.output, err)
	}
	if opts.seek != 0 {
		if _, err := file.Seek(opts.seek*opts.outBlock, io.SeekStart); err != nil {
			_ = file.Close()
			return nil, func() {}, err
		}
	}
	return file, func() { _ = file.Close() }, nil
}

func skipInput(reader io.Reader, bytesToSkip int64) error {
	if seeker, ok := reader.(io.Seeker); ok {
		_, err := seeker.Seek(bytesToSkip, io.SeekCurrent)
		return err
	}
	_, err := io.CopyN(io.Discard, reader, bytesToSkip)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func cmdFile(args []string) int {
	brief, dereference, mimeType := false, false, false
	var files []string
	for _, arg := range args {
		switch arg {
		case "-b", "--brief":
			brief = true
		case "-L", "--dereference":
			dereference = true
		case "-i", "--mime", "--mime-type":
			mimeType = true
		default:
			files = append(files, arg)
		}
	}
	if len(files) == 0 {
		fatalf("file", "missing file operand")
		return 1
	}
	status := 0
	for _, name := range files {
		description, err := describeFile(name, dereference)
		if err != nil {
			description, status = "cannot open: "+err.Error(), 1
		} else if mimeType {
			description = mimeFor(description)
		}
		if brief {
			fmt.Println(description)
		} else {
			fmt.Printf("%s: %s\n", name, description)
		}
	}
	return status
}

func describeFile(name string, dereference bool) (string, error) {
	stat := os.Lstat
	if dereference {
		stat = os.Stat
	}
	info, err := stat(name)
	if err != nil {
		return "", err
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(name)
		if err != nil {
			return "symbolic link", nil
		}
		return "symbolic link to " + target, nil
	case mode.IsDir():
		var bits []string
		if mode&os.ModeSetuid != 0 {
			bits = append(bits, "setuid")
		}
		if mode&os.ModeSetgid != 0 {
			bits = append(bits, "setgid")
		}
		if mode&os.ModeSticky != 0 {
			bits = append(bits, "sticky")
		}
		bits = append(bits, "directory")
		return strings.Join(bits, ", "), nil
	case mode&os.ModeNamedPipe != 0:
		return "fifo (named pipe)", nil
	case mode&os.ModeSocket != 0:
		return "socket", nil
	case mode&os.ModeDevice != 0:
		kind := "block special"
		if mode&os.ModeCharDevice != 0 {
			kind = "character special"
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			major, minor := linuxDeviceMajorMinor(st.Rdev)
			kind += fmt.Sprintf(" (%d/%d)", major, minor)
		}
		return kind, nil
	case !mode.IsRegular():
		return "special file", nil
	}
	if kind, label, _, probeErr := probeFilesystem(name); probeErr == nil && kind != "" {
		description := kind + " filesystem data"
		if label != "" {
			description += ", label " + strconv.Quote(label)
		}
		return description, nil
	}
	if elfDescription, ok := describeELF(name); ok {
		return elfDescription, nil
	}
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data := make([]byte, 8192)
	n, err := file.Read(data)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return describeData(data[:n], mode), nil
}

// mimeFor renders the media type -i/--mime prints, given the description
// describeFile already produced. It covers the descriptions this file
// applet can actually emit, plus GNU file's charset suffix.
func mimeFor(description string) string {
	charset := "us-ascii"
	mimeType := "application/octet-stream"
	switch {
	case description == "empty":
		mimeType = "inode/x-empty"
	case strings.HasPrefix(description, "ELF"):
		mimeType, charset = "application/x-executable", "binary"
		switch {
		case strings.Contains(description, "pie executable"):
			mimeType = "application/x-pie-executable"
		case strings.Contains(description, "shared object"):
			mimeType = "application/x-sharedlib"
		case strings.Contains(description, "relocatable"):
			mimeType = "application/x-object"
		}
	case strings.HasPrefix(description, "script, interpreter"):
		mimeType = "text/x-shellscript"
	case description == "gzip compressed data":
		mimeType, charset = "application/gzip", "binary"
	case description == "Zip archive data":
		mimeType, charset = "application/zip", "binary"
	case description == "PNG image data":
		mimeType, charset = "image/png", "binary"
	case description == "JPEG image data":
		mimeType, charset = "image/jpeg", "binary"
	case description == "PDF document":
		mimeType, charset = "application/pdf", "binary"
	case description == "POSIX tar archive":
		mimeType, charset = "application/x-tar", "binary"
	case strings.HasSuffix(description, "directory"):
		return "inode/directory; charset=binary"
	case description == "symbolic link" || strings.HasPrefix(description, "symbolic link to "):
		return "inode/symlink; charset=binary"
	case description == "fifo (named pipe)":
		return "inode/fifo; charset=binary"
	case description == "socket":
		return "inode/socket; charset=binary"
	case strings.HasPrefix(description, "character special"):
		return "inode/chardevice; charset=binary"
	case strings.HasPrefix(description, "block special"):
		return "inode/blockdevice; charset=binary"
	case description == "ASCII text":
		mimeType = "text/plain"
	case strings.HasPrefix(description, "Unicode text"):
		mimeType, charset = "text/plain", "utf-8"
	case description == "data":
		charset = "binary"
	default:
		charset = "binary"
	}
	return fmt.Sprintf("%s; charset=%s", mimeType, charset)
}

// describeELF renders an ELF file the way real file(1) does: class, data
// encoding, type (with the pie-executable/shared-object distinction based on
// PT_INTERP), architecture, ABI, link mode, GNU/Go build IDs, the GNU
// ABI-tag note, and stripped state. ok is false for a non-ELF or unreadable
// file, so the caller falls back to the ordinary magic-byte probe.
func describeELF(name string) (string, bool) {
	f, err := elf.Open(name)
	if err != nil {
		return "", false
	}
	defer f.Close()

	class := "32-bit"
	if f.Class == elf.ELFCLASS64 {
		class = "64-bit"
	}
	dataEnc := "MSB"
	if f.Data == elf.ELFDATA2LSB {
		dataEnc = "LSB"
	}

	hasInterp, hasDynamic, interpPath := false, false, ""
	for _, p := range f.Progs {
		switch p.Type {
		case elf.PT_INTERP:
			hasInterp = true
			if raw, readErr := io.ReadAll(p.Open()); readErr == nil {
				interpPath = string(bytes.TrimRight(raw, "\x00"))
			}
		case elf.PT_DYNAMIC:
			hasDynamic = true
		}
	}

	typeStr := "object"
	switch f.Type {
	case elf.ET_REL:
		typeStr = "relocatable"
	case elf.ET_EXEC:
		typeStr = "executable"
	case elf.ET_DYN:
		if hasInterp {
			typeStr = "pie executable"
		} else {
			typeStr = "shared object"
		}
	case elf.ET_CORE:
		typeStr = "core file"
	}

	osabi := "SYSV"
	if f.OSABI == elf.ELFOSABI_LINUX {
		osabi = "GNU/Linux"
	}

	parts := []string{
		fmt.Sprintf("ELF %s %s %s", class, dataEnc, typeStr),
		elfMachineName(f.Machine),
		fmt.Sprintf("version 1 (%s)", osabi),
	}
	if hasDynamic {
		parts = append(parts, "dynamically linked")
		if hasInterp {
			parts = append(parts, "interpreter "+interpPath)
		}
	} else {
		parts = append(parts, "statically linked")
	}

	notes := elfNotes(f)
	if goBuildID, ok := notes["Go\x00\x00"][4]; ok {
		parts = append(parts, "Go BuildID="+string(goBuildID))
	}
	if buildID, ok := notes["GNU\x00"][3]; ok {
		label := "unknown"
		switch len(buildID) {
		case 20:
			label = "sha1"
		case 16:
			label = "md5"
		case 8:
			label = "uuid"
		}
		parts = append(parts, fmt.Sprintf("BuildID[%s]=%s", label, hex.EncodeToString(buildID)))
	}
	if abiTag, ok := notes["GNU\x00"][1]; ok && len(abiTag) >= 16 {
		osID := f.ByteOrder.Uint32(abiTag[0:4])
		major := f.ByteOrder.Uint32(abiTag[4:8])
		minor := f.ByteOrder.Uint32(abiTag[8:12])
		sub := f.ByteOrder.Uint32(abiTag[12:16])
		osName := "Linux"
		switch osID {
		case 1:
			osName = "Hurd"
		case 2:
			osName = "Solaris"
		case 3:
			osName = "FreeBSD"
		}
		parts = append(parts, fmt.Sprintf("for GNU/%s %d.%d.%d", osName, major, minor, sub))
	}

	hasSymtab, hasDebugInfo := false, false
	for _, s := range f.Sections {
		switch s.Name {
		case ".symtab":
			hasSymtab = true
		case ".debug_info":
			hasDebugInfo = true
		}
	}
	if hasDebugInfo {
		parts = append(parts, "with debug_info")
	}
	if hasSymtab {
		parts = append(parts, "not stripped")
	} else {
		parts = append(parts, "stripped")
	}
	return strings.Join(parts, ", "), true
}

// elfMachineName maps the architectures this project is realistically built
// for or likely to encounter; anything else falls back to its numeric value
// rather than guessing a name.
func elfMachineName(m elf.Machine) string {
	switch m {
	case elf.EM_X86_64:
		return "x86-64"
	case elf.EM_386:
		return "Intel 80386"
	case elf.EM_ARM:
		return "ARM"
	case elf.EM_AARCH64:
		return "ARM aarch64"
	case elf.EM_RISCV:
		return "RISC-V"
	case elf.EM_MIPS:
		return "MIPS"
	case elf.EM_PPC64:
		return "PowerPC64"
	default:
		return fmt.Sprintf("unknown architecture 0x%x", uint16(m))
	}
}

// elfNotes collects every ELF note this file has, from both SHT_NOTE
// sections and PT_NOTE segments (a fully stripped binary can lack section
// headers but keeps the segments), keyed by [name][type] -> descriptor
// bytes. The note format itself -- namesz/descsz/type then 4-byte-aligned
// name and descriptor -- is the same for 32- and 64-bit ELF alike.
func elfNotes(f *elf.File) map[string]map[uint32][]byte {
	notes := map[string]map[uint32][]byte{}
	add := func(data []byte) {
		for len(data) >= 12 {
			nameSz := f.ByteOrder.Uint32(data[0:4])
			descSz := f.ByteOrder.Uint32(data[4:8])
			noteType := f.ByteOrder.Uint32(data[8:12])
			off := 12
			nameEnd := off + int(nameSz)
			if nameEnd > len(data) {
				return
			}
			name := string(data[off:nameEnd])
			off = align4(nameEnd)
			descEnd := off + int(descSz)
			if descEnd > len(data) {
				return
			}
			desc := data[off:descEnd]
			if notes[name] == nil {
				notes[name] = map[uint32][]byte{}
			}
			notes[name][noteType] = desc
			data = data[align4(descEnd):]
		}
	}
	for _, s := range f.Sections {
		if s.Type == elf.SHT_NOTE {
			if data, err := s.Data(); err == nil {
				add(data)
			}
		}
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_NOTE {
			if data, err := io.ReadAll(p.Open()); err == nil {
				add(data)
			}
		}
	}
	return notes
}

func align4(n int) int {
	return (n + 3) &^ 3
}

func describeData(data []byte, mode os.FileMode) string {
	switch {
	case len(data) == 0:
		return "empty"
	case bytes.HasPrefix(data, []byte{0x7f, 'E', 'L', 'F'}):
		// Only reached when describeELF's full parse (via debug/elf, which
		// needs random file access) failed on an otherwise ELF-looking file.
		bits := "32-bit"
		if len(data) > 4 && data[4] == 2 {
			bits = "64-bit"
		}
		return "ELF " + bits + " executable or object"
	case bytes.HasPrefix(data, []byte("#!")):
		line := strings.TrimSpace(strings.SplitN(string(data[2:]), "\n", 2)[0])
		return "script, interpreter " + line
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		return "gzip compressed data"
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return "Zip archive data"
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "PNG image data"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "JPEG image data"
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return "PDF document"
	case len(data) > 262 && string(data[257:262]) == "ustar":
		return "POSIX tar archive"
	}
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		text := "ASCII text"
		if !isASCII(data) {
			text = "Unicode text, UTF-8 text"
		}
		if mode&0o111 != 0 {
			return text + ", executable"
		}
		return text
	}
	return "data"
}

func isASCII(data []byte) bool {
	for _, b := range data {
		if b >= 0x80 {
			return false
		}
	}
	return true
}

func cmdMktemp(args []string) int {
	directory, baseDirectory := false, ""
	var template string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--directory":
			directory = true
		case "-p", "--tmpdir":
			i++
			if i >= len(args) {
				fatalf("mktemp", "-p requires a directory")
				return 1
			}
			baseDirectory = args[i]
		case "-t":
			if baseDirectory == "" {
				baseDirectory = os.TempDir()
			}
		case "--":
			if i+1 < len(args) {
				template = args[i+1]
				i++
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				fatalf("mktemp", "unsupported option %q", args[i])
				return 1
			}
			if template != "" {
				fatalf("mktemp", "extra operand %q", args[i])
				return 1
			}
			template = args[i]
		}
	}
	if template == "" {
		template = "tmp.XXXXXXXXXX"
		if baseDirectory == "" {
			baseDirectory = os.TempDir()
		}
	}
	prefix, placeholders, suffix, err := mktempPattern(baseDirectory, template)
	if err != nil {
		fatalf("mktemp", "%v", err)
		return 1
	}
	name, err := createTemp(prefix, suffix, placeholders, directory)
	if err != nil {
		kind := "file"
		if directory {
			kind = "directory"
		}
		fatalf("mktemp", "failed to create %s via template '%s%s%s': %s",
			kind, prefix, strings.Repeat("X", placeholders), suffix, errText(err))
		return 1
	}
	fmt.Println(name)
	return 0
}

// mktempPattern splits a template into the text before the run of X's, the
// number of X's, and the text after. mktemp(1) replaces exactly those X's, so
// the count has to survive into the name that is created.
func mktempPattern(baseDirectory, template string) (string, int, string, error) {
	dir, name := filepath.Dir(template), filepath.Base(template)
	switch {
	case baseDirectory == "":
		// The template carries its own directory, if any.
		if dir == "." && !strings.HasPrefix(template, "./") {
			dir = ""
		}
	case strings.HasSuffix(baseDirectory, "/"):
		dir = strings.TrimSuffix(baseDirectory, "/")
	default:
		dir = baseDirectory
	}
	end := strings.LastIndex(name, "X") + 1
	start := end
	for start > 0 && name[start-1] == 'X' {
		start--
	}
	if end-start < 3 {
		return "", 0, "", fmt.Errorf("too few X's in template '%s'", template)
	}
	prefix := name[:start]
	if dir != "" {
		prefix = dir + "/" + prefix
	}
	return prefix, end - start, name[end:], nil
}

// tempNameAlphabet is the character set mkstemp(3) draws from.
const tempNameAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// createTemp creates a file or directory whose name is prefix, exactly count
// random characters, then suffix. os.CreateTemp cannot do this: it substitutes
// a decimal number of whatever length it needs, so "XXXX" would become a name
// like "3160348960" that no longer matches the template.
func createTemp(prefix, suffix string, count int, directory bool) (string, error) {
	random := make([]byte, count)
	for attempt := 0; ; attempt++ {
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		name := make([]byte, count)
		for i, b := range random {
			name[i] = tempNameAlphabet[int(b)%len(tempNameAlphabet)]
		}
		path := prefix + string(name) + suffix
		var err error
		if directory {
			err = os.Mkdir(path, 0o700)
		} else {
			var file *os.File
			if file, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
				err = file.Close()
			}
		}
		if err == nil {
			return path, nil
		}
		// Collisions are expected with a short template; anything else is real.
		if !errors.Is(err, os.ErrExist) || attempt >= 100 {
			return "", err
		}
	}
}

func cmdTimeout(args []string) int {
	signal, killAfter := syscall.SIGTERM, time.Duration(0)
	for len(args) > 0 {
		switch {
		case args[0] == "-s" || args[0] == "--signal":
			if len(args) < 2 {
				fatalf("timeout", "%s requires an argument", args[0])
				return 125
			}
			parsed, err := parseSignal(args[1])
			if err != nil {
				fatalf("timeout", "%v", err)
				return 125
			}
			signal, args = parsed, args[2:]
		case args[0] == "-k" || args[0] == "--kill-after":
			if len(args) < 2 {
				return 125
			}
			parsed, err := commandDuration(args[1])
			if err != nil {
				fatalf("timeout", "invalid duration %q", args[1])
				return 125
			}
			killAfter, args = parsed, args[2:]
		case args[0] == "--":
			args = args[1:]
			goto parsed
		case strings.HasPrefix(args[0], "-"):
			fatalf("timeout", "unsupported option %q", args[0])
			return 125
		default:
			goto parsed
		}
	}
parsed:
	if len(args) < 2 {
		fatalf("timeout", "missing duration or command")
		return 125
	}
	duration, err := commandDuration(args[0])
	if err != nil {
		fatalf("timeout", "invalid duration %q", args[0])
		return 125
	}
	command := exec.Command(args[1], args[2:]...) //nolint:gosec // G204: timeout intentionally runs the selected command.
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		fatalf("timeout", "%v", err)
		if errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	if duration == 0 {
		if err := <-done; err != nil {
			return commandStatus("timeout", err)
		}
		return 0
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return commandStatus("timeout", err)
		}
		return 0
	case <-timer.C:
		_ = syscall.Kill(-command.Process.Pid, signal)
	}
	if killAfter > 0 {
		killTimer := time.NewTimer(killAfter)
		select {
		case <-done:
			killTimer.Stop()
		case <-killTimer.C:
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			<-done
		}
	} else {
		<-done
	}
	return 124
}

func commandDuration(value string) (time.Duration, error) {
	seconds, err := parseSleepDuration(value)
	if err != nil || seconds > float64(math.MaxInt64)/float64(time.Second) {
		return 0, fmt.Errorf("invalid duration")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
