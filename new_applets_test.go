//go:build linux

package main

import (
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCoreExtraApplets(t *testing.T) {
	status, out, _ := captureApplet(t, cmdPrintf, []string{"%s:%04d\\n", "ok", "7"}, "")
	if status != 0 || out != "ok:0007\n" {
		t.Fatalf("printf = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdSeq, []string{"2", "2", "6"}, "")
	if status != 0 || out != "2\n4\n6\n" {
		t.Fatalf("seq = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdExpr, []string{"2", "+", "3", "*", "4"}, "")
	if status != 0 || out != "14\n" {
		t.Fatalf("expr = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdBase64, nil, "hello")
	if status != 0 || out != "aGVsbG8=\n" {
		t.Fatalf("base64 = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdBase64, []string{"-d"}, "aGVsbG8=\n")
	if status != 0 || out != "hello" {
		t.Fatalf("base64 decode = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdStrings, []string{"-n", "4"}, "\x00hello\x01bye")
	if status != 0 || out != "hello\n" {
		t.Fatalf("strings = (%d, %q)", status, out)
	}
}

func TestCompareAndDiff(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("one\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, _ := captureApplet(t, cmdCmp, []string{a, a}, "")
	if status != 0 {
		t.Fatalf("equal cmp status = %d", status)
	}
	status, _, _ = captureApplet(t, cmdCmp, []string{"-s", a, b}, "")
	if status != 1 {
		t.Fatalf("different cmp status = %d", status)
	}
	status, out, _ := captureApplet(t, cmdDiff, []string{a, b}, "")
	if status != 1 || !strings.Contains(out, "-two") || !strings.Contains(out, "+three") {
		t.Fatalf("diff = (%d, %q)", status, out)
	}
}

func TestShellParsingAndExecution(t *testing.T) {
	t.Setenv("BA6_SHELL_TEST", "expanded")
	words, err := shellTokens("printf '%s' '$BA6_SHELL_TEST' \"$BA6_SHELL_TEST\" ''")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"printf", "%s", "$BA6_SHELL_TEST", "expanded", ""}
	if strings.Join(words, "|") != strings.Join(want, "|") {
		t.Fatalf("tokens = %#v, want %#v", words, want)
	}
	status, out, _ := captureApplet(t, cmdSh, []string{"-c", "/bin/printf shell-ok"}, "")
	if status != 0 || out != "shell-ok" {
		t.Fatalf("sh = (%d, %q)", status, out)
	}
	status, out, _ = captureApplet(t, cmdXargs, []string{"-n", "1", "/bin/printf", "[%s]"}, "a b")
	if status != 0 || out != "[a][b]" {
		t.Fatalf("xargs = (%d, %q)", status, out)
	}
}

func TestInitSupervisesAndPropagatesStatus(t *testing.T) {
	status, out, stderr := captureApplet(t, cmdInit, []string{"/bin/sh", "-c", "printf init-ok; exit 7"}, "")
	if status != 7 || out != "init-ok" || stderr != "" {
		t.Fatalf("init = (%d, %q, %q)", status, out, stderr)
	}
	status, _, _ = captureApplet(t, cmdInit, []string{"/bin/sh", "-c", "kill -TERM $$"}, "")
	if status != 128+int(syscall.SIGTERM) {
		t.Fatalf("signaled init status = %d", status)
	}
}

func TestWhichAndPowerOptionValidation(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "rescue-command")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // G306: executable bit is required to test which.
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	status, out, _ := captureApplet(t, cmdWhich, []string{"rescue-command"}, "")
	if status != 0 || strings.TrimSpace(out) != executable {
		t.Fatalf("which = (%d, %q)", status, out)
	}
	status, _, _ = captureApplet(t, cmdReboot, []string{"--invalid"}, "")
	if status != 1 {
		t.Fatalf("invalid reboot status = %d", status)
	}
}

func TestHTTPClientApplet(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("downloaded"))
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	status, out, _ := captureApplet(t, cmdCurl, []string{"-s", server.URL}, "")
	if status != 0 || out != "downloaded" {
		t.Fatalf("curl = (%d, %q)", status, out)
	}
}

func TestStorageHelpersAndEditorModel(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	status, _, _ := captureApplet(t, cmdMknod, []string{fifo, "p"}, "")
	info, err := os.Stat(fifo)
	if status != 0 || err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("mknod fifo: status=%d info=%v err=%v", status, info, err)
	}

	image := make([]byte, 4096)
	image[1080], image[1081] = 0x53, 0xef
	binary.LittleEndian.PutUint32(image[1116:1120], 4)
	binary.LittleEndian.PutUint32(image[1120:1124], 0x40)
	copy(image[1144:1160], []byte("rescue"))
	device := filepath.Join(dir, "filesystem.img")
	if err := os.WriteFile(device, image, 0o600); err != nil {
		t.Fatal(err)
	}
	kind, label, _, err := probeFilesystem(device)
	if err != nil || kind != "ext4" || label != "rescue" {
		t.Fatalf("probe = (%q, %q, %v)", kind, label, err)
	}

	file := filepath.Join(dir, "edited")
	editor := newMiniEditor(file)
	editor.handleKey('a')
	editor.newline()
	editor.handleKey('b')
	if err := editor.save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "a\nb\n" {
		t.Fatalf("saved editor data = %q, %v", data, err)
	}
}

func TestUnrestrictedAppletClassification(t *testing.T) {
	for _, name := range []string{"sh", "init", "xargs", "mount", "ping", "wget", "nc"} {
		if !appletNeedsUnrestrictedSyscalls(name) {
			t.Errorf("%s should bypass seccomp", name)
		}
	}
	for _, name := range []string{"cat", "nano", "ss", "dmesg"} {
		if appletNeedsUnrestrictedSyscalls(name) {
			t.Errorf("%s should retain seccomp", name)
		}
	}
}
