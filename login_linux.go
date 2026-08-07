// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	loginPasswdPath = "/etc/passwd"
	loginShadowPath = "/etc/shadow"
	loginGroupPath  = "/etc/group"
	loginNologin    = "/etc/nologin"
	loginMaxLine    = 1024
)

var errUnsupportedPasswordHash = errors.New("unsupported password hash")

type loginAccount struct {
	name, password, home, shell string
	uid, gid                    int
}

type shadowAccount struct {
	password string
	expires  int64
}

func cmdLogin(args []string) int {
	username, ok := parseLoginArgs(args)
	if !ok {
		return 1
	}
	if os.Geteuid() != 0 {
		fatalf("login", "must be run as root")
		return 1
	}

	reader := bufio.NewReaderSize(os.Stdin, loginMaxLine+2)
	for attempts := 0; attempts < 3; attempts++ {
		current := username
		if current == "" {
			hostname, _ := os.Hostname()
			if hostname != "" {
				fmt.Fprintf(os.Stdout, "%s login: ", hostname)
			} else {
				fmt.Fprint(os.Stdout, "login: ")
			}
			line, err := readLoginLine(reader, loginMaxLine)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					fatalf("login", "%v", err)
				}
				return 1
			}
			current = strings.TrimSpace(string(line))
			clearBytes(line)
		}

		fmt.Fprint(os.Stdout, "Password: ")
		password, err := readLoginPassword(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fatalf("login", "%v", err)
			}
			return 1
		}
		account, authErr := authenticateLogin(current, password, time.Now())
		clearBytes(password)
		if authErr != nil && !errors.Is(authErr, errUnsupportedPasswordHash) {
			fatalf("login", "%v", authErr)
			return 1
		}
		if account != nil {
			if err := beginLoginSession(account); err != nil {
				fatalf("login", "%v", err)
				return 1
			}
			return 0
		}

		fmt.Fprintln(os.Stdout, "Login incorrect")
		time.Sleep(time.Second)
	}
	return 1
}

func parseLoginArgs(args []string) (string, bool) {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) > 1 {
		fatalf("login", "extra operand %q", args[1])
		return "", false
	}
	if len(args) == 1 {
		if strings.HasPrefix(args[0], "-") || strings.ContainsAny(args[0], ":\r\n") {
			fatalf("login", "invalid user name")
			return "", false
		}
		return args[0], true
	}
	return "", true
}

func readLoginPassword(reader *bufio.Reader) ([]byte, error) {
	old, err := disableTerminalEcho(os.Stdin.Fd())
	if err != nil && !errors.Is(err, syscall.ENOTTY) {
		return nil, fmt.Errorf("disable terminal echo: %w", err)
	}
	if old != nil {
		defer restoreTerminal(os.Stdin.Fd(), old)
	}
	password, readErr := readLoginLine(reader, loginMaxLine)
	if old != nil {
		fmt.Fprintln(os.Stdout)
	}
	return password, readErr
}

func disableTerminalEcho(fd uintptr) (*syscall.Termios, error) {
	old := new(syscall.Termios)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(old))); errno != 0 { //nolint:gosec // G103: fixed Termios buffer for TCGETS.
		return nil, errno
	}
	changed := *old
	changed.Lflag &^= syscall.ECHO | syscall.ECHONL
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&changed))); errno != 0 { //nolint:gosec // G103: fixed Termios buffer for TCSETS.
		return nil, errno
	}
	return old, nil
}

func readLoginLine(reader *bufio.Reader, limit int) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > limit+1 {
		clearBytes(line)
		for errors.Is(err, bufio.ErrBufferFull) {
			var discarded []byte
			discarded, err = reader.ReadSlice('\n')
			clearBytes(discarded)
		}
		return nil, fmt.Errorf("input line is too long")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	line = bytesWithoutLineEnding(line)
	if len(line) > limit {
		clearBytes(line)
		return nil, fmt.Errorf("input line is too long")
	}
	result := append([]byte(nil), line...)
	clearBytes(line)
	if errors.Is(err, io.EOF) && len(result) == 0 {
		return nil, io.EOF
	}
	return result, nil
}

func bytesWithoutLineEnding(value []byte) []byte {
	if len(value) > 0 && value[len(value)-1] == '\n' {
		value = value[:len(value)-1]
	}
	if len(value) > 0 && value[len(value)-1] == '\r' {
		value = value[:len(value)-1]
	}
	return value
}

func authenticateLogin(username string, password []byte, now time.Time) (*loginAccount, error) {
	return authenticateLoginFiles(loginPasswdPath, loginShadowPath, username, password, now)
}

func authenticateLoginFiles(passwdPath, shadowPath, username string, password []byte, now time.Time) (*loginAccount, error) {
	account, found, err := findLoginAccount(passwdPath, username)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", passwdPath, err)
	}
	if !found {
		// Do the same expensive operation used for a normal SHA-512 password so
		// an unknown user is not immediately distinguishable by response time.
		_, _ = verifyLoginPassword(password, "$6$saltsalt$qFmFH.bQmmtXzyBY0s9v7Oicd2z4XSIecDzlB5KiA2/jctKu9YterLp8wwnSq.qc.eoxqOmSuNp2xS0ktL3nh/")
		return nil, nil
	}

	stored := account.password
	shadow := shadowAccount{expires: -1}
	if stored == "x" {
		shadow, found, err = findShadowAccount(shadowPath, username)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", shadowPath, err)
		}
		if !found {
			return nil, fmt.Errorf("no shadow entry for %s", username)
		}
		stored = shadow.password
	}
	valid, err := verifyLoginPassword(password, stored)
	if err != nil || stored == "" || stored[0] == '!' || stored[0] == '*' {
		_, _ = verifyLoginPassword(password, "$6$saltsalt$qFmFH.bQmmtXzyBY0s9v7Oicd2z4XSIecDzlB5KiA2/jctKu9YterLp8wwnSq.qc.eoxqOmSuNp2xS0ktL3nh/")
	}
	if err != nil || !valid {
		return nil, err
	}
	if shadow.expires >= 0 && now.Unix()/86400 >= shadow.expires {
		return nil, nil
	}
	return account, nil
}

func findLoginAccount(path, username string) (*loginAccount, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	return parseLoginAccount(file, username)
}

func parseLoginAccount(reader io.Reader, username string) (*loginAccount, bool, error) {
	scanner := newLineScanner(reader)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) != 7 || fields[0] != username {
			continue
		}
		uid, uidErr := strconv.ParseUint(fields[2], 10, 31)
		gid, gidErr := strconv.ParseUint(fields[3], 10, 31)
		if uidErr != nil || gidErr != nil || fields[0] == "" {
			return nil, false, fmt.Errorf("invalid passwd entry for %s", username)
		}
		shell := fields[6]
		if shell == "" {
			shell = "/bin/sh"
		}
		if !filepath.IsAbs(shell) {
			return nil, false, fmt.Errorf("invalid shell for %s", username)
		}
		return &loginAccount{
			name: fields[0], password: fields[1], uid: int(uid), gid: int(gid),
			home: fields[5], shell: shell,
		}, true, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

func findShadowAccount(path, username string) (shadowAccount, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return shadowAccount{}, false, err
	}
	defer file.Close()
	return parseShadowAccount(file, username)
}

func parseShadowAccount(reader io.Reader, username string) (shadowAccount, bool, error) {
	scanner := newLineScanner(reader)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) < 2 || fields[0] != username {
			continue
		}
		expires := int64(-1)
		if len(fields) > 7 && fields[7] != "" {
			parsed, err := strconv.ParseInt(fields[7], 10, 64)
			if err != nil || parsed < 0 {
				return shadowAccount{}, false, fmt.Errorf("invalid shadow entry for %s", username)
			}
			expires = parsed
		}
		return shadowAccount{password: fields[1], expires: expires}, true, nil
	}
	if err := scanner.Err(); err != nil {
		return shadowAccount{}, false, err
	}
	return shadowAccount{}, false, nil
}

func verifyLoginPassword(password []byte, stored string) (bool, error) {
	if stored == "" {
		return len(password) == 0, nil
	}
	if stored[0] == '!' || stored[0] == '*' {
		return false, nil
	}
	calculated, err := shaCryptPassword(password, stored)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(calculated), []byte(stored)) == 1, nil
}

func shaCryptPassword(password []byte, encoded string) (string, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) < 4 || parts[0] != "" || parts[1] != "5" && parts[1] != "6" {
		return "", errUnsupportedPasswordHash
	}
	index := 2
	rounds := 5000
	explicitRounds := false
	if strings.HasPrefix(parts[index], "rounds=") {
		parsed, err := strconv.ParseUint(strings.TrimPrefix(parts[index], "rounds="), 10, 32)
		if err != nil {
			return "", errUnsupportedPasswordHash
		}
		if parsed < 1000 {
			parsed = 1000
		}
		if parsed > 999999999 {
			parsed = 999999999
		}
		rounds = int(parsed)
		explicitRounds = true
		index++
	}
	if len(parts) != index+2 {
		return "", errUnsupportedPasswordHash
	}
	salt := parts[index]
	if len(salt) > 16 || strings.ContainsAny(salt, "$:\r\n") {
		return "", errUnsupportedPasswordHash
	}

	var digest []byte
	if parts[1] == "6" {
		digest = shaCryptDigest(password, []byte(salt), rounds, sha512.New)
	} else {
		digest = shaCryptDigest(password, []byte(salt), rounds, sha256.New)
	}
	var output strings.Builder
	output.WriteByte('$')
	output.WriteString(parts[1])
	output.WriteByte('$')
	if explicitRounds {
		fmt.Fprintf(&output, "rounds=%d$", rounds)
	}
	output.WriteString(salt)
	output.WriteByte('$')
	if parts[1] == "6" {
		encodeSHA512Crypt(&output, digest)
	} else {
		encodeSHA256Crypt(&output, digest)
	}
	clearBytes(digest)
	return output.String(), nil
}

func shaCryptDigest(password, salt []byte, rounds int, newHash func() hash.Hash) []byte {
	primary := newHash()
	primary.Write(password)
	primary.Write(salt)
	alternate := newHash()
	alternate.Write(password)
	alternate.Write(salt)
	alternate.Write(password)
	alternateSum := alternate.Sum(nil)
	writeRepeated(primary, alternateSum, len(password))
	for count := len(password); count > 0; count >>= 1 {
		if count&1 != 0 {
			primary.Write(alternateSum)
		} else {
			primary.Write(password)
		}
	}
	result := primary.Sum(nil)

	passwordDigest := newHash()
	for range len(password) {
		passwordDigest.Write(password)
	}
	passwordSequence := repeatedBytes(passwordDigest.Sum(nil), len(password))

	saltDigest := newHash()
	for range 16 + int(result[0]) {
		saltDigest.Write(salt)
	}
	saltSequence := repeatedBytes(saltDigest.Sum(nil), len(salt))

	for round := 0; round < rounds; round++ {
		current := newHash()
		if round&1 != 0 {
			current.Write(passwordSequence)
		} else {
			current.Write(result)
		}
		if round%3 != 0 {
			current.Write(saltSequence)
		}
		if round%7 != 0 {
			current.Write(passwordSequence)
		}
		if round&1 != 0 {
			current.Write(result)
		} else {
			current.Write(passwordSequence)
		}
		clearBytes(result)
		result = current.Sum(nil)
	}
	clearBytes(alternateSum)
	clearBytes(passwordSequence)
	clearBytes(saltSequence)
	return result
}

func writeRepeated(destination hash.Hash, value []byte, count int) {
	for count > 0 && len(value) > 0 {
		amount := min(count, len(value))
		destination.Write(value[:amount])
		count -= amount
	}
}

func repeatedBytes(value []byte, count int) []byte {
	result := make([]byte, 0, count)
	for len(result) < count && len(value) > 0 {
		amount := min(count-len(result), len(value))
		result = append(result, value[:amount]...)
	}
	clearBytes(value)
	return result
}

const cryptBase64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func encodeCrypt24(output *strings.Builder, b0, b1, b2 byte, count int) {
	value := uint(b0)<<16 | uint(b1)<<8 | uint(b2)
	for range count {
		output.WriteByte(cryptBase64[value&0x3f])
		value >>= 6
	}
}

func encodeSHA512Crypt(output *strings.Builder, sum []byte) {
	triples := [][3]int{
		{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4},
		{47, 5, 26}, {6, 27, 48}, {28, 49, 7}, {50, 8, 29}, {9, 30, 51},
		{31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13}, {56, 14, 35},
		{15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19},
		{62, 20, 41},
	}
	for _, triple := range triples {
		encodeCrypt24(output, sum[triple[0]], sum[triple[1]], sum[triple[2]], 4)
	}
	encodeCrypt24(output, 0, 0, sum[63], 2)
}

func encodeSHA256Crypt(output *strings.Builder, sum []byte) {
	triples := [][3]int{
		{0, 10, 20}, {21, 1, 11}, {12, 22, 2}, {3, 13, 23}, {24, 4, 14},
		{15, 25, 5}, {6, 16, 26}, {27, 7, 17}, {18, 28, 8}, {9, 19, 29},
	}
	for _, triple := range triples {
		encodeCrypt24(output, sum[triple[0]], sum[triple[1]], sum[triple[2]], 4)
	}
	encodeCrypt24(output, 0, sum[31], sum[30], 3)
}

func beginLoginSession(account *loginAccount) error {
	if account.uid != 0 {
		if message, err := os.ReadFile(loginNologin); err == nil {
			_, _ = os.Stdout.Write(message)
			if len(message) == 0 || message[len(message)-1] != '\n' {
				fmt.Fprintln(os.Stdout)
			}
			return fmt.Errorf("system login is disabled")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read %s: %w", loginNologin, err)
		}
	}

	groups, err := loginSupplementaryGroups(loginGroupPath, account.name, account.gid)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", loginGroupPath, err)
	}
	if err := syscall.Setgroups(groups); err != nil {
		return fmt.Errorf("set supplementary groups: %w", err)
	}
	if isTerminal(os.Stdin.Fd()) {
		if err := syscall.Fchown(int(os.Stdin.Fd()), account.uid, account.gid); err != nil {
			return fmt.Errorf("set terminal owner: %w", err)
		}
		if err := syscall.Fchmod(int(os.Stdin.Fd()), 0o600); err != nil {
			return fmt.Errorf("set terminal mode: %w", err)
		}
	}
	if err := syscall.Setresgid(account.gid, account.gid, account.gid); err != nil {
		return fmt.Errorf("set gid: %w", err)
	}
	if err := syscall.Setresuid(account.uid, account.uid, account.uid); err != nil {
		return fmt.Errorf("set uid: %w", err)
	}

	home := account.home
	if home == "" {
		home = "/"
	}
	if err := os.Chdir(home); err != nil {
		fmt.Fprintf(os.Stderr, "login: cannot change directory to %s: %v; using /\n", home, err)
		home = "/"
		if err := os.Chdir(home); err != nil {
			return fmt.Errorf("change directory to /: %w", err)
		}
	}
	term := os.Getenv("TERM")
	os.Clearenv()
	path := "/usr/local/bin:/usr/bin:/bin"
	if account.uid == 0 {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	for name, value := range map[string]string{
		"HOME": home, "USER": account.name, "LOGNAME": account.name,
		"SHELL": account.shell, "PATH": path,
	} {
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set environment: %w", err)
		}
	}
	if term != "" {
		_ = os.Setenv("TERM", term)
	}
	syscall.Umask(0o022)
	argv := []string{"-" + filepath.Base(account.shell)}
	if err := syscall.Exec(account.shell, argv, os.Environ()); err != nil { //nolint:gosec // G204: the root-owned passwd database selects the login shell.
		return fmt.Errorf("execute %s: %w", account.shell, err)
	}
	return nil
}

func loginSupplementaryGroups(path, username string, primary int) ([]int, error) {
	groups := []int{primary}
	seen := map[int]bool{primary: true}
	file, err := os.Open(path)
	if err != nil {
		return groups, err
	}
	defer file.Close()
	scanner := newLineScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) != 4 {
			continue
		}
		member := false
		for _, name := range strings.Split(fields[3], ",") {
			if name == username {
				member = true
				break
			}
		}
		if !member {
			continue
		}
		gid, parseErr := strconv.ParseUint(fields[2], 10, 31)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid group entry %q", fields[0])
		}
		value := int(gid)
		if !seen[value] {
			groups = append(groups, value)
			seen[value] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}

func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&termios))) //nolint:gosec // G103: fixed Termios buffer for TCGETS.
	return errno == 0
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
