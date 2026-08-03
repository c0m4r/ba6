//go:build linux

package main

import (
	"encoding/binary"
	"testing"
)

func TestParseIptablesAppendAndDelete(t *testing.T) {
	spec, err := parseIptables([]string{"-A", "INPUT", "-p", "tcp", "-s", "192.0.2.0/24", "--dport", "22", "-j", "ACCEPT"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.command != 'A' || spec.chain != "INPUT" || spec.protocol != "tcp" || spec.destPort == nil || *spec.destPort != 22 || spec.target != "ACCEPT" {
		t.Fatalf("unexpected rule: %+v", spec)
	}
	if got := iptablesRuleText(spec); got != "-p tcp -s 192.0.2.0/24 -d 0.0.0.0/0 --dport 22 -j ACCEPT" {
		t.Fatalf("rule text=%q", got)
	}
	deleted, err := parseIptables([]string{"-D", "INPUT", "2"})
	if err != nil || deleted.deleteLine != 2 {
		t.Fatalf("numeric delete=%+v err=%v", deleted, err)
	}
}

func TestParseIptablesPolicyAndValidation(t *testing.T) {
	policy, err := parseIptables([]string{"-P", "FORWARD", "DROP"})
	if err != nil || policy.command != 'P' || policy.chain != "FORWARD" || policy.target != "DROP" {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	invalid := [][]string{
		{"-A", "INPUT", "--dport", "53", "-j", "ACCEPT"},
		{"-A", "INPUT", "-p", "tcp", "-j", "LOG"},
		{"-P", "INPUT", "REJECT"},
		{"-A", "PREROUTING", "-j", "DROP"},
	}
	for _, args := range invalid {
		if _, err := parseIptables(args); err == nil {
			t.Errorf("parseIptables(%q) succeeded", args)
		}
	}
}

func TestBuildIptablesExpressions(t *testing.T) {
	spec, err := parseIptables([]string{"-A", "OUTPUT", "-p", "udp", "-d", "198.51.100.0/24", "--dport", "53", "-j", "DROP"})
	if err != nil {
		t.Fatal(err)
	}
	expressions, err := parseRawNetlinkAttributes(buildIptablesExpressions(spec))
	if err != nil {
		t.Fatal(err)
	}
	// protocol payload+cmp, destination payload+bitwise+cmp, port
	// payload+cmp, and verdict immediate.
	if len(expressions) != 8 {
		t.Fatalf("expression count=%d", len(expressions))
	}
	for _, expression := range expressions {
		if expression.typeID != nftaListElem {
			t.Fatalf("unexpected list attribute type %d", expression.typeID)
		}
	}
}

func TestNftBatchTargetsNftablesSubsystem(t *testing.T) {
	message := nftBatchGenMessage()
	if len(message) != 4 || binary.BigEndian.Uint16(message[2:]) != nfnlSubsysNFT {
		t.Fatalf("batch nfgenmsg=%v", message)
	}
}
