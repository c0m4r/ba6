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
	ndaDst    = 1
	ndaLLAddr = 2

	rtmNewRule = 32
	rtmDelRule = 33
	rtmGetRule = 34

	fraDst       = 1
	fraSrc       = 2
	fraPriority  = 6
	fraTable     = 15
	frActToTable = 1

	nlmFReplace = 0x100
)

var neighborStates = map[string]uint16{
	"incomplete": 0x01,
	"reachable":  0x02,
	"stale":      0x04,
	"delay":      0x08,
	"probe":      0x10,
	"failed":     0x20,
	"noarp":      0x40,
	"permanent":  0x80,
}

type neighborSpec struct {
	family  int
	ip      net.IP
	dev     string
	lladdr  net.HardwareAddr
	state   uint16
	replace bool
}

func ipNeighbor(family int, args []string) error {
	if len(args) == 0 || ipMatches(args[0], "show", "list", "lst") {
		if len(args) > 0 {
			args = args[1:]
		}
		dev, err := parseShowDev(args)
		if err != nil {
			return err
		}
		return showNeighbors(family, dev)
	}
	var operation string
	switch {
	case ipMatches(args[0], "add"):
		operation = "add"
	case ipMatches(args[0], "replace"):
		operation = "replace"
	case ipMatches(args[0], "delete"), args[0] == "remove":
		operation = "del"
	default:
		return fmt.Errorf("unknown neighbor command %q", args[0])
	}
	spec, err := parseNeighborSpec(family, args[1:])
	if err != nil {
		return err
	}
	spec.replace = operation == "replace"
	if operation != "del" && len(spec.lladdr) == 0 {
		return fmt.Errorf("neighbor %s requires lladdr ADDRESS", operation)
	}
	return changeNeighbor(operation != "del", spec)
}

func parseNeighborSpec(family int, args []string) (neighborSpec, error) {
	var spec neighborSpec
	if len(args) == 0 {
		return spec, fmt.Errorf("missing neighbor address")
	}
	spec.ip = net.ParseIP(args[0])
	if spec.ip == nil {
		return spec, fmt.Errorf("invalid neighbor address %q", args[0])
	}
	spec.family = familyOfIP(spec.ip)
	if family != syscall.AF_UNSPEC && family != spec.family {
		return spec, fmt.Errorf("neighbor has the wrong address family")
	}
	spec.state = neighborStates["permanent"]
	args = args[1:]
	for len(args) > 0 {
		if len(args) < 2 {
			return spec, fmt.Errorf("missing value after %q", args[0])
		}
		key, value := args[0], args[1]
		args = args[2:]
		switch key {
		case "dev":
			spec.dev = value
		case "lladdr":
			address, err := net.ParseMAC(value)
			if err != nil {
				return spec, fmt.Errorf("invalid link-layer address %q", value)
			}
			spec.lladdr = address
		case "nud":
			state, ok := neighborStates[strings.ToLower(value)]
			if !ok {
				return spec, fmt.Errorf("invalid neighbor state %q", value)
			}
			spec.state = state
		default:
			return spec, fmt.Errorf("unknown neighbor option %q", key)
		}
	}
	if spec.dev == "" {
		return spec, fmt.Errorf("neighbor requires dev IFACE")
	}
	return spec, nil
}

func changeNeighbor(add bool, spec neighborSpec) error {
	iface, err := net.InterfaceByName(spec.dev)
	if err != nil {
		return fmt.Errorf("device %q: %w", spec.dev, err)
	}
	payload := make([]byte, 12, 64)
	payload[0] = byte(spec.family)                                   //nolint:gosec // parser restricts the family to IPv4 or IPv6.
	binary.NativeEndian.PutUint32(payload[4:8], uint32(iface.Index)) //nolint:gosec // interface indices are nonnegative.
	binary.NativeEndian.PutUint16(payload[8:10], spec.state)
	payload[11] = syscall.RTN_UNICAST
	payload = append(payload, netlinkAttribute(ndaDst, ipBytesForFamily(spec.ip, spec.family))...)
	if len(spec.lladdr) > 0 {
		payload = append(payload, netlinkAttribute(ndaLLAddr, spec.lladdr)...)
	}
	msgType := uint16(syscall.RTM_DELNEIGH)
	flags := uint16(syscall.NLM_F_REQUEST | syscall.NLM_F_ACK)
	if add {
		msgType = syscall.RTM_NEWNEIGH
		flags |= syscall.NLM_F_CREATE
		if spec.replace {
			flags |= nlmFReplace
		} else {
			flags |= syscall.NLM_F_EXCL
		}
	}
	return netlinkRequest(msgType, flags, payload)
}

func showNeighbors(family int, dev string) error {
	filterIndex := 0
	if dev != "" {
		iface, err := net.InterfaceByName(dev)
		if err != nil {
			return fmt.Errorf("device %q: %w", dev, err)
		}
		filterIndex = iface.Index
	}
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETNEIGH, family)
	if err != nil {
		return err
	}
	messages, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return err
	}
	out := bufio.NewWriter(os.Stdout)
	for _, message := range messages {
		if message.Header.Type != syscall.RTM_NEWNEIGH || len(message.Data) < 12 {
			continue
		}
		entryFamily := int(message.Data[0])
		if family != syscall.AF_UNSPEC && family != entryFamily {
			continue
		}
		index := int(binary.NativeEndian.Uint32(message.Data[4:8]))
		if filterIndex != 0 && index != filterIndex {
			continue
		}
		state := binary.NativeEndian.Uint16(message.Data[8:10])
		attrs, err := parseRawNetlinkAttributes(message.Data[12:])
		if err != nil {
			return err
		}
		var address net.IP
		var hardware net.HardwareAddr
		for _, attr := range attrs {
			switch attr.typeID {
			case ndaDst:
				address = net.IP(append([]byte(nil), attr.value...))
			case ndaLLAddr:
				hardware = net.HardwareAddr(append([]byte(nil), attr.value...))
			}
		}
		iface, _ := net.InterfaceByIndex(index)
		name := strconv.Itoa(index)
		if iface != nil {
			name = iface.Name
		}
		fmt.Fprintf(out, "%s dev %s", normalizeIP(address, entryFamily), name)
		if len(hardware) > 0 {
			fmt.Fprintf(out, " lladdr %s", hardware)
		}
		fmt.Fprintf(out, " %s\n", neighborStateName(state))
	}
	return out.Flush()
}

func neighborStateName(state uint16) string {
	for name, value := range neighborStates {
		if state == value {
			return strings.ToUpper(name)
		}
	}
	return fmt.Sprintf("0x%x", state)
}

type rulePrefix struct {
	ip     net.IP
	prefix uint8
}

type ruleSpec struct {
	family   int
	from, to rulePrefix
	priority *uint32
	table    uint32
}

func ipRule(family int, args []string) error {
	if len(args) == 0 || ipMatches(args[0], "show", "list", "lst") {
		if len(args) > 1 {
			return fmt.Errorf("rule show takes no arguments")
		}
		return showRules(family)
	}
	var operation string
	switch {
	case ipMatches(args[0], "add"):
		operation = "add"
	case ipMatches(args[0], "delete"), args[0] == "remove":
		operation = "del"
	default:
		return fmt.Errorf("unknown rule command %q", args[0])
	}
	spec, err := parseRuleSpec(family, args[1:])
	if err != nil {
		return err
	}
	return changeRule(operation == "add", spec)
}

func parseRuleSpec(family int, args []string) (ruleSpec, error) {
	spec := ruleSpec{family: family, table: rtTableMain}
	for len(args) > 0 {
		if len(args) < 2 {
			return spec, fmt.Errorf("missing value after %q", args[0])
		}
		key, value := args[0], args[1]
		args = args[2:]
		switch key {
		case "from", "to":
			prefix, detected, err := parseRulePrefix(value, spec.family)
			if err != nil {
				return spec, err
			}
			if detected != syscall.AF_UNSPEC {
				spec.family = detected
			}
			if key == "from" {
				spec.from = prefix
			} else {
				spec.to = prefix
			}
		case "priority", "pref":
			parsed, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return spec, fmt.Errorf("invalid priority %q", value)
			}
			priority := uint32(parsed)
			spec.priority = &priority
		case "table", "lookup":
			table, err := parseRouteTable(value)
			if err != nil {
				return spec, err
			}
			spec.table = table
		default:
			return spec, fmt.Errorf("unknown rule option %q", key)
		}
	}
	if spec.family == syscall.AF_UNSPEC {
		spec.family = syscall.AF_INET
	}
	return spec, nil
}

func parseRulePrefix(value string, family int) (rulePrefix, int, error) {
	if value == "all" {
		return rulePrefix{}, family, nil
	}
	ip, network, err := net.ParseCIDR(value)
	if err != nil {
		ip = net.ParseIP(value)
		if ip == nil {
			return rulePrefix{}, family, fmt.Errorf("invalid rule prefix %q", value)
		}
		bits := 128
		if ip.To4() != nil {
			bits = 32
		}
		network = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
	}
	detected := familyOfIP(ip)
	if family != syscall.AF_UNSPEC && family != detected {
		return rulePrefix{}, family, fmt.Errorf("rule prefix has the wrong address family")
	}
	ones, _ := network.Mask.Size()
	return rulePrefix{ip: network.IP, prefix: uint8(ones)}, detected, nil //nolint:gosec // IP prefix sizes are at most 128.
}

func parseRouteTable(value string) (uint32, error) {
	switch value {
	case "local":
		return 255, nil
	case "main":
		return 254, nil
	case "default":
		return 253, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid route table %q", value)
	}
	return uint32(parsed), nil
}

func changeRule(add bool, spec ruleSpec) error {
	payload := make([]byte, 12, 64)
	payload[0] = byte(spec.family) //nolint:gosec // family is IPv4 or IPv6.
	payload[1], payload[2] = spec.to.prefix, spec.from.prefix
	if spec.table < 256 {
		payload[4] = byte(spec.table) //nolint:gosec // guarded by the table range check.
	}
	payload[7] = frActToTable
	if spec.from.prefix > 0 {
		payload = append(payload, netlinkAttribute(fraSrc, ipBytesForFamily(spec.from.ip, spec.family))...)
	}
	if spec.to.prefix > 0 {
		payload = append(payload, netlinkAttribute(fraDst, ipBytesForFamily(spec.to.ip, spec.family))...)
	}
	if spec.priority != nil {
		value := make([]byte, 4)
		binary.NativeEndian.PutUint32(value, *spec.priority)
		payload = append(payload, netlinkAttribute(fraPriority, value)...)
	}
	if spec.table >= 256 {
		value := make([]byte, 4)
		binary.NativeEndian.PutUint32(value, spec.table)
		payload = append(payload, netlinkAttribute(fraTable, value)...)
	}
	msgType := uint16(rtmDelRule)
	flags := uint16(syscall.NLM_F_REQUEST | syscall.NLM_F_ACK)
	if add {
		msgType = rtmNewRule
		flags |= syscall.NLM_F_CREATE | syscall.NLM_F_EXCL
	}
	return netlinkRequest(msgType, flags, payload)
}

func showRules(family int) error {
	rib, err := syscall.NetlinkRIB(rtmGetRule, family)
	if err != nil {
		return err
	}
	messages, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return err
	}
	out := bufio.NewWriter(os.Stdout)
	for _, message := range messages {
		if message.Header.Type != rtmNewRule || len(message.Data) < 12 {
			continue
		}
		entryFamily := int(message.Data[0])
		if family != syscall.AF_UNSPEC && family != entryFamily {
			continue
		}
		toPrefix, fromPrefix := message.Data[1], message.Data[2]
		table := uint32(message.Data[4])
		attrs, err := parseRawNetlinkAttributes(message.Data[12:])
		if err != nil {
			return err
		}
		var from, to net.IP
		var priority uint32
		for _, attr := range attrs {
			switch attr.typeID {
			case fraSrc:
				from = net.IP(append([]byte(nil), attr.value...))
			case fraDst:
				to = net.IP(append([]byte(nil), attr.value...))
			case fraPriority:
				if len(attr.value) >= 4 {
					priority = binary.NativeEndian.Uint32(attr.value)
				}
			case fraTable:
				if len(attr.value) >= 4 {
					table = binary.NativeEndian.Uint32(attr.value)
				}
			}
		}
		fmt.Fprintf(out, "%d:\tfrom %s", priority, formatRulePrefix(from, fromPrefix, entryFamily))
		if toPrefix > 0 {
			fmt.Fprintf(out, " to %s", formatRulePrefix(to, toPrefix, entryFamily))
		}
		fmt.Fprintf(out, " lookup %s\n", routeTableName(table))
	}
	return out.Flush()
}

func formatRulePrefix(ip net.IP, prefix byte, family int) string {
	if prefix == 0 {
		return "all"
	}
	return fmt.Sprintf("%s/%d", normalizeIP(ip, family), prefix)
}

func routeTableName(table uint32) string {
	switch table {
	case 255:
		return "local"
	case 254:
		return "main"
	case 253:
		return "default"
	default:
		return strconv.FormatUint(uint64(table), 10)
	}
}

func routeGet(family int, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: ip route get ADDRESS")
	}
	destination := net.ParseIP(args[0])
	if destination == nil {
		return fmt.Errorf("invalid destination %q", args[0])
	}
	detected := familyOfIP(destination)
	if family != syscall.AF_UNSPEC && family != detected {
		return fmt.Errorf("destination has the wrong address family")
	}
	bits := 128
	if detected == syscall.AF_INET {
		bits = 32
	}
	payload := make([]byte, 12, 64)
	payload[0] = byte(detected) //nolint:gosec // detected is IPv4 or IPv6.
	payload[1] = byte(bits)     //nolint:gosec // address widths are 32 or 128.
	payload = append(payload, netlinkAttribute(syscall.RTA_DST, ipBytesForFamily(destination, detected))...)
	messages, err := netlinkQuery(syscall.RTM_GETROUTE, syscall.NLM_F_REQUEST, payload)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if message.Header.Type == syscall.RTM_NEWROUTE && len(message.Data) >= 12 {
			return printRouteGet(message, destination, detected)
		}
	}
	return fmt.Errorf("no route to %s", destination)
}

func printRouteGet(message syscall.NetlinkMessage, destination net.IP, family int) error {
	attrs, err := syscall.ParseNetlinkRouteAttr(&message)
	if err != nil {
		return err
	}
	var gateway, preferred net.IP
	oif := 0
	for _, attr := range attrs {
		switch attr.Attr.Type {
		case syscall.RTA_GATEWAY:
			gateway = net.IP(append([]byte(nil), attr.Value...))
		case syscall.RTA_OIF:
			if len(attr.Value) >= 4 {
				oif = int(binary.NativeEndian.Uint32(attr.Value))
			}
		case rtaPrefsrc:
			preferred = net.IP(append([]byte(nil), attr.Value...))
		}
	}
	fmt.Fprint(os.Stdout, normalizeIP(destination, family))
	if gateway != nil {
		fmt.Fprintf(os.Stdout, " via %s", normalizeIP(gateway, family))
	}
	if iface, err := net.InterfaceByIndex(oif); err == nil {
		fmt.Fprintf(os.Stdout, " dev %s", iface.Name)
	}
	if preferred != nil {
		fmt.Fprintf(os.Stdout, " src %s", normalizeIP(preferred, family))
	}
	fmt.Fprintln(os.Stdout)
	return nil
}

func netlinkQuery(msgType, flags uint16, payload []byte) ([]syscall.NetlinkMessage, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, err
	}
	seq := netlinkSequence.Add(1)
	request := make([]byte, syscall.NLMSG_HDRLEN+len(payload))
	binary.NativeEndian.PutUint32(request[0:4], uint32(len(request))) //nolint:gosec // requests are far below uint32 size.
	binary.NativeEndian.PutUint16(request[4:6], msgType)
	binary.NativeEndian.PutUint16(request[6:8], flags)
	binary.NativeEndian.PutUint32(request[8:12], seq)
	copy(request[syscall.NLMSG_HDRLEN:], payload)
	if err := syscall.Sendto(fd, request, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, err
	}
	buffer := make([]byte, 64*1024)
	for {
		n, _, recvErr := syscall.Recvfrom(fd, buffer, 0)
		if errors.Is(recvErr, syscall.EINTR) {
			continue
		}
		if recvErr != nil {
			return nil, recvErr
		}
		messages, parseErr := syscall.ParseNetlinkMessage(buffer[:n])
		if parseErr != nil {
			return nil, parseErr
		}
		var matched []syscall.NetlinkMessage
		done := false
		for _, message := range messages {
			if message.Header.Seq != seq {
				continue
			}
			if message.Header.Type == syscall.NLMSG_ERROR {
				if len(message.Data) < 4 {
					return nil, fmt.Errorf("short netlink error response")
				}
				code := int32(binary.NativeEndian.Uint32(message.Data[:4])) //nolint:gosec // kernel encodes a signed errno.
				if code != 0 {
					return nil, syscall.Errno(-code) //nolint:gosec // negative kernel errno becomes positive.
				}
				continue
			}
			if message.Header.Type == syscall.NLMSG_DONE {
				done = true
			} else {
				matched = append(matched, message)
			}
		}
		if len(matched) > 0 || done {
			return matched, nil
		}
	}
}
