// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestLsmodColumns pins the layout kmod prints: nineteen columns for the name
// and eight for the size, with the holders listed in the order sysfs gives
// them rather than the order /proc/modules does.
func TestLsmodColumns(t *testing.T) {
	status, out, errOut := captureApplet(t, cmdLsmod, nil, "")
	if status != 0 {
		t.Skipf("lsmod = (%d, %q)", status, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[0] != "Module                  Size  Used by" {
		t.Fatalf("lsmod header = %q", lines[0])
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			t.Fatalf("lsmod printed %q", line)
		}
		if _, err := strconv.Atoi(fields[1]); err != nil {
			t.Fatalf("lsmod size column = %q", fields[1])
		}
		// The name occupies nineteen columns and the size eight, so a row
		// whose values fit both ends its size at column 28.
		if len(fields[0]) < 19 && len(fields[1]) < 8 && line[28] != ' ' {
			t.Fatalf("lsmod columns are misaligned: %q", line)
		}
	}
	// The options kmod accepts change nothing here.
	if status, _, _ := captureApplet(t, cmdLsmod, []string{"-s", "-v"}, ""); status != 0 {
		t.Fatalf("lsmod -s -v = %d", status)
	}
	if status, _, errOut := captureApplet(t, cmdLsmod, []string{"nonsense"}, ""); status != 1 ||
		!strings.Contains(errOut, "unexpected operand") {
		t.Fatalf("lsmod with an operand = (%d, %q)", status, errOut)
	}
}

// TestModuleToolDiagnostics pins the wording kmod uses for the failures an
// unprivileged run can reach.
func TestModuleToolDiagnostics(t *testing.T) {
	for _, c := range []struct {
		applet applet
		args   []string
		want   string
	}{
		{cmdRmmod, nil, "ERROR: missing module name."},
		// rmmod names the module the way the kernel spells it, with the dashes
		// turned into underscores.
		{cmdRmmod, []string{"definitely-no-such-module"}, "ERROR: Module definitely_no_such_module is not currently loaded"},
		{cmdRmmod, []string{"--nosuch"}, "unrecognized option '--nosuch'"},
		{cmdInsmod, nil, "ERROR: missing filename."},
		{cmdInsmod, []string{"/definitely/absent.ko"}, "ERROR: could not load module /definitely/absent.ko: No such file or directory"},
		{cmdInsmod, []string{"--nosuch"}, "unrecognized option '--nosuch'"},
		{cmdModprobe, nil, "ERROR: missing parameters. See -h."},
		{cmdModprobe, []string{"--nosuch"}, "unrecognized option '--nosuch'"},
	} {
		status, out, errOut := captureApplet(t, c.applet, c.args, "")
		if status != 1 || out != "" || !strings.Contains(errOut, c.want) {
			t.Fatalf("%v = (%d, %q, %q), want %q", c.args, status, out, errOut, c.want)
		}
	}
	// A module name nothing answers to is kmod's fatal error, naming the
	// directory it searched; -q says nothing at all.
	status, _, errOut := captureApplet(t, cmdModprobe, []string{"definitely-no-such-module"}, "")
	if status != 1 || !strings.Contains(errOut, "FATAL: Module definitely-no-such-module not found in directory /lib/modules/") {
		t.Fatalf("modprobe on an unknown module = (%d, %q)", status, errOut)
	}
	if status, _, errOut = captureApplet(t, cmdModprobe, []string{"-q", "definitely-no-such-module"}, ""); status != 1 || errOut != "" {
		t.Fatalf("modprobe -q = (%d, %q)", status, errOut)
	}
}

// TestModuleLoadOrder pins the order dependencies are inserted in: modules.dep
// lists a module's whole closure nearest-first, so loading walks it backwards
// and the module itself goes last.
func TestModuleLoadOrder(t *testing.T) {
	dir := t.TempDir()
	dep := "kernel/a.ko: kernel/b.ko kernel/c.ko\nkernel/b.ko:\nkernel/c.ko:\n"
	if err := os.WriteFile(filepath.Join(dir, "modules.dep"), []byte(dep), 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := readModuleDatabase(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(database.loadOrder("a"), ","); got != "c,b,a" {
		t.Fatalf("load order = %q, want c,b,a", got)
	}
	if got := strings.Join(database.loadOrder("b"), ","); got != "b" {
		t.Fatalf("load order for a leaf = %q", got)
	}
}
