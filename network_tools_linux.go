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
	"strconv"
	"strings"
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
	for _, a := range args {
		switch {
		case a == "--all":
			all = true
		case a == "--listening":
			listen = true
		case a == "--tcp":
			tcp = true
		case a == "--udp":
			udp = true
		case a == "--unix":
			unix = true
		// Short options bundle, so -tuln is the same as -t -u -l -n.
		case len(a) > 1 && a[0] == '-':
			for _, f := range a[1:] {
				switch f {
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
				case 'n', 'p':
					// Addresses are always numeric here, and process
					// information is never shown.
				default:
					fatalf("ss", "unsupported option -%c", f)
					return 1
				}
			}
		default:
			fatalf("ss", "unsupported operand %q", a)
			return 1
		}
	}
	if !tcp && !udp && !unix {
		tcp, udp, unix = true, true, true
	}
	fmt.Println("Netid State  Local Address:Port       Peer Address:Port")
	if tcp {
		readSocketTable("tcp", "/proc/net/tcp", listen, all)
		readSocketTable("tcp6", "/proc/net/tcp6", listen, all)
	}
	if udp {
		readSocketTable("udp", "/proc/net/udp", listen, all)
		readSocketTable("udp6", "/proc/net/udp6", listen, all)
	}
	if unix {
		readUnixSockets(listen, all)
	}
	return 0
}
func readSocketTable(kind, path string, listen, all bool) {
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
		isListen := state == "LISTEN" || kind[:3] == "udp" && f[2] == "00000000:0000"
		if listen && !isListen || !listen && !all && isListen {
			continue
		}
		fmt.Printf("%-5s %-6s %-24s %s\n", kind, state, decodeSocketAddr(f[1]), decodeSocketAddr(f[2]))
	}
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
	separator := strings.LastIndexByte(value, ':')
	if separator < 0 {
		return value
	}
	digits := value[:separator]
	if len(digits)%8 != 0 || len(digits) == 0 {
		return value
	}
	address := make(net.IP, 0, len(digits)/2)
	for start := 0; start < len(digits); start += 8 {
		word, err := strconv.ParseUint(digits[start:start+8], 16, 32)
		if err != nil {
			return value
		}
		//nolint:gosec // The word is 32 bits and each conversion takes one byte of it.
		address = append(address, byte(word), byte(word>>8), byte(word>>16), byte(word>>24))
	}
	// An unset port is a wildcard, and so is an unspecified IPv6 address.
	port := "*"
	if number, err := strconv.ParseUint(value[separator+1:], 16, 16); err == nil && number != 0 {
		port = strconv.FormatUint(number, 10)
	}
	switch {
	case len(address) == net.IPv6len && address.IsUnspecified():
		return "*:" + port
	case len(address) == net.IPv6len:
		return "[" + address.String() + "]:" + port
	default:
		return address.String() + ":" + port
	}
}
func readUnixSockets(listen, all bool) {
	data, e := os.ReadFile("/proc/net/unix")
	if e != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		state := "UNCONN"
		if f[5] == "01" {
			state = "LISTEN"
		}
		if listen && state != "LISTEN" || !listen && !all && state == "LISTEN" {
			continue
		}
		path := ""
		if len(f) > 7 {
			path = f[7]
		}
		fmt.Printf("%-5s %-6s %-24s %s\n", "u_str", state, path, "*")
	}
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
