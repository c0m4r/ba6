// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	passwdSaltLength = 16
	passwdHashRounds = 100000
)

type passwdOptions struct {
	lock       bool
	unlock     bool
	delete     bool
	expire     bool
	status     bool
	all        bool
	stdin      bool
	quiet      bool
	keepTokens bool
	warnDays   int
	maxDays    int
	minDays    int
	inactDays  int
	expireDate int64
	setWarn    bool
	setMax     bool
	setMin     bool
	setInact   bool
	setExpire  bool
	root       string
	prefix     string
	repo       string
	user       string
}

func parsePasswdOptions(args []string) (*passwdOptions, error) {
	args = expandShortOptions(args, "irRPwxn")
	opts := &passwdOptions{
		minDays:   -1,
		maxDays:   -1,
		warnDays:  -1,
		inactDays: -1,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				if opts.user != "" {
					return nil, fmt.Errorf("extra operand %q", args[i+1])
				}
				opts.user = args[i+1]
				if i+2 < len(args) {
					return nil, fmt.Errorf("extra operand %q", args[i+2])
				}
			}
			break
		}
		nextVal := func(name string) (string, error) {
			if strings.Contains(arg, "=") {
				return strings.SplitN(arg, "=", 2)[1], nil
			}
			i++
			if i >= len(args) {
				return "", fmt.Errorf("option %s requires an argument", name)
			}
			return args[i], nil
		}

		switch {
		case arg == "-a" || arg == "--all":
			opts.all = true
		case arg == "-d" || arg == "--delete":
			opts.delete = true
		case arg == "-e" || arg == "--expire":
			opts.expire = true
		case arg == "-k" || arg == "--keep-tokens":
			opts.keepTokens = true
		case arg == "-l" || arg == "--lock":
			opts.lock = true
		case arg == "-q" || arg == "--quiet":
			opts.quiet = true
		case arg == "-S" || arg == "--status":
			opts.status = true
		case arg == "-u" || arg == "--unlock":
			opts.unlock = true
		case arg == "-s" || arg == "--stdin":
			opts.stdin = true
		case arg == "-i" || arg == "--inactive" || strings.HasPrefix(arg, "--inactive="):
			val, err := nextVal(arg)
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid numeric argument %q", val)
			}
			opts.inactDays = n
			opts.setInact = true
		case arg == "-r" || arg == "--repository" || strings.HasPrefix(arg, "--repository="):
			val, err := nextVal(arg)
			if err != nil {
				return nil, err
			}
			if val != "files" {
				return nil, fmt.Errorf("only files repository supported")
			}
			opts.repo = val
		case arg == "-R" || arg == "--root" || strings.HasPrefix(arg, "--root="):
			val, err := nextVal(arg)
			if err != nil {
				return nil, err
			}
			opts.root = val
		case arg == "-P" || arg == "--prefix" || strings.HasPrefix(arg, "--prefix="):
			val, err := nextVal(arg)
			if err != nil {
				return nil, err
			}
			opts.prefix = val
		case arg == "-w" || arg == "--warndays" || strings.HasPrefix(arg, "--warndays="):
			val, err := nextVal(arg)
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid numeric argument %q", val)
			}
			opts.warnDays = n
			opts.setWarn = true
		case arg == "-x" || arg == "--maxdays" || strings.HasPrefix(arg, "--maxdays="):
			val, err := nextVal(arg)
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid numeric argument %q", val)
			}
			opts.maxDays = n
			opts.setMax = true
		case arg == "-n" || arg == "--mindays" || strings.HasPrefix(arg, "--mindays="):
			val, err := nextVal(arg)
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid numeric argument %q", val)
			}
			opts.minDays = n
			opts.setMin = true
		case arg == "--expiredate" || strings.HasPrefix(arg, "--expiredate="):
			val, err := nextVal(arg)
			if err != nil {
				return nil, err
			}
			if val == "" || val == "-1" {
				opts.expireDate = -1
			} else if n, err := strconv.ParseInt(val, 10, 64); err == nil {
				opts.expireDate = n
			} else if t, err := time.Parse("2006-01-02", val); err == nil {
				opts.expireDate = t.Unix() / 86400
			} else {
				return nil, fmt.Errorf("invalid expiration date %q", val)
			}
			opts.setExpire = true
		case strings.HasPrefix(arg, "-"):
			return nil, fmt.Errorf("unsupported option %q", arg)
		default:
			if opts.user != "" {
				return nil, fmt.Errorf("extra operand %q", arg)
			}
			if strings.ContainsAny(arg, ":\r\n") || arg == "" {
				return nil, fmt.Errorf("invalid user name")
			}
			opts.user = arg
		}
	}
	return opts, nil
}

func cmdPasswd(args []string) int {
	opts, err := parsePasswdOptions(args)
	if err != nil {
		fatalf("passwd", "%v", err)
		return 1
	}

	callerUID := os.Getuid()
	isAdminMod := opts.lock || opts.unlock || opts.delete || opts.expire ||
		opts.setWarn || opts.setMax || opts.setMin || opts.setInact || opts.setExpire ||
		opts.root != "" || opts.prefix != ""

	if callerUID != 0 {
		if isAdminMod || opts.all || opts.stdin {
			fatalf("passwd", "permission denied")
			return 1
		}
	}

	passwdPath := loginPasswdPath
	shadowPath := loginShadowPath
	if opts.root != "" || opts.prefix != "" {
		baseDir := filepath.Join(opts.root, opts.prefix)
		passwdPath = filepath.Join(baseDir, loginPasswdPath)
		shadowPath = filepath.Join(baseDir, loginShadowPath)
	}

	if opts.status {
		if opts.all {
			return dumpAllPasswdStatus(passwdPath, shadowPath)
		}
		username := opts.user
		account, found, err := findPasswdTarget(passwdPath, username, callerUID)
		if err != nil {
			fatalf("passwd", "read %s: %v", passwdPath, err)
			return 1
		}
		if !found {
			if username == "" {
				fatalf("passwd", "no passwd entry for uid %d", callerUID)
			} else {
				fatalf("passwd", "unknown user %q", username)
			}
			return 1
		}
		if callerUID != 0 && callerUID != account.uid {
			fatalf("passwd", "permission denied")
			return 1
		}
		if err := printPasswdStatus(os.Stdout, account, shadowPath); err != nil {
			fatalf("passwd", "%v", err)
			return 1
		}
		return 0
	}

	username := opts.user
	account, found, err := findPasswdTarget(passwdPath, username, callerUID)
	if err != nil {
		fatalf("passwd", "read %s: %v", passwdPath, err)
		return 1
	}
	if !found {
		if username == "" {
			fatalf("passwd", "no passwd entry for uid %d", callerUID)
		} else {
			fatalf("passwd", "unknown user %q", username)
		}
		return 1
	}
	if callerUID != 0 && callerUID != account.uid {
		fatalf("passwd", "permission denied")
		return 1
	}

	if isAdminMod {
		targetPath := passwdPath
		isShadow := false
		if account.password == "x" {
			targetPath = shadowPath
			isShadow = true
		}
		err := modifyAccountRecord(targetPath, account.name, isShadow, opts)
		if err != nil {
			fatalf("passwd", "%v", err)
			return 1
		}
		if !opts.quiet {
			switch {
			case opts.lock:
				fmt.Fprintln(os.Stdout, "passwd: password locked")
			case opts.unlock:
				fmt.Fprintln(os.Stdout, "passwd: password unlocked")
			case opts.delete:
				fmt.Fprintln(os.Stdout, "passwd: password deleted")
			case opts.expire:
				fmt.Fprintln(os.Stdout, "passwd: password expiry information changed")
			default:
				fmt.Fprintln(os.Stdout, "passwd: password expiry information changed")
			}
		}
		return 0
	}

	var newPassword []byte
	if opts.stdin {
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			passwdInputError(err)
			return 1
		}
		newPassword = []byte(strings.TrimRight(line, "\r\n"))
		if len(newPassword) == 0 {
			fatalf("passwd", "empty passwords are not allowed")
			return 1
		}
	} else {
		reader := bufio.NewReaderSize(os.Stdin, loginMaxLine+2)
		if callerUID != 0 {
			if !verifyCurrentPasswdPassword(account, reader) {
				return 1
			}
		}

		pwd, err := promptPasswdPassword(reader, "New password: ")
		if err != nil {
			passwdInputError(err)
			return 1
		}
		if len(pwd) == 0 {
			fatalf("passwd", "empty passwords are not allowed")
			return 1
		}
		confirmation, err := promptPasswdPassword(reader, "Retype new password: ")
		if err != nil {
			clearBytes(pwd)
			passwdInputError(err)
			return 1
		}
		if !constantTimeBytesEqual(pwd, confirmation) {
			clearBytes(pwd)
			clearBytes(confirmation)
			fatalf("passwd", "passwords do not match")
			return 1
		}
		clearBytes(confirmation)
		newPassword = pwd
	}
	defer clearBytes(newPassword)

	hash, err := makePasswdHash(newPassword, rand.Reader)
	if err != nil {
		fatalf("passwd", "generate password hash: %v", err)
		return 1
	}
	path := passwdPath
	shadow := false
	if account.password == "x" {
		path = shadowPath
		shadow = true
	}
	if err := replacePasswordRecord(path, account.name, hash, shadow, time.Now()); err != nil {
		fatalf("passwd", "update %s: %v", path, err)
		return 1
	}
	if !opts.quiet {
		fmt.Fprintln(os.Stdout, "Password changed.")
	}
	return 0
}

func printPasswdStatus(w io.Writer, account *loginAccount, shadowPath string) error {
	status := "P"
	changeDate := "1970-01-01"
	minDays := "0"
	maxDays := "99999"
	warnDays := "7"
	inactDays := "-1"

	if account.password == "" {
		status = "NP"
	} else if strings.HasPrefix(account.password, "!") || strings.HasPrefix(account.password, "*") {
		status = "L"
	}

	if account.password == "x" {
		file, err := os.Open(shadowPath)
		if err == nil {
			defer file.Close()
			scanner := newLineScanner(file)
			for scanner.Scan() {
				fields := strings.Split(scanner.Text(), ":")
				if len(fields) >= 2 && fields[0] == account.name {
					pwd := fields[1]
					if pwd == "" {
						status = "NP"
					} else if strings.HasPrefix(pwd, "!") || strings.HasPrefix(pwd, "*") {
						status = "L"
					} else {
						status = "P"
					}
					if len(fields) > 2 && fields[2] != "" {
						if days, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
							changeDate = time.Unix(days*86400, 0).UTC().Format("2006-01-02")
						}
					}
					if len(fields) > 3 && fields[3] != "" {
						minDays = fields[3]
					}
					if len(fields) > 4 && fields[4] != "" {
						maxDays = fields[4]
					}
					if len(fields) > 5 && fields[5] != "" {
						warnDays = fields[5]
					}
					if len(fields) > 6 && fields[6] != "" {
						inactDays = fields[6]
					}
					break
				}
			}
		}
	}
	fmt.Fprintf(w, "%s %s %s %s %s %s %s\n", account.name, status, changeDate, minDays, maxDays, warnDays, inactDays)
	return nil
}

func dumpAllPasswdStatus(passwdPath, shadowPath string) int {
	file, err := os.Open(passwdPath)
	if err != nil {
		fatalf("passwd", "%v", err)
		return 1
	}
	defer file.Close()
	scanner := newLineScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) != 7 || fields[0] == "" {
			continue
		}
		parsedUID, _ := strconv.ParseUint(fields[2], 10, 31)
		parsedGID, _ := strconv.ParseUint(fields[3], 10, 31)
		acc := &loginAccount{
			name: fields[0], password: fields[1], uid: int(parsedUID), gid: int(parsedGID),
			home: fields[5], shell: fields[6],
		}
		_ = printPasswdStatus(os.Stdout, acc, shadowPath)
	}
	return 0
}

func modifyAccountRecord(path, username string, shadow bool, opts *passwdOptions) error {
	lock, err := lockPasswdDatabase(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	file, err := openPasswdDatabase(path)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxScanLine+1))
	if err != nil {
		return err
	}
	if len(data) > maxScanLine {
		return fmt.Errorf("file is too large")
	}

	hadFinalNewline := len(data) > 0 && data[len(data)-1] == '\n'
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	found := false
	for index, line := range lines {
		if strings.HasSuffix(line, "\r") {
			return fmt.Errorf("invalid carriage return in database")
		}
		fields := strings.Split(line, ":")
		if len(fields) < 2 || fields[0] != username {
			continue
		}
		if found {
			return fmt.Errorf("duplicate entry for %s", username)
		}
		if shadow {
			for len(fields) < 9 {
				fields = append(fields, "")
			}
			if opts.lock {
				if !strings.HasPrefix(fields[1], "!") {
					fields[1] = "!" + fields[1]
				}
			}
			if opts.unlock {
				fields[1] = strings.TrimPrefix(fields[1], "!")
			}
			if opts.delete {
				fields[1] = ""
			}
			if opts.expire {
				fields[2] = "0"
			}
			if opts.setMin {
				fields[3] = strconv.Itoa(opts.minDays)
			}
			if opts.setMax {
				fields[4] = strconv.Itoa(opts.maxDays)
			}
			if opts.setWarn {
				fields[5] = strconv.Itoa(opts.warnDays)
			}
			if opts.setInact {
				fields[6] = strconv.Itoa(opts.inactDays)
			}
			if opts.setExpire {
				fields[7] = strconv.FormatInt(opts.expireDate, 10)
			}
		} else {
			if len(fields) != 7 {
				return fmt.Errorf("invalid passwd entry for %s", username)
			}
			if opts.lock {
				if !strings.HasPrefix(fields[1], "!") {
					fields[1] = "!" + fields[1]
				}
			}
			if opts.unlock {
				fields[1] = strings.TrimPrefix(fields[1], "!")
			}
			if opts.delete {
				fields[1] = ""
			}
		}
		lines[index] = strings.Join(fields, ":")
		found = true
	}
	if !found {
		return fmt.Errorf("no entry for %s", username)
	}
	result := []byte(strings.Join(lines, "\n"))
	if hadFinalNewline {
		result = append(result, '\n')
	}
	return atomicReplacePasswdFile(path, result, info)
}

func findPasswdTarget(path, username string, uid int) (*loginAccount, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	scanner := newLineScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) != 7 || fields[0] == "" {
			continue
		}
		parsedUID, uidErr := strconv.ParseUint(fields[2], 10, 31)
		parsedGID, gidErr := strconv.ParseUint(fields[3], 10, 31)
		if uidErr != nil || gidErr != nil {
			if username != "" && fields[0] == username {
				return nil, false, fmt.Errorf("invalid passwd entry for %s", username)
			}
			continue
		}
		if username != "" && fields[0] != username || username == "" && int(parsedUID) != uid {
			continue
		}
		shell := fields[6]
		if shell == "" {
			shell = "/bin/sh"
		}
		return &loginAccount{
			name: fields[0], password: fields[1], uid: int(parsedUID), gid: int(parsedGID),
			home: fields[5], shell: shell,
		}, true, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

func verifyCurrentPasswdPassword(account *loginAccount, reader *bufio.Reader) bool {
	stored := account.password
	if stored == "x" {
		shadow, found, err := findShadowAccount(loginShadowPath, account.name)
		if err != nil {
			fatalf("passwd", "read %s: %v", loginShadowPath, err)
			return false
		}
		if !found {
			fatalf("passwd", "no shadow entry for %s", account.name)
			return false
		}
		stored = shadow.password
	}
	current, err := promptPasswdPassword(reader, "Current password: ")
	if err != nil {
		passwdInputError(err)
		return false
	}
	valid, verifyErr := verifyLoginPassword(current, stored)
	clearBytes(current)
	if verifyErr != nil {
		fatalf("passwd", "cannot verify current password: %v", verifyErr)
		return false
	}
	if !valid {
		fatalf("passwd", "authentication failure")
		return false
	}
	return true
}

func promptPasswdPassword(reader *bufio.Reader, prompt string) ([]byte, error) {
	fmt.Fprint(os.Stdout, prompt)
	return readLoginPassword(reader)
}

func passwdInputError(err error) {
	if !errors.Is(err, io.EOF) {
		fatalf("passwd", "%v", err)
	} else {
		fatalf("passwd", "unexpected end of input")
	}
}

func constantTimeBytesEqual(left, right []byte) bool {
	return subtle.ConstantTimeCompare(left, right) == 1
}

func makePasswdHash(password []byte, random io.Reader) (string, error) {
	saltBytes := make([]byte, passwdSaltLength)
	defer clearBytes(saltBytes)
	if _, err := io.ReadFull(random, saltBytes); err != nil {
		return "", err
	}
	for index := range saltBytes {
		saltBytes[index] = cryptBase64[int(saltBytes[index])&63]
	}
	setting := fmt.Sprintf("$6$rounds=%d$%s$", passwdHashRounds, saltBytes)
	return shaCryptPassword(password, setting)
}

func replacePasswordRecord(path, username, hash string, shadow bool, now time.Time) error {
	if strings.ContainsAny(username, ":\r\n") || username == "" || hash == "" || strings.ContainsAny(hash, ":\r\n") {
		return fmt.Errorf("invalid password record")
	}
	lock, err := lockPasswdDatabase(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck // Best-effort unlock before close.

	file, err := openPasswdDatabase(path)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxScanLine+1))
	if err != nil {
		return err
	}
	if len(data) > maxScanLine {
		return fmt.Errorf("file is too large")
	}
	updated, err := updatedPasswordDatabase(data, username, hash, shadow, now)
	if err != nil {
		return err
	}
	defer clearBytes(updated)
	return atomicReplacePasswdFile(path, updated, info)
}

func lockPasswdDatabase(path string) (*os.File, error) {
	lockPath := filepath.Join(filepath.Dir(path), ".pwd.lock")
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open password database lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), lockPath)
	// flock serializes concurrent calls in this process. Acquire it before the
	// process-scoped POSIX lock so a failed second call cannot disturb the
	// first call's record lock by closing its own descriptor.
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("password database is busy")
		}
		return nil, fmt.Errorf("lock password database: %w", err)
	}
	// The POSIX record lock interoperates with lckpwdf(3)-based tools.
	recordLock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart}
	if err := syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &recordLock); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("password database is busy")
		}
		return nil, fmt.Errorf("lock password database: %w", err)
	}
	return file, nil
}

func openPasswdDatabase(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func updatedPasswordDatabase(data []byte, username, hash string, shadow bool, now time.Time) ([]byte, error) {
	hadFinalNewline := len(data) > 0 && data[len(data)-1] == '\n'
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	found := false
	for index, line := range lines {
		if strings.HasSuffix(line, "\r") {
			return nil, fmt.Errorf("invalid carriage return in database")
		}
		fields := strings.Split(line, ":")
		if len(fields) < 2 || fields[0] != username {
			continue
		}
		if found {
			return nil, fmt.Errorf("duplicate entry for %s", username)
		}
		if shadow {
			if len(fields) > 9 {
				return nil, fmt.Errorf("invalid shadow entry for %s", username)
			}
			for len(fields) < 3 {
				fields = append(fields, "")
			}
			fields[2] = strconv.FormatInt(now.Unix()/86400, 10)
		} else if len(fields) != 7 {
			return nil, fmt.Errorf("invalid passwd entry for %s", username)
		}
		fields[1] = hash
		lines[index] = strings.Join(fields, ":")
		found = true
	}
	if !found {
		return nil, fmt.Errorf("no entry for %s", username)
	}
	result := []byte(strings.Join(lines, "\n"))
	if hadFinalNewline {
		result = append(result, '\n')
	}
	return result, nil
}

func atomicReplacePasswdFile(path string, data []byte, original os.FileInfo) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".ba6-passwd-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()

	if written, err := temporary.Write(data); err != nil {
		return err
	} else if written != len(data) {
		return io.ErrShortWrite
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	stat, ok := original.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine file ownership")
	}
	if err := temporary.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
		return err
	}
	if err := temporary.Chmod(original.Mode().Perm()); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true

	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return directoryFile.Sync()
}
