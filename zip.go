// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	archivezip "archive/zip"
	"bytes"
	"compress/flate"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type zipOptions struct {
	archive   string
	recursive bool
	store     bool
	junk      bool
	quiet     bool
	verbose   bool
	files     []string
	excludes  []string
}

// report prints the line the original prints for each member it adds. The
// percentage is the saving, which this writer can only know after the fact, so
// a stored member always reads 0%.
func (o *zipOptions) report(name, method string, plain int64, compressed uint64) {
	if o.quiet {
		return
	}
	fmt.Printf("  adding: %s (%s %s%%)\n", name, method,
		zipRatio(uint64(plain), compressed)) //nolint:gosec // G115: a member's size is nonnegative.
}

// selects reports whether a path survives the -x patterns.
func (o *zipOptions) selects(name string) bool {
	for _, pattern := range o.excludes {
		if tarPatternMatch(pattern, name) {
			return false
		}
	}
	return true
}

func cmdZip(args []string) int {
	opts, err := parseZipOptions(args)
	if err != nil {
		fatalf("zip", "%v", err)
		return 1
	}
	if err := createZip(opts); err != nil {
		fatalf("zip", "%v", err)
		return 1
	}
	return 0
}

func parseZipOptions(args []string) (zipOptions, error) {
	opts := zipOptions{}
	parsing, excluding := true, false
	for _, arg := range args {
		if parsing && arg == "--" {
			parsing = false
			continue
		}
		if parsing && len(arg) > 1 && arg[0] == '-' {
			if strings.HasPrefix(arg, "--") {
				switch arg {
				case "--recurse-paths":
					opts.recursive = true
				case "--exclude":
					excluding = true
				case "--quiet":
					opts.quiet = true
				case "--verbose":
					opts.verbose = true
				case "--junk-paths":
					opts.junk = true
				case "--store":
					opts.store = true
				default:
					return opts, fmt.Errorf("unsupported option %q", arg)
				}
				continue
			}
			// The short options cluster, as they do in the original.
			for _, letter := range arg[1:] {
				switch letter {
				case 'r':
					opts.recursive = true
				case 'x':
					excluding = true
				case 'q':
					opts.quiet = true
				case 'v':
					opts.verbose = true
				case 'j':
					opts.junk = true
				case '0':
					opts.store = true
				case '1', '2', '3', '4', '5', '6', '7', '8', '9':
					// Every level above zero maps onto deflate, which is the
					// only method this writer produces.
					opts.store = false
				default:
					return opts, fmt.Errorf("unsupported option %q", arg)
				}
			}
			continue
		}
		switch {
		case opts.archive == "":
			opts.archive = arg
		case excluding:
			opts.excludes = append(opts.excludes, arg)
		default:
			opts.files = append(opts.files, arg)
		}
	}
	if opts.archive == "" || len(opts.files) == 0 {
		return opts, fmt.Errorf("expected ARCHIVE and at least one file")
	}
	return opts, nil
}

func createZip(opts zipOptions) (retErr error) {
	if err := validateTarCreateSources(&tarOptions{archive: opts.archive, directory: ".", files: opts.files}, "."); err != nil {
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
	writer := archivezip.NewWriter(output)
	for _, operand := range opts.files {
		name := zipMemberName(operand)
		if err := addZipPath(writer, operand, name, &opts); err != nil {
			_ = writer.Close()
			return err
		}
	}
	return writer.Close()
}

func zipMemberName(path string) string {
	cleaned := filepath.Clean(path)
	if filepath.IsAbs(cleaned) {
		cleaned = strings.TrimLeft(filepath.ToSlash(cleaned), "/")
	}
	return filepath.ToSlash(cleaned)
}

func addZipPath(writer *archivezip.Writer, source, name string, opts *zipOptions) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if opts.junk && !info.IsDir() {
		// -j stores each file under its own name alone.
		name = path.Base(name)
	}
	if info.IsDir() {
		if !strings.HasSuffix(name, "/") {
			name += "/"
		}
		// A pattern that covers the directory covers everything under it, in
		// either spelling of its name.
		if !opts.selects(name) || !opts.selects(strings.TrimSuffix(name, "/")) {
			return nil
		}
		if !opts.junk {
			if err := addZipEntry(writer, source, name, info, opts); err != nil {
				return err
			}
		}
		if !opts.recursive {
			return nil
		}
		// The entries go in the order the directory gives them, which is the
		// order the original stores and reports them in.
		entries, err := readDirRaw(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			child := filepath.ToSlash(filepath.Join(name, entry))
			if err := addZipPath(writer, filepath.Join(source, entry), child, opts); err != nil {
				return err
			}
		}
		return nil
	}
	if !opts.selects(name) {
		return nil
	}
	return addZipEntry(writer, source, name, info, opts)
}

func addZipEntry(writer *archivezip.Writer, source, name string, info os.FileInfo, opts *zipOptions) error {
	header, err := archivezip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	header.SetMode(info.Mode())
	if info.IsDir() {
		header.Method = archivezip.Store
		if _, err := writer.CreateHeader(header); err != nil {
			return err
		}
		opts.report(name, "stored", 0, 0)
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		header.Method = archivezip.Store
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		_, err = io.WriteString(entry, target)
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported file type for %q", source)
	}
	// The member is compressed first so that the smaller of the two forms can
	// be stored, which is the choice the original makes, and so that its line
	// can report the saving it actually achieved.
	payload, err := compressZipMember(source, info, opts.store)
	if err != nil {
		return err
	}
	header.Method = payload.method
	header.CRC32 = payload.crc
	header.CompressedSize64 = uint64(len(payload.data))   //nolint:gosec // G115: a member's size is nonnegative.
	header.UncompressedSize64 = uint64(payload.plainSize) //nolint:gosec // G115: same.
	entry, err := writer.CreateRaw(header)
	if err != nil {
		return err
	}
	if _, err := entry.Write(payload.data); err != nil {
		return err
	}
	method := "deflated"
	if payload.method == archivezip.Store {
		method = "stored"
	}
	opts.report(name, method, payload.plainSize, header.CompressedSize64)
	return nil
}

// zipAlreadyCompressed reports whether a name carries one of the suffixes the
// original never tries to deflate, because the contents are compressed already.
func zipAlreadyCompressed(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range []string{".z", ".zip", ".zoo", ".arc", ".lzh", ".arj"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// zipPayload is one member's bytes in the form the archive will store them.
type zipPayload struct {
	method    uint16
	crc       uint32
	data      []byte
	plainSize int64
}

// compressZipMember reads a file and picks the smaller of its stored and
// deflated forms, which is what keeps a tiny or already-compressed file from
// growing — the choice the original makes for every member.
func compressZipMember(source string, info os.FileInfo, store bool) (zipPayload, error) {
	file, err := os.Open(source) //nolint:gosec // zip reads a user-selected input after lstat validation.
	if err != nil {
		return zipPayload{}, err
	}
	defer file.Close()
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, opened) {
		return zipPayload{}, fmt.Errorf("file changed while archiving: %s", source)
	}
	plain, err := io.ReadAll(io.LimitReader(file, maxExpandedArchiveBytes+1))
	if err != nil {
		return zipPayload{}, err
	}
	if int64(len(plain)) > maxExpandedArchiveBytes {
		return zipPayload{}, fmt.Errorf("%s exceeds the 64 GiB limit", source)
	}
	payload := zipPayload{
		method:    archivezip.Store,
		crc:       crc32.ChecksumIEEE(plain),
		data:      plain,
		plainSize: int64(len(plain)),
	}
	if store || zipAlreadyCompressed(source) {
		return payload, nil
	}
	var buffer bytes.Buffer
	compressor, err := flate.NewWriter(&buffer, flate.DefaultCompression)
	if err != nil {
		return zipPayload{}, err
	}
	if _, err := compressor.Write(plain); err != nil {
		return zipPayload{}, err
	}
	if err := compressor.Close(); err != nil {
		return zipPayload{}, err
	}
	if buffer.Len() < len(plain) {
		payload.method, payload.data = archivezip.Deflate, buffer.Bytes()
	}
	return payload, nil
}

type unzipOptions struct {
	archive   string
	directory string
	// matched records whether any member was selected, which is what the
	// "filename not matched" caution and its status 11 key off.
	matched     bool
	list        bool
	verboseList bool
	test        bool
	pipe        bool
	overwrite   bool
	never       bool
	junk        bool
	quiet       int
	members     []string
	excludes    []string
}

func cmdUnzip(args []string) int {
	opts, err := parseUnzipOptions(args)
	if err != nil {
		fatalf("unzip", "%v", err)
		return 1
	}
	return runUnzip(&opts)
}

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func parseUnzipOptions(args []string) (unzipOptions, error) {
	opts := unzipOptions{directory: "."}
	// Everything after -x is an exclusion, and everything after the archive is
	// a member pattern, which is how Info-ZIP reads its command line.
	excluding := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			for _, operand := range args[i+1:] {
				switch {
				case opts.archive == "":
					opts.archive = operand
				case excluding:
					opts.excludes = append(opts.excludes, operand)
				default:
					opts.members = append(opts.members, operand)
				}
			}
			break
		}
		if len(arg) > 1 && arg[0] == '-' {
			if arg == "-d" || arg == "-x" {
				if arg == "-x" {
					excluding = true
					continue
				}
				i++
				if i >= len(args) {
					return opts, fmt.Errorf("option -d requires an argument")
				}
				opts.directory = args[i]
				continue
			}
			for _, letter := range arg[1:] {
				switch letter {
				case 'l':
					opts.list = true
				case 'v':
					opts.list, opts.verboseList = true, true
				case 't':
					opts.test = true
				case 'p':
					opts.pipe, opts.quiet = true, 2
				case 'c':
					opts.pipe = true
				case 'o':
					opts.overwrite, opts.never = true, false
				case 'n':
					opts.never, opts.overwrite = true, false
				case 'j':
					opts.junk = true
				case 'q':
					opts.quiet++
				case 'a', 'b', 'C', 'K', 'L', 'U', 'X', 'V', 'W', 'D':
					// Accepted for compatibility: there is no text conversion,
					// no case-insensitive matching and no attribute restore.
				default:
					return opts, fmt.Errorf("invalid option -- '%c'", letter)
				}
			}
			continue
		}
		switch {
		case opts.archive == "":
			opts.archive = arg
		case excluding:
			opts.excludes = append(opts.excludes, arg)
		default:
			opts.members = append(opts.members, arg)
		}
	}
	if opts.archive == "" || opts.archive == "-" {
		return opts, fmt.Errorf("a regular archive file is required")
	}
	return opts, nil
}

// selects reports whether a member was asked for: every one when no pattern
// was given, and never one an -x pattern covers. The patterns are globs in
// which "*" crosses a slash, as Info-ZIP's own matching does.
func (o *unzipOptions) selects(name string) bool {
	for _, pattern := range o.excludes {
		if tarPatternMatch(pattern, name) {
			return false
		}
	}
	if len(o.members) == 0 {
		return true
	}
	for _, pattern := range o.members {
		if tarPatternMatch(pattern, name) || pattern == name {
			return true
		}
	}
	return false
}

// runUnzip drives one unzip run and returns the status the original would.
func runUnzip(opts *unzipOptions) int {
	archive, err := archivezip.OpenReader(opts.archive)
	if err != nil {
		// Info-ZIP names all three spellings it looked for.
		fmt.Fprintf(os.Stderr, "unzip:  cannot find or open %s, %s.zip or %s.ZIP.\n",
			opts.archive, opts.archive, opts.archive)
		return 9
	}
	defer archive.Close()
	if opts.quiet < 1 {
		fmt.Printf("Archive:  %s\n", opts.archive)
	}
	if opts.list {
		return listZip(opts, archive)
	}
	if opts.test {
		return testZip(opts, archive)
	}
	if err := extractZip(opts, archive); err != nil {
		fatalf("unzip", "%v", err)
		return 2
	}
	if !opts.matched && len(opts.members) > 0 {
		for _, pattern := range opts.members {
			fmt.Printf("caution: filename not matched:  %s\n", pattern)
		}
		return 11
	}
	return 0
}

// reportName is the path the original names in its output: the member's own,
// under the -d directory when one was given.
func (o *unzipOptions) reportName(name string) string {
	if o.directory == "." || o.directory == "" {
		return name
	}
	joined := path.Join(filepath.ToSlash(o.directory), name)
	if strings.HasSuffix(name, "/") {
		joined += "/"
	}
	return joined
}

// report prints one member's line in the original's own layout: the verb is
// right-aligned in eight columns with "ing:" after it, and the name is padded
// so that a status column would line up.
func (o *unzipOptions) report(verb, name, status string) {
	if o.quiet > 0 {
		return
	}
	if strings.HasSuffix(name, "/") {
		fmt.Printf("%8sing: %s%s\n", verb, name, status)
		return
	}
	fmt.Printf("%8sing: %-22s  %s\n", verb, name, status)
}

// listZip prints the -l and -v tables, whose columns are Info-ZIP's own.
func listZip(opts *unzipOptions, archive *archivezip.ReadCloser) int {
	if opts.verboseList {
		fmt.Println(" Length   Method    Size  Cmpr    Date    Time   CRC-32   Name")
		fmt.Println("--------  ------  ------- ---- ---------- ----- --------  ----")
	} else {
		fmt.Println("  Length      Date    Time    Name")
		fmt.Println("---------  ---------- -----   ----")
	}
	var total, compressed uint64
	count := 0
	for _, member := range archive.File {
		if !opts.selects(member.Name) {
			continue
		}
		opts.matched = true
		count++
		total += member.UncompressedSize64
		compressed += member.CompressedSize64
		stamp := member.Modified.Format("2006-01-02 15:04")
		if !opts.verboseList {
			fmt.Printf("%9d  %s   %s\n", member.UncompressedSize64, stamp, member.Name)
			continue
		}
		fmt.Printf("%8d  %-6s %8d %3s%% %s %08x  %s\n", member.UncompressedSize64,
			zipMethodName(member.Method), member.CompressedSize64,
			zipRatio(member.UncompressedSize64, member.CompressedSize64),
			stamp, member.CRC32, member.Name)
	}
	noun := "files"
	if count == 1 {
		noun = "file"
	}
	if opts.verboseList {
		fmt.Println("--------          -------  ---                            -------")
		fmt.Printf("%8d         %8d %3s%%                            %d %s\n",
			total, compressed, zipRatio(total, compressed), count, noun)
	} else {
		fmt.Println("---------                     -------")
		fmt.Printf("%9d                     %d %s\n", total, count, noun)
	}
	if !opts.matched && len(opts.members) > 0 {
		return 11
	}
	return 0
}

// zipMethodName is the short method name the -v table prints. Deflate carries
// the level letter the original derives from the general-purpose flags.
func zipMethodName(method uint16) string {
	switch method {
	case archivezip.Store:
		return "Stored"
	case archivezip.Deflate:
		return "Defl:N"
	}
	return "Unk:" + strconv.Itoa(int(method))
}

// zipRatio is the saving Info-ZIP prints, rounded the way it rounds.
func zipRatio(uncompressed, compressed uint64) string {
	if uncompressed == 0 {
		return "0"
	}
	saved := (uncompressed - compressed) * 100
	return strconv.FormatUint((saved+uncompressed/2)/uncompressed, 10)
}

// testZip reads every selected member through, reporting each one the way -t
// reports it.
func testZip(opts *unzipOptions, archive *archivezip.ReadCloser) int {
	status := 0
	for _, member := range archive.File {
		if !opts.selects(member.Name) {
			continue
		}
		opts.matched = true
		reader, err := member.Open()
		if err == nil {
			_, err = io.Copy(io.Discard, reader) //nolint:gosec // G110: nothing is stored, so a large member costs only time.
			if closeErr := reader.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			fmt.Printf("    testing: %-22s   %v\n", member.Name, err)
			status = 2
			continue
		}
		if opts.quiet < 1 {
			fmt.Printf("    testing: %-22s   OK\n", member.Name)
		}
	}
	if status == 0 && opts.quiet < 2 {
		// One -q drops the per-member lines but keeps this summary; only -qq
		// silences it too.
		fmt.Printf("No errors detected in compressed data of %s.\n", opts.archive)
	}
	return status
}

func extractZip(opts *unzipOptions, archive *archivezip.ReadCloser) error {
	root, err := filepath.Abs(opts.directory)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil { //nolint:gosec // extraction root uses conventional permissions.
		return err
	}
	var directories []zipDirectory
	var extracted uint64
	for _, member := range archive.File {
		if !opts.selects(member.Name) {
			continue
		}
		opts.matched = true
		if member.UncompressedSize64 > uint64(maxExpandedArchiveBytes)-extracted {
			return fmt.Errorf("archive exceeds the 64 GiB extraction limit")
		}
		name := member.Name
		if opts.junk {
			// -j drops the directories a member is stored under.
			if base := path.Base(strings.TrimSuffix(name, "/")); !strings.HasSuffix(name, "/") {
				name = base
			} else {
				continue
			}
		}
		if opts.pipe {
			// -p and -c write the members out instead of unpacking them; -c
			// also names each one first and separates them with a blank line.
			verb := "extract"
			if member.Method != archivezip.Store {
				verb = "inflat"
			}
			// -c names a directory member the same way it names a file, with
			// nothing to write out for it.
			if opts.quiet < 1 {
				fmt.Printf("%8sing: %-22s  \n", verb, opts.reportName(name))
			}
			if strings.HasSuffix(member.Name, "/") {
				if opts.quiet < 1 {
					fmt.Println()
				}
				continue
			}
			reader, openErr := member.Open()
			if openErr != nil {
				return openErr
			}
			_, copyErr := io.Copy(os.Stdout, io.LimitReader(reader, maxExpandedArchiveBytes))
			if closeErr := reader.Close(); copyErr == nil {
				copyErr = closeErr
			}
			if copyErr != nil {
				return copyErr
			}
			if opts.quiet < 1 {
				fmt.Println()
			}
			continue
		}
		target, err := safeTarTarget(root, name)
		if err != nil {
			return err
		}
		mode := member.Mode()
		if member.FileInfo().IsDir() || strings.HasSuffix(member.Name, "/") {
			if err := ensureTarParents(root, target); err != nil {
				return err
			}
			if info, statErr := os.Lstat(target); statErr == nil && !info.IsDir() {
				return fmt.Errorf("refusing to replace non-directory path %q", member.Name)
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			directories = append(directories, zipDirectory{path: target, mode: mode, modified: member.Modified})
			opts.report("creat", opts.reportName(name), "")
			continue
		}
		if mode&os.ModeSymlink != 0 {
			if err := extractZipSymlink(member, root, target); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("unsupported archive member type for %q", member.Name)
		}
		// -n keeps what is already there; without -o the original would ask,
		// and this build takes the conservative answer rather than prompting.
		if _, statErr := os.Lstat(target); statErr == nil && !opts.overwrite {
			continue
		}
		reader, err := member.Open()
		if err != nil {
			return err
		}
		verb := "extract"
		if member.Method != archivezip.Store {
			verb = "inflat"
		}
		opts.report(verb, opts.reportName(name), "")
		header := &archiveRegularHeader{size: member.UncompressedSize64, modified: member.Modified}
		err = extractArchiveRegular(reader, root, target, header, mode)
		closeErr := reader.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		extracted += member.UncompressedSize64
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Chmod(directories[index].path, directories[index].mode.Perm()); err != nil {
			return err
		}
		if !directories[index].modified.IsZero() {
			if err := os.Chtimes(directories[index].path, directories[index].modified, directories[index].modified); err != nil {
				return err
			}
		}
	}
	return nil
}

type zipDirectory struct {
	path     string
	mode     os.FileMode
	modified time.Time
}

// archiveRegularHeader keeps extraction's generic fields distinct from
// archive/tar's richer header type, so non-tar formats share one safe writer.
type archiveRegularHeader struct {
	size     uint64
	modified time.Time
}

func extractArchiveRegular(reader io.Reader, root, target string, header *archiveRegularHeader, mode os.FileMode) error {
	if header.size > uint64(maxExpandedArchiveBytes) {
		return fmt.Errorf("archive exceeds the 64 GiB extraction limit")
	}
	if err := ensureTarParents(root, target); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace non-regular path %q", target)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".ba6-zip-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	installed := false
	defer func() {
		_ = file.Close()
		if !installed {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := io.CopyN(file, reader, int64(header.size)); err != nil {
		return err
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if !header.modified.IsZero() {
		if err := os.Chtimes(temporary, header.modified, header.modified); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	installed = true
	return nil
}

func extractZipSymlink(member *archivezip.File, root, target string) error {
	reader, err := member.Open()
	if err != nil {
		return err
	}
	targetBytes, readErr := io.ReadAll(io.LimitReader(reader, 4097))
	closeErr := reader.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(targetBytes) > 4096 {
		return fmt.Errorf("symbolic link target is too long for %q", member.Name)
	}
	link := string(targetBytes)
	if err := ensureTarParents(root, target); err != nil {
		return err
	}
	if err := validateTarSymlink(root, target, member.Name, link); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("refusing to replace existing path %q with a symbolic link", member.Name)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(link, target)
}
