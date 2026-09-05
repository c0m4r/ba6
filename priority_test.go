// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAwkCommonRecoveryPrograms(t *testing.T) {
	status, output, stderr := captureApplet(t, cmdAwk, []string{"-F:", "$3 == 0 { print $1 }"}, "root:x:0\nuser:x:1000\n")
	if status != 0 || output != "root\n" || stderr != "" {
		t.Fatalf("awk filter = (%d, %q, %q)", status, output, stderr)
	}
	status, output, stderr = captureApplet(t, cmdAwk, []string{"{ total += $1 } END { print total }"}, "1\n2\n3\n")
	if status != 0 || output != "6\n" || stderr != "" {
		t.Fatalf("awk sum = (%d, %q, %q)", status, output, stderr)
	}
	status, output, _ = captureApplet(t, cmdAwk, []string{"BEGIN { print toupper(\"ok\"), 2 + 3 }"}, "")
	if status != 0 || output != "OK 5\n" {
		t.Fatalf("awk BEGIN = (%d, %q)", status, output)
	}
}

func TestDdCopiesBlocksAndSeeks(t *testing.T) {
	directory := t.TempDir()
	input, output := filepath.Join(directory, "input"), filepath.Join(directory, "output")
	if err := os.WriteFile(input, []byte("abcdefgh"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, stderr := captureApplet(t, cmdDd, []string{"if=" + input, "of=" + output, "bs=2", "skip=1", "count=2", "status=none"}, "")
	data, err := os.ReadFile(output)
	if status != 0 || err != nil || string(data) != "cdef" || stderr != "" {
		t.Fatalf("dd = status %d, data %q, err %v, stderr %q", status, data, err, stderr)
	}
	if value, err := parseDDNumber("2x1K"); err != nil || value != 2048 {
		t.Fatalf("parseDDNumber = %d, %v", value, err)
	}
}

func TestFileMagicAndMktemp(t *testing.T) {
	if got := describeData([]byte("\x7fELF\x02"), 0); !strings.Contains(got, "64-bit") {
		t.Fatalf("ELF description = %q", got)
	}
	if got := describeData([]byte("#!/bin/sh\necho ok\n"), 0o755); !strings.Contains(got, "/bin/sh") {
		t.Fatalf("script description = %q", got)
	}
	directory := t.TempDir()
	status, output, stderr := captureApplet(t, cmdMktemp, []string{"-p", directory, "rescue.XXXXXX"}, "")
	name := strings.TrimSpace(output)
	info, err := os.Stat(name)
	if status != 0 || stderr != "" || err != nil || !info.Mode().IsRegular() || filepath.Dir(name) != directory {
		t.Fatalf("mktemp = (%d, %q, %q), info=%v err=%v", status, output, stderr, info, err)
	}
}

func TestFileELFDescriptionAndMime(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	desc, ok := describeELF(self)
	if !ok || !strings.HasPrefix(desc, "ELF 64-bit LSB") || !strings.Contains(desc, "x86-64") {
		t.Fatalf("describeELF(self) = (%q, %v)", desc, ok)
	}
	if !strings.Contains(desc, "dynamically linked") && !strings.Contains(desc, "statically linked") {
		t.Fatalf("describeELF(self) should report a link mode, got %q", desc)
	}
	if !strings.HasSuffix(desc, "stripped") {
		t.Fatalf("describeELF(self) should end in a stripped state, got %q", desc)
	}

	mime := mimeFor(desc)
	if !strings.HasPrefix(mime, "application/x-") || !strings.HasSuffix(mime, "; charset=binary") {
		t.Fatalf("mimeFor(ELF) = %q", mime)
	}

	if _, ok := describeELF(filepath.Join(t.TempDir(), "does-not-exist")); ok {
		t.Fatal("describeELF should report ok=false for a missing file")
	}
	nonELF := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(nonELF, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := describeELF(nonELF); ok {
		t.Fatal("describeELF should report ok=false for a non-ELF file")
	}

	cases := map[string]string{
		"ASCII text":               "text/plain; charset=us-ascii",
		"Unicode text, UTF-8 text": "text/plain; charset=utf-8",
		"directory":                "inode/directory; charset=binary",
		"sticky, directory":        "inode/directory; charset=binary",
		"empty":                    "inode/x-empty; charset=us-ascii",
		"symbolic link to /x":      "inode/symlink; charset=binary",
	}
	for description, want := range cases {
		if got := mimeFor(description); got != want {
			t.Fatalf("mimeFor(%q) = %q, want %q", description, got, want)
		}
	}

	if major, minor := linuxDeviceMajorMinor(linuxMakeDevice(1, 3)); major != 1 || minor != 3 {
		t.Fatalf("linuxDeviceMajorMinor round-trip = (%d, %d), want (1, 3)", major, minor)
	}
	desc2, err := describeFile("/dev/null", false)
	if err != nil || desc2 != "character special (1/3)" {
		t.Fatalf("describeFile(/dev/null) = (%q, %v)", desc2, err)
	}
}

func TestTimeoutAndTop(t *testing.T) {
	status, _, _ := captureApplet(t, cmdTimeout, []string{"0", "/bin/true"}, "")
	if status != 0 {
		t.Fatalf("disabled timeout status = %d", status)
	}
	status, _, _ = captureApplet(t, cmdTimeout, []string{"0.02s", "/bin/sleep", "1"}, "")
	if status != 124 {
		t.Fatalf("timeout status = %d", status)
	}
	status, output, stderr := captureApplet(t, cmdTop, []string{"-b", "-n", "1"}, "")
	if status != 0 || stderr != "" || !strings.Contains(output, "load average:") || !strings.Contains(output, "PID USER") {
		t.Fatalf("top = (%d, %q, %q)", status, output, stderr)
	}
}

func TestReadBlockDevices(t *testing.T) {
	directory := t.TempDir()
	sysRoot := filepath.Join(directory, "block")
	for _, name := range []string{"sda", "sda1"} {
		path := filepath.Join(sysRoot, name)
		if err := os.MkdirAll(path, 0o755); err != nil { //nolint:gosec // Synthetic sysfs paths intentionally match real sysfs traversal modes.
			t.Fatal(err)
		}
		for file, value := range map[string]string{"dev": "8:0\n", "size": "2048\n", "removable": "0\n", "ro": "0\n"} {
			if err := os.WriteFile(filepath.Join(path, file), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(sysRoot, "sda1", "partition"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mountInfo := filepath.Join(directory, "mountinfo")
	if err := os.WriteFile(mountInfo, []byte("1 0 8:1 / /mnt rw - ext4 /dev/sda1 rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	devices, err := readBlockDevices(sysRoot, mountInfo)
	if err != nil || len(devices) != 2 {
		t.Fatalf("devices = %#v, %v", devices, err)
	}
	var partition blockDevice
	for _, device := range devices {
		if device.name == "sda1" {
			partition = device
		}
	}
	if partition.kind != "part" || partition.parent != "sda" || partition.mountpoint != "/mnt" || partition.bytes != 1<<20 {
		t.Fatalf("partition = %#v", partition)
	}
}

func TestModuleDatabaseResolution(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "modules.dep"), []byte("kernel/net/main.ko: kernel/lib/helper.ko\nkernel/lib/helper.ko:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "modules.alias"), []byte("alias pci:v00001234* main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := readModuleDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	if target := database.resolve("pci:v00001234d00005678"); target != "main" {
		t.Fatalf("alias resolved to %q", target)
	}
	if order := strings.Join(database.loadOrder("main"), ","); order != "helper,main" {
		t.Fatalf("load order = %q", order)
	}
	if moduleName("foo-bar.ko.xz") != "foo_bar" {
		t.Fatalf("module name normalization failed")
	}
}

func TestDHCPPacketRoundTrip(t *testing.T) {
	hardware := net.HardwareAddr{0x02, 0, 0, 0, 0, 1}
	transactionID := uint32(0x12345678)
	packet := make([]byte, 240)
	packet[0], packet[1], packet[2] = 2, 1, 6
	binary.BigEndian.PutUint32(packet[4:8], transactionID)
	copy(packet[16:20], net.IPv4(192, 0, 2, 10).To4())
	copy(packet[28:34], hardware)
	copy(packet[236:240], []byte{99, 130, 83, 99})
	packet = appendDHCPOption(packet, 53, []byte{dhcpAck})
	packet = appendDHCPOption(packet, 54, net.IPv4(192, 0, 2, 1).To4())
	packet = appendDHCPOption(packet, 1, net.CIDRMask(24, 32))
	packet = appendDHCPOption(packet, 3, net.IPv4(192, 0, 2, 1).To4())
	packet = appendDHCPOption(packet, 6, append(net.IPv4(1, 1, 1, 1).To4(), net.IPv4(8, 8, 8, 8).To4()...))
	packet = append(packet, 255)
	lease, messageType, err := parseDHCPReply(packet, transactionID, hardware)
	if err != nil || messageType != dhcpAck || lease.address.String() != "192.0.2.10" || len(lease.dns) != 2 {
		t.Fatalf("lease=%#v type=%d err=%v", lease, messageType, err)
	}
	discover := buildDHCPPacket(transactionID, hardware, dhcpDiscover, dhcpLease{}, "rescue")
	options, err := parseDHCPOptions(discover[240:])
	if err != nil || len(options[53]) != 1 || options[53][0] != dhcpDiscover || string(options[12]) != "rescue" {
		t.Fatalf("discover options=%#v err=%v", options, err)
	}
}

func TestPrivilegedPriorityGuardsAndHardening(t *testing.T) {
	if os.Getpid() != 1 {
		status, _, stderr := captureApplet(t, cmdSwitchRoot, []string{"/", "/bin/init"}, "")
		if status != 1 || !strings.Contains(stderr, "PID 1") {
			t.Fatalf("switch_root guard = (%d, %q)", status, stderr)
		}
	}
	for _, name := range []string{"chroot", "insmod", "modprobe", "pivot_root", "rmmod", "switch_root", "timeout", "udhcpc"} {
		if !appletNeedsUnrestrictedSyscalls(name) {
			t.Errorf("%s should bypass seccomp", name)
		}
	}
	for _, name := range []string{"chroot", "switch_root", "timeout"} {
		profile := hardeningForApplet(name, 42, true)
		if profile.noNewPrivs || profile.seccomp {
			t.Errorf("%s execution profile = %+v", name, profile)
		}
	}
	// pivot_root keeps no_new_privs (it does not exec anything, unlike
	// chroot/switch_root) and only sheds seccomp for the pivot_root(2) call.
	if profile := hardeningForApplet("pivot_root", 42, true); !profile.noNewPrivs || profile.seccomp {
		t.Errorf("pivot_root execution profile = %+v", profile)
	}
}

func TestPivotRootArgumentGuard(t *testing.T) {
	status, _, stderr := captureApplet(t, cmdPivotRoot, []string{"/new-root"}, "")
	if status != 1 || !strings.Contains(stderr, "NEW_ROOT PUT_OLD") {
		t.Fatalf("pivot_root missing arg = (%d, %q)", status, stderr)
	}
	status, _, stderr = captureApplet(t, cmdPivotRoot, []string{"/new-root", "/new-root/old", "extra"}, "")
	if status != 1 || !strings.Contains(stderr, "NEW_ROOT PUT_OLD") {
		t.Fatalf("pivot_root extra arg = (%d, %q)", status, stderr)
	}
}

// TestLosetupTable pins the two listing forms and the column rules, which are
// what losetup prints when it is only asked to look.
func TestLosetupTable(t *testing.T) {
	devices := readLoopDevices()
	if len(devices) == 0 {
		t.Skip("no loop devices are configured")
	}
	status, out, errOut := captureApplet(t, cmdLosetup, nil, "")
	if status != 0 {
		t.Fatalf("losetup = (%d, %q)", status, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if strings.Join(strings.Fields(lines[0]), " ") != "NAME SIZELIMIT OFFSET AUTOCLEAR RO BACK-FILE DIO LOG-SEC" {
		t.Fatalf("losetup header = %q", lines[0])
	}
	// -a is the older one-line-per-device form.
	_, older, _ := captureApplet(t, cmdLosetup, []string{"-a"}, "")
	for _, line := range strings.Split(strings.TrimRight(older, "\n"), "\n") {
		if !strings.HasPrefix(line, "/dev/loop") || !strings.Contains(line, ": [") || !strings.HasSuffix(line, ")") {
			t.Fatalf("losetup -a printed %q", line)
		}
	}
	// -O chooses the columns, and an unknown one is refused by name.
	_, chosen, _ := captureApplet(t, cmdLosetup, []string{"-O", "NAME,BACK-FILE"}, "")
	if fields := strings.Fields(strings.Split(chosen, "\n")[0]); len(fields) != 2 || fields[0] != "NAME" {
		t.Fatalf("losetup -O = %q", chosen)
	}
	status, out, errOut = captureApplet(t, cmdLosetup, []string{"-O", "BOGUS"}, "")
	if status != 1 || out != "" || !strings.Contains(errOut, "unknown column: BOGUS") {
		t.Fatalf("losetup -O BOGUS = (%d, %q, %q)", status, out, errOut)
	}
	// -j keeps only the devices backed by that file.
	_, filtered, _ := captureApplet(t, cmdLosetup, []string{"-j", devices[0].backingFile}, "")
	if !strings.Contains(filtered, devices[0].name) {
		t.Fatalf("losetup -j = %q", filtered)
	}
	if _, empty, _ := captureApplet(t, cmdLosetup, []string{"-j", "/definitely/absent"}, ""); empty != "" {
		t.Fatalf("losetup -j on an unused file = %q", empty)
	}
	// --noheadings on its own has no listing to qualify.
	if status, _, errOut = captureApplet(t, cmdLosetup, []string{"-n"}, ""); status != 1 ||
		!strings.Contains(errOut, "no loop device specified") {
		t.Fatalf("losetup -n = (%d, %q)", status, errOut)
	}
	// An unknown option carries the original's Try line.
	if status, _, errOut = captureApplet(t, cmdLosetup, []string{"--nosuch"}, ""); status != 1 ||
		!strings.Contains(errOut, "unrecognized option '--nosuch'") ||
		!strings.Contains(errOut, "Try 'losetup --help'") {
		t.Fatalf("losetup --nosuch = (%d, %q)", status, errOut)
	}
}
