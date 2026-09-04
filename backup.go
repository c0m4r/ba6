// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// backupMethod is the naming scheme --backup asks for. The coreutils tools
// share one implementation of this, so cp, ln and mv agree on what a backup
// is called and when a numbered one is made.
type backupMethod int

const (
	backupNone     backupMethod = iota // no backups at all
	backupSimple                       // one backup, named with the suffix
	backupNumbered                     // name.~1~, name.~2~, ...
	backupExisting                     // numbered if numbered ones exist, else simple
)

// backupControls maps every name --backup accepts onto its method. GNU also
// accepts any unambiguous abbreviation of these.
var backupControls = []struct {
	name   string
	method backupMethod
}{
	{"none", backupNone}, {"off", backupNone},
	{"simple", backupSimple}, {"never", backupSimple},
	{"existing", backupExisting}, {"nil", backupExisting},
	{"numbered", backupNumbered}, {"t", backupNumbered},
}

// parseBackupControl resolves a --backup or VERSION_CONTROL value. An
// unambiguous prefix is accepted, as in the originals: "nu" is numbered, while
// "n" matches none, never and nil at once and is refused.
func parseBackupControl(value string) (backupMethod, bool) {
	method, distinct := backupNone, 0
	if value == "" {
		return method, false
	}
	for _, control := range backupControls {
		if control.name == value {
			return control.method, true
		}
		// Two spellings of one method are not an ambiguity; two methods are.
		if strings.HasPrefix(control.name, value) && (distinct == 0 || control.method != method) {
			method, distinct = control.method, distinct+1
		}
	}
	return method, distinct == 1
}

// backupControlAmbiguous separates the two ways a control name can be refused,
// which the originals word differently.
func backupControlAmbiguous(value string) bool {
	method, distinct := backupNone, 0
	for _, control := range backupControls {
		if control.name == value {
			return false
		}
		if strings.HasPrefix(control.name, value) && (distinct == 0 || control.method != method) {
			method, distinct = control.method, distinct+1
		}
	}
	return distinct > 1
}

// backupUsageError reports an unusable control name the way the originals do,
// listing the valid arguments before the "Try ..." line.
func backupUsageError(prog, kind, value string) int {
	problem := "invalid"
	if backupControlAmbiguous(value) {
		problem = "ambiguous"
	}
	fatalf(prog, "%s argument %s for %s", problem, quoteLocaleName(value), quoteLocaleName(kind))
	fmt.Fprint(os.Stderr, "Valid arguments are:\n"+
		"  - 'none', 'off'\n"+
		"  - 'simple', 'never'\n"+
		"  - 'existing', 'nil'\n"+
		"  - 'numbered', 't'\n")
	fmt.Fprintf(os.Stderr, "Try '%s --help' for more information.\n", prog)
	return 1
}

// defaultBackupMethod is what a bare -b means: whatever VERSION_CONTROL says,
// and "existing" when it says nothing.
func defaultBackupMethod(prog string) (backupMethod, bool) {
	value, set := os.LookupEnv("VERSION_CONTROL")
	if !set || value == "" {
		return backupExisting, true
	}
	method, ok := parseBackupControl(value)
	if !ok {
		backupUsageError(prog, "$VERSION_CONTROL", value)
		return backupNone, false
	}
	return method, true
}

// defaultBackupSuffix is the suffix a simple backup gets: SIMPLE_BACKUP_SUFFIX
// when it is set, and a tilde otherwise.
func defaultBackupSuffix() string {
	if value, set := os.LookupEnv("SIMPLE_BACKUP_SUFFIX"); set && value != "" {
		return value
	}
	return "~"
}

// backupName is what the existing file at path would be renamed to. It returns
// an empty name when no backup is wanted, or when there is nothing to back up.
func backupName(path string, method backupMethod, suffix string) string {
	if method == backupNone {
		return ""
	}
	if _, err := os.Lstat(path); err != nil {
		return ""
	}
	if method == backupExisting {
		method = backupSimple
		if backupHighestNumber(path) > 0 {
			method = backupNumbered
		}
	}
	if method == backupNumbered {
		return path + ".~" + strconv.Itoa(backupHighestNumber(path)+1) + "~"
	}
	return path + suffix
}

// backupHighestNumber is the largest N for which path.~N~ already exists.
func backupHighestNumber(path string) int {
	directory, base := filepath.Dir(path), filepath.Base(path)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0
	}
	highest := 0
	for _, entry := range entries {
		digits, found := strings.CutPrefix(entry.Name(), base+".~")
		if !found {
			continue
		}
		digits, found = strings.CutSuffix(digits, "~")
		if !found {
			continue
		}
		if n, convErr := strconv.Atoi(digits); convErr == nil && n > highest {
			highest = n
		}
	}
	return highest
}

// makeBackup renames an existing destination out of the way and reports the
// name it now has, or an empty name when no backup was called for.
func makeBackup(path string, method backupMethod, suffix string) (string, error) {
	name := backupName(path, method, suffix)
	if name == "" {
		return "", nil
	}
	if err := os.Rename(path, name); err != nil {
		return "", err
	}
	return name, nil
}
