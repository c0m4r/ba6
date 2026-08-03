package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
	"unsafe"
)

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
		fatalf(prog, "%s: %v", name, err)
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
