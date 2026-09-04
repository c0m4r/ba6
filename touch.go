// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// utimeOmit tells utimensat(2) to leave one of the two timestamps alone; using
// it rather than reading the current value back avoids a stat/set race and is
// what the original does for -a and -m.
const (
	utimeOmit        = (1 << 30) - 2
	atSymlinkNoFollw = 0x100 // AT_SYMLINK_NOFOLLOW
)

// atFdCwd is AT_FDCWD. It is a variable rather than a constant because
// converting the negative constant to uintptr is not a legal constant
// expression in Go.
var atFdCwd = -0x64

// touchOptions is one parsed touch(1) command line.
type touchOptions struct {
	noCreate  bool
	atimeOnly bool
	mtimeOnly bool
	noDeref   bool
	haveTime  bool
	atime     time.Time
	mtime     time.Time
}

// cmdTouch implements touch(1): create files if missing and update their
// access/modification times. -c/--no-create skips creation, -a/-m limit which
// timestamp is changed, -d/-t/-r choose a time other than now, and -h acts on a
// symlink itself.
func cmdTouch(args []string) int {
	opts := touchOptions{}
	var files []string
	var dateSpec, stamp, reference string
	haveDate, haveStamp := false, false

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		value := func(name string) (string, bool) {
			i++
			if i >= len(args) {
				fatalf("touch", "option requires an argument -- '%s'", name)
				return "", false
			}
			return args[i], true
		}
		var ok bool
		switch {
		case a == "--":
			i++
			goto rest
		case a == "--no-create":
			opts.noCreate = true
		case a == "--no-dereference":
			opts.noDeref = true
		case a == "--date" || a == "-d":
			if dateSpec, ok = value("d"); !ok {
				return 1
			}
			haveDate = true
		case strings.HasPrefix(a, "--date="):
			dateSpec, haveDate = strings.TrimPrefix(a, "--date="), true
		case strings.HasPrefix(a, "-d") && len(a) > 2:
			dateSpec, haveDate = a[2:], true
		case a == "-t":
			if stamp, ok = value("t"); !ok {
				return 1
			}
			haveStamp = true
		case strings.HasPrefix(a, "-t") && len(a) > 2:
			stamp, haveStamp = a[2:], true
		case a == "--reference" || a == "-r":
			if reference, ok = value("r"); !ok {
				return 1
			}
		case strings.HasPrefix(a, "--reference="):
			reference = strings.TrimPrefix(a, "--reference=")
		case strings.HasPrefix(a, "-r") && len(a) > 2:
			reference = a[2:]
		case a == "--time" || strings.HasPrefix(a, "--time="):
			word := strings.TrimPrefix(a, "--time=")
			if a == "--time" {
				if word, ok = value("-time"); !ok {
					return 1
				}
			}
			switch word {
			case "access", "atime", "use":
				opts.atimeOnly = true
			case "modify", "mtime":
				opts.mtimeOnly = true
			default:
				fatalf("touch", "invalid argument '%s' for '--time'", word)
				return 1
			}
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				switch c {
				case 'c':
					opts.noCreate = true
				case 'a':
					opts.atimeOnly = true
				case 'm':
					opts.mtimeOnly = true
				case 'h':
					opts.noDeref = true
				case 'f':
					// Accepted and ignored, as in the original (BSD compatibility).
				default:
					fatalf("touch", "invalid option -- '%c'", c)
					return 1
				}
			}
		default:
			files = append(files, a)
		}
	}
rest:
	files = append(files, args[i:]...)

	now := time.Now()
	switch {
	case reference != "":
		info, err := os.Lstat(reference)
		if err == nil && info.Mode()&os.ModeSymlink != 0 && !opts.noDeref {
			info, err = os.Stat(reference)
		}
		if err != nil {
			fatalf("touch", "failed to get attributes of %s: %s", quoteForceName(reference), errText(err))
			return 1
		}
		opts.atime, opts.mtime, opts.haveTime = atimeOf(info), info.ModTime(), true
	case haveStamp:
		when, err := parseTouchStamp(stamp, now)
		if err != nil {
			fatalf("touch", "%v", err)
			return 1
		}
		opts.atime, opts.mtime, opts.haveTime = when, when, true
	case haveDate:
		when, err := parseTimeSpec(dateSpec, now)
		if err != nil {
			// touch words this differently from date for the same parser.
			fatalf("touch", "invalid date format '%s'", dateSpec)
			return 1
		}
		opts.atime, opts.mtime, opts.haveTime = when, when, true
	}
	if len(files) == 0 {
		fatalf("touch", "missing file operand")
		return 1
	}

	status := 0
	for _, f := range files {
		if touchOne(f, &opts, now) != 0 {
			status = 1
		}
	}
	return status
}

// touchOne creates f when needed and applies the requested timestamps.
func touchOne(f string, opts *touchOptions, now time.Time) int {
	if _, err := os.Lstat(f); err != nil && os.IsNotExist(err) {
		if opts.noCreate {
			return 0
		}
		// 0666 matches POSIX touch; the process umask narrows it as usual.
		fh, err := os.OpenFile(f, os.O_CREATE|os.O_WRONLY, 0o666) //nolint:gosec // G302: touch must honor umask, not force 0600
		if err != nil {
			fatalf("touch", "cannot touch %s: %s", quoteForceName(f), errText(err))
			return 1
		}
		if err := fh.Close(); err != nil {
			fatalf("touch", "cannot close %s: %s", quoteForceName(f), errText(err))
			return 1
		}
	}

	atime, mtime := now, now
	if opts.haveTime {
		atime, mtime = opts.atime, opts.mtime
	}
	times := [2]syscall.Timespec{timespecOf(atime), timespecOf(mtime)}
	// When only one of -a/-m is given, the other timestamp is left untouched.
	if opts.atimeOnly != opts.mtimeOnly {
		if opts.atimeOnly {
			times[1] = syscall.Timespec{Sec: 0, Nsec: utimeOmit}
		} else {
			times[0] = syscall.Timespec{Sec: 0, Nsec: utimeOmit}
		}
	}
	if err := utimensAt(f, &times, opts.noDeref); err != nil {
		fatalf("touch", "setting times of %s: %s", quoteForceName(f), errText(err))
		return 1
	}
	return 0
}

func timespecOf(value time.Time) syscall.Timespec {
	return syscall.NsecToTimespec(value.UnixNano())
}

// utimensAt sets a path's timestamps. Go's os.Chtimes always follows symlinks
// and cannot express UTIME_OMIT, so touch calls utimensat(2) directly.
func utimensAt(path string, times *[2]syscall.Timespec, noDeref bool) error {
	name, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	flags := uintptr(0)
	if noDeref {
		flags = atSymlinkNoFollw
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_UTIMENSAT, uintptr(atFdCwd),
		uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(times)), //nolint:gosec // G103: NUL-terminated path and a fixed two-element Timespec array, both read-only to the kernel.
		flags, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
