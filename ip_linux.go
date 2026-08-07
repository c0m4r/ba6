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
	"sync/atomic"
	"syscall"
)

const (
	rtaPrefsrc     = 7
	rtaTable       = 15
	rtTableMain    = 254
	rtScopeLink    = 253
	rtScopeNowhere = 255
	rtProtKernel   = 2
	rtProtBoot     = 3
	rtProtStatic   = 4
)

var netlinkSequence atomic.Uint32

func cmdIP(args []string) int {
	family := syscall.AF_UNSPEC
	for len(args) > 0 {
		switch args[0] {
		case "-4":
			family, args = syscall.AF_INET, args[1:]
		case "-6":
			family, args = syscall.AF_INET6, args[1:]
		case "--":
			args = args[1:]
			goto object
		default:
			goto object
		}
	}

object:
	if len(args) == 0 {
		fatalf("ip", "missing object (expected addr, link, neigh, route, or rule)")
		return 1
	}
	var err error
	switch args[0] {
	case "addr", "address", "a":
		err = ipAddress(family, args[1:])
	case "link", "l":
		err = ipLink(args[1:])
	case "neighbor", "neighbour", "neigh", "n":
		err = ipNeighbor(family, args[1:])
	case "route", "r":
		err = ipRoute(family, args[1:])
	case "rule", "ru":
		err = ipRule(family, args[1:])
	default:
		fatalf("ip", "unknown object %q", args[0])
		return 1
	}
	if err != nil {
		fatalf("ip", "%v", err)
		return 1
	}
	return 0
}

func ipAddress(family int, args []string) error {
	if len(args) == 0 || args[0] == "show" || args[0] == "list" || args[0] == "s" {
		if len(args) > 0 {
			args = args[1:]
		}
		dev, err := parseShowDev(args)
		if err != nil {
			return err
		}
		return showAddresses(family, dev)
	}
	operation := args[0]
	if operation == "remove" {
		operation = "del"
	}
	if operation != "add" && operation != "del" {
		return fmt.Errorf("unknown address command %q", args[0])
	}
	if len(args) < 4 || args[2] != "dev" || len(args) != 4 {
		return fmt.Errorf("usage: ip addr %s ADDRESS dev IFACE", operation)
	}
	ip, prefix, family, err := parseAddress(args[1], family)
	if err != nil {
		return err
	}
	iface, err := net.InterfaceByName(args[3])
	if err != nil {
		return fmt.Errorf("device %q: %w", args[3], err)
	}
	return changeAddress(operation == "add", family, ip, prefix, iface.Index)
}

func parseShowDev(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) == 2 && args[0] == "dev" {
		return args[1], nil
	}
	return "", fmt.Errorf("expected 'dev IFACE'")
}

func showAddresses(family int, dev string) error {
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	out := bufio.NewWriter(os.Stdout)
	matched := false
	for _, iface := range interfaces {
		if dev != "" && iface.Name != dev {
			continue
		}
		matched = true
		state := "DOWN"
		if iface.Flags&net.FlagUp != 0 {
			state = "UP"
		}
		fmt.Fprintf(out, "%d: %s: <%s> mtu %d state %s\n",
			iface.Index, iface.Name, interfaceFlags(iface.Flags), iface.MTU, state)
		addrs, addrErr := iface.Addrs()
		if addrErr != nil {
			return addrErr
		}
		for _, addr := range addrs {
			ip, network, parseErr := net.ParseCIDR(addr.String())
			if parseErr != nil {
				continue
			}
			currentFamily := familyOfIP(ip)
			if family != syscall.AF_UNSPEC && family != currentFamily {
				continue
			}
			kind := "inet6"
			if currentFamily == syscall.AF_INET {
				kind = "inet"
			}
			ones, _ := network.Mask.Size()
			fmt.Fprintf(out, "    %s %s/%d scope %s %s\n", kind, ip.String(), ones, ipScope(ip), iface.Name)
		}
	}
	if dev != "" && !matched {
		return fmt.Errorf("device %q does not exist", dev)
	}
	return out.Flush()
}

func interfaceFlags(flags net.Flags) string {
	var names []string
	if flags&net.FlagUp != 0 {
		names = append(names, "UP")
	}
	if flags&net.FlagBroadcast != 0 {
		names = append(names, "BROADCAST")
	}
	if flags&net.FlagLoopback != 0 {
		names = append(names, "LOOPBACK")
	}
	if flags&net.FlagPointToPoint != 0 {
		names = append(names, "POINTOPOINT")
	}
	if flags&net.FlagMulticast != 0 {
		names = append(names, "MULTICAST")
	}
	return strings.Join(names, ",")
}

func ipScope(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "host"
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return "link"
	default:
		return "global"
	}
}

func changeAddress(add bool, family int, ip net.IP, prefix uint8, index int) error {
	payload := make([]byte, 8, 32)
	payload[0] = byte(family) //nolint:gosec // G115: validated AF_INET or AF_INET6.
	payload[1] = prefix
	payload[3] = byte(syscall.RT_SCOPE_UNIVERSE)
	binary.NativeEndian.PutUint32(payload[4:], uint32(index)) //nolint:gosec // G115: kernel interface indices are nonnegative uint32 values.
	ipBytes := ipBytesForFamily(ip, family)
	payload = append(payload, netlinkAttribute(syscall.IFA_LOCAL, ipBytes)...)
	payload = append(payload, netlinkAttribute(syscall.IFA_ADDRESS, ipBytes)...)
	msgType := uint16(syscall.RTM_DELADDR)
	flags := uint16(syscall.NLM_F_REQUEST | syscall.NLM_F_ACK)
	if add {
		msgType = syscall.RTM_NEWADDR
		flags |= syscall.NLM_F_CREATE | syscall.NLM_F_EXCL
	}
	return netlinkRequest(msgType, flags, payload)
}

type routeSpec struct {
	family int
	dst    net.IP
	prefix uint8
	via    net.IP
	dev    string
	metric *uint32
}

func ipRoute(family int, args []string) error {
	if len(args) == 0 || args[0] == "show" || args[0] == "list" {
		if len(args) > 0 {
			args = args[1:]
		}
		dev, err := parseShowDev(args)
		if err != nil {
			return err
		}
		return showRoutes(family, dev)
	}
	if args[0] == "get" {
		return routeGet(family, args[1:])
	}
	operation := args[0]
	if operation == "remove" {
		operation = "del"
	}
	if operation != "add" && operation != "del" {
		return fmt.Errorf("unknown route command %q", args[0])
	}
	spec, err := parseRouteSpec(family, args[1:])
	if err != nil {
		return err
	}
	if operation == "add" && spec.via == nil && spec.dev == "" {
		return fmt.Errorf("route add requires via GATEWAY or dev IFACE")
	}
	return changeRoute(operation == "add", spec)
}

func parseRouteSpec(family int, args []string) (routeSpec, error) {
	var spec routeSpec
	if len(args) == 0 {
		return spec, fmt.Errorf("missing route prefix")
	}
	destination := args[0]
	args = args[1:]
	for len(args) > 0 {
		if len(args) < 2 {
			return spec, fmt.Errorf("missing value after %q", args[0])
		}
		key, value := args[0], args[1]
		args = args[2:]
		switch key {
		case "via":
			if spec.via != nil {
				return spec, fmt.Errorf("duplicate via")
			}
			spec.via = net.ParseIP(value)
			if spec.via == nil {
				return spec, fmt.Errorf("invalid gateway %q", value)
			}
		case "dev":
			if spec.dev != "" {
				return spec, fmt.Errorf("duplicate dev")
			}
			spec.dev = value
		case "metric":
			v, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return spec, fmt.Errorf("invalid metric %q", value)
			}
			metric := uint32(v)
			spec.metric = &metric
		default:
			return spec, fmt.Errorf("unknown route option %q", key)
		}
	}
	if family == syscall.AF_UNSPEC && spec.via != nil {
		family = familyOfIP(spec.via)
	}
	if destination == "default" {
		if family == syscall.AF_UNSPEC {
			family = syscall.AF_INET
		}
		spec.family, spec.prefix = family, 0
		if family == syscall.AF_INET {
			spec.dst = net.IPv4zero
		} else {
			spec.dst = net.IPv6zero
		}
	} else {
		ip, network, err := net.ParseCIDR(destination)
		if err != nil {
			return spec, fmt.Errorf("invalid route prefix %q", destination)
		}
		detected := familyOfIP(ip)
		if family != syscall.AF_UNSPEC && family != detected {
			return spec, fmt.Errorf("route prefix has the wrong address family")
		}
		ones, _ := network.Mask.Size()
		spec.family, spec.prefix, spec.dst = detected, uint8(ones), network.IP //nolint:gosec // G115: IP mask sizes are 0..128.
	}
	if spec.via != nil && familyOfIP(spec.via) != spec.family {
		return spec, fmt.Errorf("gateway has the wrong address family")
	}
	return spec, nil
}

func changeRoute(add bool, spec routeSpec) error {
	payload := make([]byte, 12)
	payload[0] = byte(spec.family) //nolint:gosec // G115: parser restricts this to AF_INET or AF_INET6.
	payload[1] = spec.prefix
	payload[4] = rtTableMain
	if add {
		payload[5] = rtProtBoot
		payload[6] = syscall.RT_SCOPE_UNIVERSE
		payload[7] = syscall.RTN_UNICAST
		if spec.via == nil {
			payload[6] = rtScopeLink
		}
	} else {
		payload[6] = rtScopeNowhere
	}
	if spec.prefix > 0 {
		payload = append(payload, netlinkAttribute(syscall.RTA_DST, ipBytesForFamily(spec.dst, spec.family))...)
	}
	if spec.via != nil {
		payload = append(payload, netlinkAttribute(syscall.RTA_GATEWAY, ipBytesForFamily(spec.via, spec.family))...)
	}
	if spec.dev != "" {
		iface, err := net.InterfaceByName(spec.dev)
		if err != nil {
			return fmt.Errorf("device %q: %w", spec.dev, err)
		}
		value := make([]byte, 4)
		binary.NativeEndian.PutUint32(value, uint32(iface.Index)) //nolint:gosec // G115: kernel interface indices are nonnegative uint32 values.
		payload = append(payload, netlinkAttribute(syscall.RTA_OIF, value)...)
	}
	if spec.metric != nil {
		value := make([]byte, 4)
		binary.NativeEndian.PutUint32(value, *spec.metric)
		payload = append(payload, netlinkAttribute(syscall.RTA_PRIORITY, value)...)
	}
	msgType := uint16(syscall.RTM_DELROUTE)
	flags := uint16(syscall.NLM_F_REQUEST | syscall.NLM_F_ACK)
	if add {
		msgType = syscall.RTM_NEWROUTE
		flags |= syscall.NLM_F_CREATE | syscall.NLM_F_EXCL
	}
	return netlinkRequest(msgType, flags, payload)
}

func showRoutes(family int, dev string) error {
	filterIndex := 0
	if dev != "" {
		iface, err := net.InterfaceByName(dev)
		if err != nil {
			return fmt.Errorf("device %q: %w", dev, err)
		}
		filterIndex = iface.Index
	}
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, family)
	if err != nil {
		return err
	}
	messages, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return err
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	names := make(map[int]string, len(interfaces))
	for _, iface := range interfaces {
		names[iface.Index] = iface.Name
	}
	out := bufio.NewWriter(os.Stdout)
	for _, message := range messages {
		if message.Header.Type != syscall.RTM_NEWROUTE || len(message.Data) < 12 {
			continue
		}
		routeFamily := int(message.Data[0])
		if family != syscall.AF_UNSPEC && routeFamily != family {
			continue
		}
		prefix := message.Data[1]
		table := uint32(message.Data[4])
		protocol, scope, routeType := message.Data[5], message.Data[6], message.Data[7]
		if routeType != syscall.RTN_UNICAST {
			continue
		}
		attrs, attrErr := syscall.ParseNetlinkRouteAttr(&message)
		if attrErr != nil {
			return attrErr
		}
		var dst, gateway, preferred net.IP
		oif := 0
		var metric *uint32
		for _, attr := range attrs {
			switch attr.Attr.Type {
			case syscall.RTA_DST:
				dst = net.IP(append([]byte(nil), attr.Value...))
			case syscall.RTA_GATEWAY:
				gateway = net.IP(append([]byte(nil), attr.Value...))
			case syscall.RTA_OIF:
				if len(attr.Value) >= 4 {
					oif = int(binary.NativeEndian.Uint32(attr.Value))
				}
			case syscall.RTA_PRIORITY:
				if len(attr.Value) >= 4 {
					v := binary.NativeEndian.Uint32(attr.Value)
					metric = &v
				}
			case rtaPrefsrc:
				preferred = net.IP(append([]byte(nil), attr.Value...))
			case rtaTable:
				if len(attr.Value) >= 4 {
					table = binary.NativeEndian.Uint32(attr.Value)
				} else if len(attr.Value) > 0 {
					table = uint32(attr.Value[0])
				}
			}
		}
		if table != rtTableMain || filterIndex != 0 && oif != filterIndex {
			continue
		}
		if prefix == 0 {
			fmt.Fprint(out, "default")
		} else {
			fmt.Fprintf(out, "%s/%d", normalizeIP(dst, routeFamily), prefix)
		}
		if gateway != nil {
			fmt.Fprintf(out, " via %s", normalizeIP(gateway, routeFamily))
		}
		if name := names[oif]; name != "" {
			fmt.Fprintf(out, " dev %s", name)
		}
		if protocolName := routeProtocolName(protocol); protocolName != "" {
			fmt.Fprintf(out, " proto %s", protocolName)
		}
		if scope == rtScopeLink {
			fmt.Fprint(out, " scope link")
		}
		if preferred != nil {
			fmt.Fprintf(out, " src %s", normalizeIP(preferred, routeFamily))
		}
		if metric != nil {
			fmt.Fprintf(out, " metric %d", *metric)
		}
		fmt.Fprintln(out)
	}
	return out.Flush()
}

func routeProtocolName(protocol byte) string {
	switch protocol {
	case rtProtKernel:
		return "kernel"
	case rtProtBoot:
		return "boot"
	case rtProtStatic:
		return "static"
	case 9:
		return "ra"
	case 16:
		return "dhcp"
	default:
		if protocol == 0 {
			return ""
		}
		return strconv.Itoa(int(protocol))
	}
}

func parseAddress(value string, family int) (net.IP, uint8, int, error) {
	var ip net.IP
	prefix := 0
	if strings.Contains(value, "/") {
		parsed, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("invalid address %q", value)
		}
		ip = parsed
		prefix, _ = network.Mask.Size()
	} else {
		ip = net.ParseIP(value)
		if ip == nil {
			return nil, 0, 0, fmt.Errorf("invalid address %q", value)
		}
		if ip.To4() != nil {
			prefix = 32
		} else {
			prefix = 128
		}
	}
	detected := familyOfIP(ip)
	if family != syscall.AF_UNSPEC && family != detected {
		return nil, 0, 0, fmt.Errorf("address has the wrong address family")
	}
	return ip, uint8(prefix), detected, nil
}

func familyOfIP(ip net.IP) int {
	if ip.To4() != nil {
		return syscall.AF_INET
	}
	return syscall.AF_INET6
}

func ipBytesForFamily(ip net.IP, family int) []byte {
	if family == syscall.AF_INET {
		return append([]byte(nil), ip.To4()...)
	}
	return append([]byte(nil), ip.To16()...)
}

func normalizeIP(ip net.IP, family int) net.IP {
	if family == syscall.AF_INET {
		return ip.To4()
	}
	return ip.To16()
}

func netlinkAttribute(attrType uint16, value []byte) []byte {
	length := 4 + len(value)
	aligned := (length + 3) &^ 3
	attr := make([]byte, aligned)
	binary.NativeEndian.PutUint16(attr[0:2], uint16(length)) //nolint:gosec // G115: callers provide fixed address/scalar attributes under 20 bytes.
	binary.NativeEndian.PutUint16(attr[2:4], attrType)
	copy(attr[4:], value)
	return attr
}

func netlinkRequest(msgType, flags uint16, payload []byte) error {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return err
	}
	seq := netlinkSequence.Add(1)
	message := make([]byte, syscall.NLMSG_HDRLEN+len(payload))
	binary.NativeEndian.PutUint32(message[0:4], uint32(len(message))) //nolint:gosec // G115: requests are fixed and far below uint32 size.
	binary.NativeEndian.PutUint16(message[4:6], msgType)
	binary.NativeEndian.PutUint16(message[6:8], flags)
	binary.NativeEndian.PutUint32(message[8:12], seq)
	copy(message[syscall.NLMSG_HDRLEN:], payload)
	if err := syscall.Sendto(fd, message, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 64*1024)
	for {
		n, _, recvErr := syscall.Recvfrom(fd, buf, 0)
		if errors.Is(recvErr, syscall.EINTR) {
			continue
		}
		if recvErr != nil {
			return recvErr
		}
		messages, parseErr := syscall.ParseNetlinkMessage(buf[:n])
		if parseErr != nil {
			return parseErr
		}
		for _, message := range messages {
			if message.Header.Seq != seq {
				continue
			}
			switch message.Header.Type {
			case syscall.NLMSG_ERROR:
				if len(message.Data) < 4 {
					return fmt.Errorf("short netlink error response")
				}
				code := int32(binary.NativeEndian.Uint32(message.Data[:4])) //nolint:gosec // G115: NLMSG_ERROR encodes a signed int32.
				if code == 0 {
					return nil
				}
				return syscall.Errno(-code) //nolint:gosec // G115: a negative kernel int32 is converted to its positive errno.
			case syscall.NLMSG_DONE:
				return nil
			}
		}
	}
}
