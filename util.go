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

// The two character sets below were derived by probing GNU coreutils rather
// than read off any rule: shellNameBare holds the bytes it prints unquoted,
// and shellNameDoubleQuotable the bytes it is willing to wrap in double quotes
// instead of single ones. They overlap but neither contains the other -- '{'
// is fine bare yet forces single quotes, while ' ' and ':' are the reverse.
const (
	shellNameBare           = "#%+,-./@]_{}~"
	shellNameDoubleQuotable = " %'+,-./:@]_"
)

func shellNameByteIsAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// shellNameByteIsSafe reports whether b can appear in a diagnostic unquoted.
func shellNameByteIsSafe(b byte) bool {
	if shellNameByteIsAlnum(b) || b >= 0x80 { // >= 0x80: multibyte text in a UTF-8 locale
		return true
	}
	return strings.IndexByte(shellNameBare, b) >= 0
}

// quoteLocaleName renders a value the way the originals' quote() does in the C
// locale: always inside single quotes, with backslashes and control bytes
// escaped. sort uses it to echo back a rejected -t argument.
func quoteLocaleName(value string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for i := 0; i < len(value); i++ {
		switch c := value[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&b, `\%03o`, c)
				continue
			}
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// quoteForceName quotes a name the way the tools that always quote do (touch's
// "cannot touch 'x'", for one): the same rules as shellQuoteName, except that a
// name needing no escaping still gets a plain pair of single quotes.
func quoteForceName(name string) string {
	if quoted := shellQuoteName(name); quoted != name {
		return quoted
	}
	return "'" + name + "'"
}

// shellQuoteName renders name the way the GNU tools quote a path in a
// diagnostic: bare when every byte is unambiguous, and otherwise quoted so the
// result could be pasted back into a shell. Control characters need ANSI-C
// $'..' escapes, so a single-quoted string alone is not always enough.
func shellQuoteName(name string) string {
	if name == "" {
		// An empty name still needs a visible pair of quotes.
		return "''"
	}
	safe, hasSingleQuote, doubleQuotable := true, false, true
	for index := 0; index < len(name); index++ {
		b := name[index]
		if !shellNameByteIsSafe(b) {
			safe = false
		}
		if b == '\'' {
			hasSingleQuote = true
		}
		if !shellNameByteIsAlnum(b) && b < 0x80 && strings.IndexByte(shellNameDoubleQuotable, b) < 0 {
			doubleQuotable = false
		}
	}
	if safe {
		return name
	}
	// Double quotes are the shorter rendering when the single quote is the
	// only reason to quote at all, and are what the originals then choose.
	if hasSingleQuote && doubleQuotable {
		return `"` + name + `"`
	}
	// The originals emit one quoted run, stepping outside it for an escaped
	// quote or an ANSI-C group. An escaped quote leaves the run open, so an
	// empty '' closes it before anything that is not itself a quoted run --
	// another group, or the end of the name.
	var out strings.Builder
	quoted, pendingEmpty := false, false
	for index := 0; index < len(name); index++ {
		b := name[index]
		switch {
		case b == '\'':
			if quoted {
				out.WriteByte('\'')
				quoted = false
			} else if pendingEmpty {
				out.WriteString("''")
			}
			out.WriteString(`\'`)
			pendingEmpty = true
		case b < 0x20 || b == 0x7f:
			if quoted {
				out.WriteByte('\'')
				quoted = false
			} else if pendingEmpty {
				out.WriteString("''")
			}
			// Adjacent control characters share one $'..' group, as in the
			// originals' output.
			out.WriteString("$'")
			for ; index < len(name) && (name[index] < 0x20 || name[index] == 0x7f); index++ {
				out.WriteString(ansiCEscape(name[index]))
			}
			index--
			out.WriteByte('\'')
			pendingEmpty = false
		default:
			if !quoted {
				out.WriteByte('\'')
				quoted = true
			}
			out.WriteByte(b)
			pendingEmpty = false
		}
	}
	switch {
	case quoted:
		out.WriteByte('\'')
	case pendingEmpty:
		out.WriteString("''")
	}
	return out.String()
}

func ansiCEscape(b byte) string {
	switch b {
	case '\a':
		return `\a`
	case '\b':
		return `\b`
	case '\t':
		return `\t`
	case '\n':
		return `\n`
	case '\v':
		return `\v`
	case '\f':
		return `\f`
	case '\r':
		return `\r`
	}
	return fmt.Sprintf(`\%03o`, b)
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
