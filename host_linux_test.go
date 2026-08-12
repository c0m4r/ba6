// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startTestResolver answers one query per connection from a canned reply
// builder, and returns the address it listens on.
func startTestResolver(t *testing.T, reply func(query []byte) []byte) (string, string) {
	t.Helper()
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local UDP unavailable: %v", err)
	}
	t.Cleanup(func() { server.Close() })
	go func() {
		buffer := make([]byte, 512)
		for {
			n, address, readErr := server.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			if _, writeErr := server.WriteTo(reply(buffer[:n]), address); writeErr != nil {
				return
			}
		}
	}()
	address, port, err := net.SplitHostPort(server.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	return address, port
}

// dnsReply turns a query into a response with the given rcode and answer
// records, which are appended verbatim after a compression pointer to the
// question name.
func dnsReply(query []byte, rcode byte, answers ...[]byte) []byte {
	response := append([]byte(nil), query...)
	response[2], response[3] = 0x81, 0x80|rcode
	binary.BigEndian.PutUint16(response[6:8], uint16(len(answers))) //nolint:gosec // G115: fixtures hold a handful of records.
	for _, answer := range answers {
		response = append(response, answer...)
	}
	return response
}

// dnsAnswer builds one answer record for the question name.
func dnsAnswer(kind uint16, data []byte) []byte {
	answer := []byte{0xc0, 0x0c}
	answer = binary.BigEndian.AppendUint16(answer, kind)
	answer = binary.BigEndian.AppendUint16(answer, 1)
	answer = binary.BigEndian.AppendUint32(answer, 60)
	answer = binary.BigEndian.AppendUint16(answer, uint16(len(data))) //nolint:gosec // G115: fixture record data is a few bytes.
	return append(answer, data...)
}

func TestHostReportsAddressesAndBanner(t *testing.T) {
	address, port := startTestResolver(t, func(query []byte) []byte {
		return dnsReply(query, 0, dnsAnswer(1, []byte{192, 0, 2, 1}))
	})
	status, stdout, stderr := captureApplet(t, cmdHost,
		[]string{"-p", port, "-t", "A", "example.test", address}, "")
	want := "Using domain server:\nName: " + address + "\nAddress: " + address + "#" + port +
		"\nAliases: \n\nexample.test has address 192.0.2.1\n"
	if status != 0 || stdout != want {
		t.Fatalf("host = (%d, %q, %q), want %q", status, stdout, stderr, want)
	}
}

func TestHostReportsMissingNameAndMissingRecord(t *testing.T) {
	address, port := startTestResolver(t, func(query []byte) []byte {
		// The question name decides the outcome: "nope" does not exist, while
		// the other name exists without the record that was asked for.
		if strings.Contains(string(query), "nope") {
			return dnsReply(query, 3)
		}
		return dnsReply(query, 0)
	})
	status, stdout, _ := captureApplet(t, cmdHost, []string{"-p", port, "nope.test", address}, "")
	if status != 1 || !strings.HasSuffix(stdout, "Host nope.test not found: 3(NXDOMAIN)\n") {
		t.Fatalf("NXDOMAIN lookup = (%d, %q)", status, stdout)
	}
	status, stdout, _ = captureApplet(t, cmdHost, []string{"-p", port, "-t", "MX", "example.test", address}, "")
	if status != 0 || !strings.HasSuffix(stdout, "example.test has no MX record\n") {
		t.Fatalf("empty answer = (%d, %q)", status, stdout)
	}
}

// The default question set stays silent when the name exists but holds none of
// the records it asks for.
func TestHostDefaultQuestionSetIsSilentWithoutRecords(t *testing.T) {
	asked := make(chan string, len(hostDefaultTypes))
	address, port := startTestResolver(t, func(query []byte) []byte {
		kind := binary.BigEndian.Uint16(query[len(query)-4 : len(query)-2])
		asked <- dnsTypeName(kind)
		return dnsReply(query, 0)
	})
	status, stdout, stderr := captureApplet(t, cmdHost, []string{"-p", port, "example.test", address}, "")
	if status != 0 || !strings.HasSuffix(stdout, "Aliases: \n\n") || stderr != "" {
		t.Fatalf("default lookup = (%d, %q, %q)", status, stdout, stderr)
	}
	close(asked)
	var types []string
	for kind := range asked {
		types = append(types, kind)
	}
	if strings.Join(types, ",") != strings.Join(hostDefaultTypes, ",") {
		t.Fatalf("asked for %q, want %q", types, hostDefaultTypes)
	}
}

func TestHostUnreachableServerIsReported(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local UDP unavailable: %v", err)
	}
	address, port, err := net.SplitHostPort(listener.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	// Closing the socket leaves a port nothing is listening on.
	listener.Close()
	status, stdout, _ := captureApplet(t, cmdHost,
		[]string{"-p", port, "-W", "1", "-t", "A", "example.test", address}, "")
	if status != 1 || !strings.Contains(stdout, "communications error to "+address+"#"+port) ||
		!strings.HasSuffix(stdout, ";; no servers could be reached\n") {
		t.Fatalf("unreachable server = (%d, %q)", status, stdout)
	}
}

func TestHostVerboseShowsEverySection(t *testing.T) {
	address, port := startTestResolver(t, func(query []byte) []byte {
		return dnsReply(query, 0, dnsAnswer(1, []byte{192, 0, 2, 1}))
	})
	status, stdout, _ := captureApplet(t, cmdHost,
		[]string{"-p", port, "-v", "-t", "A", "example.test", address}, "")
	for _, fragment := range []string{
		"Trying \"example.test\"\n",
		";; ->>HEADER<<- opcode: QUERY, status: NOERROR, id: ",
		";; flags: qr rd ra; QUERY: 1, ANSWER: 1, AUTHORITY: 0, ADDITIONAL: 0\n",
		"\n;; QUESTION SECTION:\n;example.test.\t\t\tIN\tA\n",
		"\n;; ANSWER SECTION:\nexample.test.\t\t60\tIN\tA\t192.0.2.1\n",
		"Received ", " bytes from " + address + "#" + port + " in ",
	} {
		if !strings.Contains(stdout, fragment) {
			t.Errorf("verbose output is missing %q:\n%s", fragment, stdout)
		}
	}
	if status != 0 || strings.Contains(stdout, "has address") {
		t.Fatalf("verbose lookup = (%d, %q)", status, stdout)
	}
}

func TestHostReverseLookupUsesPointerName(t *testing.T) {
	var asked string
	address, port := startTestResolver(t, func(query []byte) []byte {
		name, _, err := decodeDNSName(query, 12, 0)
		if err == nil {
			asked = name
		}
		return dnsReply(query, 0, dnsAnswer(12, append([]byte{4}, append([]byte("host"),
			append([]byte{4}, append([]byte("test"), 0)...)...)...)))
	})
	status, stdout, _ := captureApplet(t, cmdHost, []string{"-p", port, "192.0.2.55", address}, "")
	if status != 0 || !strings.HasSuffix(stdout, "55.2.0.192.in-addr.arpa domain name pointer host.test.\n") {
		t.Fatalf("reverse lookup = (%d, %q)", status, stdout)
	}
	if asked != "55.2.0.192.in-addr.arpa." {
		t.Fatalf("queried %q", asked)
	}
}

func TestReverseDNSName(t *testing.T) {
	tests := []struct{ address, want string }{
		{"1.2.3.4", "4.3.2.1.in-addr.arpa."},
		{"::1", "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.ip6.arpa."},
		{"2001:db8::1", "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa."},
	}
	for _, test := range tests {
		if got := reverseDNSName(net.ParseIP(test.address)); got != test.want {
			t.Errorf("reverseDNSName(%s) = %q, want %q", test.address, got, test.want)
		}
	}
}

func TestHostRecordWording(t *testing.T) {
	tests := []struct {
		record dnsRecord
		want   string
	}{
		{dnsRecord{name: "a.test.", kind: "A", value: "192.0.2.1"}, "a.test has address 192.0.2.1"},
		{dnsRecord{name: "a.test.", kind: "AAAA", value: "2001:db8::1"}, "a.test has IPv6 address 2001:db8::1"},
		{dnsRecord{name: "a.test.", kind: "MX", value: "10 m.test."}, "a.test mail is handled by 10 m.test."},
		{dnsRecord{name: "a.test.", kind: "CNAME", value: "b.test."}, "a.test is an alias for b.test."},
		{dnsRecord{name: "a.test.", kind: "NS", value: "n.test."}, "a.test name server n.test."},
		{dnsRecord{name: "a.test.", kind: "TXT", value: `"v=spf1 -all"`}, `a.test descriptive text "v=spf1 -all"`},
		{dnsRecord{name: "a.test.", kind: "HTTPS", value: `1 . alpn="h2"`}, `a.test has HTTP service bindings 1 . alpn="h2"`},
		{dnsRecord{name: "a.test.", kind: "SRV", value: "0 5 5060 s.test."}, "a.test has SRV record 0 5 5060 s.test."},
	}
	for _, test := range tests {
		if got := hostRecordLine(test.record); got != test.want {
			t.Errorf("hostRecordLine(%+v) = %q, want %q", test.record, got, test.want)
		}
	}
}

// The master-file layout pads with tabs to fixed columns, and falls back to a
// single space once a field has overflowed its column.
func TestDNSMasterLineColumns(t *testing.T) {
	short := dnsMasterLine(dnsRecord{name: "example.com.", kind: "A", class: "IN", ttl: 64, value: "192.0.2.1"})
	if short != "example.com.\t\t64\tIN\tA\t192.0.2.1" {
		t.Errorf("short name line = %q", short)
	}
	long := dnsMasterLine(dnsRecord{
		name: "elliott.ns.cloudflare.com.", kind: "A", class: "IN", ttl: 27606, value: "192.0.2.1",
	})
	if long != "elliott.ns.cloudflare.com. 27606 IN\tA\t192.0.2.1" {
		t.Errorf("long name line = %q", long)
	}
}

func TestServiceBindingPresentation(t *testing.T) {
	// priority 1, root target, then alpn, ipv4hint and port parameters.
	record := append(make([]byte, 0, 27), 0x00, 0x01, 0x00)
	record = append(record, 0x00, 0x01, 0x00, 0x06, 0x02, 'h', '3', 0x02, 'h', '2')
	record = append(record, 0x00, 0x04, 0x00, 0x04, 192, 0, 2, 1)
	record = append(record, 0x00, 0x03, 0x00, 0x02, 0x01, 0xbb)
	message := append(make([]byte, 12), record...)
	value, err := formatDNSRData(message, 12, len(record), 65)
	want := `1 . alpn="h3,h2" ipv4hint=192.0.2.1 port=443`
	if err != nil || value != want {
		t.Fatalf("HTTPS record = (%q, %v), want %q", value, err, want)
	}
}

func TestParseHostOptions(t *testing.T) {
	opts, name, server, err := parseHostOptions([]string{"-4", "-t", "mx", "-W2", "-p", "5353", "a.test", "b.test"})
	if err != nil || name != "a.test" || server != "b.test" || opts.family != 4 ||
		len(opts.types) != 1 || opts.types[0] != "MX" || opts.wait != 2*time.Second || opts.port != 5353 {
		t.Fatalf("opts=%+v name=%q server=%q err=%v", opts, name, server, err)
	}
	if opts, _, _, err := parseHostOptions([]string{"-a", "x.test"}); err != nil ||
		!opts.verbose || len(opts.types) != 1 || opts.types[0] != "ANY" {
		t.Fatalf("-a gave opts=%+v err=%v", opts, err)
	}
	for _, args := range [][]string{
		{"-t", "NOSUCH", "a.test"},
		{"-c", "XX", "a.test"},
		{"-p", "0", "a.test"},
		{"-W", "x", "a.test"},
		{"-l", "a.test"},
		{"-Z", "a.test"},
		{"a.test", "b.test", "c.test"},
		{"-t"},
	} {
		if _, _, _, err := parseHostOptions(args); err == nil {
			t.Errorf("parseHostOptions(%q) was accepted", args)
		}
	}
}

func TestHostNeedsUnrestrictedSyscalls(t *testing.T) {
	if !appletNeedsUnrestrictedSyscalls("host") {
		t.Error("host must bypass the socket-denying seccomp profile")
	}
	if appletNeedsUnrestrictedSyscalls("less") {
		t.Error("less does not open sockets and must keep the default profile")
	}
}

func TestDNSClassRoundTrip(t *testing.T) {
	for name, code := range map[string]uint16{"IN": 1, "CH": 3, "HS": 4} {
		if got, ok := dnsClassCode(name); !ok || got != code {
			t.Errorf("dnsClassCode(%s) = (%d, %v)", name, got, ok)
		}
		if got := dnsClassName(code); got != name {
			t.Errorf("dnsClassName(%d) = %q", code, got)
		}
	}
	if got := dnsClassName(99); got != "CLASS"+strconv.Itoa(99) {
		t.Errorf("unknown class = %q", got)
	}
}
