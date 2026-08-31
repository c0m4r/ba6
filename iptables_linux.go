// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"
)

// iptables(8) on current distributions is iptables-nft: the filter/nat/mangle
// tables, their chains and every rule live in nftables, and an iptables rule is
// stored as a list of nft expressions. This applet speaks the same netlink
// protocol against the same tables, so it lists what the system tool lists and
// what it appends stays visible to the system tool.
const (
	netlinkNetfilter = 12
	nfprotoIPv4      = 2
	nfnlSubsysNFT    = 10
	nfnlBatchBegin   = 0x10
	nfnlBatchEnd     = 0x11

	nftMsgNewTable = 0
	nftMsgNewChain = 3
	nftMsgGetChain = 4
	nftMsgNewRule  = 6
	nftMsgGetRule  = 7
	nftMsgDelRule  = 8

	nftaTableName = 1

	nftaChainTable    = 1
	nftaChainName     = 3
	nftaChainHook     = 4
	nftaChainPolicy   = 5
	nftaChainType     = 7
	nftaChainCounters = 8

	nftaHookNum      = 1
	nftaHookPriority = 2

	nftaRuleTable       = 1
	nftaRuleChain       = 2
	nftaRuleHandle      = 3
	nftaRuleExpressions = 4
	nftaRuleCompat      = 5

	nftaRuleCompatProto = 1
	nftaRuleCompatFlags = 2

	nftaListElem = 1
	nftaExprName = 1
	nftaExprData = 2

	nftaDataValue   = 1
	nftaDataVerdict = 2

	nftaVerdictCode  = 1
	nftaVerdictChain = 2

	nftaImmediateDreg = 1
	nftaImmediateData = 2

	nftaPayloadDreg   = 1
	nftaPayloadBase   = 2
	nftaPayloadOffset = 3
	nftaPayloadLen    = 4

	nftaCmpSreg = 1
	nftaCmpOp   = 2
	nftaCmpData = 3

	nftaRangeSreg     = 1
	nftaRangeOp       = 2
	nftaRangeFromData = 3
	nftaRangeToData   = 4

	nftaLimitRate  = 1
	nftaLimitUnit  = 2
	nftaLimitBurst = 3

	nftaBitwiseSreg = 1
	nftaBitwiseDreg = 2
	nftaBitwiseLen  = 3
	nftaBitwiseMask = 4
	nftaBitwiseXor  = 5

	nftaMetaDreg = 1
	nftaMetaKey  = 2

	nftaCtDreg = 1
	nftaCtKey  = 2

	nftaCounterBytes   = 1
	nftaCounterPackets = 2

	nftaMatchName = 1
	nftaMatchRev  = 2
	nftaMatchInfo = 3

	nftaTargetName = 1
	nftaTargetRev  = 2
	nftaTargetInfo = 3

	nftaRejectType     = 1
	nftaRejectICMPCode = 2

	nftaLogPrefix = 2
	nftaLogLevel  = 5

	nftaNatType = 1

	nftRegVerdict = 0
	nftReg32_00   = 8

	nftPayloadNetwork   = 1
	nftPayloadTransport = 2

	nftCmpEq  = 0
	nftCmpNeq = 1
	nftCmpLt  = 2
	nftCmpLte = 3
	nftCmpGt  = 4
	nftCmpGte = 5

	nftMetaProtocol = 1
	nftMetaIIF      = 4
	nftMetaOIF      = 5
	nftMetaIIFName  = 6
	nftMetaOIFName  = 7
	nftMetaNFProto  = 15
	nftMetaL4Proto  = 16

	nftCtState = 0

	nfDrop      = 0
	nfAccept    = 1
	nfQueue     = 3
	nftContinue = -1
	nftJump     = -3
	nftGoto     = -4
	nftReturn   = -5

	nfHookPreRouting  = 0
	nfHookLocalIn     = 1
	nfHookForward     = 2
	nfHookLocalOut    = 3
	nfHookPostRouting = 4

	// XT_ALIGN() rounds an extension's data block up to 8 bytes; the kernel
	// checks the attribute length against exactly that.
	xtAlign = 8
)

// iptPortRange is an inclusive port range, the shape both xt_tcp/xt_udp and
// multiport store their ports in.
type iptPortRange struct {
	min uint16
	max uint16
}

func (r iptPortRange) String() string {
	if r.min == r.max {
		return strconv.Itoa(int(r.min))
	}
	return fmt.Sprintf("%d:%d", r.min, r.max)
}

// iptMatchSpec holds the matches and target of a rule given on the command
// line. Rules read back from the kernel become an iptRule instead; the two are
// compared through their -S rendering, which is how -D finds its victim.
type iptMatchSpec struct {
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
	sport      *iptPortRange
	sportInv   bool
	dport      *iptPortRange
	dportInv   bool
	icmpType   *uint8
	icmpCode   *byte
	icmpInv    bool
	target     string
	targetGoto bool
	rejectWith string
	given      bool
}

type iptablesSpec struct {
	command    byte
	table      string
	chain      string
	policy     string
	deleteLine int
	list       iptListOptions
	match      iptMatchSpec
}

func cmdIptables(args []string) int {
	spec, err := parseIptables(args)
	if err != nil {
		fatalf("iptables", "%v", err)
		return 2
	}
	switch spec.command {
	case 'L':
		err = listIptables(spec)
	case 'S':
		err = saveIptables(spec)
	case 'A':
		err = appendIptablesRule(spec)
	case 'D':
		err = deleteIptablesRule(spec)
	case 'F':
		err = flushIptablesRules(spec)
	case 'P':
		err = setIptablesPolicy(spec)
	}
	if err != nil {
		fatalf("iptables", "%s", iptablesErrorText(err))
		return 1
	}
	return 0
}

// These carry the original's wording, capital letter and full stop included,
// so a script that keys on iptables diagnostics reads the same text here.
//
//nolint:staticcheck // ST1005: matching the original is the point.
var (
	errIptablesNoSuchRule      = errors.New("Bad rule (does a matching rule exist in that chain?).")
	errIptablesDeletionIndex   = errors.New("Index of deletion too big.")
	errIptablesBadBuiltinChain = errors.New("Bad built-in chain name.")
)

// iptablesErrorText maps the errno the kernel returns onto the wording the
// original prints, so scripts that grep for it keep working.
func iptablesErrorText(err error) string {
	switch {
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		return "Permission denied (you must be root)."
	case errors.Is(err, syscall.ENOENT):
		return "No chain/target/match by that name."
	case errors.Is(err, syscall.EEXIST):
		return "Chain already exists."
	}
	return err.Error()
}

// expandIptablesOptions splits getopt-style short option clusters, so the
// habitual "iptables -nvL" reaches the parser as "-n -v -L". A cluster letter
// that takes an argument swallows the rest of the cluster, exactly as getopt
// does with optstring entries marked ":".
func expandIptablesOptions(args []string) []string {
	const withArgument = "tpsdiogmcwW"
	expanded := make([]string, 0, len(args))
	for _, arg := range args {
		if len(arg) < 3 || arg[0] != '-' || arg[1] == '-' {
			expanded = append(expanded, arg)
			continue
		}
		if _, err := strconv.Atoi(arg); err == nil {
			expanded = append(expanded, arg)
			continue
		}
		for i := 1; i < len(arg); i++ {
			expanded = append(expanded, "-"+string(arg[i]))
			if strings.IndexByte(withArgument, arg[i]) >= 0 {
				if i+1 < len(arg) {
					expanded = append(expanded, arg[i+1:])
				}
				break
			}
		}
	}
	return expanded
}

func parseIptables(args []string) (iptablesSpec, error) {
	spec := iptablesSpec{table: "filter"}
	args = expandIptablesOptions(args)
	negated := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func(option string) (string, error) {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s requires an argument", option)
			}
			return args[i], nil
		}
		// A command may be followed by an optional chain name; "-L", "-S" and
		// "-F" list or clear the whole table without one.
		optionalChain := func() {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && args[i+1] != "!" {
				i++
				spec.chain = args[i]
			}
		}
		if arg == "!" {
			negated = true
			continue
		}
		switch arg {
		case "-L", "--list", "-S", "--list-rules", "-F", "--flush":
			if err := setIptablesCommand(&spec, map[string]byte{"-L": 'L', "--list": 'L', "-S": 'S', "--list-rules": 'S', "-F": 'F', "--flush": 'F'}[arg]); err != nil {
				return spec, err
			}
			optionalChain()
		case "-A", "--append", "-D", "--delete", "-P", "--policy":
			if err := setIptablesCommand(&spec, map[string]byte{"-A": 'A', "--append": 'A', "-D": 'D', "--delete": 'D', "-P": 'P', "--policy": 'P'}[arg]); err != nil {
				return spec, err
			}
			chain, err := next(arg)
			if err != nil {
				return spec, err
			}
			spec.chain = chain
			if spec.command == 'P' {
				policy, err := next(arg)
				if err != nil {
					return spec, err
				}
				spec.policy = strings.ToUpper(policy)
				if spec.policy != "ACCEPT" && spec.policy != "DROP" {
					return spec, fmt.Errorf("policy must be ACCEPT or DROP")
				}
			}
			if spec.command == 'D' && i+1 < len(args) {
				if line, convErr := strconv.Atoi(args[i+1]); convErr == nil && line > 0 {
					spec.deleteLine = line
					i++
				}
			}
		case "-t", "--table":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			switch value {
			case "filter", "nat", "mangle", "raw", "security":
				spec.table = value
			default:
				return spec, fmt.Errorf("unknown table %q", value)
			}
		case "-p", "--protocol":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			protocol, ok := iptablesProtocolNumber(value)
			if !ok {
				return spec, fmt.Errorf("unsupported protocol %q", value)
			}
			spec.match.proto, spec.match.protoInv, spec.match.given = protocol, negated, true
		case "-s", "--source", "--src", "-d", "--destination", "--dst":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			network, err := parseIptablesNetwork(value)
			if err != nil {
				return spec, err
			}
			if arg == "-d" || arg == "--destination" || arg == "--dst" {
				spec.match.dst, spec.match.dstInv = network, negated
			} else {
				spec.match.src, spec.match.srcInv = network, negated
			}
			spec.match.given = true
		case "-i", "--in-interface", "-o", "--out-interface":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			if err := validateIptablesInterface(value); err != nil {
				return spec, err
			}
			if arg == "-i" || arg == "--in-interface" {
				spec.match.inIface, spec.match.inInv = value, negated
			} else {
				spec.match.outIface, spec.match.outInv = value, negated
			}
			spec.match.given = true
		case "--sport", "--source-port", "--dport", "--destination-port":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			ports, err := parseIptablesPortRange(value)
			if err != nil {
				return spec, err
			}
			if arg == "--sport" || arg == "--source-port" {
				spec.match.sport, spec.match.sportInv = &ports, negated
			} else {
				spec.match.dport, spec.match.dportInv = &ports, negated
			}
			spec.match.given = true
		case "--icmp-type":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			icmpType, icmpCode, ok := iptablesICMPTypeNumber(value)
			if !ok {
				return spec, fmt.Errorf("unsupported ICMP type %q", value)
			}
			spec.match.icmpType, spec.match.icmpCode = &icmpType, icmpCode
			spec.match.icmpInv, spec.match.given = negated, true
		case "-f", "--fragment":
			spec.match.fragment, spec.match.fragInv, spec.match.given = true, negated, true
		case "-m", "--match":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			// tcp/udp/icmp are the implicit matches -p already implies; every
			// other extension would need its own option parser.
			switch value {
			case "tcp", "udp", "icmp":
			default:
				return spec, fmt.Errorf("match extension %q is not supported", value)
			}
		case "-j", "--jump", "-g", "--goto":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			spec.match.target = value
			if strings.EqualFold(value, "ACCEPT") || strings.EqualFold(value, "DROP") ||
				strings.EqualFold(value, "RETURN") || strings.EqualFold(value, "REJECT") ||
				strings.EqualFold(value, "QUEUE") {
				spec.match.target = strings.ToUpper(value)
			}
			spec.match.targetGoto = arg == "-g" || arg == "--goto"
			spec.match.given = true
		case "--reject-with":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			if _, ok := iptablesRejectCode(value); !ok {
				return spec, fmt.Errorf("unsupported reject type %q", value)
			}
			spec.match.rejectWith = value
		case "-n", "--numeric":
			spec.list.numeric = true
		case "-v", "--verbose":
			spec.list.verbose = true
		case "-x", "--exact":
			spec.list.exact = true
		case "--line-numbers":
			spec.list.lineNumbers = true
		case "-4", "--ipv4":
		case "-w", "--wait", "-W", "--wait-interval":
			// Locking is a no-op here: nftables commits each batch atomically.
			if i+1 < len(args) {
				if _, convErr := strconv.Atoi(args[i+1]); convErr == nil {
					i++
				}
			}
		case "-6", "--ipv6":
			return spec, fmt.Errorf("IPv6 is not supported, use iptables for IPv4 only")
		case "--":
			if i+1 < len(args) {
				return spec, fmt.Errorf("unexpected operand %q", args[i+1])
			}
		default:
			return spec, fmt.Errorf("unsupported option or operand %q", arg)
		}
		negated = false
	}
	return spec, validateIptablesSpec(&spec)
}

func setIptablesCommand(spec *iptablesSpec, command byte) error {
	if spec.command != 0 {
		return fmt.Errorf("only one command may be specified")
	}
	spec.command = command
	return nil
}

func validateIptablesSpec(spec *iptablesSpec) error {
	if spec.command == 0 {
		return fmt.Errorf("one of -L, -S, -A, -D, -F, or -P is required")
	}
	if spec.chain != "" && !validIptablesChainName(spec.chain) {
		return fmt.Errorf("invalid chain name %q", spec.chain)
	}
	match := &spec.match
	if (match.sport != nil || match.dport != nil) && match.proto != protocolTCP && match.proto != protocolUDP {
		return fmt.Errorf("ports require -p tcp or -p udp")
	}
	if match.icmpType != nil && match.proto != protocolICMP {
		return fmt.Errorf("--icmp-type requires -p icmp")
	}
	if match.rejectWith != "" && match.target != "REJECT" {
		return fmt.Errorf("--reject-with requires -j REJECT")
	}
	switch spec.command {
	case 'A':
		if match.target == "" {
			return fmt.Errorf("a rule requires -j TARGET")
		}
	case 'D':
		if spec.deleteLine == 0 && match.target == "" {
			return fmt.Errorf("a rule requires -j TARGET or a rule number")
		}
		if spec.deleteLine > 0 && match.given {
			return fmt.Errorf("rule matches are not valid with a rule number")
		}
	case 'L', 'S', 'F', 'P':
		if match.given {
			return fmt.Errorf("rule matches are not valid with this command")
		}
	}
	return nil
}

// validIptablesChainName mirrors the kernel's limit; chain names are free-form
// otherwise, because a target may name any user chain.
func validIptablesChainName(name string) bool {
	if name == "" || len(name) > 31 {
		return false
	}
	return !strings.ContainsAny(name, " \t\n!")
}

func validateIptablesInterface(name string) error {
	if name == "" || len(name) > 15 {
		return fmt.Errorf("invalid interface name %q", name)
	}
	if strings.Contains(strings.TrimSuffix(name, "+"), "+") {
		return fmt.Errorf("invalid interface name %q", name)
	}
	return nil
}

func parseIptablesNetwork(value string) (*net.IPNet, error) {
	if value == "anywhere" || value == "0.0.0.0/0" {
		return nil, nil
	}
	if !strings.Contains(value, "/") {
		value += "/32"
	}
	ip, network, err := net.ParseCIDR(value)
	if err != nil || ip.To4() == nil {
		return nil, fmt.Errorf("invalid IPv4 network %q", value)
	}
	network.IP = ip.To4().Mask(network.Mask)
	return network, nil
}

func parseIptablesPortRange(value string) (iptPortRange, error) {
	low, high, isRange := strings.Cut(value, ":")
	first, err := parseIptablesPort(low)
	if err != nil {
		return iptPortRange{}, err
	}
	if !isRange {
		return iptPortRange{min: first, max: first}, nil
	}
	last := uint16(65535)
	if high != "" {
		if last, err = parseIptablesPort(high); err != nil {
			return iptPortRange{}, err
		}
	}
	if last < first {
		return iptPortRange{}, fmt.Errorf("invalid port range %q", value)
	}
	return iptPortRange{min: first, max: last}, nil
}

func parseIptablesPort(value string) (uint16, error) {
	if parsed, err := strconv.ParseUint(value, 10, 16); err == nil {
		return uint16(parsed), nil
	}
	if port, ok := iptablesServicePort(value); ok {
		return port, nil
	}
	return 0, fmt.Errorf("invalid port %q", value)
}

// appendIptablesRule adds the rule to the end of a chain of the real table.
func appendIptablesRule(spec iptablesSpec) error {
	ruleset, _, err := ensureIptablesChain(spec.table, spec.chain, false)
	if err != nil {
		return err
	}
	if err := resolveIptablesTarget(&spec, ruleset); err != nil {
		return err
	}
	// An ICMP type match rides on the kernel's xtables compatibility layer,
	// the way iptables itself encodes it, so the system tool prints back
	// exactly the rule that was asked for. Where that layer is missing, fall
	// back to a native nftables payload comparison, which filters the same.
	err = appendIptablesRuleExpressions(spec, true)
	if usesIptablesCompat(spec.match) && isIptablesCompatUnavailable(err) {
		err = appendIptablesRuleExpressions(spec, false)
	}
	return err
}

// ensureIptablesChain looks up a chain and, for the filter table, creates the
// standard table and base chains first if the kernel does not hold them yet.
// Chains this applet only pretends exist for listing are not writable.
func ensureIptablesChain(table, name string, rules bool) (*iptRuleset, *iptChain, error) {
	ruleset, err := readIptablesTable(table, rules, true)
	if err != nil {
		return nil, nil, err
	}
	chain := ruleset.chain(name)
	if chain != nil && !chain.synthetic {
		return ruleset, chain, nil
	}
	if table != "filter" || (chain == nil && ruleset.exists) {
		return nil, nil, syscall.ENOENT
	}
	if err := createIptablesFilterTable(); err != nil {
		return nil, nil, err
	}
	if ruleset, err = readIptablesTable(table, rules, true); err != nil {
		return nil, nil, err
	}
	if chain = ruleset.chain(name); chain == nil || chain.synthetic {
		return nil, nil, syscall.ENOENT
	}
	return ruleset, chain, nil
}

func appendIptablesRuleExpressions(spec iptablesSpec, compat bool) error {
	payload := nftGenMessage(nfprotoIPv4)
	payload = append(payload, nftStringAttr(nftaRuleTable, spec.table)...)
	payload = append(payload, nftStringAttr(nftaRuleChain, spec.chain)...)
	payload = append(payload, netlinkAttribute(nftaRuleExpressions|nlaFNested, buildIptablesExpressions(spec.match, compat))...)
	if compat && usesIptablesCompat(spec.match) {
		compatData := nftU32Attr(nftaRuleCompatProto, uint32(spec.match.proto))
		flags := uint32(0)
		if spec.match.protoInv {
			flags = 1 << 1 // NFT_RULE_COMPAT_F_INV
		}
		compatData = append(compatData, nftU32Attr(nftaRuleCompatFlags, flags)...)
		payload = append(payload, netlinkAttribute(nftaRuleCompat|nlaFNested, compatData)...)
	}
	return nftTransaction(nftMsgNewRule, syscall.NLM_F_CREATE|syscall.NLM_F_APPEND, payload)
}

// usesIptablesCompat reports whether the rule needs an xtables extension, which
// only the nft_compat kernel module can carry.
func usesIptablesCompat(match iptMatchSpec) bool {
	return match.icmpType != nil || match.target == "REJECT"
}

func isIptablesCompatUnavailable(err error) bool {
	return errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.EPROTONOSUPPORT) || errors.Is(err, syscall.EINVAL)
}

// resolveIptablesTarget checks a jump target against the chains that exist, the
// way the original rejects "No chain/target/match by that name".
func resolveIptablesTarget(spec *iptablesSpec, ruleset *iptRuleset) error {
	switch spec.match.target {
	case "ACCEPT", "DROP", "RETURN", "REJECT", "QUEUE":
		return nil
	}
	if ruleset.chain(spec.match.target) == nil {
		return syscall.ENOENT
	}
	return nil
}

func buildIptablesExpressions(match iptMatchSpec, compat bool) []byte {
	var expressions []byte
	if match.proto != 0 {
		expressions = append(expressions, nftPayloadExpression(nftPayloadNetwork, 9, 1)...)
		expressions = append(expressions, nftCmpExpression(cmpOperator(match.protoInv), []byte{match.proto})...)
	}
	expressions = append(expressions, nftNetworkExpressions(match.src, 12, match.srcInv)...)
	expressions = append(expressions, nftNetworkExpressions(match.dst, 16, match.dstInv)...)
	expressions = append(expressions, nftInterfaceExpressions(nftMetaIIFName, match.inIface, match.inInv)...)
	expressions = append(expressions, nftInterfaceExpressions(nftMetaOIFName, match.outIface, match.outInv)...)
	if match.fragment {
		expressions = append(expressions, nftFragmentExpressions(match.fragInv)...)
	}
	if match.sport != nil {
		expressions = append(expressions, nftPortExpressions(0, *match.sport, match.sportInv)...)
	}
	if match.dport != nil {
		expressions = append(expressions, nftPortExpressions(2, *match.dport, match.dportInv)...)
	}
	if match.icmpType != nil {
		expressions = append(expressions, buildIptablesICMPMatch(match, compat)...)
	}
	expressions = append(expressions, nftExpression("counter", append(nftU64Attr(nftaCounterBytes, 0), nftU64Attr(nftaCounterPackets, 0)...))...)
	return append(expressions, buildIptablesTarget(match, compat)...)
}

// buildIptablesICMPMatch encodes --icmp-type as the xt_icmp blob iptables hands
// to nft_compat, or as a plain header comparison where that is unavailable.
func buildIptablesICMPMatch(match iptMatchSpec, compat bool) []byte {
	if !compat {
		expressions := nftPayloadExpression(nftPayloadTransport, 0, 1)
		return append(expressions, nftCmpExpression(cmpOperator(match.icmpInv), []byte{*match.icmpType})...)
	}
	// struct ipt_icmp { u8 type; u8 code[2]; u8 invflags; }
	info := make([]byte, xtAlignSize(4))
	info[0] = *match.icmpType
	info[1], info[2] = 0, 255 // any code
	if match.icmpCode != nil {
		info[1], info[2] = *match.icmpCode, *match.icmpCode
	}
	info[3] = invFlags(match.icmpInv, 0x01)
	return nftMatchExpression("icmp", 0, info)
}

func xtAlignSize(size int) int { return (size + xtAlign - 1) &^ (xtAlign - 1) }

func invFlags(invert bool, bit byte) byte {
	if invert {
		return bit
	}
	return 0
}

// buildIptablesTarget encodes the verdict. REJECT goes through the xtables
// target iptables itself uses: a native nftables reject expression works, but
// it makes the whole table unreadable to the system tool.
func buildIptablesTarget(match iptMatchSpec, compat bool) []byte {
	if match.target == "REJECT" {
		rejectType, _ := iptablesRejectCode(match.rejectWith)
		if compat {
			// struct ipt_reject_info holds one enum, padded by XT_ALIGN.
			info := make([]byte, xtAlignSize(4))
			binary.NativeEndian.PutUint32(info[0:4], uint32(rejectType)) //nolint:gosec // G115: the reject enum is a small constant.
			return nftTargetExpression("REJECT", 0, info)
		}
		if rejectType == rejectTCPReset {
			return nftExpression("reject", nftU32Attr(nftaRejectType, 1)) // NFT_REJECT_TCP_RST
		}
		data := nftU32Attr(nftaRejectType, 0) // NFT_REJECT_ICMP_UNREACH
		data = append(data, netlinkAttribute(nftaRejectICMPCode, []byte{rejectICMPCode(rejectType)})...)
		return nftExpression("reject", data)
	}
	verdict := int32(nftJump)
	var chain []byte
	switch match.target {
	case "ACCEPT":
		verdict = nfAccept
	case "DROP":
		verdict = nfDrop
	case "QUEUE":
		verdict = nfQueue
	case "RETURN":
		verdict = nftReturn
	default:
		if match.targetGoto {
			verdict = nftGoto
		}
		chain = nftStringAttr(nftaVerdictChain, match.target)
	}
	code := netlinkAttribute(nftaVerdictCode, nftU32(uint32(verdict))) //nolint:gosec // G115: nft verdicts are negative int32 constants.
	data := netlinkAttribute(nftaDataVerdict|nlaFNested, append(code, chain...))
	immediate := append(nftU32Attr(nftaImmediateDreg, nftRegVerdict), netlinkAttribute(nftaImmediateData|nlaFNested, data)...)
	return nftExpression("immediate", immediate)
}

func cmpOperator(invert bool) uint32 {
	if invert {
		return nftCmpNeq
	}
	return nftCmpEq
}

func nftNetworkExpressions(network *net.IPNet, offset uint32, invert bool) []byte {
	if network == nil {
		return nil
	}
	ones, _ := network.Mask.Size()
	if ones == 0 {
		return nil
	}
	result := nftPayloadExpression(nftPayloadNetwork, offset, 4)
	if ones < 32 {
		mask := []byte(network.Mask)
		data := append(nftU32Attr(nftaBitwiseSreg, nftReg32_00), nftU32Attr(nftaBitwiseDreg, nftReg32_00)...)
		data = append(data, nftU32Attr(nftaBitwiseLen, 4)...)
		data = append(data, netlinkAttribute(nftaBitwiseMask|nlaFNested, netlinkAttribute(nftaDataValue, mask))...)
		data = append(data, netlinkAttribute(nftaBitwiseXor|nlaFNested, netlinkAttribute(nftaDataValue, []byte{0, 0, 0, 0}))...)
		result = append(result, nftExpression("bitwise", data)...)
	}
	masked := network.IP.Mask(network.Mask)
	return append(result, nftCmpExpression(cmpOperator(invert), []byte(masked.To4()))...)
}

// nftInterfaceExpressions matches meta iifname/oifname. A trailing "+" is a
// wildcard: iptables encodes it by comparing only the fixed prefix, without the
// terminating NUL an exact name carries.
func nftInterfaceExpressions(key uint32, name string, invert bool) []byte {
	if name == "" {
		return nil
	}
	value := []byte(name)
	if strings.HasSuffix(name, "+") {
		value = value[:len(value)-1]
		if len(value) == 0 {
			return nil
		}
	} else {
		value = append(value, 0)
	}
	result := nftExpression("meta", append(nftU32Attr(nftaMetaDreg, nftReg32_00), nftU32Attr(nftaMetaKey, key)...))
	return append(result, nftCmpExpression(cmpOperator(invert), value)...)
}

func nftPortExpressions(offset uint32, ports iptPortRange, invert bool) []byte {
	result := nftPayloadExpression(nftPayloadTransport, offset, 2)
	if ports.min == ports.max {
		return append(result, nftCmpExpression(cmpOperator(invert), nftPortBytes(ports.min))...)
	}
	operator := uint32(0) // NFT_RANGE_EQ
	if invert {
		operator = 1
	}
	data := append(nftU32Attr(nftaRangeSreg, nftReg32_00), nftU32Attr(nftaRangeOp, operator)...)
	data = append(data, netlinkAttribute(nftaRangeFromData|nlaFNested, netlinkAttribute(nftaDataValue, nftPortBytes(ports.min)))...)
	data = append(data, netlinkAttribute(nftaRangeToData|nlaFNested, netlinkAttribute(nftaDataValue, nftPortBytes(ports.max)))...)
	return append(result, nftExpression("range", data)...)
}

// nftFragmentExpressions matches -f: a non-zero fragment offset in the header,
// which "! -f" turns into a test for a zero one.
func nftFragmentExpressions(invert bool) []byte {
	result := nftPayloadExpression(nftPayloadNetwork, 6, 2)
	data := append(nftU32Attr(nftaBitwiseSreg, nftReg32_00), nftU32Attr(nftaBitwiseDreg, nftReg32_00)...)
	data = append(data, nftU32Attr(nftaBitwiseLen, 2)...)
	data = append(data, netlinkAttribute(nftaBitwiseMask|nlaFNested, netlinkAttribute(nftaDataValue, []byte{0x1f, 0xff}))...)
	data = append(data, netlinkAttribute(nftaBitwiseXor|nlaFNested, netlinkAttribute(nftaDataValue, []byte{0, 0}))...)
	result = append(result, nftExpression("bitwise", data)...)
	operator := uint32(nftCmpNeq)
	if invert {
		operator = nftCmpEq
	}
	return append(result, nftCmpExpression(operator, []byte{0, 0})...)
}

func nftPayloadExpression(base, offset, length uint32) []byte {
	data := append(nftU32Attr(nftaPayloadDreg, nftReg32_00), nftU32Attr(nftaPayloadBase, base)...)
	data = append(data, nftU32Attr(nftaPayloadOffset, offset)...)
	data = append(data, nftU32Attr(nftaPayloadLen, length)...)
	return nftExpression("payload", data)
}

func nftCmpExpression(operator uint32, value []byte) []byte {
	data := append(nftU32Attr(nftaCmpSreg, nftReg32_00), nftU32Attr(nftaCmpOp, operator)...)
	data = append(data, netlinkAttribute(nftaCmpData|nlaFNested, netlinkAttribute(nftaDataValue, value))...)
	return nftExpression("cmp", data)
}

func nftMatchExpression(name string, revision uint32, info []byte) []byte {
	data := nftStringAttr(nftaMatchName, name)
	data = append(data, nftU32Attr(nftaMatchRev, revision)...)
	data = append(data, netlinkAttribute(nftaMatchInfo, info)...)
	return nftExpression("match", data)
}

func nftTargetExpression(name string, revision uint32, info []byte) []byte {
	data := nftStringAttr(nftaTargetName, name)
	data = append(data, nftU32Attr(nftaTargetRev, revision)...)
	data = append(data, netlinkAttribute(nftaTargetInfo, info)...)
	return nftExpression("target", data)
}

func nftExpression(name string, data []byte) []byte {
	value := nftStringAttr(nftaExprName, name)
	value = append(value, netlinkAttribute(nftaExprData|nlaFNested, data)...)
	return netlinkAttribute(nftaListElem|nlaFNested, value)
}

func nftPortBytes(port uint16) []byte {
	value := make([]byte, 2)
	binary.BigEndian.PutUint16(value, port)
	return value
}

// createIptablesFilterTable creates the filter table and its three base chains
// with the layout iptables uses: type filter, priority 0, policy ACCEPT and
// per-chain counters so the policy line can report packets and bytes.
func createIptablesFilterTable() error {
	payload := nftGenMessage(nfprotoIPv4)
	payload = append(payload, nftStringAttr(nftaTableName, "filter")...)
	if err := nftTransaction(nftMsgNewTable, syscall.NLM_F_CREATE, payload); err != nil && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	chains := []struct {
		name string
		hook uint32
	}{{"INPUT", nfHookLocalIn}, {"FORWARD", nfHookForward}, {"OUTPUT", nfHookLocalOut}}
	for _, chain := range chains {
		payload = nftGenMessage(nfprotoIPv4)
		payload = append(payload, nftStringAttr(nftaChainTable, "filter")...)
		payload = append(payload, nftStringAttr(nftaChainName, chain.name)...)
		payload = append(payload, nftStringAttr(nftaChainType, "filter")...)
		hook := append(nftU32Attr(nftaHookNum, chain.hook), nftU32Attr(nftaHookPriority, 0)...)
		payload = append(payload, netlinkAttribute(nftaChainHook|nlaFNested, hook)...)
		payload = append(payload, nftU32Attr(nftaChainPolicy, nfAccept)...)
		counters := append(nftU64Attr(nftaCounterBytes, 0), nftU64Attr(nftaCounterPackets, 0)...)
		payload = append(payload, netlinkAttribute(nftaChainCounters|nlaFNested, counters)...)
		if err := nftTransaction(nftMsgNewChain, syscall.NLM_F_CREATE|syscall.NLM_F_EXCL, payload); err != nil && !errors.Is(err, syscall.EEXIST) {
			return err
		}
	}
	return nil
}

// deleteIptablesRule removes a rule by number, or the first rule whose saved
// form is identical to the one described on the command line.
func deleteIptablesRule(spec iptablesSpec) error {
	_, chain, err := ensureIptablesChain(spec.table, spec.chain, true)
	if err != nil {
		return err
	}
	if spec.deleteLine > 0 {
		if spec.deleteLine > len(chain.rules) {
			return errIptablesDeletionIndex
		}
		return deleteIptablesHandle(spec.table, chain.name, chain.rules[spec.deleteLine-1].handle)
	}
	wanted := iptSaveRuleArguments(iptablesSpecRule(spec.match))
	for _, rule := range chain.rules {
		if iptSaveRuleArguments(rule) == wanted {
			return deleteIptablesHandle(spec.table, chain.name, rule.handle)
		}
	}
	return errIptablesNoSuchRule
}

func flushIptablesRules(spec iptablesSpec) error {
	if spec.chain != "" {
		_, chain, err := ensureIptablesChain(spec.table, spec.chain, true)
		if err != nil {
			return err
		}
		return flushIptablesChain(spec.table, chain)
	}
	ruleset, err := readIptablesTable(spec.table, true, true)
	if err != nil {
		return err
	}
	for _, chain := range ruleset.chains {
		if err := flushIptablesChain(spec.table, chain); err != nil {
			return err
		}
	}
	return nil
}

func flushIptablesChain(table string, chain *iptChain) error {
	for _, rule := range chain.rules {
		if err := deleteIptablesHandle(table, chain.name, rule.handle); err != nil {
			return err
		}
	}
	return nil
}

func deleteIptablesHandle(table, chain string, handle uint64) error {
	payload := nftGenMessage(nfprotoIPv4)
	payload = append(payload, nftStringAttr(nftaRuleTable, table)...)
	payload = append(payload, nftStringAttr(nftaRuleChain, chain)...)
	payload = append(payload, nftU64Attr(nftaRuleHandle, handle)...)
	return nftTransaction(nftMsgDelRule, 0, payload)
}

func setIptablesPolicy(spec iptablesSpec) error {
	_, chain, err := ensureIptablesChain(spec.table, spec.chain, false)
	if errors.Is(err, syscall.ENOENT) {
		return errIptablesBadBuiltinChain
	}
	if err != nil {
		return err
	}
	if !chain.base {
		return errIptablesBadBuiltinChain
	}
	policy := uint32(nfDrop)
	if spec.policy == "ACCEPT" {
		policy = nfAccept
	}
	payload := nftGenMessage(nfprotoIPv4)
	payload = append(payload, nftStringAttr(nftaChainTable, spec.table)...)
	payload = append(payload, nftStringAttr(nftaChainName, spec.chain)...)
	payload = append(payload, nftU32Attr(nftaChainPolicy, policy)...)
	return nftTransaction(nftMsgNewChain, 0, payload)
}

func nftGenMessage(family byte) []byte { return []byte{family, 0, 0, 0} }

func nftStringAttr(attr uint16, value string) []byte {
	return netlinkAttribute(attr, append([]byte(value), 0))
}

func nftU32(value uint32) []byte {
	data := make([]byte, 4)
	binary.BigEndian.PutUint32(data, value)
	return data
}

func nftU32Attr(attr uint16, value uint32) []byte {
	return netlinkAttribute(attr, nftU32(value))
}

func nftU64Attr(attr uint16, value uint64) []byte {
	data := make([]byte, 8)
	binary.BigEndian.PutUint64(data, value)
	return netlinkAttribute(attr, data)
}

func nftBatchGenMessage() []byte {
	message := nftGenMessage(0)
	binary.BigEndian.PutUint16(message[2:4], nfnlSubsysNFT)
	return message
}

func nftMessage(messageType, flags uint16, sequence uint32, payload []byte) []byte {
	message := make([]byte, syscall.NLMSG_HDRLEN+len(payload))
	binary.NativeEndian.PutUint32(message[0:4], uint32(len(message))) //nolint:gosec // Netlink messages are bounded in memory.
	binary.NativeEndian.PutUint16(message[4:6], messageType)
	binary.NativeEndian.PutUint16(message[6:8], flags)
	binary.NativeEndian.PutUint32(message[8:12], sequence)
	copy(message[syscall.NLMSG_HDRLEN:], payload)
	return message
}

func nftTransaction(operation uint16, flags uint16, payload []byte) error {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, netlinkNetfilter)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return err
	}
	beginSeq := netlinkSequence.Add(1)
	opSeq := netlinkSequence.Add(1)
	endSeq := netlinkSequence.Add(1)
	request := nftMessage(nfnlBatchBegin, syscall.NLM_F_REQUEST, beginSeq, nftBatchGenMessage())
	request = append(request, nftMessage(uint16(nfnlSubsysNFT<<8)|operation,
		syscall.NLM_F_REQUEST|syscall.NLM_F_ACK|flags, opSeq, payload)...)
	request = append(request, nftMessage(nfnlBatchEnd, syscall.NLM_F_REQUEST, endSeq, nftBatchGenMessage())...)
	if err := syscall.Sendto(fd, request, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return err
	}
	buffer := make([]byte, 64*1024)
	for {
		n, from, recvErr := syscall.Recvfrom(fd, buffer, 0)
		if errors.Is(recvErr, syscall.EINTR) {
			continue
		}
		if recvErr != nil {
			return recvErr
		}
		if sender, ok := from.(*syscall.SockaddrNetlink); !ok || sender.Pid != 0 {
			continue
		}
		messages, parseErr := syscall.ParseNetlinkMessage(buffer[:n])
		if parseErr != nil {
			return parseErr
		}
		for _, message := range messages {
			if message.Header.Type != syscall.NLMSG_ERROR {
				continue
			}
			if len(message.Data) < 4 {
				return fmt.Errorf("short netfilter response")
			}
			code := int32(binary.NativeEndian.Uint32(message.Data[:4])) //nolint:gosec // Kernel error codes are signed int32 values.
			if code == 0 && message.Header.Seq == opSeq {
				return nil
			}
			if code == 0 {
				continue
			}
			return syscall.Errno(-code) //nolint:gosec // Negative kernel errno becomes positive.
		}
	}
}

func nftDump(operation uint16, payload []byte) ([]syscall.NetlinkMessage, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, netlinkNetfilter)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, err
	}
	sequence := netlinkSequence.Add(1)
	request := nftMessage(uint16(nfnlSubsysNFT<<8)|operation, syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP, sequence, payload)
	if err := syscall.Sendto(fd, request, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, err
	}
	// A dump arrives as a series of datagrams; the kernel caps each one well
	// below this buffer, and a short read would silently truncate a rule.
	buffer := make([]byte, 256*1024)
	var result []syscall.NetlinkMessage
	for {
		n, from, recvErr := syscall.Recvfrom(fd, buffer, 0)
		if errors.Is(recvErr, syscall.EINTR) {
			continue
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if sender, ok := from.(*syscall.SockaddrNetlink); !ok || sender.Pid != 0 {
			continue
		}
		messages, parseErr := syscall.ParseNetlinkMessage(buffer[:n])
		if parseErr != nil {
			return nil, parseErr
		}
		for _, message := range messages {
			if message.Header.Seq != sequence {
				continue
			}
			switch message.Header.Type {
			case syscall.NLMSG_DONE:
				return result, nil
			case syscall.NLMSG_ERROR:
				if len(message.Data) < 4 {
					return nil, fmt.Errorf("short netfilter response")
				}
				code := int32(binary.NativeEndian.Uint32(message.Data[:4])) //nolint:gosec // Kernel error codes are signed int32 values.
				if code != 0 {
					return nil, syscall.Errno(-code) //nolint:gosec // Negative kernel errno becomes positive.
				}
			default:
				// A dumped message keeps a copy of its payload, which the
				// receive buffer is about to overwrite.
				stored := message
				stored.Data = append([]byte(nil), message.Data...)
				result = append(result, stored)
			}
		}
	}
}
