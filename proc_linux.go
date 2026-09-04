// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// clockTicks is the kernel's USER_HZ, the unit of the times in /proc/PID/stat.
// A static binary cannot call sysconf(_SC_CLK_TCK), and Linux has used 100 on
// every architecture ba6 targets.
const clockTicks = 100

type processInfo struct {
	pid, ppid, pgrp  int
	session, tpgid   int
	tty              int
	uid              uint32 // effective UID; this is the identity ps displays
	realUID          uint32
	savedUID         uint32
	fsUID            uint32
	user             string
	state            string
	locked           bool
	priority         int
	nice             int
	threads          int
	vsz, rss, shared uint64
	utime, stime     uint64 // clock ticks of user and system time
	cutime, cstime   uint64 // clock ticks used by waited-for children
	startTicks       uint64 // clock ticks between boot and process start
	flags            uint64 // the kernel task flags ps reports as F
	minflt, majflt   uint64
	policy           int // the scheduling class ps reports as CLS
	processor        int // the CPU the process last ran on
	gid              uint32
	realGID          uint32
	comm             string
	args             string
}

// psOptions holds one command line. Selection follows ps(1): the BSD options
// "a" and "x" together lift every restriction, "a" alone keeps the processes
// that hold a terminal, and "x" alone keeps the caller's own.
// psSpec is one output column: the name -o gave and the heading to print,
// which "name=HEADING" replaces.
type psSpec struct {
	name    string
	heading string
	custom  bool
}

// psSelection is everything the selection options ask for. Any list that is
// non-empty restricts the output, and the lists are additive, as in ps(1).
type psSelection struct {
	pids     map[int]bool
	ppids    map[int]bool
	euids    map[uint32]bool
	ruids    map[uint32]bool
	egids    map[uint32]bool
	rgids    map[uint32]bool
	commands map[string]bool
	ttys     map[string]bool
	sessions map[int]bool
	groups   map[int]bool // process groups, from -g
	any      bool
	deselect bool
}

type psOptions struct {
	full                bool
	extraFull           bool
	noFlags             bool // -y: the long format shows RSS in place of ADDR
	cumulative          bool
	forceHeaders        bool
	longFormat          bool
	jobsFormat          bool
	userFormat          bool
	noHeaders           bool
	bsdTerminal, bsdOwn bool
	selection           psSelection
	sortKeys            []psSortKey
	columns             []psSpec
}

// psSortKey is one --sort entry: a column name and whether it was negated.
type psSortKey struct {
	name       string
	descending bool
}

func cmdPs(args []string) int {
	options := psOptions{selection: newPSSelection()}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func(name string) (string, bool) {
			i++
			if i >= len(args) {
				fatalf("ps", "option %s requires an argument", name)
				return "", false
			}
			return args[i], true
		}
		// A long option may carry its argument after "=".
		name, argument, hasArgument := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, argument, hasArgument = arg[:eq], arg[eq+1:], true
			}
		}
		longValue := func() (string, bool) {
			if hasArgument {
				return argument, true
			}
			return value(name)
		}
		switch {
		case name == "--no-headers" || name == "--no-heading" || name == "--noheadings":
			options.noHeaders = true
		case name == "--headers":
			options.forceHeaders = true
		case name == "--cumulative":
			options.cumulative = true
		case name == "--forest":
			// The listing is not drawn as a tree; the order is unchanged.
		case name == "--cols" || name == "--columns" || name == "--width" ||
			name == "--lines" || name == "--rows":
			if _, ok := longValue(); !ok {
				return 1
			}
			// Output is never truncated here, so a screen size changes nothing.
		case name == "--quick-pid":
			v, ok := longValue()
			if !ok || !options.selection.add('p', v) {
				return 1
			}
		case name == "--deselect":
			options.selection.deselect = true
		case name == "--sort" || name == "--Sort":
			v, ok := longValue()
			if !ok || !options.addSortKeys(v) {
				return 1
			}
		case name == "--format":
			v, ok := longValue()
			if !ok {
				return 1
			}
			options.columns = append(options.columns, splitPSColumns(v)...)
		case name == "--pid" || name == "--ppid" || name == "--user" || name == "--User" ||
			name == "--group" || name == "--Group" || name == "--sid" || name == "--tty" ||
			name == "--command":
			v, ok := longValue()
			if !ok || !options.selection.add(psSelectionKind(name), v) {
				return 1
			}
		case strings.HasPrefix(arg, "--"):
			fatalf("ps", "unsupported option %q", arg)
			return 1
		case len(arg) > 1 && arg[0] == '-':
			if !options.parseShort(arg, args, &i) {
				return 1
			}
		default:
			// BSD options carry no dash, so "ps axu" is a single operand
			// naming three of them.
			if err := options.parseBSD(arg); err != nil {
				fatalf("ps", "%v", err)
				return 1
			}
		}
	}
	options.applyDefaultColumns()
	for _, column := range options.columns {
		if _, ok := psColumns[psColumnName(column.name)]; !ok {
			fmt.Fprintf(os.Stderr, "error: unknown user-defined format specifier %q\n", column.name)
			return 1
		}
	}
	for _, key := range options.sortKeys {
		if _, ok := psColumns[psColumnName(key.name)]; !ok {
			fmt.Fprintf(os.Stderr, "error: unknown user-defined sorting specifier %q\n", key.name)
			return 1
		}
	}
	processes, err := readProcesses(options.selection.candidatePIDs())
	if err != nil {
		fatalf("ps", "%v", err)
		return 1
	}

	runtime := newPSRuntime()
	runtime.cumulative = options.cumulative
	selected := options.filter(processes)
	options.sort(selected, runtime)
	writePS(selected, options.columns, runtime, options.noHeaders && !options.forceHeaders)
	// A selection that named nothing is an error, as in the original.
	if options.selection.any && len(selected) == 0 {
		return 1
	}
	return 0
}

func newPSSelection() psSelection {
	return psSelection{
		pids: map[int]bool{}, ppids: map[int]bool{},
		euids: map[uint32]bool{}, ruids: map[uint32]bool{},
		egids: map[uint32]bool{}, rgids: map[uint32]bool{},
		commands: map[string]bool{}, ttys: map[string]bool{},
		sessions: map[int]bool{}, groups: map[int]bool{},
	}
}

// psSelectionKind maps a long option to the selection list it fills.
func psSelectionKind(name string) byte {
	switch name {
	case "--pid":
		return 'p'
	case "--ppid":
		return 'P'
	case "--user":
		return 'u'
	case "--User":
		return 'U'
	case "--group":
		return 'g'
	case "--Group":
		return 'G'
	case "--sid":
		return 's'
	case "--tty":
		return 't'
	case "--command":
		return 'C'
	}
	return 0
}

// parseShort handles one dashed option word, which may bundle several letters
// and end with one that takes a value.
func (o *psOptions) parseShort(arg string, args []string, i *int) bool {
	for j := 1; j < len(arg); j++ {
		flag := arg[j]
		switch flag {
		case 'e', 'A':
			// Every process; this is the default here already.
		case 'f':
			o.full = true
		case 'F':
			o.full, o.extraFull = true, true
		case 'y':
			o.noFlags = true
		case 'c':
			// Scheduling-class output is not a separate format here.
		case 'l':
			o.longFormat = true
		case 'j':
			o.jobsFormat = true
		case 'N':
			o.selection.deselect = true
		case 'w':
			// Wide output. Command lines are never truncated here.
		case 'H':
			// Hierarchy indentation is not drawn; the listing is unchanged.
		case 'p', 'q', 'P', 'u', 'U', 'g', 'G', 's', 't', 'C', 'o', 'O':
			v := arg[j+1:]
			if v == "" {
				*i++
				if *i >= len(args) {
					fatalf("ps", "option requires an argument -- '%c'", flag)
					return false
				}
				v = args[*i]
			}
			switch flag {
			case 'o':
				o.columns = append(o.columns, splitPSColumns(v)...)
			case 'O':
				if !o.addSortKeys(v) {
					return false
				}
			case 'q':
				if !o.selection.add('p', v) {
					return false
				}
			default:
				if !o.selection.add(flag, v) {
					return false
				}
			}
			return true
		default:
			fatalf("ps", "invalid option -- '%c'", flag)
			return false
		}
	}
	return true
}

// add records one selection list. Numeric and named forms are both accepted
// wherever ps accepts them.
func (s *psSelection) add(kind byte, value string) bool {
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' }) {
		s.any = true
		switch kind {
		case 'p':
			number, err := strconv.Atoi(item)
			if err != nil || number < 1 {
				fmt.Fprintln(os.Stderr, "error: process ID list syntax error")
				return false
			}
			s.pids[number] = true
		case 'P':
			number, err := strconv.Atoi(item)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error: process ID list syntax error")
				return false
			}
			s.ppids[number] = true
		case 'u', 'U':
			id, ok := psLookupUser(item)
			if !ok {
				fmt.Fprintln(os.Stderr, "error: user name does not exist")
				return false
			}
			if kind == 'u' {
				s.euids[id] = true
			} else {
				s.ruids[id] = true
			}
		case 'g':
			// -g takes a session id when it is a number and an effective
			// group name otherwise, as in the original.
			if number, err := strconv.Atoi(item); err == nil {
				s.sessions[number] = true
				continue
			}
			id, ok := psLookupGroup(item)
			if !ok {
				fmt.Fprintln(os.Stderr, "error: group name does not exist")
				return false
			}
			s.egids[id] = true
		case 'G':
			id, ok := psLookupGroup(item)
			if !ok {
				fmt.Fprintln(os.Stderr, "error: group name does not exist")
				return false
			}
			s.rgids[id] = true
		case 's':
			number, err := strconv.Atoi(item)
			if err != nil {
				fatalf("ps", "invalid session id")
				return false
			}
			s.sessions[number] = true
		case 't':
			s.ttys[strings.TrimPrefix(strings.TrimPrefix(item, "/dev/"), "tty")] = true
			s.ttys[strings.TrimPrefix(item, "/dev/")] = true
		case 'C':
			s.commands[item] = true
		}
	}
	return true
}

func psLookupUser(item string) (uint32, bool) {
	if id, err := strconv.ParseUint(item, 10, 32); err == nil {
		return uint32(id), true //nolint:gosec // parsed with a 32-bit limit.
	}
	account, err := user.Lookup(item)
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseUint(account.Uid, 10, 32)
	return uint32(id), err == nil //nolint:gosec // parsed with a 32-bit limit.
}

func psLookupGroup(item string) (uint32, bool) {
	if id, err := strconv.ParseUint(item, 10, 32); err == nil {
		return uint32(id), true //nolint:gosec // parsed with a 32-bit limit.
	}
	group, err := user.LookupGroup(item)
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseUint(group.Gid, 10, 32)
	return uint32(id), err == nil //nolint:gosec // parsed with a 32-bit limit.
}

// addSortKeys parses --sort's [+|-]key[,...] list.
func (o *psOptions) addSortKeys(value string) bool {
	for _, item := range strings.Split(value, ",") {
		key := psSortKey{name: item}
		if strings.HasPrefix(item, "-") {
			key = psSortKey{name: item[1:], descending: true}
		} else if strings.HasPrefix(item, "+") {
			key = psSortKey{name: item[1:]}
		}
		if key.name == "" {
			fatalf("ps", "empty sort key")
			return false
		}
		o.sortKeys = append(o.sortKeys, key)
	}
	return true
}

// sort applies --sort. Keys are compared numerically where the column holds a
// number, so that PIDs and sizes order the way ps orders them.
func (o *psOptions) sort(processes []processInfo, runtime psRuntime) {
	if len(o.sortKeys) == 0 {
		return
	}
	sort.SliceStable(processes, func(a, b int) bool {
		for _, key := range o.sortKeys {
			spec := psColumns[psColumnName(key.name)]
			left, right := spec.value(runtime, processes[a]), spec.value(runtime, processes[b])
			cmp := comparePSValues(left, right)
			if cmp == 0 {
				continue
			}
			if key.descending {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
}

func comparePSValues(left, right string) int {
	leftNumber, leftErr := strconv.ParseFloat(strings.TrimSpace(left), 64)
	rightNumber, rightErr := strconv.ParseFloat(strings.TrimSpace(right), 64)
	if leftErr == nil && rightErr == nil {
		switch {
		case leftNumber < rightNumber:
			return -1
		case leftNumber > rightNumber:
			return 1
		}
		return 0
	}
	return strings.Compare(left, right)
}

// parseBSD reads one dashless operand: either a PID list, as in "ps 1 2", or a
// group of BSD option letters, as in "ps axu".
func (o *psOptions) parseBSD(arg string) error {
	if arg != "" && strings.IndexFunc(arg, func(r rune) bool { return r != ',' && (r < '0' || r > '9') }) < 0 {
		if !o.selection.add('p', arg) {
			return fmt.Errorf("invalid PID list")
		}
		return nil
	}
	for _, flag := range arg {
		switch flag {
		case 'a':
			o.bsdTerminal = true
		case 'x':
			o.bsdOwn = true
		case 'u':
			o.userFormat = true
		case 'A':
			// The BSD "A" is the same selection as -e; the dashless "e" means
			// something else entirely (show the environment) and is not
			// supported here.
			o.bsdTerminal, o.bsdOwn = true, true
		case 'w':
			// Wide output. Command lines are never truncated here.
		default:
			return fmt.Errorf("unsupported operand %q", arg)
		}
	}
	return nil
}

func (o *psOptions) applyDefaultColumns() {
	if len(o.columns) > 0 {
		return
	}
	switch {
	case o.userFormat:
		o.columns = psFormat("user", "pid", "pcpu", "pmem", "vsz", "rss", "tname", "stat", "start_time", "bsdtime", "args")
	case o.longFormat:
		// The long format, with the UID as a number and PRI on the kernel's
		// own scale, as in the original. -y drops the flags column and shows
		// the resident size where the address would be.
		if o.noFlags {
			o.columns = psFormat("s", "uid", "pid", "ppid", "c", "opri", "ni", "rss", "sz", "wchan", "tname", "cputime", "ucmd")
			return
		}
		o.columns = psFormat("f", "s", "uid", "pid", "ppid", "c", "opri", "ni", "addr_1", "sz", "wchan", "tname", "cputime", "ucmd")
	case o.jobsFormat:
		o.columns = psFormat("pid", "pgid", "sid", "tname", "cputime", "ucmd")
	case o.full:
		o.columns = []psSpec{{name: "user", heading: "UID", custom: true}}
		if o.extraFull {
			o.columns = append(o.columns, psFormat("pid", "ppid", "c", "sz", "rss", "psr", "stime", "tname", "cputime", "cmd")...)
			return
		}
		o.columns = append(o.columns, psFormat("pid", "ppid", "c", "stime", "tname", "cputime", "cmd")...)
	case o.bsdTerminal || o.bsdOwn:
		o.columns = psFormat("pid", "tname", "stat", "bsdtime", "args")
	default:
		o.columns = psFormat("pid", "stat", "args")
	}
}

// filter applies the selection options. The lists are additive: a process is
// kept when it matches any of them, and -N/--deselect keeps the rest instead.
// Without any list, and without a or x, every process is listed, which is what
// a rescue shell wants and what ba6 has always done.
func (o *psOptions) filter(processes []processInfo) []processInfo {
	if o.selection.any {
		kept := make([]processInfo, 0, len(processes))
		for _, process := range processes {
			if o.selection.matches(process) != o.selection.deselect {
				kept = append(kept, process)
			}
		}
		return kept
	}
	if o.bsdTerminal && o.bsdOwn || !o.bsdTerminal && !o.bsdOwn {
		return processes
	}
	//nolint:gosec // G115: a Linux user ID is a 32-bit unsigned value.
	euid := uint32(os.Geteuid())
	kept := make([]processInfo, 0, len(processes))
	for _, process := range processes {
		if o.bsdTerminal && process.tty != 0 || o.bsdOwn && process.uid == euid {
			kept = append(kept, process)
		}
	}
	return kept
}

// matches reports whether one process is named by any selection list.
func (s *psSelection) matches(p processInfo) bool {
	switch {
	case s.pids[p.pid], s.ppids[p.ppid], s.euids[p.uid], s.ruids[p.realUID],
		s.egids[p.gid], s.rgids[p.realGID], s.sessions[p.session], s.groups[p.pgrp],
		s.commands[p.comm]:
		return true
	}
	if len(s.ttys) > 0 && s.ttys[ttyName(p.tty)] {
		return true
	}
	return false
}

func parsePIDList(value string, result map[int]bool) error {
	for _, part := range strings.Split(value, ",") {
		pid, err := strconv.Atoi(part)
		if err != nil || pid < 1 {
			return fmt.Errorf("invalid pid")
		}
		result[pid] = true
	}
	return nil
}

// splitPSColumns parses one -o argument: a comma or space separated list of
// column names, each optionally carrying its own heading after "=".
func splitPSColumns(value string) []psSpec {
	items := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })
	specs := make([]psSpec, 0, len(items))
	for _, item := range items {
		spec := psSpec{name: strings.ToLower(item)}
		if eq := strings.IndexByte(item, '='); eq >= 0 {
			spec = psSpec{name: strings.ToLower(item[:eq]), heading: item[eq+1:], custom: true}
		}
		specs = append(specs, spec)
	}
	return specs
}

// psFormat turns a list of column names into specs with their default
// headings, for the built-in formats.
func psFormat(names ...string) []psSpec {
	specs := make([]psSpec, len(names))
	for i, name := range names {
		specs[i] = psSpec{name: name}
	}
	return specs
}

// psFit says what ps(1) does with a value wider than its column: it widens the
// PID and command columns to fit, cuts a user name short and marks it with a
// trailing "+", cuts a kernel symbol short without a marker, and lets
// everything else overflow, shifting the rest of that row.
type psFit int

const (
	psOverflow psFit = iota
	psGrow
	psClip
	psTruncate
)

// psColumn describes one output column: its heading, whether values are
// right-aligned, the width ps reserves for it, and what happens when a value
// does not fit.
type psColumn struct {
	heading string
	right   bool
	width   int
	fit     psFit
	value   func(psRuntime, processInfo) string
}

var psColumns = map[string]psColumn{
	"pid":   {"PID", true, 0, psGrow, func(_ psRuntime, p processInfo) string { return strconv.Itoa(p.pid) }},
	"ppid":  {"PPID", true, 0, psGrow, func(_ psRuntime, p processInfo) string { return strconv.Itoa(p.ppid) }},
	"uid":   {"UID", true, 5, psGrow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint(uint64(p.uid), 10) }},
	"user":  {"USER", false, 8, psClip, func(_ psRuntime, p processInfo) string { return p.user }},
	"stat":  {"STAT", false, 4, psOverflow, func(_ psRuntime, p processInfo) string { return psState(p) }},
	"vsz":   {"VSZ", true, 6, psOverflow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint((p.vsz+1023)/1024, 10) }},
	"rss":   {"RSS", true, 5, psOverflow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint((p.rss+1023)/1024, 10) }},
	"pcpu":  {"%CPU", true, 4, psOverflow, func(r psRuntime, p processInfo) string { return r.cpuPercent(p) }},
	"pmem":  {"%MEM", true, 4, psOverflow, func(r psRuntime, p processInfo) string { return r.memoryPercent(p) }},
	"tty":   {"TT", false, 8, psOverflow, func(_ psRuntime, p processInfo) string { return ttyName(p.tty) }},
	"tname": {"TTY", false, 8, psOverflow, func(_ psRuntime, p processInfo) string { return ttyName(p.tty) }},
	// Three spellings of a start time: the BSD "START" column, the seconds
	// -o start prints, and the shorter -o bsdstart.
	"start_time": {"START", true, 5, psOverflow, func(r psRuntime, p processInfo) string { return r.startTime(p) }},
	"start":      {"STARTED", true, 8, psOverflow, func(r psRuntime, p processInfo) string { return r.startedLong(p) }},
	"bsdstart":   {"START", true, 6, psOverflow, func(r psRuntime, p processInfo) string { return r.startedShort(p) }},
	"comm":       {"COMMAND", false, 15, psGrow, func(_ psRuntime, p processInfo) string { return p.comm }},
	"ucmd":       {"CMD", false, 15, psGrow, func(_ psRuntime, p processInfo) string { return p.comm }},
	"args":       {"COMMAND", false, 27, psGrow, func(_ psRuntime, p processInfo) string { return p.args }},
	"cmd":        {"CMD", false, 27, psGrow, func(_ psRuntime, p processInfo) string { return p.args }},

	// The columns the long and jobs formats add, plus the rest of the set
	// that -o accepts.
	"f":    {"F", true, 1, psOverflow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint(p.flags>>6&7, 8) }},
	"s":    {"S", false, 1, psOverflow, func(_ psRuntime, p processInfo) string { return p.state }},
	"c":    {"C", true, 2, psOverflow, func(r psRuntime, p processInfo) string { return strconv.FormatUint(r.cpuTenths(p)/10, 10) }},
	"pri":  {"PRI", true, 3, psOverflow, func(_ psRuntime, p processInfo) string { return strconv.Itoa(39 - p.priority) }},
	"opri": {"PRI", true, 3, psOverflow, func(_ psRuntime, p processInfo) string { return strconv.Itoa(60 + p.priority) }},
	"ni": {"NI", true, 3, psOverflow, func(_ psRuntime, p processInfo) string {
		// A process on a real-time policy has no nice value to show, and the
		// original prints a dash for it.
		if p.policy != 0 {
			return "-"
		}
		return strconv.Itoa(p.nice)
	}},
	"addr":   {"ADDR", true, 4, psOverflow, func(_ psRuntime, _ processInfo) string { return "-" }},
	"addr_1": {"ADDR", false, 1, psOverflow, func(_ psRuntime, _ processInfo) string { return "-" }},
	"sz": {"SZ", true, 5, psOverflow, func(_ psRuntime, p processInfo) string {
		return strconv.FormatUint(p.vsz/uint64(os.Getpagesize()), 10) //nolint:gosec // the page size is positive.
	}},
	"wchan":   {"WCHAN", false, 6, psTruncate, func(_ psRuntime, p processInfo) string { return psWchan(p) }},
	"pgid":    {"PGID", true, 0, psGrow, func(_ psRuntime, p processInfo) string { return strconv.Itoa(p.pgrp) }},
	"sid":     {"SID", true, 0, psGrow, func(_ psRuntime, p processInfo) string { return strconv.Itoa(p.session) }},
	"sess":    {"SESS", true, 0, psGrow, func(_ psRuntime, p processInfo) string { return strconv.Itoa(p.session) }},
	"psr":     {"PSR", true, 3, psOverflow, func(_ psRuntime, p processInfo) string { return strconv.Itoa(p.processor) }},
	"nlwp":    {"NLWP", true, 4, psOverflow, func(_ psRuntime, p processInfo) string { return strconv.Itoa(p.threads) }},
	"thcount": {"THCNT", true, 5, psOverflow, func(_ psRuntime, p processInfo) string { return strconv.Itoa(p.threads) }},
	"etime":   {"ELAPSED", true, 11, psOverflow, func(r psRuntime, p processInfo) string { return psElapsed(r.elapsedSeconds(p)) }},
	"etimes": {"ELAPSED", true, 7, psOverflow, func(r psRuntime, p processInfo) string {
		return strconv.FormatUint(r.elapsedSeconds(p), 10)
	}},
	"ruser":   {"RUSER", false, 8, psClip, func(_ psRuntime, p processInfo) string { return psUserName(p.realUID) }},
	"group":   {"GROUP", false, 8, psClip, func(_ psRuntime, p processInfo) string { return psGroupName(p.gid) }},
	"rgroup":  {"RGROUP", false, 8, psClip, func(_ psRuntime, p processInfo) string { return psGroupName(p.realGID) }},
	"euid":    {"EUID", true, 5, psGrow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint(uint64(p.uid), 10) }},
	"ruid":    {"RUID", true, 5, psGrow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint(uint64(p.realUID), 10) }},
	"gid":     {"GID", true, 5, psGrow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint(uint64(p.gid), 10) }},
	"egid":    {"EGID", true, 5, psGrow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint(uint64(p.gid), 10) }},
	"rgid":    {"RGID", true, 5, psGrow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint(uint64(p.realGID), 10) }},
	"cls":     {"CLS", true, 3, psOverflow, func(_ psRuntime, p processInfo) string { return psSchedClass(p.policy) }},
	"cputime": {"TIME", true, 8, psOverflow, func(_ psRuntime, p processInfo) string { return psCPUTimeLong(p) }},
	"bsdtime": {"TIME", true, 6, psOverflow, func(r psRuntime, p processInfo) string { return psCPUTime(p, r.cumulative) }},
	"lstart":  {"STARTED", true, 24, psOverflow, func(r psRuntime, p processInfo) string { return r.startedAt(p).Format("Mon Jan _2 15:04:05 2006") }},
	"stime":   {"STIME", true, 5, psOverflow, func(r psRuntime, p processInfo) string { return r.startTime(p) }},
	"minflt":  {"MINFLT", true, 6, psOverflow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint(p.minflt, 10) }},
	"majflt":  {"MAJFLT", true, 6, psOverflow, func(_ psRuntime, p processInfo) string { return strconv.FormatUint(p.majflt, 10) }},
}

// psColumnName resolves the spellings ps(1) accepts for the same column.
func psColumnName(column string) string {
	switch column {
	case "%cpu", "pcpu":
		return "pcpu"
	case "%mem", "pmem":
		return "pmem"
	case "command", "args":
		return "args"
	case "ucomm":
		return "comm"
	case "tt", "tty":
		return "tty"
	case "time", "cputime":
		return "cputime"
	case "s", "state":
		return "s"
	case "stat":
		return "stat"
	case "nice":
		return "ni"
	case "flag", "flags":
		return "f"
	case "pgrp", "pgid":
		return "pgid"
	case "session":
		return "sid"
	case "class", "policy":
		return "cls"
	case "uid":
		return "uid"

	case "egroup":
		return "group"
	case "min_flt":
		return "minflt"
	case "maj_flt":
		return "majflt"
	}
	return column
}

// psState builds the STAT field: the run state followed by the modifiers ps
// documents, in its order -- priority, session leader, multithreaded, and
// foreground process group.
func psState(p processInfo) string {
	state := p.state
	switch {
	case p.nice < 0:
		state += "<"
	case p.nice > 0:
		state += "N"
	}
	if p.locked {
		state += "L"
	}
	if p.session == p.pid {
		state += "s"
	}
	if p.threads > 1 {
		state += "l"
	}
	if p.tty != 0 && p.tpgid == p.pgrp {
		state += "+"
	}
	return state
}

// psCPUTimeLong is the [DD-]HH:MM:SS form -o time and the -f and -l formats
// use, where the BSD formats use the shorter psCPUTime.
func psCPUTimeLong(p processInfo) string {
	seconds := (p.utime + p.stime) / clockTicks
	if days := seconds / 86400; days > 0 {
		return fmt.Sprintf("%d-%02d:%02d:%02d", days, seconds/3600%24, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
}

// psElapsed renders a process's wall-clock age the way ELAPSED does:
// [[DD-]HH:]MM:SS.
func psElapsed(seconds uint64) string {
	minutes, secs := seconds/60%60, seconds%60
	switch {
	case seconds >= 86400:
		return fmt.Sprintf("%d-%02d:%02d:%02d", seconds/86400, seconds/3600%24, minutes, secs)
	case seconds >= 3600:
		return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, minutes, secs)
	}
	return fmt.Sprintf("%02d:%02d", minutes, secs)
}

// psSchedClass names a scheduling policy the way ps abbreviates it.
func psSchedClass(policy int) string {
	switch policy {
	case 0:
		return "TS"
	case 1:
		return "FF"
	case 2:
		return "RR"
	case 3:
		return "B"
	case 4:
		return "ISO"
	case 5:
		return "IDL"
	case 6:
		return "DLN"
	}
	return "?"
}

// psWchan reports the kernel function a process is sleeping in. A running or
// unreadable process has none, which ps prints as a dash.
func psWchan(p processInfo) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(p.pid), "wchan"))
	if err != nil {
		return "-"
	}
	name := strings.TrimSpace(string(data))
	if name == "" || name == "0" {
		return "-"
	}
	return name
}

// psCPUTime is the BSD TIME field: whole minutes and seconds, with no hours
// field, so an hour of CPU reads 60:00 rather than 1:00:00.
func psCPUTime(p processInfo, cumulative bool) string {
	ticks := p.utime + p.stime
	if cumulative {
		// --cumulative adds what this process's reaped children used.
		ticks += p.cutime + p.cstime
	}
	seconds := ticks / clockTicks
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

// psRuntime holds the system-wide values the computed columns need, so that
// every row is rendered against one consistent snapshot.
type psRuntime struct {
	uptime   float64
	memTotal uint64
	boot     time.Time
	now      time.Time
	pidWidth int
	// cumulative is --cumulative, which the original applies to the BSD TIME
	// column alone.
	cumulative bool
}

func newPSRuntime() psRuntime {
	runtime := psRuntime{now: time.Now(), pidWidth: 5}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		if fields := strings.Fields(string(data)); len(fields) > 0 {
			runtime.uptime, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	// ps dates a process from the kernel's own boot timestamp, so that two runs
	// a moment apart agree on the minute a process started.
	runtime.boot = runtime.now.Add(-time.Duration(runtime.uptime * float64(time.Second)))
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if seconds, found := strings.CutPrefix(line, "btime "); found {
				if epoch, convErr := strconv.ParseInt(strings.TrimSpace(seconds), 10, 64); convErr == nil {
					runtime.boot = time.Unix(epoch, 0)
				}
				break
			}
		}
	}
	if values, err := readMeminfo(); err == nil {
		runtime.memTotal = values["MemTotal"]
	}
	// ps sizes its PID columns from the largest PID the kernel will hand out.
	if data, err := os.ReadFile("/proc/sys/kernel/pid_max"); err == nil {
		if width := len(strings.TrimSpace(string(data))); width > runtime.pidWidth {
			runtime.pidWidth = width
		}
	}
	return runtime
}

// cpuPercent and memoryPercent mirror the integer arithmetic ps uses: both
// figures are computed in tenths of a percent and truncated rather than
// rounded, so two seconds of CPU spent over an hour of life reads 0.0.
func (r psRuntime) cpuPercent(p processInfo) string {
	return psTenths(r.cpuTenths(p))
}

// cpuTenths is the shared arithmetic behind %CPU and the integer C column.
func (r psRuntime) cpuTenths(p processInfo) uint64 {
	elapsed := r.elapsedSeconds(p)
	if elapsed == 0 {
		return 0
	}
	return (p.utime + p.stime) * 1000 / clockTicks / elapsed
}

// elapsedSeconds is how long the process has been alive.
func (r psRuntime) elapsedSeconds(p processInfo) uint64 {
	uptime := uint64(r.uptime) //nolint:gosec // G115: /proc/uptime is a nonnegative number of seconds.
	started := p.startTicks / clockTicks
	if uptime <= started {
		return 0
	}
	return uptime - started
}

func (r psRuntime) startedAt(p processInfo) time.Time {
	return r.boot.Add(time.Duration(p.startTicks/clockTicks) * time.Second) //nolint:gosec // G115: a tick count is nonnegative and well inside int64 seconds.
}

func (r psRuntime) memoryPercent(p processInfo) string {
	if r.memTotal == 0 {
		return "0.0"
	}
	return psTenths(p.rss * 1000 / r.memTotal)
}

// psTenths prints a value given in tenths of a percent, dropping the decimal
// once the figure reaches 100 percent, as ps does.
func psTenths(tenths uint64) string {
	if tenths > 999 {
		return strconv.FormatUint(tenths/10, 10)
	}
	return fmt.Sprintf("%d.%d", tenths/10, tenths%10)
}

// startTime prints the wall-clock start of a process the way ps does: the time
// of day for processes started today, the date within this year, and otherwise
// the year alone.
func (r psRuntime) startTime(p processInfo) string {
	start := r.startedAt(p)
	switch {
	case start.Year() == r.now.Year() && start.YearDay() == r.now.YearDay():
		return start.Format("15:04")
	case start.Year() == r.now.Year():
		return start.Format("Jan02")
	}
	return start.Format("2006")
}

// startedLong and startedShort are the two other spellings of a start time:
// -o start prints the clock to the second for a process started today and the
// month and day otherwise, and -o bsdstart drops the seconds.
func (r psRuntime) startedLong(p processInfo) string {
	start := r.startedAt(p)
	if start.Year() == r.now.Year() && start.YearDay() == r.now.YearDay() {
		return start.Format("15:04:05")
	}
	return start.Format("Jan _2")
}

func (r psRuntime) startedShort(p processInfo) string {
	start := r.startedAt(p)
	if start.Year() == r.now.Year() && start.YearDay() == r.now.YearDay() {
		return start.Format("15:04")
	}
	return start.Format("Jan _2")
}

// ttyDevices maps a terminal's device number to its name under /dev. It is
// built once, and only for a device the fixed table below does not cover.
var ttyDevices struct {
	once  sync.Once
	names map[uint64]string
}

// ttyName renders the tty_nr field of /proc/PID/stat. The common terminals have
// fixed device numbers; anything else (a hypervisor or USB console, say) is
// looked up in /dev.
func ttyName(device int) string {
	if device == 0 {
		return "?"
	}
	major, minor := (device>>8)&0xfff, (device&0xff)|((device>>12)&0xfff00)
	switch {
	case major == 4 && minor < 64:
		return "tty" + strconv.Itoa(minor)
	case major == 4:
		return "ttyS" + strconv.Itoa(minor-64)
	case major == 5 && minor == 1:
		return "console"
	case major >= 136 && major <= 143:
		return "pts/" + strconv.Itoa((major-136)*256+minor)
	}
	ttyDevices.once.Do(readTTYDevices)
	if name, ok := ttyDevices.names[uint64(device)]; ok { //nolint:gosec // G115: a device number is a nonnegative 32-bit value.
		return name
	}
	return "?"
}

func readTTYDevices() {
	ttyDevices.names = map[uint64]string{}
	for _, directory := range []string{"/dev", "/dev/pts"} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil || info.Mode()&os.ModeCharDevice == 0 {
				continue
			}
			if status, ok := info.Sys().(*syscall.Stat_t); ok {
				ttyDevices.names[status.Rdev] = strings.TrimPrefix(filepath.Join(directory, entry.Name()), "/dev/")
			}
		}
	}
}

func readProcesses(selected map[int]bool) ([]processInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var processes []processInfo
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || len(selected) > 0 && !selected[pid] {
			continue
		}
		process, err := readProcess(pid)
		if err == nil {
			processes = append(processes, process)
		}
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].pid < processes[j].pid })
	return processes, nil
}

func readProcess(pid int) (processInfo, error) {
	var process processInfo
	statData, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return process, err
	}
	text := string(statData)
	left, right := strings.IndexByte(text, '('), strings.LastIndex(text, ") ")
	if left < 0 || right < left {
		return process, fmt.Errorf("invalid stat data")
	}
	process.pid, process.comm = pid, text[left+1:right]
	fields := strings.Fields(text[right+2:])
	if len(fields) < 22 {
		return process, fmt.Errorf("short stat data")
	}
	process.state = fields[0]
	process.ppid, _ = strconv.Atoi(fields[1])
	process.flags, _ = strconv.ParseUint(fields[6], 10, 64)
	process.minflt, _ = strconv.ParseUint(fields[7], 10, 64)
	process.majflt, _ = strconv.ParseUint(fields[9], 10, 64)
	if len(fields) > 38 {
		process.processor, _ = strconv.Atoi(fields[36])
		process.policy, _ = strconv.Atoi(fields[38])
	}
	process.pgrp, _ = strconv.Atoi(fields[2])
	process.session, _ = strconv.Atoi(fields[3])
	process.tty, _ = strconv.Atoi(fields[4])
	process.tpgid, _ = strconv.Atoi(fields[5])
	process.utime, _ = strconv.ParseUint(fields[11], 10, 64)
	process.stime, _ = strconv.ParseUint(fields[12], 10, 64)
	process.cutime, _ = strconv.ParseUint(fields[13], 10, 64)
	process.cstime, _ = strconv.ParseUint(fields[14], 10, 64)
	process.priority, _ = strconv.Atoi(fields[15])
	process.nice, _ = strconv.Atoi(fields[16])
	process.threads, _ = strconv.Atoi(fields[17])
	process.startTicks, _ = strconv.ParseUint(fields[19], 10, 64)
	process.vsz, _ = strconv.ParseUint(fields[20], 10, 64)
	rssPages, _ := strconv.ParseUint(fields[21], 10, 64)
	process.rss = rssPages * uint64(os.Getpagesize()) //nolint:gosec // page size and RSS page count are nonnegative.
	// ps reports the resident size from /proc/PID/statm, which agrees with
	// VmRSS; the counter in stat leaves some resident pages out.
	if resident, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "statm")); err == nil {
		if columns := strings.Fields(string(resident)); len(columns) > 1 {
			if pages, convErr := strconv.ParseUint(columns[1], 10, 64); convErr == nil {
				process.rss = pages * uint64(os.Getpagesize()) //nolint:gosec // the page size is positive.
			}
			if len(columns) > 2 {
				if pages, convErr := strconv.ParseUint(columns[2], 10, 64); convErr == nil {
					process.shared = pages * uint64(os.Getpagesize()) //nolint:gosec // the page size and shared-page count are nonnegative.
				}
			}
		}
	}
	status, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	for _, line := range strings.Split(string(status), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "Uid:":
			// Uid: lists the real, effective, saved and filesystem IDs. ps and
			// top display the effective identity, while top's -U filter may
			// intentionally match any of the four.
			ids := []*uint32{&process.realUID, &process.uid, &process.savedUID, &process.fsUID}
			for index, target := range ids {
				if index+1 >= len(parts) {
					break
				}
				value, convErr := strconv.ParseUint(parts[index+1], 10, 32)
				if convErr == nil {
					*target = uint32(value) //nolint:gosec // parsed with a 32-bit limit.
				}
			}
		case "Gid:":
			// Gid: lists the real, effective, saved and filesystem groups.
			ids := []*uint32{&process.realGID, &process.gid}
			for index, target := range ids {
				if index+1 >= len(parts) {
					break
				}
				value, convErr := strconv.ParseUint(parts[index+1], 10, 32)
				if convErr == nil {
					*target = uint32(value) //nolint:gosec // parsed with a 32-bit limit.
				}
			}
		case "VmLck:":
			// Locked pages are what ps marks with "L" in the STAT column.
			locked, _ := strconv.ParseUint(parts[1], 10, 64)
			process.locked = locked > 0
		}
	}
	process.user = psUserName(process.uid)
	cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	process.args = strings.TrimSpace(psEscapeArgs(string(cmdline)))
	if process.args == "" {
		process.args = "[" + process.comm + "]"
	}
	return process, nil
}

// candidatePIDs narrows the /proc scan to a PID list when that is the only
// selection given; every other list needs the process read before it can be
// judged.
func (s *psSelection) candidatePIDs() map[int]bool {
	if len(s.pids) > 0 && !s.deselect && len(s.ppids) == 0 && len(s.euids) == 0 &&
		len(s.ruids) == 0 && len(s.egids) == 0 && len(s.rgids) == 0 &&
		len(s.commands) == 0 && len(s.ttys) == 0 && len(s.sessions) == 0 && len(s.groups) == 0 {
		return s.pids
	}
	return nil
}

// psEscapeArgs renders a raw command line the way ps does: the NUL between
// arguments and any newline inside one become a space, and every other control
// byte becomes a question mark, so one process never spills onto two lines.
// Bytes above ASCII are left alone, which is what the original does in a UTF-8
// terminal.
func psEscapeArgs(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case c == 0 || c == '\n':
			b.WriteByte(' ')
		case c < 0x20 || c == 0x7f:
			b.WriteByte('?')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// psUserName and psGroupName resolve an id to a name, falling back to the
// number the way ps does when no account or group claims it.
// The pure-Go account lookups re-read /etc/passwd and /etc/group on every
// call, so a listing of a few hundred processes remembers what it has already
// resolved.
var psNames = struct {
	users  map[uint32]string
	groups map[uint32]string
}{users: map[uint32]string{}, groups: map[uint32]string{}}

func psUserName(id uint32) string {
	if name, cached := psNames.users[id]; cached {
		return name
	}
	name := strconv.FormatUint(uint64(id), 10)
	if account, err := user.LookupId(name); err == nil {
		name = account.Username
	}
	psNames.users[id] = name
	return name
}

func psGroupName(id uint32) string {
	if name, cached := psNames.groups[id]; cached {
		return name
	}
	name := strconv.FormatUint(uint64(id), 10)
	if group, err := user.LookupGroupId(name); err == nil {
		name = group.Name
	}
	psNames.groups[id] = name
	return name
}

// writePS lays the table out the way ps does: every column is as wide as its
// heading, its reserved width, and its widest value, values are right-aligned
// for the numeric columns, and the last column is never padded.
func writePS(processes []processInfo, columns []psSpec, runtime psRuntime, noHeaders bool) {
	specs := make([]psColumn, len(columns))
	widths := make([]int, len(columns))
	allEmpty := true
	for i, column := range columns {
		name := psColumnName(column.name)
		specs[i] = psColumns[name]
		if name == "pid" || name == "ppid" || name == "pgid" || name == "sid" || name == "sess" {
			specs[i].width = runtime.pidWidth
		}
		if column.custom {
			specs[i].heading = column.heading
		}
		if specs[i].heading != "" {
			allEmpty = false
		}
		// A column keeps the width its spec declares even when its heading is
		// longer: the heading then overflows exactly as an oversized value
		// does, which is how the original lays out -l's one-character ADDR.
		widths[i] = specs[i].width
		if widths[i] == 0 {
			widths[i] = len(specs[i].heading)
		}
	}
	// A format whose every heading was blanked with "name=" prints no header
	// line at all, as in the original.
	if allEmpty {
		noHeaders = true
	}
	rows := make([][]string, 0, len(processes))
	for _, process := range processes {
		row := make([]string, len(columns))
		for i, spec := range specs {
			row[i] = spec.value(runtime, process)
			switch {
			case spec.fit == psGrow && i == len(specs)-1:
				// The last column runs as long as it likes.
				widths[i] = maxInt(widths[i], len(row[i]))
			case spec.fit == psGrow && len(row[i]) > widths[i]:
				// One that is followed by another is cut to its width.
				row[i] = row[i][:widths[i]]
			case spec.fit == psClip && len(row[i]) > widths[i]:
				row[i] = row[i][:widths[i]-1] + "+"
			case spec.fit == psTruncate && len(row[i]) > widths[i]:
				row[i] = row[i][:widths[i]]
			}
		}
		rows = append(rows, row)
	}
	if !noHeaders {
		writePSRow(specs, widths, psHeadings(specs), true)
	}
	for _, row := range rows {
		writePSRow(specs, widths, row, false)
	}
}

func psHeadings(specs []psColumn) []string {
	headings := make([]string, len(specs))
	for i, spec := range specs {
		headings[i] = spec.heading
	}
	return headings
}

// writePSRow lays one row onto the column grid. ps keeps every column at a
// fixed position: a value wider than its column pushes the next ones right,
// and the first left-aligned column with padding to spare absorbs the overrun
// so the grid recovers.
func writePSRow(specs []psColumn, widths []int, fields []string, header bool) {
	var line strings.Builder
	target := 0
	// shifted records that a left-aligned value ran past its column, which
	// changes how the right-aligned columns after it are padded.
	shifted := false
	for i, field := range fields {
		if i > 0 {
			target++ // the single space between columns
		}
		target += widths[i]
		// A right-aligned column normally catches up to its place on the
		// grid, keeping at least the separating space, which is how a row
		// recovers after an oversized number. After a *left*-aligned value
		// overflowed, though, the original keeps the following right-aligned
		// columns at their full width instead, so the whole row stays shifted
		// until a left-aligned column absorbs it. The heading line always
		// catches up, which is how -l's one-character ADDR column ends up
		// with its four-character heading followed by a single space.
		if specs[i].right {
			padding := target - line.Len() - len(field)
			if shifted && !header {
				padding = widths[i] - len(field)
				if i > 0 {
					padding++ // the separating space
				}
			}
			if i > 0 && padding < 1 {
				padding = 1
			}
			line.WriteString(strings.Repeat(" ", maxInt(0, padding)) + field)
			continue
		}
		if i > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(field)
		if line.Len() > target {
			shifted = true
		} else {
			shifted = false
		}
		if i < len(fields)-1 {
			line.WriteString(strings.Repeat(" ", maxInt(0, target-line.Len())))
		}
	}
	fmt.Fprintln(os.Stdout, line.String())
}

var signalNames = map[string]syscall.Signal{
	"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT,
	"ILL": syscall.SIGILL, "ABRT": syscall.SIGABRT, "FPE": syscall.SIGFPE,
	"KILL": syscall.SIGKILL, "SEGV": syscall.SIGSEGV, "PIPE": syscall.SIGPIPE,
	"ALRM": syscall.SIGALRM, "TERM": syscall.SIGTERM, "USR1": syscall.SIGUSR1,
	"USR2": syscall.SIGUSR2, "CHLD": syscall.SIGCHLD, "CONT": syscall.SIGCONT,
	"STOP": syscall.SIGSTOP, "TSTP": syscall.SIGTSTP, "TTIN": syscall.SIGTTIN,
	"TTOU": syscall.SIGTTOU,
}

func cmdKill(args []string) int {
	signal := syscall.SIGTERM
	list := false
	for len(args) > 0 {
		arg := args[0]
		switch {
		case arg == "--":
			args = args[1:]
			goto send
		case arg == "-l" || arg == "--list":
			list, args = true, args[1:]
			goto send
		case arg == "-s" || arg == "--signal":
			if len(args) < 2 {
				fatalf("kill", "-s requires a signal")
				return 1
			}
			parsed, err := parseSignal(args[1])
			if err != nil {
				fatalf("kill", "%v", err)
				return 1
			}
			signal, args = parsed, args[2:]
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			parsed, err := parseSignal(arg[1:])
			if err != nil {
				goto send
			}
			signal, args = parsed, args[1:]
		default:
			goto send
		}
	}
send:
	if list {
		if len(args) == 1 {
			parsed, err := parseSignal(args[0])
			if err != nil {
				fatalf("kill", "%v", err)
				return 1
			}
			fmt.Fprintln(os.Stdout, signalName(parsed))
			return 0
		}
		if len(args) != 0 {
			fatalf("kill", "too many arguments for -l")
			return 1
		}
		names := make([]string, 0, len(signalNames))
		for name := range signalNames {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintln(os.Stdout, strings.Join(names, " "))
		return 0
	}
	if len(args) == 0 {
		fatalf("kill", "missing process ID")
		return 1
	}
	status := 0
	for _, value := range args {
		pid, err := strconv.Atoi(value)
		if err != nil {
			fatalf("kill", "invalid process ID %q", value)
			status = 1
			continue
		}
		if err := syscall.Kill(pid, signal); err != nil {
			fatalf("kill", "%s: %v", value, err)
			status = 1
		}
	}
	return status
}

func parseSignal(value string) (syscall.Signal, error) {
	value = strings.TrimPrefix(strings.ToUpper(value), "SIG")
	if number, err := strconv.Atoi(value); err == nil && number >= 0 && number < 128 {
		return syscall.Signal(number), nil
	}
	if signal, ok := signalNames[value]; ok {
		return signal, nil
	}
	return 0, fmt.Errorf("unknown signal %q", value)
}

func signalName(signal syscall.Signal) string {
	for name, value := range signalNames {
		if value == signal {
			return name
		}
	}
	return strconv.Itoa(int(signal))
}

// freeOptions is one free(1) command line: the unit the numbers are printed
// in, which extra rows are drawn, and how many times to repeat.
type freeOptions struct {
	unit      uint64
	human     bool
	si        bool
	wide      bool
	lohi      bool
	total     bool
	committed bool
	line      bool
	count     int64
	interval  float64
	repeat    bool
}

func cmdFree(args []string) int {
	options := freeOptions{unit: 1024, count: 1, interval: 1}
	// The unit options come in two families: -b/-k/-m/-g and their --kibi
	// spellings are powers of 1024, while --kilo and its relatives are powers
	// of 1000. Both are ignored under -h, which scales each value itself.
	units := map[string]uint64{
		"b": 1, "bytes": 1,
		"k": 1 << 10, "kibi": 1 << 10, "m": 1 << 20, "mebi": 1 << 20,
		"g": 1 << 30, "gibi": 1 << 30, "tebi": 1 << 40, "pebi": 1 << 50,
		"kilo": 1e3, "mega": 1e6, "giga": 1e9, "tera": 1e12, "peta": 1e15,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name := strings.TrimLeft(arg, "-")
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return freeUsage("unrecognized option '%s'", arg)
		}
		takeValue := func() (string, bool) {
			if eq := strings.IndexByte(arg, '='); strings.HasPrefix(arg, "--") && eq >= 0 {
				return arg[eq+1:], true
			}
			i++
			if i >= len(args) {
				return "", false
			}
			return args[i], true
		}
		if eq := strings.IndexByte(name, '='); strings.HasPrefix(arg, "--") && eq >= 0 {
			name = name[:eq]
		}
		if size, ok := units[name]; ok && (len(name) > 1 || len(arg) == 2) {
			options.unit = size
			continue
		}
		switch name {
		case "h", "human":
			options.human = true
		case "si":
			options.si = true
		case "w", "wide":
			options.wide = true
		case "l", "lohi":
			options.lohi = true
		case "t", "total":
			options.total = true
		case "v", "committed":
			options.committed = true
		case "L", "line":
			options.line = true
		case "s", "seconds":
			value, ok := takeValue()
			if !ok {
				return freeUsage("option requires an argument -- '%s'", name)
			}
			seconds, err := strconv.ParseFloat(value, 64)
			if err != nil || seconds <= 0 {
				fatalf("free", "seconds argument failed: '%s': Invalid argument", value)
				return 1
			}
			options.interval, options.repeat = seconds, true
		case "c", "count":
			value, ok := takeValue()
			if !ok {
				return freeUsage("option requires an argument -- '%s'", name)
			}
			count, err := strconv.ParseInt(value, 10, 64)
			if err != nil || count < 1 {
				fatalf("free", "failed to parse count argument: '%s': Numerical result out of range", value)
				return 1
			}
			options.count = count
		default:
			if len(arg) == 2 {
				return freeUsage("invalid option -- '%s'", name)
			}
			return freeUsage("unrecognized option '%s'", arg)
		}
	}
	// -s with no -c repeats for ever; -c with no -s uses procps' one second.
	if options.repeat && options.count == 1 {
		options.count = -1
	}
	for iteration := int64(0); options.count < 0 || iteration < options.count; iteration++ {
		if iteration > 0 {
			fmt.Fprintln(os.Stdout)
			time.Sleep(time.Duration(options.interval * float64(time.Second)))
		}
		if status := freeReport(&options); status != 0 {
			return status
		}
	}
	return 0
}

func freeUsage(format string, a ...interface{}) int {
	fatalf("free", format, a...)
	fmt.Fprintln(os.Stderr)
	if err := writeAppletHelp(os.Stderr, "free"); err != nil {
		fatalf("free", "%v", err)
	}
	return 1
}

// freeReport draws one block: the memory and swap rows, plus whichever of the
// low/high, total and committed rows were asked for — or, under -L, the whole
// thing as procps' single line.
func freeReport(options *freeOptions) int {
	values, err := readMeminfo()
	if err != nil {
		fatalf("free", "%v", err)
		return 1
	}
	total, free := values["MemTotal"], values["MemFree"]
	cache := values["Buffers"] + values["Cached"] + values["SReclaimable"]
	available := values["MemAvailable"]
	// "used" is what people read off free, so it has to be the same subtraction
	// the original makes: everything the kernel says is not available, not just
	// what is neither free nor reclaimable cache.
	used := uint64(0)
	switch {
	case available > 0 && total > available:
		used = total - available
	case total > free+cache:
		used = total - free - cache
	}
	swapTotal, swapFree := values["SwapTotal"], values["SwapFree"]
	swapUsed := swapTotal - swapFree
	format := func(value uint64) string {
		if options.human {
			return scaleMemoryBase(value, options.si)
		}
		unit := options.unit
		if options.si && unit == 1024 {
			// --si without a unit of its own moves the default to powers of 1000.
			unit = 1000
		}
		return strconv.FormatUint(value/unit, 10)
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush() //nolint:errcheck // a sticky write error is reported by the final Flush.
	if options.line {
		// The MemUse label carries a leading space of its own, which is what
		// lines the single-line form's four fields up.
		for _, field := range []struct {
			label string
			value uint64
		}{{"SwapUse", swapUsed}, {"CachUse", cache}, {" MemUse", used}, {"MemFree", free}} {
			fmt.Fprintf(out, "%s %11s ", field.label, format(field.value))
		}
		fmt.Fprintln(out)
		return 0
	}
	row := func(label string, values ...uint64) {
		fmt.Fprintf(out, "%-8s", label)
		for _, value := range values {
			fmt.Fprintf(out, "%12s", format(value))
		}
		fmt.Fprintln(out)
	}
	headings := []string{"total", "used", "free", "shared", "buff/cache", "available"}
	if options.wide {
		headings = []string{"total", "used", "free", "shared", "buffers", "cache", "available"}
	}
	fmt.Fprintf(out, "%-8s", "")
	for _, heading := range headings {
		fmt.Fprintf(out, "%12s", heading)
	}
	fmt.Fprintln(out)
	if options.wide {
		row("Mem:", total, used, free, values["Shmem"], values["Buffers"], cache-values["Buffers"], available)
	} else {
		row("Mem:", total, used, free, values["Shmem"], cache, available)
	}
	if options.lohi {
		// Without a high-memory zone — every 64-bit kernel — the low rows are
		// the whole of memory and the high rows are zero, as procps prints them.
		highTotal, highFree := values["HighTotal"], values["HighFree"]
		lowTotal, lowFree := total-highTotal, free-highFree
		row("Low:", lowTotal, lowTotal-lowFree, lowFree)
		row("High:", highTotal, highTotal-highFree, highFree)
	}
	row("Swap:", swapTotal, swapUsed, swapFree)
	if options.total {
		row("Total:", total+swapTotal, used+swapUsed, free+swapFree)
	}
	if options.committed {
		limit, committed := values["CommitLimit"], values["Committed_AS"]
		remaining := uint64(0)
		if limit > committed {
			remaining = limit - committed
		}
		row("Comm:", limit, committed, remaining)
	}
	return 0
}

// scaleMemory formats a byte count for free -h. It deliberately does not use
// humanSize: free labels its units IEC-style (Ki, Mi, Gi) and truncates whole
// numbers where the coreutils tools round up, so 11.7 GiB reads as 11Gi.
func scaleMemory(value uint64) string { return scaleMemoryBase(value, false) }

// scaleMemoryBase is the same scaling in either base: --si asks for powers of
// 1000, which free labels with the bare letter where the default IEC form uses
// the two-letter prefix.
func scaleMemoryBase(value uint64, si bool) string {
	suffixes := []string{"B", "Ki", "Mi", "Gi", "Ti", "Pi", "Ei"}
	step := uint64(1024)
	if si {
		suffixes = []string{"B", "K", "M", "G", "T", "P", "E"}
		step = 1000
	}
	divisor := uint64(1)
	index := 0
	for index < len(suffixes)-1 && value/divisor >= step {
		divisor *= step
		index++
	}
	if whole := value / divisor; whole < 10 && index > 0 {
		return fmt.Sprintf("%.1f%s", float64(value)/float64(divisor), suffixes[index])
	}
	return fmt.Sprintf("%d%s", value/divisor, suffixes[index])
}

func readMeminfo() (map[string]uint64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := make(map[string]uint64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			values[strings.TrimSuffix(fields[0], ":")] = value * 1024
		}
	}
	return values, scanner.Err()
}
