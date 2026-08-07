// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const (
	netlinkNetfilter = 12
	nfprotoIPv4      = 2
	nfnlSubsysNFT    = 10
	nfnlBatchBegin   = 0x10
	nfnlBatchEnd     = 0x11
	nftTableName     = "ba6_iptables"
	nftUserPrefix    = "ba6:iptables:v1\t"

	nftMsgNewTable = 0
	nftMsgNewChain = 3
	nftMsgGetChain = 4
	nftMsgNewRule  = 6
	nftMsgGetRule  = 7
	nftMsgDelRule  = 8

	nftaTableName = 1

	nftaChainTable  = 1
	nftaChainName   = 3
	nftaChainHook   = 4
	nftaChainPolicy = 5
	nftaChainType   = 7

	nftaHookNum      = 1
	nftaHookPriority = 2

	nftaRuleTable       = 1
	nftaRuleChain       = 2
	nftaRuleHandle      = 3
	nftaRuleExpressions = 4
	nftaRuleUserdata    = 7

	nftaListElem = 1
	nftaExprName = 1
	nftaExprData = 2

	nftaDataValue   = 1
	nftaDataVerdict = 2
	nftaVerdictCode = 1

	nftaImmediateDreg = 1
	nftaImmediateData = 2

	nftaPayloadDreg   = 1
	nftaPayloadBase   = 2
	nftaPayloadOffset = 3
	nftaPayloadLen    = 4

	nftaCmpSreg = 1
	nftaCmpOp   = 2
	nftaCmpData = 3

	nftaBitwiseSreg = 1
	nftaBitwiseDreg = 2
	nftaBitwiseLen  = 3
	nftaBitwiseMask = 4
	nftaBitwiseXor  = 5

	nftaRejectType     = 1
	nftaRejectICMPCode = 2

	nftRegVerdict = 0
	nftReg32_00   = 8

	nftPayloadNetwork   = 1
	nftPayloadTransport = 2

	nfDrop   = 0
	nfAccept = 1

	nfHookLocalIn  = 1
	nfHookForward  = 2
	nfHookLocalOut = 3
)

type iptablesSpec struct {
	command     byte
	chain       string
	protocol    string
	source      *net.IPNet
	destination *net.IPNet
	sourcePort  *uint16
	destPort    *uint16
	target      string
	deleteLine  int
	lineNumbers bool
}

type nftRuleRecord struct {
	chain    string
	handle   uint64
	userdata string
}

func cmdIptables(args []string) int {
	spec, err := parseIptables(args)
	if err != nil {
		fatalf("iptables", "%v", err)
		return 2
	}
	if err := ensureIptablesRuleset(); err != nil {
		fatalf("iptables", "%v", err)
		return 1
	}
	switch spec.command {
	case 'L':
		err = listIptables(spec)
	case 'A':
		err = appendIptablesRule(spec)
	case 'D':
		err = deleteIptablesRule(spec)
	case 'F':
		err = flushIptablesRules(spec.chain)
	case 'P':
		err = setIptablesPolicy(spec.chain, spec.target)
	}
	if err != nil {
		fatalf("iptables", "%v", err)
		return 1
	}
	return 0
}

func parseIptables(args []string) (iptablesSpec, error) {
	spec := iptablesSpec{protocol: "all"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func(option string) (string, error) {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s requires an argument", option)
			}
			return args[i], nil
		}
		switch arg {
		case "-L", "--list", "-A", "--append", "-D", "--delete", "-F", "--flush", "-P", "--policy":
			if spec.command != 0 {
				return spec, fmt.Errorf("only one command may be specified")
			}
			spec.command = map[string]byte{"-L": 'L', "--list": 'L', "-A": 'A', "--append": 'A', "-D": 'D', "--delete": 'D', "-F": 'F', "--flush": 'F', "-P": 'P', "--policy": 'P'}[arg]
			if spec.command == 'L' || spec.command == 'F' {
				if i+1 < len(args) && isIptablesChain(args[i+1]) {
					i++
					spec.chain = strings.ToUpper(args[i])
				}
			} else {
				value, err := next(arg)
				if err != nil {
					return spec, err
				}
				spec.chain = strings.ToUpper(value)
				if !isIptablesChain(spec.chain) {
					return spec, fmt.Errorf("unsupported chain %q", value)
				}
				if spec.command == 'D' && i+1 < len(args) {
					if line, convErr := strconv.Atoi(args[i+1]); convErr == nil && line > 0 && i+2 == len(args) {
						spec.deleteLine = line
						i++
					}
				}
				if spec.command == 'P' {
					value, err := next(arg)
					if err != nil {
						return spec, err
					}
					spec.target = strings.ToUpper(value)
					if spec.target != "ACCEPT" && spec.target != "DROP" {
						return spec, fmt.Errorf("policy must be ACCEPT or DROP")
					}
				}
			}
		case "-p", "--protocol":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			switch strings.ToLower(value) {
			case "all", "tcp", "udp", "icmp":
				spec.protocol = strings.ToLower(value)
			default:
				return spec, fmt.Errorf("unsupported protocol %q", value)
			}
		case "-s", "--source", "-d", "--destination":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			network, err := parseIptablesNetwork(value)
			if err != nil {
				return spec, err
			}
			if arg == "-s" || arg == "--source" {
				spec.source = network
			} else {
				spec.destination = network
			}
		case "--sport", "--source-port", "--dport", "--destination-port":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			port, err := parseIptablesPort(value)
			if err != nil {
				return spec, err
			}
			if arg == "--sport" || arg == "--source-port" {
				spec.sourcePort = &port
			} else {
				spec.destPort = &port
			}
		case "-j", "--jump":
			value, err := next(arg)
			if err != nil {
				return spec, err
			}
			spec.target = strings.ToUpper(value)
			if spec.target != "ACCEPT" && spec.target != "DROP" && spec.target != "REJECT" {
				return spec, fmt.Errorf("unsupported target %q", value)
			}
		case "-n", "--numeric", "-v", "--verbose", "-4":
		case "--line-numbers":
			spec.lineNumbers = true
		case "--":
			if i+1 < len(args) {
				return spec, fmt.Errorf("unexpected operand %q", args[i+1])
			}
		default:
			return spec, fmt.Errorf("unsupported option or operand %q", arg)
		}
	}
	if spec.command == 0 {
		return spec, fmt.Errorf("one of -L, -A, -D, -F, or -P is required")
	}
	if (spec.command == 'A' || spec.command == 'D' && spec.deleteLine == 0) && spec.target == "" {
		return spec, fmt.Errorf("a rule requires -j TARGET")
	}
	if spec.command == 'P' && spec.target == "REJECT" {
		return spec, fmt.Errorf("REJECT is not a valid chain policy")
	}
	if (spec.sourcePort != nil || spec.destPort != nil) && spec.protocol != "tcp" && spec.protocol != "udp" {
		return spec, fmt.Errorf("ports require -p tcp or -p udp")
	}
	if spec.command == 'L' || spec.command == 'F' || spec.command == 'P' || spec.deleteLine > 0 {
		if spec.source != nil || spec.destination != nil || spec.sourcePort != nil || spec.destPort != nil || spec.protocol != "all" || spec.target != "" && spec.command != 'P' {
			return spec, fmt.Errorf("rule matches are not valid with this command")
		}
	}
	return spec, nil
}

func isIptablesChain(value string) bool {
	switch strings.ToUpper(value) {
	case "INPUT", "FORWARD", "OUTPUT":
		return true
	}
	return false
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
	network.IP = ip.To4()
	return network, nil
}

func parseIptablesPort(value string) (uint16, error) {
	parsed, err := strconv.ParseUint(value, 10, 16)
	if err != nil || parsed == 0 {
		return 0, fmt.Errorf("invalid port %q", value)
	}
	return uint16(parsed), nil
}

func ensureIptablesRuleset() error {
	payload := nftGenMessage(nfprotoIPv4)
	payload = append(payload, nftStringAttr(nftaTableName, nftTableName)...)
	if err := nftTransaction(nftMsgNewTable, syscall.NLM_F_CREATE|syscall.NLM_F_EXCL, payload); err != nil && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	chains := []struct {
		name string
		hook uint32
	}{{"INPUT", nfHookLocalIn}, {"FORWARD", nfHookForward}, {"OUTPUT", nfHookLocalOut}}
	for _, chain := range chains {
		payload = nftGenMessage(nfprotoIPv4)
		payload = append(payload, nftStringAttr(nftaChainTable, nftTableName)...)
		payload = append(payload, nftStringAttr(nftaChainName, chain.name)...)
		payload = append(payload, nftStringAttr(nftaChainType, "filter")...)
		hook := append(nftU32Attr(nftaHookNum, chain.hook), nftU32Attr(nftaHookPriority, 0)...)
		payload = append(payload, netlinkAttribute(nftaChainHook|nlaFNested, hook)...)
		payload = append(payload, nftU32Attr(nftaChainPolicy, nfAccept)...)
		if err := nftTransaction(nftMsgNewChain, syscall.NLM_F_CREATE|syscall.NLM_F_EXCL, payload); err != nil && !errors.Is(err, syscall.EEXIST) {
			return err
		}
	}
	return nil
}

func appendIptablesRule(spec iptablesSpec) error {
	payload := nftGenMessage(nfprotoIPv4)
	payload = append(payload, nftStringAttr(nftaRuleTable, nftTableName)...)
	payload = append(payload, nftStringAttr(nftaRuleChain, spec.chain)...)
	payload = append(payload, netlinkAttribute(nftaRuleExpressions|nlaFNested, buildIptablesExpressions(spec))...)
	payload = append(payload, netlinkAttribute(nftaRuleUserdata, []byte(nftUserPrefix+iptablesRuleText(spec)))...)
	return nftTransaction(nftMsgNewRule, syscall.NLM_F_CREATE|syscall.NLM_F_APPEND, payload)
}

func buildIptablesExpressions(spec iptablesSpec) []byte {
	var expressions []byte
	if spec.protocol != "all" {
		protocols := map[string]byte{"icmp": 1, "tcp": 6, "udp": 17}
		expressions = append(expressions, nftPayloadExpression(nftPayloadNetwork, 9, 1)...)
		expressions = append(expressions, nftCmpExpression([]byte{protocols[spec.protocol]})...)
	}
	expressions = append(expressions, nftNetworkExpressions(spec.source, 12)...)
	expressions = append(expressions, nftNetworkExpressions(spec.destination, 16)...)
	if spec.sourcePort != nil {
		expressions = append(expressions, nftPayloadExpression(nftPayloadTransport, 0, 2)...)
		expressions = append(expressions, nftCmpExpression(nftPortBytes(*spec.sourcePort))...)
	}
	if spec.destPort != nil {
		expressions = append(expressions, nftPayloadExpression(nftPayloadTransport, 2, 2)...)
		expressions = append(expressions, nftCmpExpression(nftPortBytes(*spec.destPort))...)
	}
	if spec.target == "REJECT" {
		data := append(nftU32Attr(nftaRejectType, 0), netlinkAttribute(nftaRejectICMPCode, []byte{3})...)
		expressions = append(expressions, nftExpression("reject", data)...)
	} else {
		verdict := uint32(nfDrop)
		if spec.target == "ACCEPT" {
			verdict = nfAccept
		}
		code := netlinkAttribute(nftaVerdictCode, nftU32(verdict))
		data := netlinkAttribute(nftaDataVerdict|nlaFNested, code)
		immediate := append(nftU32Attr(nftaImmediateDreg, nftRegVerdict), netlinkAttribute(nftaImmediateData|nlaFNested, data)...)
		expressions = append(expressions, nftExpression("immediate", immediate)...)
	}
	return expressions
}

func nftNetworkExpressions(network *net.IPNet, offset uint32) []byte {
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
	return append(result, nftCmpExpression([]byte(masked.To4()))...)
}

func nftPayloadExpression(base, offset, length uint32) []byte {
	data := append(nftU32Attr(nftaPayloadDreg, nftReg32_00), nftU32Attr(nftaPayloadBase, base)...)
	data = append(data, nftU32Attr(nftaPayloadOffset, offset)...)
	data = append(data, nftU32Attr(nftaPayloadLen, length)...)
	return nftExpression("payload", data)
}

func nftCmpExpression(value []byte) []byte {
	data := append(nftU32Attr(nftaCmpSreg, nftReg32_00), nftU32Attr(nftaCmpOp, 0)...)
	data = append(data, netlinkAttribute(nftaCmpData|nlaFNested, netlinkAttribute(nftaDataValue, value))...)
	return nftExpression("cmp", data)
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

func iptablesRuleText(spec iptablesSpec) string {
	parts := []string{"-p", spec.protocol, "-s", formatIptablesNetwork(spec.source), "-d", formatIptablesNetwork(spec.destination)}
	if spec.sourcePort != nil {
		parts = append(parts, "--sport", strconv.Itoa(int(*spec.sourcePort)))
	}
	if spec.destPort != nil {
		parts = append(parts, "--dport", strconv.Itoa(int(*spec.destPort)))
	}
	parts = append(parts, "-j", spec.target)
	return strings.Join(parts, " ")
}

func formatIptablesNetwork(network *net.IPNet) string {
	if network == nil {
		return "0.0.0.0/0"
	}
	return network.String()
}

func listIptables(spec iptablesSpec) error {
	records, err := getIptablesRules()
	if err != nil {
		return err
	}
	policies, err := getIptablesPolicies()
	if err != nil {
		return err
	}
	chains := []string{"INPUT", "FORWARD", "OUTPUT"}
	if spec.chain != "" {
		chains = []string{spec.chain}
	}
	out := bufio.NewWriter(os.Stdout)
	for chainIndex, chain := range chains {
		if chainIndex > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "Chain %s (policy %s)\n", chain, policies[chain])
		if spec.lineNumbers {
			fmt.Fprintln(out, "num  rule")
		}
		line := 0
		for _, record := range records {
			if record.chain != chain {
				continue
			}
			line++
			text := strings.TrimPrefix(record.userdata, nftUserPrefix)
			if !strings.HasPrefix(record.userdata, nftUserPrefix) {
				text = fmt.Sprintf("[unmanaged nft rule handle %d]", record.handle)
			}
			if spec.lineNumbers {
				fmt.Fprintf(out, "%-4d ", line)
			}
			fmt.Fprintln(out, text)
		}
	}
	return out.Flush()
}

func getIptablesPolicies() (map[string]string, error) {
	payload := nftGenMessage(nfprotoIPv4)
	payload = append(payload, nftStringAttr(nftaChainTable, nftTableName)...)
	messages, err := nftDump(nftMsgGetChain, payload)
	if err != nil {
		return nil, err
	}
	policies := map[string]string{"INPUT": "ACCEPT", "FORWARD": "ACCEPT", "OUTPUT": "ACCEPT"}
	for _, message := range messages {
		if len(message.Data) < 4 {
			continue
		}
		attrs, err := parseRawNetlinkAttributes(message.Data[4:])
		if err != nil {
			return nil, err
		}
		name := ""
		policy := uint32(nfAccept)
		for _, attr := range attrs {
			switch attr.typeID {
			case nftaChainName:
				name = netlinkString(attr.value)
			case nftaChainPolicy:
				if len(attr.value) >= 4 {
					policy = binary.BigEndian.Uint32(attr.value)
				}
			}
		}
		if isIptablesChain(name) && policy == nfDrop {
			policies[name] = "DROP"
		}
	}
	return policies, nil
}

func getIptablesRules() ([]nftRuleRecord, error) {
	payload := nftGenMessage(nfprotoIPv4)
	payload = append(payload, nftStringAttr(nftaRuleTable, nftTableName)...)
	messages, err := nftDump(nftMsgGetRule, payload)
	if err != nil {
		return nil, err
	}
	var records []nftRuleRecord
	for _, message := range messages {
		if len(message.Data) < 4 {
			continue
		}
		attrs, err := parseRawNetlinkAttributes(message.Data[4:])
		if err != nil {
			return nil, err
		}
		var record nftRuleRecord
		for _, attr := range attrs {
			switch attr.typeID {
			case nftaRuleChain:
				record.chain = netlinkString(attr.value)
			case nftaRuleHandle:
				if len(attr.value) >= 8 {
					record.handle = binary.BigEndian.Uint64(attr.value)
				}
			case nftaRuleUserdata:
				record.userdata = strings.TrimRight(string(attr.value), "\x00")
			}
		}
		if record.chain != "" && record.handle != 0 {
			records = append(records, record)
		}
	}
	return records, nil
}

func deleteIptablesRule(spec iptablesSpec) error {
	records, err := getIptablesRules()
	if err != nil {
		return err
	}
	wanted := nftUserPrefix + iptablesRuleText(spec)
	line := 0
	for _, record := range records {
		if record.chain != spec.chain {
			continue
		}
		line++
		if spec.deleteLine > 0 && line == spec.deleteLine || spec.deleteLine == 0 && record.userdata == wanted {
			return deleteIptablesHandle(record.chain, record.handle)
		}
	}
	return fmt.Errorf("matching rule does not exist")
}

func flushIptablesRules(chain string) error {
	records, err := getIptablesRules()
	if err != nil {
		return err
	}
	for _, record := range records {
		if chain == "" || record.chain == chain {
			if err := deleteIptablesHandle(record.chain, record.handle); err != nil {
				return err
			}
		}
	}
	return nil
}

func deleteIptablesHandle(chain string, handle uint64) error {
	payload := nftGenMessage(nfprotoIPv4)
	payload = append(payload, nftStringAttr(nftaRuleTable, nftTableName)...)
	payload = append(payload, nftStringAttr(nftaRuleChain, chain)...)
	payload = append(payload, nftU64Attr(nftaRuleHandle, handle)...)
	return nftTransaction(nftMsgDelRule, 0, payload)
}

func setIptablesPolicy(chain, target string) error {
	policy := uint32(nfDrop)
	if target == "ACCEPT" {
		policy = nfAccept
	}
	payload := nftGenMessage(nfprotoIPv4)
	payload = append(payload, nftStringAttr(nftaChainTable, nftTableName)...)
	payload = append(payload, nftStringAttr(nftaChainName, chain)...)
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
	buffer := make([]byte, 64*1024)
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
				result = append(result, message)
			}
		}
	}
}
