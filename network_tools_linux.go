// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func cmdNslookup(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fatalf("nslookup", "expected NAME [SERVER]")
		return 1
	}
	name := args[0]
	resolver := net.DefaultResolver
	if len(args) == 2 {
		server := args[1]
		if !strings.Contains(server, ":") {
			server = net.JoinHostPort(server, "53")
		}
		resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp", server)
		}}
	}
	ctx, cancel := timeoutContext(5 * time.Second)
	defer cancel()
	addresses, err := resolver.LookupHost(ctx, name)
	if err != nil {
		fatalf("nslookup", "%v", err)
		return 1
	}
	fmt.Printf("Name:\t%s\n", name)
	for _, address := range addresses {
		fmt.Printf("Address:\t%s\n", address)
	}
	return 0
}

func cmdPing(args []string) int {
	count, timeout, interval := 4, time.Second, time.Second
	family := 0
	host := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-4":
			if family == 6 {
				fatalf("ping", "-4 and -6 are mutually exclusive")
				return 2
			}
			family = 4
		case "-6":
			if family == 4 {
				fatalf("ping", "-4 and -6 are mutually exclusive")
				return 2
			}
			family = 6
		case "-c":
			i++
			if i >= len(args) {
				return 2
			}
			var err error
			count, err = strconv.Atoi(args[i])
			if err != nil || count < 1 {
				fatalf("ping", "invalid count %q", args[i])
				return 2
			}
		case "-W":
			i++
			if i >= len(args) {
				return 2
			}
			n, parseErr := strconv.ParseFloat(args[i], 64)
			if parseErr != nil || n <= 0 {
				fatalf("ping", "invalid timeout %q", args[i])
				return 2
			}
			timeout = time.Duration(n * float64(time.Second))
		case "-i":
			i++
			if i >= len(args) {
				return 2
			}
			n, parseErr := strconv.ParseFloat(args[i], 64)
			if parseErr != nil || n < 0 {
				fatalf("ping", "invalid interval %q", args[i])
				return 2
			}
			interval = time.Duration(n * float64(time.Second))
		default:
			if strings.HasPrefix(args[i], "-") {
				fatalf("ping", "unsupported option %q", args[i])
				return 2
			}
			host = args[i]
		}
	}
	if host == "" || count < 1 {
		fatalf("ping", "missing or invalid host")
		return 2
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		fatalf("ping", "%v", err)
		return 2
	}
	var ip net.IP
	for _, candidate := range ips {
		if family != 6 && candidate.To4() != nil {
			ip, family = candidate.To4(), 4
			break
		}
		if family != 4 && candidate.To4() == nil && candidate.To16() != nil {
			ip, family = candidate.To16(), 6
			break
		}
	}
	if ip == nil {
		fatalf("ping", "no address found for requested family")
		return 2
	}
	network, bindAddress, requestType, replyType := "ip4:icmp", "0.0.0.0", byte(8), byte(0)
	if family == 6 {
		network, bindAddress, requestType, replyType = "ip6:ipv6-icmp", "::", 128, 129
	}
	conn, err := net.ListenPacket(network, bindAddress)
	if err != nil {
		fatalf("ping", "%v", err)
		return 2
	}
	defer conn.Close()
	id := os.Getpid() & 0xffff
	received := 0
	var total time.Duration
	fmt.Printf("PING %s (%s): 56 data bytes\n", host, ip)
	for seq := 1; seq <= count; seq++ {
		packet := make([]byte, 64)
		packet[0] = requestType
		binary.BigEndian.PutUint16(packet[4:6], uint16(id))
		binary.BigEndian.PutUint16(packet[6:8], uint16(seq))
		binary.BigEndian.PutUint64(packet[8:16], uint64(time.Now().UnixNano()))
		if family == 4 {
			binary.BigEndian.PutUint16(packet[2:4], icmpChecksum(packet))
		}
		start := time.Now()
		_ = conn.SetDeadline(start.Add(timeout))
		_, err = conn.WriteTo(packet, &net.IPAddr{IP: ip})
		if err == nil {
			buf := make([]byte, 1500)
			for {
				n, from, e := conn.ReadFrom(buf)
				if e != nil {
					break
				}
				offset, matches := matchPingReply(buf[:n], family, replyType, id, seq)
				if matches {
					elapsed := time.Since(start)
					received++
					total += elapsed
					fmt.Printf("%d bytes from %s: icmp_seq=%d time=%.3f ms\n", n-offset, from, seq, float64(elapsed.Microseconds())/1000)
					break
				}
			}
		}
		if seq < count {
			time.Sleep(interval)
		}
	}
	loss := 100 * (count - received) / count
	fmt.Printf("--- %s ping statistics ---\n%d packets transmitted, %d received, %d%% packet loss\n", host, count, received, loss)
	if received > 0 {
		fmt.Printf("round-trip avg = %.3f ms\n", float64(total.Microseconds())/1000/float64(received))
		return 0
	}
	return 1
}

func matchPingReply(packet []byte, family int, replyType byte, id, sequence int) (int, bool) {
	offset := 0
	if family == 4 && len(packet) >= 20 && packet[0]>>4 == 4 {
		offset = int(packet[0]&15) * 4
	}
	if len(packet) < offset+8 || packet[offset] != replyType {
		return offset, false
	}
	return offset, int(binary.BigEndian.Uint16(packet[offset+4:offset+6])) == id &&
		int(binary.BigEndian.Uint16(packet[offset+6:offset+8])) == sequence
}

func icmpChecksum(data []byte) uint16 {
	sum := uint32(0)
	for len(data) > 1 {
		sum += uint32(binary.BigEndian.Uint16(data))
		data = data[2:]
	}
	if len(data) > 0 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func cmdSs(args []string) int {
	tcp, udp, unix, listen, all := false, false, false, false, false
	ipv4, ipv6, summary, noHeader, noQueues, processes := false, false, false, false, false, false

	applyFamily := func(fam string) bool {
		switch strings.ToLower(fam) {
		case "inet", "4":
			ipv4 = true
		case "inet6", "6":
			ipv6 = true
		case "unix":
			unix = true
		case "tcp":
			tcp = true
		case "udp":
			udp = true
		default:
			fatalf("ss", "unsupported family %q", fam)
			return false
		}
		return true
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help":
			_ = writeAppletHelp(os.Stdout, "ss")
			return 0
		case a == "--version":
			fmt.Fprintln(os.Stdout, "ss from ba6")
			return 0
		case a == "-s" || a == "--summary":
			summary = true
		case a == "-a" || a == "--all":
			all = true
		case a == "-l" || a == "--listening":
			listen = true
		case a == "-t" || a == "--tcp":
			tcp = true
		case a == "-u" || a == "--udp":
			udp = true
		case a == "-x" || a == "--unix":
			unix = true
		case a == "-4" || a == "--ipv4":
			ipv4 = true
		case a == "-6" || a == "--ipv6":
			ipv6 = true
		case a == "-H" || a == "--no-header":
			noHeader = true
		case a == "-Q" || a == "--no-queues":
			noQueues = true
		case a == "-n" || a == "--numeric":
			// Numeric by default.
		case a == "-p" || a == "--processes":
			processes = true
		case a == "-e" || a == "--extended" || a == "-o" || a == "--options" ||
			a == "-m" || a == "--memory" || a == "-i" || a == "--info":
			// Accepted for compatibility.
		case a == "-f" || a == "--family":
			i++
			if i >= len(args) {
				fatalf("ss", "option %s requires an argument", a)
				return 1
			}
			if !applyFamily(args[i]) {
				return 1
			}
		case strings.HasPrefix(a, "--family="):
			if !applyFamily(strings.TrimPrefix(a, "--family=")) {
				return 1
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'h':
					_ = writeAppletHelp(os.Stdout, "ss")
					return 0
				case 'V':
					fmt.Fprintln(os.Stdout, "ss from ba6")
					return 0
				case 's':
					summary = true
				case 'a':
					all = true
				case 'l':
					listen = true
				case 't':
					tcp = true
				case 'u':
					udp = true
				case 'x':
					unix = true
				case '4':
					ipv4 = true
				case '6':
					ipv6 = true
				case 'H':
					noHeader = true
				case 'Q':
					noQueues = true
				case 'n':
					// Numeric addresses.
				case 'p':
					processes = true
				case 'e', 'o', 'm', 'i':
					// Accepted for compatibility.
				case 'f':
					fam := a[j+1:]
					if fam == "" {
						i++
						if i >= len(args) {
							fatalf("ss", "option -f requires an argument")
							return 1
						}
						fam = args[i]
					}
					if !applyFamily(fam) {
						return 1
					}
					j = len(a)
				default:
					fatalf("ss", "unsupported option -%c", a[j])
					return 1
				}
			}
		default:
			fatalf("ss", "unsupported operand %q", a)
			return 1
		}
	}

	if summary {
		printSsSummary()
		return 0
	}

	if !tcp && !udp && !unix {
		tcp, udp = true, true
		if !ipv4 && !ipv6 {
			unix = true
		}
	} else if (ipv4 || ipv6) && unix && !tcp && !udp {
		tcp, udp = true, true
	}

	showIpv4 := true
	showIpv6 := true
	if ipv4 && !ipv6 {
		showIpv6 = false
	} else if ipv6 && !ipv4 {
		showIpv4 = false
	}

	showNetid := unix || (tcp && udp)

	var owners map[string]string
	if processes {
		owners = findSocketOwners()
	}

	if !noHeader {
		var header []string
		if showNetid {
			header = append(header, fmt.Sprintf("%-7s", "Netid"))
		}
		header = append(header, fmt.Sprintf("%-8s", "State"))
		if !noQueues {
			header = append(header, fmt.Sprintf("%-8s %-8s", "Recv-Q", "Send-Q"))
		}
		header = append(header, fmt.Sprintf("%-24s %s", "Local Address:Port", "Peer Address:Port"))
		if processes {
			header = append(header, "Process")
		}
		fmt.Println(strings.Join(header, " "))
	}

	diagMap := map[uint64]diagSocketInfo{}
	if tcp || udp {
		diagMap = querySocketDiag()
	}
	if tcp {
		if showIpv4 {
			readSocketTable("tcp", "/proc/net/tcp", listen, all, diagMap, showNetid, noQueues, owners)
		}
		if showIpv6 {
			readSocketTable("tcp", "/proc/net/tcp6", listen, all, diagMap, showNetid, noQueues, owners)
		}
	}
	if udp {
		if showIpv4 {
			readSocketTable("udp", "/proc/net/udp", listen, all, diagMap, showNetid, noQueues, owners)
		}
		if showIpv6 {
			readSocketTable("udp", "/proc/net/udp6", listen, all, diagMap, showNetid, noQueues, owners)
		}
	}
	if unix {
		readUnixSockets(listen, all, showNetid, noQueues, owners)
	}
	return 0
}

func readSocketTable(netid, path string, listen, all bool, diagMap map[uint64]diagSocketInfo, showNetid, noQueues bool, owners map[string]string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		state := socketState(f[3])
		// A UDP socket with no peer is this table's equivalent of a listener.
		// The remote field is eight hex digits for IPv4 and thirty-two for
		// IPv6, so test what it holds rather than matching one spelling of it.
		isListen := state == "LISTEN" || netid == "udp" && strings.Trim(f[2], "0:") == ""
		if listen && !isListen || !listen && !all && isListen {
			continue
		}
		bound := false
		var recvQ, sendQ uint32
		var inode uint64
		if len(f) > 9 {
			if parsedInode, convErr := strconv.ParseUint(f[9], 10, 64); convErr == nil {
				inode = parsedInode
				if diag, ok := diagMap[inode]; ok {
					bound = diag.v6only
					recvQ = diag.recvQ
					sendQ = diag.sendQ
				}
			}
		}
		if recvQ == 0 && sendQ == 0 && len(f) > 4 {
			parts := strings.Split(f[4], ":")
			if len(parts) == 2 {
				if tx, err := strconv.ParseUint(parts[0], 16, 32); err == nil {
					sendQ = uint32(tx) //nolint:gosec // hex from /proc is 32-bit
				}
				if rx, err := strconv.ParseUint(parts[1], 16, 32); err == nil {
					recvQ = uint32(rx) //nolint:gosec // hex from /proc is 32-bit
				}
			}
		}
		localStr := decodeSocketAddrMode(f[1], bound)
		peerStr := decodeSocketAddrMode(f[2], bound)
		var row []string
		if showNetid {
			row = append(row, fmt.Sprintf("%-7s", netid))
		}
		row = append(row, fmt.Sprintf("%-8s", state))
		if !noQueues {
			row = append(row, fmt.Sprintf("%-8d %-8d", recvQ, sendQ))
		}
		row = append(row, fmt.Sprintf("%-24s %s", localStr, peerStr))
		if owners != nil {
			if proc, ok := owners[strconv.FormatUint(inode, 10)]; ok {
				row = append(row, "users:("+proc+")")
			}
		}
		fmt.Println(strings.Join(row, " "))
	}
}

type diagSocketInfo struct {
	v6only bool
	recvQ  uint32
	sendQ  uint32
}

// Linux sock_diag numbers.
const (
	netlinkSockDiag  = 4  // NETLINK_SOCK_DIAG
	sockDiagByFamily = 20 // SOCK_DIAG_BY_FAMILY
	inetDiagSKV6Only = 11 // INET_DIAG_SKV6ONLY
	inetDiagMsgLen   = 72 // sizeof(struct inet_diag_msg)
	inetDiagReqLen   = 56 // sizeof(struct inet_diag_req_v2)
)

// querySocketDiag returns socket statistics (send/receive queues, v6only) for
// IPv4 and IPv6 TCP and UDP sockets from netlink sock_diag.
func querySocketDiag() map[uint64]diagSocketInfo {
	found := map[uint64]diagSocketInfo{}
	for _, family := range []byte{syscall.AF_INET, syscall.AF_INET6} {
		for _, protocol := range []byte{syscall.IPPROTO_TCP, syscall.IPPROTO_UDP} {
			messages, err := sockDiagDump(family, protocol)
			if err != nil {
				continue
			}
			for _, message := range messages {
				if inode, rq, wq, only, ok := parseInetDiag(message.Data); ok {
					found[inode] = diagSocketInfo{
						v6only: only,
						recvQ:  rq,
						sendQ:  wq,
					}
				}
			}
		}
	}
	return found
}

// sockDiagDump asks for every socket of one family and protocol.
func sockDiagDump(family, protocol byte) ([]syscall.NetlinkMessage, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, netlinkSockDiag)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, err
	}
	// struct inet_diag_req_v2: family, protocol, ext, pad, then the state mask.
	// The 48-byte socket id that follows stays zeroed, which means "any".
	payload := make([]byte, inetDiagReqLen)
	payload[0], payload[1] = family, protocol
	binary.NativeEndian.PutUint32(payload[4:8], 0xffffffff)
	seq := netlinkSequence.Add(1)
	request := make([]byte, syscall.NLMSG_HDRLEN+len(payload))
	binary.NativeEndian.PutUint32(request[0:4], uint32(len(request))) //nolint:gosec // the request is 72 bytes.
	binary.NativeEndian.PutUint16(request[4:6], sockDiagByFamily)
	binary.NativeEndian.PutUint16(request[6:8], syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP)
	binary.NativeEndian.PutUint32(request[8:12], seq)
	copy(request[syscall.NLMSG_HDRLEN:], payload)
	if err := syscall.Sendto(fd, request, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, err
	}
	var collected []syscall.NetlinkMessage
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
		for _, message := range messages {
			if message.Header.Seq != seq {
				continue
			}
			switch message.Header.Type {
			case syscall.NLMSG_DONE:
				return collected, nil
			case syscall.NLMSG_ERROR:
				return nil, errors.New("sock_diag is unavailable")
			default:
				collected = append(collected, message)
			}
		}
	}
}

// parseInetDiag extracts the socket inode, rqueue, wqueue, and v6only flag from one
// inet_diag_msg. The fixed part is 72 bytes and the flag follows as a netlink attribute.
func parseInetDiag(data []byte) (uint64, uint32, uint32, bool, bool) {
	if len(data) < inetDiagMsgLen {
		return 0, 0, 0, false, false
	}
	rqueue := binary.NativeEndian.Uint32(data[56:60])
	wqueue := binary.NativeEndian.Uint32(data[60:64])
	inode := uint64(binary.NativeEndian.Uint32(data[68:72]))
	v6only := false
	for offset := inetDiagMsgLen; offset+4 <= len(data); {
		length := int(binary.NativeEndian.Uint16(data[offset : offset+2]))
		kind := binary.NativeEndian.Uint16(data[offset+2 : offset+4])
		if length < 4 || offset+length > len(data) {
			break
		}
		if kind == inetDiagSKV6Only && length >= 5 && offset+4 < len(data) {
			v6only = data[offset+4] != 0
		}
		offset += (length + 3) &^ 3
	}
	return inode, rqueue, wqueue, v6only, true
}
func socketState(s string) string {
	states := map[string]string{"01": "ESTAB", "02": "SYN-SENT", "03": "SYN-RECV", "04": "FIN-WAIT-1", "05": "FIN-WAIT-2", "06": "TIME-WAIT", "07": "UNCONN", "08": "CLOSE-WAIT", "09": "LAST-ACK", "0A": "LISTEN", "0B": "CLOSING"}
	if v := states[s]; v != "" {
		return v
	}
	return s
}

// decodeSocketAddr renders one ADDRESS:PORT field from /proc/net the way ss
// prints it. Both the IPv4 and IPv6 tables store the address as little-endian
// 32-bit words, so each group of eight hex digits has to be put back into
// network byte order; printing the digits as they appear gives an address that
// is not merely unformatted but wrong.
func decodeSocketAddr(value string) string {
	return decodeSocketAddrMode(value, false)
}

// parseProcSocketAddress splits one "ADDRESS:PORT" field of /proc/net into its
// two parts. Both the IPv4 and IPv6 tables store the address as little-endian
// 32-bit words, so each group of eight hex digits has to be put back into
// network byte order; reading the digits as they appear gives an address that
// is not merely unformatted but wrong.
func parseProcSocketAddress(value string) (net.IP, uint64, bool) {
	separator := strings.LastIndexByte(value, ':')
	if separator < 0 {
		return nil, 0, false
	}
	digits := value[:separator]
	if len(digits)%8 != 0 || len(digits) == 0 {
		return nil, 0, false
	}
	address := make(net.IP, 0, len(digits)/2)
	for start := 0; start < len(digits); start += 8 {
		word, err := strconv.ParseUint(digits[start:start+8], 16, 32)
		if err != nil {
			return nil, 0, false
		}
		//nolint:gosec // The word is 32 bits and each conversion takes one byte of it.
		address = append(address, byte(word), byte(word>>8), byte(word>>16), byte(word>>24))
	}
	port, err := strconv.ParseUint(value[separator+1:], 16, 16)
	if err != nil {
		return nil, 0, false
	}
	return address, port, true
}

// decodeSocketAddrMode renders an address field, showing the IPv6 wildcard as
// "[::]" when the socket is v6-only and as "*" when it also accepts IPv4.
func decodeSocketAddrMode(value string, v6only bool) string {
	address, number, ok := parseProcSocketAddress(value)
	if !ok {
		return value
	}
	// An unset port is a wildcard, and so is an unspecified IPv6 address.
	port := "*"
	if number != 0 {
		port = strconv.FormatUint(number, 10)
	}
	switch {
	case len(address) == net.IPv6len && address.IsUnspecified() && v6only:
		return "[::]:" + port
	case len(address) == net.IPv6len && address.IsUnspecified():
		return "*:" + port
	case len(address) == net.IPv6len:
		return "[" + address.String() + "]:" + port
	default:
		return address.String() + ":" + port
	}
}
func readUnixSockets(listen, all, showNetid, noQueues bool, owners map[string]string) {
	data, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		netid := "u_str"
		if len(f) > 4 {
			switch f[4] {
			case "0002":
				netid = "u_dgr"
			case "0005":
				netid = "u_seq"
			}
		}
		state := "UNCONN"
		switch f[5] {
		case "01":
			state = "LISTEN"
		case "03":
			state = "ESTAB"
		}
		if listen && state != "LISTEN" || !listen && !all && state == "LISTEN" {
			continue
		}
		path := "*"
		if len(f) > 7 && f[7] != "" {
			path = f[7]
		}
		localAddr := path + " " + f[6]
		peerAddr := "* 0"
		var row []string
		if showNetid {
			row = append(row, fmt.Sprintf("%-7s", netid))
		}
		row = append(row, fmt.Sprintf("%-8s", state))
		if !noQueues {
			row = append(row, fmt.Sprintf("%-8d %-8d", 0, 0))
		}
		row = append(row, fmt.Sprintf("%-24s %s", localAddr, peerAddr))
		if owners != nil {
			if proc, ok := owners[f[6]]; ok {
				row = append(row, "users:("+proc+")")
			}
		}
		fmt.Println(strings.Join(row, " "))
	}
}

func findSocketOwners() map[string]string {
	owners := map[string]string{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return owners
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", entry.Name(), "fd"))
		if err != nil {
			continue
		}
		comm := ""
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join("/proc", entry.Name(), "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			if comm == "" {
				if data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm")); err == nil {
					comm = strings.TrimSpace(string(data))
				}
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			userEntry := fmt.Sprintf(`("%s",pid=%d,fd=%s)`, comm, pid, fd.Name())
			if existing, exists := owners[inode]; exists {
				owners[inode] = existing + "," + userEntry
			} else {
				owners[inode] = userEntry
			}
		}
	}
	return owners
}

func printSsSummary() {
	var totalSockets, tcpInuse, tcpOrphan, tcpTw, tcpAlloc, udpInuse, rawInuse, fragInuse int
	var tcp6Inuse, udp6Inuse, raw6Inuse, frag6Inuse int
	var currEstab int

	if data, err := os.ReadFile("/proc/net/sockstat"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "sockets:":
				for i := 1; i+1 < len(fields); i += 2 {
					if fields[i] == "used" {
						totalSockets, _ = strconv.Atoi(fields[i+1])
					}
				}
			case "TCP:":
				for i := 1; i+1 < len(fields); i += 2 {
					switch fields[i] {
					case "inuse":
						tcpInuse, _ = strconv.Atoi(fields[i+1])
					case "orphan":
						tcpOrphan, _ = strconv.Atoi(fields[i+1])
					case "tw":
						tcpTw, _ = strconv.Atoi(fields[i+1])
					case "alloc":
						tcpAlloc, _ = strconv.Atoi(fields[i+1])
					}
				}
			case "UDP:":
				for i := 1; i+1 < len(fields); i += 2 {
					if fields[i] == "inuse" {
						udpInuse, _ = strconv.Atoi(fields[i+1])
					}
				}
			case "RAW:":
				for i := 1; i+1 < len(fields); i += 2 {
					if fields[i] == "inuse" {
						rawInuse, _ = strconv.Atoi(fields[i+1])
					}
				}
			case "FRAG:":
				for i := 1; i+1 < len(fields); i += 2 {
					if fields[i] == "inuse" {
						fragInuse, _ = strconv.Atoi(fields[i+1])
					}
				}
			}
		}
	}

	if data, err := os.ReadFile("/proc/net/sockstat6"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "TCP6:":
				for i := 1; i+1 < len(fields); i += 2 {
					if fields[i] == "inuse" {
						tcp6Inuse, _ = strconv.Atoi(fields[i+1])
					}
				}
			case "UDP6:":
				for i := 1; i+1 < len(fields); i += 2 {
					if fields[i] == "inuse" {
						udp6Inuse, _ = strconv.Atoi(fields[i+1])
					}
				}
			case "RAW6:":
				for i := 1; i+1 < len(fields); i += 2 {
					if fields[i] == "inuse" {
						raw6Inuse, _ = strconv.Atoi(fields[i+1])
					}
				}
			case "FRAG6:":
				for i := 1; i+1 < len(fields); i += 2 {
					if fields[i] == "inuse" {
						frag6Inuse, _ = strconv.Atoi(fields[i+1])
					}
				}
			}
		}
	}

	if data, err := os.ReadFile("/proc/net/snmp"); err == nil {
		lines := strings.Split(string(data), "\n")
		for i := 0; i+1 < len(lines); i++ {
			headerFields := strings.Fields(lines[i])
			if len(headerFields) > 0 && headerFields[0] == "Tcp:" {
				valFields := strings.Fields(lines[i+1])
				if len(valFields) == len(headerFields) {
					for idx, name := range headerFields {
						if name == "CurrEstab" {
							currEstab, _ = strconv.Atoi(valFields[idx])
							break
						}
					}
				}
				break
			}
		}
	}

	tcpTotal := tcpAlloc + tcpTw
	closed := tcpTotal - (tcpInuse + tcp6Inuse)
	if closed < 0 {
		closed = 0
	}

	fmt.Printf("Total: %d\n", totalSockets)
	fmt.Printf("TCP:   %d (estab %d, closed %d, orphaned %d, timewait %d)\n\n",
		tcpTotal, currEstab, closed, tcpOrphan, tcpTw)
	fmt.Printf("Transport Total     IP        IPv6\n")
	fmt.Printf("RAW\t  %-9d %-9d %-9d\n", rawInuse+raw6Inuse, rawInuse, raw6Inuse)
	fmt.Printf("UDP\t  %-9d %-9d %-9d\n", udpInuse+udp6Inuse, udpInuse, udp6Inuse)
	fmt.Printf("TCP\t  %-9d %-9d %-9d\n", tcpInuse+tcp6Inuse, tcpInuse, tcp6Inuse)
	inet4 := rawInuse + udpInuse + tcpInuse
	inet6 := raw6Inuse + udp6Inuse + tcp6Inuse
	fmt.Printf("INET\t  %-9d %-9d %-9d\n", inet4+inet6, inet4, inet6)
	fmt.Printf("FRAG\t  %-9d %-9d %-9d\n\n", fragInuse+frag6Inuse, fragInuse, frag6Inuse)
}

// fetchOptions is the union of what curl and wget need from their very different
// command lines. Each applet parses its own flags into this struct and hands it to
// runFetch, so the two tools stay independent at the surface while sharing one
// transfer implementation.
type fetchOptions struct {
	method      string
	body        string
	headers     http.Header
	output      string // "" derive a name, "-" standard output
	directory   string // wget -P
	quiet       bool
	noVerbose   bool // wget -nv
	trace       bool // curl -v, wget -d
	showHeaders bool // wget -S, curl -i
	follow      bool
	maxRedirect int
	headOnly    bool
	failOnError bool // curl -f
	spider      bool // wget --spider
	resume      bool // wget -c
	noClobber   bool // wget -nc
	insecure    bool
	timeout     time.Duration
	tries       int
	userAgent   string
	user        string
	password    string
	toFile      bool // save under a derived name when no -O was given
	progress    bool
}

func newFetchOptions() *fetchOptions {
	return &fetchOptions{method: "GET", headers: http.Header{}, maxRedirect: 20, tries: 1,
		timeout: 60 * time.Second}
}

// cmdCurl writes to standard output and does not follow redirects unless asked,
// matching curl's defaults.
func cmdCurl(args []string) int {
	o := newFetchOptions()
	remoteName := false
	targets := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		// curl accepts clustered short flags such as -sSL.
		if len(a) > 2 && strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			cluster := true
			for _, flag := range a[1:] {
				switch flag {
				case 's':
					o.quiet = true
				case 'v':
					o.trace = true
				case 'L':
					o.follow = true
				case 'f':
					o.failOnError = true
				case 'i':
					o.showHeaders = true
				case 'k':
					o.insecure = true
				case 'I':
					// curl -I reports the headers it fetched.
					o.headOnly, o.showHeaders, o.method = true, true, "HEAD"
				case 'O':
					remoteName = true
				default:
					cluster = false
				}
			}
			if cluster {
				continue
			}
		}
		next := func() (string, bool) {
			i++
			if i >= len(args) {
				fatalf("curl", "option %q requires an argument", a)
				return "", false
			}
			return args[i], true
		}
		switch a {
		case "-o", "--output":
			v, ok := next()
			if !ok {
				return 2
			}
			o.output = v
		case "-O", "--remote-name":
			remoteName = true
		case "-s", "--silent":
			o.quiet = true
		case "-v", "--verbose":
			o.trace = true
		case "-i", "--show-headers", "--include":
			o.showHeaders = true
		case "-f", "--fail":
			o.failOnError = true
		case "-k", "--insecure":
			o.insecure = true
		case "-L", "--location":
			o.follow = true
		case "-I", "--head":
			o.headOnly, o.showHeaders, o.method = true, true, "HEAD"
		case "-A", "--user-agent":
			v, ok := next()
			if !ok {
				return 2
			}
			o.userAgent = v
		case "-u", "--user":
			v, ok := next()
			if !ok {
				return 2
			}
			o.user, o.password = splitCredentials(v)
		case "--max-time", "--connect-timeout":
			v, ok := next()
			if !ok {
				return 2
			}
			seconds, err := strconv.ParseFloat(v, 64)
			if err != nil {
				fatalf("curl", "invalid time %q", v)
				return 2
			}
			o.timeout = time.Duration(seconds * float64(time.Second))
		case "-X", "--request":
			v, ok := next()
			if !ok {
				return 2
			}
			o.method = v
		case "-d", "--data":
			v, ok := next()
			if !ok {
				return 2
			}
			o.body = v
			if o.method == "GET" {
				o.method = "POST"
			}
		case "-H", "--header":
			v, ok := next()
			if !ok {
				return 2
			}
			addHeaderLine(o.headers, v)
		case "--":
			targets = append(targets, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(a, "-") {
				fatalf("curl", "unsupported option %q", a)
				return 2
			}
			targets = append(targets, a)
		}
	}
	if len(targets) == 0 {
		fatalf("curl", "no URL specified")
		return 2
	}
	if remoteName {
		o.toFile = true
	}
	return runFetchAll("curl", o, targets)
}

// cmdWget follows redirects, saves to a file named after the URL, and reports
// progress on standard error, matching wget's defaults.
func cmdWget(args []string) int {
	o := newFetchOptions()
	o.follow, o.toFile, o.progress = true, true, true
	o.tries = 20
	targets := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, bool) {
			i++
			if i >= len(args) {
				fatalf("wget", "option %q requires an argument", a)
				return "", false
			}
			return args[i], true
		}
		// wget spells most long options --name=value.
		name, value := a, ""
		if strings.HasPrefix(a, "--") {
			if eq := strings.Index(a, "="); eq > 0 {
				name, value = a[:eq], a[eq+1:]
			}
		}
		inline := func() (string, bool) {
			if value != "" {
				return value, true
			}
			return next()
		}
		switch name {
		case "-O", "--output-document":
			v, ok := inline()
			if !ok {
				return 2
			}
			o.output, o.toFile = v, false
		case "-P", "--directory-prefix":
			v, ok := inline()
			if !ok {
				return 2
			}
			o.directory = v
		case "-q", "--quiet":
			o.quiet = true
		case "-nv", "--no-verbose":
			o.noVerbose = true
		case "-v", "--verbose":
			o.quiet, o.noVerbose = false, false
		case "-d", "--debug":
			o.trace = true
		case "-S", "--server-response":
			o.showHeaders = true
		case "-c", "--continue":
			o.resume = true
		case "-nc", "--no-clobber":
			o.noClobber = true
		case "--spider":
			o.spider, o.method = true, "HEAD"
		case "--no-check-certificate":
			o.insecure = true
		case "-T", "--timeout", "--connect-timeout", "--read-timeout", "--dns-timeout":
			v, ok := inline()
			if !ok {
				return 2
			}
			seconds, err := strconv.ParseFloat(v, 64)
			if err != nil {
				fatalf("wget", "invalid timeout %q", v)
				return 2
			}
			o.timeout = time.Duration(seconds * float64(time.Second))
		case "-t", "--tries":
			v, ok := inline()
			if !ok {
				return 2
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				fatalf("wget", "invalid number of tries %q", v)
				return 2
			}
			if n == 0 {
				n = 1 << 20 // wget spells "retry forever" as 0 or inf.
			}
			o.tries = n
		case "--max-redirect":
			v, ok := inline()
			if !ok {
				return 2
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				fatalf("wget", "invalid redirect count %q", v)
				return 2
			}
			o.maxRedirect = n
			o.follow = n > 0
		case "-U", "--user-agent":
			v, ok := inline()
			if !ok {
				return 2
			}
			o.userAgent = v
		case "--user":
			v, ok := inline()
			if !ok {
				return 2
			}
			o.user = v
		case "--password":
			v, ok := inline()
			if !ok {
				return 2
			}
			o.password = v
		case "--header":
			v, ok := inline()
			if !ok {
				return 2
			}
			addHeaderLine(o.headers, v)
		case "--method":
			v, ok := inline()
			if !ok {
				return 2
			}
			o.method = strings.ToUpper(v)
		case "--post-data", "--body-data":
			v, ok := inline()
			if !ok {
				return 2
			}
			o.body = v
			if o.method == "GET" {
				o.method = "POST"
			}
		case "--post-file", "--body-file":
			v, ok := inline()
			if !ok {
				return 2
			}
			content, err := os.ReadFile(v)
			if err != nil {
				fatalf("wget", "%v", err)
				return 2
			}
			o.body = string(content)
			if o.method == "GET" {
				o.method = "POST"
			}
		case "--":
			targets = append(targets, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(a, "-") {
				fatalf("wget", "unsupported option %q", a)
				return 2
			}
			targets = append(targets, a)
		}
	}
	if len(targets) == 0 {
		fatalf("wget", "missing URL")
		return 2
	}
	return runFetchAll("wget", o, targets)
}

func splitCredentials(value string) (string, string) {
	if colon := strings.Index(value, ":"); colon >= 0 {
		return value[:colon], value[colon+1:]
	}
	return value, ""
}

func addHeaderLine(headers http.Header, line string) {
	p := strings.SplitN(line, ":", 2)
	if len(p) == 2 {
		headers.Add(strings.TrimSpace(p[0]), strings.TrimSpace(p[1]))
	}
}

type downloadProgress struct {
	destination io.Writer
	output      io.Writer
	total       int64
	downloaded  int64
	lastUpdate  time.Time
}

func newDownloadProgress(destination, output io.Writer, total int64) *downloadProgress {
	p := &downloadProgress{destination: destination, output: output, total: total}
	p.report(false)
	return p
}

func (p *downloadProgress) Write(data []byte) (int, error) {
	n, err := p.destination.Write(data)
	p.downloaded += int64(n)
	if time.Since(p.lastUpdate) >= 200*time.Millisecond || p.total > 0 && p.downloaded >= p.total {
		p.report(false)
	}
	return n, err
}

func (p *downloadProgress) finish() {
	p.report(true)
}

func (p *downloadProgress) report(final bool) {
	if p.total > 0 {
		percent := p.downloaded * 100 / p.total
		if percent > 100 {
			percent = 100
		}
		fmt.Fprintf(p.output, "\rwget: %3d%% (%d/%d bytes)", percent, p.downloaded, p.total)
	} else {
		fmt.Fprintf(p.output, "\rwget: %d bytes downloaded", p.downloaded)
	}
	if final {
		fmt.Fprintln(p.output)
	}
	p.lastUpdate = time.Now()
}

func cmdNc(args []string) int {
	listen, udp := false, false
	timeout := time.Duration(0)
	localPort := ""
	operands := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-l":
			listen = true
		case "-u":
			udp = true
		case "-p":
			i++
			if i >= len(args) {
				return 2
			}
			localPort = args[i]
		case "-w":
			i++
			if i >= len(args) {
				return 2
			}
			n, _ := strconv.ParseFloat(args[i], 64)
			timeout = time.Duration(n * float64(time.Second))
		case "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(args[i], "-") {
				fatalf("nc", "unsupported option %q", args[i])
				return 2
			}
			operands = append(operands, args[i])
		}
	}
	network := "tcp"
	if udp {
		network = "udp"
	}
	var conn net.Conn
	var err error
	if listen {
		if localPort == "" && len(operands) > 0 {
			localPort = operands[len(operands)-1]
		}
		if localPort == "" {
			return 2
		}
		if udp {
			pc, e := net.ListenPacket("udp", ":"+localPort)
			if e != nil {
				err = e
			} else {
				defer pc.Close()
				buf := make([]byte, 65535)
				n, addr, e := pc.ReadFrom(buf)
				if e != nil {
					return 1
				}
				os.Stdout.Write(buf[:n])
				rest, _ := io.ReadAll(os.Stdin)
				if len(rest) > 0 {
					if _, err := pc.WriteTo(rest, addr); err != nil {
						fatalf("nc", "%v", err)
						return 1
					}
				}
				return 0
			}
		} else {
			ln, e := net.Listen("tcp", ":"+localPort)
			if e != nil {
				err = e
			} else {
				defer ln.Close()
				conn, err = ln.Accept()
			}
		}
	} else {
		if len(operands) != 2 {
			return 2
		}
		dialer := net.Dialer{Timeout: timeout}
		conn, err = dialer.Dial(network, net.JoinHostPort(operands[0], operands[1]))
	}
	if err != nil {
		fatalf("nc", "%v", err)
		return 1
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		close(done)
	}()
	_, copyErr := io.Copy(os.Stdout, conn)
	<-done
	if copyErr != nil && !isTimeout(copyErr) {
		fatalf("nc", "%v", copyErr)
		return 1
	}
	return 0
}
func isTimeout(err error) bool {
	var e net.Error
	return errors.As(err, &e) && e.Timeout()
}

// timeoutContext is kept here to avoid imposing context plumbing on each
// resolver call.
func timeoutContext(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// runFetchAll performs one transfer per URL. A -O/-o destination is opened once so
// that several URLs concatenate into it, which is what both tools do.
func runFetchAll(prog string, o *fetchOptions, targets []string) int {
	var shared *os.File
	if o.output != "" && o.output != "-" {
		f, err := os.Create(o.output) //nolint:gosec // G304: writing the user-named output file is the applet's purpose.
		if err != nil {
			fatalf(prog, "%v", err)
			return 1
		}
		defer f.Close()
		shared = f
	}
	status := 0
	for _, target := range targets {
		if rc := runFetch(prog, o, target, shared); rc != 0 {
			status = rc
		}
	}
	return status
}

// fetchDestination decides where a transfer is written and, for wget, applies the
// -P prefix, -nc skipping and the .1/.2 uniquifying that wget does by default.
func fetchDestination(prog string, o *fetchOptions, target string) (name string, skip bool) {
	parsed, err := url.Parse(target)
	if err != nil {
		return "", false
	}
	name = strings.TrimSuffix(parsed.Path, "/")
	name = name[strings.LastIndex(name, "/")+1:]
	if name == "" {
		name = "index.html"
	}
	if o.directory != "" {
		name = o.directory + "/" + name
	}
	if _, err := os.Stat(name); err != nil {
		return name, false
	}
	if o.noClobber {
		if !o.quiet {
			fmt.Fprintf(os.Stderr, "%s: file %q already there; not retrieving\n", prog, name)
		}
		return name, true
	}
	if o.resume {
		return name, false
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s.%d", name, i)
		if _, err := os.Stat(candidate); err != nil {
			return candidate, false
		}
	}
}

// runFetch carries out a single transfer, retrying transport failures up to
// o.tries times the way wget does.
func runFetch(prog string, o *fetchOptions, target string, shared *os.File) int {
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}

	destination := ""
	if shared == nil && o.output != "-" && o.toFile {
		skip := false
		destination, skip = fetchDestination(prog, o, target)
		if skip {
			return 0
		}
	}

	client := &http.Client{Timeout: o.timeout}
	if o.insecure {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // G402: --no-check-certificate/-k is an explicit request to skip verification.
		}
	}
	redirects := o.maxRedirect
	if !o.follow {
		redirects = 0
	}
	client.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
		if len(via) >= redirects {
			return http.ErrUseLastResponse
		}
		return nil
	}

	var lastErr error
	for attempt := 1; attempt <= o.tries; attempt++ {
		if prog == "wget" && !o.quiet && !o.noVerbose {
			fmt.Fprintf(os.Stderr, "--%s--  %s\n", time.Now().Format("2006-01-02 15:04:05"), target)
		}
		status, err := fetchOnce(prog, o, target, destination, shared, client)
		if err == nil {
			return status
		}
		lastErr = err
		if attempt < o.tries && !o.quiet {
			fmt.Fprintf(os.Stderr, "%s: retrying (%d/%d): %v\n", prog, attempt, o.tries, err)
		}
	}
	fatalf(prog, "%v", lastErr)
	return 1
}

// fetchOnce runs one request. A transport-level failure is returned as an error so
// the caller can retry; an HTTP error response is a final answer and returns a status.
func fetchOnce(prog string, o *fetchOptions, target, destination string, shared *os.File,
	client *http.Client) (int, error) {
	req, err := http.NewRequest(o.method, target, strings.NewReader(o.body)) //nolint:gosec // G704: fetching a user-selected URL is this applet's purpose.
	if err != nil {
		return 2, err
	}
	req.Header = o.headers.Clone()
	if o.userAgent != "" {
		req.Header.Set("User-Agent", o.userAgent)
	}
	if o.user != "" {
		req.SetBasicAuth(o.user, o.password)
	}
	if o.body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	// wget -c asks the server to send only the part we are missing.
	var resumeAt int64
	if o.resume && destination != "" {
		if info, statErr := os.Stat(destination); statErr == nil && info.Size() > 0 {
			resumeAt = info.Size()
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeAt))
		}
	}

	if o.trace {
		traceRequest(req)
	}
	resp, err := client.Do(req) //nolint:gosec // G704: fetching a user-selected URL is this applet's purpose.
	if err != nil {
		return 1, err
	}
	defer resp.Body.Close()

	if o.trace {
		fmt.Fprintf(os.Stderr, "< %s %s\n", resp.Proto, resp.Status)
		for name, values := range resp.Header {
			for _, value := range values {
				fmt.Fprintf(os.Stderr, "< %s: %s\n", name, value)
			}
		}
		fmt.Fprintln(os.Stderr, "<")
	}
	if prog == "wget" && !o.quiet && !o.noVerbose {
		fmt.Fprintf(os.Stderr, "HTTP request sent, awaiting response... %s\n", resp.Status)
		if resp.ContentLength >= 0 {
			kind := resp.Header.Get("Content-Type")
			if cut := strings.Index(kind, ";"); cut >= 0 {
				kind = kind[:cut]
			}
			// wget only adds the human-readable size once it is worth reading.
			size := strconv.FormatInt(resp.ContentLength, 10)
			if resp.ContentLength >= 1024 {
				size = fmt.Sprintf("%d (%s)", resp.ContentLength, humanSize(resp.ContentLength))
			}
			fmt.Fprintf(os.Stderr, "Length: %s [%s]\n", size, kind)
		} else {
			fmt.Fprintln(os.Stderr, "Length: unspecified")
		}
	}
	if o.showHeaders {
		out := os.Stderr
		if prog == "curl" {
			out = os.Stdout
		}
		fmt.Fprintf(out, "%s %s\n", resp.Proto, resp.Status)
		if err := resp.Header.Write(out); err != nil {
			return 1, nil
		}
		fmt.Fprintln(out)
	}

	failed := resp.StatusCode >= 400
	if o.spider || o.headOnly {
		if failed {
			return serverErrorStatus(prog, o), nil
		}
		return 0, nil
	}
	if failed && o.failOnError {
		return 22, nil // curl -f reports 22 and discards the body.
	}

	w, file, err := openFetchTarget(prog, o, destination, shared, resp, resumeAt)
	if err != nil {
		fatalf(prog, "%v", err)
		return 1, nil
	}
	if file != nil {
		defer file.Close()
	}

	var progress *downloadProgress
	if o.progress && !o.quiet && !o.noVerbose {
		if destination != "" {
			fmt.Fprintf(os.Stderr, "Saving to: %q\n\n", destination)
		}
		progress = newDownloadProgress(w, os.Stderr, resp.ContentLength)
		w = progress
	}
	written, err := io.Copy(w, io.LimitReader(resp.Body, 1<<34))
	if progress != nil {
		progress.finish()
	}
	if err != nil {
		return 1, err
	}
	if prog == "wget" && !o.quiet && destination != "" {
		fmt.Fprintf(os.Stderr, "%q saved [%d]\n", destination, resumeAt+written)
	}
	if failed {
		return serverErrorStatus(prog, o), nil
	}
	return 0, nil
}

// openFetchTarget resolves the writer for a transfer, honouring an already-open
// -O destination, a resumed download, or plain standard output.
func openFetchTarget(prog string, o *fetchOptions, destination string, shared *os.File,
	resp *http.Response, resumeAt int64) (io.Writer, *os.File, error) {
	if shared != nil {
		return shared, nil, nil
	}
	if destination == "" {
		return os.Stdout, nil, nil
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if resumeAt > 0 && resp.StatusCode == http.StatusPartialContent {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	} else if resumeAt > 0 && !o.quiet {
		fmt.Fprintf(os.Stderr, "%s: server ignored the range request; restarting\n", prog)
	}
	file, err := os.OpenFile(destination, flags, 0o666) //nolint:gosec // G302,G304: the download follows the process umask and the user names the file.
	if err != nil {
		return nil, nil, err
	}
	return file, file, nil
}

// serverErrorStatus maps an HTTP error response onto each tool's exit code: wget
// reports 8 for a server error, curl reports success unless -f was given.
func serverErrorStatus(prog string, o *fetchOptions) int {
	if prog == "wget" {
		return 8
	}
	if o.failOnError {
		return 22
	}
	return 0
}

func traceRequest(req *http.Request) {
	trace := &httptrace.ClientTrace{
		ConnectStart: func(network, address string) {
			fmt.Fprintf(os.Stderr, "* Connecting to %s over %s\n", address, network)
		},
		GotConn: func(info httptrace.GotConnInfo) {
			fmt.Fprintf(os.Stderr, "* Connected to %s from %s\n", info.Conn.RemoteAddr(), info.Conn.LocalAddr())
		},
	}
	*req = *req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	path := req.URL.RequestURI()
	if path == "" {
		path = "/"
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	fmt.Fprintf(os.Stderr, "> %s %s HTTP/1.1\n> Host: %s\n", req.Method, path, host)
	for name, values := range req.Header {
		for _, value := range values {
			fmt.Fprintf(os.Stderr, "> %s: %s\n", name, value)
		}
	}
	fmt.Fprintln(os.Stderr, ">")
}
