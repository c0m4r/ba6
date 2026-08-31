// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestExpandIptablesOptions(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"-nvL"}, []string{"-n", "-v", "-L"}},
		{[]string{"-L", "-vxn"}, []string{"-L", "-v", "-x", "-n"}},
		{[]string{"-tnat", "-S"}, []string{"-t", "nat", "-S"}},
		{[]string{"--line-numbers", "-D", "INPUT", "2"}, []string{"--line-numbers", "-D", "INPUT", "2"}},
	}
	for _, test := range cases {
		got := expandIptablesOptions(test.args)
		if len(got) != len(test.want) {
			t.Fatalf("expandIptablesOptions(%q)=%q", test.args, got)
		}
		for i := range got {
			if got[i] != test.want[i] {
				t.Fatalf("expandIptablesOptions(%q)=%q want %q", test.args, got, test.want)
			}
		}
	}
}

func TestParseIptablesListOptions(t *testing.T) {
	spec, err := parseIptables([]string{"-L", "-vxn", "--line-numbers"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.command != 'L' || spec.table != "filter" || spec.chain != "" {
		t.Fatalf("unexpected command: %+v", spec)
	}
	if !spec.list.verbose || !spec.list.exact || !spec.list.numeric || !spec.list.lineNumbers {
		t.Fatalf("list options=%+v", spec.list)
	}
	save, err := parseIptables([]string{"-t", "nat", "-S", "POSTROUTING"})
	if err != nil || save.command != 'S' || save.table != "nat" || save.chain != "POSTROUTING" {
		t.Fatalf("save=%+v err=%v", save, err)
	}
}

func TestParseIptablesAppendAndDelete(t *testing.T) {
	spec, err := parseIptables([]string{"-A", "INPUT", "-p", "tcp", "-s", "192.0.2.0/24", "--dport", "22", "-j", "ACCEPT"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.command != 'A' || spec.chain != "INPUT" || spec.match.proto != protocolTCP || spec.match.target != "ACCEPT" {
		t.Fatalf("unexpected rule: %+v", spec)
	}
	want := "-s 192.0.2.0/24 -p tcp -m tcp --dport 22 -j ACCEPT"
	if got := iptSaveRuleArguments(iptablesSpecRule(spec.match)); got != want {
		t.Fatalf("rule text=%q want %q", got, want)
	}
	negated, err := parseIptables([]string{"-A", "FORWARD", "!", "-i", "eno1", "-o", "docker0", "-j", "DOCKER-USER"})
	if err != nil {
		t.Fatal(err)
	}
	want = "! -i eno1 -o docker0 -j DOCKER-USER"
	if got := iptSaveRuleArguments(iptablesSpecRule(negated.match)); got != want {
		t.Fatalf("negated rule=%q want %q", got, want)
	}
	deleted, err := parseIptables([]string{"-D", "INPUT", "2"})
	if err != nil || deleted.deleteLine != 2 {
		t.Fatalf("numeric delete=%+v err=%v", deleted, err)
	}
}

func TestParseIptablesPolicyAndValidation(t *testing.T) {
	policy, err := parseIptables([]string{"-P", "FORWARD", "DROP"})
	if err != nil || policy.command != 'P' || policy.chain != "FORWARD" || policy.policy != "DROP" {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	invalid := [][]string{
		{"-A", "INPUT", "--dport", "53", "-j", "ACCEPT"},
		{"-P", "INPUT", "REJECT"},
		{"-L", "-p", "tcp"},
		{"-A", "INPUT", "-p", "tcp"},
		{"-D", "INPUT", "2", "-p", "tcp", "-j", "DROP"},
		{"-A", "INPUT", "-m", "recent", "-j", "DROP"},
		{"-L", "-6"},
		{"-t", "bogus", "-L"},
	}
	for _, args := range invalid {
		if _, err := parseIptables(args); err == nil {
			t.Errorf("parseIptables(%q) succeeded", args)
		}
	}
}

// TestIptablesExpressionRoundTrip builds the nft expressions for a rule and
// decodes them again: the encoder and the decoder have to agree on every
// attribute layout, including the xtables blob the port match travels in.
func TestIptablesExpressionRoundTrip(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-A", "INPUT", "-p", "tcp", "--dport", "22", "-j", "ACCEPT"}, "-p tcp -m tcp --dport 22 -j ACCEPT"},
		{[]string{"-A", "INPUT", "-p", "udp", "-d", "198.51.100.0/24", "--dport", "53", "-j", "DROP"}, "-d 198.51.100.0/24 -p udp -m udp --dport 53 -j DROP"},
		{[]string{"-A", "INPUT", "-p", "icmp", "--icmp-type", "8", "-j", "ACCEPT"}, "-p icmp -m icmp --icmp-type 8 -j ACCEPT"},
		{[]string{"-A", "FORWARD", "!", "-i", "eno1", "-o", "docker0", "-j", "DROP"}, "! -i eno1 -o docker0 -j DROP"},
		{[]string{"-A", "INPUT", "-s", "10.0.0.1", "-j", "RETURN"}, "-s 10.0.0.1/32 -j RETURN"},
		{[]string{"-A", "INPUT", "-p", "tcp", "--dport", "1000:2000", "-j", "REJECT"}, "-p tcp -m tcp --dport 1000:2000 -j REJECT --reject-with icmp-port-unreachable"},
		{[]string{"-A", "INPUT", "-i", "wg0", "-j", "f2b-sshd"}, "-i wg0 -j f2b-sshd"},
	}
	for _, test := range cases {
		spec, err := parseIptables(test.args)
		if err != nil {
			t.Fatalf("parseIptables(%q): %v", test.args, err)
		}
		if got := iptSaveRuleArguments(iptablesSpecRule(spec.match)); got != test.want {
			t.Errorf("spec render(%q)=%q want %q", test.args, got, test.want)
		}
		for _, compat := range []bool{true, false} {
			decoded := &iptRule{}
			decodeIptablesExpressions(buildIptablesExpressions(spec.match, compat), decoded, true)
			if got := iptSaveRuleArguments(decoded); got != test.want {
				t.Errorf("decode(%q, compat=%v)=%q want %q", test.args, compat, got, test.want)
			}
		}
	}
}

func TestIptablesExpressionCounters(t *testing.T) {
	spec, err := parseIptables([]string{"-A", "INPUT", "-j", "ACCEPT"})
	if err != nil {
		t.Fatal(err)
	}
	expressions, err := parseRawNetlinkAttributes(buildIptablesExpressions(spec.match, true))
	if err != nil {
		t.Fatal(err)
	}
	// A bare rule is a counter plus the verdict, and every element of the list
	// carries the same attribute type.
	if len(expressions) != 2 {
		t.Fatalf("expression count=%d", len(expressions))
	}
	for _, expression := range expressions {
		if expression.typeID != nftaListElem {
			t.Fatalf("unexpected list attribute type %d", expression.typeID)
		}
	}
}

// TestIptablesListLayout pins the -L column layout to real "iptables -L -vxn"
// output, down to the padding of every field.
func TestIptablesListLayout(t *testing.T) {
	opts := iptListOptions{verbose: true, exact: true, numeric: true}
	header := "    pkts      bytes target     prot opt in     out     source               destination         "
	if got := iptColumnHeaderLine(opts); got != header {
		t.Errorf("column header=%q\n            want %q", got, header)
	}
	chain := &iptChain{name: "INPUT", base: true, policy: "DROP", packets: 3019211, bytes: 155728110}
	if got, want := iptChainHeaderLine(chain, opts), "Chain INPUT (policy DROP 3019211 packets, 155728110 bytes)"; got != want {
		t.Errorf("chain header=%q want %q", got, want)
	}
	user := &iptChain{name: "DOCKER", refs: 2}
	if got, want := iptChainHeaderLine(user, opts), "Chain DOCKER (2 references)"; got != want {
		t.Errorf("user chain header=%q want %q", got, want)
	}
	cases := []struct {
		rule *iptRule
		want string
	}{
		{
			&iptRule{packets: 75002, bytes: 11275991, target: "f2b-sshd", proto: protocolTCP,
				matches: []iptMatch{{list: "multiport dports 22"}}},
			"   75002 11275991 f2b-sshd   tcp  --  *      *       0.0.0.0/0            0.0.0.0/0            multiport dports 22",
		},
		{
			&iptRule{packets: 132393, bytes: 7583412, target: "ACCEPT", proto: protocolTCP,
				inIface: "eno1", inInv: true, matches: []iptMatch{{list: "tcp dpt:25"}}},
			"  132393  7583412 ACCEPT     tcp  --  !eno1  *       0.0.0.0/0            0.0.0.0/0            tcp dpt:25",
		},
		{
			&iptRule{packets: 10166093, bytes: 19692896121, target: "ACCEPT",
				matches: []iptMatch{{list: "ctstate RELATED,ESTABLISHED"}}},
			"10166093 19692896121 ACCEPT     all  --  *      *       0.0.0.0/0            0.0.0.0/0            ctstate RELATED,ESTABLISHED",
		},
		{
			&iptRule{packets: 1236863, bytes: 1142522999, target: "DOCKER-USER"},
			" 1236863 1142522999 DOCKER-USER  all  --  *      *       0.0.0.0/0            0.0.0.0/0           ",
		},
		{
			&iptRule{packets: 12, bytes: 720, target: "ACCEPT", proto: protocolTCP,
				inIface: "br-26ccb6283d5a", inInv: true, outIface: "br-26ccb6283d5a",
				dst:     &net.IPNet{IP: net.IPv4(172, 27, 0, 4).To4(), Mask: net.CIDRMask(32, 32)},
				matches: []iptMatch{{list: "tcp dpt:8080"}}},
			"      12      720 ACCEPT     tcp  --  !br-26ccb6283d5a br-26ccb6283d5a  0.0.0.0/0            172.27.0.4           tcp dpt:8080",
		},
	}
	for _, test := range cases {
		if got := iptRuleLine(test.rule, 1, opts); got != test.want {
			t.Errorf("rule line=%q\n         want %q", got, test.want)
		}
	}
}

func TestIptablesListNonVerbose(t *testing.T) {
	opts := iptListOptions{numeric: true}
	want := "target     prot opt source               destination         "
	if got := iptColumnHeaderLine(opts); got != want {
		t.Errorf("column header=%q want %q", got, want)
	}
	if got, want := iptChainHeaderLine(&iptChain{name: "INPUT", base: true, policy: "DROP"}, opts), "Chain INPUT (policy DROP)"; got != want {
		t.Errorf("chain header=%q want %q", got, want)
	}
	rule := &iptRule{target: "ACCEPT", proto: protocolTCP, matches: []iptMatch{{list: "tcp dpt:443"}}}
	want = "ACCEPT     tcp  --  0.0.0.0/0            0.0.0.0/0            tcp dpt:443"
	if got := iptRuleLine(rule, 1, opts); got != want {
		t.Errorf("rule line=%q want %q", got, want)
	}
	numbered := iptListOptions{numeric: true, lineNumbers: true}
	if got, want := iptRuleLine(rule, 7, numbered), "7    "+want; got != want {
		t.Errorf("numbered rule=%q want %q", got, want)
	}
}

// TestIptablesCounterText covers the K/M/G rounding the original applies when
// -x is absent.
func TestIptablesCounterText(t *testing.T) {
	cases := []struct {
		value  uint64
		exact  bool
		padded bool
		want   string
	}{
		{99999, false, true, "99999 "},
		{100000, false, true, " 100K "},
		{19692896121, false, true, "  20G "},
		{155728110, false, false, "156M "},
		{155728110, true, false, "155728110 "},
		{720, true, true, "     720 "},
	}
	for _, test := range cases {
		if got := iptCounterText(test.value, test.exact, test.padded); got != test.want {
			t.Errorf("iptCounterText(%d, %v, %v)=%q want %q", test.value, test.exact, test.padded, got, test.want)
		}
	}
}

// TestIptablesXtMatchDecoding feeds the extension blobs the kernel hands back
// for rules the system tool created.
func TestIptablesXtMatchDecoding(t *testing.T) {
	multiport := make([]byte, 48)
	multiport[0] = 1 // XT_MULTIPORT_DESTINATION
	multiport[1] = 1
	binary.NativeEndian.PutUint16(multiport[2:4], 22)
	decoder := newIptDecoder(&iptRule{}, true)
	decoder.xtMatch("multiport", 1, multiport)
	if got, want := decoder.rule.matches[0].list, "multiport dports 22"; got != want {
		t.Errorf("multiport list=%q want %q", got, want)
	}
	if got, want := decoder.rule.matches[0].save, "-m multiport --dports 22"; got != want {
		t.Errorf("multiport save=%q want %q", got, want)
	}

	conntrack := make([]byte, 164)
	binary.NativeEndian.PutUint16(conntrack[146:148], xtConntrackState)
	binary.NativeEndian.PutUint16(conntrack[150:152], 0x02|0x04) // ESTABLISHED,RELATED
	decoder = newIptDecoder(&iptRule{}, true)
	decoder.xtMatch("conntrack", 3, conntrack)
	if got, want := decoder.rule.matches[0].list, "ctstate RELATED,ESTABLISHED"; got != want {
		t.Errorf("conntrack list=%q want %q", got, want)
	}

	tcp := make([]byte, 16)
	binary.NativeEndian.PutUint16(tcp[2:4], 65535) // full source range
	binary.NativeEndian.PutUint16(tcp[4:6], 25)
	binary.NativeEndian.PutUint16(tcp[6:8], 25)
	decoder = newIptDecoder(&iptRule{proto: protocolTCP}, true)
	decoder.xtMatch("tcp", 0, tcp)
	if got, want := decoder.rule.matches[0].list, "tcp dpt:25"; got != want {
		t.Errorf("tcp list=%q want %q", got, want)
	}

	icmp := []byte{8, 0, 255, 0}
	decoder = newIptDecoder(&iptRule{proto: protocolICMP}, true)
	decoder.xtMatch("icmp", 0, icmp)
	if got, want := decoder.rule.matches[0].list, "icmptype 8"; got != want {
		t.Errorf("icmp list=%q want %q", got, want)
	}

	// An extension with no decoder still names itself rather than vanishing.
	decoder = newIptDecoder(&iptRule{}, true)
	decoder.xtMatch("recent", 0, []byte{1, 2, 3})
	if got, want := decoder.rule.matches[0].save, "-m recent"; got != want {
		t.Errorf("unknown match save=%q want %q", got, want)
	}
}

// TestIptablesTargetDecoding covers the xtables targets whose arguments the
// original prints beside the target name.
func TestIptablesTargetDecoding(t *testing.T) {
	// struct nf_nat_range2: flags, min and max address, then the port range.
	dnat := make([]byte, 48)
	binary.NativeEndian.PutUint32(dnat[0:4], 0x03) // MAP_IPS|PROTO_SPECIFIED
	copy(dnat[4:8], []byte{172, 27, 0, 4})
	copy(dnat[20:24], []byte{172, 27, 0, 4})
	binary.BigEndian.PutUint16(dnat[36:38], 8080)
	binary.BigEndian.PutUint16(dnat[38:40], 8080)
	decoder := newIptDecoder(&iptRule{}, true)
	decoder.xtTarget("DNAT", 2, dnat)
	if got, want := decoder.rule.targetList, "to:172.27.0.4:8080"; got != want {
		t.Errorf("DNAT list=%q want %q", got, want)
	}
	if got, want := decoder.rule.targetSave, "--to-destination 172.27.0.4:8080"; got != want {
		t.Errorf("DNAT save=%q want %q", got, want)
	}

	// struct nf_nat_ipv4_multi_range_compat: a count, then one range.
	snat := make([]byte, 24)
	binary.NativeEndian.PutUint32(snat[0:4], 1)
	binary.NativeEndian.PutUint32(snat[4:8], 0x01)
	copy(snat[8:12], []byte{1, 2, 3, 4})
	copy(snat[12:16], []byte{1, 2, 3, 4})
	decoder = newIptDecoder(&iptRule{}, true)
	decoder.xtTarget("SNAT", 0, snat)
	if got, want := decoder.rule.targetSave, "--to-source 1.2.3.4"; got != want {
		t.Errorf("SNAT save=%q want %q", got, want)
	}

	mark := make([]byte, 8)
	binary.NativeEndian.PutUint32(mark[0:4], 1)
	binary.NativeEndian.PutUint32(mark[4:8], 0xffffffff)
	decoder = newIptDecoder(&iptRule{}, true)
	decoder.xtTarget("MARK", 2, mark)
	if got, want := decoder.rule.targetList, "MARK set 0x1"; got != want {
		t.Errorf("MARK list=%q want %q", got, want)
	}
	if got, want := decoder.rule.targetSave, "--set-xmark 0x1/0xffffffff"; got != want {
		t.Errorf("MARK save=%q want %q", got, want)
	}

	reject := make([]byte, 8)
	binary.NativeEndian.PutUint32(reject[0:4], 6) // icmp-host-prohibited
	decoder = newIptDecoder(&iptRule{}, true)
	decoder.xtTarget("REJECT", 0, reject)
	if got, want := decoder.rule.targetList, "reject-with icmp-host-prohibited"; got != want {
		t.Errorf("REJECT list=%q want %q", got, want)
	}
}

func TestIptablesFragmentRoundTrip(t *testing.T) {
	for _, args := range [][]string{
		{"-A", "INPUT", "-f", "-j", "DROP"},
		{"-A", "INPUT", "!", "-f", "-j", "DROP"},
	} {
		spec, err := parseIptables(args)
		if err != nil {
			t.Fatal(err)
		}
		rule := &iptRule{}
		decodeIptablesExpressions(buildIptablesExpressions(spec.match, true), rule, true)
		if !rule.fragment || rule.fragInv != spec.match.fragInv {
			t.Errorf("fragment round trip(%q)=%+v", args, rule)
		}
	}
}

func TestIptablesVerdictDecoding(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{{"ACCEPT", "ACCEPT"}, {"DROP", "DROP"}, {"RETURN", "RETURN"}, {"QUEUE", "QUEUE"}, {"DOCKER-USER", "DOCKER-USER"}}
	for _, test := range cases {
		rule := &iptRule{}
		decodeIptablesExpressions(buildIptablesTarget(iptMatchSpec{target: test.target}, true), rule, true)
		if rule.target != test.want {
			t.Errorf("target=%q want %q", rule.target, test.want)
		}
	}
	rule := &iptRule{}
	decodeIptablesExpressions(buildIptablesTarget(iptMatchSpec{target: "DOCKER", targetGoto: true}, true), rule, true)
	if !rule.targetGoto || rule.target != "DOCKER" {
		t.Fatalf("goto verdict=%+v", rule)
	}
	if got, want := iptSaveRuleArguments(rule), "-g DOCKER"; got != want {
		t.Errorf("goto save=%q want %q", got, want)
	}
}

func TestIptablesInterfaceWildcard(t *testing.T) {
	if got := iptInterfaceName([]byte("eno1\x00")); got != "eno1" {
		t.Errorf("exact interface=%q", got)
	}
	if got := iptInterfaceName([]byte("br-")); got != "br-+" {
		t.Errorf("wildcard interface=%q", got)
	}
	spec, err := parseIptables([]string{"-A", "INPUT", "-i", "eth+", "-j", "DROP"})
	if err != nil {
		t.Fatal(err)
	}
	rule := &iptRule{}
	decodeIptablesExpressions(buildIptablesExpressions(spec.match, true), rule, true)
	if rule.inIface != "eth+" {
		t.Fatalf("wildcard round trip=%q", rule.inIface)
	}
}

func TestIptablesLimitRateAndCtStates(t *testing.T) {
	if got := iptablesLimitRate(10000); got != "1/sec" {
		t.Errorf("limit rate=%q", got)
	}
	if got := iptablesLimitRate(20000); got != "30/min" {
		t.Errorf("limit rate=%q", got)
	}
	if got := iptablesCtStateNames(0x02|0x04, true); got != "RELATED,ESTABLISHED" {
		t.Errorf("ct states=%q", got)
	}
	if got := iptablesCtStateNames(0x01|0x08, false); got != "INVALID,NEW" {
		t.Errorf("ct states=%q", got)
	}
}

func TestNftBatchTargetsNftablesSubsystem(t *testing.T) {
	message := nftBatchGenMessage()
	if len(message) != 4 || binary.BigEndian.Uint16(message[2:]) != nfnlSubsysNFT {
		t.Fatalf("batch nfgenmsg=%v", message)
	}
}
