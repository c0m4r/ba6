// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	archivezip "archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type zipOptions struct {
	archive   string
	recursive bool
	store     bool
	files     []string
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
	parsing := true
	for _, arg := range args {
		if parsing && arg == "--" {
			parsing = false
			continue
		}
		if parsing {
			switch arg {
			case "-r", "--recurse-paths":
				opts.recursive = true
				continue
			case "-0", "--store":
				opts.store = true
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unsupported option %q", arg)
			}
		}
		if opts.archive == "" {
			opts.archive = arg
		} else {
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
		if err := addZipPath(writer, operand, name, opts.recursive, opts.store); err != nil {
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

func addZipPath(writer *archivezip.Writer, source, name string, recursive, store bool) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if !strings.HasSuffix(name, "/") {
			name += "/"
		}
		if err := addZipEntry(writer, source, name, info, store); err != nil {
			return err
		}
		if !recursive {
			return nil
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if err := addZipPath(writer, filepath.Join(source, entry.Name()), filepath.ToSlash(filepath.Join(name, entry.Name())), true, store); err != nil {
				return err
			}
		}
		return nil
	}
	return addZipEntry(writer, source, name, info, store)
}

func addZipEntry(writer *archivezip.Writer, source, name string, info os.FileInfo, store bool) error {
	header, err := archivezip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	header.SetMode(info.Mode())
	if info.IsDir() {
		header.Method = archivezip.Store
		_, err := writer.CreateHeader(header)
		return err
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
	if store {
		header.Method = archivezip.Store
	} else {
		header.Method = archivezip.Deflate
	}
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	file, err := os.Open(source) //nolint:gosec // zip reads a user-selected input after lstat validation.
	if err != nil {
		return err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, opened) {
		file.Close()
		return fmt.Errorf("file changed while archiving: %s", source)
	}
	_, copyErr := io.Copy(entry, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

type unzipOptions struct {
	archive   string
	directory string
	list      bool
	members   map[string]bool
}

func cmdUnzip(args []string) int {
	opts, err := parseUnzipOptions(args)
	if err != nil {
		fatalf("unzip", "%v", err)
		return 1
	}
	if err := runUnzip(opts); err != nil {
		fatalf("unzip", "%v", err)
		return 1
	}
	return 0
}

func parseUnzipOptions(args []string) (unzipOptions, error) {
	opts := unzipOptions{directory: ".", members: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 >= len(args) {
				return opts, fmt.Errorf("missing archive")
			}
			if opts.archive == "" {
				opts.archive = args[i+1]
				i++
			}
			for _, member := range args[i+1:] {
				opts.members[member] = true
			}
			break
		}
		switch arg {
		case "-l", "--list":
			opts.list = true
		case "-d":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("option -d requires an argument")
			}
			opts.directory = args[i]
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unsupported option %q", arg)
			}
			if opts.archive == "" {
				opts.archive = arg
			} else {
				opts.members[arg] = true
			}
		}
	}
	if opts.archive == "" || opts.archive == "-" {
		return opts, fmt.Errorf("a regular archive file is required")
	}
	return opts, nil
}

func runUnzip(opts unzipOptions) error {
	archive, err := archivezip.OpenReader(opts.archive)
	if err != nil {
		return err
	}
	defer archive.Close()
	if opts.list {
		for _, member := range archive.File {
			if len(opts.members) == 0 || opts.members[member.Name] {
				if _, err := fmt.Fprintln(os.Stdout, member.Name); err != nil {
					return err
				}
			}
		}
		return nil
	}
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
		if len(opts.members) != 0 && !opts.members[member.Name] {
			continue
		}
		if member.UncompressedSize64 > uint64(maxExpandedArchiveBytes)-extracted {
			return fmt.Errorf("archive exceeds the 64 GiB extraction limit")
		}
		target, err := safeTarTarget(root, member.Name)
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
		reader, err := member.Open()
		if err != nil {
			return err
		}
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
