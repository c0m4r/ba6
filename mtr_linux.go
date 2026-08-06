//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The report and full-screen layouts below reproduce mtr's column geometry:
// eight fixed-width statistics fields follow a variable-width host column, so
// the header, the group caption, and every hop row line up at the same offset.
const (
	mtrTitle       = "My traceroute  [ba6]"
	mtrKeys        = "Keys:  Help   Toggle DNS   Restart statistics   Pause   quit"
	mtrGroupHeader = "   Packets               Pings"
	mtrFieldHeader = " Loss%" + "   Snt" + " " + "  Last" + "   Avg" + "  Best" + "  Wrst" + " StDev"
	mtrFieldFormat = " %4.1f%%" + " %5d" + " " + " %5.1f" + " %5.1f" + " %5.1f" + " %5.1f" + " %5.1f"
	mtrFieldWidth  = len(mtrFieldHeader)
	mtrHostWidth   = 28
	mtrTimestamp   = "2006-01-02T15:04:05-0700"
	mtrMissLimit   = 5
	mtrUnknownHost = "???"
)

type mtrOptions struct {
	cycles, maxHops, firstHop, packetSize int
	interval, timeout                     time.Duration
	family                                int
	numeric, showIPs, report, wide        bool
	udp, icmp                             bool
}

var mtrValueOptions = map[string]bool{
	"-c": true, "--report-cycles": true,
	"-m": true, "--max-ttl": true,
	"-f": true, "--first-ttl": true,
	"-s": true, "--psize": true,
	"-i": true, "--interval": true,
	"-Z": true, "--timeout": true,
}

func cmdMtr(args []string) int {
	opts, host, err := parseMtrOptions(args)
	if err != nil {
		fatalf("mtr", "%v", err)
		return 2
	}
	target, family, err := resolveTraceHost(host, opts.family)
	if err != nil {
		fatalf("mtr", "%v", err)
		return 1
	}
	opts.family = family
	live := !opts.report && isTerminal(os.Stdout.Fd()) && isTerminal(os.Stdin.Fd())
	if opts.cycles == 0 && !live {
		opts.cycles = 10
	}
	prober, err := newMtrProber(target, family, opts)
	if err != nil {
		fatalf("mtr", "%v", err)
		return 1
	}
	defer prober.close()
	session := newMtrSession(host, target, family, opts)
	if live {
		return runMtrLive(session, prober)
	}
	return runMtrReport(session, prober)
}

func parseMtrOptions(args []string) (mtrOptions, string, error) {
	opts := mtrOptions{maxHops: 30, firstHop: 1, packetSize: 56, interval: time.Second, timeout: time.Second}
	host := ""
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if option, value, ok := splitMtrOption(argument); ok {
			if err := applyMtrValue(&opts, option, value); err != nil {
				return opts, "", err
			}
			continue
		}
		if mtrValueOptions[argument] {
			remaining := args[index+1:]
			if len(remaining) == 0 {
				return opts, "", fmt.Errorf("%s requires a value", argument)
			}
			index++
			if err := applyMtrValue(&opts, argument, remaining[0]); err != nil {
				return opts, "", err
			}
			continue
		}
		switch argument {
		case "-4":
			opts.family = 4
		case "-6":
			opts.family = 6
		case "-n", "--no-dns":
			opts.numeric = true
		case "-b", "--show-ips":
			opts.showIPs = true
		case "-r", "--report":
			opts.report = true
		case "-w", "--report-wide":
			opts.report, opts.wide = true, true
		case "-u", "--udp":
			opts.udp = true
		case "-I", "--icmp":
			opts.icmp = true
		default:
			if strings.HasPrefix(argument, "-") && argument != "-" {
				return opts, "", fmt.Errorf("unsupported option %q", argument)
			}
			if host != "" {
				return opts, "", fmt.Errorf("unexpected operand %q", argument)
			}
			host = argument
		}
	}
	if host == "" {
		return opts, "", errors.New("missing host")
	}
	if opts.udp && opts.icmp {
		return opts, "", errors.New("-u and -I are mutually exclusive")
	}
	if opts.firstHop > opts.maxHops {
		return opts, "", fmt.Errorf("first hop %d is past max hop %d", opts.firstHop, opts.maxHops)
	}
	return opts, host, nil
}

// splitMtrOption recognises the glued forms getopt accepts, so both "-c10" and
// "--report-cycles=10" reach applyMtrValue like their spaced spellings do.
func splitMtrOption(argument string) (string, string, bool) {
	if strings.HasPrefix(argument, "--") {
		option, value, found := strings.Cut(argument, "=")
		return option, value, found && mtrValueOptions[option]
	}
	if len(argument) > 2 && argument[0] == '-' && mtrValueOptions[argument[:2]] {
		return argument[:2], argument[2:], true
	}
	return "", "", false
}

func applyMtrValue(opts *mtrOptions, option, value string) error {
	switch option {
	case "-c", "--report-cycles":
		return assignMtrCount(&opts.cycles, option, value, 1, 100000)
	case "-m", "--max-ttl":
		return assignMtrCount(&opts.maxHops, option, value, 1, 255)
	case "-f", "--first-ttl":
		return assignMtrCount(&opts.firstHop, option, value, 1, 255)
	case "-s", "--psize":
		return assignMtrCount(&opts.packetSize, option, value, 16, 1400)
	case "-i", "--interval":
		return assignMtrSeconds(&opts.interval, option, value, 0.01, 3600)
	case "-Z", "--timeout":
		return assignMtrSeconds(&opts.timeout, option, value, 0.01, 60)
	}
	return fmt.Errorf("unsupported option %q", option)
}

func assignMtrCount(target *int, option, value string, low, high int) error {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < low || parsed > high {
		return fmt.Errorf("invalid %s value %q", option, value)
	}
	*target = parsed
	return nil
}

func assignMtrSeconds(target *time.Duration, option, value string, low, high float64) error {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < low || parsed > high {
		return fmt.Errorf("invalid %s value %q", option, value)
	}
	*target = time.Duration(parsed * float64(time.Second))
	return nil
}

type mtrHop struct {
	address               string
	sent, received        int
	last, best, worst     float64
	total, totalOfSquares float64
}

func (hop *mtrHop) record(delay time.Duration) {
	milliseconds := float64(delay.Microseconds()) / 1000
	hop.received++
	hop.last = milliseconds
	if hop.received == 1 || milliseconds < hop.best {
		hop.best = milliseconds
	}
	if milliseconds > hop.worst {
		hop.worst = milliseconds
	}
	hop.total += milliseconds
	hop.totalOfSquares += milliseconds * milliseconds
}

func (hop mtrHop) loss() float64 {
	if hop.sent == 0 {
		return 0
	}
	return 100 * float64(hop.sent-hop.received) / float64(hop.sent)
}

func (hop mtrHop) average() float64 {
	if hop.received == 0 {
		return 0
	}
	return hop.total / float64(hop.received)
}

// stdev is the sample standard deviation mtr reports, computed from the
// running sums so no per-probe history has to be kept.
func (hop mtrHop) stdev() float64 {
	if hop.received < 2 {
		return 0
	}
	count := float64(hop.received)
	variance := (hop.totalOfSquares - hop.total*hop.total/count) / (count - 1)
	if variance <= 0 {
		return 0
	}
	return math.Sqrt(variance)
}

func (hop mtrHop) fields() string {
	return fmt.Sprintf(mtrFieldFormat, hop.loss(), hop.sent, hop.last, hop.average(), hop.best, hop.worst, hop.stdev())
}

// mtrResolver caches reverse lookups. The full-screen display resolves in the
// background so a slow PTR never stalls the probe loop, while report mode
// waits because it prints its table exactly once.
type mtrResolver struct {
	mutex   sync.Mutex
	names   map[string]string
	pending map[string]bool
}

func newMtrResolver() *mtrResolver {
	return &mtrResolver{names: map[string]string{}, pending: map[string]bool{}}
}

func (resolver *mtrResolver) name(address string, wait bool) string {
	resolver.mutex.Lock()
	if name, ok := resolver.names[address]; ok {
		resolver.mutex.Unlock()
		return name
	}
	if wait {
		resolver.mutex.Unlock()
		return resolver.lookup(address)
	}
	if !resolver.pending[address] {
		resolver.pending[address] = true
		go func() { _ = resolver.lookup(address) }()
	}
	resolver.mutex.Unlock()
	return address
}

func (resolver *mtrResolver) lookup(address string) string {
	name := address
	if names, err := net.LookupAddr(address); err == nil && len(names) > 0 {
		name = strings.TrimSuffix(names[0], ".")
	}
	resolver.mutex.Lock()
	resolver.names[address] = name
	resolver.mutex.Unlock()
	return name
}

type mtrSession struct {
	opts               mtrOptions
	host               string
	target             net.IP
	family             int
	localName, localIP string
	hops               []mtrHop
	limit, sequence    int
	found              bool
	resolver           *mtrResolver
	started            time.Time
}

func newMtrSession(host string, target net.IP, family int, opts mtrOptions) *mtrSession {
	session := &mtrSession{
		opts:     opts,
		host:     host,
		target:   target,
		family:   family,
		hops:     make([]mtrHop, opts.maxHops),
		limit:    opts.maxHops,
		resolver: newMtrResolver(),
		started:  time.Now(),
	}
	session.localName, _ = os.Hostname()
	if session.localName == "" {
		session.localName = "localhost"
	}
	session.localIP = mtrSourceAddress(target)
	return session
}

// mtrSourceAddress reports the address the kernel would source probes from.
// Connecting a UDP socket only installs a route; it sends nothing.
func mtrSourceAddress(target net.IP) string {
	connection, err := net.Dial("udp", net.JoinHostPort(target.String(), "33434")) //nolint:gosec // G704: the probe target is this applet's operand.
	if err != nil {
		return ""
	}
	defer connection.Close()
	address, ok := connection.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return address.IP.String()
}

// probeCycle sends one probe per hop. after runs once per probe and returns
// false to abandon the sweep, which is how the interactive display reacts to a
// quit key without waiting for the whole cycle.
func (session *mtrSession) probeCycle(prober mtrProber, after func() bool) error {
	misses := 0
	for ttl := session.opts.firstHop; ttl <= session.limit; ttl++ {
		session.sequence = (session.sequence + 1) & 0xffff
		reply := prober.probe(ttl, session.sequence, session.opts.timeout)
		if reply.err != nil {
			return reply.err
		}
		hop := &session.hops[ttl-1]
		hop.sent++
		if reply.ok {
			misses = 0
			hop.address = reply.address
			hop.record(reply.delay)
			if reply.reached {
				session.found, session.limit = true, ttl
			}
		} else {
			misses++
			if !session.found && misses >= mtrMissLimit {
				session.limit = ttl
			}
		}
		if after != nil && !after() {
			return nil
		}
	}
	return nil
}

func (session *mtrSession) reset() {
	for index := range session.hops {
		session.hops[index] = mtrHop{address: session.hops[index].address}
	}
	session.found, session.started = false, time.Now()
}

// displayRange is the half-open hop range both layouts show. Probing can start
// past the first hop with -f, and trailing unanswered hops stay visible as
// "???" exactly as mtr shows them.
func (session *mtrSession) displayRange() (int, int) {
	start := session.opts.firstHop - 1
	if start < 0 {
		start = 0
	}
	end := start
	for index := start; index < len(session.hops); index++ {
		if session.hops[index].sent > 0 {
			end = index + 1
		}
	}
	return start, end
}

func (session *mtrSession) hopName(hop mtrHop) string {
	if hop.address == "" {
		return mtrUnknownHost
	}
	if session.opts.numeric {
		return hop.address
	}
	name := session.resolver.name(hop.address, session.opts.report)
	if name == hop.address || !session.opts.showIPs {
		return name
	}
	return name + " (" + hop.address + ")"
}

func runMtrReport(session *mtrSession, prober mtrProber) int {
	next := time.Now()
	for cycle := 0; cycle < session.opts.cycles; cycle++ {
		if wait := time.Until(next); wait > 0 {
			time.Sleep(wait)
		}
		next = time.Now().Add(session.opts.interval)
		if err := session.probeCycle(prober, nil); err != nil {
			fatalf("mtr", "%v", err)
			return 1
		}
	}
	if err := writeMtrReport(os.Stdout, session); err != nil {
		fatalf("mtr", "write error: %v", err)
		return 1
	}
	return 0
}

func writeMtrReport(w io.Writer, session *mtrSession) error {
	width := session.hostColumn()
	if _, err := fmt.Fprintf(w, "Start: %s\n", session.started.Format(mtrTimestamp)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "HOST: "+mtrPad(session.localName, width+2)+mtrFieldHeader); err != nil {
		return err
	}
	start, end := session.displayRange()
	for index := start; index < end; index++ {
		hop := session.hops[index]
		line := fmt.Sprintf("%3d.|-- ", index+1) + mtrPad(session.hopName(hop), width) + hop.fields()
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// hostColumn keeps the report at 80 columns by truncating names, unless -w
// asked for the wide report that sizes the column to its content.
func (session *mtrSession) hostColumn() int {
	width := mtrHostWidth
	if !session.opts.wide {
		return width
	}
	if length := len(session.localName) - 2; length > width {
		width = length
	}
	start, end := session.displayRange()
	for index := start; index < end; index++ {
		if length := len(session.hopName(session.hops[index])); length > width {
			width = length
		}
	}
	return width
}

func mtrPad(text string, width int) string {
	if len(text) >= width {
		return text[:width]
	}
	return text + strings.Repeat(" ", width-len(text))
}

func mtrClip(text string, width int) string {
	if len(text) > width {
		return text[:width]
	}
	return text
}

func mtrCenter(text string, width int) string {
	if len(text) >= width {
		return mtrClip(text, width)
	}
	return strings.Repeat(" ", (width-len(text))/2) + text
}

// mtrLiveState is the interactive display state driven by the key reader.
type mtrLiveState struct {
	quit, paused, help bool
}

func runMtrLive(session *mtrSession, prober mtrProber) int {
	descriptor := os.Stdin.Fd()
	previous, err := terminalRaw(descriptor)
	if err != nil {
		return runMtrReport(session, prober)
	}
	defer restoreTerminal(descriptor, previous)
	fmt.Fprint(os.Stdout, "\x1b[?25l\x1b[2J")
	keys := mtrKeyReader()
	state := &mtrLiveState{}
	render := func() { fmt.Fprint(os.Stdout, session.liveFrame(state)) }
	render()
	after := func() bool {
		render()
		session.waitKeys(keys, state, 0, render)
		for state.paused && !state.quit {
			session.waitKeys(keys, state, time.Second, render)
		}
		return !state.quit
	}
	var failure error
	next := time.Now()
	for cycle := 0; !state.quit && (session.opts.cycles == 0 || cycle < session.opts.cycles); cycle++ {
		session.waitKeys(keys, state, time.Until(next), render)
		next = time.Now().Add(session.opts.interval)
		if state.quit {
			break
		}
		if failure = session.probeCycle(prober, after); failure != nil {
			break
		}
	}
	fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H\x1b[?25h")
	restoreTerminal(descriptor, previous)
	if failure != nil {
		fatalf("mtr", "%v", failure)
		return 1
	}
	return 0
}

// mtrKeyReader forwards raw-mode keystrokes; the channel closes on EOF so the
// display loop also exits when stdin disappears.
func mtrKeyReader() <-chan byte {
	keys := make(chan byte, 16)
	go func() {
		defer close(keys)
		buffer := make([]byte, 1)
		for {
			count, err := os.Stdin.Read(buffer)
			if err != nil {
				return
			}
			if count > 0 {
				keys <- buffer[0]
			}
		}
	}()
	return keys
}

// waitKeys applies pending keystrokes for up to duration, redrawing after each
// one. A zero duration drains without blocking.
func (session *mtrSession) waitKeys(keys <-chan byte, state *mtrLiveState, duration time.Duration, render func()) {
	deadline := time.Now().Add(duration)
	for !state.quit {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			select {
			case key, ok := <-keys:
				if !ok {
					state.quit = true
					return
				}
				session.applyKey(key, state)
				render()
			default:
				return
			}
			continue
		}
		timer := time.NewTimer(remaining)
		select {
		case key, ok := <-keys:
			timer.Stop()
			if !ok {
				state.quit = true
				return
			}
			session.applyKey(key, state)
			render()
		case <-timer.C:
			return
		}
	}
}

func (session *mtrSession) applyKey(key byte, state *mtrLiveState) {
	if state.help {
		state.help = false
		if key != 'q' && key != 'Q' && key != 3 {
			return
		}
	}
	switch key {
	case 'q', 'Q', 3:
		state.quit = true
	case 'p', 'P':
		state.paused = true
	case ' ':
		state.paused = false
	case 'r', 'R':
		session.reset()
	case 'n', 'N':
		session.opts.numeric = !session.opts.numeric
	case 'h', 'H', '?':
		state.help = true
	}
}

var mtrHelpLines = []string{
	"Command:",
	"  h ?    this help",
	"  n      toggle DNS resolution on and off",
	"  p      pause probing (SPACE resumes)",
	"  r      restart statistics",
	"  q      quit",
	"",
	"Press any key to continue ...",
}

// liveFrame renders one full screen. Raw mode has post-processing disabled, so
// every line ends with an explicit CRLF plus an erase-to-end-of-line.
func (session *mtrSession) liveFrame(state *mtrLiveState) string {
	rows, columns, ok := terminalDimensions(os.Stdout.Fd())
	if !ok {
		rows, columns = 24, 80
	}
	width := columns - 1
	if width < mtrFieldWidth+8 {
		width = mtrFieldWidth + 8
	}
	statColumn := width - mtrFieldWidth
	lines := []string{mtrCenter(mtrTitle, width), session.liveHeader(width), mtrKeys}
	if state.paused {
		lines[2] = mtrKeys + "   [paused]"
	}
	if state.help {
		lines = append(lines, "")
		lines = append(lines, mtrHelpLines...)
	} else {
		lines = append(lines,
			strings.Repeat(" ", statColumn)+mtrGroupHeader,
			mtrPad(" Host", statColumn)+mtrFieldHeader)
		start, end := session.displayRange()
		for index := start; index < end && len(lines) < rows-1; index++ {
			hop := session.hops[index]
			label := fmt.Sprintf("%2d. %s", index+1, session.hopName(hop))
			lines = append(lines, mtrPad(label, statColumn)+hop.fields())
		}
	}
	var frame strings.Builder
	frame.WriteString("\x1b[H")
	for _, line := range lines {
		frame.WriteString(mtrClip(line, width))
		frame.WriteString("\x1b[K\r\n")
	}
	frame.WriteString("\x1b[J")
	return frame.String()
}

func (session *mtrSession) liveHeader(width int) string {
	left := session.localName
	if session.localIP != "" {
		left += " (" + session.localIP + ")"
	}
	left += " -> " + session.host + " (" + session.target.String() + ")"
	stamp := time.Now().Format(mtrTimestamp)
	if len(left)+1+len(stamp) > width {
		return mtrClip(left+" "+stamp, width)
	}
	return left + strings.Repeat(" ", width-len(left)-len(stamp)) + stamp
}

// mtrProber sends a single probe at the given TTL and reports which router
// answered. traceReply is shared with traceroute.
type mtrProber interface {
	probe(ttl, sequence int, wait time.Duration) traceReply
	close()
}

// newMtrProber prefers ICMP echo like mtr does, because the high UDP ports
// traceroute uses are commonly filtered before the destination. Hosts without
// unprivileged ICMP sockets silently fall back to the UDP error queue unless
// -I demanded ICMP.
func newMtrProber(target net.IP, family int, opts mtrOptions) (mtrProber, error) {
	if opts.udp {
		return &mtrUDPProber{target: target, family: family}, nil
	}
	prober, err := newMtrICMPProber(target, family, opts.packetSize)
	if err == nil {
		return prober, nil
	}
	if opts.icmp {
		return nil, fmt.Errorf("ICMP socket: %w", err)
	}
	return &mtrUDPProber{target: target, family: family}, nil
}

type mtrUDPProber struct {
	target net.IP
	family int
}

func (prober *mtrUDPProber) probe(ttl, sequence int, wait time.Duration) traceReply {
	return runTraceProbe(prober.target, prober.family, ttl, sequence%8, wait)
}

func (prober *mtrUDPProber) close() {}

type mtrICMPProber struct {
	fd, family, identifier, payload int
	target                          net.IP
	sockaddr                        syscall.Sockaddr
}

func newMtrICMPProber(target net.IP, family, payload int) (*mtrICMPProber, error) {
	domain, protocol := syscall.AF_INET, syscall.IPPROTO_ICMP
	if family == 6 {
		domain, protocol = syscall.AF_INET6, syscall.IPPROTO_ICMPV6
	}
	// Linux grants unprivileged ICMP datagram sockets to the group range in
	// net.ipv4.ping_group_range; SOCK_RAW is the CAP_NET_RAW fallback.
	fd, err := syscall.Socket(domain, syscall.SOCK_DGRAM, protocol)
	if err != nil {
		if fd, err = syscall.Socket(domain, syscall.SOCK_RAW, protocol); err != nil {
			return nil, err
		}
	}
	level, option := syscall.IPPROTO_IP, syscall.IP_RECVERR
	if family == 6 {
		level, option = syscall.IPPROTO_IPV6, syscall.IPV6_RECVERR
	}
	// Without IP_RECVERR the kernel drops the ICMP time-exceeded replies that
	// carry the address of every intermediate router.
	if err := syscall.SetsockoptInt(fd, level, option, 1); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	prober := &mtrICMPProber{fd: fd, family: family, identifier: os.Getpid() & 0xffff, payload: payload, target: target}
	if family == 6 {
		address := &syscall.SockaddrInet6{}
		copy(address.Addr[:], target.To16())
		prober.sockaddr = address
	} else {
		address := &syscall.SockaddrInet4{}
		copy(address.Addr[:], target.To4())
		prober.sockaddr = address
	}
	return prober, nil
}

func (prober *mtrICMPProber) close() { _ = syscall.Close(prober.fd) }

func (prober *mtrICMPProber) probe(ttl, sequence int, wait time.Duration) traceReply {
	level, option := syscall.IPPROTO_IP, syscall.IP_TTL
	if prober.family == 6 {
		level, option = syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS
	}
	if err := syscall.SetsockoptInt(prober.fd, level, option, ttl); err != nil {
		return traceReply{err: err}
	}
	start := time.Now()
	if err := syscall.Sendto(prober.fd, prober.echoRequest(sequence), 0, prober.sockaddr); err != nil {
		if errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.EAGAIN) {
			return traceReply{}
		}
		return traceReply{err: err}
	}
	deadline := start.Add(wait)
	// Unrelated traffic on a shared ICMP socket can wake the wait, so the loop
	// is bounded by both the deadline and a spin guard.
	for attempt := 0; attempt < 64; attempt++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		ready, err := waitReadable(prober.fd, remaining)
		if err != nil {
			return traceReply{err: err}
		}
		if !ready {
			continue
		}
		if reply, ok := prober.readError(sequence, start); ok {
			return reply
		}
		if reply, ok := prober.readEcho(sequence, start); ok {
			return reply
		}
	}
	return traceReply{}
}

func (prober *mtrICMPProber) echoRequest(sequence int) []byte {
	packet := make([]byte, 8+prober.payload)
	packet[0] = 8
	if prober.family == 6 {
		packet[0] = 128
	}
	identifier, counter := prober.identifier&0xffff, sequence&0xffff
	binary.BigEndian.PutUint16(packet[4:6], uint16(identifier))
	binary.BigEndian.PutUint16(packet[6:8], uint16(counter))
	for index := 8; index < len(packet); index++ {
		packet[index] = byte(index)
	}
	// The kernel fills in the ICMPv6 checksum, and recomputes the ICMPv4 one on
	// datagram sockets after rewriting the identifier.
	if prober.family == 4 {
		binary.BigEndian.PutUint16(packet[2:4], icmpChecksum(packet))
	}
	return packet
}

// readError drains one message from the socket error queue, where the kernel
// parks the ICMP errors that intermediate routers send back.
func (prober *mtrICMPProber) readError(sequence int, start time.Time) (traceReply, bool) {
	payload := make([]byte, 512)
	control := make([]byte, 512)
	length, controlLength, _, _, err := syscall.Recvmsg(prober.fd, payload, control, syscall.MSG_ERRQUEUE|syscall.MSG_DONTWAIT)
	if err != nil || controlLength == 0 {
		return traceReply{}, false
	}
	if !matchesEchoSequence(payload[:length], sequence) {
		return traceReply{}, false
	}
	messages, err := syscall.ParseSocketControlMessage(control[:controlLength])
	if err != nil {
		return traceReply{}, false
	}
	wantLevel, wantType := int32(syscall.IPPROTO_IP), int32(syscall.IP_RECVERR)
	exceeded, unreachable := byte(11), byte(3)
	if prober.family == 6 {
		wantLevel, wantType = int32(syscall.IPPROTO_IPV6), int32(syscall.IPV6_RECVERR)
		exceeded, unreachable = 3, 1
	}
	for _, message := range messages {
		if message.Header.Level != wantLevel || message.Header.Type != wantType || len(message.Data) < 16 {
			continue
		}
		icmpType := message.Data[5]
		if icmpType != exceeded && icmpType != unreachable {
			continue
		}
		address := traceOffenderAddress(message.Data[16:], prober.family)
		if address == "" {
			address = prober.target.String()
		}
		reached := icmpType == unreachable && address == prober.target.String()
		return traceReply{address: address, delay: time.Since(start), reached: reached, ok: true}, true
	}
	return traceReply{}, false
}

// readEcho picks up the echo reply that only the destination itself sends.
func (prober *mtrICMPProber) readEcho(sequence int, start time.Time) (traceReply, bool) {
	buffer := make([]byte, 1500)
	length, sender, err := syscall.Recvfrom(prober.fd, buffer, syscall.MSG_DONTWAIT)
	if err != nil || length <= 0 {
		return traceReply{}, false
	}
	packet := buffer[:length]
	offset := 0
	if prober.family == 4 && length >= 20 && packet[0]>>4 == 4 {
		offset = int(packet[0]&15) * 4
	}
	replyType := byte(0)
	if prober.family == 6 {
		replyType = 129
	}
	if len(packet) < offset+8 || packet[offset] != replyType {
		return traceReply{}, false
	}
	if int(binary.BigEndian.Uint16(packet[offset+6:offset+8])) != sequence {
		return traceReply{}, false
	}
	address := prober.target.String()
	switch source := sender.(type) {
	case *syscall.SockaddrInet4:
		address = net.IP(source.Addr[:]).String()
	case *syscall.SockaddrInet6:
		address = net.IP(source.Addr[:]).String()
	}
	return traceReply{address: address, delay: time.Since(start), reached: true, ok: true}, true
}

// matchesEchoSequence checks the quoted request a router returned. Routers may
// quote less than the eight bytes that hold the sequence number, in which case
// the reply is accepted as-is.
func matchesEchoSequence(payload []byte, sequence int) bool {
	if len(payload) < 8 {
		return true
	}
	return int(binary.BigEndian.Uint16(payload[6:8])) == sequence
}

// waitReadable blocks until the socket has a datagram or a queued error. A
// pending error queue entry reports POLLERR, which select() folds into the
// read set.
func waitReadable(fd int, timeout time.Duration) (bool, error) {
	var set syscall.FdSet
	if fd < 0 || fd >= len(set.Bits)*64 {
		return false, fmt.Errorf("socket descriptor %d out of range", fd)
	}
	set.Bits[fd/64] |= 1 << (uint(fd) % 64)
	value := syscall.NsecToTimeval(int64(timeout))
	count, err := syscall.Select(fd+1, &set, nil, nil, &value)
	if err != nil {
		if errors.Is(err, syscall.EINTR) {
			return false, nil
		}
		return false, err
	}
	return count > 0, nil
}
