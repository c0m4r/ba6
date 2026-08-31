// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type tarOptions struct {
	operation byte
	archive   string
	directory string
	gzip      bool
	verbose   bool
	keepOld   bool
	files     []string
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
		err = createTar(opts)
	case 'x':
		err = extractTar(opts)
	case 't':
		err = listTar(opts)
	}
	if err != nil {
		fatalf("tar", "%v", err)
		return 1
	}
	return 0
}

func parseTarOptions(args []string) (tarOptions, error) {
	opts := tarOptions{archive: "-", directory: "."}
	args = append([]string(nil), args...)
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' && strings.Trim(args[0], "ctxzvfkC") == "" {
		args[0] = "-" + args[0]
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			opts.files = append(opts.files, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "--") {
			switch arg {
			case "--create":
				opts.operation = 'c'
			case "--extract", "--get":
				opts.operation = 'x'
			case "--list":
				opts.operation = 't'
			case "--gzip":
				opts.gzip = true
			case "--verbose":
				opts.verbose = true
			case "--keep-old-files":
				opts.keepOld = true
			default:
				return opts, fmt.Errorf("invalid option %q", arg)
			}
			continue
		}
		if len(arg) > 1 && arg[0] == '-' {
			for pos := 1; pos < len(arg); pos++ {
				option := arg[pos]
				switch option {
				case 'c', 'x', 't':
					if opts.operation != 0 && opts.operation != arg[pos] {
						return opts, fmt.Errorf("multiple operations specified")
					}
					opts.operation = arg[pos]
				case 'z':
					opts.gzip = true
				case 'v':
					opts.verbose = true
				case 'k':
					opts.keepOld = true
				case 'f', 'C':
					var value string
					if pos+1 < len(arg) {
						value = arg[pos+1:]
						pos = len(arg)
					} else {
						i++
						if i >= len(args) {
							return opts, fmt.Errorf("option -%c requires an argument", arg[pos])
						}
						value = args[i]
					}
					if option == 'f' {
						opts.archive = value
					} else {
						opts.directory = value
					}
				default:
					return opts, fmt.Errorf("invalid option -- '%c'", option)
				}
			}
			continue
		}
		opts.files = append(opts.files, arg)
	}
	if opts.operation == 0 {
		return opts, fmt.Errorf("one of -c, -x, or -t is required")
	}
	if opts.operation == 'c' && len(opts.files) == 0 {
		return opts, fmt.Errorf("refusing to create an empty archive")
	}
	if opts.operation != 'c' && len(opts.files) > 0 {
		return opts, fmt.Errorf("member selection is not supported")
	}
	return opts, nil
}

func createTar(opts tarOptions) (retErr error) {
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
	archiveWriter := output
	var gzipWriter *gzip.Writer
	if opts.gzip {
		gzipWriter = gzip.NewWriter(output)
		archiveWriter = gzipWriter
	}
	tarWriter := tar.NewWriter(archiveWriter)
	for _, operand := range opts.files {
		source := operand
		if !filepath.IsAbs(source) {
			source = filepath.Join(base, source)
		}
		archiveName := filepath.Clean(operand)
		if filepath.IsAbs(archiveName) {
			archiveName = strings.TrimLeft(filepath.ToSlash(archiveName), "/")
		}
		archiveName = filepath.ToSlash(archiveName)
		if err := addTarPath(tarWriter, source, archiveName, opts.verbose); err != nil {
			tarWriter.Close()
			if gzipWriter != nil {
				gzipWriter.Close()
			}
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if gzipWriter != nil {
		if err := gzipWriter.Close(); err != nil {
			return err
		}
	}
	return nil
}

func validateTarCreateSources(opts tarOptions, base string) error {
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

func addTarPath(writer *tar.Writer, source, archiveName string, verbose bool) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name := archiveName
		if relative != "." {
			name = filepath.ToSlash(filepath.Join(archiveName, relative))
		}
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		header.Name = name
		if info.IsDir() && !strings.HasSuffix(header.Name, "/") {
			header.Name += "/"
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if verbose {
			fmt.Fprintln(os.Stderr, name)
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
	})
}

func openTarReader(opts tarOptions) (io.ReadCloser, *tar.Reader, error) {
	input, err := openInput(opts.archive)
	if err != nil {
		return nil, nil, err
	}
	var reader io.Reader = input
	if opts.gzip {
		gzipReader, gzipErr := gzip.NewReader(input)
		if gzipErr != nil {
			input.Close()
			return nil, nil, gzipErr
		}
		reader = gzipReader
		return &combinedReadCloser{Reader: gzipReader, closers: []io.Closer{gzipReader, input}}, tar.NewReader(reader), nil
	}
	return input, tar.NewReader(reader), nil
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

func listTar(opts tarOptions) error {
	input, reader, err := openTarReader(opts)
	if err != nil {
		return err
	}
	defer input.Close()
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			return nil
		}
		if nextErr != nil {
			return nextErr
		}
		if _, err := fmt.Fprintln(os.Stdout, header.Name); err != nil {
			return err
		}
	}
}

func extractTar(opts tarOptions) error {
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
		target, err := safeTarTarget(root, header.Name)
		if err != nil {
			return err
		}
		if opts.verbose {
			fmt.Fprintln(os.Stderr, header.Name)
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
				return fmt.Errorf("refusing to replace non-directory path %q", header.Name)
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
					return fmt.Errorf("refusing to replace existing path %q", header.Name)
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("refusing to replace non-regular path %q", header.Name)
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
			if err := validateTarSymlink(root, target, header.Name, header.Linkname); err != nil {
				return err
			}
			if err := extractTarSymlink(target, header.Linkname, header.Name, opts.keepOld); err != nil {
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
			return fmt.Errorf("unsupported archive member type for %q", header.Name)
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

func cmdGzip(args []string) int {
	decompress, stdout, keep, force := false, false, false, false
	var files []string
	parsing := true
	for _, arg := range args {
		if parsing && arg == "--" {
			parsing = false
			continue
		}
		if parsing && len(arg) > 1 && arg[0] == '-' {
			for _, flag := range arg[1:] {
				switch flag {
				case 'd':
					decompress = true
				case 'c':
					stdout = true
				case 'k':
					keep = true
				case 'f':
					force = true
				default:
					fatalf("gzip", "invalid option -- '%c'", flag)
					return 1
				}
			}
			continue
		}
		files = append(files, arg)
	}
	if len(files) == 0 {
		files, stdout = []string{"-"}, true
	}
	status := 0
	for _, name := range files {
		if err := transformGzipFile(name, decompress, stdout, keep, force); err != nil {
			fatalf("gzip", "%s: %v", name, err)
			status = 1
		}
	}
	return status
}

func cmdGunzip(args []string) int {
	return cmdGzip(append([]string{"-d"}, args...))
}

func transformGzipFile(name string, decompress, stdout, keep, force bool) (retErr error) {
	input, err := openInput(name)
	if err != nil {
		return err
	}
	defer input.Close()
	outputName := "-"
	if !stdout && name != "-" {
		if decompress {
			switch {
			case strings.HasSuffix(name, ".gz"):
				outputName = strings.TrimSuffix(name, ".gz")
			case strings.HasSuffix(name, ".tgz"):
				outputName = strings.TrimSuffix(name, ".tgz") + ".tar"
			default:
				return fmt.Errorf("unknown suffix -- use -c")
			}
		} else {
			outputName = name + ".gz"
		}
	}
	var output io.WriteCloser
	temporaryName := ""
	if outputName == "-" {
		output = nopWriteCloser{os.Stdout}
	} else {
		if _, statErr := os.Lstat(outputName); statErr == nil && !force {
			return fmt.Errorf("output file exists")
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
	if decompress {
		reader, gzipErr := gzip.NewReader(input)
		if gzipErr != nil {
			output.Close()
			return gzipErr
		}
		written, copyErr := io.Copy(output, io.LimitReader(reader, maxExpandedArchiveBytes+1)) //nolint:gosec // G110: output is explicitly capped at 64 GiB.
		err = copyErr
		if err == nil && written > maxExpandedArchiveBytes {
			err = fmt.Errorf("decompressed data exceeds the 64 GiB limit")
		}
		if closeErr := reader.Close(); err == nil {
			err = closeErr
		}
	} else {
		writer := gzip.NewWriter(output)
		if name != "-" {
			writer.Name = filepath.Base(name)
			if info, statErr := os.Stat(name); statErr == nil {
				writer.ModTime = info.ModTime()
			}
		}
		_, err = io.Copy(writer, input)
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
		if err := os.Rename(temporaryName, outputName); err != nil {
			return err
		}
	}
	success = true
	if name != "-" && outputName != "-" && !keep {
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
