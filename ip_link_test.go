//go:build linux

package main

import (
	"encoding/binary"
	"testing"
)

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
