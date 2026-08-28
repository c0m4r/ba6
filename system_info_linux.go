// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

func cmdUname(args []string) int {
	selected := map[byte]bool{}
	for _, arg := range args {
		if arg == "--" {
			continue
		}
		if len(arg) < 2 || arg[0] != '-' {
			fatalf("uname", "extra operand %q", arg)
			return 1
		}
		for _, flag := range arg[1:] {
			switch flag {
			case 'a':
				for _, all := range []byte("snrvmo") {
					selected[all] = true
				}
			case 's':
				selected['s'] = true
			case 'n':
				selected['n'] = true
			case 'r':
				selected['r'] = true
			case 'v':
				selected['v'] = true
			case 'm':
				selected['m'] = true
			case 'o':
				selected['o'] = true
			default:
				fatalf("uname", "invalid option -- '%c'", flag)
				return 1
			}
		}
	}
	if len(selected) == 0 {
		selected['s'] = true
	}
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		fatalf("uname", "%v", err)
		return 1
	}
	values := map[byte]string{
		's': utsField(uts.Sysname[:]),
		'n': utsField(uts.Nodename[:]),
		'r': utsField(uts.Release[:]),
		'v': utsField(uts.Version[:]),
		'm': utsField(uts.Machine[:]),
		'o': "GNU/Linux",
	}
	var output []string
	for _, field := range []byte{'s', 'n', 'r', 'v', 'm', 'o'} {
		if selected[field] {
			output = append(output, values[field])
		}
	}
	if _, err := fmt.Fprintln(os.Stdout, strings.Join(output, " ")); err != nil {
		return 1
	}
	return 0
}

func utsField(value []int8) string {
	bytes := make([]byte, 0, len(value))
	for _, current := range value {
		if current == 0 {
			break
		}
		bytes = append(bytes, byte(current)) //nolint:gosec // utsname bytes are an ASCII C string.
	}
	return string(bytes)
}

func cmdWhoami(args []string) int {
	if len(args) != 0 {
		fatalf("whoami", "extra operand %q", args[0])
		return 1
	}
	current, err := user.Current()
	if err != nil {
		fatalf("whoami", "%v", err)
		return 1
	}
	_, err = fmt.Fprintln(os.Stdout, current.Username)
	if err != nil {
		return 1
	}
	return 0
}

func cmdHostname(args []string) int {
	alias, fqdnFlag, ipFlag, dns, short, yp := false, false, false, false, false, false
	fileName, setName := "", ""
	hasName := false
	i := 0
	for ; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-a" || arg == "--aliases":
			alias, yp, fqdnFlag, ipFlag, dns, short = true, false, false, false, false, false
		case arg == "-d" || arg == "--domain":
			fqdnFlag, dns, yp, alias, ipFlag, short = true, true, false, false, false, false
		case arg == "-f" || arg == "--fqdn" || arg == "--long":
			fqdnFlag, yp, alias, ipFlag, dns, short = true, false, false, false, false, false
		case arg == "-i" || arg == "--ip-addresses":
			ipFlag, yp, alias, fqdnFlag, dns, short = true, false, false, false, false, false
		case arg == "-s" || arg == "--short":
			fqdnFlag, short, yp, alias, ipFlag, dns = true, true, false, false, false, false
		case arg == "-y" || arg == "--yp" || arg == "--nis":
			yp, alias, fqdnFlag, ipFlag, dns, short = true, false, false, false, false, false
		case arg == "-F" || arg == "--file":
			i++
			if i >= len(args) {
				fatalf("hostname", "option requires an argument -- 'F'")
				return 1
			}
			fileName = args[i]
		case strings.HasPrefix(arg, "--file="):
			fileName = strings.TrimPrefix(arg, "--file=")
		case len(arg) > 0 && arg[0] == '-':
			fatalf("hostname", "unsupported operand %q", arg)
			return 1
		default:
			setName, hasName = arg, true
		}
	}
	if fileName != "" || hasName {
		name := setName
		if fileName != "" {
			var err error
			name, err = hostnameFileToken(fileName)
			if err != nil {
				fatalf("hostname", "fopen: %v", errText(err))
				return 1
			}
		}
		if name == "" {
			fatalf("hostname", "Empty hostname")
			return 1
		}
		if err := syscall.Sethostname([]byte(name)); err != nil {
			fatalf("hostname", "sethostname: %v", errText(err))
			return 1
		}
		return 0
	}
	name, err := os.Hostname()
	if err != nil {
		fatalf("hostname", "cannot determine name")
		return 1
	}
	if yp {
		var uts syscall.Utsname
		if err := syscall.Uname(&uts); err == nil {
			name = utsField(uts.Domainname[:])
		}
	}
	out := name
	switch {
	case alias:
		out = hostnameAliases(name)
	case fqdnFlag:
		out = hostnameFqdn(name)
		// A valid reply replaces the base name for -d/-s, the way the
		// original reuses gethostbyname's result; "(none)" and empty
		// replies keep the original.
		if out != "" && !strings.HasPrefix(out, "(") {
			name = out
		}
		if dns {
			out = hostnameDomain(name)
		} else if short {
			out = hostnameShort(name)
		}
	case ipFlag:
		addresses, ok := hostnameAddresses(name)
		if !ok {
			fatalf("hostname", "gethostbyname: Host not found")
			return 1
		}
		out = strings.Join(addresses, " ")
	}
	if out != "" {
		if _, err := fmt.Fprintln(os.Stdout, out); err != nil {
			return 1
		}
	}
	return 0
}

// hostnameFileToken extracts the first word of the first non-comment line of
// a hostname file, matching inetutils' parse_file.
func hostnameFileToken(fileName string) (string, error) {
	data, err := os.ReadFile(fileName)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 1 {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("getline: No text")
}

// hostLookup mirrors gethostbyname for the files-then-dns order the
// originals use: /etc/hosts first, falling back to the DNS resolver.
var hostsFilePath = "/etc/hosts"

func hostLookup(name string) (canonical string, addrs, aliases []string, ok bool) {
	if data, err := os.ReadFile(hostsFilePath); err == nil {
		found := false
		for _, raw := range strings.Split(string(data), "\n") {
			if idx := strings.IndexByte(raw, '#'); idx >= 0 {
				raw = raw[:idx]
			}
			fields := strings.Fields(raw)
			if len(fields) < 2 {
				continue
			}
			match := false
			for _, f := range fields[1:] {
				if strings.EqualFold(f, name) {
					match = true
				}
			}
			if !match {
				continue
			}
			addrs = append(addrs, fields[0])
			if !found {
				found = true
				canonical = fields[1]
				aliases = fields[2:]
			}
		}
		if found {
			return canonical, addrs, aliases, true
		}
	}
	// G704: resolving the name the user asked about is the whole point of
	// hostname -i/-f; there is no privilege boundary being crossed here.
	ips, err := net.LookupHost(name) //nolint:gosec
	if err != nil {
		return "", nil, nil, false
	}
	canonical = name
	if cname, err := net.LookupCNAME(name); err == nil {
		canonical = strings.TrimSuffix(cname, ".")
	}
	return canonical, ips, nil, true
}

func hostnameFqdn(name string) string {
	if canonical, _, _, ok := hostLookup(name); ok {
		return canonical
	}
	return name
}

func hostnameAliases(name string) string {
	_, _, aliases, ok := hostLookup(name)
	if !ok {
		return ""
	}
	// Each alias keeps a trailing blank, matching the original's loop.
	out := ""
	for _, a := range aliases {
		out += a + " "
	}
	return out
}

func hostnameAddresses(name string) ([]string, bool) {
	_, addrs, _, ok := hostLookup(name)
	return addrs, ok
}

func hostnameDomain(name string) string {
	if idx := strings.IndexByte(name, '.'); idx >= 0 {
		return name[idx+1:]
	}
	return "(none)"
}

func hostnameShort(name string) string {
	if idx := strings.IndexByte(name, '.'); idx >= 0 {
		return name[:idx]
	}
	return name
}

func cmdTty(args []string) int {
	silent := false
	for _, arg := range args {
		switch arg {
		case "-s", "--silent", "--quiet":
			silent = true
		default:
			fatalf("tty", "extra operand %q", arg)
			return 1
		}
	}
	if !isTerminal(os.Stdin.Fd()) {
		if !silent {
			if _, err := fmt.Fprintln(os.Stdout, "not a tty"); err != nil {
				return 1
			}
		}
		return 1
	}
	if silent {
		return 0
	}
	name, err := os.Readlink("/proc/self/fd/0")
	if err != nil {
		fatalf("tty", "%v", err)
		return 1
	}
	if _, err := fmt.Fprintln(os.Stdout, name); err != nil {
		return 1
	}
	return 0
}

func cmdId(args []string) int {
	mode := byte(0)
	names := false
	var operand string
	for _, arg := range args {
		if len(arg) > 1 && arg[0] == '-' {
			for _, flag := range arg[1:] {
				switch flag {
				case 'u':
					if mode != 0 && mode != 'u' {
						fatalf("id", "options -u, -g, and -G are mutually exclusive")
						return 1
					}
					mode = 'u'
				case 'g':
					if mode != 0 && mode != 'g' {
						fatalf("id", "options -u, -g, and -G are mutually exclusive")
						return 1
					}
					mode = 'g'
				case 'G':
					if mode != 0 && mode != 'G' {
						fatalf("id", "options -u, -g, and -G are mutually exclusive")
						return 1
					}
					mode = 'G'
				case 'n':
					names = true
				default:
					fatalf("id", "invalid option -- '%c'", flag)
					return 1
				}
			}
			continue
		}
		if operand != "" {
			fatalf("id", "extra operand %q", arg)
			return 1
		}
		operand = arg
	}
	if names && mode == 0 {
		fatalf("id", "-n requires -u, -g, or -G")
		return 1
	}
	identity, err := lookupIdentity(operand)
	if err != nil {
		fatalf("id", "%v", err)
		return 1
	}
	groupIDs, _ := identity.GroupIds()
	if len(groupIDs) == 0 {
		groupIDs = []string{identity.Gid}
	}
	groupIDs = orderGroupIDs(identity.Gid, groupIDs)
	switch mode {
	case 'u':
		value := identity.Uid
		if names {
			value = identity.Username
		}
		fmt.Fprintln(os.Stdout, value)
	case 'g':
		value := identity.Gid
		if names {
			value = groupNameString(identity.Gid)
		}
		fmt.Fprintln(os.Stdout, value)
	case 'G':
		values := append([]string(nil), groupIDs...)
		if names {
			for i := range values {
				values[i] = groupNameString(values[i])
			}
		}
		fmt.Fprintln(os.Stdout, strings.Join(values, " "))
	default:
		groups := make([]string, 0, len(groupIDs))
		for _, gid := range groupIDs {
			groups = append(groups, gid+"("+groupNameString(gid)+")")
		}
		fmt.Fprintf(os.Stdout, "uid=%s(%s) gid=%s(%s) groups=%s\n", identity.Uid, identity.Username,
			identity.Gid, groupNameString(identity.Gid), strings.Join(groups, ","))
	}
	return 0
}

// orderGroupIDs puts the primary group first and the supplementary groups in
// ascending numeric order behind it. That is the order id(1) prints in both its
// long form and -G, and the two must agree with each other.
func orderGroupIDs(primary string, groupIDs []string) []string {
	rest := make([]string, 0, len(groupIDs))
	for _, gid := range groupIDs {
		if gid != primary {
			rest = append(rest, gid)
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		a, _ := strconv.Atoi(rest[i])
		b, _ := strconv.Atoi(rest[j])
		return a < b
	})
	return append([]string{primary}, rest...)
}

func lookupIdentity(value string) (*user.User, error) {
	if value == "" {
		return user.Current()
	}
	if _, err := strconv.ParseUint(value, 10, 32); err == nil {
		return user.LookupId(value)
	}
	return user.Lookup(value)
}

func groupNameString(gid string) string {
	group, err := user.LookupGroupId(gid)
	if err != nil {
		return gid
	}
	return group.Name
}
