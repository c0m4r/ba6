// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type tarOptions struct {
	operation byte
	archive   string
	directory string
	// compression is the codec letter: 'z' gzip, 'j' bzip2, 'J' xz, 'Z' zstd,
	// and zero for a plain archive.
	compression     byte
	gzip            bool
	verbose         bool
	keepOld         bool
	overwrite       bool
	toStdout        bool
	absolute        bool
	numericOwner    bool
	preserve        bool
	stripComponents int
	excludes        []string
	files           []string
	// matched records which selections have been seen, so the ones that never
	// turn up can be reported the way the original reports them.
	matched map[string]bool
	// warnedAbsolute keeps the leading-slash warning to one line per run.
	warnedAbsolute bool
	status         int
}

// listing is where the -v file list goes: standard output, unless the archive
// itself is being written there.
func (o *tarOptions) listing() *os.File {
	if o.archive == "-" && o.operation == 'c' {
		return os.Stderr
	}
	return os.Stdout
}

// selects reports whether an archive member was asked for. With no operands
// every member is wanted; otherwise a member matches a selection exactly, or
// as something inside a directory that was named.
func (o *tarOptions) selects(name string) bool {
	if len(o.files) == 0 {
		return true
	}
	clean := strings.TrimSuffix(filepath.ToSlash(name), "/")
	for _, want := range o.files {
		wanted := strings.TrimSuffix(filepath.ToSlash(want), "/")
		if clean == wanted || strings.HasPrefix(clean, wanted+"/") {
			o.matched[want] = true
			return true
		}
	}
	return false
}

// excluded reports whether --exclude covers this member. The patterns are
// matched the way the original matches them, against the whole name and with
// "*" free to cross a slash.
func (o *tarOptions) excluded(name string) bool {
	// A directory's own name carries a trailing slash in the archive, which the
	// patterns are written without; the original matches the trimmed form, so
	// "*/sub/*" leaves the directory itself in place and drops its contents.
	trimmed := strings.TrimSuffix(filepath.ToSlash(name), "/")
	for _, pattern := range o.excludes {
		if tarPatternMatch(pattern, trimmed) {
			return true
		}
	}
	return false
}

// tarPatternMatch is fnmatch without FNM_PATHNAME, which is what tar's
// --exclude uses: a "*" matches separators too.
func tarPatternMatch(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	// The implementation is the same recursive walk fnmatch does, kept here so
	// that "*" is not stopped by a slash the way filepath.Match stops it.
	var match func(p, s string) bool
	match = func(p, s string) bool {
		for len(p) > 0 {
			switch p[0] {
			case '*':
				for i := 0; i <= len(s); i++ {
					if match(p[1:], s[i:]) {
						return true
					}
				}
				return false
			case '?':
				if len(s) == 0 {
					return false
				}
				p, s = p[1:], s[1:]
			case '[':
				if len(s) == 0 {
					return false
				}
				end := strings.IndexByte(p[1:], ']')
				if end < 0 {
					if s[0] != '[' {
						return false
					}
					p, s = p[1:], s[1:]
					continue
				}
				set := p[1 : 1+end]
				negate := strings.HasPrefix(set, "!") || strings.HasPrefix(set, "^")
				if negate {
					set = set[1:]
				}
				found := false
				for i := 0; i < len(set); i++ {
					if i+2 < len(set) && set[i+1] == '-' {
						if s[0] >= set[i] && s[0] <= set[i+2] {
							found = true
						}
						i += 2
						continue
					}
					if set[i] == s[0] {
						found = true
					}
				}
				if found == negate {
					return false
				}
				p, s = p[2+end:], s[1:]
			case '\\':
				if len(p) < 2 || len(s) == 0 || p[1] != s[0] {
					return false
				}
				p, s = p[2:], s[1:]
			default:
				if len(s) == 0 || p[0] != s[0] {
					return false
				}
				p, s = p[1:], s[1:]
			}
		}
		return len(s) == 0
	}
	return match(pattern, name)
}

const maxExpandedArchiveBytes int64 = 64 << 30

func cmdTar(args []string) int {
	opts, err := parseTarOptions(args)
	if err != nil {
		fatalf("tar", "%v", err)
		return 1
	}
	switch opts.operation {
	case 'c':
		err = createTar(&opts)
	case 'x':
		err = extractTar(&opts)
	case 't':
		err = listTar(&opts)
	}
	if err != nil {
		fatalf("tar", "%v", err)
		var fatal tarFatal
		if errors.As(err, &fatal) {
			fmt.Fprintln(os.Stderr, "tar: Error is not recoverable: exiting now")
			return 2
		}
		return 1
	}
	// A selection that never turned up is the original's own failure, reported
	// after the members that did.
	for _, want := range opts.files {
		if opts.operation != 'c' && !opts.matched[want] {
			fatalf("tar", "%s: Not found in archive", want)
			opts.status = 2
		}
	}
	if opts.status != 0 {
		fmt.Fprintln(os.Stderr, "tar: Exiting with failure status due to previous errors")
	}
	return opts.status
}

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func parseTarOptions(args []string) (tarOptions, error) {
	opts := tarOptions{archive: "-", directory: ".", matched: map[string]bool{}}
	args = append([]string(nil), args...)
	// The historic first-argument form takes the option letters without a dash.
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' && strings.Trim(args[0], "ctxzjJvfkCOpP") == "" {
		args[0] = "-" + args[0]
	}
	var filesFrom []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			opts.files = append(opts.files, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := arg, "", false
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
			needValue := func() (string, error) {
				if hasValue {
					return value, nil
				}
				i++
				if i >= len(args) {
					return "", fmt.Errorf("option '%s' requires an argument", name)
				}
				return args[i], nil
			}
			var err error
			switch name {
			case "--create":
				opts.operation = 'c'
			case "--extract", "--get":
				opts.operation = 'x'
			case "--list":
				opts.operation = 't'
			case "--gzip", "--gunzip", "--ungzip":
				opts.compression = 'z'
			case "--bzip2":
				opts.compression = 'j'
			case "--xz":
				opts.compression = 'J'
			case "--zstd":
				opts.compression = 'Z'
			case "--verbose":
				opts.verbose = true
			case "--keep-old-files":
				opts.keepOld = true
			case "--overwrite":
				opts.overwrite, opts.keepOld = true, false
			case "--to-stdout":
				opts.toStdout = true
			case "--absolute-names":
				opts.absolute = true
			case "--numeric-owner":
				opts.numericOwner = true
			case "--preserve-permissions", "--same-permissions":
				opts.preserve = true
			case "--no-same-owner", "--same-owner", "--no-same-permissions", "--wildcards", "--no-wildcards":
				// Accepted for compatibility: ownership is only restored when
				// the caller can set it, and the patterns are always globs.
			case "--file":
				if opts.archive, err = needValue(); err != nil {
					return opts, err
				}
			case "--directory":
				if opts.directory, err = needValue(); err != nil {
					return opts, err
				}
			case "--exclude":
				pattern, patternErr := needValue()
				if patternErr != nil {
					return opts, patternErr
				}
				opts.excludes = append(opts.excludes, pattern)
			case "--files-from":
				list, listErr := needValue()
				if listErr != nil {
					return opts, listErr
				}
				filesFrom = append(filesFrom, list)
			case "--strip-components":
				count, countErr := needValue()
				if countErr != nil {
					return opts, countErr
				}
				n, convErr := strconv.Atoi(count)
				if convErr != nil || n < 0 {
					return opts, fmt.Errorf("invalid number of components stripped: %s", count)
				}
				opts.stripComponents = n
			default:
				return opts, fmt.Errorf("unrecognized option '%s'", arg)
			}
			continue
		}
		if len(arg) > 1 && arg[0] == '-' {
			for pos := 1; pos < len(arg); pos++ {
				option := arg[pos]
				switch option {
				case 'c', 'x', 't':
					if opts.operation != 0 && opts.operation != option {
						return opts, fmt.Errorf("multiple operations specified")
					}
					opts.operation = option
				case 'z':
					opts.compression = 'z'
				case 'j':
					opts.compression = 'j'
				case 'J':
					opts.compression = 'J'
				case 'v':
					opts.verbose = true
				case 'k':
					opts.keepOld = true
				case 'O':
					opts.toStdout = true
				case 'P':
					opts.absolute = true
				case 'p':
					opts.preserve = true
				case 'f', 'C', 'T':
					var value string
					if pos+1 < len(arg) {
						value = arg[pos+1:]
						pos = len(arg)
					} else {
						i++
						if i >= len(args) {
							return opts, fmt.Errorf("option -%c requires an argument", option)
						}
						value = args[i]
					}
					switch option {
					case 'f':
						opts.archive = value
					case 'C':
						opts.directory = value
					case 'T':
						filesFrom = append(filesFrom, value)
					}
				default:
					return opts, fmt.Errorf("invalid option -- '%c'", option)
				}
			}
			continue
		}
		opts.files = append(opts.files, arg)
	}
	opts.gzip = opts.compression == 'z'
	for _, list := range filesFrom {
		names, err := readTarFileList(list)
		if err != nil {
			return opts, err
		}
		opts.files = append(opts.files, names...)
	}
	if opts.operation == 0 {
		return opts, fmt.Errorf("one of -c, -x, or -t is required")
	}
	if opts.operation == 'c' && len(opts.files) == 0 {
		return opts, fmt.Errorf("refusing to create an empty archive")
	}
	return opts, nil
}

// readTarFileList reads the member names -T points at, one per line.
func readTarFileList(path string) ([]string, error) {
	data, err := readInputBytes(path)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// tarCompressedWriter wraps the archive stream in whichever codec was chosen.
func tarCompressedWriter(output io.Writer, codec byte) (io.Writer, io.Closer, error) {
	switch codec {
	case 'z':
		writer := gzip.NewWriter(output)
		return writer, writer, nil
	case 'j':
		writer, err := newBzip2Writer(output)
		if err != nil {
			return nil, nil, err
		}
		return writer, writer, nil
	case 'J':
		writer, err := newXZWriter(output)
		if err != nil {
			return nil, nil, err
		}
		return writer, writer, nil
	case 'Z':
		writer, err := newZstdWriter(output)
		if err != nil {
			return nil, nil, err
		}
		return writer, writer, nil
	}
	return output, nil, nil
}

// tarCompressedReader is the same in the other direction.
func tarCompressedReader(input io.Reader, codec byte) (io.Reader, error) {
	switch codec {
	case 'z':
		return gzip.NewReader(input)
	case 'j':
		return bzip2.NewReader(input), nil
	case 'J':
		return newXZReader(input)
	case 'Z':
		return newZstdReader(input)
	}
	return input, nil
}

func createTar(opts *tarOptions) (retErr error) {
	base, err := filepath.Abs(opts.directory)
	if err != nil {
		return err
	}
	if err := validateTarCreateSources(opts, base); err != nil {
		return err
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
	archiveWriter, compressor, err := tarCompressedWriter(output, opts.compression)
	if err != nil {
		return err
	}
	tarWriter := tar.NewWriter(archiveWriter)
	for _, operand := range opts.files {
		source := operand
		if !filepath.IsAbs(source) {
			source = filepath.Join(base, source)
		}
		archiveName := filepath.Clean(operand)
		if filepath.IsAbs(archiveName) && !opts.absolute {
			// The original strips the leading slash and says so once.
			if !opts.warnedAbsolute {
				fmt.Fprintln(os.Stderr, "tar: Removing leading `/' from member names")
				opts.warnedAbsolute = true
			}
			archiveName = strings.TrimLeft(filepath.ToSlash(archiveName), "/")
		}
		archiveName = filepath.ToSlash(archiveName)
		if err := addTarPath(tarWriter, source, archiveName, opts); err != nil {
			tarWriter.Close()
			if compressor != nil {
				compressor.Close()
			}
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if compressor != nil {
		if err := compressor.Close(); err != nil {
			return err
		}
	}
	return nil
}

func validateTarCreateSources(opts *tarOptions, base string) error {
	if opts.archive == "-" {
		return nil
	}
	archivePath, err := resolveProspectivePath(opts.archive)
	if err != nil {
		return err
	}
	archiveInfo, archiveErr := os.Stat(opts.archive)
	for _, operand := range opts.files {
		source := operand
		if !filepath.IsAbs(source) {
			source = filepath.Join(base, source)
		}
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if archiveErr == nil {
			if sourceInfo, statErr := os.Stat(source); statErr == nil && os.SameFile(sourceInfo, archiveInfo) {
				return fmt.Errorf("archive output is also an input: %s", operand)
			}
		}
		if !info.IsDir() {
			sourcePath, absErr := filepath.Abs(source)
			if absErr == nil && filepath.Clean(sourcePath) == filepath.Clean(archivePath) {
				return fmt.Errorf("archive output is also an input: %s", operand)
			}
			continue
		}
		resolvedSource, err := filepath.EvalSymlinks(source)
		if err != nil {
			return err
		}
		resolvedSource, err = filepath.Abs(resolvedSource)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(resolvedSource, archivePath)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("archive output %q is inside input directory %q", opts.archive, operand)
		}
	}
	return nil
}

func createArchiveOutput(name string) (io.Writer, func() error, error) {
	if name == "-" {
		return os.Stdout, func() error { return nil }, nil
	}
	if info, err := os.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("refusing to replace non-regular archive path")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666) //nolint:gosec // tar output honors the process umask.
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}

// addTarPath adds one operand and, for a directory, everything inside it. The
// entries are visited in the order the directory gives them, which is the order
// the original stores and lists them in.
func addTarPath(writer *tar.Writer, source, archiveName string, opts *tarOptions) error {
	var walk func(path, name string) error
	walk = func(path, name string) error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if err := addTarEntry(writer, path, name, info, opts); err != nil {
			if errors.Is(err, filepath.SkipDir) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			return nil
		}
		entries, readErr := readDirRaw(path)
		if readErr != nil {
			return readErr
		}
		for _, entry := range entries {
			if err := walk(filepath.Join(path, entry), name+"/"+entry); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(source, archiveName)
}

// addTarEntry writes one member's header, and its contents when it has any.
// A member --exclude covers is reported as filepath.SkipDir so the caller can
// leave a whole directory out.
func addTarEntry(writer *tar.Writer, path, name string, info os.FileInfo, opts *tarOptions) error {
	linkTarget := ""
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		linkTarget = target
	}
	{
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		header.Name = name
		if info.IsDir() && !strings.HasSuffix(header.Name, "/") {
			header.Name += "/"
		}
		if opts.excluded(header.Name) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if opts.numericOwner {
			header.Uname, header.Gname = "", ""
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if opts.verbose {
			fmt.Fprintln(opts.listing(), header.Name)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path) //nolint:gosec // G122: creation only reads a user-selected tree; metadata is rechecked below.
		if err != nil {
			return err
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !os.SameFile(info, openedInfo) {
			file.Close()
			return fmt.Errorf("file changed while archiving: %s", path)
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
}

// tarFatal is a failure the original reports as unrecoverable: its own message,
// then the "Error is not recoverable" line, and exit status 2.
type tarFatal struct{ text string }

func (f tarFatal) Error() string { return f.text }

func openTarReader(opts *tarOptions) (io.ReadCloser, *tar.Reader, error) {
	input, err := openInput(opts.archive)
	if err != nil {
		return nil, nil, tarFatal{fmt.Sprintf("%s: Cannot open: %s", opts.archive, errText(err))}
	}
	reader, err := tarCompressedReader(input, opts.compression)
	if err != nil {
		input.Close()
		return nil, nil, err
	}
	closers := []io.Closer{input}
	if closer, ok := reader.(io.Closer); ok && opts.compression != 0 {
		closers = append([]io.Closer{closer}, closers...)
	}
	return &combinedReadCloser{Reader: reader, closers: closers}, tar.NewReader(reader), nil
}

type combinedReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (c *combinedReadCloser) Close() error {
	var result error
	for _, closer := range c.closers {
		if err := closer.Close(); result == nil && err != nil {
			result = err
		}
	}
	return result
}

func listTar(opts *tarOptions) error {
	input, reader, err := openTarReader(opts)
	if err != nil {
		return err
	}
	defer input.Close()
	// The user/group and size share one field whose width only ever grows, as
	// the original's own running maximum does.
	width := 19
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			return nil
		}
		if nextErr != nil {
			return nextErr
		}
		if !opts.selects(header.Name) || opts.excluded(header.Name) {
			continue
		}
		if !opts.verbose {
			if _, err := fmt.Fprintln(os.Stdout, header.Name); err != nil {
				return err
			}
			continue
		}
		line, grown := tarLongLine(header, width, opts.numericOwner)
		width = grown
		if _, err := fmt.Fprintln(os.Stdout, line); err != nil {
			return err
		}
	}
}

// tarLongLine renders one member the way "tar tv" does, and reports the field
// width to carry into the next line.
func tarLongLine(header *tar.Header, width int, numeric bool) (string, int) {
	owner := header.Uname
	group := header.Gname
	if numeric || owner == "" {
		owner = strconv.Itoa(header.Uid)
	}
	if numeric || group == "" {
		group = strconv.Itoa(header.Gid)
	}
	identity := owner + "/" + group
	size := strconv.FormatInt(header.Size, 10)
	if header.Typeflag == tar.TypeChar || header.Typeflag == tar.TypeBlock {
		size = fmt.Sprintf("%d,%d", header.Devmajor, header.Devminor)
	}
	pad := width - len(identity) - len(size)
	if pad < 1 {
		pad = 1
		width = len(identity) + len(size) + 1
	}
	name := header.Name
	switch header.Typeflag {
	case tar.TypeSymlink:
		name += " -> " + header.Linkname
	case tar.TypeLink:
		name += " link to " + header.Linkname
	}
	mode := modeString(header.FileInfo().Mode())
	return fmt.Sprintf("%s %s%*s %s %s", mode, identity, pad+len(size), size,
		header.ModTime.Format("2006-01-02 15:04"), name), width
}

func extractTar(opts *tarOptions) error {
	input, reader, err := openTarReader(opts)
	if err != nil {
		return err
	}
	defer input.Close()
	root, err := filepath.Abs(opts.directory)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil { //nolint:gosec // extraction destination follows conventional directory permissions.
		return err
	}
	type directoryMetadata struct {
		path string
		mode os.FileMode
		time time.Time
	}
	var directories []directoryMetadata
	extractedBytes := int64(0)
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		if !opts.selects(header.Name) || opts.excluded(header.Name) {
			continue
		}
		// --strip-components drops leading path components, and a member left
		// with nothing at all is skipped.
		memberName := tarStripComponents(header.Name, opts.stripComponents)
		if memberName == "" {
			continue
		}
		if opts.toStdout {
			// -O writes the members' contents out instead of unpacking them.
			if header.Typeflag == tar.TypeReg || header.Typeflag == byte(0) {
				if _, err := io.CopyN(os.Stdout, reader, header.Size); err != nil {
					return err
				}
			}
			continue
		}
		target, err := safeTarTarget(root, memberName)
		if err != nil {
			return err
		}
		if opts.verbose {
			fmt.Fprintln(opts.listing(), header.Name)
		}
		if header.Mode < 0 || header.Mode > 0o7777 {
			return fmt.Errorf("invalid mode for archive member %q", header.Name)
		}
		mode := fileModeFromOctal(uint64(header.Mode)) //nolint:gosec // G115: mode was validated as nonnegative and <= 07777.
		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureTarParents(root, target); err != nil {
				return err
			}
			if info, statErr := os.Lstat(target); statErr == nil && !info.IsDir() {
				return fmt.Errorf("refusing to replace non-directory path %q", memberName)
			} else if statErr != nil && !os.IsNotExist(statErr) {
				return statErr
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			directories = append(directories, directoryMetadata{path: target, mode: mode, time: header.ModTime})
		case tar.TypeReg, byte(0):
			if header.Size < 0 || header.Size > maxExpandedArchiveBytes-extractedBytes {
				return fmt.Errorf("archive exceeds the 64 GiB extraction limit")
			}
			extractedBytes += header.Size
			if err := ensureTarParents(root, target); err != nil {
				return err
			}
			if info, statErr := os.Lstat(target); statErr == nil {
				if opts.keepOld {
					// The original reports this per member and carries on.
					fatalf("tar", "%s: Cannot open: File exists", memberName)
					opts.status = 2
					continue
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("refusing to replace non-regular path %q", memberName)
				}
			} else if !os.IsNotExist(statErr) {
				return statErr
			}
			if err := extractTarRegular(reader, target, header, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := ensureTarParents(root, target); err != nil {
				return err
			}
			if err := validateTarSymlink(root, target, memberName, header.Linkname); err != nil {
				return err
			}
			if err := extractTarSymlink(target, header.Linkname, memberName, opts.keepOld); err != nil {
				if opts.keepOld && strings.Contains(err.Error(), "refusing to replace") {
					fatalf("tar", "%s: Cannot create symlink to '%s': File exists", memberName, header.Linkname)
					opts.status = 2
					continue
				}
				return err
			}
		case tar.TypeLink:
			linkTarget, err := safeTarTarget(root, header.Linkname)
			if err != nil {
				return err
			}
			if err := ensureTarParents(root, target); err != nil {
				return err
			}
			if info, statErr := os.Lstat(linkTarget); statErr != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("hard-link target is not a regular extracted file: %q", header.Linkname)
			}
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archive member type for %q", memberName)
		}
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Chmod(directories[i].path, directories[i].mode); err != nil {
			return err
		}
		if err := os.Chtimes(directories[i].path, directories[i].time, directories[i].time); err != nil {
			return err
		}
	}
	return nil
}

// tarStripComponents removes the first n path components from a member name,
// which is what --strip-components asks for.
func tarStripComponents(name string, count int) string {
	if count == 0 {
		return name
	}
	trailing := strings.HasSuffix(name, "/")
	parts := strings.Split(strings.Trim(filepath.ToSlash(name), "/"), "/")
	if len(parts) <= count {
		return ""
	}
	stripped := strings.Join(parts[count:], "/")
	if trailing {
		stripped += "/"
	}
	return stripped
}

// extractTarSymlink installs a symlink with rename(2), just as regular
// members are installed. Re-extracting an archive therefore replaces a stale
// symlink without an unlink/create window. -k/--keep-old-files retains the
// deliberately conservative behavior for callers that need it.
func extractTarSymlink(target, link, member string, keepOld bool) error {
	if keepOld {
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("refusing to replace existing path %q with a symbolic link", member)
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	// CreateTemp gives the temporary symlink a private, unpredictable name in
	// the target directory. The placeholder is only used to obtain that name;
	// the .link sibling is created directly as a symlink before it is renamed.
	placeholder, err := os.CreateTemp(filepath.Dir(target), ".ba6-tar-*")
	if err != nil {
		return err
	}
	placeholderName := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(placeholderName)
		return err
	}
	if err := os.Remove(placeholderName); err != nil {
		return err
	}
	temporary := placeholderName + ".link"
	created := false
	installed := false
	defer func() {
		if created && !installed {
			_ = os.Remove(temporary)
		}
	}()
	if err := os.Symlink(link, temporary); err != nil {
		return err
	}
	created = true
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	installed = true
	return nil
}

// extractTarRegular writes a member to a new inode and atomically installs it.
// Truncating an existing target in place would also modify every hard link to
// that inode, including links outside the extraction root.
func extractTarRegular(reader io.Reader, target string, header *tar.Header, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(target), ".ba6-tar-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	installed := false
	open := true
	defer func() {
		if open {
			_ = file.Close()
		}
		if !installed {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := io.CopyN(file, reader, header.Size); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	open = false
	accessTime := header.AccessTime
	if accessTime.IsZero() {
		accessTime = header.ModTime
	}
	if err := os.Chtimes(temporary, accessTime, header.ModTime); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	installed = true
	return nil
}

func safeTarTarget(root, name string) (string, error) {
	converted := filepath.FromSlash(name)
	if filepath.IsAbs(converted) {
		return "", fmt.Errorf("unsafe absolute archive path %q", name)
	}
	cleaned := filepath.Clean(converted)
	if !filepath.IsLocal(cleaned) {
		return "", fmt.Errorf("archive path escapes destination: %q", name)
	}
	target := filepath.Join(root, cleaned)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive path escapes destination: %q", name)
	}
	return target, nil
}

func ensureTarParents(root, target string) error {
	parent := filepath.Dir(target)
	relative, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	current := root
	if relative != "." {
		for _, component := range strings.Split(relative, string(os.PathSeparator)) {
			current = filepath.Join(current, component)
			info, statErr := os.Lstat(current)
			switch {
			case statErr == nil && info.Mode()&os.ModeSymlink != 0:
				return fmt.Errorf("archive path traverses symbolic link %q", current)
			case statErr == nil && !info.IsDir():
				return fmt.Errorf("archive parent is not a directory: %q", current)
			case os.IsNotExist(statErr):
				if err := os.Mkdir(current, 0o700); err != nil {
					return err
				}
			case statErr != nil:
				return statErr
			}
		}
	}
	return nil
}

// validateTarSymlink rejects archive symbolic links that would point outside
// the extraction root. The lexical check catches links that escape on their
// own; resolving the components that already exist on disk also catches
// escapes staged across members, where a purely lexical check folds
// "subdir/link/.." back into "subdir" even though "subdir/link" is a symbolic
// link extracted earlier from the same archive. The parent directory of the
// link must already exist, so callers run ensureTarParents first.
func validateTarSymlink(root, target, name, link string) error {
	if filepath.IsAbs(filepath.FromSlash(link)) {
		return fmt.Errorf("unsafe absolute symbolic link %q", name)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(name)), filepath.FromSlash(link)))
	if !filepath.IsLocal(resolved) {
		return fmt.Errorf("symbolic link escapes destination: %q", name)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	base, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return err
	}
	destination := resolveArchiveLink(base, link)
	relative, err := filepath.Rel(realRoot, destination)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("symbolic link escapes destination: %q", name)
	}
	return nil
}

// resolveArchiveLink walks the components of an archive symbolic-link target
// from the directory that will hold the link, following the links that already
// exist on disk the way the kernel would. Components that do not resolve are
// appended as written, since they cannot redirect the link anywhere.
func resolveArchiveLink(base, link string) string {
	current := base
	for _, component := range strings.Split(filepath.ToSlash(link), "/") {
		switch component {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, component)
		if resolved, err := filepath.EvalSymlinks(next); err == nil {
			current = resolved
			continue
		}
		current = next
	}
	return current
}

// gzipUsage reports a command-line mistake the way gzip does, with its own
// backquoted spelling of the "Try ..." line.
func gzipUsage(format string, a ...interface{}) int {
	fatalf("gzip", format, a...)
	fmt.Fprintln(os.Stderr, "Try `gzip --help' for more information.")
	return 1
}

// gzipOptions is one gzip or gunzip command line.
type gzipOptions struct {
	decompress bool
	stdout     bool
	keep       bool
	force      bool
	test       bool
	list       bool
	verbose    bool
	quiet      bool
	recursive  bool
	noName     bool // -n: store no name or timestamp, and restore neither
	useName    bool // -N: restore the stored name and timestamp
	suffix     string
	level      int
	// status is the exit status: 1 for an error, 2 for a warning, as in the
	// original.
	status int
	// listed marks that the -l header has already been printed.
	listed bool
	// totals accumulate across the operands of one -l run. The original counts
	// only the last member's container bytes towards the totals row's ratio,
	// because it keeps that figure in a single variable.
	totalIn, totalOut uint64
	lastOverhead      uint64
	files             int
}

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func cmdGzip(args []string) int {
	options := gzipOptions{suffix: ".gz", level: gzip.DefaultCompression}
	var files []string
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !parsing || arg == "-" || !strings.HasPrefix(arg, "-") {
			files = append(files, arg)
			continue
		}
		if arg == "--" {
			parsing = false
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := arg, "", false
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
			switch name {
			case "--decompress", "--uncompress":
				options.decompress = true
			case "--stdout", "--to-stdout":
				options.stdout = true
			case "--keep":
				options.keep = true
			case "--force":
				options.force = true
			case "--test":
				options.test, options.decompress = true, true
			case "--list":
				options.list = true
			case "--verbose":
				options.verbose = true
			case "--quiet", "--silent":
				options.quiet = true
			case "--recursive":
				options.recursive = true
			case "--no-name":
				options.noName, options.useName = true, false
			case "--name":
				options.useName, options.noName = true, false
			case "--fast":
				options.level = gzip.BestSpeed
			case "--best":
				options.level = gzip.BestCompression
			case "--suffix":
				if !hasValue {
					i++
					if i >= len(args) {
						fatalf("gzip", "option '--suffix' requires an argument")
						return 1
					}
					value = args[i] //nolint:gosec // G602: the bound was just checked above.
				}
				options.suffix = value
			default:
				return gzipUsage("unrecognized option '%s'", arg)
			}
			continue
		}
		cluster := arg[1:]
		for len(cluster) > 0 {
			flag := cluster[0]
			cluster = cluster[1:]
			switch flag {
			case 'd':
				options.decompress = true
			case 'c':
				options.stdout = true
			case 'k':
				options.keep = true
			case 'f':
				options.force = true
			case 't':
				options.test, options.decompress = true, true
			case 'l':
				options.list = true
			case 'v':
				options.verbose = true
			case 'q':
				options.quiet = true
			case 'r':
				options.recursive = true
			case 'n':
				options.noName, options.useName = true, false
			case 'N':
				options.useName, options.noName = true, false
			case '1', '2', '3', '4', '5', '6', '7', '8', '9':
				options.level = int(flag - '0')
			case 'S':
				value := cluster
				cluster = ""
				if value == "" {
					i++
					if i >= len(args) {
						fatalf("gzip", "option requires an argument -- 'S'")
						return 1
					}
					value = args[i] //nolint:gosec // G602: the bound was just checked above.
				}
				options.suffix = value
			default:
				return gzipUsage("invalid option -- '%c'", flag)
			}
		}
	}
	if len(files) == 0 {
		files, options.stdout = []string{"-"}, true
	}
	for _, name := range files {
		options.handle(name)
	}
	if options.list && options.files > 1 {
		options.printListRow("(totals)", options.totalOut, options.totalIn,
			gzipHeader{overhead: options.lastOverhead})
	}
	return options.status
}

func cmdGunzip(args []string) int {
	return cmdGzip(append([]string{"-d"}, args...))
}

// fail reports an error and sets the exit status the original uses for it: 1
// for a real failure, 2 for something it calls a warning.
func (o *gzipOptions) fail(status int, format string, a ...interface{}) {
	if !o.quiet {
		fatalf("gzip", format, a...)
	}
	if status > o.status {
		o.status = status
	}
}

// failFormat reports a member that is not gzip at all. The original prints this
// one whatever -q says, and puts a blank line in front of it.
func (o *gzipOptions) failFormat(name string) {
	fmt.Fprintf(os.Stderr, "\ngzip: %s: not in gzip format\n", name)
	if o.status < 1 {
		o.status = 1
	}
}

// handle deals with one operand, descending into it under -r.
func (o *gzipOptions) handle(name string) {
	if name != "-" && o.recursive {
		if info, err := os.Lstat(name); err == nil && info.IsDir() {
			entries, readErr := os.ReadDir(name)
			if readErr != nil {
				o.fail(1, "%s: %s", name, errText(readErr))
				return
			}
			for _, entry := range entries {
				o.handle(filepath.Join(name, entry.Name()))
			}
			return
		}
	}
	switch {
	case o.list:
		o.listOne(name)
	case o.test:
		o.testOne(name)
	default:
		o.transform(name)
	}
}

// gzipHeader is what the front of a member says about the file it holds.
type gzipHeader struct {
	name    string
	modTime time.Time
	method  string
	crc     uint32
	// overhead is the part of the member that is not deflate output, which the
	// ratio leaves out.
	overhead uint64
}

// readGzipInfo reads one member's header and the size the trailer records,
// which is all -l and -t's listing need.
func readGzipInfo(path string) (gzipHeader, uint64, uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return gzipHeader{}, 0, 0, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		// gzip words this one itself rather than passing on the library's text.
		return gzipHeader{}, 0, 0, fmt.Errorf("not in gzip format")
	}
	defer reader.Close()
	info, err := file.Stat()
	if err != nil {
		return gzipHeader{}, 0, 0, err
	}
	compressed := uint64(info.Size()) //nolint:gosec // G115: a file size is nonnegative.
	// The last four bytes of the stream hold the uncompressed size modulo 2^32,
	// which is what the original reports without reading the data back.
	var trailer [8]byte
	if compressed >= 8 {
		if _, err := file.ReadAt(trailer[:], info.Size()-8); err != nil {
			return gzipHeader{}, 0, 0, err
		}
	}
	header := gzipHeader{
		name:     reader.Name,
		modTime:  reader.ModTime,
		method:   "defla",
		crc:      binary.LittleEndian.Uint32(trailer[0:4]),
		overhead: gzipOverhead(path),
	}
	return header, compressed, uint64(binary.LittleEndian.Uint32(trailer[4:8])), nil
}

// listOne prints one -l row.
func (o *gzipOptions) listOne(name string) {
	header, compressed, uncompressed, err := readGzipInfo(name)
	if err != nil {
		if err.Error() == "not in gzip format" {
			o.failFormat(name)
			return
		}
		o.fail(1, "%s: %s", name, errText(err))
		return
	}
	o.files++
	o.totalIn += uncompressed
	o.totalOut += compressed
	o.lastOverhead = header.overhead
	o.printListRow(o.listedName(name, header), compressed, uncompressed, header)
}

// listedName is the name -l reports: the file's own name with its suffix taken
// off, which is the name it would decompress to. Only -N asks for the name the
// member itself carries.
func (o *gzipOptions) listedName(name string, header gzipHeader) string {
	if o.useName && header.name != "" {
		return header.name
	}
	if stripped, ok := gzipStripSuffix(name, o.suffix); ok {
		return stripped
	}
	return name
}

func (o *gzipOptions) printListRow(name string, compressed, uncompressed uint64, header gzipHeader) {
	if !o.listed {
		o.listed = true
		if o.verbose {
			fmt.Print("method  crc     date  time  ")
		}
		fmt.Printf("%19s%20s%7s %s\n", "compressed", "uncompressed", "ratio", "uncompressed_name")
	}
	if o.verbose {
		stamp := header.modTime
		if stamp.IsZero() {
			stamp = time.Unix(0, 0)
		}
		fmt.Printf("%-5s %08x %s ", header.method, header.crc, stamp.Format("Jan _2 15:04"))
	}
	fmt.Printf("%19d%20d%7s %s\n", compressed, uncompressed, gzipRatio(compressed, uncompressed, header.overhead), name)
}

// gzipRatio is the space saved. The original measures the deflate stream
// alone, so the member's header and its eight-byte trailer are taken off the
// compressed side first — which is why gzip reports a better ratio than the
// two file sizes alone would suggest.
func gzipRatio(compressed, uncompressed, overhead uint64) string {
	if uncompressed == 0 {
		return "0.0%"
	}
	payload := int64(0)
	if compressed > overhead {
		payload = int64(compressed - overhead) //nolint:gosec // G115: a file size fits an int64.
	}
	// The original prints this with "%5.1f%%", so the tenth is rounded, and a
	// stream that grew rather than shrank reports a negative ratio.
	total := int64(uncompressed) //nolint:gosec // G115: same.
	saved := (total - payload) * 1000
	if saved < 0 {
		saved -= total / 2
	} else {
		saved += total / 2
	}
	tenths := saved / total
	sign := ""
	if tenths < 0 {
		sign, tenths = "-", -tenths
	}
	return fmt.Sprintf("%s%d.%d%%", sign, tenths/10, tenths%10)
}

// gzipOverhead is how many bytes of one member are not deflate output: the
// header, whose length depends on which optional fields it carries, and the
// trailer.
func gzipOverhead(path string) uint64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	var fixed [10]byte
	if _, err := io.ReadFull(file, fixed[:]); err != nil {
		return 0
	}
	length, flags := uint64(10), fixed[3]
	skipString := func() {
		buffer := make([]byte, 1)
		for {
			if _, err := io.ReadFull(file, buffer); err != nil {
				return
			}
			length++
			if buffer[0] == 0 {
				return
			}
		}
	}
	if flags&0x04 != 0 { // FEXTRA
		var extra [2]byte
		if _, err := io.ReadFull(file, extra[:]); err == nil {
			size := uint64(binary.LittleEndian.Uint16(extra[:]))
			length += 2 + size
			if _, err := file.Seek(int64(size), io.SeekCurrent); err != nil { //nolint:gosec // G115: a 16-bit length fits an int64.
				return length + 8
			}
		}
	}
	if flags&0x08 != 0 { // FNAME
		skipString()
	}
	if flags&0x10 != 0 { // FCOMMENT
		skipString()
	}
	if flags&0x02 != 0 { // FHCRC
		length += 2
	}
	return length + 8
}

// testOne checks one member end to end without writing anything out.
func (o *gzipOptions) testOne(name string) {
	input, err := openInput(name)
	if err != nil {
		o.fail(1, "%s: %s", name, errText(err))
		return
	}
	defer input.Close()
	reader, err := gzip.NewReader(input)
	if err != nil {
		o.failFormat(name)
		return
	}
	defer reader.Close()
	// The output is discarded, so there is nothing for an oversized member to
	// fill; -t exists precisely to read one all the way through.
	if _, err := io.Copy(io.Discard, reader); err != nil { //nolint:gosec // G110: nothing is stored, so a large member costs only time.
		o.fail(1, "%s: %s", name, gzipErrorText(err))
		return
	}
	if o.verbose {
		fmt.Printf("%s:\t OK\n", name)
	}
}

// gzipErrorText words the two integrity failures the way the original does.
func gzipErrorText(err error) string {
	switch {
	case errors.Is(err, gzip.ErrChecksum):
		return "invalid compressed data--crc error"
	case errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF):
		return "unexpected end of file"
	}
	return errText(err)
}

// gzipStripSuffix takes a known compressed suffix off a name.
func gzipStripSuffix(name, suffix string) (string, bool) {
	if suffix != "" && strings.HasSuffix(name, suffix) {
		return strings.TrimSuffix(name, suffix), true
	}
	for _, known := range []string{".gz", ".z", "-gz", "-z", "_z"} {
		if strings.HasSuffix(name, known) {
			return strings.TrimSuffix(name, known), true
		}
	}
	for _, known := range []struct{ from, to string }{{".tgz", ".tar"}, {".taz", ".tar"}} {
		if strings.HasSuffix(name, known.from) {
			return strings.TrimSuffix(name, known.from) + known.to, true
		}
	}
	return name, false
}

func (o *gzipOptions) transform(name string) {
	if err := o.transformOne(name); err != nil {
		var warning gzipWarning
		if errors.As(err, &warning) {
			o.fail(2, "%s", warning.text)
			return
		}
		if err.Error() == "not in gzip format" {
			o.failFormat(name)
			return
		}
		o.fail(1, "%s: %s", name, gzipErrorText(err))
	}
}

// gzipWarning is a failure the original counts as a warning, which exits 2 and
// carries its own complete message.
type gzipWarning struct{ text string }

func (w gzipWarning) Error() string { return w.text }

//nolint:gocyclo // one straight-line path per direction, with the file swap at the end.
func (o *gzipOptions) transformOne(name string) (retErr error) {
	input, err := openInput(name)
	if err != nil {
		return err
	}
	defer input.Close()
	outputName := "-"
	if !o.stdout && name != "-" {
		if o.decompress {
			stripped, ok := gzipStripSuffix(name, o.suffix)
			if !ok {
				return gzipWarning{fmt.Sprintf("%s: unknown suffix -- ignored", name)}
			}
			outputName = stripped
			// -N writes to the name the member carries, so the "already
			// exists" check has to see that name rather than the derived one.
			if o.useName {
				if header, _, _, err := readGzipInfo(name); err == nil && header.name != "" {
					outputName = filepath.Join(filepath.Dir(name), header.name)
				}
			}
		} else {
			if strings.HasSuffix(name, o.suffix) {
				return gzipWarning{fmt.Sprintf("%s already has %s suffix -- unchanged", name, o.suffix)}
			}
			outputName = name + o.suffix
		}
	}
	var output io.WriteCloser
	temporaryName := ""
	if outputName == "-" {
		output = nopWriteCloser{os.Stdout}
	} else {
		if _, statErr := os.Lstat(outputName); statErr == nil && !o.force {
			return gzipWarning{fmt.Sprintf("%s already exists;\tnot overwritten", outputName)}
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		file, openErr := os.CreateTemp(filepath.Dir(outputName), "."+filepath.Base(outputName)+".tmp-*")
		if openErr != nil {
			return openErr
		}
		temporaryName = file.Name()
		output = file
	}
	success := false
	defer func() {
		if !success && temporaryName != "" {
			_ = os.Remove(temporaryName)
		}
	}()
	var stored gzipHeader
	written := uint64(0)
	read := uint64(0)
	if info, statErr := os.Stat(name); statErr == nil {
		read = uint64(info.Size()) //nolint:gosec // G115: a file size is nonnegative.
	}
	if o.decompress {
		reader, gzipErr := gzip.NewReader(input)
		if gzipErr != nil {
			output.Close()
			return fmt.Errorf("not in gzip format")
		}
		stored = gzipHeader{name: reader.Name, modTime: reader.ModTime}
		count, copyErr := io.Copy(output, io.LimitReader(reader, maxExpandedArchiveBytes+1)) //nolint:gosec // G110: output is explicitly capped at 64 GiB.
		err = copyErr
		written = uint64(count) //nolint:gosec // G115: io.Copy reports a nonnegative count.
		if err == nil && count > maxExpandedArchiveBytes {
			err = fmt.Errorf("decompressed data exceeds the 64 GiB limit")
		}
		if closeErr := reader.Close(); err == nil {
			err = closeErr
		}
	} else {
		writer, levelErr := gzip.NewWriterLevel(output, o.level)
		if levelErr != nil {
			output.Close()
			return levelErr
		}
		if name != "-" && !o.noName {
			writer.Name = filepath.Base(name)
			if info, statErr := os.Stat(name); statErr == nil {
				writer.ModTime = info.ModTime()
			}
		}
		count, copyErr := io.Copy(writer, input)
		read = uint64(count) //nolint:gosec // G115: same.
		err = copyErr
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if temporaryName != "" {
		if info, statErr := os.Stat(temporaryName); statErr == nil {
			written = uint64(info.Size()) //nolint:gosec // G115: same.
		}
		// -N restores the timestamp the member carried, and its name when the
		// caller did not choose one.
		if o.decompress && o.useName && !stored.modTime.IsZero() {
			_ = os.Chtimes(temporaryName, stored.modTime, stored.modTime)
		}
		if info, statErr := os.Stat(name); statErr == nil {
			_ = os.Chmod(temporaryName, info.Mode().Perm())
			if !o.decompress || !o.useName {
				_ = os.Chtimes(temporaryName, info.ModTime(), info.ModTime())
			}
		}
		if err := os.Rename(temporaryName, outputName); err != nil {
			return err
		}
	}
	success = true
	if o.verbose && name != "-" {
		// The percentage is the space the compressed side saves, whichever
		// direction the transform went in.
		compressed, uncompressed, container := read, written, gzipOverhead(name)
		if !o.decompress {
			compressed, uncompressed, container = written, read, gzipOverhead(outputName)
		}
		if outputName == "-" {
			fmt.Fprintf(os.Stderr, "%s:\t%7s\n", name, gzipRatio(compressed, uncompressed, container))
		} else {
			fmt.Fprintf(os.Stderr, "%s:\t%6s -- replaced with %s\n", name, gzipRatio(compressed, uncompressed, container), outputName)
		}
	}
	if name != "-" && outputName != "-" && !o.keep {
		if err := input.Close(); err != nil {
			return err
		}
		if err := os.Remove(name); err != nil {
			return err
		}
	}
	return nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
