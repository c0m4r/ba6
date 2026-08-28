// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type openFileRecord struct {
	command, user, fd, kind, name string
	pid                           int
}

func cmdLsof(args []string) int {
	selected := map[int]bool{}
	internetOnly, numeric := false, false
	var paths []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-p":
			index++
			if index >= len(args) || parsePIDList(args[index], selected) != nil {
				fatalf("lsof", "invalid PID list")
				return 1
			}
		case strings.HasPrefix(arg, "-p") && len(arg) > 2:
			if parsePIDList(arg[2:], selected) != nil {
				fatalf("lsof", "invalid PID list")
				return 1
			}
		case arg == "-i":
			internetOnly = true
		case arg == "-n" || arg == "-P" || arg == "-nP" || arg == "-Pn":
			numeric = true
		case arg == "--":
			paths = append(paths, args[index+1:]...)
			index = len(args)
		case strings.HasPrefix(arg, "-"):
			fatalf("lsof", "unsupported option %q", arg)
			return 1
		default:
			paths = append(paths, arg)
		}
	}
	sockets := readSocketNames(numeric)
	records, err := collectOpenFiles("/proc", selected, sockets, internetOnly, paths)
	if err != nil {
		fatalf("lsof", "%v", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "COMMAND PID USER FD TYPE NAME")
	for _, record := range records {
		fmt.Fprintf(os.Stdout, "%s %d %s %s %s %s\n", record.command, record.pid, record.user, record.fd, record.kind, record.name)
	}
	if len(records) == 0 && (len(selected) > 0 || internetOnly || len(paths) > 0) {
		return 1
	}
	return 0
}

func collectOpenFiles(procRoot string, selected map[int]bool, sockets map[string]string, internetOnly bool, paths []string) ([]openFileRecord, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	cleanPaths := make(map[string]bool, len(paths))
	for _, path := range paths {
		absolute, absErr := filepath.Abs(path)
		if absErr == nil {
			cleanPaths[filepath.Clean(absolute)] = true
		} else {
			cleanPaths[filepath.Clean(path)] = true
		}
	}
	var records []openFileRecord
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || len(selected) > 0 && !selected[pid] {
			continue
		}
		base := filepath.Join(procRoot, entry.Name())
		commandData, readErr := os.ReadFile(filepath.Join(base, "comm"))
		if readErr != nil {
			continue
		}
		command := strings.TrimSpace(string(commandData))
		userName := procUserName(filepath.Join(base, "status"))
		for _, special := range []struct{ name, fd, kind string }{
			{"cwd", "cwd", "DIR"}, {"root", "rtd", "DIR"}, {"exe", "txt", "REG"},
		} {
			target, linkErr := os.Readlink(filepath.Join(base, special.name))
			if linkErr != nil || internetOnly || !lsofPathMatches(target, cleanPaths) {
				continue
			}
			records = append(records, openFileRecord{command: command, pid: pid, user: userName, fd: special.fd, kind: special.kind, name: target})
		}
		fds, readErr := os.ReadDir(filepath.Join(base, "fd"))
		if readErr != nil {
			continue
		}
		for _, fdEntry := range fds {
			target, linkErr := os.Readlink(filepath.Join(base, "fd", fdEntry.Name()))
			if linkErr != nil {
				continue
			}
			kind, display := lsofTarget(target, sockets)
			if internetOnly && kind != "IPv4" && kind != "IPv6" {
				continue
			}
			if len(cleanPaths) > 0 && !lsofPathMatches(target, cleanPaths) {
				continue
			}
			fdName := fdEntry.Name() + lsofFDMode(filepath.Join(base, "fdinfo", fdEntry.Name()))
			records = append(records, openFileRecord{command: command, pid: pid, user: userName, fd: fdName, kind: kind, name: display})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].pid != records[j].pid {
			return records[i].pid < records[j].pid
		}
		return records[i].fd < records[j].fd
	})
	return records, nil
}

func procUserName(statusPath string) string {
	data, _ := os.ReadFile(statusPath)
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 1 {
			if account, err := user.LookupId(fields[1]); err == nil {
				return account.Username
			}
			return fields[1]
		}
	}
	return "?"
}

func lsofFDMode(path string) string {
	data, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "flags:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			break
		}
		flags, err := strconv.ParseUint(fields[1], 8, 32)
		if err != nil {
			break
		}
		switch flags & syscall.O_ACCMODE {
		case syscall.O_WRONLY:
			return "w"
		case syscall.O_RDWR:
			return "u"
		default:
			return "r"
		}
	}
	return "?"
}

func lsofTarget(target string, sockets map[string]string) (string, string) {
	switch {
	case strings.HasPrefix(target, "socket:["):
		if name := sockets[target]; name != "" {
			kind := "IPv4"
			if strings.HasPrefix(name, "TCP6 ") || strings.HasPrefix(name, "UDP6 ") {
				kind = "IPv6"
			}
			return kind, name
		}
		return "sock", target
	case strings.HasPrefix(target, "pipe:["):
		return "FIFO", target
	case strings.HasPrefix(target, "anon_inode:"):
		return "a_inode", target
	}
	info, err := os.Stat(target)
	if err == nil && info.IsDir() {
		return "DIR", target
	}
	return "REG", target
}

func lsofPathMatches(target string, paths map[string]bool) bool {
	if len(paths) == 0 {
		return true
	}
	return paths[filepath.Clean(strings.TrimSuffix(target, " (deleted)"))]
}

func readSocketNames(numeric bool) map[string]string {
	result := make(map[string]string)
	for _, table := range []struct{ path, protocol string }{
		{"/proc/net/tcp", "TCP"}, {"/proc/net/tcp6", "TCP6"}, {"/proc/net/udp", "UDP"}, {"/proc/net/udp6", "UDP6"},
	} {
		data, err := os.ReadFile(table.path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			local := decodeProcSocketAddress(fields[1], strings.HasSuffix(table.protocol, "6"), numeric)
			remote := decodeProcSocketAddress(fields[2], strings.HasSuffix(table.protocol, "6"), numeric)
			name := table.protocol + " " + local
			if !strings.HasSuffix(remote, ":0") && !strings.HasSuffix(remote, "]:0") {
				name += "->" + remote
			}
			result["socket:["+fields[9]+"]"] = name + " (" + socketState(fields[3]) + ")"
		}
	}
	return result
}

func decodeProcSocketAddress(value string, ipv6, numeric bool) string {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return value
	}
	port, _ := strconv.ParseUint(parts[1], 16, 16)
	var ip net.IP
	if ipv6 && len(parts[0]) == 32 {
		raw := make([]byte, 16)
		for word := 0; word < 4; word++ {
			parsed, err := strconv.ParseUint(parts[0][word*8:(word+1)*8], 16, 32)
			if err != nil {
				return value
			}
			binary.LittleEndian.PutUint32(raw[word*4:(word+1)*4], uint32(parsed))
		}
		ip = net.IP(raw)
	} else {
		parsed, err := strconv.ParseUint(parts[0], 16, 32)
		if err != nil {
			return value
		}
		ip = net.IPv4(byte(parsed), byte(parsed>>8), byte(parsed>>16), byte(parsed>>24)) //nolint:gosec // Value was parsed with a 32-bit limit.
	}
	host := ip.String()
	if !numeric {
		if names, err := net.LookupAddr(host); err == nil && len(names) > 0 {
			host = strings.TrimSuffix(names[0], ".")
		}
	}
	return net.JoinHostPort(host, strconv.FormatUint(port, 10))
}

type dnsRecord struct {
	name, kind, class, value string
	ttl                      uint32
}

// digSections tracks which parts of dig's default report to print, driven
// by +noall and the individual +question/+answer/+stats/+comments toggles.
type digSections struct {
	comments, question, answer, stats bool
}

func cmdDig(args []string) int {
	args = expandShortOptions(args, "tcpx")
	server, name, queryType, className := "", "", "A", "IN"
	short, tcpOnly, typeSet := false, false, false
	port := "53"
	timeout := 5 * time.Second
	sections := digSections{comments: true, question: true, answer: true, stats: true}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func(opt string) (string, bool) {
			i++
			if i >= len(args) {
				fatalf("dig", "option '%s' requires an argument", opt)
				return "", false
			}
			return args[i], true
		}
		switch {
		case strings.HasPrefix(arg, "@"):
			server = strings.TrimPrefix(arg, "@")
		case arg == "+short":
			short = true
		case arg == "+tcp":
			tcpOnly = true
		case arg == "+noall":
			sections = digSections{}
		case arg == "+comments":
			sections.comments = true
		case arg == "+nocomments":
			sections.comments = false
		case arg == "+question":
			sections.question = true
		case arg == "+noquestion":
			sections.question = false
		case arg == "+answer":
			sections.answer = true
		case arg == "+noanswer":
			sections.answer = false
		case arg == "+stats":
			sections.stats = true
		case arg == "+nostats":
			sections.stats = false
		case arg == "+noedns" || arg == "+edns" || strings.HasPrefix(arg, "+edns=") || arg == "+cmd" || arg == "+nocmd":
			// no-op: ba6 never attaches an EDNS OPT record to its query
			// (equivalent to always running with +noedns), and it never
			// prints the "; <<>> DiG ..." command banner +cmd/+nocmd toggle.
		case strings.HasPrefix(arg, "+time="):
			seconds, err := strconv.Atoi(strings.TrimPrefix(arg, "+time="))
			if err != nil || seconds < 1 || seconds > 60 {
				fatalf("dig", "invalid timeout %q", arg)
				return 1
			}
			timeout = time.Duration(seconds) * time.Second
		case strings.HasPrefix(arg, "+"):
			fatalf("dig", "unsupported option %q", arg)
			return 1
		case arg == "-x":
			v, ok := next("-x")
			if !ok {
				return 1
			}
			ip := net.ParseIP(v)
			if ip == nil {
				fatalf("dig", "invalid address '%s'", v)
				return 1
			}
			name, queryType, typeSet = reverseDNSName(ip), "PTR", true
		case arg == "-t":
			v, ok := next("-t")
			if !ok {
				return 1
			}
			queryType, typeSet = strings.ToUpper(v), true
		case arg == "-c":
			v, ok := next("-c")
			if !ok {
				return 1
			}
			className = strings.ToUpper(v)
		case arg == "-p":
			v, ok := next("-p")
			if !ok {
				return 1
			}
			port = v
		// Operands are recognised by what they are, not by their position:
		// dig accepts the name and the record type in either order, so
		// "dig +short A example.com" and "dig example.com A" are the same query.
		default:
			upper := strings.ToUpper(arg)
			if _, known := dnsTypeCode(upper); known && !typeSet {
				queryType, typeSet = upper, true
				continue
			}
			if name == "" {
				name = arg
				continue
			}
			fatalf("dig", "unexpected operand %q", arg)
			return 1
		}
	}
	if name == "" {
		fatalf("dig", "missing name")
		return 1
	}
	if className != "IN" {
		fatalf("dig", "unsupported class %q", className)
		return 1
	}
	typeCode, ok := dnsTypeCode(queryType)
	if !ok {
		fatalf("dig", "unsupported query type %q", queryType)
		return 1
	}
	if server == "" {
		server = firstNameServer()
	}
	if server == "" {
		fatalf("dig", "no DNS server configured")
		return 1
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, port)
	}
	query, id, err := buildDNSQuery(name, typeCode, dnsClassIN, true)
	if err != nil {
		fatalf("dig", "%v", err)
		return 1
	}
	start := time.Now()
	network := dnsNetwork(tcpOnly)
	response, err := exchangeDNS(server, query, timeout, network)
	if err != nil {
		fmt.Fprintf(os.Stdout, ";; communications error to %s: %s\n", hostServerDisplay(server), hostErrorText(err))
		fmt.Fprintln(os.Stdout, ";; no servers could be reached")
		return 9
	}
	records, authority, truncated, rcode, err := parseDNSResponseSections(response, id)
	if err != nil {
		fatalf("dig", "%v", err)
		return 1
	}
	if truncated && !tcpOnly {
		network = dnsNetwork(true)
		response, err = exchangeDNS(server, query, timeout, network)
		if err != nil {
			fmt.Fprintf(os.Stdout, ";; communications error to %s: %s\n", hostServerDisplay(server), hostErrorText(err))
			fmt.Fprintln(os.Stdout, ";; no servers could be reached")
			return 9
		}
		records, authority, _, rcode, err = parseDNSResponseSections(response, id)
		if err != nil {
			fatalf("dig", "%v", err)
			return 1
		}
	}
	elapsed := time.Since(start)

	if short {
		for _, record := range records {
			fmt.Fprintln(os.Stdout, record.value)
		}
		return 0
	}

	questionName := name
	if !strings.HasSuffix(questionName, ".") {
		questionName += "."
	}

	if sections.comments {
		flagsLine, qd, an, ns, ar := digHeaderFields(response)
		fmt.Fprintln(os.Stdout, ";; Got answer:")
		fmt.Fprintf(os.Stdout, ";; ->>HEADER<<- opcode: QUERY, status: %s, id: %d\n", dnsRCodeName(rcode), id)
		fmt.Fprintf(os.Stdout, ";; flags: %s; QUERY: %d, ANSWER: %d, AUTHORITY: %d, ADDITIONAL: %d\n\n",
			flagsLine, qd, an, ns, ar)
	}
	if sections.question {
		fmt.Fprintln(os.Stdout, ";; QUESTION SECTION:")
		fmt.Fprintln(os.Stdout, digQuestionLine(questionName, queryType))
		fmt.Fprintln(os.Stdout)
	}
	if sections.answer && len(records) > 0 {
		if sections.comments {
			fmt.Fprintln(os.Stdout, ";; ANSWER SECTION:")
		}
		for _, record := range records {
			fmt.Fprintln(os.Stdout, dnsMasterLine(record))
		}
		if sections.comments {
			fmt.Fprintln(os.Stdout)
		}
	}
	if sections.answer && len(authority) > 0 {
		if sections.comments {
			fmt.Fprintln(os.Stdout, ";; AUTHORITY SECTION:")
		}
		for _, record := range authority {
			fmt.Fprintln(os.Stdout, dnsMasterLine(record))
		}
		if sections.comments {
			fmt.Fprintln(os.Stdout)
		}
	}
	if sections.stats {
		serverHost, serverPort, _ := net.SplitHostPort(server)
		transport := "UDP"
		if strings.HasPrefix(network, "tcp") {
			transport = "TCP"
		}
		fmt.Fprintf(os.Stdout, ";; Query time: %d msec\n", elapsed.Milliseconds())
		fmt.Fprintf(os.Stdout, ";; SERVER: %s#%s(%s) (%s)\n", serverHost, serverPort, serverHost, transport)
		fmt.Fprintf(os.Stdout, ";; WHEN: %s\n", time.Now().Format("Mon Jan _2 15:04:05 MST 2006"))
		fmt.Fprintf(os.Stdout, ";; MSG SIZE  rcvd: %d\n", len(response))
	}
	return 0
}

// digHeaderFields reads the flags and section counts straight from the raw
// DNS header, which parseDNSResponse doesn't otherwise surface: the flags
// line lists only the bits that are actually set, in dig's own qr/aa/tc/rd/
// ra/ad/cd order.
func digHeaderFields(response []byte) (flags string, qd, an, ns, ar int) {
	if len(response) < 12 {
		return "", 0, 0, 0, 0
	}
	b2, b3 := response[2], response[3]
	var set []string
	if b2&0x80 != 0 {
		set = append(set, "qr")
	}
	if b2&0x04 != 0 {
		set = append(set, "aa")
	}
	if b2&0x02 != 0 {
		set = append(set, "tc")
	}
	if b2&0x01 != 0 {
		set = append(set, "rd")
	}
	if b3&0x80 != 0 {
		set = append(set, "ra")
	}
	if b3&0x20 != 0 {
		set = append(set, "ad")
	}
	if b3&0x10 != 0 {
		set = append(set, "cd")
	}
	return strings.Join(set, " "),
		int(binary.BigEndian.Uint16(response[4:6])),
		int(binary.BigEndian.Uint16(response[6:8])),
		int(binary.BigEndian.Uint16(response[8:10])),
		int(binary.BigEndian.Uint16(response[10:12]))
}

// digQuestionLine lays out the ";NAME  IN  TYPE" question line in the same
// tab-stop columns as an answer line, minus the TTL column an answer has and
// the question doesn't.
func digQuestionLine(name, queryType string) string {
	line := ";" + name
	line += dnsColumnPad(dnsPadWidth(line), 32) + "IN"
	line += dnsColumnPad(dnsPadWidth(line), 40) + queryType
	return line
}

// dnsTypes maps every record type the bundled DNS tools can name. OPT is
// listed so an EDNS0 pseudo-record can be recognised and skipped.
var dnsTypes = map[string]uint16{
	"A": 1, "NS": 2, "CNAME": 5, "SOA": 6, "PTR": 12, "MX": 15, "TXT": 16,
	"AAAA": 28, "SRV": 33, "OPT": 41, "DS": 43, "DNSKEY": 48, "SVCB": 64,
	"HTTPS": 65, "ANY": 255, "CAA": 257,
}

// dnsClasses holds the query classes; CH is the one that matters in practice,
// because it is how a server is asked for its own version.
var dnsClasses = map[string]uint16{"IN": 1, "CH": 3, "HS": 4}

// dnsClassIN is the ordinary Internet class, used for every query but the
// deliberate -c CH lookups.
const dnsClassIN = 1

func dnsTypeCode(name string) (uint16, bool) {
	value, ok := dnsTypes[name]
	return value, ok
}

func dnsTypeName(value uint16) string {
	for name, code := range dnsTypes {
		if code == value && name != "ANY" {
			return name
		}
	}
	return "TYPE" + strconv.Itoa(int(value))
}

func dnsClassCode(name string) (uint16, bool) {
	value, ok := dnsClasses[name]
	return value, ok
}

func dnsClassName(value uint16) string {
	for name, code := range dnsClasses {
		if code == value {
			return name
		}
	}
	return "CLASS" + strconv.Itoa(int(value))
}

func dnsNetwork(tcp bool) string {
	if tcp {
		return "tcp"
	}
	return "udp"
}

// nameServers lists the resolvers configured in /etc/resolv.conf, in file
// order.
func nameServers() []string {
	data, _ := os.ReadFile("/etc/resolv.conf")
	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			servers = append(servers, fields[1])
		}
	}
	return servers
}

func firstNameServer() string {
	if servers := nameServers(); len(servers) > 0 {
		return servers[0]
	}
	return ""
}

func buildDNSQuery(name string, queryType, queryClass uint16, recursive bool) ([]byte, uint16, error) {
	idBytes := []byte{0, 0}
	if _, err := rand.Read(idBytes); err != nil {
		return nil, 0, err
	}
	id := binary.BigEndian.Uint16(idBytes)
	flags := uint16(0x0100) // RD, recursion desired
	if !recursive {
		flags = 0
	}
	message := make([]byte, 12)
	binary.BigEndian.PutUint16(message[0:2], id)
	binary.BigEndian.PutUint16(message[2:4], flags)
	binary.BigEndian.PutUint16(message[4:6], 1)
	encoded, err := encodeDNSName(name)
	if err != nil {
		return nil, 0, err
	}
	message = append(message, encoded...)
	message = binary.BigEndian.AppendUint16(message, queryType)
	message = binary.BigEndian.AppendUint16(message, queryClass)
	return message, id, nil
}

func encodeDNSName(name string) ([]byte, error) {
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return []byte{0}, nil
	}
	var encoded []byte
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, fmt.Errorf("invalid DNS label in %q", name)
		}
		encoded = append(encoded, byte(len(label))) //nolint:gosec // DNS label length was bounded to 63.
		encoded = append(encoded, label...)
	}
	if len(encoded) >= 255 {
		return nil, fmt.Errorf("DNS name is too long")
	}
	return append(encoded, 0), nil
}

// exchangeDNS sends one query and returns the raw response. network is a Go
// dial network ("udp", "tcp4", ...), which is how the callers pin both the
// transport and, for host -4/-6, the address family.
func exchangeDNS(server string, query []byte, timeout time.Duration, network string) ([]byte, error) {
	tcp := strings.HasPrefix(network, "tcp")
	conn, err := net.DialTimeout(network, server, timeout) //nolint:gosec // G704: contacting the user-selected DNS server is this applet's purpose.
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if tcp {
		framed := make([]byte, 2, len(query)+2)
		binary.BigEndian.PutUint16(framed, uint16(len(query))) //nolint:gosec // DNS messages built here are far below 64 KiB.
		framed = append(framed, query...)
		if _, err := conn.Write(framed); err != nil {
			return nil, err
		}
		length := []byte{0, 0}
		if _, err := io.ReadFull(conn, length); err != nil {
			return nil, err
		}
		size := binary.BigEndian.Uint16(length)
		if size < 12 {
			return nil, fmt.Errorf("short DNS response")
		}
		response := make([]byte, size)
		_, err = io.ReadFull(conn, response)
		return response, err
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	response := make([]byte, 65535)
	n, err := conn.Read(response)
	if err != nil {
		return nil, err
	}
	return response[:n], nil
}

// parseDNSResponseSections is parseDNSResponse extended with the authority
// section, which dig's default report shows for a negative answer (the SOA
// that comes back with an NXDOMAIN, for instance).
func parseDNSResponseSections(message []byte, id uint16) (answers, authority []dnsRecord, truncated bool, rcode int, err error) {
	if len(message) < 12 || binary.BigEndian.Uint16(message[0:2]) != id || message[2]&0x80 == 0 {
		return nil, nil, false, 0, fmt.Errorf("invalid DNS response header")
	}
	questionCount := int(binary.BigEndian.Uint16(message[4:6]))
	answerCount := int(binary.BigEndian.Uint16(message[6:8]))
	authorityCount := int(binary.BigEndian.Uint16(message[8:10]))
	if questionCount > 64 || answerCount > 4096 || authorityCount > 4096 {
		return nil, nil, false, 0, fmt.Errorf("unreasonable DNS section count")
	}
	offset := 12
	for index := 0; index < questionCount; index++ {
		_, next, decodeErr := decodeDNSName(message, offset, 0)
		if decodeErr != nil || next+4 > len(message) {
			return nil, nil, false, 0, fmt.Errorf("invalid DNS question")
		}
		offset = next + 4
	}
	answers, offset, err = parseDNSRecordSection(message, offset, answerCount)
	if err != nil {
		return nil, nil, false, 0, err
	}
	authority, _, err = parseDNSRecordSection(message, offset, authorityCount)
	if err != nil {
		return nil, nil, false, 0, err
	}
	return answers, authority, message[2]&0x02 != 0, int(message[3] & 0x0f), nil
}

// parseDNSRecordSection decodes count consecutive resource records starting
// at offset, returning the IN-class ones and the offset just past the
// section.
func parseDNSRecordSection(message []byte, offset, count int) ([]dnsRecord, int, error) {
	records := make([]dnsRecord, 0, count)
	for index := 0; index < count; index++ {
		name, next, err := decodeDNSName(message, offset, 0)
		if err != nil || next+10 > len(message) {
			return nil, 0, fmt.Errorf("invalid DNS record name")
		}
		kind := binary.BigEndian.Uint16(message[next : next+2])
		class := binary.BigEndian.Uint16(message[next+2 : next+4])
		ttl := binary.BigEndian.Uint32(message[next+4 : next+8])
		length := int(binary.BigEndian.Uint16(message[next+8 : next+10]))
		rdata := next + 10
		if rdata+length > len(message) {
			return nil, 0, fmt.Errorf("truncated DNS record")
		}
		value, valueErr := formatDNSRData(message, rdata, length, kind)
		if valueErr != nil {
			return nil, 0, valueErr
		}
		if class == dnsClassIN {
			records = append(records, dnsRecord{
				name: name, kind: dnsTypeName(kind), class: dnsClassName(class), value: value, ttl: ttl,
			})
		}
		offset = rdata + length
	}
	return records, offset, nil
}

func decodeDNSName(message []byte, offset, depth int) (string, int, error) {
	if depth > 16 || offset < 0 || offset >= len(message) {
		return "", 0, fmt.Errorf("invalid compressed DNS name")
	}
	labels := []string{}
	for steps := 0; steps < 128; steps++ {
		if offset >= len(message) {
			return "", 0, fmt.Errorf("truncated DNS name")
		}
		length := int(message[offset])
		if length&0xc0 == 0xc0 {
			if offset+1 >= len(message) {
				return "", 0, fmt.Errorf("truncated DNS pointer")
			}
			pointer := (length&0x3f)<<8 | int(message[offset+1])
			name, _, err := decodeDNSName(message, pointer, depth+1)
			if err != nil {
				return "", 0, err
			}
			labels = append(labels, strings.TrimSuffix(name, "."))
			return strings.Join(labels, ".") + ".", offset + 2, nil
		}
		if length&0xc0 != 0 || length > 63 || offset+1+length > len(message) {
			return "", 0, fmt.Errorf("invalid DNS label")
		}
		offset++
		if length == 0 {
			return strings.Join(labels, ".") + ".", offset, nil
		}
		labels = append(labels, string(message[offset:offset+length]))
		offset += length
	}
	return "", 0, fmt.Errorf("DNS name has too many labels")
}

func formatDNSRData(message []byte, offset, length int, kind uint16) (string, error) {
	data := message[offset : offset+length]
	switch kind {
	case 1:
		if length != 4 {
			break
		}
		return net.IP(data).String(), nil
	case 28:
		if length != 16 {
			break
		}
		return net.IP(data).String(), nil
	case 2, 5, 12:
		name, _, err := decodeDNSName(message, offset, 0)
		return name, err
	case 15:
		if length < 3 {
			break
		}
		name, _, err := decodeDNSName(message, offset+2, 0)
		return fmt.Sprintf("%d %s", binary.BigEndian.Uint16(data[:2]), name), err
	case 16:
		var values []string
		for position := 0; position < len(data); {
			size := int(data[position])
			position++
			if position+size > len(data) {
				return "", fmt.Errorf("invalid TXT record")
			}
			values = append(values, strconv.Quote(string(data[position:position+size])))
			position += size
		}
		return strings.Join(values, " "), nil
	case 33:
		if length < 7 {
			break
		}
		target, _, err := decodeDNSName(message, offset+6, 0)
		return fmt.Sprintf("%d %d %d %s", binary.BigEndian.Uint16(data[0:2]),
			binary.BigEndian.Uint16(data[2:4]), binary.BigEndian.Uint16(data[4:6]), target), err
	case 64, 65:
		return formatServiceBinding(message, offset, length)
	case 257:
		if length < 2 || int(data[1])+2 > length {
			break
		}
		tag := int(data[1])
		return fmt.Sprintf("%d %s %s", data[0], data[2:2+tag], strconv.Quote(string(data[2+tag:]))), nil
	case 6:
		primary, next, err := decodeDNSName(message, offset, 0)
		if err != nil {
			return "", err
		}
		mailbox, numbers, err := decodeDNSName(message, next, 0)
		if err != nil || numbers+20 > offset+length {
			return "", fmt.Errorf("invalid SOA record")
		}
		return fmt.Sprintf("%s %s %d %d %d %d %d", primary, mailbox,
			binary.BigEndian.Uint32(message[numbers:numbers+4]), binary.BigEndian.Uint32(message[numbers+4:numbers+8]),
			binary.BigEndian.Uint32(message[numbers+8:numbers+12]), binary.BigEndian.Uint32(message[numbers+12:numbers+16]),
			binary.BigEndian.Uint32(message[numbers+16:numbers+20])), nil
	}
	return fmt.Sprintf("\\# %d %x", length, data), nil
}

// serviceBindingKeys names the SVCB/HTTPS parameters that have a defined
// presentation form; anything else is printed as keyNNN.
var serviceBindingKeys = map[uint16]string{
	0: "mandatory", 1: "alpn", 2: "no-default-alpn", 3: "port",
	4: "ipv4hint", 5: "ech", 6: "ipv6hint",
}

// formatServiceBinding renders an SVCB or HTTPS record in the presentation
// format of RFC 9460: the priority, the target name, and then the parameters
// in the order they appear on the wire.
func formatServiceBinding(message []byte, offset, length int) (string, error) {
	if length < 3 {
		return "", fmt.Errorf("invalid service binding record")
	}
	priority := binary.BigEndian.Uint16(message[offset : offset+2])
	target, next, err := decodeDNSName(message, offset+2, 0)
	if err != nil {
		return "", err
	}
	parts := []string{strconv.Itoa(int(priority)), target}
	for next+4 <= offset+length {
		key := binary.BigEndian.Uint16(message[next : next+2])
		size := int(binary.BigEndian.Uint16(message[next+2 : next+4]))
		next += 4
		if next+size > offset+length {
			return "", fmt.Errorf("invalid service binding parameter")
		}
		parts = append(parts, formatServiceBindingParam(key, message[next:next+size]))
		next += size
	}
	return strings.Join(parts, " "), nil
}

func formatServiceBindingParam(key uint16, value []byte) string {
	name, known := serviceBindingKeys[key]
	if !known {
		name = "key" + strconv.Itoa(int(key))
	}
	switch key {
	case 2: // no-default-alpn carries no value
		return name
	case 1: // a sequence of length-prefixed protocol identifiers
		var protocols []string
		for position := 0; position < len(value); {
			size := int(value[position])
			position++
			if position+size > len(value) {
				break
			}
			protocols = append(protocols, string(value[position:position+size]))
			position += size
		}
		return fmt.Sprintf("%s=%q", name, strings.Join(protocols, ","))
	case 3:
		if len(value) != 2 {
			break
		}
		return fmt.Sprintf("%s=%d", name, binary.BigEndian.Uint16(value))
	case 4, 6:
		step := 4
		if key == 6 {
			step = 16
		}
		var addresses []string
		for position := 0; position+step <= len(value); position += step {
			addresses = append(addresses, net.IP(value[position:position+step]).String())
		}
		return fmt.Sprintf("%s=%s", name, strings.Join(addresses, ","))
	case 5:
		return fmt.Sprintf("%s=%s", name, base64.StdEncoding.EncodeToString(value))
	}
	return fmt.Sprintf("%s=%q", name, string(value))
}

func dnsRCodeName(code int) string {
	names := []string{"NOERROR", "FORMERR", "SERVFAIL", "NXDOMAIN", "NOTIMP", "REFUSED"}
	if code >= 0 && code < len(names) {
		return names[code]
	}
	return strconv.Itoa(code)
}

type traceOptions struct {
	maxHops, probes int
	wait            time.Duration
	numeric         bool
	family          int
}

type traceReply struct {
	address string
	delay   time.Duration
	reached bool
	ok      bool
	err     error
}

func cmdTraceroute(args []string) int {
	opts, host, err := parseTraceOptions(args)
	if err != nil {
		fatalf("traceroute", "%v", err)
		return 2
	}
	ip, family, err := resolveTraceHost(host, opts.family)
	if err != nil {
		fatalf("traceroute", "%v", err)
		return 1
	}
	opts.family = family
	fmt.Fprintf(os.Stdout, "traceroute to %s (%s), %d hops max\n", host, ip, opts.maxHops)
	for hop := 1; hop <= opts.maxHops; hop++ {
		fmt.Fprintf(os.Stdout, "%2d ", hop)
		reached := false
		for probe := 0; probe < opts.probes; probe++ {
			reply := runTraceProbe(ip, family, hop, probe, opts.wait)
			if reply.err != nil {
				fatalf("traceroute", "%v", reply.err)
				return 1
			}
			if !reply.ok {
				fmt.Fprint(os.Stdout, " *")
				continue
			}
			name := traceAddressName(reply.address, opts.numeric)
			fmt.Fprintf(os.Stdout, " %s %.3f ms", name, float64(reply.delay.Microseconds())/1000)
			reached = reached || reply.reached
		}
		fmt.Fprintln(os.Stdout)
		if reached {
			return 0
		}
	}
	return 1
}

func parseTraceOptions(args []string) (traceOptions, string, error) {
	opts := traceOptions{maxHops: 30, probes: 3, wait: time.Second}
	host := ""
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-4":
			opts.family = 4
		case "-6":
			opts.family = 6
		case "-n":
			opts.numeric = true
		case "-m", "-q", "-w":
			option := args[index]
			remaining := args[index+1:]
			if len(remaining) == 0 {
				return opts, "", fmt.Errorf("%s requires a value", option)
			}
			valueText := remaining[0]
			index++
			if option == "-w" {
				seconds, err := strconv.ParseFloat(valueText, 64)
				if err != nil || seconds <= 0 || seconds > 60 {
					return opts, "", fmt.Errorf("invalid wait time %q", valueText)
				}
				opts.wait = time.Duration(seconds * float64(time.Second))
				continue
			}
			value, err := strconv.Atoi(valueText)
			if err != nil || value < 1 || value > 255 {
				return opts, "", fmt.Errorf("invalid value %q", valueText)
			}
			if option == "-m" {
				opts.maxHops = value
			} else {
				opts.probes = value
			}
		default:
			if strings.HasPrefix(args[index], "-") {
				return opts, "", fmt.Errorf("unsupported option %q", args[index])
			}
			if host != "" {
				return opts, "", fmt.Errorf("unexpected operand %q", args[index])
			}
			host = args[index]
		}
	}
	if host == "" {
		return opts, "", fmt.Errorf("missing host")
	}
	return opts, host, nil
}

func resolveTraceHost(host string, requested int) (net.IP, int, error) {
	addresses, err := net.LookupIP(host)
	if err != nil {
		return nil, 0, err
	}
	for _, address := range addresses {
		if requested != 6 && address.To4() != nil {
			return address.To4(), 4, nil
		}
		if requested != 4 && address.To4() == nil && address.To16() != nil {
			return address.To16(), 6, nil
		}
	}
	return nil, 0, fmt.Errorf("no address found for requested family")
}

func runTraceProbe(target net.IP, family, hop, probe int, wait time.Duration) traceReply {
	port := 33434 + hop*8 + probe
	network := "udp4"
	if family == 6 {
		network = "udp6"
	}
	connection, err := net.DialUDP(network, nil, &net.UDPAddr{IP: target, Port: port})
	if err != nil {
		return traceReply{err: err}
	}
	defer connection.Close()
	raw, err := connection.SyscallConn()
	if err != nil {
		return traceReply{err: err}
	}
	var socketErr error
	controlErr := raw.Control(func(fd uintptr) {
		if family == 6 {
			socketErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS, hop) //nolint:gosec // Socket descriptor comes from net.UDPConn.
			if socketErr == nil {
				socketErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_RECVERR, 1) //nolint:gosec // Socket descriptor comes from net.UDPConn.
			}
		} else {
			socketErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, hop) //nolint:gosec // Socket descriptor comes from net.UDPConn.
			if socketErr == nil {
				socketErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_RECVERR, 1) //nolint:gosec // Socket descriptor comes from net.UDPConn.
			}
		}
	})
	if controlErr != nil || socketErr != nil {
		if controlErr != nil {
			return traceReply{err: controlErr}
		}
		return traceReply{err: socketErr}
	}
	start := time.Now()
	if _, err := connection.Write([]byte("ba6-trace")); err != nil {
		return traceReply{err: err}
	}
	_ = connection.SetReadDeadline(start.Add(wait))
	oob := make([]byte, 256)
	var oobLength int
	var receiveErr error
	err = raw.Read(func(fd uintptr) bool {
		_, oobLength, _, _, receiveErr = syscall.Recvmsg(int(fd), nil, oob, syscall.MSG_ERRQUEUE|syscall.MSG_DONTWAIT) //nolint:gosec // Socket descriptor comes from net.UDPConn; payload data is intentionally discarded.
		return receiveErr != syscall.EAGAIN && receiveErr != syscall.EWOULDBLOCK
	})
	if err != nil {
		if os.IsTimeout(err) {
			return traceReply{}
		}
		return traceReply{err: err}
	}
	if receiveErr != nil {
		if receiveErr == syscall.EAGAIN || receiveErr == syscall.EWOULDBLOCK {
			return traceReply{}
		}
		return traceReply{err: receiveErr}
	}
	if oobLength == 0 {
		return traceReply{}
	}
	address, reached, err := parseTraceErrorQueue(oob[:oobLength], family, target)
	if err != nil {
		return traceReply{err: err}
	}
	return traceReply{address: address, delay: time.Since(start), reached: reached, ok: true}
}

func parseTraceErrorQueue(oob []byte, family int, target net.IP) (string, bool, error) {
	messages, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return "", false, err
	}
	for _, message := range messages {
		wantLevel, wantType := int32(syscall.IPPROTO_IP), int32(syscall.IP_RECVERR)
		if family == 6 {
			wantLevel, wantType = int32(syscall.IPPROTO_IPV6), int32(syscall.IPV6_RECVERR)
		}
		if message.Header.Level != wantLevel || message.Header.Type != wantType || len(message.Data) < 16 {
			continue
		}
		icmpType, icmpCode := message.Data[5], message.Data[6]
		address := traceOffenderAddress(message.Data[16:], family)
		if address == "" {
			address = target.String()
		}
		reached := family == 4 && icmpType == 3 && icmpCode == 3 || family == 6 && icmpType == 1 && icmpCode == 4
		return address, reached, nil
	}
	return "", false, fmt.Errorf("no extended socket error")
}

func traceOffenderAddress(sockaddr []byte, family int) string {
	if len(sockaddr) < 2 {
		return ""
	}
	addressFamily := binary.LittleEndian.Uint16(sockaddr[:2])
	if family == 4 && addressFamily == syscall.AF_INET && len(sockaddr) >= 8 {
		return net.IP(sockaddr[4:8]).String()
	}
	if family == 6 && addressFamily == syscall.AF_INET6 && len(sockaddr) >= 24 {
		return net.IP(sockaddr[8:24]).String()
	}
	return ""
}

func traceAddressName(address string, numeric bool) string {
	if numeric {
		return address
	}
	names, err := net.LookupAddr(address)
	if err != nil || len(names) == 0 {
		return address
	}
	return strings.TrimSuffix(names[0], ".") + " (" + address + ")"
}

type interfaceCounters struct {
	receive, transmit uint64
}

func cmdIftop(args []string) int {
	interfaceName := ""
	duration := time.Second
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-t", "-n", "-N", "-P":
		case "-i":
			index++
			if index >= len(args) {
				fatalf("iftop", "-i requires an interface")
				return 1
			}
			interfaceName = args[index]
		case "-s":
			index++
			if index >= len(args) {
				fatalf("iftop", "-s requires seconds")
				return 1
			}
			seconds, err := strconv.ParseFloat(args[index], 64)
			if err != nil || seconds <= 0 || seconds > 3600 {
				fatalf("iftop", "invalid duration %q", args[index])
				return 1
			}
			duration = time.Duration(seconds * float64(time.Second))
		default:
			fatalf("iftop", "unsupported option %q", args[index])
			return 1
		}
	}
	before, err := readInterfaceCounters("/proc/net/dev")
	if err != nil {
		fatalf("iftop", "%v", err)
		return 1
	}
	time.Sleep(duration)
	after, err := readInterfaceCounters("/proc/net/dev")
	if err != nil {
		fatalf("iftop", "%v", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "Interface RX/s TX/s RX-total TX-total (%s sample)\n", duration)
	names := make([]string, 0, len(after))
	for name := range after {
		if interfaceName == "" || interfaceName == name {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		old, ok := before[name]
		if !ok {
			old = after[name]
		}
		current := after[name]
		rxDelta, txDelta := counterDelta(old.receive, current.receive), counterDelta(old.transmit, current.transmit)
		fmt.Fprintf(os.Stdout, "%-9s %10s %10s %10s %10s\n", name,
			humanSizeUint64(uint64(float64(rxDelta)/duration.Seconds())), humanSizeUint64(uint64(float64(txDelta)/duration.Seconds())),
			humanSizeUint64(current.receive), humanSizeUint64(current.transmit))
	}
	if interfaceName != "" && len(names) == 0 {
		fatalf("iftop", "unknown interface %q", interfaceName)
		return 1
	}
	return 0
}

func readInterfaceCounters(path string) (map[string]interfaceCounters, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make(map[string]interfaceCounters)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		name, values, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(values)
		if len(fields) < 16 {
			continue
		}
		receive, receiveErr := strconv.ParseUint(fields[0], 10, 64)
		transmit, transmitErr := strconv.ParseUint(fields[8], 10, 64)
		if receiveErr == nil && transmitErr == nil {
			result[strings.TrimSpace(name)] = interfaceCounters{receive: receive, transmit: transmit}
		}
	}
	return result, scanner.Err()
}

func counterDelta(before, after uint64) uint64 {
	if after < before {
		return 0
	}
	return after - before
}
