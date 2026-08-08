// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	iflaInfoKind   = 1
	iflaInfoData   = 2
	iflaVlanID     = 1
	iflaBondMode   = 1
	iflaBondMiimon = 3
	iflaIfAlias    = 20

	nlaFNested  = 1 << 15
	nlaTypeMask = 0x3fff
	iffUp       = 1 << 0
)

var bondModes = map[string]uint8{
	"balance-rr":    0,
	"active-backup": 1,
	"balance-xor":   2,
	"broadcast":     3,
	"802.3ad":       4,
	"balance-tlb":   5,
	"balance-alb":   6,
}

type linkAddSpec struct {
	name     string
	kind     string
	parent   string
	vlanID   *uint16
	bondMode *uint8
	miimon   *uint32
}

type linkSetSpec struct {
	name     string
	up       *bool
	master   string
	noMaster bool
	mtu      *uint32
	address  net.HardwareAddr
	alias    *string
	rename   string
}

func ipLink(args []string) error {
	if len(args) == 0 || args[0] == "show" || args[0] == "list" {
		if len(args) > 0 {
			args = args[1:]
		}
		// "ip link show [dev IFACE] [up]" restricts the listing to running
		// interfaces; the filter always comes last.
		up := false
		if len(args) > 0 && args[len(args)-1] == "up" {
			up, args = true, args[:len(args)-1]
		}
		dev, err := parseShowDev(args)
		if err != nil {
			return err
		}
		return showLinks(dev, up)
	}
	switch args[0] {
	case "add":
		spec, err := parseLinkAdd(args[1:])
		if err != nil {
			return err
		}
		return addLink(spec)
	case "delete", "del":
		name, err := parseLinkName(args[1:])
		if err != nil {
			return err
		}
		return deleteLink(name)
	case "set":
		spec, err := parseLinkSet(args[1:])
		if err != nil {
			return err
		}
		return setLink(spec)
	default:
		return fmt.Errorf("unknown link command %q", args[0])
	}
}

func parseLinkAdd(args []string) (linkAddSpec, error) {
	var spec linkAddSpec
	if len(args) == 0 {
		return spec, fmt.Errorf("missing link name")
	}
	pos := 0
	if args[pos] == "link" {
		if len(args) < pos+2 {
			return spec, fmt.Errorf("missing parent after link")
		}
		spec.parent = args[pos+1]
		pos += 2
	}
	if pos < len(args) && args[pos] == "name" {
		if len(args) < pos+2 {
			return spec, fmt.Errorf("missing link name")
		}
		spec.name = args[pos+1]
		pos += 2
	} else if pos < len(args) {
		spec.name = args[pos]
		pos++
	}
	if err := validateLinkName(spec.name); err != nil {
		return spec, err
	}
	if len(args) < pos+2 || args[pos] != "type" {
		return spec, fmt.Errorf("expected 'type bond' or 'type vlan'")
	}
	spec.kind = args[pos+1]
	pos += 2
	options := args[pos:]
	switch spec.kind {
	case "bond":
		if spec.parent != "" {
			return spec, fmt.Errorf("a bond cannot have a parent link")
		}
		for len(options) > 0 {
			if len(options) < 2 {
				return spec, fmt.Errorf("missing value after %q", options[0])
			}
			key, value := options[0], options[1]
			options = options[2:]
			switch key {
			case "mode":
				mode, err := parseBondMode(value)
				if err != nil {
					return spec, err
				}
				spec.bondMode = &mode
			case "miimon":
				parsed, err := strconv.ParseUint(value, 10, 32)
				if err != nil {
					return spec, fmt.Errorf("invalid miimon value %q", value)
				}
				miimon := uint32(parsed)
				spec.miimon = &miimon
			default:
				return spec, fmt.Errorf("unknown bond option %q", key)
			}
		}
	case "vlan":
		if spec.parent == "" {
			return spec, fmt.Errorf("a VLAN requires 'link PARENT'")
		}
		if len(options) != 2 || options[0] != "id" {
			return spec, fmt.Errorf("a VLAN requires 'id VLAN_ID'")
		}
		value, err := strconv.ParseUint(options[1], 10, 16)
		if err != nil || value < 1 || value > 4094 {
			return spec, fmt.Errorf("invalid VLAN ID %q", options[1])
		}
		vlanID := uint16(value)
		spec.vlanID = &vlanID
	default:
		return spec, fmt.Errorf("unsupported link type %q", spec.kind)
	}
	return spec, nil
}

func parseBondMode(value string) (uint8, error) {
	if mode, ok := bondModes[value]; ok {
		return mode, nil
	}
	numeric, err := strconv.ParseUint(value, 10, 8)
	if err == nil && numeric <= 6 {
		return uint8(numeric), nil
	}
	return 0, fmt.Errorf("invalid bond mode %q", value)
}

func parseLinkName(args []string) (string, error) {
	if len(args) == 2 && args[0] == "dev" {
		args = args[1:]
	}
	if len(args) != 1 {
		return "", fmt.Errorf("expected one link name")
	}
	if err := validateLinkName(args[0]); err != nil {
		return "", err
	}
	return args[0], nil
}

func parseLinkSet(args []string) (linkSetSpec, error) {
	var spec linkSetSpec
	if len(args) > 0 && args[0] == "dev" {
		args = args[1:]
	}
	if len(args) < 2 {
		return spec, fmt.Errorf("usage: ip link set dev IFACE OPTION")
	}
	spec.name, args = args[0], args[1:]
	if err := validateLinkName(spec.name); err != nil {
		return spec, err
	}
	for len(args) > 0 {
		switch args[0] {
		case "up", "down":
			up := args[0] == "up"
			spec.up = &up
			args = args[1:]
		case "master":
			if len(args) < 2 {
				return spec, fmt.Errorf("missing bond name after master")
			}
			spec.master, spec.noMaster = args[1], false
			if err := validateLinkName(spec.master); err != nil {
				return spec, err
			}
			args = args[2:]
		case "nomaster":
			spec.master, spec.noMaster = "", true
			args = args[1:]
		case "mtu":
			if len(args) < 2 {
				return spec, fmt.Errorf("missing value after mtu")
			}
			value, err := strconv.ParseUint(args[1], 10, 32)
			if err != nil || value == 0 {
				return spec, fmt.Errorf("invalid MTU %q", args[1])
			}
			mtu := uint32(value)
			spec.mtu = &mtu
			args = args[2:]
		case "address":
			if len(args) < 2 {
				return spec, fmt.Errorf("missing value after address")
			}
			address, err := net.ParseMAC(args[1])
			if err != nil {
				return spec, fmt.Errorf("invalid link address %q", args[1])
			}
			spec.address = address
			args = args[2:]
		case "alias":
			if len(args) < 2 {
				return spec, fmt.Errorf("missing value after alias")
			}
			alias := args[1]
			if len(alias) > 255 || strings.ContainsRune(alias, 0) {
				return spec, fmt.Errorf("invalid link alias")
			}
			spec.alias = &alias
			args = args[2:]
		case "name":
			if len(args) < 2 {
				return spec, fmt.Errorf("missing value after name")
			}
			if err := validateLinkName(args[1]); err != nil {
				return spec, err
			}
			spec.rename = args[1]
			args = args[2:]
		default:
			return spec, fmt.Errorf("unknown link set option %q", args[0])
		}
	}
	return spec, nil
}

func validateLinkName(name string) error {
	if name == "" || len(name) > 15 || strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid link name %q", name)
	}
	return nil
}

func addLink(spec linkAddSpec) error {
	payload := make([]byte, 16, 128)
	payload = append(payload, netlinkAttribute(syscall.IFLA_IFNAME, append([]byte(spec.name), 0))...)
	var data []byte
	switch spec.kind {
	case "bond":
		if spec.bondMode != nil {
			data = append(data, netlinkAttribute(iflaBondMode, []byte{*spec.bondMode})...)
		}
		if spec.miimon != nil {
			value := make([]byte, 4)
			binary.NativeEndian.PutUint32(value, *spec.miimon)
			data = append(data, netlinkAttribute(iflaBondMiimon, value)...)
		}
	case "vlan":
		parent, err := net.InterfaceByName(spec.parent)
		if err != nil {
			return fmt.Errorf("parent device %q: %w", spec.parent, err)
		}
		value := make([]byte, 4)
		binary.NativeEndian.PutUint32(value, uint32(parent.Index)) //nolint:gosec // G115: kernel interface indices are nonnegative uint32 values.
		payload = append(payload, netlinkAttribute(syscall.IFLA_LINK, value)...)
		vlanID := make([]byte, 2)
		binary.NativeEndian.PutUint16(vlanID, *spec.vlanID)
		data = append(data, netlinkAttribute(iflaVlanID, vlanID)...)
	}
	linkInfo := netlinkAttribute(iflaInfoKind, append([]byte(spec.kind), 0))
	if len(data) > 0 {
		linkInfo = append(linkInfo, netlinkAttribute(iflaInfoData|nlaFNested, data)...)
	}
	payload = append(payload, netlinkAttribute(syscall.IFLA_LINKINFO|nlaFNested, linkInfo)...)
	flags := uint16(syscall.NLM_F_REQUEST | syscall.NLM_F_ACK | syscall.NLM_F_CREATE | syscall.NLM_F_EXCL)
	return netlinkRequest(syscall.RTM_NEWLINK, flags, payload)
}

func deleteLink(name string) error {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("device %q: %w", name, err)
	}
	payload := make([]byte, 16)
	binary.NativeEndian.PutUint32(payload[4:8], uint32(iface.Index)) //nolint:gosec // G115: kernel interface indices are nonnegative uint32 values.
	return netlinkRequest(syscall.RTM_DELLINK, syscall.NLM_F_REQUEST|syscall.NLM_F_ACK, payload)
}

func setLink(spec linkSetSpec) error {
	iface, err := net.InterfaceByName(spec.name)
	if err != nil {
		return fmt.Errorf("device %q: %w", spec.name, err)
	}
	payload := make([]byte, 16, 24)
	binary.NativeEndian.PutUint32(payload[4:8], uint32(iface.Index)) //nolint:gosec // G115: kernel interface indices are nonnegative uint32 values.
	if spec.up != nil {
		binary.NativeEndian.PutUint32(payload[12:16], iffUp)
		if *spec.up {
			binary.NativeEndian.PutUint32(payload[8:12], iffUp)
		}
	}
	if spec.master != "" || spec.noMaster {
		masterIndex := uint32(0)
		if spec.master != "" {
			master, masterErr := net.InterfaceByName(spec.master)
			if masterErr != nil {
				return fmt.Errorf("master device %q: %w", spec.master, masterErr)
			}
			masterIndex = uint32(master.Index) //nolint:gosec // G115: kernel interface indices are nonnegative uint32 values.
		}
		value := make([]byte, 4)
		binary.NativeEndian.PutUint32(value, masterIndex)
		payload = append(payload, netlinkAttribute(syscall.IFLA_MASTER, value)...)
	}
	if spec.mtu != nil {
		value := make([]byte, 4)
		binary.NativeEndian.PutUint32(value, *spec.mtu)
		payload = append(payload, netlinkAttribute(syscall.IFLA_MTU, value)...)
	}
	if len(spec.address) > 0 {
		payload = append(payload, netlinkAttribute(syscall.IFLA_ADDRESS, spec.address)...)
	}
	if spec.alias != nil {
		payload = append(payload, netlinkAttribute(iflaIfAlias, append([]byte(*spec.alias), 0))...)
	}
	if spec.rename != "" {
		payload = append(payload, netlinkAttribute(syscall.IFLA_IFNAME, append([]byte(spec.rename), 0))...)
	}
	return netlinkRequest(syscall.RTM_NEWLINK, syscall.NLM_F_REQUEST|syscall.NLM_F_ACK, payload)
}

type rawNetlinkAttribute struct {
	typeID uint16
	value  []byte
}

type linkDetails struct {
	index    int
	name     string
	mtu      uint32
	flags    uint32
	state    uint8
	master   int
	parent   int
	kind     string
	vlanID   *uint16
	bondMode *uint8
	miimon   *uint32
	address  net.HardwareAddr
	alias    string
}

func showLinks(dev string, up bool) error {
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETLINK, syscall.AF_UNSPEC)
	if err != nil {
		return err
	}
	messages, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return err
	}
	var links []linkDetails
	names := make(map[int]string)
	for _, message := range messages {
		if message.Header.Type != syscall.RTM_NEWLINK || len(message.Data) < 16 {
			continue
		}
		link, parseErr := parseLinkDetails(message.Data)
		if parseErr != nil {
			return parseErr
		}
		if link.name == "" {
			continue
		}
		names[link.index] = link.name
		links = append(links, link)
	}
	sort.Slice(links, func(i, j int) bool { return links[i].index < links[j].index })
	out := bufio.NewWriter(os.Stdout)
	matched := false
	for _, link := range links {
		if dev != "" && link.name != dev {
			continue
		}
		if up && link.flags&syscall.IFF_UP == 0 {
			continue
		}
		matched = true
		displayName := link.name
		if parent := names[link.parent]; link.kind == "vlan" && parent != "" {
			displayName += "@" + parent
		}
		fmt.Fprintf(out, "%d: %s: <%s> mtu %d", link.index, displayName, rawLinkFlags(link.flags), link.mtu)
		if master := names[link.master]; master != "" {
			fmt.Fprintf(out, " master %s", master)
		}
		fmt.Fprintf(out, " state %s", linkStateName(link.state))
		if len(link.address) > 0 {
			fmt.Fprintf(out, " address %s", link.address)
		}
		if link.alias != "" {
			fmt.Fprintf(out, " alias %q", link.alias)
		}
		if link.kind != "" {
			fmt.Fprintf(out, " type %s", link.kind)
		}
		if link.vlanID != nil {
			fmt.Fprintf(out, " id %d", *link.vlanID)
		}
		if link.bondMode != nil {
			fmt.Fprintf(out, " mode %s", bondModeName(*link.bondMode))
		}
		if link.miimon != nil {
			fmt.Fprintf(out, " miimon %d", *link.miimon)
		}
		fmt.Fprintln(out)
	}
	if dev != "" && !matched {
		return fmt.Errorf("device %q does not exist", dev)
	}
	return out.Flush()
}

func parseLinkDetails(data []byte) (linkDetails, error) {
	var link linkDetails
	link.index = int(int32(binary.NativeEndian.Uint32(data[4:8]))) //nolint:gosec // G115: ifinfomsg encodes the index as signed int32.
	link.flags = binary.NativeEndian.Uint32(data[8:12])
	attrs, err := parseRawNetlinkAttributes(data[16:])
	if err != nil {
		return link, err
	}
	for _, attr := range attrs {
		switch attr.typeID {
		case syscall.IFLA_IFNAME:
			link.name = netlinkString(attr.value)
		case syscall.IFLA_MTU:
			if len(attr.value) >= 4 {
				link.mtu = binary.NativeEndian.Uint32(attr.value)
			}
		case syscall.IFLA_LINK:
			if len(attr.value) >= 4 {
				link.parent = int(binary.NativeEndian.Uint32(attr.value))
			}
		case syscall.IFLA_MASTER:
			if len(attr.value) >= 4 {
				link.master = int(binary.NativeEndian.Uint32(attr.value))
			}
		case syscall.IFLA_OPERSTATE:
			if len(attr.value) > 0 {
				link.state = attr.value[0]
			}
		case syscall.IFLA_ADDRESS:
			link.address = net.HardwareAddr(append([]byte(nil), attr.value...))
		case iflaIfAlias:
			link.alias = netlinkString(attr.value)
		case syscall.IFLA_LINKINFO:
			if err := parseLinkInfo(&link, attr.value); err != nil {
				return link, err
			}
		}
	}
	return link, nil
}

func parseLinkInfo(link *linkDetails, data []byte) error {
	attrs, err := parseRawNetlinkAttributes(data)
	if err != nil {
		return err
	}
	var infoData []byte
	for _, attr := range attrs {
		switch attr.typeID {
		case iflaInfoKind:
			link.kind = netlinkString(attr.value)
		case iflaInfoData:
			infoData = attr.value
		}
	}
	if len(infoData) == 0 {
		return nil
	}
	dataAttrs, err := parseRawNetlinkAttributes(infoData)
	if err != nil {
		return err
	}
	for _, attr := range dataAttrs {
		switch link.kind {
		case "vlan":
			if attr.typeID == iflaVlanID && len(attr.value) >= 2 {
				value := binary.NativeEndian.Uint16(attr.value)
				link.vlanID = &value
			}
		case "bond":
			switch attr.typeID {
			case iflaBondMode:
				if len(attr.value) > 0 {
					value := attr.value[0]
					link.bondMode = &value
				}
			case iflaBondMiimon:
				if len(attr.value) >= 4 {
					value := binary.NativeEndian.Uint32(attr.value)
					link.miimon = &value
				}
			}
		}
	}
	return nil
}

func parseRawNetlinkAttributes(data []byte) ([]rawNetlinkAttribute, error) {
	var attrs []rawNetlinkAttribute
	for len(data) > 0 {
		if len(data) < 4 {
			if bytes.Count(data, []byte{0}) == len(data) {
				break
			}
			return nil, fmt.Errorf("short netlink attribute header")
		}
		length := int(binary.NativeEndian.Uint16(data[0:2]))
		if length < 4 || length > len(data) {
			return nil, fmt.Errorf("invalid netlink attribute length %d", length)
		}
		typeID := binary.NativeEndian.Uint16(data[2:4]) & nlaTypeMask
		attrs = append(attrs, rawNetlinkAttribute{typeID: typeID, value: data[4:length]})
		aligned := (length + 3) &^ 3
		if aligned > len(data) {
			break
		}
		data = data[aligned:]
	}
	return attrs, nil
}

func netlinkString(value []byte) string {
	if end := bytes.IndexByte(value, 0); end >= 0 {
		value = value[:end]
	}
	return string(value)
}

func bondModeName(mode uint8) string {
	for name, value := range bondModes {
		if value == mode {
			return name
		}
	}
	return strconv.Itoa(int(mode))
}

func rawLinkFlags(flags uint32) string {
	type flagName struct {
		flag uint32
		name string
	}
	known := []flagName{
		{1 << 0, "UP"},
		{1 << 1, "BROADCAST"},
		{1 << 3, "LOOPBACK"},
		{1 << 4, "POINTOPOINT"},
		{1 << 6, "RUNNING"},
		{1 << 12, "MULTICAST"},
		{1 << 16, "LOWER_UP"},
	}
	var names []string
	for _, item := range known {
		if flags&item.flag != 0 {
			names = append(names, item.name)
		}
	}
	return strings.Join(names, ",")
}

func linkStateName(state uint8) string {
	states := []string{"UNKNOWN", "NOTPRESENT", "DOWN", "LOWERLAYERDOWN", "TESTING", "DORMANT", "UP"}
	if int(state) < len(states) {
		return states[state]
	}
	return strconv.Itoa(int(state))
}
