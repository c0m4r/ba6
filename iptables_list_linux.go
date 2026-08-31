// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Reading a ruleset back is the inverse of building one: the kernel returns nft
// expressions, and iptables syntax has to be recovered from them. Core matches
// (-s, -d, -p, -i, -o) arrive as payload and meta comparisons; everything an
// extension contributes (-m tcp, -m multiport, -m conntrack, -j REJECT) travels
// as an xtables blob inside a "match" or "target" expression.

const (
	protocolICMP = 1
	protocolTCP  = 6
	protocolUDP  = 17
)

type iptListOptions struct {
	verbose     bool
	exact       bool
	numeric     bool
	lineNumbers bool
}

type iptMatch struct {
	list string // as -L prints it, without the leading space
	save string // as -S prints it, e.g. "-m tcp --dport 22"
}

type iptRule struct {
	handle     uint64
	packets    uint64
	bytes      uint64
	proto      uint8
	protoInv   bool
	src        *net.IPNet
	srcInv     bool
	dst        *net.IPNet
	dstInv     bool
	inIface    string
	inInv      bool
	outIface   string
	outInv     bool
	fragment   bool
	fragInv    bool
	matches    []iptMatch
	target     string
	targetGoto bool
	targetList string // target detail as -L prints it
	targetSave string // target detail as -S prints it
}

type iptChain struct {
	name      string
	base      bool
	hook      uint32
	policy    string
	packets   uint64
	bytes     uint64
	refs      int
	synthetic bool // listed like the original does, but absent from the kernel
	rules     []*iptRule
}

type iptRuleset struct {
	table  string
	exists bool
	chains []*iptChain
}

func (r *iptRuleset) chain(name string) *iptChain {
	for _, chain := range r.chains {
		if chain.name == name {
			return chain
		}
	}
	return nil
}

// iptablesBuiltinChains lists the base chains each table always presents. The
// original shows them whether or not the kernel holds them yet, so an empty
// firewall still lists its chains and their policies.
var iptablesBuiltinChains = map[string][]struct {
	name string
	hook uint32
}{
	"filter":   {{"INPUT", nfHookLocalIn}, {"FORWARD", nfHookForward}, {"OUTPUT", nfHookLocalOut}},
	"nat":      {{"PREROUTING", nfHookPreRouting}, {"INPUT", nfHookLocalIn}, {"OUTPUT", nfHookLocalOut}, {"POSTROUTING", nfHookPostRouting}},
	"mangle":   {{"PREROUTING", nfHookPreRouting}, {"INPUT", nfHookLocalIn}, {"FORWARD", nfHookForward}, {"OUTPUT", nfHookLocalOut}, {"POSTROUTING", nfHookPostRouting}},
	"raw":      {{"PREROUTING", nfHookPreRouting}, {"OUTPUT", nfHookLocalOut}},
	"security": {{"INPUT", nfHookLocalIn}, {"FORWARD", nfHookForward}, {"OUTPUT", nfHookLocalOut}},
}

// readIptablesTable returns the chains of one table, base chains first in hook
// order and user chains after them by name, which is the order the original
// lists and saves them in.
func readIptablesTable(table string, rules, numeric bool) (*iptRuleset, error) {
	ruleset := &iptRuleset{table: table}
	messages, err := nftDump(nftMsgGetChain, nftGenMessage(nfprotoIPv4))
	if err != nil {
		return nil, err
	}
	for _, message := range messages {
		if chain, ok := decodeIptablesChain(message, table); ok {
			ruleset.chains = append(ruleset.chains, chain)
			ruleset.exists = true
		}
	}
	for _, builtin := range iptablesBuiltinChains[table] {
		if ruleset.chain(builtin.name) == nil {
			ruleset.chains = append(ruleset.chains, &iptChain{
				name: builtin.name, base: true, hook: builtin.hook,
				policy: "ACCEPT", synthetic: true,
			})
		}
	}
	sort.SliceStable(ruleset.chains, func(i, j int) bool {
		first, second := ruleset.chains[i], ruleset.chains[j]
		if first.base != second.base {
			return first.base
		}
		if first.base {
			return first.hook < second.hook
		}
		return first.name < second.name
	})
	if !rules {
		return ruleset, nil
	}
	return ruleset, readIptablesRules(ruleset, numeric)
}

func decodeIptablesChain(message syscall.NetlinkMessage, table string) (*iptChain, bool) {
	if len(message.Data) < 4 || message.Data[0] != nfprotoIPv4 {
		return nil, false
	}
	attrs, err := parseRawNetlinkAttributes(message.Data[4:])
	if err != nil {
		return nil, false
	}
	chain := &iptChain{}
	inTable := false
	for _, attr := range attrs {
		switch attr.typeID {
		case nftaChainTable:
			inTable = netlinkString(attr.value) == table
		case nftaChainName:
			chain.name = netlinkString(attr.value)
		case nftaChainHook:
			chain.base = true
			if hook, hookErr := parseRawNetlinkAttributes(attr.value); hookErr == nil {
				for _, entry := range hook {
					if entry.typeID == nftaHookNum && len(entry.value) >= 4 {
						chain.hook = binary.BigEndian.Uint32(entry.value)
					}
				}
			}
		case nftaChainPolicy:
			chain.policy = "ACCEPT"
			if len(attr.value) >= 4 && binary.BigEndian.Uint32(attr.value) == nfDrop {
				chain.policy = "DROP"
			}
		case nftaChainCounters:
			chain.packets, chain.bytes = decodeIptablesCounters(attr.value)
		}
	}
	if !inTable || chain.name == "" {
		return nil, false
	}
	if chain.base && chain.policy == "" {
		chain.policy = "ACCEPT"
	}
	return chain, true
}

func decodeIptablesCounters(data []byte) (packets, bytes uint64) {
	attrs, err := parseRawNetlinkAttributes(data)
	if err != nil {
		return 0, 0
	}
	for _, attr := range attrs {
		if len(attr.value) < 8 {
			continue
		}
		switch attr.typeID {
		case nftaCounterPackets:
			packets = binary.BigEndian.Uint64(attr.value)
		case nftaCounterBytes:
			bytes = binary.BigEndian.Uint64(attr.value)
		}
	}
	return packets, bytes
}

// readIptablesRules attaches every rule to its chain and counts how many rules
// jump to each user chain, which is the "(N references)" the original reports.
func readIptablesRules(ruleset *iptRuleset, numeric bool) error {
	messages, err := nftDump(nftMsgGetRule, nftGenMessage(nfprotoIPv4))
	if err != nil {
		return err
	}
	for _, message := range messages {
		if len(message.Data) < 4 || message.Data[0] != nfprotoIPv4 {
			continue
		}
		attrs, attrErr := parseRawNetlinkAttributes(message.Data[4:])
		if attrErr != nil {
			return attrErr
		}
		rule := &iptRule{}
		chainName, table := "", ""
		var expressions []byte
		for _, attr := range attrs {
			switch attr.typeID {
			case nftaRuleTable:
				table = netlinkString(attr.value)
			case nftaRuleChain:
				chainName = netlinkString(attr.value)
			case nftaRuleHandle:
				if len(attr.value) >= 8 {
					rule.handle = binary.BigEndian.Uint64(attr.value)
				}
			case nftaRuleExpressions:
				expressions = attr.value
			}
		}
		chain := ruleset.chain(chainName)
		if table != ruleset.table || chain == nil {
			continue
		}
		decodeIptablesExpressions(expressions, rule, numeric)
		chain.rules = append(chain.rules, rule)
	}
	for _, chain := range ruleset.chains {
		for _, rule := range chain.rules {
			if target := ruleset.chain(rule.target); target != nil && !target.base {
				target.refs++
			}
		}
	}
	return nil
}

// nftRegister remembers what a preceding expression loaded into a register, so
// the comparison that follows can be turned back into an iptables match.
type nftRegister struct {
	kind   int
	offset uint32
	length uint32
	key    uint32
	mask   []byte
}

const (
	regUnset = iota
	regPayloadNetwork
	regPayloadTransport
	regMeta
	regCt
)

type iptDecoder struct {
	rule      *iptRule
	regs      map[uint32]nftRegister
	numeric   bool
	portName  string
	sport     *iptPortRange
	sportInv  bool
	dport     *iptPortRange
	dportInv  bool
	portMatch int // where the implicit port match sits in rule.matches
}

func decodeIptablesExpressions(expressions []byte, rule *iptRule, numeric bool) {
	decoder := newIptDecoder(rule, numeric)
	elements, err := parseRawNetlinkAttributes(expressions)
	if err != nil {
		return
	}
	for _, element := range elements {
		if element.typeID != nftaListElem {
			continue
		}
		attrs, attrErr := parseRawNetlinkAttributes(element.value)
		if attrErr != nil {
			continue
		}
		name, data := "", []byte(nil)
		for _, attr := range attrs {
			switch attr.typeID {
			case nftaExprName:
				name = netlinkString(attr.value)
			case nftaExprData:
				data = attr.value
			}
		}
		decoder.expression(name, data)
	}
}

func newIptDecoder(rule *iptRule, numeric bool) *iptDecoder {
	return &iptDecoder{rule: rule, regs: map[uint32]nftRegister{}, numeric: numeric, portMatch: -1}
}

func (d *iptDecoder) expression(name string, data []byte) {
	attrs := nftAttributes(data)
	switch name {
	case "payload":
		kind := regUnset
		switch nftAttrU32(attrs, nftaPayloadBase) {
		case nftPayloadNetwork:
			kind = regPayloadNetwork
		case nftPayloadTransport:
			kind = regPayloadTransport
		}
		d.regs[nftAttrU32(attrs, nftaPayloadDreg)] = nftRegister{
			kind:   kind,
			offset: nftAttrU32(attrs, nftaPayloadOffset),
			length: nftAttrU32(attrs, nftaPayloadLen),
		}
	case "meta":
		if _, ok := attrs[nftaMetaDreg]; ok {
			d.regs[nftAttrU32(attrs, nftaMetaDreg)] = nftRegister{kind: regMeta, key: nftAttrU32(attrs, nftaMetaKey)}
		}
	case "ct":
		if _, ok := attrs[nftaCtDreg]; ok {
			d.regs[nftAttrU32(attrs, nftaCtDreg)] = nftRegister{kind: regCt, key: nftAttrU32(attrs, nftaCtKey)}
		}
	case "bitwise":
		register := d.regs[nftAttrU32(attrs, nftaBitwiseSreg)]
		register.mask = nftDataValue(attrs[nftaBitwiseMask])
		d.regs[nftAttrU32(attrs, nftaBitwiseDreg)] = register
	case "cmp":
		d.compare(d.regs[nftAttrU32(attrs, nftaCmpSreg)], nftAttrU32(attrs, nftaCmpOp), nftDataValue(attrs[nftaCmpData]))
	case "range":
		d.rangeCompare(attrs)
	case "limit":
		d.limitDetail(attrs)
	case "counter":
		d.rule.packets, d.rule.bytes = decodeIptablesCounters(data)
	case "immediate":
		if nftAttrU32(attrs, nftaImmediateDreg) == nftRegVerdict {
			d.verdict(attrs[nftaImmediateData])
		}
	case "match":
		d.xtMatch(nftAttrString(attrs, nftaMatchName), nftAttrU32(attrs, nftaMatchRev), attrs[nftaMatchInfo])
	case "target":
		d.xtTarget(nftAttrString(attrs, nftaTargetName), nftAttrU32(attrs, nftaTargetRev), attrs[nftaTargetInfo])
	case "reject":
		d.rule.target = "REJECT"
		d.rejectDetail(attrs)
	case "log":
		d.logDetail(attrs)
	case "masq":
		d.rule.target = "MASQUERADE"
	case "redir":
		d.rule.target = "REDIRECT"
	case "nat":
		d.rule.target = "SNAT"
		if nftAttrU32(attrs, nftaNatType) == 1 { // NFT_NAT_DNAT
			d.rule.target = "DNAT"
		}
	case "":
	default:
		// An expression with no iptables equivalent still has a name, and
		// showing it beats printing a rule that looks broader than it is.
		d.rule.matches = append(d.rule.matches, iptMatch{list: "/* " + name + " */", save: "-m " + name})
	}
}

func (d *iptDecoder) compare(register nftRegister, operator uint32, value []byte) {
	inverted := operator == nftCmpNeq
	switch register.kind {
	case regPayloadNetwork:
		d.comparePayloadNetwork(register, inverted, value)
	case regPayloadTransport:
		d.comparePayloadTransport(register, operator, inverted, value)
	case regMeta:
		d.compareMeta(register, inverted, value)
	case regCt:
		if register.key == nftCtState && len(register.mask) >= 4 {
			states := binary.NativeEndian.Uint32(register.mask)
			d.addCtState(iptablesCtStateNames(states, false), operator == nftCmpEq)
		}
	}
}

func (d *iptDecoder) comparePayloadNetwork(register nftRegister, inverted bool, value []byte) {
	address := register.offset == 12 || register.offset == 16
	switch {
	case register.offset == 9 && register.length == 1 && len(value) >= 1:
		d.rule.proto, d.rule.protoInv = value[0], inverted
	case address && register.length >= 1 && register.length <= 4 && len(value) >= int(register.length):
		network := iptNetwork(value, register.mask, int(register.length))
		if register.offset == 12 {
			d.rule.src, d.rule.srcInv = network, inverted
		} else {
			d.rule.dst, d.rule.dstInv = network, inverted
		}
	case register.offset == 6 && register.length == 2:
		d.rule.fragment, d.rule.fragInv = true, !inverted
	}
}

func (d *iptDecoder) comparePayloadTransport(register nftRegister, operator uint32, inverted bool, value []byte) {
	if register.offset == 0 && register.length == 1 && d.rule.proto == protocolICMP && len(value) >= 1 {
		d.rule.matches = append(d.rule.matches, iptICMPMatch(value[0], 0, 255, inverted, d.numeric))
		return
	}
	if register.length != 2 || len(value) < 2 {
		return
	}
	source := register.offset == 0
	if !source && register.offset != 2 {
		return
	}
	ports := d.portRange(source)
	port := binary.BigEndian.Uint16(value)
	// A port span is two comparisons against the same field, or the range
	// expression below; a single port is one equality test.
	switch operator {
	case nftCmpGte, nftCmpGt:
		ports.min = port
	case nftCmpLte, nftCmpLt:
		ports.max = port
	default:
		ports.min, ports.max = port, port
		d.invertPort(source, inverted)
	}
	d.refreshPortMatch()
}

func (d *iptDecoder) rangeCompare(attrs map[uint16][]byte) {
	register := d.regs[nftAttrU32(attrs, nftaRangeSreg)]
	from, to := nftDataValue(attrs[nftaRangeFromData]), nftDataValue(attrs[nftaRangeToData])
	if register.kind != regPayloadTransport || register.length != 2 || len(from) < 2 || len(to) < 2 {
		return
	}
	source := register.offset == 0
	if !source && register.offset != 2 {
		return
	}
	ports := d.portRange(source)
	ports.min, ports.max = binary.BigEndian.Uint16(from), binary.BigEndian.Uint16(to)
	d.invertPort(source, nftAttrU32(attrs, nftaRangeOp) == 1) // NFT_RANGE_NEQ
	d.refreshPortMatch()
}

func (d *iptDecoder) portRange(source bool) *iptPortRange {
	target := &d.dport
	if source {
		target = &d.sport
	}
	if *target == nil {
		*target = &iptPortRange{min: 0, max: 65535}
	}
	return *target
}

func (d *iptDecoder) invertPort(source, inverted bool) {
	if source {
		d.sportInv = inverted
		return
	}
	d.dportInv = inverted
}

// refreshPortMatch keeps both ports in one match, in the position the first of
// them appeared, which is where the original prints the implicit tcp/udp match.
func (d *iptDecoder) refreshPortMatch() {
	name := d.portName
	if name == "" {
		name = iptProtocolMatchName(d.rule.proto)
	}
	match := iptPortsMatch(name, d.rule.proto, d.sport, d.dport, d.sportInv, d.dportInv, d.numeric)
	if d.portMatch < 0 {
		d.portMatch = len(d.rule.matches)
		d.rule.matches = append(d.rule.matches, match)
		return
	}
	d.rule.matches[d.portMatch] = match
}

func (d *iptDecoder) compareMeta(register nftRegister, inverted bool, value []byte) {
	switch register.key {
	case nftMetaIIFName, nftMetaOIFName:
		d.setInterface(register.key == nftMetaIIFName, iptInterfaceName(value), inverted)
	case nftMetaIIF, nftMetaOIF:
		if len(value) < 4 {
			return
		}
		name := ""
		if link, err := net.InterfaceByIndex(int(binary.NativeEndian.Uint32(value))); err == nil { //nolint:gosec // G115: an interface index is a small positive integer.
			name = link.Name
		}
		d.setInterface(register.key == nftMetaIIF, name, inverted)
	case nftMetaL4Proto:
		if len(value) >= 1 {
			d.rule.proto, d.rule.protoInv = value[0], inverted
		}
	case nftMetaNFProto, nftMetaProtocol:
		// The address family selector iptables-nft adds; it carries no match.
	}
}

func (d *iptDecoder) setInterface(incoming bool, name string, inverted bool) {
	if incoming {
		d.rule.inIface, d.rule.inInv = name, inverted
		return
	}
	d.rule.outIface, d.rule.outInv = name, inverted
}

func (d *iptDecoder) verdict(data []byte) {
	verdict := nftAttributes(nftNestedValue(data, nftaDataVerdict))
	code := int32(nftAttrU32(verdict, nftaVerdictCode)) //nolint:gosec // G115: nft verdicts are signed int32 constants.
	switch code {
	case nfAccept:
		d.rule.target = "ACCEPT"
	case nfDrop:
		d.rule.target = "DROP"
	case nfQueue:
		d.rule.target = "QUEUE"
	case nftReturn:
		d.rule.target = "RETURN"
	case nftContinue:
	case nftGoto:
		d.rule.target, d.rule.targetGoto = nftAttrString(verdict, nftaVerdictChain), true
	default:
		d.rule.target = nftAttrString(verdict, nftaVerdictChain)
	}
}

func (d *iptDecoder) rejectDetail(attrs map[uint16][]byte) {
	name := "icmp-port-unreachable"
	if nftAttrU32(attrs, nftaRejectType) == 1 { // NFT_REJECT_TCP_RST
		name = "tcp-reset"
	} else if code, ok := attrs[nftaRejectICMPCode]; ok && len(code) >= 1 {
		name = iptablesRejectNameForICMPCode(code[0])
	}
	d.rule.targetList = "reject-with " + name
	d.rule.targetSave = "--reject-with " + name
}

// limitDetail decodes the native rate limiter iptables now emits for -m limit.
func (d *iptDecoder) limitDetail(attrs map[uint16][]byte) {
	rate := iptablesLimitText(nftAttrU64(attrs, nftaLimitRate), nftAttrU64(attrs, nftaLimitUnit))
	d.rule.matches = append(d.rule.matches, iptLimitMatch(rate, nftAttrU32(attrs, nftaLimitBurst)))
}

func (d *iptDecoder) logDetail(attrs map[uint16][]byte) {
	d.rule.target = "LOG"
	level := byte(nftAttrU32(attrs, nftaLogLevel)) //nolint:gosec // G115: a syslog level is 0 to 7.
	d.rule.targetList, d.rule.targetSave = iptLogText(level, 0, nftAttrString(attrs, nftaLogPrefix), d.numeric)
}

// xtMatch decodes the xtables extensions that appear in ordinary rulesets. The
// blobs are plain C structs in host byte order.
func (d *iptDecoder) xtMatch(name string, revision uint32, info []byte) {
	switch name {
	case "tcp", "udp", "udplite", "sctp", "dccp":
		if d.xtPortMatch(name, info) {
			return
		}
	case "multiport":
		if match, ok := iptMultiportMatch(revision, info, d.rule.proto, d.numeric); ok {
			d.rule.matches = append(d.rule.matches, match)
			return
		}
	case "conntrack":
		// xt_conntrack_mtinfo2 and later keep match_flags, invert_flags and
		// state_mask at the end of a fixed 150-byte address and port block.
		if len(info) >= 152 {
			matchFlags := binary.NativeEndian.Uint16(info[146:148])
			invertFlags := binary.NativeEndian.Uint16(info[148:150])
			if matchFlags&xtConntrackState != 0 {
				states := uint32(binary.NativeEndian.Uint16(info[150:152]))
				if revision < 2 {
					states = uint32(info[150])
				}
				d.addCtState(iptablesCtStateNames(states, true), invertFlags&xtConntrackState != 0)
				return
			}
		}
	case "state":
		if len(info) >= 4 {
			d.addCtState(iptablesCtStateNames(binary.NativeEndian.Uint32(info[0:4]), true), false)
			return
		}
	case "icmp":
		if len(info) >= 4 {
			d.rule.matches = append(d.rule.matches, iptICMPMatch(info[0], info[1], info[2], info[3]&0x01 != 0, d.numeric))
			return
		}
	case "comment":
		if text := netlinkString(info); text != "" {
			d.rule.matches = append(d.rule.matches, iptMatch{
				list: "/* " + text + " */",
				save: fmt.Sprintf("-m comment --comment %q", text),
			})
			return
		}
	case "limit":
		if len(info) >= 8 {
			rate := iptablesLimitRate(binary.NativeEndian.Uint32(info[0:4]))
			d.rule.matches = append(d.rule.matches, iptLimitMatch(rate, binary.NativeEndian.Uint32(info[4:8])))
			return
		}
	}
	d.rule.matches = append(d.rule.matches, iptMatch{list: name, save: "-m " + name})
}

func (d *iptDecoder) xtTarget(name string, revision uint32, info []byte) {
	d.rule.target = name
	switch name {
	case "REJECT":
		if len(info) >= 4 {
			rejectName := iptablesRejectName(int(binary.NativeEndian.Uint32(info[0:4]))) //nolint:gosec // G115: the reject enum is a small constant.
			d.rule.targetList = "reject-with " + rejectName
			d.rule.targetSave = "--reject-with " + rejectName
		}
	case "LOG":
		// struct ipt_log_info: level, logflags, then a 30 byte prefix.
		if len(info) >= 32 {
			d.rule.targetList, d.rule.targetSave = iptLogText(info[0], info[1], netlinkString(info[2:32]), d.numeric)
		}
	case "SNAT", "DNAT":
		address, ports, ok := iptNatRange(info, revision)
		if !ok || address == "" {
			return
		}
		option := "--to-source"
		if name == "DNAT" {
			option = "--to-destination"
		}
		if ports != "" {
			address += ":" + ports
		}
		d.rule.targetList = "to:" + address
		d.rule.targetSave = option + " " + address
	case "MASQUERADE", "REDIRECT", "NETMAP":
		address, ports, ok := iptNatRange(info, revision)
		if !ok {
			return
		}
		if ports != "" {
			label := "masq ports: "
			if name != "MASQUERADE" {
				label = "redir ports "
			}
			d.rule.targetList = label + ports
			d.rule.targetSave = "--to-ports " + ports
		}
		if name == "NETMAP" && address != "" {
			d.rule.targetList = "to:" + address
			d.rule.targetSave = "--to " + address
		}
	case "MARK", "CONNMARK":
		// struct xt_mark_tginfo2 { u32 mark; u32 mask; }
		if len(info) >= 8 {
			mark := binary.NativeEndian.Uint32(info[0:4])
			mask := binary.NativeEndian.Uint32(info[4:8])
			d.rule.targetList = name + " " + iptMarkOperation(mark, mask)
			d.rule.targetSave = fmt.Sprintf("--set-xmark 0x%x/0x%x", mark, mask)
		}
	}
}

// iptNatRange decodes the address and port range a NAT target carries. Revision
// 2 and later hold a struct nf_nat_range2; earlier ones wrap a single
// nf_nat_ipv4_range in a count.
func iptNatRange(info []byte, revision uint32) (address, ports string, ok bool) {
	var flags uint32
	var minAddress, maxAddress []byte
	var minPort, maxPort uint16
	switch {
	case revision >= 2 && len(info) >= 40:
		flags = binary.NativeEndian.Uint32(info[0:4])
		minAddress, maxAddress = info[4:8], info[20:24]
		minPort, maxPort = binary.BigEndian.Uint16(info[36:38]), binary.BigEndian.Uint16(info[38:40])
	case len(info) >= 20:
		flags = binary.NativeEndian.Uint32(info[4:8])
		minAddress, maxAddress = info[8:12], info[12:16]
		minPort, maxPort = binary.BigEndian.Uint16(info[16:18]), binary.BigEndian.Uint16(info[18:20])
	default:
		return "", "", false
	}
	if flags&0x01 != 0 { // NF_NAT_RANGE_MAP_IPS
		address = net.IP(minAddress).String()
		if string(minAddress) != string(maxAddress) {
			address += "-" + net.IP(maxAddress).String()
		}
	}
	if flags&0x02 != 0 { // NF_NAT_RANGE_PROTO_SPECIFIED
		ports = strconv.Itoa(int(minPort))
		if maxPort != minPort {
			ports += "-" + strconv.Itoa(int(maxPort))
		}
	}
	return address, ports, true
}

// iptMarkOperation names what a mark target does to the packet mark, which the
// original derives from the relationship between the value and its mask.
func iptMarkOperation(mark, mask uint32) string {
	switch {
	case mark == 0:
		return fmt.Sprintf("and 0x%x", ^mask)
	case mark == mask:
		return fmt.Sprintf("or 0x%x", mark)
	case mask == 0:
		return fmt.Sprintf("xor 0x%x", mark)
	case mask == 0xffffffff:
		return fmt.Sprintf("set 0x%x", mark)
	}
	return fmt.Sprintf("xset 0x%x/0x%x", mark, mask)
}

func (d *iptDecoder) addCtState(states string, inverted bool) {
	if states == "" {
		return
	}
	list, save := "ctstate "+states, "-m conntrack --ctstate "+states
	if inverted {
		list, save = "!ctstate "+states, "-m conntrack ! --ctstate "+states
	}
	d.rule.matches = append(d.rule.matches, iptMatch{list: list, save: save})
}

// xtPortMatch reads the source and destination port ranges shared by the tcp,
// udp, udplite, sctp and dccp matches: two host-order ranges at the front of
// the blob, with tcp keeping its inversion flags one byte further along.
func (d *iptDecoder) xtPortMatch(name string, info []byte) bool {
	if len(info) < 9 {
		return false
	}
	invertOffset := 8
	if name == "tcp" {
		invertOffset = 11
	}
	invert := byte(0)
	if invertOffset < len(info) {
		invert = info[invertOffset]
	}
	d.portName = name
	source := d.portRange(true)
	source.min, source.max = binary.NativeEndian.Uint16(info[0:2]), binary.NativeEndian.Uint16(info[2:4])
	destination := d.portRange(false)
	destination.min, destination.max = binary.NativeEndian.Uint16(info[4:6]), binary.NativeEndian.Uint16(info[6:8])
	d.sportInv, d.dportInv = invert&0x01 != 0, invert&0x02 != 0
	d.refreshPortMatch()
	return true
}

// iptPortsMatch renders the implicit tcp/udp match. A full range that is not
// inverted means the option was never given, so it stays out of the output.
func iptPortsMatch(name string, proto uint8, source, destination *iptPortRange, sourceInv, destinationInv, numeric bool) iptMatch {
	match := iptMatch{list: name, save: "-m " + name}
	if source != nil && (source.min != 0 || source.max != 65535 || sourceInv) {
		match.list += " " + iptPortText("spt", *source, sourceInv, proto, numeric)
		match.save += " " + iptPortSaveText("--sport", *source, sourceInv)
	}
	if destination != nil && (destination.min != 0 || destination.max != 65535 || destinationInv) {
		match.list += " " + iptPortText("dpt", *destination, destinationInv, proto, numeric)
		match.save += " " + iptPortSaveText("--dport", *destination, destinationInv)
	}
	return match
}

func iptPortText(prefix string, ports iptPortRange, inverted bool, proto uint8, numeric bool) string {
	inversion := ""
	if inverted {
		inversion = "!"
	}
	if ports.min == ports.max {
		return prefix + ":" + inversion + iptPortName(ports.min, proto, numeric)
	}
	return prefix + "s:" + inversion + iptPortName(ports.min, proto, numeric) + ":" + iptPortName(ports.max, proto, numeric)
}

func iptPortSaveText(option string, ports iptPortRange, inverted bool) string {
	if inverted {
		return "! " + option + " " + ports.String()
	}
	return option + " " + ports.String()
}

func iptMultiportMatch(revision uint32, info []byte, proto uint8, numeric bool) (iptMatch, bool) {
	// struct xt_multiport_v1 { u8 flags; u8 count; u16 ports[15];
	//                          u8 pflags[15]; u8 invert; }
	if len(info) < 32 {
		return iptMatch{}, false
	}
	count := int(info[1])
	if count > 15 {
		return iptMatch{}, false
	}
	listName, saveOption := "ports", "--ports"
	switch info[0] {
	case 0:
		listName, saveOption = "sports", "--sports"
	case 1:
		listName, saveOption = "dports", "--dports"
	}
	hasRanges := revision >= 1 && len(info) >= 48
	var ports []string
	for i := 0; i < count; i++ {
		text := iptPortName(binary.NativeEndian.Uint16(info[2+i*2:4+i*2]), proto, numeric)
		// A set range flag means this entry opens a range the next one closes.
		if hasRanges && info[32+i] != 0 && i+1 < count {
			i++
			text += ":" + iptPortName(binary.NativeEndian.Uint16(info[2+i*2:4+i*2]), proto, numeric)
		}
		ports = append(ports, text)
	}
	joined := strings.Join(ports, ",")
	if hasRanges && info[47] != 0 {
		return iptMatch{
			list: "multiport " + listName + " !" + joined,
			save: "-m multiport ! " + saveOption + " " + joined,
		}, true
	}
	return iptMatch{
		list: "multiport " + listName + " " + joined,
		save: "-m multiport " + saveOption + " " + joined,
	}, true
}

func iptICMPMatch(icmpType, codeMin, codeMax byte, inverted, numeric bool) iptMatch {
	inversion := ""
	if inverted {
		inversion = "! "
	}
	list := ""
	if name, ok := iptablesICMPTypeName(icmpType, codeMin, codeMax); ok && !numeric {
		list = inversion + "icmp " + name
	} else {
		list = fmt.Sprintf("%sicmptype %d", inversion, icmpType)
		switch {
		case codeMin == codeMax:
			list += fmt.Sprintf(" code %d", codeMin)
		case codeMin != 0 || codeMax != 255:
			list += fmt.Sprintf(" codes %d-%d", codeMin, codeMax)
		}
	}
	option := fmt.Sprintf("--icmp-type %d", icmpType)
	if codeMin == codeMax {
		option += fmt.Sprintf("/%d", codeMin)
	}
	if inverted {
		return iptMatch{list: list, save: "-m icmp ! " + option}
	}
	return iptMatch{list: list, save: "-m icmp " + option}
}

// iptLogFlags names the extra fields the LOG target can add to a message, in
// the order the original prints them.
var iptLogFlags = []struct {
	bit  byte
	name string
	save string
}{
	{0x01, "tcp-sequence", "--log-tcp-sequence"},
	{0x02, "tcp-options", "--log-tcp-options"},
	{0x04, "ip-options", "--log-ip-options"},
	{0x08, "uid", "--log-uid"},
	{0x20, "macdecode", "--log-macdecode"},
}

// iptLogSyslogLevels names a syslog level; the original prints the number only
// when -n asks it to.
var iptLogSyslogLevels = [8]string{"emerg", "alert", "crit", "error", "warn", "notice", "info", "debug"}

func iptLogText(level, flags byte, prefix string, numeric bool) (list, save string) {
	if numeric {
		list = fmt.Sprintf("LOG flags %d level %d", flags, level)
	} else if int(level) < len(iptLogSyslogLevels) {
		list = "LOG level " + iptLogSyslogLevels[level]
	} else {
		list = fmt.Sprintf("LOG UNKNOWN level %d", level)
	}
	if !numeric {
		for _, flag := range iptLogFlags {
			if flags&flag.bit != 0 {
				list += " " + flag.name
			}
		}
	}
	if prefix != "" {
		list += fmt.Sprintf(" prefix %q", prefix)
		save = fmt.Sprintf("--log-prefix %q", prefix)
	}
	// The original leaves the level out of a saved rule while it is the
	// default of warning.
	if level != 4 {
		save = strings.TrimPrefix(save+fmt.Sprintf(" --log-level %d", level), " ")
	}
	for _, flag := range iptLogFlags {
		if flags&flag.bit != 0 {
			save = strings.TrimPrefix(save+" "+flag.save, " ")
		}
	}
	return list, save
}

// iptLimitMatch renders a rate limiter. The original leaves the burst out of
// saved rules while it holds the default of five.
func iptLimitMatch(rate string, burst uint32) iptMatch {
	match := iptMatch{
		list: fmt.Sprintf("limit: avg %s burst %d", rate, burst),
		save: "-m limit --limit " + rate,
	}
	if burst != 5 {
		match.save += fmt.Sprintf(" --limit-burst %d", burst)
	}
	return match
}

// iptInterfaceName recovers an interface match. iptables compares the whole
// name including its NUL for an exact match, and only the fixed prefix for the
// "eth+" wildcard form.
func iptInterfaceName(value []byte) string {
	if len(value) > 0 && value[len(value)-1] == 0 {
		return netlinkString(value)
	}
	return string(value) + "+"
}

// iptNetwork rebuilds an address match. iptables compares only the leading
// bytes a prefix covers, so a shorter comparison stands for a shorter prefix,
// and a bitwise mask refines the last byte of it.
func iptNetwork(address, mask []byte, length int) *net.IPNet {
	network := &net.IPNet{IP: make(net.IP, 4), Mask: make(net.IPMask, 4)}
	copy(network.IP, address[:length])
	for i := 0; i < length; i++ {
		network.Mask[i] = 0xff
		if i < len(mask) {
			network.Mask[i] = mask[i]
		}
	}
	network.IP = network.IP.Mask(network.Mask)
	return network
}

func iptProtocolMatchName(proto uint8) string {
	switch proto {
	case protocolUDP:
		return "udp"
	case 33:
		return "dccp"
	case 132:
		return "sctp"
	case 136:
		return "udplite"
	}
	return "tcp"
}

// nftAttributes flattens a netlink attribute blob; nft attribute types are
// unique within one message, so a map keeps the decoders readable.
func nftAttributes(data []byte) map[uint16][]byte {
	result := map[uint16][]byte{}
	attrs, err := parseRawNetlinkAttributes(data)
	if err != nil {
		return result
	}
	for _, attr := range attrs {
		result[attr.typeID] = attr.value
	}
	return result
}

func nftAttrU32(attrs map[uint16][]byte, key uint16) uint32 {
	if value, ok := attrs[key]; ok && len(value) >= 4 {
		return binary.BigEndian.Uint32(value)
	}
	return 0
}

func nftAttrU64(attrs map[uint16][]byte, key uint16) uint64 {
	if value, ok := attrs[key]; ok && len(value) >= 8 {
		return binary.BigEndian.Uint64(value)
	}
	return 0
}

func nftAttrString(attrs map[uint16][]byte, key uint16) string {
	return netlinkString(attrs[key])
}

// nftDataValue unwraps the NFTA_DATA_VALUE payload nft wraps constants in.
func nftDataValue(data []byte) []byte { return nftNestedValue(data, nftaDataValue) }

func nftNestedValue(data []byte, key uint16) []byte {
	if data == nil {
		return nil
	}
	attrs, err := parseRawNetlinkAttributes(data)
	if err != nil {
		return nil
	}
	for _, attr := range attrs {
		if attr.typeID == key {
			return attr.value
		}
	}
	return nil
}
