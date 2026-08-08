// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	cpioNewcMagic     = "070701"
	cpioHeaderSize    = 110
	cpioMaxNameBytes  = 64 << 10
	cpioModeTypeMask  = 0o170000
	cpioModeRegular   = 0o100000
	cpioModeDirectory = 0o040000
	cpioModeSymlink   = 0o120000
	maxCpioFieldValue = uint64(1<<32 - 1)
	maxCpioReadSize   = uint64(1<<63 - 1)
)

type cpioOptions struct {
	operation byte
	archive   string
	outputDir string
	verbose   bool
}

func cmdCpio(args []string) int {
	opts, err := parseCpioOptions(args)
	if err != nil {
		fatalf("cpio", "%v", err)
		return 1
	}
	switch opts.operation {
	case 'o':
		err = createCpio(opts)
	case 'i', 't':
		err = readCpio(opts)
	}
	if err != nil {
		fatalf("cpio", "%v", err)
		return 1
	}
	return 0
}

func parseCpioOptions(args []string) (cpioOptions, error) {
	opts := cpioOptions{archive: "-", outputDir: "."}
	args = expandShortOptions(args, "FH")
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 != len(args) {
				return opts, fmt.Errorf("unexpected operand %q", args[i+1])
			}
			break
		}
		switch arg {
		case "-o", "--create":
			if opts.operation != 0 && opts.operation != 'o' {
				return opts, fmt.Errorf("multiple operations specified")
			}
			opts.operation = 'o'
		case "-i", "--extract":
			if opts.operation != 0 && opts.operation != 'i' && opts.operation != 't' {
				return opts, fmt.Errorf("multiple operations specified")
			}
			if opts.operation == 0 {
				opts.operation = 'i'
			}
		case "-t", "--list":
			if opts.operation == 'o' {
				return opts, fmt.Errorf("multiple operations specified")
			}
			opts.operation = 't'
		case "-v", "--verbose":
			opts.verbose = true
		case "-F", "--file":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("option %s requires an argument", arg)
			}
			opts.archive = args[i]
		case "-H", "--format":
			i++
			if i >= len(args) || args[i] != "newc" {
				return opts, fmt.Errorf("only the newc format is supported")
			}
		case "-d", "--make-directories":
			// Extraction creates safe parent directories unconditionally.
		default:
			return opts, fmt.Errorf("unsupported option %q", arg)
		}
	}
	if opts.operation == 0 {
		return opts, fmt.Errorf("one of -o, -i, or -t is required")
	}
	return opts, nil
}

func createCpio(opts cpioOptions) (retErr error) {
	paths, err := cpioInputPaths(os.Stdin)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no input paths")
	}
	if opts.archive != "-" {
		if err := validateTarCreateSources(tarOptions{archive: opts.archive, directory: ".", files: paths}, "."); err != nil {
			return err
		}
	}
	output, closeOutput, err := createArchiveOutput(opts.archive)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeOutput(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	for _, path := range paths {
		if err := writeCpioPath(output, path, cpioMemberName(path), opts.verbose); err != nil {
			return err
		}
	}
	return writeCpioHeader(output, cpioHeader{name: "TRAILER!!!"})
}

func cpioInputPaths(input io.Reader) ([]string, error) {
	scanner := newLineScanner(input)
	paths := []string{}
	for scanner.Scan() {
		path := strings.TrimSuffix(scanner.Text(), "\r")
		if path == "" {
			continue
		}
		paths = append(paths, path)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return paths, nil
}

func cpioMemberName(path string) string {
	return zipMemberName(path)
}

type cpioHeader struct {
	ino, mode, uid, gid, nlink, mtime, size  uint32
	devMajor, devMinor, rdevMajor, rdevMinor uint32
	name                                     string
}

func writeCpioPath(output io.Writer, source, name string, verbose bool) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read metadata for %q", source)
	}
	ino, err := cpioUint32(stat.Ino, "inode number")
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	nlink, err := cpioUint32(stat.Nlink, "link count")
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	mtime, err := cpioInt64ToUint32(info.ModTime().Unix(), "modification time")
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	device := stat.Dev
	devMajor, err := cpioUint32((device>>8)&0xfff, "device major number")
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	devMinor, err := cpioUint32((device&0xff)|((device>>12)&0xfff00), "device minor number")
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	header := cpioHeader{ino: ino, mode: stat.Mode, uid: stat.Uid, gid: stat.Gid, nlink: nlink,
		mtime: mtime, devMajor: devMajor, devMinor: devMinor, name: name}
	var data io.Reader
	var closeData func() error
	switch {
	case info.IsDir():
		data = strings.NewReader("")
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		data = strings.NewReader(target)
		header.size, err = cpioIntToUint32(len(target), "symbolic link target length")
		if err != nil {
			return err
		}
	case info.Mode().IsRegular():
		header.size, err = cpioInt64ToUint32(info.Size(), "file size")
		if err != nil {
			return fmt.Errorf("file is too large for newc: %q", source)
		}
		file, err := os.Open(source) //nolint:gosec // cpio reads a selected path after lstat validation.
		if err != nil {
			return err
		}
		opened, statErr := file.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			file.Close()
			return fmt.Errorf("file changed while archiving: %s", source)
		}
		data, closeData = file, file.Close
	default:
		return fmt.Errorf("unsupported file type for %q", source)
	}
	if closeData != nil {
		defer func() { _ = closeData() }()
	}
	if err := writeCpioHeader(output, header); err != nil {
		return err
	}
	if header.size > 0 {
		if _, err := io.CopyN(output, data, int64(header.size)); err != nil {
			return err
		}
	}
	if err := writeCpioPadding(output, uint64(header.size)); err != nil {
		return err
	}
	if verbose {
		_, err := fmt.Fprintln(os.Stderr, name)
		return err
	}
	return nil
}

func writeCpioHeader(output io.Writer, header cpioHeader) error {
	if header.name == "" || len(header.name)+1 > cpioMaxNameBytes {
		return fmt.Errorf("invalid cpio member name")
	}
	nameSize, err := cpioIntToUint32(len(header.name)+1, "member name length")
	if err != nil {
		return err
	}
	values := []uint32{header.ino, header.mode, header.uid, header.gid, header.nlink, header.mtime, header.size,
		header.devMajor, header.devMinor, header.rdevMajor, header.rdevMinor, nameSize, 0}
	var text strings.Builder
	text.Grow(cpioHeaderSize)
	text.WriteString(cpioNewcMagic)
	for _, value := range values {
		fmt.Fprintf(&text, "%08X", value)
	}
	if text.Len() != cpioHeaderSize {
		return fmt.Errorf("internal cpio header error")
	}
	if _, err := io.WriteString(output, text.String()); err != nil {
		return err
	}
	if _, err := io.WriteString(output, header.name); err != nil {
		return err
	}
	if _, err := output.Write([]byte{0}); err != nil {
		return err
	}
	return writeCpioPadding(output, uint64(cpioHeaderSize+len(header.name)+1))
}

func writeCpioPadding(output io.Writer, size uint64) error {
	padding := (4 - size%4) % 4
	if padding == 0 {
		return nil
	}
	_, err := output.Write(make([]byte, padding))
	return err
}

func readCpio(opts cpioOptions) error {
	input, err := openInput(opts.archive)
	if err != nil {
		return err
	}
	defer input.Close()
	reader := bufio.NewReader(input)
	root := ""
	if opts.operation == 'i' {
		root, err = filepath.Abs(opts.outputDir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(root, 0o755); err != nil { //nolint:gosec // extraction root follows conventional permissions.
			return err
		}
	}
	var directories []cpioDirectory
	var extracted uint64
	for {
		header, err := readCpioHeader(reader)
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("missing cpio trailer")
		}
		if err != nil {
			return err
		}
		if header.name == "TRAILER!!!" {
			if header.size != 0 {
				return fmt.Errorf("invalid cpio trailer")
			}
			break
		}
		if uint64(header.size) > uint64(maxExpandedArchiveBytes)-extracted {
			return fmt.Errorf("archive exceeds the 64 GiB extraction limit")
		}
		if opts.operation == 't' {
			if _, err := fmt.Fprintln(os.Stdout, header.name); err != nil {
				return err
			}
			if err := skipCpioData(reader, uint64(header.size)); err != nil {
				return err
			}
			continue
		}
		target, err := safeTarTarget(root, header.name)
		if err != nil {
			return err
		}
		mode := fileModeFromOctal(uint64(header.mode & 0o7777))
		switch header.mode & cpioModeTypeMask {
		case cpioModeDirectory:
			if header.size != 0 {
				return fmt.Errorf("directory member has data: %q", header.name)
			}
			if err := ensureTarParents(root, target); err != nil {
				return err
			}
			if info, statErr := os.Lstat(target); statErr == nil && !info.IsDir() {
				return fmt.Errorf("refusing to replace non-directory path %q", header.name)
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			directories = append(directories, cpioDirectory{path: target, mode: mode, modified: time.Unix(int64(header.mtime), 0)})
		case cpioModeRegular:
			if err := extractArchiveRegular(reader, root, target, &archiveRegularHeader{size: uint64(header.size), modified: time.Unix(int64(header.mtime), 0)}, mode); err != nil {
				return err
			}
			extracted += uint64(header.size)
		case cpioModeSymlink:
			if err := extractCpioSymlink(reader, root, target, header); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archive member type for %q", header.name)
		}
		if err := skipCpioPadding(reader, uint64(header.size)); err != nil {
			return err
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Chmod(directories[index].path, directories[index].mode); err != nil {
			return err
		}
		if err := os.Chtimes(directories[index].path, directories[index].modified, directories[index].modified); err != nil {
			return err
		}
	}
	return nil
}

type cpioDirectory struct {
	path     string
	mode     os.FileMode
	modified time.Time
}

func readCpioHeader(reader io.Reader) (cpioHeader, error) {
	var header cpioHeader
	text := make([]byte, cpioHeaderSize)
	if _, err := io.ReadFull(reader, text); err != nil {
		return header, err
	}
	if string(text[:6]) != cpioNewcMagic {
		return header, fmt.Errorf("unsupported cpio format")
	}
	values := make([]uint32, 13)
	for index := range values {
		value, err := strconv.ParseUint(string(text[6+index*8:14+index*8]), 16, 32)
		if err != nil {
			return header, fmt.Errorf("invalid cpio header")
		}
		values[index] = uint32(value)
	}
	header = cpioHeader{ino: values[0], mode: values[1], uid: values[2], gid: values[3], nlink: values[4], mtime: values[5], size: values[6],
		devMajor: values[7], devMinor: values[8], rdevMajor: values[9], rdevMinor: values[10]}
	nameSize := values[11]
	if nameSize == 0 || nameSize > cpioMaxNameBytes {
		return header, fmt.Errorf("invalid cpio member name length")
	}
	name := make([]byte, nameSize)
	if _, err := io.ReadFull(reader, name); err != nil {
		return header, err
	}
	if name[len(name)-1] != 0 {
		return header, fmt.Errorf("unterminated cpio member name")
	}
	header.name = string(name[:len(name)-1])
	if header.name == "" || strings.ContainsRune(header.name, '\x00') {
		return header, fmt.Errorf("invalid cpio member name")
	}
	if err := skipCpioPadding(reader, uint64(cpioHeaderSize+nameSize)); err != nil {
		return header, err
	}
	return header, nil
}

func skipCpioData(reader io.Reader, size uint64) error {
	if size > maxCpioReadSize {
		return fmt.Errorf("cpio member is too large")
	}
	if _, err := io.CopyN(io.Discard, reader, int64(size)); err != nil {
		return err
	}
	return skipCpioPadding(reader, size)
}

func cpioUint32(value uint64, field string) (uint32, error) {
	if value > maxCpioFieldValue {
		return 0, fmt.Errorf("%s exceeds the newc format limit", field)
	}
	return uint32(value), nil //nolint:gosec // The bound check above limits value to 32 bits.
}

func cpioInt64ToUint32(value int64, field string) (uint32, error) {
	if value < 0 {
		return 0, fmt.Errorf("%s is negative", field)
	}
	return cpioUint32(uint64(value), field) //nolint:gosec // Negative values are rejected above.
}

func cpioIntToUint32(value int, field string) (uint32, error) {
	if value < 0 {
		return 0, fmt.Errorf("%s is negative", field)
	}
	return cpioUint32(uint64(value), field) //nolint:gosec // Negative values are rejected above.
}

func skipCpioPadding(reader io.Reader, size uint64) error {
	padding := (4 - size%4) % 4
	if padding == 0 {
		return nil
	}
	_, err := io.CopyN(io.Discard, reader, int64(padding))
	return err
}

func extractCpioSymlink(reader io.Reader, root, target string, header cpioHeader) error {
	if header.size > 4096 {
		return fmt.Errorf("symbolic link target is too long for %q", header.name)
	}
	value := make([]byte, header.size)
	if _, err := io.ReadFull(reader, value); err != nil {
		return err
	}
	link := string(value)
	if err := validateTarSymlink(header.name, link); err != nil {
		return err
	}
	if err := ensureTarParents(root, target); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("refusing to replace existing path %q with a symbolic link", header.name)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(link, target)
}
