// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"fmt"
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
	short := false
	for _, arg := range args {
		switch arg {
		case "-s", "--short":
			short = true
		case "--":
		default:
			fatalf("hostname", "unsupported operand %q", arg)
			return 1
		}
	}
	name, err := os.Hostname()
	if err != nil {
		fatalf("hostname", "%v", err)
		return 1
	}
	if short {
		name, _, _ = strings.Cut(name, ".")
	}
	if _, err := fmt.Fprintln(os.Stdout, name); err != nil {
		return 1
	}
	return 0
}

func cmdTty(args []string) int {
	if len(args) != 0 {
		fatalf("tty", "extra operand %q", args[0])
		return 1
	}
	if !isTerminal(os.Stdin.Fd()) {
		if _, err := fmt.Fprintln(os.Stdout, "not a tty"); err != nil {
			return 1
		}
		return 1
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
