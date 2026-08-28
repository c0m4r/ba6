// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"
)

// expandShortOptions rewrites clustered short options into separate arguments
// so a parser that matches whole arguments still accepts every form the
// originals do: "-qn2" becomes "-q -n 2" and "-n2" becomes "-n 2". Letters
// listed in withValue consume the rest of their cluster as the value, which is
// why the scan stops there. Long options, "--", "-" and operands pass through
// untouched.
func expandShortOptions(args []string, withValue string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if len(arg) < 3 || arg[0] != '-' || arg[1] == '-' {
			out = append(out, arg)
			continue
		}
		for j := 1; j < len(arg); j++ {
			out = append(out, "-"+string(arg[j]))
			if strings.IndexByte(withValue, arg[j]) >= 0 {
				if j+1 < len(arg) {
					out = append(out, arg[j+1:])
				}
				break
			}
		}
	}
	return out
}

// errText renders err the way the C tools do: the strerror(3) sentence on its
// own. Go wraps syscall errors in *os.PathError and friends, which prepend the
// operation and the path ("lstat f: no such file or directory"); the originals
// print their own wording around a bare, capitalised message instead.
func errText(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		text := errno.Error()
		if r, size := utf8.DecodeRuneInString(text); size > 0 && unicode.IsLower(r) {
			return string(unicode.ToUpper(r)) + text[size:]
		}
		return text
	}
	return err.Error()
}

func humanSizeUint64(value uint64) string {
	if value > math.MaxInt64 {
		return fmt.Sprintf("%.1fE", float64(value)/(1024*1024*1024*1024*1024*1024))
	}
	return humanSize(int64(value))
}

// openInput returns a reader for the given path. The conventional "-" means
// standard input. The caller is responsible for closing the returned io.Closer
// (which is a no-op for stdin).
func openInput(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(path)
}

// maxScanLine bounds a single input line for the scanner-based applets. Lines
// longer than this are reported as an error rather than silently truncated.
const maxScanLine = 64 * 1024 * 1024

// newLineScanner returns a bufio.Scanner configured to read lines up to
// maxScanLine bytes. Callers must check scanErr after iterating so an
// over-long line surfaces as a loud failure instead of silent data loss.
func newLineScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxScanLine)
	return sc
}

// scanErr reports a non-EOF scanner error (e.g. a line exceeding maxScanLine)
// for the named source. It returns true if an error was reported.
func scanErr(prog, name string, sc *bufio.Scanner) bool {
	if err := sc.Err(); err != nil {
		if name == "-" || name == "" {
			name = "standard input"
		}
		// Each original words a failed read its own way; cut and grep simply
		// name the file.
		switch prog {
		case "sort":
			fatalf(prog, "read failed: %s: %s", name, errText(err))
		case "uniq":
			fatalf(prog, "error reading '%s': %s", name, errText(err))
		default:
			fatalf(prog, "%s: %s", name, errText(err))
		}
		return true
	}
	return false
}

// atimeOf extracts the access time from a FileInfo on Linux, falling back to
// the modification time if the underlying stat data is unavailable.
func atimeOf(info os.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Atim.Sec, st.Atim.Nsec)
	}
	return info.ModTime()
}

// fileModeFromOctal converts traditional Unix permission bits into Go's
// FileMode representation, whose set-id and sticky bits live outside Perm().
func fileModeFromOctal(bits uint64) os.FileMode {
	mode := os.FileMode(bits & 0o777)
	if bits&0o4000 != 0 {
		mode |= os.ModeSetuid
	}
	if bits&0o2000 != 0 {
		mode |= os.ModeSetgid
	}
	if bits&0o1000 != 0 {
		mode |= os.ModeSticky
	}
	return mode
}

// octalFromFileMode is the inverse of fileModeFromOctal: it packs the
// permission and set-id/sticky bits back into a traditional 12-bit value.
func octalFromFileMode(mode os.FileMode) uint64 {
	bits := uint64(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		bits |= 0o1000
	}
	return bits
}

// isCrossDevice reports whether err is the EXDEV ("invalid cross-device link")
// error returned by rename(2) when source and destination are on different
// filesystems.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// winsize mirrors struct winsize from <termios.h>.
type winsize struct {
	rows, cols, xpix, ypix uint16
}

// unixWinsize returns the column count of the terminal on fd via the
// TIOCGWINSZ ioctl. It errors if fd is not a terminal.
func unixWinsize(fd uintptr) (int, error) {
	var ws winsize
	// ws is a fixed-size local struct; the kernel writes the window dimensions
	// into it via TIOCGWINSZ. This is the standard, audited way to query the
	// terminal size and cannot read or write out of bounds.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd,
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))) //nolint:gosec // G103: bounded ioctl into local struct
	if errno != 0 {
		return 0, errno
	}
	return int(ws.cols), nil
}
