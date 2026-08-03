//go:build linux

package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	host := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c":
			i++
			if i >= len(args) {
				return 2
			}
			count, _ = strconv.Atoi(args[i])
		case "-W":
			i++
			if i >= len(args) {
				return 2
			}
			n, _ := strconv.ParseFloat(args[i], 64)
			timeout = time.Duration(n * float64(time.Second))
		case "-i":
			i++
			if i >= len(args) {
				return 2
			}
			n, _ := strconv.ParseFloat(args[i], 64)
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
		if candidate.To4() != nil {
			ip = candidate.To4()
			break
		}
	}
	if ip == nil {
		fatalf("ping", "IPv4 address required")
		return 2
	}
	conn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
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
		packet[0] = 8
		binary.BigEndian.PutUint16(packet[4:6], uint16(id))
		binary.BigEndian.PutUint16(packet[6:8], uint16(seq))
		binary.BigEndian.PutUint64(packet[8:16], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint16(packet[2:4], icmpChecksum(packet))
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
				offset := 0
				if n >= 20 && buf[0]>>4 == 4 {
					offset = int(buf[0]&15) * 4
				}
				if n >= offset+8 && buf[offset] == 0 && int(binary.BigEndian.Uint16(buf[offset+4:offset+6])) == id && int(binary.BigEndian.Uint16(buf[offset+6:offset+8])) == seq {
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
		if a == "-a" || a == "--all" {
			all = true
			continue
		}
		if a == "-l" || a == "--listening" {
			listen = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			for _, f := range strings.TrimPrefix(a, "-") {
				switch f {
				case 't':
					tcp = true
				case 'u':
					udp = true
				case 'x':
					unix = true
				case 'n', 'p':
				default:
					fatalf("ss", "unsupported option -%c", f)
					return 1
				}
			}
		} else {
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
func decodeSocketAddr(value string) string {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return value
	}
	port, _ := strconv.ParseUint(parts[1], 16, 16)
	raw, err := strconv.ParseUint(parts[0], 16, 32)
	if err == nil && len(parts[0]) == 8 {
		return fmt.Sprintf("%d.%d.%d.%d:%d", raw&0xff, raw>>8&0xff, raw>>16&0xff, raw>>24&0xff, port)
	}
	return "[" + parts[0] + "]:" + strconv.FormatUint(port, 10)
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

func cmdWget(args []string) int { return httpFetch("wget", args) }
func cmdCurl(args []string) int { return httpFetch("curl", args) }
func httpFetch(prog string, args []string) int {
	output, method, data := "", "GET", ""
	headers := http.Header{}
	head, quiet, follow := false, false, prog == "wget"
	target := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-O", "-o", "--output":
			i++
			if i >= len(args) {
				return 2
			}
			output = args[i]
		case "-q", "--quiet", "-s", "--silent":
			quiet = true
		case "-L", "--location":
			follow = true
		case "-I", "--head":
			head = true
			method = "HEAD"
		case "-X", "--request":
			i++
			if i >= len(args) {
				return 2
			}
			method = args[i]
		case "-d", "--data":
			i++
			if i >= len(args) {
				return 2
			}
			data = args[i]
			if method == "GET" {
				method = "POST"
			}
		case "-H", "--header":
			i++
			if i >= len(args) {
				return 2
			}
			p := strings.SplitN(args[i], ":", 2)
			if len(p) == 2 {
				headers.Add(strings.TrimSpace(p[0]), strings.TrimSpace(p[1]))
			}
		case "--":
			if i+1 < len(args) {
				target = args[i+1]
			}
			i = len(args)
		default:
			if strings.HasPrefix(a, "-") {
				fatalf(prog, "unsupported option %q", a)
				return 2
			}
			target = a
		}
	}
	if target == "" {
		fatalf(prog, "missing URL")
		return 2
	}
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	req, err := http.NewRequest(method, target, strings.NewReader(data)) //nolint:gosec // G704: fetching a user-selected URL is this applet's purpose.
	if err != nil {
		fatalf(prog, "%v", err)
		return 2
	}
	req.Header = headers
	client := &http.Client{Timeout: 60 * time.Second}
	if !follow {
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := client.Do(req) //nolint:gosec // G704: fetching a user-selected URL is this applet's purpose.
	if err != nil {
		fatalf(prog, "%v", err)
		return 1
	}
	defer resp.Body.Close()
	if !quiet {
		fmt.Fprintf(os.Stderr, "%s: %s\n", prog, resp.Status)
	}
	if head {
		if err := resp.Header.Write(os.Stdout); err != nil {
			return 1
		}
		return 0
	}
	var w io.Writer = os.Stdout
	var file *os.File
	if output != "" && output != "-" {
		file, err = os.Create(output)
		if err != nil {
			fatalf(prog, "%v", err)
			return 1
		}
		defer file.Close()
		w = file
	} else if output == "" && prog == "wget" {
		parsed, _ := url.Parse(target)
		name := strings.TrimSuffix(parsed.Path, "/")
		name = name[strings.LastIndex(name, "/")+1:]
		if name == "" {
			name = "index.html"
		}
		file, err = os.Create(name)
		if err != nil {
			return 1
		}
		defer file.Close()
		w = file
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, 1<<34))
	if err != nil {
		fatalf(prog, "%v", err)
		return 1
	}
	if resp.StatusCode >= 400 {
		return 1
	}
	return 0
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
