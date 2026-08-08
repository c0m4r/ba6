// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"compress/bzip2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type compressionCodec struct {
	suffix     string
	inputNames map[string]string
	newWriter  func(io.Writer) (io.WriteCloser, error)
	newReader  func(io.Reader) (io.Reader, error)
}

var (
	bzip2Codec = compressionCodec{
		suffix: ".bz2", inputNames: map[string]string{".bz2": "", ".tbz": ".tar", ".tbz2": ".tar"},
		newWriter: func(output io.Writer) (io.WriteCloser, error) { return newBzip2Writer(output) },
		newReader: func(input io.Reader) (io.Reader, error) { return bzip2.NewReader(input), nil },
	}
	xzCodec = compressionCodec{
		suffix: ".xz", inputNames: map[string]string{".xz": ""},
		newWriter: func(output io.Writer) (io.WriteCloser, error) { return newXZWriter(output) },
		newReader: newXZReader,
	}
	zstdCodec = compressionCodec{
		suffix: ".zst", inputNames: map[string]string{".zst": "", ".zstd": ""},
		newWriter: func(output io.Writer) (io.WriteCloser, error) { return newZstdWriter(output) },
		newReader: newZstdReader,
	}
)

func cmdBzip2(args []string) int { return cmdCodec("bzip2", bzip2Codec, args, false) }
func cmdBunzip2(args []string) int {
	return cmdCodec("bunzip2", bzip2Codec, append([]string{"-d"}, args...), true)
}
func cmdXz(args []string) int { return cmdCodec("xz", xzCodec, args, false) }
func cmdUnxz(args []string) int {
	return cmdCodec("unxz", xzCodec, append([]string{"-d"}, args...), true)
}
func cmdZstd(args []string) int { return cmdCodec("zstd", zstdCodec, args, false) }
func cmdUnzstd(args []string) int {
	return cmdCodec("unzstd", zstdCodec, append([]string{"-d"}, args...), true)
}

func cmdCodec(prog string, codec compressionCodec, args []string, decompress bool) int {
	stdout, keep, force := false, false, false
	files := []string{}
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
				case 'q':
					// The focused applets do not otherwise emit progress output.
				default:
					fatalf(prog, "invalid option -- '%c'", flag)
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
		if err := transformCodecFile(codec, name, decompress, stdout, keep, force); err != nil {
			fatalf(prog, "%s: %v", name, err)
			status = 1
		}
	}
	return status
}

func transformCodecFile(codec compressionCodec, name string, decompress, stdout, keep, force bool) error {
	input, err := openInput(name)
	if err != nil {
		return err
	}
	defer input.Close()
	outputName := "-"
	if !stdout && name != "-" {
		if decompress {
			outputName, err = codecOutputName(codec, name)
			if err != nil {
				return err
			}
		} else {
			outputName = name + codec.suffix
		}
	}
	output, temporary, err := openCodecOutput(outputName, force)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success && temporary != "" {
			_ = os.Remove(temporary)
		}
	}()
	if decompress {
		reader, readerErr := codec.newReader(input)
		if readerErr != nil {
			err = readerErr
		}
		if err != nil {
			output.Close()
			return err
		}
		written, copyErr := io.Copy(output, io.LimitReader(reader, maxExpandedArchiveBytes+1)) //nolint:gosec // decompression is capped at 64 GiB.
		if copyErr != nil {
			err = copyErr
		} else if written > maxExpandedArchiveBytes {
			err = fmt.Errorf("decompressed data exceeds the 64 GiB limit")
		}
	} else {
		writer, writerErr := codec.newWriter(output)
		if writerErr != nil {
			err = writerErr
		}
		if err == nil {
			_, err = io.Copy(writer, input)
			if closeErr := writer.Close(); err == nil {
				err = closeErr
			}
		}
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if temporary != "" {
		if err := os.Rename(temporary, outputName); err != nil {
			return err
		}
	}
	success = true
	if name != "-" && outputName != "-" && !keep {
		if err := input.Close(); err != nil {
			return err
		}
		return os.Remove(name)
	}
	return nil
}

func codecOutputName(codec compressionCodec, name string) (string, error) {
	suffixes := make([]string, 0, len(codec.inputNames))
	for suffix := range codec.inputNames {
		suffixes = append(suffixes, suffix)
	}
	sort.Slice(suffixes, func(i, j int) bool { return len(suffixes[i]) > len(suffixes[j]) })
	for _, suffix := range suffixes {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix) + codec.inputNames[suffix], nil
		}
	}
	return "", fmt.Errorf("unknown suffix -- use -c")
}

func openCodecOutput(name string, force bool) (io.WriteCloser, string, error) {
	if name == "-" {
		return nopWriteCloser{os.Stdout}, "", nil
	}
	if _, err := os.Lstat(name); err == nil && !force {
		return nil, "", fmt.Errorf("output file exists")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}
	file, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".tmp-*")
	if err != nil {
		return nil, "", err
	}
	return file, file.Name(), nil
}
