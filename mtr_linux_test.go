//go:build linux

package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

// mtrTestSession builds a session with fixed content so the layout assertions
// below do not depend on the host, its routes, or DNS.
func mtrTestSession(opts mtrOptions) *mtrSession {
	opts.maxHops = 30
	opts.firstHop = 1
	opts.numeric = true
	session := &mtrSession{
		opts:      opts,
		host:      "wp.pl",
		target:    net.ParseIP("212.77.98.9").To4(),
		family:    4,
		localName: "polaris2",
		localIP:   "192.168.55.14",
		hops:      make([]mtrHop, opts.maxHops),
		limit:     opts.maxHops,
		resolver:  newMtrResolver(),
		started:   time.Now(),
	}
	session.hops[0] = mtrHop{address: "192.168.55.1", sent: 12, received: 12, last: 0.9, best: 0.8, worst: 3.9, total: 16.8, totalOfSquares: 34.7}
	session.hops[1] = mtrHop{address: "77.46.44.1", sent: 12, received: 11, last: 2.7, best: 1.7, worst: 5.7, total: 29.7, totalOfSquares: 95.1}
	session.hops[2] = mtrHop{sent: 12}
	return session
}

// TestMtrReportMatchesOriginalGeometry pins the column offsets of mtr's report:
// the statistics start at column 36 and the row fits an 80-column terminal.
func TestMtrReportMatchesOriginalGeometry(t *testing.T) {
	var out strings.Builder
	if err := writeMtrReport(&out, mtrTestSession(mtrOptions{report: true})); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("report has %d lines:\n%s", len(lines), out.String())
	}
	if !strings.HasPrefix(lines[0], "Start: ") {
		t.Errorf("first report line = %q", lines[0])
	}
	header := lines[1]
	if index := strings.Index(header, "Loss%"); index != 37 {
		t.Errorf("Loss%% header at column %d, want 37", index)
	}
	if len(header) != 79 {
		t.Errorf("header width = %d, want 79", len(header))
	}
	for _, line := range lines[2:4] {
		if len(line) != 79 {
			t.Errorf("row width = %d, want 79: %q", len(line), line)
		}
	}
	if !strings.HasPrefix(lines[2], "  1.|-- 192.168.55.1") {
		t.Errorf("first hop row = %q", lines[2])
	}
	// A fully lost hop prints "100.0%" into a five-column float, which widens
	// the row by one; the original overflows the same way.
	if !strings.Contains(lines[4], "???") || len(lines[4]) != 80 {
		t.Errorf("unanswered hop row = %q", lines[4])
	}
}

func TestMtrWideReportKeepsFullNames(t *testing.T) {
	session := mtrTestSession(mtrOptions{report: true, wide: true, showIPs: true})
	session.hops[0].address = "an-extremely-long-router-name.example.invalid"
	var out strings.Builder
	if err := writeMtrReport(&out, session); err != nil {
		t.Fatal(err)
	}
	line := strings.Split(out.String(), "\n")[2]
	if !strings.Contains(line, "an-extremely-long-router-name.example.invalid") {
		t.Fatalf("wide report truncated the host: %q", line)
	}
	header := strings.Split(out.String(), "\n")[1]
	// Both spellings of the field end at the same column: "Loss%" is right
	// aligned in six columns and " %4.1f%%" fills the same six.
	if strings.Index(header, "Loss%")+len("Loss%") != strings.Index(line, "0.0%")+len("0.0%") {
		t.Fatalf("wide report columns drifted:\n%s\n%s", header, line)
	}
}

// TestMtrLiveFrameMatchesOriginalGeometry checks the full-screen layout against
// the original curses display on an 80-column terminal.
func TestMtrLiveFrameMatchesOriginalGeometry(t *testing.T) {
	frame := mtrTestSession(mtrOptions{}).liveFrame(&mtrLiveState{})
	replacer := strings.NewReplacer("\x1b[H", "", "\x1b[K", "", "\x1b[J", "")
	lines := strings.Split(replacer.Replace(frame), "\r\n")
	if len(lines) < 8 {
		t.Fatalf("frame has %d lines: %q", len(lines), frame)
	}
	if got := strings.Index(lines[0], "My traceroute"); got != 29 {
		t.Errorf("title centred at %d, want 29", got)
	}
	if !strings.HasPrefix(lines[1], "polaris2 (192.168.55.14) -> wp.pl (212.77.98.9)") || len(lines[1]) != 79 {
		t.Errorf("header line = %q", lines[1])
	}
	if got := strings.Index(lines[3], "Packets"); got != 39 {
		t.Errorf("Packets caption at %d, want 39", got)
	}
	if got := strings.Index(lines[4], "Loss%"); got != 37 {
		t.Errorf("Loss%% header at %d, want 37", got)
	}
	if !strings.HasPrefix(lines[5], " 1. 192.168.55.1") || len(lines[5]) != 79 {
		t.Errorf("first hop row = %q", lines[5])
	}
}

func TestMtrHelpOverlayReplacesHopTable(t *testing.T) {
	frame := mtrTestSession(mtrOptions{}).liveFrame(&mtrLiveState{help: true})
	if !strings.Contains(frame, "Command:") || strings.Contains(frame, "Loss%") {
		t.Fatalf("help overlay frame = %q", frame)
	}
}

func TestMtrHopStatistics(t *testing.T) {
	hop := mtrHop{sent: 4}
	for _, delay := range []time.Duration{2 * time.Millisecond, 6 * time.Millisecond, 4 * time.Millisecond} {
		hop.record(delay)
	}
	if hop.last != 4 || hop.best != 2 || hop.worst != 6 || hop.average() != 4 {
		t.Fatalf("hop=%+v avg=%v", hop, hop.average())
	}
	if hop.loss() != 25 {
		t.Errorf("loss = %v, want 25", hop.loss())
	}
	if difference := hop.stdev() - 2; difference > 0.0001 || difference < -0.0001 {
		t.Errorf("stdev = %v, want 2", hop.stdev())
	}
	if empty := (mtrHop{sent: 3}); empty.loss() != 100 || empty.average() != 0 || empty.stdev() != 0 {
		t.Errorf("unanswered hop = %+v", empty)
	}
}

func TestMtrOptionParsing(t *testing.T) {
	opts, host, err := parseMtrOptions([]string{"-r", "-c", "5", "--interval=0.5", "-Z2", "-6", "-b", "wp.pl"})
	if err != nil || host != "wp.pl" {
		t.Fatalf("host=%q err=%v", host, err)
	}
	if !opts.report || opts.cycles != 5 || opts.interval != 500*time.Millisecond || opts.timeout != 2*time.Second {
		t.Fatalf("opts=%+v", opts)
	}
	if opts.family != 6 || !opts.showIPs {
		t.Fatalf("opts=%+v", opts)
	}
	if opts, _, err := parseMtrOptions([]string{"-w", "example.com"}); err != nil || !opts.report || !opts.wide {
		t.Fatalf("-w should imply a wide report: opts=%+v err=%v", opts, err)
	}
	for _, args := range [][]string{
		{},
		{"-c", "0", "wp.pl"},
		{"-c"},
		{"-m", "400", "wp.pl"},
		{"-i", "nope", "wp.pl"},
		{"-u", "-I", "wp.pl"},
		{"-f", "5", "-m", "3", "wp.pl"},
		{"--nonsense", "wp.pl"},
		{"wp.pl", "example.com"},
	} {
		if _, _, err := parseMtrOptions(args); err == nil {
			t.Errorf("parseMtrOptions(%q) accepted invalid input", args)
		}
	}
}

func TestMtrKeysDriveTheDisplay(t *testing.T) {
	session := mtrTestSession(mtrOptions{})
	state := &mtrLiveState{}
	session.applyKey('h', state)
	if !state.help {
		t.Fatal("h did not open the help overlay")
	}
	session.applyKey('x', state)
	if state.help {
		t.Fatal("a key press did not dismiss the help overlay")
	}
	session.applyKey('n', state)
	if session.opts.numeric {
		t.Fatal("n did not toggle DNS resolution")
	}
	session.applyKey('p', state)
	session.applyKey(' ', state)
	if state.paused {
		t.Fatal("SPACE did not resume probing")
	}
	session.applyKey('r', state)
	if session.hops[0].sent != 0 || session.hops[0].address != "192.168.55.1" {
		t.Fatalf("r should clear counters but keep addresses: %+v", session.hops[0])
	}
	session.applyKey('q', state)
	if !state.quit {
		t.Fatal("q did not quit")
	}
}

func TestMtrEchoSequenceMatching(t *testing.T) {
	quoted := []byte{8, 0, 0, 0, 0x00, 0x14, 0x00, 0x07}
	if !matchesEchoSequence(quoted, 7) || matchesEchoSequence(quoted, 8) {
		t.Fatal("quoted echo header did not match on sequence alone")
	}
	if !matchesEchoSequence([]byte{8, 0}, 7) {
		t.Fatal("a router quoting less than eight bytes should not drop the reply")
	}
}

// TestMtrICMPProbeLoopback exercises the unprivileged ICMP datagram path end to
// end; environments that forbid those sockets fall back to UDP at runtime.
func TestMtrICMPProbeLoopback(t *testing.T) {
	prober, err := newMtrICMPProber(net.ParseIP("127.0.0.1").To4(), 4, 16)
	if err != nil {
		t.Skipf("no unprivileged ICMP socket: %v", err)
	}
	defer prober.close()
	reply := prober.probe(1, 4242, time.Second)
	if reply.err != nil {
		t.Skipf("sandbox blocked the ICMP probe: %v", reply.err)
	}
	if !reply.ok || !reply.reached || reply.address != "127.0.0.1" {
		t.Fatalf("loopback ICMP reply = %+v", reply)
	}
}

func TestMtrProberFallsBackToUDP(t *testing.T) {
	prober, err := newMtrProber(net.ParseIP("127.0.0.1").To4(), 4, mtrOptions{udp: true, packetSize: 56})
	if err != nil {
		t.Fatal(err)
	}
	defer prober.close()
	if _, ok := prober.(*mtrUDPProber); !ok {
		t.Fatalf("-u selected %T", prober)
	}
}
