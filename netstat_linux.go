// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// netstatOptions is one net-tools netstat(8) command line. The socket lists,
// the routing table, and the interface table are three separate reports, so
// the display selectors are kept apart from the protocol selectors.
type netstatOptions struct {
	tcp, udp, raw, unix bool
	listening, all      bool
	numeric             bool
	programs            bool
	route, interfaces   bool
}

func cmdNetstat(args []string) int {
	var options netstatOptions
	for _, arg := range args {
		switch {
		case arg == "--tcp":
			options.tcp = true
		case arg == "--udp":
			options.udp = true
		case arg == "--raw":
			options.raw = true
		case arg == "--unix":
			options.unix = true
		case arg == "--listening":
			options.listening = true
		case arg == "--all":
			options.all = true
		case arg == "--numeric", arg == "--numeric-hosts", arg == "--numeric-ports", arg == "--numeric-users":
			options.numeric = true
		case arg == "--programs":
			options.programs = true
		case arg == "--route":
			options.route = true
		case arg == "--interfaces":
			options.interfaces = true
		case arg == "--extend", arg == "--verbose", arg == "--wide", arg == "--inet":
			// Accepted for compatibility: this netstat never truncates
			// addresses and has no extra columns to add.
		case len(arg) > 1 && arg[0] == '-':
			// Short options bundle, so -tulpn is the same as -t -u -l -p -n.
			for _, flag := range arg[1:] {
				switch flag {
				case 't':
					options.tcp = true
				case 'u':
					options.udp = true
				case 'w':
					options.raw = true
				case 'x':
					options.unix = true
				case 'l':
					options.listening = true
				case 'a':
					options.all = true
				case 'n':
					options.numeric = true
				case 'p':
					options.programs = true
				case 'r':
					options.route = true
				case 'i':
					options.interfaces = true
				case 'e', 'v', 'W':
				default:
					fatalf("netstat", "invalid option -- '%c'", flag)
					return 1
				}
			}
		default:
			fatalf("netstat", "unsupported operand %q", arg)
			return 1
		}
	}
	if options.route {
		return writeRoutingTable(options.numeric)
	}
	if options.interfaces {
		return writeInterfaceTable()
	}
	return writeSocketReport(options)
}

func writeSocketReport(options netstatOptions) int {
	if !options.tcp && !options.udp && !options.raw && !options.unix {
		options.tcp, options.udp, options.raw, options.unix = true, true, true, true
	}
	owners := map[string]string{}
	if options.programs {
		owners = socketOwners()
	}
	if options.tcp || options.udp || options.raw {
		fmt.Printf("Active Internet connections (%s)\n", netstatScope(options))
		fmt.Print("Proto Recv-Q Send-Q Local Address           Foreign Address         State      ")
		if options.programs {
			fmt.Print(" PID/Program name    ")
		}
		fmt.Println()
		if options.tcp {
			writeInternetSockets("tcp", "/proc/net/tcp", options, owners)
			writeInternetSockets("tcp6", "/proc/net/tcp6", options, owners)
		}
		if options.udp {
			writeInternetSockets("udp", "/proc/net/udp", options, owners)
			writeInternetSockets("udp6", "/proc/net/udp6", options, owners)
		}
		if options.raw {
			writeInternetSockets("raw", "/proc/net/raw", options, owners)
			writeInternetSockets("raw6", "/proc/net/raw6", options, owners)
		}
	}
	if options.unix {
		fmt.Printf("Active UNIX domain sockets (%s)\n", netstatScope(options))
		fmt.Print("Proto RefCnt Flags       Type       State         I-Node  ")
		if options.programs {
			fmt.Print(" PID/Program name    ")
		}
		fmt.Println(" Path")
		writeUnixSockets(options, owners)
	}
	return 0
}

// netstatScope names the selection in each section heading, exactly as
// net-tools does.
func netstatScope(options netstatOptions) string {
	switch {
	case options.all:
		return "servers and established"
	case options.listening:
		return "only servers"
	}
	return "w/o servers"
}

func writeInternetSockets(protocol, path string, options netstatOptions, owners map[string]string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		state := netstatSocketState(protocol, fields[3])
		// Only TCP has a listening state of its own. For the connectionless
		// protocols net-tools treats a socket with no peer as the server.
		listening := state == "LISTEN" || !strings.HasPrefix(protocol, "tcp") && strings.Trim(fields[2], "0:") == ""
		if options.listening && !listening || !options.listening && !options.all && listening {
			continue
		}
		receiveQueue, sendQueue := splitSocketQueues(fields[4])
		program := "-"
		if owner, ok := owners[fields[9]]; ok {
			program = owner
		}
		fmt.Printf("%-6s%6d %6d %-23s %-23s %-11s", protocol, receiveQueue, sendQueue,
			netstatAddress(fields[1]), netstatAddress(fields[2]), state)
		if options.programs {
			fmt.Printf(" %-19.19s ", program)
		}
		fmt.Println()
	}
}

// netstatAddress renders one address field the way netstat does: the wildcard
// address keeps its numeric form, an unset port becomes "*", and an IPv6
// address is written without brackets, so a wildcard listener reads ":::22". A
// long address is cut short rather than pushing the columns apart, which is how
// net-tools keeps its rows aligned.
func netstatAddress(value string) string {
	address, number, ok := parseProcSocketAddress(value)
	if !ok {
		return value
	}
	port := "*"
	if number != 0 {
		port = strconv.FormatUint(number, 10)
	}
	text := address.String()
	if room := netstatAddressWidth - len(port) - 1; len(text) > room && room > 0 {
		text = text[:room]
	}
	return text + ":" + port
}

// netstatAddressWidth is the width of the local and foreign address columns.
const netstatAddressWidth = 23

// netstatSocketState spells the states the way net-tools does; ss(8) and
// netstat(8) disagree on nearly every name.
func netstatSocketState(protocol, value string) string {
	if strings.HasPrefix(protocol, "raw") {
		// Raw sockets have no connection state to report, so net-tools shows
		// the protocol-independent state number itself.
		if number, err := strconv.ParseUint(value, 16, 8); err == nil {
			return strconv.FormatUint(number, 10)
		}
		return value
	}
	if strings.HasPrefix(protocol, "udp") {
		if value == "01" {
			return "ESTABLISHED"
		}
		return ""
	}
	states := map[string]string{
		"01": "ESTABLISHED", "02": "SYN_SENT", "03": "SYN_RECV", "04": "FIN_WAIT1",
		"05": "FIN_WAIT2", "06": "TIME_WAIT", "07": "CLOSE", "08": "CLOSE_WAIT",
		"09": "LAST_ACK", "0A": "LISTEN", "0B": "CLOSING",
	}
	if state, ok := states[value]; ok {
		return state
	}
	return "UNKNOWN"
}

// splitSocketQueues reads the "TXQ:RXQ" field of /proc/net. The transmit queue
// comes first there and second in the output.
func splitSocketQueues(value string) (uint64, uint64) {
	transmit, receive := value, ""
	if colon := strings.IndexByte(value, ':'); colon >= 0 {
		transmit, receive = value[:colon], value[colon+1:]
	}
	sent, _ := strconv.ParseUint(transmit, 16, 64)
	waiting, _ := strconv.ParseUint(receive, 16, 64)
	return waiting, sent
}

func writeUnixSockets(options netstatOptions, owners map[string]string) {
	data, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return
	}
	// /proc/net/unix lists: Num RefCount Protocol Flags Type St Inode Path.
	types := map[string]string{"0001": "STREAM", "0002": "DGRAM", "0005": "SEQPACKET"}
	states := map[string]string{"00": "FREE", "01": "", "02": "CONNECTING", "03": "CONNECTED", "04": "DISCONNECTING"}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		socketFlags, _ := strconv.ParseUint(fields[3], 16, 32)
		listening := socketFlags&soAcceptConn != 0
		if options.listening && !listening || !options.listening && !options.all && listening {
			continue
		}
		flags, state := "[ ]", states[fields[5]]
		if listening {
			flags, state = "[ ACC ]", "LISTENING"
		}
		references, _ := strconv.ParseUint(fields[1], 16, 64)
		socketType := types[fields[4]]
		if socketType == "" {
			socketType = "UNKNOWN"
		}
		path := ""
		if len(fields) > 7 {
			path = fields[7]
		}
		fmt.Printf("unix  %-6d %-11s %-10s %-13s %-8s", references, flags, socketType, state, fields[6])
		if options.programs {
			program := "-"
			if owner, ok := owners[fields[6]]; ok {
				program = owner
			}
			fmt.Printf(" %-20.19s", program)
		}
		fmt.Printf(" %s\n", path)
	}
}

// socketOwners maps a socket inode to the "PID/program" field. It reads the
// same /proc/PID/fd links lsof does, so only the caller's own processes are
// listed unless netstat runs as root -- which is exactly what net-tools warns
// about here.
func socketOwners() map[string]string {
	owners := map[string]string{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return owners
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "(Not all processes could be identified, non-owned process info")
		fmt.Fprintln(os.Stderr, " will not be shown, you would have to be root to see it all.)")
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		descriptors, err := os.ReadDir(filepath.Join("/proc", entry.Name(), "fd"))
		if err != nil {
			continue
		}
		name := ""
		for _, descriptor := range descriptors {
			target, err := os.Readlink(filepath.Join("/proc", entry.Name(), "fd", descriptor.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			if name == "" {
				name = processName(pid)
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if _, taken := owners[inode]; !taken {
				owners[inode] = strconv.Itoa(pid) + "/" + name
			}
		}
	}
	return owners
}

// processName is the program name net-tools shows: the last path element of
// argv[0], falling back to the kernel's own name for processes without a
// command line.
func processName(pid int) string {
	if data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline")); err == nil {
		argv0, _, _ := strings.Cut(string(data), "\x00")
		if argv0 != "" {
			return filepath.Base(argv0)
		}
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return "-"
	}
	return strings.TrimSpace(string(data))
}

// writeRoutingTable prints the IPv4 kernel routing table from /proc/net/route.
// Addresses are always numeric: the default route is the only entry net-tools
// names, and resolving the rest would need DNS.
func writeRoutingTable(numeric bool) int {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		fatalf("netstat", "%v", err)
		return 1
	}
	fmt.Println("Kernel IP routing table")
	fmt.Println("Destination     Gateway         Genmask         Flags   MSS Window  irtt Iface")
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&routeFlagUp == 0 {
			continue
		}
		destination, gateway, mask := hexIPv4(fields[1]), hexIPv4(fields[2]), hexIPv4(fields[7])
		if !numeric && destination == "0.0.0.0" && mask == "0.0.0.0" {
			destination = "default"
		}
		mtu, _ := strconv.Atoi(fields[8])
		window, _ := strconv.Atoi(fields[9])
		irtt, _ := strconv.Atoi(fields[10])
		fmt.Printf("%-15s %-15s %-15s %-6s %4d %-6d %5d %s\n",
			destination, gateway, mask, routeFlagNames(uint32(flags)), mtu, window, irtt, fields[0]) //nolint:gosec // G115: parsed with a 32-bit limit.
	}
	return 0
}

// Route flags from <linux/route.h>, in the order net-tools prints them.
const (
	routeFlagUp        = 0x0001
	routeFlagGateway   = 0x0002
	routeFlagHost      = 0x0004
	routeFlagReinstate = 0x0008
	routeFlagDynamic   = 0x0010
	routeFlagModified  = 0x0020
	routeFlagReject    = 0x0200
)

func routeFlagNames(flags uint32) string {
	names := ""
	for _, flag := range []struct {
		bit  uint32
		name string
	}{
		{routeFlagUp, "U"}, {routeFlagGateway, "G"}, {routeFlagHost, "H"},
		{routeFlagReinstate, "R"}, {routeFlagDynamic, "D"}, {routeFlagModified, "M"},
		{routeFlagReject, "!"},
	} {
		if flags&flag.bit != 0 {
			names += flag.name
		}
	}
	return names
}

// hexIPv4 decodes one address of /proc/net/route, which holds it as a
// little-endian 32-bit word written in hexadecimal.
func hexIPv4(value string) string {
	word, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return value
	}
	return fmt.Sprintf("%d.%d.%d.%d", byte(word), byte(word>>8), byte(word>>16), byte(word>>24)) //nolint:gosec // G115: each conversion takes one byte of a 32-bit word.
}

// writeInterfaceTable prints the packet counters of /proc/net/dev together with
// the MTU and flags of each interface, which come from rtnetlink because sysfs
// reports neither the running state nor the user-visible promiscuity flags.
func writeInterfaceTable() int {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		fatalf("netstat", "%v", err)
		return 1
	}
	interfaces := map[string]linkDetails{}
	links, _, err := readLinks()
	if err != nil {
		fatalf("netstat", "%v", err)
		return 1
	}
	for _, link := range links {
		interfaces[link.name] = link
	}
	fmt.Println("Kernel Interface table")
	fmt.Println("Iface             MTU    RX-OK RX-ERR RX-DRP RX-OVR    TX-OK TX-ERR TX-DRP TX-OVR Flg")
	lines := strings.Split(string(data), "\n")
	if len(lines) > 2 {
		lines = lines[2:]
	}
	rows := make([]string, 0, len(lines))
	for _, line := range lines {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		counters := strings.Fields(line[colon+1:])
		if name == "" || len(counters) < 16 {
			continue
		}
		value := func(index int) uint64 {
			number, _ := strconv.ParseUint(counters[index], 10, 64)
			return number
		}
		// /proc/net/dev counts bytes first, then packets, errors, drops, and
		// FIFO overruns, for the receive side and then the transmit side.
		link := interfaces[name]
		rows = append(rows, fmt.Sprintf("%-16s%5d%9d%7d%7d %-6d%9d%7d%7d%7d %s",
			name, link.mtu, value(1), value(2), value(3), value(4),
			value(9), value(10), value(11), value(12), interfaceFlagNames(link.flags)))
	}
	sort.Strings(rows)
	for _, row := range rows {
		fmt.Println(row)
	}
	return 0
}

// soAcceptConn is SO_ACCEPTCON in the flags column of /proc/net/unix, the bit
// that marks a socket as a listener.
const soAcceptConn = 0x10000

// Interface flags from <net/if.h>, in the order net-tools abbreviates them.
var interfaceFlagLetters = []struct {
	bit    uint32
	letter string
}{
	{0x200, "A"}, {0x2, "B"}, {0x4, "D"}, {0x8, "L"}, {0x1000, "M"},
	{0x20, "N"}, {0x80, "O"}, {0x10, "P"}, {0x800, "s"}, {0x400, "m"},
	{0x40, "R"}, {0x1, "U"},
}

func interfaceFlagNames(flags uint32) string {
	letters := ""
	for _, flag := range interfaceFlagLetters {
		if flags&flag.bit != 0 {
			letters += flag.letter
		}
	}
	return letters
}
