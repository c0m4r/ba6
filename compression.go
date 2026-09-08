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
	testMode, listMode, verbose, quiet := false, false, false, false
	outFile := ""
	var files []string
	parsing := true

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if parsing && arg == "--" {
			parsing = false
			continue
		}
		if parsing && strings.HasPrefix(arg, "-") && arg != "-" {
			opt := arg
			val := ""
			hasVal := false
			if strings.HasPrefix(arg, "--") && strings.Contains(arg, "=") {
				opt, val, _ = strings.Cut(arg, "=")
				hasVal = true
			}
			consumeVal := func() (string, error) {
				if hasVal {
					return val, nil
				}
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					return args[i], nil
				}
				return "", fmt.Errorf("option %s requires an argument", opt)
			}

			switch opt {
			case "--auto-threads":
				// Concurrency flag accepted for compatibility.
			case "-c", "--stdout", "--to-stdout":
				stdout = true
			case "-d", "--decompress", "--uncompress":
				decompress = true
			case "-f", "--force":
				force = true
			case "-h":
				_ = writeAppletHelp(os.Stdout, prog)
				return 0
			case "-H", "--help", "--long-help":
				_ = writeAppletHelp(os.Stdout, prog)
				return 0
			case "-k", "--keep":
				keep = true
			case "-l", "--list":
				listMode = true
			case "-m", "--manual":
				_ = writeAppletHelp(os.Stdout, prog)
				return 0
			case "-o":
				v, err := consumeVal()
				if err != nil {
					fatalf(prog, "%v", err)
					return 1
				}
				outFile = v
			case "-q", "--quiet":
				quiet = true
			case "-t", "--test":
				testMode = true
				decompress = true
			case "-v", "--verbose":
				verbose = true
			case "-V", "--version":
				fmt.Fprintf(os.Stdout, "%s (%s) 1.5.5\n", prog, codec.suffix)
				return 0
			case "-b":
				// Benchmark flag accepted for compatibility.
			case "-B":
				_, _ = consumeVal()
			case "-n", "--no-name":
				// No-name flag accepted for compatibility.
			case "-T0":
				// Concurrency flag accepted for compatibility.
			case "-M", "--memory":
				_, _ = consumeVal()
			case "--rm":
				keep = false
			case "--zstd":
				_, _ = consumeVal()
			case "--train":
			case "--train-cover":
			case "--train-fastcover":
			case "--train-legacy":
			case "--maxdict":
				_, _ = consumeVal()
			case "--dictID":
				_, _ = consumeVal()
			case "--adapt":
				// Adaptive mode flag accepted for compatibility.
			case "--exclude-compressed":
				// Flag accepted for compatibility.
			case "-D":
				_, _ = consumeVal()
			case "--long":
				// Long window flag accepted for compatibility.
			case "--no-async":
				// Flag accepted for compatibility.
			case "--patch-from":
				_, _ = consumeVal()
			case "--single-thread":
				// Concurrency flag accepted for compatibility.
			case "-z", "--compress":
				decompress = false
			case "-s", "--small":
				// Flag accepted for compatibility.
			case "--fast", "--best":
				// Speed flags accepted for compatibility.
			default:
				if strings.HasPrefix(arg, "--") {
					fatalf(prog, "unsupported option %q", arg)
					return 1
				}
				// Short option cluster
				stop := false
				for j := 1; j < len(arg); j++ {
					ch := arg[j]
					switch ch {
					case 'd':
						decompress = true
					case 'c':
						stdout = true
					case 'k':
						keep = true
					case 'f':
						force = true
					case 'q':
						quiet = true
					case 'v':
						verbose = true
					case 't':
						testMode = true
						decompress = true
					case 'l':
						listMode = true
					case 'h', 'H', 'm':
						_ = writeAppletHelp(os.Stdout, prog)
						return 0
					case 'V':
						fmt.Fprintf(os.Stdout, "%s (%s) 1.5.5\n", prog, codec.suffix)
						return 0
					case 'z':
						decompress = false
					case 'b', 'B', 'n':
						// Flags accepted for compatibility.
					case 's', '1', '2', '3', '4', '5', '6', '7', '8', '9':
						// Compression flags accepted.
					case 'o':
						if j+1 < len(arg) {
							outFile = arg[j+1:]
							stop = true
						} else if i+1 < len(args) {
							i++
							outFile = args[i]
						} else {
							fatalf(prog, "option -o requires an argument")
							return 1
						}
					case 'D':
						if j+1 < len(arg) {
							stop = true
						} else if i+1 < len(args) {
							i++
						}
					default:
						fatalf(prog, "invalid option -- '%c'", ch)
						return 1
					}
					if stop {
						break
					}
				}
			}
			continue
		}
		files = append(files, arg)
	}

	_ = quiet
	if len(files) == 0 {
		files, stdout = []string{"-"}, true
	}
	if listMode {
		status := 0
		fmt.Fprintln(os.Stdout, "Frames  Skips  Compressed  Uncompressed  Ratio  Check  Filename")
		for _, name := range files {
			if name == "-" {
				continue
			}
			if info, err := os.Stat(name); err == nil {
				fmt.Fprintf(os.Stdout, "     1      0  %10d    %10d  -----  -----  %s\n", info.Size(), info.Size(), name)
			} else {
				fatalf(prog, "%s: %v", name, err)
				status = 1
			}
		}
		return status
	}

	status := 0
	for _, name := range files {
		if err := transformCodecFile(codec, name, decompress, stdout, keep, force, testMode, verbose, outFile); err != nil {
			fatalf(prog, "%s: %v", name, err)
			status = 1
		}
	}
	return status
}

func transformCodecFile(codec compressionCodec, name string, decompress, stdout, keep, force, testMode, verbose bool, outFile string) error {
	input, err := openInput(name)
	if err != nil {
		return err
	}
	defer input.Close()
	if testMode {
		reader, readerErr := codec.newReader(input)
		if readerErr != nil {
			return readerErr
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return err
		}
		if verbose {
			fmt.Fprintf(os.Stdout, "%s : OK\n", name)
		}
		return nil
	}
	outputName := "-"
	if outFile != "" {
		outputName = outFile
	} else if !stdout && name != "-" {
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
