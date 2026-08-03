package main

import (
	"os"
	"time"
)

// cmdTouch implements touch(1): create files if missing and update their
// access/modification times to now. -c/--no-create skips creation; -a/-m
// limit which timestamp is changed.
func cmdTouch(args []string) int {
	noCreate := false
	atimeOnly := false
	mtimeOnly := false
	var files []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "--no-create":
			noCreate = true
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				switch c {
				case 'c':
					noCreate = true
				case 'a':
					atimeOnly = true
				case 'm':
					mtimeOnly = true
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
	if len(files) == 0 {
		fatalf("touch", "missing file operand")
		return 1
	}

	now := time.Now()
	status := 0
	for _, f := range files {
		existing, statErr := os.Stat(f)
		if os.IsNotExist(statErr) {
			if noCreate {
				continue
			}
			// 0666 matches POSIX touch; the process umask narrows it as usual.
			fh, err := os.OpenFile(f, os.O_CREATE|os.O_WRONLY, 0o666) //nolint:gosec // G302: touch must honor umask, not force 0600
			if err != nil {
				fatalf("touch", "cannot touch '%s': %v", f, err)
				status = 1
				continue
			}
			if err := fh.Close(); err != nil {
				fatalf("touch", "cannot close '%s': %v", f, err)
				status = 1
				continue
			}
			existing = nil
		}

		atime, mtime := now, now
		// When only one of -a/-m is given, preserve the other timestamp.
		if existing != nil && (atimeOnly != mtimeOnly) {
			if atimeOnly && !mtimeOnly {
				mtime = existing.ModTime()
			}
			if mtimeOnly && !atimeOnly {
				atime = atimeOf(existing)
			}
		}

		if err := os.Chtimes(f, atime, mtime); err != nil {
			fatalf("touch", "setting times of '%s': %v", f, err)
			status = 1
		}
	}
	return status
}
