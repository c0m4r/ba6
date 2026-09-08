// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPasswdHashGeneration(t *testing.T) {
	random := bytes.NewReader(bytes.Repeat([]byte{0x41}, passwdSaltLength))
	hash, err := makePasswdHash([]byte("new secret"), random)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$6$rounds=100000$////////////////$") {
		t.Fatalf("hash has unexpected setting: %q", hash)
	}
	valid, err := verifyLoginPassword([]byte("new secret"), hash)
	if err != nil || !valid {
		t.Fatalf("generated hash was not verifiable: valid=%v err=%v", valid, err)
	}
	if valid, err := verifyLoginPassword([]byte("wrong"), hash); err != nil || valid {
		t.Fatalf("generated hash accepted wrong password: valid=%v err=%v", valid, err)
	}
	if _, err := makePasswdHash([]byte("secret"), strings.NewReader("short")); err == nil {
		t.Fatal("short random input was accepted")
	}
}

func TestUpdatedPasswordDatabase(t *testing.T) {
	now := time.Unix(25000*86400+123, 0)
	shadow := []byte("root:!:20000::::::\nuser:$6$old$hash:20000:0:99999:7:::\n")
	updated, err := updatedPasswordDatabase(shadow, "user", "$6$new$hash", true, now)
	if err != nil {
		t.Fatal(err)
	}
	want := "root:!:20000::::::\nuser:$6$new$hash:25000:0:99999:7:::\n"
	if string(updated) != want {
		t.Fatalf("updated shadow = %q, want %q", updated, want)
	}

	passwd := []byte("root:x:0:0:root:/root:/bin/sh\nuser:legacy:1000:100::/home/user:/bin/sh")
	updated, err = updatedPasswordDatabase(passwd, "user", "$6$new$hash", false, now)
	if err != nil {
		t.Fatal(err)
	}
	want = "root:x:0:0:root:/root:/bin/sh\nuser:$6$new$hash:1000:100::/home/user:/bin/sh"
	if string(updated) != want {
		t.Fatalf("updated passwd = %q, want %q", updated, want)
	}
	if _, err := updatedPasswordDatabase(shadow, "missing", "hash", true, now); err == nil {
		t.Fatal("missing shadow entry was accepted")
	}
	duplicate := []byte("user:a:1::::::\nuser:b:2::::::\n")
	if _, err := updatedPasswordDatabase(duplicate, "user", "hash", true, now); err == nil {
		t.Fatal("duplicate shadow entries were accepted")
	}
}

func TestReplacePasswordRecordAtomically(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "shadow")
	if err := os.WriteFile(path, []byte("user:old:20000:0:99999:7:::\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(26000*86400, 0)
	if err := replacePasswordRecord(path, "user", "$6$salt$hash", true, now); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "user:$6$salt$hash:26000:0:99999:7:::\n" {
		t.Fatalf("shadow content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("shadow mode = %o", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".ba6-passwd-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v, %v", matches, err)
	}
}

func TestPasswdDatabaseLockSerializesReplacements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadow")
	lock, err := lockPasswdDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	if _, err := lockPasswdDatabase(path); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("second lock attempt returned %v", err)
	}
}

func TestPasswdTargetAndArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwd")
	data := "root:x:0:0:root:/root:/bin/sh\nuser:x:1000:100::/home/user:/bin/sh\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	account, found, err := findPasswdTarget(path, "", 1000)
	if err != nil || !found || account.name != "user" {
		t.Fatalf("uid lookup = %+v, %v, %v", account, found, err)
	}
	account, found, err = findPasswdTarget(path, "root", 1000)
	if err != nil || !found || account.uid != 0 {
		t.Fatalf("name lookup = %+v, %v, %v", account, found, err)
	}
}

func TestPasswdOptions(t *testing.T) {
	opts, err := parsePasswdOptions([]string{"--", "user"})
	if err != nil || opts.user != "user" {
		t.Fatalf("parsePasswdOptions(-- user) = %+v, %v", opts, err)
	}
	opts, err = parsePasswdOptions([]string{"-l", "-u", "-d", "-e", "-k", "-q", "-S", "-a", "-s", "user"})
	if err != nil || !opts.lock || !opts.unlock || !opts.delete || !opts.expire || !opts.keepTokens || !opts.quiet || !opts.status || !opts.all || !opts.stdin || opts.user != "user" {
		t.Fatalf("parsePasswdOptions flags = %+v, %v", opts, err)
	}
	opts, err = parsePasswdOptions([]string{"-w", "7", "-x", "90", "-n", "1", "-i", "14", "--expiredate=2026-12-31", "-r", "files", "-R", "/chroot", "-P", "/prefix", "user"})
	if err != nil || opts.warnDays != 7 || opts.maxDays != 90 || opts.minDays != 1 || opts.inactDays != 14 || opts.repo != "files" || opts.root != "/chroot" || opts.prefix != "/prefix" || opts.user != "user" {
		t.Fatalf("parsePasswdOptions params = %+v, %v", opts, err)
	}
	if _, err := parsePasswdOptions([]string{"-unknown"}); err == nil {
		t.Fatal("expected error on -unknown")
	}
}

func TestModifyAccountRecord(t *testing.T) {
	dir := t.TempDir()
	shadowPath := filepath.Join(dir, "shadow")
	if err := os.WriteFile(shadowPath, []byte("user:$6$salt$hash:20000:0:99999:7:::\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Lock
	opts := &passwdOptions{lock: true}
	if err := modifyAccountRecord(shadowPath, "user", true, opts); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(shadowPath)
	if !strings.Contains(string(data), "user:!$6$salt$hash") {
		t.Fatalf("lock failed: %s", data)
	}

	// Unlock
	opts = &passwdOptions{unlock: true}
	if err := modifyAccountRecord(shadowPath, "user", true, opts); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(shadowPath)
	if strings.Contains(string(data), "user:!") {
		t.Fatalf("unlock failed: %s", data)
	}

	// Delete
	opts = &passwdOptions{delete: true}
	if err := modifyAccountRecord(shadowPath, "user", true, opts); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(shadowPath)
	if !strings.HasPrefix(string(data), "user::") {
		t.Fatalf("delete failed: %s", data)
	}

	// Expiry params
	opts = &passwdOptions{
		expire:     true,
		setMin:     true,
		minDays:    5,
		setMax:     true,
		maxDays:    60,
		setWarn:    true,
		warnDays:   10,
		setInact:   true,
		inactDays:  3,
		setExpire:  true,
		expireDate: 25000,
	}
	if err := modifyAccountRecord(shadowPath, "user", true, opts); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(shadowPath)
	want := "user::0:5:60:10:3:25000:\n"
	if string(data) != want {
		t.Fatalf("expiry update got %q, want %q", string(data), want)
	}
}

func TestPrintPasswdStatus(t *testing.T) {
	dir := t.TempDir()
	shadowPath := filepath.Join(dir, "shadow")
	if err := os.WriteFile(shadowPath, []byte("user:$6$salt$hash:20000:0:99999:7:::\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	acc := &loginAccount{name: "user", password: "x", uid: 1000}
	var buf bytes.Buffer
	if err := printPasswdStatus(&buf, acc, shadowPath); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "user P ") {
		t.Fatalf("printPasswdStatus got %q, want user P ...", out)
	}
}

