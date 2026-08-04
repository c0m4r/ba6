//go:build linux

package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoginSHA512AndSHA256Crypt(t *testing.T) {
	tests := []struct {
		password string
		hash     string
	}{
		{
			password: "password",
			hash:     "$6$saltsalt$qFmFH.bQmmtXzyBY0s9v7Oicd2z4XSIecDzlB5KiA2/jctKu9YterLp8wwnSq.qc.eoxqOmSuNp2xS0ktL3nh/",
		},
		{
			password: "password",
			hash:     "$5$saltsalt$gOjOtoMpVhru2uyjeJSEc/JaLQWOXMNmlOnj6T4AtC.",
		},
		{ //nolint:gosec // G101: fixed public crypt test vector, not a credential.
			password: "$a very much longer password phrase",
			hash:     "$6$hfT7jp2q$fdalL2BU2.2IF8cjO5XiVYWfSNS4THfnUzLXILjR/2pHAVqZmdenkEWRMfr5y/JL7GkunKuqUQNSMRzcnH/Rl1",
		},
		{
			password: "password",
			hash:     "$6$rounds=1000$roundtrip$VGM5AVzwu67GdZ3xIAFhMxR.O9CeyP7f9HVpQFTVH01QCSAlx3/VxpHTpLahmWdbM0OEveuLtHAgJ7yB8sSM2.",
		},
		{
			password: "password",
			hash:     "$5$rounds=1000$roundtrip$nzaSzXnhWbx6FMNW7p9MGCtk/hHcrdVfowkPiBPIsg.",
		},
	}
	for _, test := range tests {
		valid, err := verifyLoginPassword([]byte(test.password), test.hash)
		if err != nil || !valid {
			calculated, _ := shaCryptPassword([]byte(test.password), test.hash)
			t.Fatalf("reference hash %q rejected: got=%q valid=%v err=%v", test.hash, calculated, valid, err)
		}
		valid, err = verifyLoginPassword([]byte(test.password+"!"), test.hash)
		if err != nil || valid {
			t.Fatalf("wrong password accepted for %q: valid=%v err=%v", test.hash, valid, err)
		}
	}
	if valid, err := verifyLoginPassword(nil, ""); err != nil || !valid {
		t.Fatalf("passwordless account rejected: valid=%v err=%v", valid, err)
	}
	if valid, err := verifyLoginPassword([]byte("password"), "!locked"); err != nil || valid {
		t.Fatalf("locked account result: valid=%v err=%v", valid, err)
	}
	if _, err := verifyLoginPassword([]byte("password"), "$y$j9T$unsupported"); !errors.Is(err, errUnsupportedPasswordHash) {
		t.Fatalf("unsupported hash error = %v", err)
	}
}

func TestLoginAccountAndShadowParsing(t *testing.T) {
	passwd := "root:x:0:0:root:/root:/bin/bash\nuser:x:1000:100:User:/home/user:/bin/sh\n" //nolint:gosec // G101: synthetic passwd fixture, not a credential.
	account, found, err := parseLoginAccount(strings.NewReader(passwd), "user")
	if err != nil || !found {
		t.Fatalf("parse passwd: account=%+v found=%v err=%v", account, found, err)
	}
	if account.uid != 1000 || account.gid != 100 || account.home != "/home/user" || account.shell != "/bin/sh" {
		t.Fatalf("parsed account = %+v", account)
	}
	if _, _, err := parseLoginAccount(strings.NewReader("user:x:nope:100::/:/bin/sh\n"), "user"); err == nil {
		t.Fatal("invalid uid was accepted")
	}

	shadowText := "root:!:20000::::::\nuser:$6$salt$hash:20000:0:99999:7::25000:\n"
	shadow, found, err := parseShadowAccount(strings.NewReader(shadowText), "user")
	if err != nil || !found || shadow.password != "$6$salt$hash" || shadow.expires != 25000 {
		t.Fatalf("parse shadow: record=%+v found=%v err=%v", shadow, found, err)
	}
}

func TestLoginAuthenticationUsesShadowAndExpiry(t *testing.T) {
	directory := t.TempDir()
	passwdPath := filepath.Join(directory, "passwd")
	shadowPath := filepath.Join(directory, "shadow")
	hash := "$6$saltsalt$qFmFH.bQmmtXzyBY0s9v7Oicd2z4XSIecDzlB5KiA2/jctKu9YterLp8wwnSq.qc.eoxqOmSuNp2xS0ktL3nh/"
	if err := os.WriteFile(passwdPath, []byte("user:x:1000:100:User:/home/user:/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadowPath, []byte("user:"+hash+":20000:0:99999:7::25000:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeExpiry := time.Unix(24000*86400, 0)
	account, err := authenticateLoginFiles(passwdPath, shadowPath, "user", []byte("password"), beforeExpiry)
	if err != nil || account == nil || account.name != "user" {
		t.Fatalf("valid authentication: account=%+v err=%v", account, err)
	}
	account, err = authenticateLoginFiles(passwdPath, shadowPath, "user", []byte("wrong"), beforeExpiry)
	if err != nil || account != nil {
		t.Fatalf("wrong password: account=%+v err=%v", account, err)
	}
	account, err = authenticateLoginFiles(passwdPath, shadowPath, "user", []byte("password"), time.Unix(25000*86400, 0))
	if err != nil || account != nil {
		t.Fatalf("expired account: account=%+v err=%v", account, err)
	}
	account, err = authenticateLoginFiles(passwdPath, shadowPath, "unknown", []byte("password"), beforeExpiry)
	if err != nil || account != nil {
		t.Fatalf("unknown account: account=%+v err=%v", account, err)
	}
}

func TestLoginLineAndSupplementaryGroups(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader("secret\r\n"), loginMaxLine+2)
	line, err := readLoginLine(reader, loginMaxLine)
	if err != nil || string(line) != "secret" {
		t.Fatalf("read line = %q, %v", line, err)
	}
	reader = bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 20)+"\nnext\n"), 8)
	if _, err := readLoginLine(reader, 8); err == nil {
		t.Fatal("overlong login input was accepted")
	}
	line, err = readLoginLine(reader, 8)
	if err != nil || string(line) != "next" {
		t.Fatalf("reader did not recover after long line: %q, %v", line, err)
	}

	path := filepath.Join(t.TempDir(), "group")
	data := "users:x:100:user,other\nwheel:x:10:user\nduplicate:x:100:user\nignored:x:200:other\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	groups, err := loginSupplementaryGroups(path, "user", 100)
	if err != nil || !slices.Equal(groups, []int{100, 10}) {
		t.Fatalf("supplementary groups = %v, %v", groups, err)
	}
}

func TestLoginArguments(t *testing.T) {
	if username, ok := parseLoginArgs([]string{"--", "root"}); !ok || username != "root" {
		t.Fatalf("parsed username = %q, %v", username, ok)
	}
	if _, ok := parseLoginArgs([]string{"-f", "root"}); ok {
		t.Fatal("authentication bypass option was accepted")
	}
	if _, err := readLoginLine(bufio.NewReader(strings.NewReader("")), 10); !errors.Is(err, io.EOF) {
		t.Fatalf("empty input error = %v", err)
	}
}
