//go:build linux

package main

import (
	"encoding/binary"
	"hash/crc32"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFdiskListsMBRImageReadOnly(t *testing.T) {
	image := filepath.Join(t.TempDir(), "disk.img")
	original := make([]byte, 4*1024*1024)
	original[446+4] = 0x83
	binary.LittleEndian.PutUint32(original[446+8:446+12], 2048)
	binary.LittleEndian.PutUint32(original[446+12:446+16], 2048)
	original[510], original[511] = 0x55, 0xaa
	if err := os.WriteFile(image, original, 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdFdisk, []string{"-l", image}, "")
	if status != 0 || !strings.Contains(stdout, "2048 4095 2048 83") {
		t.Fatalf("fdisk status=%d out=%q stderr=%q", status, stdout, stderr)
	}
	got, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("fdisk -l modified its image")
	}
}

func TestFdiskValidatesAndListsGPT(t *testing.T) {
	const sectors = 8192
	image := make([]byte, sectors*512)
	image[446+4] = 0xee
	binary.LittleEndian.PutUint32(image[446+8:446+12], 1)
	binary.LittleEndian.PutUint32(image[446+12:446+16], sectors-1)
	image[510], image[511] = 0x55, 0xaa
	entries := image[2*512 : 2*512+128*128]
	copy(entries[:16], []byte{0xaf, 0x3d, 0xc6, 0x0f, 0x83, 0x84, 0x72, 0x47, 0x8e, 0x79, 0x3d, 0x69, 0xd8, 0x47, 0x7d, 0xe4})
	copy(entries[16:32], []byte("unique-guid-0001"))
	binary.LittleEndian.PutUint64(entries[32:40], 2048)
	binary.LittleEndian.PutUint64(entries[40:48], 4095)
	for index, character := range "rescue" {
		binary.LittleEndian.PutUint16(entries[56+index*2:58+index*2], uint16(character)) //nolint:gosec // Fixed ASCII fixture text.
	}
	header := image[512:1024]
	copy(header[:8], "EFI PART")
	binary.LittleEndian.PutUint32(header[8:12], 0x00010000)
	binary.LittleEndian.PutUint32(header[12:16], 92)
	binary.LittleEndian.PutUint64(header[24:32], 1)
	binary.LittleEndian.PutUint64(header[32:40], sectors-1)
	binary.LittleEndian.PutUint64(header[40:48], 34)
	binary.LittleEndian.PutUint64(header[48:56], sectors-34)
	copy(header[56:72], []byte("disk-guid-0000001"))
	binary.LittleEndian.PutUint64(header[72:80], 2)
	binary.LittleEndian.PutUint32(header[80:84], 128)
	binary.LittleEndian.PutUint32(header[84:88], 128)
	binary.LittleEndian.PutUint32(header[88:92], crc32.ChecksumIEEE(entries))
	binary.LittleEndian.PutUint32(header[16:20], crc32.ChecksumIEEE(header[:92]))
	path := filepath.Join(t.TempDir(), "gpt.img")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := captureApplet(t, cmdFdisk, []string{"-l", path}, "")
	if status != 0 || !strings.Contains(stdout, "Disklabel type: gpt") || !strings.Contains(stdout, "Linux filesystem rescue") {
		t.Fatalf("fdisk GPT status=%d out=%q stderr=%q", status, stdout, stderr)
	}
	header[16] ^= 1
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	status, _, _ = captureApplet(t, cmdFdisk, []string{"-l", path}, "")
	if status == 0 {
		t.Fatal("fdisk accepted a corrupt GPT header checksum")
	}
}

func TestCurlVerboseShowsProtocolAndPreservesBody(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Rescue", "yes")
		_, _ = w.Write([]byte("body"))
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	status, stdout, stderr := captureApplet(t, cmdCurl, []string{"-v", server.URL + "/status?q=1"}, "")
	if status != 0 || stdout != "body" || !strings.Contains(stderr, "> GET /status?q=1 HTTP/1.1") ||
		!strings.Contains(stderr, "< HTTP/1.1 200 OK") || !strings.Contains(stderr, "< X-Rescue: yes") {
		t.Fatalf("curl -v status=%d out=%q stderr=%q", status, stdout, stderr)
	}
}

func TestDigAgainstLocalServer(t *testing.T) {
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local UDP unavailable: %v", err)
	}
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		query := make([]byte, 512)
		n, address, readErr := server.ReadFrom(query)
		if readErr != nil || n < 12 {
			return
		}
		response := append([]byte(nil), query[:n]...)
		response[2], response[3] = 0x81, 0x80
		binary.BigEndian.PutUint16(response[6:8], 1)
		response = append(response, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01)
		response = binary.BigEndian.AppendUint32(response, 60)
		response = append(response, 0x00, 0x04, 192, 0, 2, 1)
		_, _ = server.WriteTo(response, address)
	}()
	status, stdout, stderr := captureApplet(t, cmdDig, []string{"@" + server.LocalAddr().String(), "example.test", "A", "+short"}, "")
	<-done
	if status != 0 || stdout != "192.0.2.1\n" {
		t.Fatalf("dig status=%d out=%q stderr=%q", status, stdout, stderr)
	}
}

func TestLsofFindsOpenFileForPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open-file")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	status, stdout, stderr := captureApplet(t, cmdLsof, []string{"-n", "-p", strconv.Itoa(os.Getpid()), path}, "")
	if status != 0 || !strings.Contains(stdout, path) {
		t.Fatalf("lsof status=%d out=%q stderr=%q", status, stdout, stderr)
	}
}

func TestUnprivilegedTraceProbeLoopback(t *testing.T) {
	reply := runTraceProbe(net.ParseIP("127.0.0.1").To4(), 4, 1, 0, time.Second)
	if reply.err != nil && (strings.Contains(reply.err.Error(), "operation not permitted") || strings.Contains(reply.err.Error(), "permission denied")) {
		t.Skipf("sandbox blocks UDP error queues: %v", reply.err)
	}
	if !reply.ok || !reply.reached || reply.address != "127.0.0.1" {
		t.Fatalf("loopback trace reply=%+v", reply)
	}
}

func TestPingIPv6ReplyMatching(t *testing.T) {
	reply := make([]byte, 64)
	reply[0] = 129
	binary.BigEndian.PutUint16(reply[4:6], 123)
	binary.BigEndian.PutUint16(reply[6:8], 7)
	offset, matched := matchPingReply(reply, 6, 129, 123, 7)
	if !matched || offset != 0 {
		t.Fatalf("IPv6 echo reply offset=%d matched=%v", offset, matched)
	}
}

func TestInterfaceCounterParsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netdev")
	fixture := "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n" +
		" eth0: 100 1 0 0 0 0 0 0 250 2 0 0 0 0 0 0\n"
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	counters, err := readInterfaceCounters(path)
	if err != nil || counters["eth0"] != (interfaceCounters{receive: 100, transmit: 250}) {
		t.Fatalf("counters=%v err=%v", counters, err)
	}
}

func TestTraceOptionValidation(t *testing.T) {
	opts, host, err := parseTraceOptions([]string{"-6", "-m", "5", "-q", "2", "-w", "0.1", "::1"})
	if err != nil || host != "::1" || opts.family != 6 || opts.maxHops != 5 || opts.probes != 2 || opts.wait != 100*time.Millisecond {
		t.Fatalf("opts=%+v host=%q err=%v", opts, host, err)
	}
}

func TestDiagnosticHardeningClassification(t *testing.T) {
	for _, name := range []string{"dig", "mtr", "traceroute"} {
		if !appletNeedsUnrestrictedSyscalls(name) {
			t.Errorf("%s should bypass the socket-denying seccomp profile", name)
		}
	}
	for _, name := range []string{"fdisk", "iftop", "lsof"} {
		if appletNeedsUnrestrictedSyscalls(name) {
			t.Errorf("%s should retain the default seccomp profile", name)
		}
	}
}
