// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// hostOptions is one host(1) command line. The lookup is deliberately direct:
// the name is asked for exactly as written, so there is no resolv.conf search
// list to walk and no ndots rule to apply.
type hostOptions struct {
	types     []string
	class     string
	port      int
	wait      time.Duration
	retries   int
	tcp       bool
	udp       bool
	verbose   bool
	norecurse bool
	family    int // 0 selects either address family, 4 or 6 restrict transport
}

// hostDefaultTypes is the question set used when no -t is given: the records
// that answer "where does this name point", asked in that order.
var hostDefaultTypes = []string{"A", "AAAA", "MX", "HTTPS"}

// hostQuestion is one entry of a response's question section.
type hostQuestion struct {
	name, kind, class string
}

// hostMessage is a decoded DNS response, kept whole because -v prints every
// section rather than the answers alone.
type hostMessage struct {
	id                        uint16
	opcode, rcode             int
	flags                     []string
	truncated                 bool
	questions                 []hostQuestion
	answers, authority, extra []dnsRecord
	questionCount, answerCount,
	authorityCount, additionalCount int
	size int
}

func cmdHost(args []string) int {
	opts, name, server, err := parseHostOptions(args)
	if err != nil {
		fatalf("host", "%v", err)
		return 1
	}
	if name == "" {
		if err := writeAppletHelp(os.Stderr, "host"); err != nil {
			fatalf("host", "write error: %v", err)
		}
		return 1
	}

	// A dotted-quad or colon-separated operand is a request for the reverse
	// name, which is what makes "host 1.2.3.4" print a pointer record.
	query := name
	if address := net.ParseIP(name); address != nil {
		query = reverseDNSName(address)
		if len(opts.types) == 0 {
			opts.types = []string{"PTR"}
		}
	}
	explicit := len(opts.types) > 0
	if !explicit {
		opts.types = hostDefaultTypes
	}

	servers, err := hostServers(server, opts)
	if err != nil {
		fatalf("host", "%v", err)
		return 1
	}
	session := &hostSession{opts: opts, servers: servers, explicit: explicit}
	if server != "" {
		// The named server is announced once an answer actually comes back, so
		// a resolver that never replies is only reported as unreachable.
		session.banner = fmt.Sprintf("Using domain server:\nName: %s\nAddress: %s\nAliases: \n\n",
			server, hostServerDisplay(servers[0]))
	}

	// With an explicit -t a name that holds no such record is reported as
	// such; the default question set simply stays silent, as the original does.
	for _, kind := range opts.types {
		if status := session.query(query, kind); status != 0 {
			return status
		}
	}
	return 0
}

// hostSession is one run of the applet: the options, the resolvers to ask, and
// the banner that is still waiting to be printed.
type hostSession struct {
	opts     hostOptions
	servers  []string
	banner   string
	explicit bool
}

// query asks one question of the first server that answers and prints the
// result. It returns a nonzero exit status when the name does not resolve.
func (s *hostSession) query(query, kind string) int {
	opts := s.opts
	// Diagnostics echo the name that was asked for, absolute dot included: a
	// reverse lookup reports 4.3.2.1.in-addr.arpa. rather than the address.
	display := query
	if opts.verbose {
		fmt.Fprintf(os.Stdout, "Trying %q\n", strings.TrimSuffix(query, "."))
	}
	response, server, elapsed, err := hostExchange(opts, query, kind, s.servers)
	if err != nil {
		fmt.Fprintln(os.Stdout, ";; no servers could be reached")
		return 1
	}
	if s.banner != "" {
		fmt.Fprint(os.Stdout, s.banner)
		s.banner = ""
	}
	if response.rcode != 0 {
		fmt.Fprintf(os.Stdout, "Host %s not found: %d(%s)\n",
			display, response.rcode, dnsRCodeName(response.rcode))
		if opts.verbose {
			fmt.Fprintf(os.Stdout, "Received %d bytes from %s in %d ms\n",
				response.size, hostServerDisplay(server), elapsed.Milliseconds())
		}
		return 1
	}
	if opts.verbose {
		writeHostVerbose(response, server, elapsed)
		return 0
	}
	for _, record := range response.answers {
		fmt.Fprintln(os.Stdout, hostRecordLine(record))
	}
	if len(response.answers) == 0 && s.explicit {
		fmt.Fprintf(os.Stdout, "%s has no %s record\n", display, kind)
	}
	return 0
}

// hostExchange tries every server in turn, each up to retries+1 times, and
// returns the first response it gets. Servers that never answer are reported
// individually, the way the original narrates a failing resolver list.
func hostExchange(opts hostOptions, query, kind string, servers []string) (*hostMessage, string, time.Duration, error) {
	typeCode, ok := dnsTypeCode(kind)
	if !ok {
		return nil, "", 0, fmt.Errorf("unsupported query type %q", kind)
	}
	classCode, ok := dnsClassCode(opts.class)
	if !ok {
		return nil, "", 0, fmt.Errorf("unsupported query class %q", opts.class)
	}
	// Type ANY answers are large enough that the original asks them over TCP
	// from the start; -U forces the datagram path back on.
	useTCP := opts.tcp || (kind == "ANY" && !opts.udp)
	var lastErr error
	for _, server := range servers {
		for attempt := 0; attempt <= opts.retries; attempt++ {
			message, id, err := buildDNSQuery(query, typeCode, classCode, !opts.norecurse)
			if err != nil {
				return nil, "", 0, err
			}
			started := time.Now()
			raw, err := exchangeDNS(server, message, opts.wait, hostNetwork(useTCP, opts.family))
			if err == nil && !useTCP {
				// A truncated datagram answer is retried over TCP, which is the
				// only way to see the whole record set.
				if parsed, parseErr := parseHostMessage(raw, id); parseErr == nil && parsed.truncated {
					if retried, retryErr := exchangeDNS(server, message, opts.wait, hostNetwork(true, opts.family)); retryErr == nil {
						raw = retried
					}
				}
			}
			if err != nil {
				lastErr = err
				fmt.Fprintf(os.Stdout, ";; communications error to %s: %s\n", hostServerDisplay(server), hostErrorText(err))
				continue
			}
			parsed, err := parseHostMessage(raw, id)
			if err != nil {
				lastErr = err
				fmt.Fprintf(os.Stdout, ";; %s\n", err)
				continue
			}
			return parsed, server, time.Since(started), nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no DNS server configured")
	}
	return nil, "", 0, lastErr
}

// hostServerDisplay writes a resolver address the way the DNS tools quote it,
// with the port after a hash rather than a colon.
func hostServerDisplay(server string) string {
	address, port, err := net.SplitHostPort(server)
	if err != nil {
		return server
	}
	return address + "#" + port
}

func hostNetwork(tcp bool, family int) string {
	network := "udp"
	if tcp {
		network = "tcp"
	}
	switch family {
	case 4:
		return network + "4"
	case 6:
		return network + "6"
	}
	return network
}

// hostRecordLine renders one answer the way host words it: a sentence per
// record type rather than the master-file syntax dig prints.
func hostRecordLine(record dnsRecord) string {
	name := strings.TrimSuffix(record.name, ".")
	switch record.kind {
	case "A":
		return fmt.Sprintf("%s has address %s", name, record.value)
	case "AAAA":
		return fmt.Sprintf("%s has IPv6 address %s", name, record.value)
	case "CNAME":
		return fmt.Sprintf("%s is an alias for %s", name, record.value)
	case "MX":
		return fmt.Sprintf("%s mail is handled by %s", name, record.value)
	case "NS":
		return fmt.Sprintf("%s name server %s", name, record.value)
	case "PTR":
		return fmt.Sprintf("%s domain name pointer %s", name, record.value)
	case "TXT":
		return fmt.Sprintf("%s descriptive text %s", name, record.value)
	case "HTTPS":
		return fmt.Sprintf("%s has HTTP service bindings %s", name, record.value)
	}
	return fmt.Sprintf("%s has %s record %s", name, record.kind, record.value)
}

// writeHostVerbose prints the whole response in the master-file layout that -v
// shares with dig, including the sections an answer-only view discards.
func writeHostVerbose(response *hostMessage, server string, elapsed time.Duration) {
	fmt.Fprintf(os.Stdout, ";; ->>HEADER<<- opcode: %s, status: %s, id: %d\n",
		dnsOpcodeName(response.opcode), dnsRCodeName(response.rcode), response.id)
	fmt.Fprintf(os.Stdout, ";; flags: %s; QUERY: %d, ANSWER: %d, AUTHORITY: %d, ADDITIONAL: %d\n",
		strings.Join(response.flags, " "), response.questionCount, response.answerCount,
		response.authorityCount, response.additionalCount)
	if len(response.questions) > 0 {
		fmt.Fprint(os.Stdout, "\n;; QUESTION SECTION:\n")
		for _, question := range response.questions {
			line := ";" + question.name
			line += dnsColumnPad(dnsPadWidth(line), 32) + question.class
			line += dnsColumnPad(dnsPadWidth(line), 40) + question.kind
			fmt.Fprintln(os.Stdout, line)
		}
	}
	for _, section := range []struct {
		title   string
		records []dnsRecord
	}{
		{"ANSWER", response.answers},
		{"AUTHORITY", response.authority},
		{"ADDITIONAL", response.extra},
	} {
		if len(section.records) == 0 {
			continue
		}
		fmt.Fprintf(os.Stdout, "\n;; %s SECTION:\n", section.title)
		for _, record := range section.records {
			fmt.Fprintln(os.Stdout, dnsMasterLine(record))
		}
	}
	fmt.Fprintf(os.Stdout, "\nReceived %d bytes from %s in %d ms\n",
		response.size, hostServerDisplay(server), elapsed.Milliseconds())
}

// dnsMasterLine lays out one record in the tab-stop columns dig uses: the name
// is padded to column 24 and the TTL, class and type to the following eight
// column boundaries.
func dnsMasterLine(record dnsRecord) string {
	line := record.name
	line += dnsColumnPad(dnsPadWidth(line), 24) + strconv.FormatUint(uint64(record.ttl), 10)
	line += dnsColumnPad(dnsPadWidth(line), 32) + record.class
	line += dnsColumnPad(dnsPadWidth(line), 40) + record.kind
	line += dnsColumnPad(dnsPadWidth(line), 48) + record.value
	return line
}

// dnsPadWidth is the printed width of a partially built line, counting a tab as
// an advance to the next eight-column stop.
func dnsPadWidth(line string) int {
	width := 0
	for _, character := range line {
		if character == '\t' {
			width = (width/8 + 1) * 8
			continue
		}
		width++
	}
	return width
}

// dnsColumnPad returns the tabs needed to move from column to target, or a
// single space when the field has already overflowed its column.
func dnsColumnPad(column, target int) string {
	if column >= target {
		return " "
	}
	pad := ""
	for column < target {
		pad += "\t"
		column = (column/8 + 1) * 8
	}
	return pad
}

// hostServers is the list of resolvers to ask, either the one named on the
// command line or every nameserver in resolv.conf that matches -4/-6.
func hostServers(server string, opts hostOptions) ([]string, error) {
	port := strconv.Itoa(opts.port)
	if server != "" {
		address := server
		if net.ParseIP(server) == nil {
			resolved, err := net.LookupHost(server) //nolint:gosec // G704: resolving the server named on the command line is the operand's purpose.
			if err != nil || len(resolved) == 0 {
				return nil, fmt.Errorf("couldn't get address for '%s': not found", server)
			}
			address = resolved[0]
		}
		return []string{net.JoinHostPort(address, port)}, nil
	}
	var servers []string
	for _, address := range nameServers() {
		if !hostFamilyMatches(address, opts.family) {
			continue
		}
		servers = append(servers, net.JoinHostPort(address, port))
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("no DNS server configured")
	}
	return servers, nil
}

func hostFamilyMatches(address string, family int) bool {
	parsed := net.ParseIP(address)
	if parsed == nil || family == 0 {
		return parsed != nil
	}
	if family == 4 {
		return parsed.To4() != nil
	}
	return parsed.To4() == nil
}

// reverseDNSName builds the in-addr.arpa or ip6.arpa name that holds an
// address's pointer record.
func reverseDNSName(address net.IP) string {
	if four := address.To4(); four != nil {
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa.", four[3], four[2], four[1], four[0])
	}
	sixteen := address.To16()
	var name strings.Builder
	for index := len(sixteen) - 1; index >= 0; index-- {
		fmt.Fprintf(&name, "%x.%x.", sixteen[index]&0x0f, sixteen[index]>>4)
	}
	name.WriteString("ip6.arpa.")
	return name.String()
}

// hostErrorText words a failed exchange the way the resolver libraries do: the
// bare strerror sentence, or "timed out" when no answer arrived at all.
func hostErrorText(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timed out"
	}
	return err.Error()
}

// parseHostMessage decodes a complete response. Unlike the answer-only parser
// dig uses, it keeps the header, the question and every section so -v can print
// them.
func parseHostMessage(message []byte, id uint16) (*hostMessage, error) {
	if len(message) < 12 || binary.BigEndian.Uint16(message[0:2]) != id || message[2]&0x80 == 0 {
		return nil, fmt.Errorf("invalid DNS response header")
	}
	parsed := &hostMessage{
		id:              id,
		opcode:          int(message[2]>>3) & 0x0f,
		rcode:           int(message[3] & 0x0f),
		truncated:       message[2]&0x02 != 0,
		size:            len(message),
		questionCount:   int(binary.BigEndian.Uint16(message[4:6])),
		answerCount:     int(binary.BigEndian.Uint16(message[6:8])),
		authorityCount:  int(binary.BigEndian.Uint16(message[8:10])),
		additionalCount: int(binary.BigEndian.Uint16(message[10:12])),
		flags:           dnsFlagNames(message[2], message[3]),
	}
	if parsed.questionCount > 64 || parsed.answerCount > 4096 ||
		parsed.authorityCount > 4096 || parsed.additionalCount > 4096 {
		return nil, fmt.Errorf("unreasonable DNS section count")
	}
	offset := 12
	for index := 0; index < parsed.questionCount; index++ {
		name, next, err := decodeDNSName(message, offset, 0)
		if err != nil || next+4 > len(message) {
			return nil, fmt.Errorf("invalid DNS question")
		}
		parsed.questions = append(parsed.questions, hostQuestion{
			name:  name,
			kind:  dnsTypeName(binary.BigEndian.Uint16(message[next : next+2])),
			class: dnsClassName(binary.BigEndian.Uint16(message[next+2 : next+4])),
		})
		offset = next + 4
	}
	sections := []struct {
		count   int
		records *[]dnsRecord
	}{
		{parsed.answerCount, &parsed.answers},
		{parsed.authorityCount, &parsed.authority},
		{parsed.additionalCount, &parsed.extra},
	}
	for _, section := range sections {
		for index := 0; index < section.count; index++ {
			record, next, err := parseHostRecord(message, offset)
			if err != nil {
				return nil, err
			}
			// An EDNS0 pseudo-record carries transport parameters, not data.
			if record.kind != "OPT" {
				*section.records = append(*section.records, record)
			}
			offset = next
		}
	}
	return parsed, nil
}

func parseHostRecord(message []byte, offset int) (dnsRecord, int, error) {
	name, next, err := decodeDNSName(message, offset, 0)
	if err != nil || next+10 > len(message) {
		return dnsRecord{}, 0, fmt.Errorf("invalid DNS record name")
	}
	kind := binary.BigEndian.Uint16(message[next : next+2])
	class := binary.BigEndian.Uint16(message[next+2 : next+4])
	ttl := binary.BigEndian.Uint32(message[next+4 : next+8])
	length := int(binary.BigEndian.Uint16(message[next+8 : next+10]))
	rdata := next + 10
	if rdata+length > len(message) {
		return dnsRecord{}, 0, fmt.Errorf("truncated DNS record")
	}
	value, err := formatDNSRData(message, rdata, length, kind)
	if err != nil {
		return dnsRecord{}, 0, err
	}
	return dnsRecord{
		name:  name,
		kind:  dnsTypeName(kind),
		class: dnsClassName(class),
		value: value,
		ttl:   ttl,
	}, rdata + length, nil
}

// dnsFlagNames lists the header bits that are set, in the order dig prints
// them.
func dnsFlagNames(high, low byte) []string {
	var flags []string
	for _, flag := range []struct {
		name string
		set  bool
	}{
		{"qr", high&0x80 != 0},
		{"aa", high&0x04 != 0},
		{"tc", high&0x02 != 0},
		{"rd", high&0x01 != 0},
		{"ra", low&0x80 != 0},
		{"ad", low&0x20 != 0},
		{"cd", low&0x10 != 0},
	} {
		if flag.set {
			flags = append(flags, flag.name)
		}
	}
	return flags
}

func dnsOpcodeName(opcode int) string {
	names := []string{"QUERY", "IQUERY", "STATUS", "3", "NOTIFY", "UPDATE"}
	if opcode >= 0 && opcode < len(names) {
		return names[opcode]
	}
	return strconv.Itoa(opcode)
}

func parseHostOptions(args []string) (hostOptions, string, string, error) {
	opts := hostOptions{class: "IN", port: 53, wait: 5 * time.Second, retries: 1}
	name, server := "", ""
	expanded := expandShortOptions(args, "cmNpRtWk")
	// value consumes the operand that follows an option, advancing the loop
	// index past it.
	index := 0
	value := func() (string, error) {
		if index+1 >= len(expanded) {
			return "", fmt.Errorf("option %s requires a value", expanded[index])
		}
		index++
		return expanded[index], nil
	}
	for ; index < len(expanded); index++ {
		argument := expanded[index]
		var err error
		switch argument {
		case "-4":
			opts.family = 4
		case "-6":
			opts.family = 6
		case "-a":
			opts.verbose, opts.types = true, []string{"ANY"}
		case "-d", "-v":
			opts.verbose = true
		case "-r":
			opts.norecurse = true
		case "-s":
			// One server is asked at a time here, so a SERVFAIL already stops
			// the query rather than moving on.
		case "-T":
			opts.tcp = true
		case "-U":
			opts.udp = true
		case "-w":
			opts.wait = time.Hour
		case "-c":
			text, valueErr := value()
			if valueErr != nil {
				return opts, "", "", valueErr
			}
			opts.class = strings.ToUpper(text)
			if _, ok := dnsClassCode(opts.class); !ok {
				return opts, "", "", fmt.Errorf("unsupported query class %q", text)
			}
		case "-t":
			text, valueErr := value()
			if valueErr != nil {
				return opts, "", "", valueErr
			}
			kind := strings.ToUpper(text)
			if _, ok := dnsTypeCode(kind); !ok {
				return opts, "", "", fmt.Errorf("unsupported query type %q", text)
			}
			opts.types = []string{kind}
		case "-p":
			if opts.port, err = hostNumber(value, 1, 65535); err != nil {
				return opts, "", "", err
			}
		case "-R":
			if opts.retries, err = hostNumber(value, 0, 100); err != nil {
				return opts, "", "", err
			}
			if opts.retries < 1 {
				opts.retries = 1
			}
		case "-W":
			seconds := 0
			if seconds, err = hostNumber(value, 1, 86400); err != nil {
				return opts, "", "", err
			}
			opts.wait = time.Duration(seconds) * time.Second
		case "-A", "-C", "-l", "-m", "-N", "-k", "-V":
			return opts, "", "", fmt.Errorf("unsupported option %s", argument)
		default:
			if strings.HasPrefix(argument, "-") && argument != "-" {
				return opts, "", "", fmt.Errorf("invalid option %s", argument)
			}
			switch {
			case name == "":
				name = argument
			case server == "":
				server = argument
			default:
				return opts, "", "", fmt.Errorf("unexpected operand %q", argument)
			}
		}
	}
	return opts, name, server, nil
}

// hostNumber reads a bounded integer option value.
func hostNumber(value func() (string, error), low, high int) (int, error) {
	text, err := value()
	if err != nil {
		return 0, err
	}
	number, err := strconv.Atoi(text)
	if err != nil || number < low || number > high {
		return 0, fmt.Errorf("invalid value %q", text)
	}
	return number, nil
}
