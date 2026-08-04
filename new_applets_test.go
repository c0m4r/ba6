//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestInittabParsingAndBootOrdering(t *testing.T) {
	input := strings.Join([]string{
		"# boot configuration",
		"::wait:/bin/echo wait",
		"::sysinit:/bin/mount -t proc proc /proc",
		"ttyS0::respawn:/bin/sh",
		"::once:/bin/echo once",
		"::shutdown:/bin/umount -a -r",
		"broken line",
	}, "\n")
	entries, err := parseInittab(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "line 7") {
		t.Fatalf("expected a line-numbered parse warning, got %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("parsed %d entries, want 5", len(entries))
	}
	var order []string
	for _, action := range []initAction{initSysinit, initWait, initOnce} {
		for _, entry := range entriesForAction(entries, action) {
			order = append(order, string(entry.action))
		}
	}
	if strings.Join(order, ",") != "sysinit,wait,once" {
		t.Fatalf("boot order = %v", order)
	}
	respawn := entriesForAction(entries, initRespawn)
	if len(respawn) != 1 || respawn[0].id != "ttyS0" || respawn[0].command != "/bin/sh" {
		t.Fatalf("respawn entry = %#v", respawn)
	}
}

func TestInitBootAndShutdownActionsExecuteInOrder(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "order")
	entries := []inittabEntry{
		{action: initWait, command: "printf W >> " + marker, line: 1},
		{action: initSysinit, command: "printf S >> " + marker, line: 2},
		{action: initShutdown, command: "printf X >> " + marker, line: 3},
	}
	for _, action := range []initAction{initSysinit, initWait} {
		runInitActions(entries, action)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "SW" {
		t.Fatalf("boot order = %q, %v", data, err)
	}
	runInitActions(entries, initShutdown)
	data, err = os.ReadFile(marker)
	if err != nil || string(data) != "SWX" {
		t.Fatalf("shutdown order = %q, %v", data, err)
	}
}

func TestInitEnvironmentAndHardeningProfiles(t *testing.T) {
	oldDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	oldMask := syscall.Umask(0)
	syscall.Umask(oldMask)
	t.Cleanup(func() { _ = os.Chdir(oldDirectory); syscall.Umask(oldMask) })
	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM"} {
		t.Setenv(name, "")
	}
	establishInitEnvironment()
	if os.Getenv("PATH") != "/sbin:/bin:/usr/sbin:/usr/bin" || os.Getenv("HOME") != "/root" || os.Getenv("TERM") != "linux" {
		t.Fatalf("incomplete init environment: PATH=%q HOME=%q TERM=%q", os.Getenv("PATH"), os.Getenv("HOME"), os.Getenv("TERM"))
	}
	if profile := hardeningForApplet("init", 1, true); profile.noNewPrivs || profile.seccomp {
		t.Fatalf("PID 1 profile = %+v", profile)
	}
	if profile := hardeningForApplet("init", 42, true); !profile.noNewPrivs || profile.seccomp {
		t.Fatalf("non-PID-1 init profile = %+v", profile)
	}
	if profile := hardeningForApplet("sh", 42, true); profile.noNewPrivs || profile.seccomp {
		t.Fatalf("execution frontend profile = %+v", profile)
	}
	if profile := hardeningForApplet("cat", 1, true); !profile.noNewPrivs || !profile.seccomp {
		t.Fatalf("ordinary applet profile = %+v", profile)
	}
	if respawnDelay(1) != time.Second || respawnDelay(9) != 32*time.Second {
		t.Fatalf("unexpected respawn delays")
	}
	if !powerStatusRestored("O\n") || powerStatusRestored("F\n") {
		t.Fatalf("power status classification failed")
	}
}

func TestInitPIDNamespaceRespawnsAndStaysAlive(t *testing.T) {
	if os.Getenv("BA6_PID1_HELPER") == "1" {
		cmdInit([]string{"-f", os.Getenv("BA6_INITTAB")})
		t.Fatal("PID 1 returned")
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare is unavailable")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "respawned")
	powerMarker := filepath.Join(dir, "power")
	shutdownMarker := filepath.Join(dir, "shutdown")
	inittab := filepath.Join(dir, "inittab")
	configuration := "::respawn:/bin/sh -c 'echo x >> " + marker + "; exit 0'\n" +
		"::powerfail:/bin/sh -c 'echo power >> " + powerMarker + "'\n" +
		"::shutdown:/bin/sh -c 'echo shutdown >> " + shutdownMarker + "'\n"
	if err := os.WriteFile(inittab, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(unshare, "--user", "--map-root-user", "--mount", "--pid", "--fork", "--kill-child=SIGKILL", "--mount-proc", os.Args[0], "-test.run=^TestInitPIDNamespaceRespawnsAndStaysAlive$") //nolint:gosec // G204: fixed integration-test command using the current test binary.
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	command.Env = append(os.Environ(), "BA6_PID1_HELPER=1", "BA6_INITTAB="+inittab)
	if err := command.Start(); err != nil {
		t.Skipf("cannot start PID namespace: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if finished {
			return
		}
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		<-done
	})
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case waitErr := <-done:
			finished = true
			message := output.String()
			if strings.Contains(message, "Operation not permitted") || strings.Contains(message, "Permission denied") {
				t.Skipf("PID namespaces unavailable: %s", strings.TrimSpace(message))
			}
			t.Fatalf("PID namespace init exited early: %v: %s", waitErr, message)
		default:
		}
		data, _ := os.ReadFile(marker)
		if strings.Count(string(data), "x\n") >= 2 {
			initPID, err := namespaceInitHostPID(command.Process.Pid)
			if err != nil {
				t.Fatalf("locate namespace PID 1: %v", err)
			}
			if err := syscall.Kill(initPID, 0); err != nil {
				t.Fatalf("PID 1 did not remain alive after respawn: %v", err)
			}
			if err := syscall.Kill(initPID, syscall.SIGPWR); err != nil {
				t.Fatalf("send SIGPWR: %v", err)
			}
			shutdownDeadline := time.Now().Add(4 * time.Second)
			for time.Now().Before(shutdownDeadline) {
				power, _ := os.ReadFile(powerMarker)
				shutdown, _ := os.ReadFile(shutdownMarker)
				if strings.Contains(string(power), "power") && strings.Contains(string(shutdown), "shutdown") {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatalf("SIGPWR actions did not run: %s", output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("respawn was not observed: %s", output.String())
}

func TestInitBinaryPID1HardeningProfile(t *testing.T) {
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare is unavailable")
	}
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "ba6")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binaryPath, ".") //nolint:gosec // G204: fixed Go build integration command.
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOCACHE="+filepath.Join(dir, "go-cache"))
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build init binary: %v: %s", buildErr, output)
	}
	statusFile := filepath.Join(dir, "status")
	parentStatus, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	parentNoNewPrivs := procStatusValue(string(parentStatus), "NoNewPrivs")
	parentFilters := procStatusValue(string(parentStatus), "Seccomp_filters")
	inittab := filepath.Join(dir, "inittab")
	configuration := "::once:/bin/sh -c \"grep -E '^(NoNewPrivs|Seccomp|Seccomp_filters):' /proc/1/status > " + statusFile + "\"\n" +
		"::respawn:/bin/sleep 30\n"
	if err := os.WriteFile(inittab, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(unshare, "--user", "--map-root-user", "--mount", "--pid", "--fork", "--kill-child=SIGKILL", "--mount-proc", binaryPath, "init", "-f", inittab) //nolint:gosec // G204: fixed PID-namespace integration command.
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Skipf("cannot start PID namespace: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if finished {
			return
		}
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		<-done
	})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case waitErr := <-done:
			finished = true
			message := output.String()
			if strings.Contains(message, "Operation not permitted") || strings.Contains(message, "Permission denied") {
				t.Skipf("PID namespaces unavailable: %s", strings.TrimSpace(message))
			}
			t.Fatalf("PID 1 exited early: %v: %s", waitErr, message)
		default:
		}
		data, _ := os.ReadFile(statusFile)
		status := string(data)
		if procStatusValue(status, "NoNewPrivs") == parentNoNewPrivs && procStatusValue(status, "Seccomp_filters") == parentFilters {
			initPID, locateErr := namespaceInitHostPID(command.Process.Pid)
			if locateErr != nil {
				t.Fatal(locateErr)
			}
			if err := syscall.Kill(initPID, syscall.SIGUSR2); err != nil {
				t.Fatalf("stop namespace init: %v", err)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("PID 1 hardening status not observed: %s", output.String())
}

func procStatusValue(status, name string) string {
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimSuffix(fields[0], ":") == name {
			return fields[1]
		}
	}
	return ""
}

func namespaceInitHostPID(unsharePID int) (int, error) {
	path := fmt.Sprintf("/proc/%d/task/%d/children", unsharePID, unsharePID)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) > 0 {
				return strconv.Atoi(fields[0])
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0, fmt.Errorf("no child listed in %s", path)
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
