// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// modeComputer derives a file's new mode from its current mode, given whether
// it is a directory (needed for symbolic 'X').
type modeComputer func(old os.FileMode, isDir bool) os.FileMode

// chmodUsage reports a command-line mistake the way chmod does, with the
// "Try ..." line after the diagnostic.
func chmodUsage(format string, a ...interface{}) int {
	fatalf("chmod", format, a...)
	fmt.Fprintln(os.Stderr, "Try 'chmod --help' for more information.")
	return 1
}

func cmdChmod(args []string) int {
	args = expandShortOptions(args, "")
	recursive, verbose, changesOnly, quiet := false, false, false, false
	preserveRoot := false
	reference := ""
	haveReference := false
	var operands []string
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case parsing && arg == "--":
			parsing = false
		case parsing && (arg == "-R" || arg == "--recursive"):
			recursive = true
		case parsing && (arg == "-v" || arg == "--verbose"):
			verbose = true
		case parsing && (arg == "-c" || arg == "--changes"):
			changesOnly = true
		case parsing && (arg == "-f" || arg == "--silent" || arg == "--quiet"):
			quiet = true
		case parsing && arg == "--reference":
			i++
			if i >= len(args) {
				fatalf("chmod", "option '--reference' requires an argument")
				return 1
			}
			reference, haveReference = args[i], true
		case parsing && strings.HasPrefix(arg, "--reference="):
			reference, haveReference = strings.TrimPrefix(arg, "--reference="), true
		case parsing && arg == "--preserve-root":
			preserveRoot = true
		case parsing && arg == "--no-preserve-root":
			preserveRoot = false
		case parsing && strings.HasPrefix(arg, "--"):
			return chmodUsage("unrecognized option '%s'", arg)
		case parsing && len(arg) > 1 && arg[0] == '-':
			return chmodUsage("invalid option -- '%c'", arg[1])
		default:
			operands = append(operands, arg)
		}
	}

	var compute modeComputer
	var files []string
	if haveReference {
		if len(operands) < 1 {
			return chmodUsage("missing file operand")
		}
		refInfo, err := os.Stat(reference)
		if err != nil {
			fatalf("chmod", "cannot stat attributes of '%s': %v", reference, errText(err))
			return 1
		}
		target := refInfo.Mode()
		compute = func(os.FileMode, bool) os.FileMode { return target }
		files = operands
	} else {
		if len(operands) < 1 {
			return chmodUsage("missing operand")
		}
		if len(operands) < 2 {
			return chmodUsage("missing operand after '%s'", operands[0])
		}
		spec := operands[0]
		if len(spec) > 0 && spec[0] >= '0' && spec[0] <= '9' {
			parsed, err := strconv.ParseUint(strings.TrimPrefix(spec, "0o"), 8, 32)
			if err != nil || parsed > 0o7777 {
				return chmodUsage("invalid mode: '%s'", spec)
			}
			target := fileModeFromOctal(parsed)
			compute = func(os.FileMode, bool) os.FileMode { return target }
		} else {
			ops, err := parseSymbolicMode(spec)
			if err != nil {
				return chmodUsage("%v", err)
			}
			umask := currentUmask()
			compute = func(old os.FileMode, isDir bool) os.FileMode {
				bits := octalFromFileMode(old)
				for _, op := range ops {
					bits = applyChmodClause(bits, isDir, op, umask)
				}
				return fileModeFromOctal(bits)
			}
		}
		files = operands[1:]
	}

	if recursive {
		if preserveRoot {
			for _, path := range files {
				if filepath.Clean(path) == "/" {
					fatalf("chmod", "it is dangerous to operate recursively on '/'")
					fmt.Fprintln(os.Stderr, "chmod: use --no-preserve-root to override this failsafe")
					return 1
				}
			}
		}
		return chmodRecursive(files, compute, quiet, verbose, changesOnly)
	}
	return chmodApply(files, compute, quiet, verbose, changesOnly)
}

func chmodApply(paths []string, compute modeComputer, quiet, verbose, changesOnly bool) int {
	status := 0
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			if !quiet {
				fatalf("chmod", "cannot access '%s': %v", path, errText(err))
			}
			status = 1
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		newMode := compute(info.Mode(), info.IsDir())
		if err := os.Chmod(path, newMode); err != nil {
			if !quiet {
				fatalf("chmod", "cannot change '%s': %v", path, errText(err))
			}
			status = 1
			continue
		}
		reportChmod(path, info.Mode(), newMode, verbose, changesOnly)
	}
	return status
}

// chmodRecursive reads each directory before applying a possibly restrictive
// final mode. If a directory is initially unreadable, owner traversal bits are
// enabled temporarily so chmod can also be used to repair locked trees.
func chmodRecursive(roots []string, compute modeComputer, quiet, verbose, changesOnly bool) int {
	status := 0
	// The walk is collected in the order the original reports in — a directory
	// before its contents — but applied in the opposite order, so a mode that
	// takes away a directory's search bit is set only once everything inside
	// it has been reached. The reports are held back and printed in walk order
	// so the output still matches.
	type chmodStep struct {
		path   string
		info   os.FileInfo
		report string
	}
	var steps []*chmodStep
	var visit func(string)
	visit = func(path string) {
		info, err := os.Lstat(path)
		if err != nil {
			if !quiet {
				fatalf("chmod", "cannot access '%s': %v", path, errText(err))
			}
			status = 1
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return
		}
		steps = append(steps, &chmodStep{path: path, info: info})
		if !info.IsDir() {
			return
		}
		entries, readErr := os.ReadDir(path)
		if os.IsPermission(readErr) {
			// A directory the caller cannot read is opened up for the walk, so
			// that chmod can repair a tree that has locked itself out.
			if err := os.Chmod(path, info.Mode()|0o700); err == nil {
				entries, readErr = os.ReadDir(path)
			} else {
				readErr = err
			}
		}
		if readErr != nil {
			if !quiet {
				fatalf("chmod", "cannot access '%s': %v", path, errText(readErr))
			}
			status = 1
			return
		}
		for _, entry := range entries {
			visit(filepath.Join(path, entry.Name()))
		}
	}
	// Each operand is applied and reported before the next one starts, so
	// naming a directory twice reads the second pass's modes, as it does in
	// the original.
	apply := func() {
		for i := len(steps) - 1; i >= 0; i-- {
			step := steps[i]
			newMode := compute(step.info.Mode(), step.info.IsDir())
			if err := os.Chmod(step.path, newMode); err != nil {
				if !quiet {
					fatalf("chmod", "cannot change '%s': %v", step.path, errText(err))
				}
				status = 1
				continue
			}
			step.report = chmodReport(step.path, step.info.Mode(), newMode, verbose, changesOnly)
		}
		for _, step := range steps {
			if step.report != "" {
				fmt.Print(step.report)
			}
		}
		steps = steps[:0]
	}
	for _, root := range roots {
		visit(root)
		apply()
	}
	return status
}

// reportChmod prints the -v/-c change line GNU chmod does; it is a no-op
// unless one of those flags was given.
func reportChmod(path string, old, updated os.FileMode, verbose, changesOnly bool) {
	fmt.Print(chmodReport(path, old, updated, verbose, changesOnly))
}

// chmodReport builds that line, so a recursive run can hold it back and print
// the batch in walk order.
func chmodReport(path string, old, updated os.FileMode, verbose, changesOnly bool) string {
	if !verbose && !changesOnly {
		return ""
	}
	oldBits, newBits := octalFromFileMode(old), octalFromFileMode(updated)
	if oldBits == newBits {
		if verbose {
			return fmt.Sprintf("mode of '%s' retained as 0%03o (%s)\n", path, newBits, modeString(updated)[1:])
		}
		return ""
	}
	return fmt.Sprintf("mode of '%s' changed from 0%03o (%s) to 0%03o (%s)\n",
		path, oldBits, modeString(old)[1:], newBits, modeString(updated)[1:])
}

// currentUmask peeks at the process umask without permanently changing it.
func currentUmask() uint64 {
	old := syscall.Umask(0)
	syscall.Umask(old)
	return uint64(old) //nolint:gosec // umask(2) always returns a small mode value, never negative
}

type chmodClauseOp struct {
	who        string
	omittedWho bool
	op         byte
	permChars  string
	copyFrom   byte
}

// parseSymbolicMode parses a GNU chmod symbolic mode spec such as "u+x",
// "go-r", "a=rw", "u=rwx,g=rx" or "u+x-w" into the sequence of clauses to
// apply in order.
func parseSymbolicMode(spec string) ([]chmodClauseOp, error) {
	var ops []chmodClauseOp
	for _, clause := range strings.Split(spec, ",") {
		who, i := parseChmodWho(clause)
		omittedWho := who == ""
		if omittedWho {
			who = "ugo"
		}
		if i >= len(clause) || !strings.ContainsRune("+-=", rune(clause[i])) {
			return nil, fmt.Errorf("invalid mode: '%s'", spec)
		}
		for i < len(clause) {
			op := clause[i]
			i++
			start := i
			for i < len(clause) && strings.ContainsRune("rwxXst", rune(clause[i])) {
				i++
			}
			permChars := clause[start:i]
			var copyFrom byte
			if permChars == "" && i < len(clause) && strings.ContainsRune("ugo", rune(clause[i])) {
				copyFrom = clause[i]
				i++
			}
			ops = append(ops, chmodClauseOp{who: who, omittedWho: omittedWho, op: op, permChars: permChars, copyFrom: copyFrom})
			if i < len(clause) && strings.ContainsRune("+-=", rune(clause[i])) {
				continue
			}
			break
		}
		if i != len(clause) {
			return nil, fmt.Errorf("invalid mode: '%s'", spec)
		}
	}
	return ops, nil
}

func parseChmodWho(clause string) (string, int) {
	who := ""
	i := 0
	for i < len(clause) {
		switch clause[i] {
		case 'a':
			who = "ugo"
		case 'u', 'g', 'o':
			if !strings.ContainsRune(who, rune(clause[i])) {
				who += string(clause[i])
			}
		default:
			return who, i
		}
		i++
	}
	return who, i
}

// applyChmodClause applies one parsed clause to a 12-bit traditional mode
// value, returning the updated bits. umask only affects rwx bits, and only
// when the clause's who list was omitted (defaulted to "a").
func applyChmodClause(bits uint64, isDir bool, c chmodClauseOp, umask uint64) uint64 {
	var rwx uint64
	hasRWX := false
	setuid, setgid, sticky, touchSpecial := false, false, false, false
	if c.copyFrom != 0 {
		hasRWX = true
		switch c.copyFrom {
		case 'u':
			rwx = (bits >> 6) & 0o7
		case 'g':
			rwx = (bits >> 3) & 0o7
		case 'o':
			rwx = bits & 0o7
		}
	} else {
		for _, p := range c.permChars {
			switch p {
			case 'r':
				rwx |= 0o4
				hasRWX = true
			case 'w':
				rwx |= 0o2
				hasRWX = true
			case 'x':
				rwx |= 0o1
				hasRWX = true
			case 'X':
				hasRWX = true
				if isDir || bits&0o111 != 0 {
					rwx |= 0o1
				}
			case 's':
				touchSpecial, setuid, setgid = true, true, true
			case 't':
				touchSpecial, sticky = true, true
			}
		}
	}
	for _, w := range c.who {
		var shift uint
		switch w {
		case 'u':
			shift = 6
		case 'g':
			shift = 3
		case 'o':
			shift = 0
		}
		classRWX := rwx
		if c.omittedWho && hasRWX {
			classRWX &^= (umask >> shift) & 0o7
		}
		switch c.op {
		case '+':
			if hasRWX {
				bits |= classRWX << shift
			}
			if touchSpecial {
				if setuid && w == 'u' {
					bits |= 0o4000
				}
				if setgid && w == 'g' {
					bits |= 0o2000
				}
				if sticky {
					bits |= 0o1000
				}
			}
		case '-':
			if hasRWX {
				bits &^= classRWX << shift
			}
			if touchSpecial {
				if setuid && w == 'u' {
					bits &^= 0o4000
				}
				if setgid && w == 'g' {
					bits &^= 0o2000
				}
				if sticky {
					bits &^= 0o1000
				}
			}
		case '=':
			bits &^= 0o7 << shift
			if hasRWX {
				bits |= classRWX << shift
			}
			switch w {
			case 'u':
				if setuid {
					bits |= 0o4000
				} else {
					bits &^= 0o4000
				}
			case 'g':
				if setgid {
					bits |= 0o2000
				} else {
					bits &^= 0o2000
				}
			case 'o':
				if sticky {
					bits |= 0o1000
				} else {
					bits &^= 0o1000
				}
			}
		}
	}
	return bits
}

// ownerChange is one chown or chgrp command line: which ids to set, which
// files to touch, and how much to say about it.
type ownerOptions struct {
	prog         string
	recursive    bool
	changes      bool // -c
	quiet        bool // -f
	verbose      bool // -v
	noDeref      bool // -h
	deref        derefMode
	preserveRoot bool
	// fromUID and fromGID hold --from's condition, or -1 when it was not given.
	fromUID, fromGID int
	uid, gid         int
	// showGroup is set when chown was given a group, which is what makes its
	// messages name owner and group together.
	showGroup bool
	status    int
}

func cmdChown(args []string) int {
	options, operands, ok := parseOwnerOptions("chown", args)
	if !ok {
		return 1
	}
	// --reference supplies both ids, so it takes the place of the owner operand.
	if options.uid == -2 {
		if len(operands) == 0 {
			return ownerUsage("chown", "missing operand")
		}
		if len(operands) == 1 {
			return ownerUsage("chown", "missing operand after '%s'", operands[0])
		}
		uid, gid, err := parseOwnerSpec(operands[0])
		if err != nil {
			// The original quotes the whole operand, whichever half was bad.
			what := "user"
			if strings.Contains(err.Error(), "group") {
				what = "group"
			}
			fatalf("chown", "invalid %s: '%s'", what, operands[0])
			return 1
		}
		options.uid, options.gid, options.showGroup = uid, gid, gid >= 0
		operands = operands[1:]
	}
	if len(operands) == 0 {
		return ownerUsage("chown", "missing operand")
	}
	return options.apply(operands)
}

func cmdChgrp(args []string) int {
	options, operands, ok := parseOwnerOptions("chgrp", args)
	if !ok {
		return 1
	}
	if options.uid == -2 {
		if len(operands) == 0 {
			return ownerUsage("chgrp", "missing operand")
		}
		if len(operands) == 1 {
			return ownerUsage("chgrp", "missing operand after '%s'", operands[0])
		}
		gid, err := resolveGroup(operands[0])
		if err != nil {
			fatalf("chgrp", "invalid group: '%s'", operands[0])
			return 1
		}
		options.uid, options.gid = -1, gid
		operands = operands[1:]
	}
	options.uid = -1
	if len(operands) == 0 {
		return ownerUsage("chgrp", "missing operand")
	}
	return options.apply(operands)
}

func ownerUsage(prog, format string, a ...interface{}) int {
	fatalf(prog, format, a...)
	fmt.Fprintf(os.Stderr, "Try '%s --help' for more information.\n", prog)
	return 1
}

// parseOwnerOptions reads the options both tools share. The returned uid is
// -2 when neither --reference nor an owner operand has supplied one yet, which
// is how the caller knows to take the next operand as the owner.
//
//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func parseOwnerOptions(prog string, args []string) (ownerOptions, []string, bool) {
	options := ownerOptions{prog: prog, fromUID: -1, fromGID: -1, uid: -2, gid: -1}
	var operands []string
	parsing := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !parsing || arg == "-" || !strings.HasPrefix(arg, "-") {
			operands = append(operands, arg)
			continue
		}
		if arg == "--" {
			parsing = false
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := arg, "", false
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
			needValue := func() (string, bool) {
				if hasValue {
					return value, true
				}
				i++
				if i >= len(args) {
					ownerUsage(prog, "option '%s' requires an argument", name)
					return "", false
				}
				return args[i], true
			}
			switch name {
			case "--recursive":
				options.recursive = true
			case "--changes":
				options.changes = true
			case "--silent", "--quiet":
				options.quiet = true
			case "--verbose":
				options.verbose = true
			case "--no-dereference":
				options.noDeref = true
			case "--dereference":
				options.noDeref = false
			case "--preserve-root":
				options.preserveRoot = true
			case "--no-preserve-root":
				options.preserveRoot = false
			case "--from":
				text, ok := needValue()
				if !ok {
					return options, nil, false
				}
				// --from names the ownership a file must already have; either
				// half may be left out to mean "anything".
				uid, gid, err := parseOwnerCondition(text)
				if err != nil {
					fatalf(prog, "%v", err)
					return options, nil, false
				}
				options.fromUID, options.fromGID = uid, gid
			case "--reference":
				text, ok := needValue()
				if !ok {
					return options, nil, false
				}
				info, err := os.Stat(text)
				if err != nil {
					fatalf(prog, "failed to get attributes of '%s': %s", text, errText(err))
					return options, nil, false
				}
				status, ok := info.Sys().(*syscall.Stat_t)
				if !ok {
					fatalf(prog, "failed to get attributes of '%s'", text)
					return options, nil, false
				}
				options.uid, options.gid = int(status.Uid), int(status.Gid)
				options.showGroup = true
				if prog == "chgrp" {
					options.uid = -1
				}
			default:
				ownerUsage(prog, "unrecognized option '%s'", arg)
				return options, nil, false
			}
			continue
		}
		for _, flag := range arg[1:] {
			switch flag {
			case 'R':
				options.recursive = true
			case 'c':
				options.changes = true
			case 'f':
				options.quiet = true
			case 'v':
				options.verbose = true
			case 'h':
				options.noDeref = true
			case 'H':
				options.deref = derefCommandLine
			case 'L':
				options.deref = derefAlways
			case 'P':
				options.deref = derefNever
			default:
				ownerUsage(prog, "invalid option -- '%c'", flag)
				return options, nil, false
			}
		}
	}
	return options, operands, true
}

// parseOwnerCondition reads --from's "USER:GROUP", where either half may be
// empty to match anything.
func parseOwnerCondition(spec string) (int, int, error) {
	owner, group, hasGroup := strings.Cut(spec, ":")
	uid, gid := -1, -1
	var err error
	if owner != "" {
		if uid, err = resolveUser(owner); err != nil {
			return -1, -1, err
		}
	}
	if hasGroup && group != "" {
		if gid, err = resolveGroup(group); err != nil {
			return -1, -1, err
		}
	}
	return uid, gid, nil
}

func (o *ownerOptions) apply(paths []string) int {
	for _, path := range paths {
		if o.preserveRoot && o.recursive && filepath.Clean(path) == "/" {
			fatalf(o.prog, "it is dangerous to operate recursively on '/'")
			fmt.Fprintf(os.Stderr, "%s: use --no-preserve-root to override this failsafe\n", o.prog)
			o.status = 1
			continue
		}
		o.walk(path, true)
	}
	return o.status
}

// walk visits one operand, applying the change to children before their parent
// so that a directory whose ownership has just changed cannot hide entries that
// were already found. commandLine says whether this path was named on the
// command line, which is what -H keys off.
func (o *ownerOptions) walk(path string, commandLine bool) {
	info, err := os.Lstat(path)
	if err != nil {
		o.report("cannot access '%s': %s", path, errText(err))
		return
	}
	follow := o.follows(info, commandLine)
	if follow {
		if followed, followErr := os.Stat(path); followErr == nil {
			info = followed
		} else {
			o.report("cannot dereference '%s': %s", path, errText(followErr))
			return
		}
	}
	if info.IsDir() && o.recursive {
		// The entries are visited in the order the kernel returns them, which
		// is the order the originals' walk reports them in.
		names, readErr := readDirRaw(path)
		if readErr != nil {
			o.report("cannot read directory '%s': %s", path, errText(readErr))
		}
		for _, name := range names {
			o.walk(filepath.Join(path, name), false)
		}
	}
	o.change(path, info, follow)
}

// follows says whether this path's symbolic link should be resolved: a plain
// invocation follows every link, while a recursive one follows only what -H or
// -L asked for, and -h never follows at all.
func (o *ownerOptions) follows(info os.FileInfo, commandLine bool) bool {
	if info.Mode()&os.ModeSymlink == 0 || o.noDeref {
		return false
	}
	if !o.recursive {
		return true
	}
	return o.deref == derefAlways || (o.deref == derefCommandLine && commandLine)
}

func (o *ownerOptions) report(format string, a ...interface{}) {
	if !o.quiet {
		fatalf(o.prog, format, a...)
	}
	o.status = 1
}

// change applies the new ids to one file and prints whatever -v or -c asked for.
func (o *ownerOptions) change(path string, info os.FileInfo, follow bool) {
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	oldUID, oldGID := int(status.Uid), int(status.Gid)
	// --from restricts the change to files that already carry those ids.
	if (o.fromUID >= 0 && oldUID != o.fromUID) || (o.fromGID >= 0 && oldGID != o.fromGID) {
		o.announce(path, oldUID, oldGID, oldUID, oldGID)
		return
	}
	newUID, newGID := oldUID, oldGID
	if o.uid >= 0 {
		newUID = o.uid
	}
	if o.gid >= 0 {
		newGID = o.gid
	}
	if newUID != oldUID || newGID != oldGID {
		var err error
		if follow {
			err = os.Chown(path, o.uid, o.gid)
		} else {
			err = os.Lchown(path, o.uid, o.gid)
		}
		if err != nil {
			what := "ownership"
			if o.prog == "chgrp" {
				what = "group"
			}
			o.report("changing %s of '%s': %s", what, path, errText(err))
			return
		}
	}
	o.announce(path, oldUID, oldGID, newUID, newGID)
}

// announce prints the -v and -c lines in the originals' wording: chgrp names
// the group alone, and chown names the group beside the owner only when one
// was asked for.
func (o *ownerOptions) announce(path string, oldUID, oldGID, newUID, newGID int) {
	changed := oldUID != newUID || oldGID != newGID
	if !o.verbose && (!o.changes || !changed) {
		return
	}
	// The "from" side names what the file carried; the "to" side names only
	// what was asked for, so "chown :group" shows an empty owner there.
	name := func(uid, gid int, requested bool) string {
		if o.prog == "chgrp" {
			return groupName(uint32(gid)) //nolint:gosec // G115: an id read from the kernel is nonnegative.
		}
		owner := ""
		if !requested || o.uid >= 0 {
			owner = userName(uint32(uid)) //nolint:gosec // G115: same.
		}
		if o.showGroup {
			return owner + ":" + groupName(uint32(gid)) //nolint:gosec // G115: same.
		}
		return owner
	}
	what := "ownership"
	if o.prog == "chgrp" {
		what = "group"
	}
	if changed {
		fmt.Fprintf(os.Stdout, "changed %s of '%s' from %s to %s\n", what, path,
			name(oldUID, oldGID, false), name(newUID, newGID, true))
		return
	}
	fmt.Fprintf(os.Stdout, "%s of '%s' retained as %s\n", what, path, name(oldUID, oldGID, false))
}

func parseOwnerSpec(spec string) (int, int, error) {
	owner, group, hasGroup := strings.Cut(spec, ":")
	if owner == "" && (!hasGroup || group == "") {
		return -1, -1, fmt.Errorf("invalid owner: %q", spec)
	}
	uid, gid := -1, -1
	var err error
	if owner != "" {
		uid, err = resolveUser(owner)
		if err != nil {
			return -1, -1, err
		}
	}
	if hasGroup {
		if group != "" {
			gid, err = resolveGroup(group)
		} else if owner != "" {
			u, lookupErr := lookupUser(owner)
			if lookupErr != nil {
				return -1, -1, lookupErr
			}
			gid, err = strconv.Atoi(u.Gid)
		}
	}
	return uid, gid, err
}

func resolveUser(value string) (int, error) {
	if id, err := parseNonnegativeID(value); err == nil {
		return id, nil
	}
	u, err := user.Lookup(value)
	if err != nil {
		return -1, fmt.Errorf("invalid user %q", value)
	}
	return strconv.Atoi(u.Uid)
}

func lookupUser(value string) (*user.User, error) {
	if _, err := parseNonnegativeID(value); err == nil {
		u, lookupErr := user.LookupId(value)
		if lookupErr != nil {
			return nil, fmt.Errorf("invalid user %q", value)
		}
		return u, nil
	}
	u, err := user.Lookup(value)
	if err != nil {
		return nil, fmt.Errorf("invalid user %q", value)
	}
	return u, nil
}

func resolveGroup(value string) (int, error) {
	if id, err := parseNonnegativeID(value); err == nil {
		return id, nil
	}
	g, err := user.LookupGroup(value)
	if err != nil {
		return -1, fmt.Errorf("invalid group %q", value)
	}
	return strconv.Atoi(g.Gid)
}

func parseNonnegativeID(value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil || id < 0 {
		return -1, fmt.Errorf("invalid id")
	}
	return id, nil
}
