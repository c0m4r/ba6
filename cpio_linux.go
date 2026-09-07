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
	cpioCrcMagic      = "070702"
	cpioOdcMagic      = "070707"
	cpioBinMagic      = 0o070707
	cpioHeaderSize    = 110
	cpioOdcHeaderSize = 76
	cpioBinHeaderSize = 26
	cpioMaxNameBytes  = 64 << 10
	cpioModeTypeMask  = 0o170000
	cpioModeRegular   = 0o100000
	cpioModeDirectory = 0o040000
	cpioModeSymlink   = 0o120000
	cpioTrailer       = "TRAILER!!!"
	maxCpioFieldValue = uint64(1<<32 - 1)
	maxCpioReadSize   = uint64(1<<63 - 1)
)

// cpioFormat is one of the archive layouts. The two hex formats share a
// header and differ only in whether the last field carries a checksum; odc is
// the POSIX octal one and bin the host-endian binary one.
type cpioFormat int

const (
	cpioFormatNewc cpioFormat = iota
	cpioFormatCrc
	cpioFormatOdc
	cpioFormatBin
)

func (f cpioFormat) String() string {
	switch f {
	case cpioFormatCrc:
		return "crc"
	case cpioFormatOdc:
		return "odc"
	case cpioFormatBin:
		return "bin"
	default:
		return "newc"
	}
}

type cpioOptions struct {
	operation   byte
	format      cpioFormat
	archive     string
	outputDir   string
	blockSize   uint64
	verbose     bool
	quiet       bool
	makeDirs    bool
	unlink      bool
	keepTimes   bool
	toStdout    bool
	nullNames   bool
	dereference bool
	nonMatching bool
	link        bool
	absolute    bool
	patterns    []string
}

// cpioUsage is the wording the original ends a rejected command line with. Its
// own option mistakes exit 64 while everything else exits 2.
func cpioUsage(status int, format string, a ...interface{}) int {
	fatalf("cpio", format, a...)
	fmt.Fprintln(os.Stderr, "Try 'cpio --help' or 'cpio --usage' for more information.")
	return status
}

func cmdCpio(args []string) int {
	opts, status, ok := parseCpioOptions(args)
	if !ok {
		return status
	}
	var err error
	switch opts.operation {
	case 'o':
		err = createCpio(&opts)
	case 'i', 't':
		err = readCpio(&opts)
	case 'p':
		err = passCpio(&opts)
	}
	if err != nil {
		fatalf("cpio", "%v", err)
		return 2
	}
	return 0
}

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func parseCpioOptions(args []string) (cpioOptions, int, bool) {
	opts := cpioOptions{archive: "-", outputDir: ".", blockSize: 512}
	setOperation := func(op byte) bool {
		if opts.operation != 0 && opts.operation != op {
			cpioUsage(2, "Mode already defined")
			return false
		}
		opts.operation = op
		return true
	}
	var operands []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		name, value, hasValue := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
		}
		needValue := func(letter string) (string, bool) {
			if hasValue {
				return value, true
			}
			index++
			if index >= len(args) {
				cpioUsage(64, "option requires an argument -- '%s'", letter)
				return "", false
			}
			return args[index], true
		}
		switch name {
		case "-o", "--create":
			if !setOperation('o') {
				return opts, 2, false
			}
		case "-i", "--extract":
			if opts.operation == 't' {
				break
			}
			if !setOperation('i') {
				return opts, 2, false
			}
		case "-t", "--list":
			if opts.operation == 'o' || opts.operation == 'p' {
				cpioUsage(2, "Mode already defined")
				return opts, 2, false
			}
			opts.operation = 't'
		case "-p", "--pass-through":
			if !setOperation('p') {
				return opts, 2, false
			}
		case "-v", "--verbose":
			opts.verbose = true
		case "--quiet":
			opts.quiet = true
		case "-F", "--file", "-I", "-O":
			text, ok := needValue(strings.TrimLeft(name, "-"))
			if !ok {
				return opts, 64, false
			}
			opts.archive = text
		case "-H", "--format":
			text, ok := needValue("H")
			if !ok {
				return opts, 64, false
			}
			format, valid := parseCpioFormat(text)
			if !valid {
				fatalf("cpio", "invalid archive format `%s'; valid formats are:", text)
				fmt.Fprintln(os.Stderr, "crc newc odc bin ustar tar (all-caps also recognized)")
				fmt.Fprintln(os.Stderr, "Try 'cpio --help' or 'cpio --usage' for more information.")
				return opts, 2, false
			}
			opts.format = format
		case "-c":
			// The historical spelling of the portable ASCII format.
			opts.format = cpioFormatOdc
		case "-B":
			opts.blockSize = 5120
		case "-C", "--io-size", "--block-size":
			text, ok := needValue("C")
			if !ok {
				return opts, 64, false
			}
			size, err := strconv.ParseUint(text, 10, 32)
			if err != nil || size == 0 {
				return opts, cpioUsage(2, "invalid block size"), false
			}
			if name == "--block-size" {
				size *= 512
			}
			opts.blockSize = size
		case "-d", "--make-directories":
			opts.makeDirs = true
		case "-u", "--unconditional":
			opts.unlink = true
		case "-m", "--preserve-modification-time":
			opts.keepTimes = true
		case "--to-stdout":
			opts.toStdout = true
		case "-0", "--null":
			opts.nullNames = true
		case "-L", "--dereference":
			opts.dereference = true
		case "-f", "--nonmatching":
			opts.nonMatching = true
		case "-l", "--link":
			opts.link = true
		case "--absolute-filenames":
			opts.absolute = true
		case "--no-absolute-filenames":
			opts.absolute = false
		case "-E", "--pattern-file":
			text, ok := needValue("E")
			if !ok {
				return opts, 64, false
			}
			patterns, err := cpioPatternFile(text)
			if err != nil {
				fatalf("cpio", "%v", err)
				return opts, 2, false
			}
			opts.patterns = append(opts.patterns, patterns...)
		case "-a", "--reset-access-time", "-R", "--owner", "--no-preserve-owner", "-V", "--dot":
			// Access times are not restored here and ownership cannot be
			// changed without privilege, so these are accepted and ignored.
			if name == "-R" || name == "--owner" {
				if _, ok := needValue("R"); !ok {
					return opts, 64, false
				}
			}
		case "--":
			operands = append(operands, args[index+1:]...)
			index = len(args)
		default:
			if len(arg) > 1 && arg[0] == '-' {
				if strings.HasPrefix(arg, "--") {
					return opts, cpioUsage(64, "unrecognized option '%s'", arg), false
				}
				if len(arg) > 2 {
					// Short options cluster, and the last one may take the
					// rest of the word as its argument.
					args = append(args[:index+1], append(cpioSplitCluster(arg), args[index+1:]...)...)
					args[index] = arg[:2]
					index--
					continue
				}
				return opts, cpioUsage(64, "invalid option -- '%c'", arg[1]), false
			}
			operands = append(operands, arg)
		}
	}
	switch opts.operation {
	case 0:
		return opts, cpioUsage(2, "You must specify one of -oipt options."), false
	case 'p':
		if len(operands) != 1 {
			return opts, cpioUsage(2, "Must specify a single directory with -p"), false
		}
		opts.outputDir = operands[0]
	case 'i', 't':
		opts.patterns = append(opts.patterns, operands...)
	default:
		if len(operands) != 0 {
			return opts, cpioUsage(2, "Too many arguments"), false
		}
	}
	return opts, 0, true
}

// cpioSplitCluster turns the tail of a bundled short option word back into
// separate words, so that "-ov" reads as "-o -v".
func cpioSplitCluster(arg string) []string {
	rest := make([]string, 0, len(arg)-2)
	for _, letter := range arg[2:] {
		rest = append(rest, "-"+string(letter))
	}
	return rest
}

func parseCpioFormat(text string) (cpioFormat, bool) {
	switch strings.ToLower(text) {
	case "newc":
		return cpioFormatNewc, true
	case "crc":
		return cpioFormatCrc, true
	case "odc":
		return cpioFormatOdc, true
	case "bin":
		return cpioFormatBin, true
	}
	return cpioFormatNewc, false
}

func cpioPatternFile(path string) ([]string, error) {
	file, err := os.Open(path) //nolint:gosec // The pattern file is named on the command line.
	if err != nil {
		return nil, fmt.Errorf("Cannot open %s: %v", path, errText(err)) //nolint:staticcheck // The original's wording starts with a capital.
	}
	defer file.Close()
	return cpioInputNames(file, false)
}

// cpioCounter tallies the bytes that pass through so that the block count can
// be reported the way the original does.
type cpioCounter struct {
	writer io.Writer
	reader io.Reader
	count  uint64
}

func (c *cpioCounter) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	c.count += uint64(n) //nolint:gosec // A write length is never negative.
	return n, err
}

func (c *cpioCounter) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.count += uint64(n) //nolint:gosec // A read length is never negative.
	return n, err
}

// reportBlocks prints the trailing "N blocks" line, which counts whole blocks
// of the chosen size and rounds up.
func (c *cpioCounter) reportBlocks(opts *cpioOptions) {
	if opts.quiet {
		return
	}
	blocks := (c.count + opts.blockSize - 1) / opts.blockSize
	if blocks == 1 {
		fmt.Fprintln(os.Stderr, "1 block")
		return
	}
	fmt.Fprintf(os.Stderr, "%d blocks\n", blocks)
}

// cpioInputNames reads the list of paths to archive, one per line or one per
// NUL byte with -0.
func cpioInputNames(input io.Reader, null bool) ([]string, error) {
	names := []string{}
	if null {
		data, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Split(string(data), "\x00") {
			if name != "" {
				names = append(names, name)
			}
		}
		return names, nil
	}
	scanner := newLineScanner(input)
	for scanner.Scan() {
		name := strings.TrimSuffix(scanner.Text(), "\r")
		if name != "" {
			names = append(names, name)
		}
	}
	return names, scanner.Err()
}

func cpioMemberName(path string) string {
	return zipMemberName(path)
}

type cpioHeader struct {
	ino, mode, uid, gid, nlink, mtime, size  uint64
	devMajor, devMinor, rdevMajor, rdevMinor uint64
	check                                    uint64
	name                                     string
}

func createCpio(opts *cpioOptions) (retErr error) {
	paths, err := cpioInputNames(os.Stdin, opts.nullNames)
	if err != nil {
		return err
	}
	if opts.archive != "-" {
		if err := validateTarCreateSources(&tarOptions{archive: opts.archive, directory: ".", files: paths}, "."); err != nil {
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
	counter := &cpioCounter{writer: output}
	failed := false
	for _, path := range paths {
		if err := writeCpioPath(counter, opts, path, cpioMemberName(path)); err != nil {
			fatalf("cpio", "%v", err)
			failed = true
		}
	}
	trailer := cpioHeader{name: cpioTrailer, nlink: 1}
	if err := writeCpioHeader(counter, opts.format, trailer); err != nil {
		return err
	}
	// The archive is rounded out to a whole block, which is what makes the
	// count the original prints match the file it leaves behind.
	if padding := (opts.blockSize - counter.count%opts.blockSize) % opts.blockSize; padding != 0 {
		if _, err := counter.Write(make([]byte, padding)); err != nil {
			return err
		}
	}
	counter.reportBlocks(opts)
	if failed {
		return errors.New("error exit delayed from previous errors")
	}
	return nil
}

func writeCpioPath(output io.Writer, opts *cpioOptions, source, name string) error {
	stat := func(path string) (os.FileInfo, error) {
		if opts.dereference {
			return os.Stat(path)
		}
		return os.Lstat(path)
	}
	info, err := stat(source)
	if err != nil {
		return fmt.Errorf("%s: Cannot stat: %v", source, errText(err))
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: Cannot stat", source)
	}
	device := sys.Dev
	header := cpioHeader{
		ino: sys.Ino, mode: uint64(sys.Mode), uid: uint64(sys.Uid), gid: uint64(sys.Gid),
		nlink: sys.Nlink, mtime: uint64(info.ModTime().Unix()), //nolint:gosec // Timestamps before 1970 are not archived.
		devMajor: (device >> 8) & 0xfff, devMinor: (device & 0xff) | ((device >> 12) & 0xfff00),
		rdevMajor: (sys.Rdev >> 8) & 0xfff, rdevMinor: (sys.Rdev & 0xff) | ((sys.Rdev >> 12) & 0xfff00),
		name: name,
	}
	var data []byte
	var file *os.File
	switch {
	case info.IsDir():
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(source)
		if err != nil {
			return fmt.Errorf("%s: Cannot readlink: %v", source, errText(err))
		}
		data, header.size = []byte(target), uint64(len(target))
	case info.Mode().IsRegular():
		header.size = uint64(info.Size()) //nolint:gosec // Regular file sizes are nonnegative.
		if header.size > maxCpioFieldValue {
			return fmt.Errorf("%s: file too large for the %s format", source, opts.format)
		}
		opened, err := os.Open(source) //nolint:gosec // cpio reads a path taken from its own input list.
		if err != nil {
			return fmt.Errorf("%s: Cannot open: %v", source, errText(err))
		}
		file = opened
		defer file.Close()
	default:
		// Devices, sockets and fifos carry no data of their own.
		header.size = 0
	}
	if opts.format == cpioFormatCrc {
		// The crc format's last header field is a plain sum of the bytes, so
		// the data has to be in hand before the header is written.
		if file != nil {
			data, err = io.ReadAll(file)
			if err != nil {
				return fmt.Errorf("%s: %v", source, errText(err))
			}
			file = nil
		}
		for _, b := range data {
			header.check += uint64(b)
		}
		header.check &= 0xffffffff
	}
	if err := writeCpioHeader(output, opts.format, header); err != nil {
		return err
	}
	switch {
	case file != nil:
		if _, err := io.CopyN(output, file, int64(header.size)); err != nil { //nolint:gosec // The size came from the same stat.
			return fmt.Errorf("%s: %v", source, errText(err))
		}
	case len(data) > 0:
		if _, err := output.Write(data); err != nil {
			return err
		}
	}
	if err := writeCpioPadding(output, opts.format, header.size); err != nil {
		return err
	}
	if opts.verbose {
		fmt.Fprintln(os.Stderr, name)
	}
	return nil
}

// cpioDataAlign is the boundary a member's data is padded to: four bytes for
// the hex formats, two for the binary one and none at all for odc.
func cpioDataAlign(format cpioFormat) uint64 {
	switch format {
	case cpioFormatOdc:
		return 1
	case cpioFormatBin:
		return 2
	default:
		return 4
	}
}

func writeCpioPadding(output io.Writer, format cpioFormat, size uint64) error {
	align := cpioDataAlign(format)
	padding := (align - size%align) % align
	if padding == 0 {
		return nil
	}
	_, err := output.Write(make([]byte, padding))
	return err
}

func writeCpioHeader(output io.Writer, format cpioFormat, header cpioHeader) error {
	if header.name == "" || len(header.name)+1 > cpioMaxNameBytes {
		return errors.New("invalid cpio member name")
	}
	nameSize := uint64(len(header.name) + 1)
	var text strings.Builder
	switch format {
	case cpioFormatBin:
		values := []uint64{cpioBinMagic, header.devMajor<<8 | header.devMinor, header.ino, header.mode,
			header.uid, header.gid, header.nlink, header.rdevMajor<<8 | header.rdevMinor,
			header.mtime >> 16, header.mtime & 0xffff, nameSize, header.size >> 16, header.size & 0xffff}
		buffer := make([]byte, 0, cpioBinHeaderSize)
		for _, value := range values {
			buffer = append(buffer, byte(value), byte(value>>8)) //nolint:gosec // Binary header values are 16-bit.
		}
		if _, err := output.Write(buffer); err != nil {
			return err
		}
	case cpioFormatOdc:
		text.WriteString(cpioOdcMagic)
		fmt.Fprintf(&text, "%06o%06o%06o%06o%06o%06o%06o%011o%06o%011o",
			(header.devMajor<<8|header.devMinor)&0o777777, header.ino&0o777777, header.mode&0o777777,
			header.uid&0o777777, header.gid&0o777777, header.nlink&0o777777,
			(header.rdevMajor<<8|header.rdevMinor)&0o777777, header.mtime&0o77777777777,
			nameSize&0o777777, header.size&0o77777777777)
		if text.Len() != cpioOdcHeaderSize {
			return errors.New("internal cpio header error")
		}
		if _, err := io.WriteString(output, text.String()); err != nil {
			return err
		}
	default:
		magic := cpioNewcMagic
		if format == cpioFormatCrc {
			magic = cpioCrcMagic
		}
		values := []uint64{header.ino, header.mode, header.uid, header.gid, header.nlink, header.mtime,
			header.size, header.devMajor, header.devMinor, header.rdevMajor, header.rdevMinor,
			nameSize, header.check}
		text.WriteString(magic)
		for _, value := range values {
			fmt.Fprintf(&text, "%08X", value&0xffffffff)
		}
		if text.Len() != cpioHeaderSize {
			return errors.New("internal cpio header error")
		}
		if _, err := io.WriteString(output, text.String()); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(output, header.name); err != nil {
		return err
	}
	if _, err := output.Write([]byte{0}); err != nil {
		return err
	}
	// The name is padded so that the data starts on the format's boundary.
	switch format {
	case cpioFormatBin:
		return writeCpioPadding(output, format, cpioBinHeaderSize+nameSize)
	case cpioFormatOdc:
		return nil
	default:
		return writeCpioPadding(output, format, cpioHeaderSize+nameSize)
	}
}

// readCpioHeader reads whichever of the four layouts comes next, telling them
// apart by their magic.
func readCpioHeader(reader *bufio.Reader) (cpioHeader, cpioFormat, error) {
	var header cpioHeader
	magic, err := reader.Peek(6)
	if err != nil {
		if errors.Is(err, io.EOF) && len(magic) < 2 {
			return header, 0, io.EOF
		}
		if len(magic) < 2 {
			return header, 0, err
		}
	}
	switch {
	case string(magic[:6]) == cpioNewcMagic:
		header, err = readCpioHexHeader(reader)
		return header, cpioFormatNewc, err
	case string(magic[:6]) == cpioCrcMagic:
		header, err = readCpioHexHeader(reader)
		return header, cpioFormatCrc, err
	case string(magic[:6]) == cpioOdcMagic:
		header, err = readCpioOdcHeader(reader)
		return header, cpioFormatOdc, err
	case uint16(magic[0])|uint16(magic[1])<<8 == cpioBinMagic:
		header, err = readCpioBinHeader(reader)
		return header, cpioFormatBin, err
	}
	return header, 0, errors.New("Bad magic") //nolint:staticcheck // The original's wording starts with a capital.
}

func readCpioHexHeader(reader io.Reader) (cpioHeader, error) {
	var header cpioHeader
	text := make([]byte, cpioHeaderSize)
	if _, err := io.ReadFull(reader, text); err != nil {
		return header, cpioTruncated(err)
	}
	values := make([]uint64, 13)
	for index := range values {
		value, err := strconv.ParseUint(string(text[6+index*8:14+index*8]), 16, 32)
		if err != nil {
			return header, errors.New("invalid cpio header")
		}
		values[index] = value
	}
	header = cpioHeader{ino: values[0], mode: values[1], uid: values[2], gid: values[3], nlink: values[4],
		mtime: values[5], size: values[6], devMajor: values[7], devMinor: values[8],
		rdevMajor: values[9], rdevMinor: values[10], check: values[12]}
	return header, readCpioName(reader, &header, values[11], cpioHeaderSize, 4)
}

func readCpioOdcHeader(reader io.Reader) (cpioHeader, error) {
	var header cpioHeader
	text := make([]byte, cpioOdcHeaderSize)
	if _, err := io.ReadFull(reader, text); err != nil {
		return header, cpioTruncated(err)
	}
	widths := []int{6, 6, 6, 6, 6, 6, 6, 11, 6, 11}
	values := make([]uint64, len(widths))
	at := 6
	for index, width := range widths {
		value, err := strconv.ParseUint(strings.TrimSpace(string(text[at:at+width])), 8, 64)
		if err != nil {
			return header, errors.New("invalid cpio header")
		}
		values[index], at = value, at+width
	}
	header = cpioHeader{devMajor: values[0] >> 8, devMinor: values[0] & 0xff, ino: values[1], mode: values[2],
		uid: values[3], gid: values[4], nlink: values[5], rdevMajor: values[6] >> 8, rdevMinor: values[6] & 0xff,
		mtime: values[7], size: values[9]}
	return header, readCpioName(reader, &header, values[8], cpioOdcHeaderSize, 1)
}

func readCpioBinHeader(reader io.Reader) (cpioHeader, error) {
	var header cpioHeader
	text := make([]byte, cpioBinHeaderSize)
	if _, err := io.ReadFull(reader, text); err != nil {
		return header, cpioTruncated(err)
	}
	values := make([]uint64, 13)
	for index := range values {
		values[index] = uint64(text[index*2]) | uint64(text[index*2+1])<<8
	}
	header = cpioHeader{devMajor: values[1] >> 8, devMinor: values[1] & 0xff, ino: values[2], mode: values[3],
		uid: values[4], gid: values[5], nlink: values[6], rdevMajor: values[7] >> 8, rdevMinor: values[7] & 0xff,
		mtime: values[8]<<16 | values[9], size: values[11]<<16 | values[12]}
	return header, readCpioName(reader, &header, values[10], cpioBinHeaderSize, 2)
}

// readCpioName reads the NUL terminated name that follows a header and steps
// over the padding that puts the data on the format's boundary.
func readCpioName(reader io.Reader, header *cpioHeader, nameSize uint64, headerSize int, align uint64) error {
	if nameSize == 0 || nameSize > cpioMaxNameBytes {
		return errors.New("invalid cpio member name length")
	}
	name := make([]byte, nameSize)
	if _, err := io.ReadFull(reader, name); err != nil {
		return cpioTruncated(err)
	}
	if name[len(name)-1] != 0 {
		return errors.New("unterminated cpio member name")
	}
	header.name = string(name[:len(name)-1])
	if header.name == "" || strings.ContainsRune(header.name, '\x00') {
		return errors.New("invalid cpio member name")
	}
	return skipCpioAlign(reader, uint64(headerSize)+nameSize, align) //nolint:gosec // headerSize is constant and positive.
}

func cpioTruncated(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return errors.New("premature end of file")
	}
	return err
}

func skipCpioAlign(reader io.Reader, size, align uint64) error {
	padding := (align - size%align) % align
	if padding == 0 {
		return nil
	}
	if _, err := io.CopyN(io.Discard, reader, int64(padding)); err != nil { //nolint:gosec // The padding is at most three bytes.
		return cpioTruncated(err)
	}
	return nil
}

func skipCpioData(reader io.Reader, format cpioFormat, size uint64) error {
	if size > maxCpioReadSize {
		return errors.New("cpio member is too large")
	}
	if _, err := io.CopyN(io.Discard, reader, int64(size)); err != nil { //nolint:gosec // The size was bounded above.
		return cpioTruncated(err)
	}
	return skipCpioAlign(reader, size, cpioDataAlign(format))
}

// cpioSelected applies the shell patterns given on the command line, which
// select members rather than exclude them unless -f turns the test around.
func cpioSelected(opts *cpioOptions, name string) bool {
	if len(opts.patterns) == 0 {
		return true
	}
	matched := false
	for _, pattern := range opts.patterns {
		if tarPatternMatch(pattern, name) {
			matched = true
			break
		}
	}
	return matched != opts.nonMatching
}

// cpioLongListing is the "ls -l" line -tv prints: the mode, link count, owner,
// group, size and time, then the name and, for a symlink, its target.
func cpioLongListing(header cpioHeader, target string) string {
	mode := fileModeFromOctal(header.mode & 0o7777)
	switch header.mode & cpioModeTypeMask {
	case cpioModeDirectory:
		mode |= os.ModeDir
	case cpioModeSymlink:
		mode |= os.ModeSymlink
	case 0o010000:
		mode |= os.ModeNamedPipe
	case 0o140000:
		mode |= os.ModeSocket
	case 0o020000:
		mode |= os.ModeDevice | os.ModeCharDevice
	case 0o060000:
		mode |= os.ModeDevice
	}
	size := strconv.FormatUint(header.size, 10)
	if mode&os.ModeDevice != 0 {
		// A device has no data, so its numbers take the size column.
		size = fmt.Sprintf("%3d, %3d", header.rdevMajor, header.rdevMinor)
	}
	line := fmt.Sprintf("%s %3d %-8s %-8s %8s %s %s", modeString(mode), header.nlink,
		userName(uint32(header.uid)), groupName(uint32(header.gid)), //nolint:gosec // Owner ids are 32 bit.
		size, formatLsTime(time.Unix(int64(header.mtime), 0)), header.name) //nolint:gosec // Timestamps fit an int64.
	if target != "" {
		line += " -> " + target
	}
	return line
}

//nolint:gocyclo // one member loop with the original's own branches.
func readCpio(opts *cpioOptions) error {
	input, err := openInput(opts.archive)
	if err != nil {
		return fmt.Errorf("Cannot open %s: %v", opts.archive, errText(err)) //nolint:staticcheck // The original's wording starts with a capital.
	}
	defer input.Close()
	counter := &cpioCounter{reader: input}
	reader := bufio.NewReader(counter)
	root := ""
	if opts.operation == 'i' && !opts.toStdout {
		root, err = filepath.Abs(opts.outputDir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(root, 0o755); err != nil { //nolint:gosec // The extraction root follows conventional permissions.
			return err
		}
	}
	var directories []cpioDirectory
	var extracted uint64
	failed := false
	for {
		header, format, err := readCpioHeader(reader)
		if errors.Is(err, io.EOF) {
			return errors.New("premature end of file")
		}
		if err != nil {
			counter.reportBlocks(opts)
			return err
		}
		if header.name == cpioTrailer {
			break
		}
		if header.size > uint64(maxExpandedArchiveBytes)-extracted {
			return errors.New("archive exceeds the 64 GiB extraction limit")
		}
		if !cpioSelected(opts, header.name) {
			if err := skipCpioData(reader, format, header.size); err != nil {
				return err
			}
			continue
		}
		if opts.operation == 't' {
			if err := listCpioMember(reader, format, opts, header); err != nil {
				return err
			}
			continue
		}
		if opts.verbose {
			fmt.Fprintln(os.Stderr, header.name)
		}
		if opts.toStdout {
			if _, err := io.CopyN(os.Stdout, reader, int64(header.size)); err != nil { //nolint:gosec // Member sizes are bounded above.
				return cpioTruncated(err)
			}
			if err := skipCpioAlign(reader, header.size, cpioDataAlign(format)); err != nil {
				return err
			}
			continue
		}
		kept, err := extractCpioMember(reader, format, opts, root, header, &directories)
		if err != nil {
			fatalf("cpio", "%v", err)
			failed = true
		}
		extracted += kept
	}
	// The last block of the archive is read whole, so the count follows the
	// file rather than the trailer.
	if remainder := counter.count % opts.blockSize; remainder != 0 {
		_, _ = io.CopyN(io.Discard, reader, int64(opts.blockSize-remainder)) //nolint:gosec // A block size fits an int64.
	}
	counter.reportBlocks(opts)
	restoreCpioDirectories(directories)
	if failed {
		return errors.New("error exit delayed from previous errors")
	}
	return nil
}

func listCpioMember(reader *bufio.Reader, format cpioFormat, opts *cpioOptions, header cpioHeader) error {
	if !opts.verbose {
		fmt.Println(header.name)
		return skipCpioData(reader, format, header.size)
	}
	target := ""
	if header.mode&cpioModeTypeMask == cpioModeSymlink && header.size <= 4096 {
		value := make([]byte, header.size)
		if _, err := io.ReadFull(reader, value); err != nil {
			return cpioTruncated(err)
		}
		target = string(value)
		fmt.Println(cpioLongListing(header, target))
		return skipCpioAlign(reader, header.size, cpioDataAlign(format))
	}
	fmt.Println(cpioLongListing(header, ""))
	return skipCpioData(reader, format, header.size)
}

// extractCpioMember writes one member out and returns the number of data bytes
// it accounted for.
func extractCpioMember(reader *bufio.Reader, format cpioFormat, opts *cpioOptions, root string,
	header cpioHeader, directories *[]cpioDirectory) (uint64, error) {
	target, err := safeTarTarget(root, header.name)
	if err != nil {
		_ = skipCpioData(reader, format, header.size)
		return 0, err
	}
	mode := fileModeFromOctal(header.mode & 0o7777)
	modified := time.Unix(int64(header.mtime), 0) //nolint:gosec // Timestamps fit an int64.
	if opts.makeDirs {
		if err := ensureTarParents(root, target); err != nil {
			_ = skipCpioData(reader, format, header.size)
			return 0, err
		}
	}
	if !opts.unlink && header.mode&cpioModeTypeMask != cpioModeDirectory {
		// Without -u an existing copy that is no older than the archived one
		// is kept, and the original says so rather than failing.
		if info, err := os.Lstat(target); err == nil && !info.ModTime().Before(modified) {
			_ = skipCpioData(reader, format, header.size)
			return 0, fmt.Errorf("%s not created: newer or same age version exists", header.name)
		}
	}
	switch header.mode & cpioModeTypeMask {
	case cpioModeDirectory:
		if info, statErr := os.Lstat(target); statErr == nil && !info.IsDir() {
			return 0, fmt.Errorf("refusing to replace non-directory path %q", header.name)
		}
		if err := os.Mkdir(target, 0o700); err != nil && !os.IsExist(err) {
			return 0, fmt.Errorf("%s: Cannot mkdir: %v", header.name, errText(err))
		}
		*directories = append(*directories, cpioDirectory{path: target, mode: mode, modified: modified})
		return 0, nil
	case cpioModeRegular:
		if opts.unlink {
			_ = os.Remove(target)
		}
		if err := extractArchiveRegular(reader, root, target,
			&archiveRegularHeader{size: header.size, modified: modified}, mode); err != nil {
			return 0, err
		}
		if err := skipCpioAlign(reader, header.size, cpioDataAlign(format)); err != nil {
			return 0, err
		}
		return header.size, nil
	case cpioModeSymlink:
		if err := extractCpioSymlink(reader, root, target, opts, header); err != nil {
			_ = skipCpioAlign(reader, header.size, cpioDataAlign(format))
			return 0, err
		}
		return 0, skipCpioAlign(reader, header.size, cpioDataAlign(format))
	default:
		_ = skipCpioData(reader, format, header.size)
		return 0, fmt.Errorf("unsupported archive member type for %q", header.name)
	}
}

func restoreCpioDirectories(directories []cpioDirectory) {
	for index := len(directories) - 1; index >= 0; index-- {
		_ = os.Chmod(directories[index].path, directories[index].mode)
		_ = os.Chtimes(directories[index].path, directories[index].modified, directories[index].modified)
	}
}

type cpioDirectory struct {
	path     string
	mode     os.FileMode
	modified time.Time
}

func extractCpioSymlink(reader io.Reader, root, target string, opts *cpioOptions, header cpioHeader) error {
	if header.size > 4096 {
		return fmt.Errorf("symbolic link target is too long for %q", header.name)
	}
	value := make([]byte, header.size)
	if _, err := io.ReadFull(reader, value); err != nil {
		return cpioTruncated(err)
	}
	link := string(value)
	if err := validateTarSymlink(root, target, header.name, link); err != nil {
		return err
	}
	if opts.unlink {
		_ = os.Remove(target)
	}
	if err := os.Symlink(link, target); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("refusing to replace existing path %q with a symbolic link", header.name)
		}
		return fmt.Errorf("%s: Cannot create symlink: %v", header.name, errText(err))
	}
	return nil
}

// passCpio is -p: the named files are copied straight into a directory without
// an archive in between, but the block count is still reported.
func passCpio(opts *cpioOptions) error {
	paths, err := cpioInputNames(os.Stdin, opts.nullNames)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(opts.outputDir)
	if err != nil {
		return err
	}
	counter := &cpioCounter{writer: io.Discard}
	failed := false
	var directories []cpioDirectory
	for _, path := range paths {
		copied, err := passCpioPath(opts, root, path, counter)
		if err != nil {
			fatalf("cpio", "%v", err)
			failed = true
			continue
		}
		if copied != nil {
			directories = append(directories, *copied)
		}
	}
	restoreCpioDirectories(directories)
	counter.reportBlocks(opts)
	if failed {
		return errors.New("error exit delayed from previous errors")
	}
	return nil
}

func passCpioPath(opts *cpioOptions, root, path string, counter *cpioCounter) (*cpioDirectory, error) {
	stat := os.Lstat
	if opts.dereference {
		stat = os.Stat
	}
	info, err := stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: Cannot stat: %v", path, errText(err))
	}
	target, err := safeTarTarget(root, cpioMemberName(path))
	if err != nil {
		return nil, err
	}
	if opts.makeDirs {
		if err := ensureTarParents(root, target); err != nil {
			return nil, err
		}
	}
	// The count the original reports for -p is the data it moved, headers and
	// all, so the same header is measured even though it is never written.
	header := cpioHeader{name: cpioMemberName(path), size: uint64(info.Size())} //nolint:gosec // File sizes are nonnegative.
	if err := writeCpioHeader(counter, opts.format, header); err != nil {
		return nil, err
	}
	switch {
	case info.IsDir():
		if err := os.Mkdir(target, 0o700); err != nil && !os.IsExist(err) {
			return nil, fmt.Errorf("%s: Cannot mkdir: %v", target, errText(err))
		}
		return &cpioDirectory{path: target, mode: info.Mode().Perm(), modified: info.ModTime()}, nil
	case info.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(path)
		if err != nil {
			return nil, fmt.Errorf("%s: Cannot readlink: %v", path, errText(err))
		}
		if err := validateTarSymlink(root, target, path, link); err != nil {
			return nil, err
		}
		if opts.unlink {
			_ = os.Remove(target)
		}
		if err := os.Symlink(link, target); err != nil {
			return nil, fmt.Errorf("%s: Cannot create symlink to '%s': %v", link, target, errText(err))
		}
		if _, err := counter.Write(make([]byte, len(link))); err != nil {
			return nil, err
		}
		return nil, nil
	case info.Mode().IsRegular():
		return nil, passCpioRegular(opts, path, target, info, counter)
	default:
		return nil, fmt.Errorf("%s: unknown file type", path)
	}
}

func passCpioRegular(opts *cpioOptions, path, target string, info os.FileInfo, counter *cpioCounter) error {
	if opts.link {
		_ = os.Remove(target)
		if err := os.Link(path, target); err == nil {
			return nil
		}
	}
	source, err := os.Open(path) //nolint:gosec // cpio reads a path taken from its own input list.
	if err != nil {
		return fmt.Errorf("%s: Cannot open: %v", path, errText(err))
	}
	defer source.Close()
	if opts.unlink {
		_ = os.Remove(target)
	}
	destination, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("%s: Cannot open: %v", target, errText(err))
	}
	defer destination.Close()
	if _, err := io.Copy(io.MultiWriter(destination, counter), source); err != nil {
		return fmt.Errorf("%s: %v", path, errText(err))
	}
	if err := writeCpioPadding(counter, opts.format, uint64(info.Size())); err != nil { //nolint:gosec // File sizes are nonnegative.
		return err
	}
	if err := destination.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if opts.keepTimes {
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	}
	return nil
}
