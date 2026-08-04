//go:build linux

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	dhcpServerPort = 67
	dhcpClientPort = 68
	dhcpDiscover   = 1
	dhcpOffer      = 2
	dhcpRequest    = 3
	dhcpAck        = 5
	dhcpNak        = 6
)

type dhcpLease struct {
	address, server, subnet, router net.IP
	dns                             []net.IP
	leaseSeconds                    uint32
}

type udhcpcOptions struct {
	interfaceName string
	attempts      int
	timeout       time.Duration
	hostname      string
	noConfigure   bool
}

func cmdUdhcpc(args []string) int {
	opts := udhcpcOptions{interfaceName: "eth0", attempts: 3, timeout: 3 * time.Second}
	if hostname, err := os.Hostname(); err == nil {
		opts.hostname = hostname
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-i", "--interface":
			i++
			if i >= len(args) {
				fatalf("udhcpc", "-i requires an interface")
				return 1
			}
			opts.interfaceName = args[i]
		case "-t", "--retries":
			i++
			if i >= len(args) {
				return 1
			}
			opts.attempts, _ = strconv.Atoi(args[i])
		case "-T", "--timeout":
			i++
			if i >= len(args) {
				return 1
			}
			seconds, err := strconv.Atoi(args[i])
			if err != nil || seconds < 1 {
				fatalf("udhcpc", "invalid timeout %q", args[i])
				return 1
			}
			opts.timeout = time.Duration(seconds) * time.Second
		case "-x", "--hostname":
			i++
			if i >= len(args) {
				return 1
			}
			opts.hostname = strings.TrimPrefix(args[i], "hostname:")
		case "-n", "-q", "-f":
			// ba6's client is always foreground, exits on failure, and exits
			// after obtaining a lease, so these BusyBox flags are compatible
			// no-ops.
		case "--no-configure":
			opts.noConfigure = true
		default:
			fatalf("udhcpc", "unsupported option %q", args[i])
			return 1
		}
	}
	if opts.attempts < 1 || opts.attempts > 100 {
		fatalf("udhcpc", "retry count must be between 1 and 100")
		return 1
	}
	lease, err := acquireDHCPLease(opts)
	if err != nil {
		fatalf("udhcpc", "%v", err)
		return 1
	}
	fmt.Printf("lease of %s obtained from %s\n", lease.address, lease.server)
	if !opts.noConfigure {
		if err := configureDHCPLease(opts.interfaceName, lease); err != nil {
			fatalf("udhcpc", "configure lease: %v", err)
			return 1
		}
	}
	return 0
}

func acquireDHCPLease(opts udhcpcOptions) (dhcpLease, error) {
	interfaceInfo, err := net.InterfaceByName(opts.interfaceName)
	if err != nil {
		return dhcpLease{}, err
	}
	if len(interfaceInfo.HardwareAddr) < 6 {
		return dhcpLease{}, fmt.Errorf("interface %s has no Ethernet address", opts.interfaceName)
	}
	var transactionBytes [4]byte
	if _, err := rand.Read(transactionBytes[:]); err != nil {
		return dhcpLease{}, err
	}
	transactionID := binary.BigEndian.Uint32(transactionBytes[:])
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: dhcpClientPort})
	if err != nil {
		return dhcpLease{}, err
	}
	defer connection.Close()
	if err := prepareDHCPSocket(connection, opts.interfaceName); err != nil {
		return dhcpLease{}, err
	}
	destination := &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpServerPort}
	for attempt := 0; attempt < opts.attempts; attempt++ {
		discover := buildDHCPPacket(transactionID, interfaceInfo.HardwareAddr, dhcpDiscover, dhcpLease{}, opts.hostname)
		if _, err := connection.WriteToUDP(discover, destination); err != nil {
			return dhcpLease{}, err
		}
		_ = connection.SetReadDeadline(time.Now().Add(opts.timeout))
		offer, err := readDHCPReply(connection, transactionID, interfaceInfo.HardwareAddr, dhcpOffer)
		if err != nil {
			if isNetworkTimeout(err) {
				continue
			}
			return dhcpLease{}, err
		}
		request := buildDHCPPacket(transactionID, interfaceInfo.HardwareAddr, dhcpRequest, offer, opts.hostname)
		if _, err := connection.WriteToUDP(request, destination); err != nil {
			return dhcpLease{}, err
		}
		_ = connection.SetReadDeadline(time.Now().Add(opts.timeout))
		lease, messageType, err := readAnyDHCPReply(connection, transactionID, interfaceInfo.HardwareAddr)
		if err != nil {
			if isNetworkTimeout(err) {
				continue
			}
			return dhcpLease{}, err
		}
		if messageType == dhcpNak {
			continue
		}
		if messageType == dhcpAck {
			return lease, nil
		}
	}
	return dhcpLease{}, fmt.Errorf("no lease received on %s", opts.interfaceName)
}

func prepareDHCPSocket(connection *net.UDPConn, interfaceName string) error {
	raw, err := connection.SyscallConn()
	if err != nil {
		return err
	}
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		if setErr := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); setErr != nil {
			socketErr = setErr
			return
		}
		socketErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, interfaceName)
	}); err != nil {
		return err
	}
	return socketErr
}

func isNetworkTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func buildDHCPPacket(transactionID uint32, hardware net.HardwareAddr, messageType byte, lease dhcpLease, hostname string) []byte {
	packet := make([]byte, 240)
	packet[0], packet[1], packet[2] = 1, 1, 6
	binary.BigEndian.PutUint32(packet[4:8], transactionID)
	packet[10], packet[11] = 0x80, 0x00 // broadcast reply requested
	copy(packet[28:44], hardware)
	copy(packet[236:240], []byte{99, 130, 83, 99})
	packet = appendDHCPOption(packet, 53, []byte{messageType})
	packet = appendDHCPOption(packet, 61, append([]byte{1}, hardware[:6]...))
	if hostname != "" {
		if len(hostname) > 63 {
			hostname = hostname[:63]
		}
		packet = appendDHCPOption(packet, 12, []byte(hostname))
	}
	switch messageType {
	case dhcpDiscover:
		packet = appendDHCPOption(packet, 55, []byte{1, 3, 6, 15, 51, 54})
	case dhcpRequest:
		packet = appendDHCPOption(packet, 50, lease.address.To4())
		packet = appendDHCPOption(packet, 54, lease.server.To4())
	}
	return append(packet, 255)
}

func appendDHCPOption(packet []byte, code byte, value []byte) []byte {
	if len(value) == 0 || len(value) > 255 {
		return packet
	}
	packet = append(packet, code, byte(len(value))) //nolint:gosec // length was bounded to 255.
	return append(packet, value...)
}

func readDHCPReply(connection *net.UDPConn, transactionID uint32, hardware net.HardwareAddr, expected byte) (dhcpLease, error) {
	for {
		lease, messageType, err := readAnyDHCPReply(connection, transactionID, hardware)
		if err != nil {
			return dhcpLease{}, err
		}
		if messageType == expected {
			return lease, nil
		}
	}
}

func readAnyDHCPReply(connection *net.UDPConn, transactionID uint32, hardware net.HardwareAddr) (dhcpLease, byte, error) {
	buffer := make([]byte, 1500)
	for {
		n, _, err := connection.ReadFromUDP(buffer)
		if err != nil {
			return dhcpLease{}, 0, err
		}
		lease, messageType, err := parseDHCPReply(buffer[:n], transactionID, hardware)
		if err == nil {
			return lease, messageType, nil
		}
	}
}

func parseDHCPReply(packet []byte, transactionID uint32, hardware net.HardwareAddr) (dhcpLease, byte, error) {
	if len(hardware) < 6 || len(packet) < 240 || packet[0] != 2 || binary.BigEndian.Uint32(packet[4:8]) != transactionID ||
		!bytes.Equal(packet[28:34], hardware[:6]) ||
		string(packet[236:240]) != string([]byte{99, 130, 83, 99}) {
		return dhcpLease{}, 0, fmt.Errorf("unrelated DHCP packet")
	}
	lease := dhcpLease{address: append(net.IP(nil), packet[16:20]...)}
	options, err := parseDHCPOptions(packet[240:])
	if err != nil {
		return dhcpLease{}, 0, err
	}
	if value := options[54]; len(value) == 4 {
		lease.server = append(net.IP(nil), value...)
	}
	if value := options[1]; len(value) == 4 {
		lease.subnet = append(net.IP(nil), value...)
	}
	if value := options[3]; len(value) >= 4 {
		lease.router = append(net.IP(nil), value[:4]...)
	}
	for value := options[6]; len(value) >= 4; value = value[4:] {
		lease.dns = append(lease.dns, append(net.IP(nil), value[:4]...))
	}
	if value := options[51]; len(value) == 4 {
		lease.leaseSeconds = binary.BigEndian.Uint32(value)
	}
	message := byte(0)
	if value := options[53]; len(value) == 1 {
		message = value[0]
	}
	if message == 0 || lease.address.To4() == nil {
		return dhcpLease{}, 0, fmt.Errorf("incomplete DHCP reply")
	}
	return lease, message, nil
}

func parseDHCPOptions(data []byte) (map[byte][]byte, error) {
	options := make(map[byte][]byte)
	for position := 0; position < len(data); {
		code := data[position]
		position++
		if code == 0 {
			continue
		}
		if code == 255 {
			return options, nil
		}
		if position >= len(data) {
			return nil, fmt.Errorf("truncated DHCP option")
		}
		length := int(data[position]) //nolint:gosec // position was checked against the slice length immediately above.
		position++
		if position+length > len(data) {
			return nil, fmt.Errorf("truncated DHCP option %d", code)
		}
		options[code] = append([]byte(nil), data[position:position+length]...)
		position += length
	}
	return options, nil
}

func configureDHCPLease(interfaceName string, lease dhcpLease) error {
	prefix := 24
	if mask := net.IPMask(lease.subnet.To4()); len(mask) == 4 {
		if ones, bits := mask.Size(); bits == 32 && ones >= 0 {
			prefix = ones
		}
	}
	if status := cmdIP([]string{"addr", "add", fmt.Sprintf("%s/%d", lease.address, prefix), "dev", interfaceName}); status != 0 {
		return fmt.Errorf("add address")
	}
	if lease.router.To4() != nil {
		if status := cmdIP([]string{"route", "add", "default", "via", lease.router.String(), "dev", interfaceName}); status != 0 {
			return fmt.Errorf("add default route")
		}
	}
	if len(lease.dns) > 0 {
		var content strings.Builder
		content.WriteString("# Generated by ba6 udhcpc\n")
		for _, server := range lease.dns {
			fmt.Fprintf(&content, "nameserver %s\n", server)
		}
		if err := replaceResolverConfiguration(content.String()); err != nil {
			return err
		}
	}
	return nil
}

func replaceResolverConfiguration(content string) error {
	target := "/etc/resolv.conf"
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	directory := filepath.Dir(target)
	temporary, err := os.CreateTemp(directory, ".resolv.conf.ba6-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o644); err != nil { //nolint:gosec // resolv.conf must remain readable by all local users.
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, target)
}
