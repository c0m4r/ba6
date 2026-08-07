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

func cmdPasswd(args []string) int {
	username, ok := parsePasswdArgs(args)
	if !ok {
		return 1
	}

	callerUID := os.Getuid()
	account, found, err := findPasswdTarget(loginPasswdPath, username, callerUID)
	if err != nil {
		fatalf("passwd", "read %s: %v", loginPasswdPath, err)
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

	reader := bufio.NewReaderSize(os.Stdin, loginMaxLine+2)
	if callerUID != 0 {
		if !verifyCurrentPasswdPassword(account, reader) {
			return 1
		}
	}

	newPassword, err := promptPasswdPassword(reader, "New password: ")
	if err != nil {
		passwdInputError(err)
		return 1
	}
	defer clearBytes(newPassword)
	if len(newPassword) == 0 {
		fatalf("passwd", "empty passwords are not allowed")
		return 1
	}
	confirmation, err := promptPasswdPassword(reader, "Retype new password: ")
	if err != nil {
		passwdInputError(err)
		return 1
	}
	if !constantTimeBytesEqual(newPassword, confirmation) {
		clearBytes(confirmation)
		fatalf("passwd", "passwords do not match")
		return 1
	}
	clearBytes(confirmation)

	hash, err := makePasswdHash(newPassword, rand.Reader)
	if err != nil {
		fatalf("passwd", "generate password hash: %v", err)
		return 1
	}
	path := loginPasswdPath
	shadow := false
	if account.password == "x" {
		path = loginShadowPath
		shadow = true
	}
	if err := replacePasswordRecord(path, account.name, hash, shadow, time.Now()); err != nil {
		fatalf("passwd", "update %s: %v", path, err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "Password changed.")
	return 0
}

func parsePasswdArgs(args []string) (string, bool) {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) > 1 {
		fatalf("passwd", "extra operand %q", args[1])
		return "", false
	}
	if len(args) == 0 {
		return "", true
	}
	if strings.HasPrefix(args[0], "-") || strings.ContainsAny(args[0], ":\r\n") || args[0] == "" {
		fatalf("passwd", "invalid user name")
		return "", false
	}
	return args[0], true
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
