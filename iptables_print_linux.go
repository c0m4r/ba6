// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// The column layout of -L is load bearing: it is what every script that parses
// a firewall listing expects. Counters are 8 and 10 wide in the header but both
// 8 wide in the rows, exactly as the original prints them, and each match adds
// its text with a single leading space.

const xtConntrackState = 0x0001

func listIptables(spec iptablesSpec) error {
	ruleset, err := readIptablesTable(spec.table, true, spec.list.numeric)
	if err != nil {
		return err
	}
	chains := ruleset.chains
	if spec.chain != "" {
		chain := ruleset.chain(spec.chain)
		if chain == nil {
			return syscall.ENOENT
		}
		chains = []*iptChain{chain}
	}
	out := bufio.NewWriter(os.Stdout)
	for index, chain := range chains {
		if index > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, iptChainHeaderLine(chain, spec.list))
		fmt.Fprintln(out, iptColumnHeaderLine(spec.list))
		for number, rule := range chain.rules {
			fmt.Fprintln(out, iptRuleLine(rule, number+1, spec.list))
		}
	}
	return out.Flush()
}

func saveIptables(spec iptablesSpec) error {
	ruleset, err := readIptablesTable(spec.table, true, true)
	if err != nil {
		return err
	}
	chains := ruleset.chains
	if spec.chain != "" {
		chain := ruleset.chain(spec.chain)
		if chain == nil {
			return syscall.ENOENT
		}
		chains = []*iptChain{chain}
	}
	out := bufio.NewWriter(os.Stdout)
	for _, chain := range chains {
		if chain.base {
			fmt.Fprintf(out, "-P %s %s\n", chain.name, chain.policy)
			continue
		}
		fmt.Fprintf(out, "-N %s\n", chain.name)
	}
	for _, chain := range chains {
		for _, rule := range chain.rules {
			if arguments := iptSaveRuleArguments(rule); arguments != "" {
				fmt.Fprintf(out, "-A %s %s\n", chain.name, arguments)
				continue
			}
			fmt.Fprintf(out, "-A %s\n", chain.name)
		}
	}
	return out.Flush()
}

func iptChainHeaderLine(chain *iptChain, opts iptListOptions) string {
	if !chain.base {
		return fmt.Sprintf("Chain %s (%d references)", chain.name, chain.refs)
	}
	header := fmt.Sprintf("Chain %s (policy %s", chain.name, chain.policy)
	if opts.verbose {
		header += " " + iptCounterText(chain.packets, opts.exact, false) + "packets, " +
			iptCounterText(chain.bytes, opts.exact, false) + "bytes"
	}
	return header + ")"
}

func iptColumnHeaderLine(opts iptListOptions) string {
	var header strings.Builder
	if opts.lineNumbers {
		fmt.Fprintf(&header, "%-4s ", "num")
	}
	if opts.verbose {
		if opts.exact {
			fmt.Fprintf(&header, "%8s %10s ", "pkts", "bytes")
		} else {
			fmt.Fprintf(&header, "%5s %5s ", "pkts", "bytes")
		}
	}
	fmt.Fprintf(&header, "%-9s  prot opt", "target")
	if opts.verbose {
		fmt.Fprintf(&header, " %-6s %-6s ", "in", "out")
	}
	fmt.Fprintf(&header, " %-20s %-20s", "source", "destination")
	return header.String()
}

func iptRuleLine(rule *iptRule, number int, opts iptListOptions) string {
	var line strings.Builder
	if opts.lineNumbers {
		fmt.Fprintf(&line, "%-4d ", number)
	}
	if opts.verbose {
		line.WriteString(iptCounterText(rule.packets, opts.exact, true))
		line.WriteString(iptCounterText(rule.bytes, opts.exact, true))
	}
	fmt.Fprintf(&line, "%-9s ", rule.target)
	line.WriteString(iptInvertChar(rule.protoInv))
	fmt.Fprintf(&line, "%-5s", iptProtocolText(rule.proto, opts.numeric))
	// The options column reports fragment matching only.
	if rule.fragInv {
		line.WriteString("!")
	} else {
		line.WriteString("-")
	}
	if rule.fragment {
		line.WriteString("f ")
	} else {
		line.WriteString("- ")
	}
	if opts.verbose {
		fmt.Fprintf(&line, " %-6s ", iptInterfaceField(rule.inIface, rule.inInv, opts.numeric))
		fmt.Fprintf(&line, "%-6s ", iptInterfaceField(rule.outIface, rule.outInv, opts.numeric))
	}
	line.WriteString(iptInvertChar(rule.srcInv))
	fmt.Fprintf(&line, "%-19s ", iptAddressText(rule.src, opts.numeric))
	line.WriteString(iptInvertChar(rule.dstInv))
	fmt.Fprintf(&line, "%-19s ", iptAddressText(rule.dst, opts.numeric))
	if rule.targetGoto {
		line.WriteString("[goto] ")
	}
	for _, match := range rule.matches {
		line.WriteString(" " + match.list)
	}
	if rule.targetList != "" {
		line.WriteString(" " + rule.targetList)
	}
	return line.String()
}

// iptCounterText prints a counter the way the original does: exact values are
// padded to eight columns, and otherwise anything above 99999 is rounded to a
// K/M/G suffix. Chain policy counters are printed unpadded.
func iptCounterText(value uint64, exact, padded bool) string {
	if exact {
		if padded {
			return fmt.Sprintf("%8d ", value)
		}
		return fmt.Sprintf("%d ", value)
	}
	if value <= 99999 {
		if padded {
			return fmt.Sprintf("%5d ", value)
		}
		return fmt.Sprintf("%d ", value)
	}
	for _, suffix := range []string{"K", "M", "G"} {
		value = (value + 500) / 1000
		if value <= 9999 || suffix == "G" {
			if padded {
				return fmt.Sprintf("%4d%s ", value, suffix)
			}
			return fmt.Sprintf("%d%s ", value, suffix)
		}
	}
	return ""
}

func iptInvertChar(inverted bool) string {
	if inverted {
		return "!"
	}
	return " "
}

func iptInterfaceField(name string, inverted, numeric bool) string {
	if name == "" {
		name = "any"
		if numeric {
			name = "*"
		}
	}
	if inverted {
		return "!" + name
	}
	return name
}

// iptAddressText renders an address column. A zero mask matches everything,
// which the original prints as "anywhere" unless -n was given.
func iptAddressText(network *net.IPNet, numeric bool) string {
	if network == nil {
		if numeric {
			return "0.0.0.0/0"
		}
		return "anywhere"
	}
	ones, bits := network.Mask.Size()
	if ones == 0 && bits != 0 {
		if numeric {
			return "0.0.0.0/0"
		}
		return "anywhere"
	}
	address := network.IP.String()
	if !numeric {
		address = iptHostText(network.IP)
	}
	if bits == 0 {
		return address + "/" + net.IP(network.Mask).String()
	}
	if ones == 32 {
		return address
	}
	return address + "/" + strconv.Itoa(ones)
}

// iptSaveRuleArguments renders everything that follows "-A CHAIN" in -S output.
// Rules given on the command line are rendered through the same function, which
// is how -D recognises the rule it was asked to remove.
func iptSaveRuleArguments(rule *iptRule) string {
	var parts []string
	appendOption := func(inverted bool, values ...string) {
		if inverted {
			parts = append(parts, "!")
		}
		parts = append(parts, values...)
	}
	if rule.src != nil {
		appendOption(rule.srcInv, "-s", iptSaveAddress(rule.src))
	}
	if rule.dst != nil {
		appendOption(rule.dstInv, "-d", iptSaveAddress(rule.dst))
	}
	if rule.inIface != "" {
		appendOption(rule.inInv, "-i", rule.inIface)
	}
	if rule.outIface != "" {
		appendOption(rule.outInv, "-o", rule.outIface)
	}
	if rule.proto != 0 {
		appendOption(rule.protoInv, "-p", iptProtocolText(rule.proto, false))
	}
	if rule.fragment {
		appendOption(rule.fragInv, "-f")
	}
	for _, match := range rule.matches {
		parts = append(parts, match.save)
	}
	if rule.target != "" {
		jump := "-j"
		if rule.targetGoto {
			jump = "-g"
		}
		parts = append(parts, jump, rule.target)
		if rule.targetSave != "" {
			parts = append(parts, rule.targetSave)
		}
	}
	return strings.Join(parts, " ")
}

func iptSaveAddress(network *net.IPNet) string {
	ones, bits := network.Mask.Size()
	if bits == 0 {
		return network.IP.String() + "/" + net.IP(network.Mask).String()
	}
	return network.IP.String() + "/" + strconv.Itoa(ones)
}

// iptablesSpecRule turns a rule given on the command line into the same shape a
// rule read back from the kernel takes, so the two can be compared.
func iptablesSpecRule(match iptMatchSpec) *iptRule {
	rule := &iptRule{
		proto: match.proto, protoInv: match.protoInv,
		src: match.src, srcInv: match.srcInv,
		dst: match.dst, dstInv: match.dstInv,
		inIface: match.inIface, inInv: match.inInv,
		outIface: match.outIface, outInv: match.outInv,
		fragment: match.fragment, fragInv: match.fragInv,
		target: match.target, targetGoto: match.targetGoto,
	}
	if match.sport != nil || match.dport != nil {
		rule.matches = append(rule.matches, iptPortsMatch(iptProtocolMatchName(match.proto),
			match.proto, match.sport, match.dport, match.sportInv, match.dportInv, true))
	}
	if match.icmpType != nil {
		codeMin, codeMax := byte(0), byte(255)
		if match.icmpCode != nil {
			codeMin, codeMax = *match.icmpCode, *match.icmpCode
		}
		rule.matches = append(rule.matches, iptICMPMatch(*match.icmpType, codeMin, codeMax, match.icmpInv, true))
	}
	if match.target == "REJECT" {
		code, _ := iptablesRejectCode(match.rejectWith)
		rule.targetSave = "--reject-with " + iptablesRejectName(code)
	}
	return rule
}

// iptProtocolText names a protocol number. -n restricts the lookup to the small
// table the original carries, so it never touches /etc/protocols.
func iptProtocolText(proto uint8, numeric bool) string {
	if proto == 0 {
		return "all"
	}
	if !numeric {
		if name, ok := protocolDatabase().names[proto]; ok {
			return name
		}
	}
	for _, entry := range iptablesProtocols {
		if entry.number == proto {
			return entry.name
		}
	}
	return strconv.Itoa(int(proto))
}

var iptablesProtocols = []struct {
	name   string
	number uint8
}{
	{"tcp", protocolTCP}, {"sctp", 132}, {"udp", protocolUDP}, {"udplite", 136},
	{"icmp", protocolICMP}, {"icmpv6", 58}, {"ipv6-icmp", 58}, {"esp", 50},
	{"ah", 51}, {"ipv6-mh", 135}, {"mh", 135}, {"all", 0},
}

func iptablesProtocolNumber(value string) (uint8, bool) {
	value = strings.ToLower(value)
	if value == "all" {
		return 0, true
	}
	if number, err := strconv.ParseUint(value, 10, 8); err == nil {
		return uint8(number), true
	}
	for _, entry := range iptablesProtocols {
		if entry.name == value {
			return entry.number, true
		}
	}
	if number, ok := protocolDatabase().numbers[value]; ok {
		return number, true
	}
	return 0, false
}

type nameDatabase struct {
	names   map[uint8]string
	numbers map[string]uint8
}

var (
	protocolsOnce sync.Once
	protocols     nameDatabase
)

// protocolDatabase reads /etc/protocols. Name resolution here is file based on
// purpose: the applet runs under a seccomp filter that permits netlink only, so
// a resolver socket would take the process down.
func protocolDatabase() nameDatabase {
	protocolsOnce.Do(func() {
		protocols = nameDatabase{names: map[uint8]string{}, numbers: map[string]uint8{}}
		data, err := os.ReadFile("/etc/protocols")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			if index := strings.IndexByte(line, '#'); index >= 0 {
				line = line[:index]
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			number, convErr := strconv.ParseUint(fields[1], 10, 8)
			if convErr != nil {
				continue
			}
			if _, seen := protocols.names[uint8(number)]; !seen {
				protocols.names[uint8(number)] = fields[0]
			}
			for _, name := range append([]string{fields[0]}, fields[2:]...) {
				protocols.numbers[strings.ToLower(name)] = uint8(number)
			}
		}
	})
	return protocols
}

type serviceDatabase struct {
	names map[string]string
	ports map[string]uint16
}

var (
	servicesOnce sync.Once
	services     serviceDatabase
)

// serviceLookup reads /etc/services, keyed "port/proto" in one direction and by
// service name in the other.
func serviceLookup() serviceDatabase {
	servicesOnce.Do(func() {
		services = serviceDatabase{names: map[string]string{}, ports: map[string]uint16{}}
		data, err := os.ReadFile("/etc/services")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			if index := strings.IndexByte(line, '#'); index >= 0 {
				line = line[:index]
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			portText, protocol, ok := strings.Cut(fields[1], "/")
			port, convErr := strconv.ParseUint(portText, 10, 16)
			if !ok || convErr != nil {
				continue
			}
			key := portText + "/" + protocol
			if _, seen := services.names[key]; !seen {
				services.names[key] = fields[0]
			}
			for _, name := range append([]string{fields[0]}, fields[2:]...) {
				if _, seen := services.ports[name]; !seen {
					services.ports[name] = uint16(port)
				}
			}
		}
	})
	return services
}

func iptPortName(port uint16, proto uint8, numeric bool) string {
	if !numeric {
		key := strconv.Itoa(int(port)) + "/" + iptProtocolMatchName(proto)
		if name, ok := serviceLookup().names[key]; ok {
			return name
		}
	}
	return strconv.Itoa(int(port))
}

func iptablesServicePort(name string) (uint16, bool) {
	port, ok := serviceLookup().ports[name]
	return port, ok
}

var (
	hostsOnce sync.Once
	hostNames map[string]string
)

// iptHostText resolves an address through /etc/hosts only. The original also
// asks the DNS resolver; that path is deliberately absent here, both for the
// seccomp policy and because a listing should not block on the network.
func iptHostText(ip net.IP) string {
	hostsOnce.Do(func() {
		hostNames = map[string]string{}
		data, err := os.ReadFile(hostsFilePath)
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			if index := strings.IndexByte(line, '#'); index >= 0 {
				line = line[:index]
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if address := net.ParseIP(fields[0]); address != nil {
				if _, seen := hostNames[address.String()]; !seen {
					hostNames[address.String()] = fields[1]
				}
			}
		}
	})
	if name, ok := hostNames[ip.String()]; ok {
		return name
	}
	return ip.String()
}

// iptablesICMPTypes carries the names the original prints for an ICMP type and
// code range, in the order it searches them.
var iptablesICMPTypes = []struct {
	name    string
	icmp    byte
	codeMin byte
	codeMax byte
}{
	{"echo-reply", 0, 0, 255},
	{"destination-unreachable", 3, 0, 255},
	{"network-unreachable", 3, 0, 0},
	{"host-unreachable", 3, 1, 1},
	{"protocol-unreachable", 3, 2, 2},
	{"port-unreachable", 3, 3, 3},
	{"fragmentation-needed", 3, 4, 4},
	{"source-route-failed", 3, 5, 5},
	{"network-unknown", 3, 6, 6},
	{"host-unknown", 3, 7, 7},
	{"network-prohibited", 3, 9, 9},
	{"host-prohibited", 3, 10, 10},
	{"communication-prohibited", 3, 13, 13},
	{"source-quench", 4, 0, 255},
	{"redirect", 5, 0, 255},
	{"network-redirect", 5, 0, 0},
	{"host-redirect", 5, 1, 1},
	{"echo-request", 8, 0, 255},
	{"router-advertisement", 9, 0, 255},
	{"router-solicitation", 10, 0, 255},
	{"time-exceeded", 11, 0, 255},
	{"ttl-zero-during-transit", 11, 0, 0},
	{"ttl-zero-during-reassembly", 11, 1, 1},
	{"parameter-problem", 12, 0, 255},
	{"ip-header-bad", 12, 0, 0},
	{"required-option-missing", 12, 1, 1},
	{"timestamp-request", 13, 0, 255},
	{"timestamp-reply", 14, 0, 255},
	{"address-mask-request", 17, 0, 255},
	{"address-mask-reply", 18, 0, 255},
}

func iptablesICMPTypeName(icmpType, codeMin, codeMax byte) (string, bool) {
	for _, entry := range iptablesICMPTypes {
		if entry.icmp == icmpType && entry.codeMin == codeMin && entry.codeMax == codeMax {
			return entry.name, true
		}
	}
	return "", false
}

// iptablesICMPTypeNumber accepts a type name, a number, or the "type/code"
// form -S prints when a rule pins one ICMP code.
func iptablesICMPTypeNumber(value string) (icmpType byte, code *byte, ok bool) {
	typeText, codeText, hasCode := strings.Cut(value, "/")
	if hasCode {
		parsed, err := strconv.ParseUint(codeText, 10, 8)
		if err != nil {
			return 0, nil, false
		}
		number := byte(parsed)
		code = &number
	}
	if parsed, err := strconv.ParseUint(typeText, 10, 8); err == nil {
		return byte(parsed), code, true
	}
	for _, entry := range iptablesICMPTypes {
		if entry.name == strings.ToLower(typeText) {
			if code == nil && entry.codeMin == entry.codeMax {
				value := entry.codeMin
				code = &value
			}
			return entry.icmp, code, true
		}
	}
	return 0, nil, false
}

// iptablesRejectTypes maps the ipt_reject_info enum onto both the names the
// original accepts and the ICMP code nftables stores for the same rejection.
var iptablesRejectTypes = []struct {
	name string
	code int
	icmp byte
}{
	{"icmp-net-unreachable", 0, 0},
	{"icmp-host-unreachable", 1, 1},
	{"icmp-proto-unreachable", 2, 2},
	{"icmp-port-unreachable", rejectPortUnreachable, 3},
	{"echo-reply", 4, 3},
	{"icmp-net-prohibited", 5, 9},
	{"icmp-host-prohibited", 6, 10},
	{"tcp-reset", rejectTCPReset, 3},
	{"icmp-admin-prohibited", 8, 13},
}

const (
	rejectPortUnreachable = 3
	rejectTCPReset        = 7
)

func iptablesRejectCode(name string) (int, bool) {
	if name == "" {
		return rejectPortUnreachable, true
	}
	for _, entry := range iptablesRejectTypes {
		if entry.name == name {
			return entry.code, true
		}
	}
	return 0, false
}

func iptablesRejectName(code int) string {
	for _, entry := range iptablesRejectTypes {
		if entry.code == code {
			return entry.name
		}
	}
	return "icmp-port-unreachable"
}

func iptablesRejectNameForICMPCode(icmp byte) string {
	for _, entry := range iptablesRejectTypes {
		if entry.icmp == icmp && entry.code != rejectTCPReset && entry.name != "echo-reply" {
			return entry.name
		}
	}
	return "icmp-port-unreachable"
}

func rejectICMPCode(code int) byte {
	for _, entry := range iptablesRejectTypes {
		if entry.code == code {
			return entry.icmp
		}
	}
	return 3
}

// iptablesCtStateNames names the bits of a connection tracking state mask.
// nftables and the xt_conntrack extension agree on every bit but UNTRACKED.
func iptablesCtStateNames(mask uint32, xtables bool) string {
	untracked := uint32(1 << 6)
	if xtables {
		untracked = 1 << 8
	}
	var names []string
	if mask&0x01 != 0 {
		names = append(names, "INVALID")
	}
	if mask&0x08 != 0 {
		names = append(names, "NEW")
	}
	if mask&0x04 != 0 {
		names = append(names, "RELATED")
	}
	if mask&0x02 != 0 {
		names = append(names, "ESTABLISHED")
	}
	if mask&untracked != 0 {
		names = append(names, "UNTRACKED")
	}
	if xtables {
		if mask&(1<<6) != 0 {
			names = append(names, "SNAT")
		}
		if mask&(1<<7) != 0 {
			names = append(names, "DNAT")
		}
	}
	return strings.Join(names, ",")
}

// iptablesLimitText names the rate of the native nftables limiter, which keeps
// a plain rate and a period in seconds.
func iptablesLimitText(rate, unit uint64) string {
	for _, entry := range []struct {
		name    string
		seconds uint64
	}{{"day", 24 * 60 * 60}, {"hour", 60 * 60}, {"min", 60}, {"sec", 1}} {
		if unit == entry.seconds {
			return fmt.Sprintf("%d/%s", rate, entry.name)
		}
	}
	return fmt.Sprintf("%d/%dsec", rate, unit)
}

// iptablesLimitRate converts the scaled period xt_limit stores back into the
// largest unit that still divides evenly, the way the original prints it.
func iptablesLimitRate(period uint32) string {
	if period == 0 {
		return "0"
	}
	const scale = 10000
	rates := []struct {
		name string
		mult uint32
	}{{"day", scale * 24 * 60 * 60}, {"hour", scale * 60 * 60}, {"min", scale * 60}, {"sec", scale}}
	index := 0
	for i := 1; i < len(rates); i++ {
		if period > rates[i].mult || rates[i].mult/period < rates[i].mult%period {
			break
		}
		index = i
	}
	return fmt.Sprintf("%d/%s", rates[index].mult/period, rates[index].name)
}
