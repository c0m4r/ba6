package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// cmdRmdir implements rmdir(1): remove empty directories, with -p to remove
// empty parent directories too and --ignore-fail-on-non-empty to suppress
// non-empty errors.
func cmdRmdir(args []string) int {
	parents := false
	ignoreNonEmpty := false
	var dirs []string

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			goto rest
		case a == "-p" || a == "--parents":
			parents = true
		case a == "--ignore-fail-on-non-empty":
			ignoreNonEmpty = true
		case len(a) > 1 && a[0] == '-':
			fatalf("rmdir", "invalid option %q", a)
			return 1
		default:
			dirs = append(dirs, a)
		}
	}
rest:
	dirs = append(dirs, args[i:]...)
	if len(dirs) == 0 {
		fatalf("rmdir", "missing operand")
		return 1
	}

	status := 0
	for _, d := range dirs {
		if !removeOneDir(d, ignoreNonEmpty) {
			status = 1
			continue
		}
		if parents {
			for p := filepath.Dir(d); p != "." && p != "/" && p != ""; p = filepath.Dir(p) {
				if !removeOneDir(p, ignoreNonEmpty) {
					break
				}
			}
		}
	}
	return status
}

func removeOneDir(d string, ignoreNonEmpty bool) bool {
	err := os.Remove(d)
	if err == nil {
		return true
	}
	if ignoreNonEmpty && isNotEmpty(err) {
		return true
	}
	fatalf("rmdir", "failed to remove '%s': %v", d, err)
	return false
}

// isNotEmpty reports whether err indicates a directory-not-empty condition.
func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}
