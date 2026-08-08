// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"encoding/binary"
	"strings"
	"testing"
)

// TestIPColorOptionAndUpFilter covers the two invocations the iproute2 bash
// completion makes: "ip -c=never link show" and "ip link show up".
func TestIPColorOptionAndUpFilter(t *testing.T) {
	status, _, stderr := captureApplet(t, cmdIP, []string{"-c=never", "link", "show", "dev", "ba6-absent0"}, "")
	if status == 0 || !strings.Contains(stderr, "does not exist") {
		t.Fatalf("ip -c=never=(%d,%q)", status, stderr)
	}
	status, stdout, stderr := captureApplet(t, cmdIP, []string{"link", "show", "up"}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("ip link show up=(%d,%q)", status, stderr)
	}
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		if line != "" && !strings.Contains(line, "UP") {
			t.Fatalf("ip link show up listed a down link: %q", line)
		}
	}
}

func TestParseBondLinkAdd(t *testing.T) {
	spec, err := parseLinkAdd([]string{"bond0", "type", "bond", "mode", "active-backup", "miimon", "100"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.name != "bond0" || spec.kind != "bond" || spec.bondMode == nil || *spec.bondMode != 1 ||
		spec.miimon == nil || *spec.miimon != 100 {
		t.Fatalf("unexpected bond spec: %+v", spec)
	}
}

func TestParseVLANLinkAdd(t *testing.T) {
	spec, err := parseLinkAdd([]string{"link", "bond0", "name", "bond0.100", "type", "vlan", "id", "100"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.name != "bond0.100" || spec.kind != "vlan" || spec.parent != "bond0" ||
		spec.vlanID == nil || *spec.vlanID != 100 {
		t.Fatalf("unexpected VLAN spec: %+v", spec)
	}
}

func TestLinkAddRejectsInvalidBondAndVLANValues(t *testing.T) {
	tests := [][]string{
		{"bond0", "type", "bond", "mode", "not-a-mode"},
		{"link", "eth0", "name", "eth0.4095", "type", "vlan", "id", "4095"},
		{"vlan100", "type", "vlan", "id", "100"},
	}
	for _, args := range tests {
		if _, err := parseLinkAdd(args); err == nil {
			t.Errorf("parseLinkAdd(%q) succeeded", args)
		}
	}
}

func TestParseCombinedLinkSet(t *testing.T) {
	spec, err := parseLinkSet([]string{"dev", "dummy0", "master", "bond0", "up"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.name != "dummy0" || spec.master != "bond0" || spec.up == nil || !*spec.up {
		t.Fatalf("unexpected set spec: %+v", spec)
	}
}

func TestParseNestedLinkInfo(t *testing.T) {
	mode := netlinkAttribute(iflaBondMode, []byte{1})
	miimonValue := make([]byte, 4)
	binary.NativeEndian.PutUint32(miimonValue, 100)
	data := append([]byte(nil), mode...)
	data = append(data, netlinkAttribute(iflaBondMiimon, miimonValue)...)
	info := netlinkAttribute(iflaInfoKind, []byte("bond\x00"))
	info = append(info, netlinkAttribute(iflaInfoData|nlaFNested, data)...)
	link := linkDetails{}
	if err := parseLinkInfo(&link, info); err != nil {
		t.Fatal(err)
	}
	if link.kind != "bond" || link.bondMode == nil || *link.bondMode != 1 || link.miimon == nil || *link.miimon != 100 {
		t.Fatalf("unexpected link details: %+v", link)
	}
}
