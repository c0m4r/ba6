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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// cbaudMask covers CBAUD and CBAUDEX, the c_cflag bits that hold the baud
// rate on Linux/amd64 (asm-generic/termbits.h). Extended rates (57600+) set
// CBAUDEX plus a low-nibble index into a second speed table; the B-constants
// below already encode that combined bit pattern.
const cbaudMask = 0o010017

var gettyBaudRates = map[string]uint32{
	"50": syscall.B50, "75": syscall.B75, "110": syscall.B110, "134": syscall.B134,
	"150": syscall.B150, "200": syscall.B200, "300": syscall.B300, "600": syscall.B600,
	"1200": syscall.B1200, "1800": syscall.B1800, "2400": syscall.B2400, "4800": syscall.B4800,
	"9600": syscall.B9600, "19200": syscall.B19200, "38400": syscall.B38400,
	"57600": syscall.B57600, "115200": syscall.B115200, "230400": syscall.B230400,
	"460800": syscall.B460800, "500000": syscall.B500000, "576000": syscall.B576000,
	"921600": syscall.B921600, "1000000": syscall.B1000000, "1152000": syscall.B1152000,
	"1500000": syscall.B1500000, "2000000": syscall.B2000000, "2500000": syscall.B2500000,
	"3000000": syscall.B3000000, "3500000": syscall.B3500000, "4000000": syscall.B4000000,
}

var errGettyTimeout = errors.New("timed out waiting for a login name")

type gettyOptions struct {
	autologin    string
	skipLogin    bool
	loginProgram string
	localLine    string // "auto", "always", or "never"
	chrootDir    string
	noClear      bool
	noIssue      bool
	eightBits    bool
	loginPause   bool
	timeout      int
}

func cmdGetty(args []string) int {
	opts, positional, ok := parseGettyArgs(args)
	if !ok {
		return 1
	}
	if len(positional) == 0 {
		fatalf("getty", "expected LINE [BAUD_RATE[,BAUD_RATE...]] [TERM]")
		return 1
	}
	line := positional[0]
	rest := positional[1:]
	baud := ""
	if len(rest) > 0 && isBaudList(rest[0]) {
		baud, rest = rest[0], rest[1:]
	}
	term := ""
	if len(rest) > 0 {
		term, rest = rest[0], rest[1:]
	}
	if len(rest) > 0 {
		fatalf("getty", "extra operand %q", rest[0])
		return 1
	}

	if err := gettyOpenTerminal(line); err != nil {
		fatalf("getty", "%v", err)
		return 1
	}
	if term == "" {
		term = gettyDefaultTerm(line)
	}
	_ = os.Setenv("TERM", term)
	if err := gettyConfigureTermios(os.Stdin.Fd(), baud, opts); err != nil {
		fatalf("getty", "%v", err)
		return 1
	}

	if !opts.noIssue {
		gettyPrintIssue(opts, line)
	}
	if opts.loginPause {
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf)
	}

	username := opts.autologin
	if username == "" && !opts.skipLogin {
		hostname, _ := os.Hostname()
		read, err := gettyReadLoginName(hostname, opts.timeout)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fatalf("getty", "%v", err)
			}
			return 1
		}
		username = read
	}

	if opts.chrootDir != "" {
		if err := syscall.Chroot(opts.chrootDir); err != nil {
			fatalf("getty", "chroot %s: %v", opts.chrootDir, err)
			return 1
		}
		if err := os.Chdir("/"); err != nil {
			fatalf("getty", "%v", err)
			return 1
		}
	}

	loginProgram := opts.loginProgram
	if loginProgram == "" {
		loginProgram = "login"
	}
	path, err := exec.LookPath(loginProgram)
	if err != nil {
		fatalf("getty", "%v", err)
		return 127
	}
	argv := []string{loginProgram}
	if username != "" {
		argv = append(argv, username)
	}
	if err := syscall.Exec(path, argv, os.Environ()); err != nil { //nolint:gosec // G204: getty intentionally launches the configured login program.
		fatalf("getty", "%v", err)
		return 126
	}
	return 0
}

func parseGettyArgs(args []string) (gettyOptions, []string, bool) {
	opts := gettyOptions{localLine: "auto"}
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			positional = append(positional, args[i+1:]...)
			return opts, positional, true
		case arg == "-a" || arg == "--autologin":
			i++
			if i >= len(args) {
				fatalf("getty", "option %q requires an argument", arg)
				return opts, nil, false
			}
			opts.autologin = args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
		case strings.HasPrefix(arg, "--autologin="):
			opts.autologin = strings.TrimPrefix(arg, "--autologin=")
		case arg == "-n" || arg == "--skip-login":
			opts.skipLogin = true
		case arg == "-l" || arg == "--login-program":
			i++
			if i >= len(args) {
				fatalf("getty", "option %q requires an argument", arg)
				return opts, nil, false
			}
			opts.loginProgram = args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
		case strings.HasPrefix(arg, "--login-program="):
			opts.loginProgram = strings.TrimPrefix(arg, "--login-program=")
		case arg == "-r" || arg == "--chroot":
			i++
			if i >= len(args) {
				fatalf("getty", "option %q requires an argument", arg)
				return opts, nil, false
			}
			opts.chrootDir = args[i] //nolint:gosec // G602: i is checked against len(args) immediately above.
		case strings.HasPrefix(arg, "--chroot="):
			opts.chrootDir = strings.TrimPrefix(arg, "--chroot=")
		case arg == "-L" || arg == "--local-line":
			opts.localLine = "always"
		case strings.HasPrefix(arg, "--local-line="):
			opts.localLine = gettyLocalLineMode(strings.TrimPrefix(arg, "--local-line="))
		case strings.HasPrefix(arg, "-L") && len(arg) > 2:
			opts.localLine = gettyLocalLineMode(strings.TrimPrefix(arg, "-L"))
		case arg == "-J" || arg == "--noclear":
			opts.noClear = true
		case arg == "-i" || arg == "--noissue":
			opts.noIssue = true
		case arg == "-8" || arg == "--8bits":
			opts.eightBits = true
		case arg == "-p" || arg == "--login-pause":
			opts.loginPause = true
		case arg == "-t" || arg == "--timeout":
			i++
			if i >= len(args) {
				fatalf("getty", "option %q requires an argument", arg)
				return opts, nil, false
			}
			seconds, err := strconv.Atoi(args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
			if err != nil || seconds < 0 {
				fatalf("getty", "invalid timeout %q", args[i]) //nolint:gosec // G602: i is checked against len(args) immediately above.
				return opts, nil, false
			}
			opts.timeout = seconds
		case strings.HasPrefix(arg, "--timeout="):
			seconds, err := strconv.Atoi(strings.TrimPrefix(arg, "--timeout="))
			if err != nil || seconds < 0 {
				fatalf("getty", "invalid timeout %q", arg)
				return opts, nil, false
			}
			opts.timeout = seconds
		case arg != "-" && strings.HasPrefix(arg, "-"):
			fatalf("getty", "unsupported option %q", arg)
			return opts, nil, false
		default:
			positional = append(positional, arg)
		}
	}
	return opts, positional, true
}

func gettyLocalLineMode(value string) string {
	switch value {
	case "auto", "always", "never":
		return value
	default:
		return "always"
	}
}

// isBaudList reports whether s looks like a comma-separated baud rate list
// (all-digit tokens) rather than a terminal type name.
func isBaudList(s string) bool {
	if s == "" {
		return false
	}
	for _, token := range strings.Split(s, ",") {
		if token == "" {
			return false
		}
		for _, r := range token {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// gettyOpenTerminal opens LINE (or reuses stdin for "-") and attaches it to
// fds 0, 1 and 2, claiming it as the controlling terminal along the way.
func gettyOpenTerminal(line string) error {
	if line == "-" {
		gettyClaimControllingTerminal(os.Stdin.Fd())
		return nil
	}
	path := line
	if !strings.HasPrefix(path, "/") {
		path = filepath.Join("/dev", path)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", path, err)
	}
	fd := int(file.Fd())
	gettyClaimControllingTerminal(uintptr(fd))
	for _, target := range []int{0, 1, 2} {
		if target != fd {
			if err := syscall.Dup2(fd, target); err != nil {
				file.Close()
				return fmt.Errorf("attach %s to fd %d: %w", path, target, err)
			}
		}
	}
	if fd > 2 {
		file.Close()
	}
	return nil
}

// gettyClaimControllingTerminal makes the process a session leader and sets
// fd as its controlling terminal, best-effort. A caller such as ba6's own
// init commonly does this already before launching getty, in which case
// setsid(2) returns EPERM and the ioctl is a harmless no-op; neither is
// treated as fatal.
func gettyClaimControllingTerminal(fd uintptr) {
	_, _ = syscall.Setsid()
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSCTTY, 0) //nolint:gosec // G103: fixed ioctl request, no memory involved.
}

// gettyConfigureTermios applies the requested baud rate, CLOCAL handling and
// 8-bit-clean mode to fd. It is a no-op on a non-terminal fd (for example
// under a test harness) and when nothing was requested.
func gettyConfigureTermios(fd uintptr, baud string, opts gettyOptions) error {
	if baud == "" && opts.localLine == "auto" && !opts.eightBits {
		return nil
	}
	var termios syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&termios))); errno != 0 { //nolint:gosec // G103: fixed Termios buffer for TCGETS.
		if errors.Is(errno, syscall.ENOTTY) {
			return nil
		}
		return errno
	}
	if baud != "" {
		first := strings.SplitN(baud, ",", 2)[0]
		speed, ok := gettyBaudRates[first]
		if !ok {
			return fmt.Errorf("unsupported baud rate %q", first)
		}
		termios.Cflag = (termios.Cflag &^ cbaudMask) | speed
		termios.Ispeed, termios.Ospeed = speed, speed
	}
	switch opts.localLine {
	case "always":
		termios.Cflag |= syscall.CLOCAL
	case "never":
		termios.Cflag &^= syscall.CLOCAL
	}
	if opts.eightBits {
		termios.Cflag = (termios.Cflag &^ (syscall.CSIZE | syscall.PARENB)) | syscall.CS8
	}
	termios.Cflag |= syscall.CREAD
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&termios))); errno != 0 { //nolint:gosec // G103: fixed Termios buffer for TCSETS.
		return errno
	}
	return nil
}

// gettyLineName renders LINE the way agetty's \l issue escape and utmp
// records do: relative to /dev (so "tty1" and "/dev/pts/3" become "tty1" and
// "pts/3"), or "-" unchanged for an already-connected stdin.
func gettyLineName(line string) string {
	if line == "-" {
		return line
	}
	return strings.TrimPrefix(line, "/dev/")
}

// gettyDefaultTerm mirrors agetty's default: "linux" on a numbered virtual
// console (ttyN), "vt100" everywhere else (typically a serial line).
func gettyDefaultTerm(line string) string {
	base := filepath.Base(line)
	if rest, ok := strings.CutPrefix(base, "tty"); ok && rest != "" {
		if _, err := strconv.Atoi(rest); err == nil {
			return "linux"
		}
	}
	return "vt100"
}

func gettyPrintIssue(opts gettyOptions, line string) {
	if !opts.noClear {
		fmt.Fprint(os.Stdout, "\033[H\033[J")
	}
	data, err := os.ReadFile("/etc/issue")
	if err != nil {
		return
	}
	fmt.Fprint(os.Stdout, gettyExpandIssue(string(data), line))
}

// gettyExpandIssue implements the classic getty subset of /etc/issue escapes:
// \n hostname, \l line, \s/\r/\v/\m from uname, \d/\t date and time.
func gettyExpandIssue(issue, line string) string {
	var uts syscall.Utsname
	_ = syscall.Uname(&uts)
	now := time.Now()
	var out strings.Builder
	for i := 0; i < len(issue); i++ {
		if issue[i] != '\\' || i+1 >= len(issue) {
			out.WriteByte(issue[i])
			continue
		}
		i++
		switch issue[i] {
		case 'n':
			hostname, _ := os.Hostname()
			out.WriteString(hostname)
		case 'l':
			out.WriteString(gettyLineName(line))
		case 's':
			out.WriteString(utsField(uts.Sysname[:]))
		case 'r':
			out.WriteString(utsField(uts.Release[:]))
		case 'v':
			out.WriteString(utsField(uts.Version[:]))
		case 'm':
			out.WriteString(utsField(uts.Machine[:]))
		case 'd':
			out.WriteString(now.Format("Mon Jan  2 2006"))
		case 't':
			out.WriteString(now.Format("15:04:05"))
		case '\\':
			out.WriteByte('\\')
		default:
			out.WriteByte('\\')
			out.WriteByte(issue[i])
		}
	}
	return out.String()
}

func gettyReadLoginName(hostname string, timeoutSeconds int) (string, error) {
	if hostname != "" {
		fmt.Fprintf(os.Stdout, "%s login: ", hostname)
	} else {
		fmt.Fprint(os.Stdout, "login: ")
	}
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		reader := bufio.NewReaderSize(os.Stdin, 1024)
		text, err := reader.ReadString('\n')
		ch <- result{strings.TrimSpace(text), err}
	}()
	if timeoutSeconds <= 0 {
		r := <-ch
		return r.line, r.err
	}
	select {
	case r := <-ch:
		return r.line, r.err
	case <-time.After(time.Duration(timeoutSeconds) * time.Second):
		fmt.Fprintln(os.Stdout)
		return "", errGettyTimeout
	}
}
