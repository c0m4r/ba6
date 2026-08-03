package main

import (
	"fmt"
	"os"
)

// cmdPwd implements pwd(1). With -L (default) it honors $PWD if it names the
// current directory; with -P it resolves symlinks to the physical path.
func cmdPwd(args []string) int {
	physical := false
	noMoreOptions := false
	for _, a := range args {
		if !noMoreOptions && a == "--" {
			noMoreOptions = true
			continue
		}
		if noMoreOptions {
			fatalf("pwd", "extra operand %q", a)
			return 1
		}
		switch a {
		case "-P", "--physical":
			physical = true
		case "-L", "--logical":
			physical = false
		default:
			if len(a) > 0 && a[0] == '-' {
				fatalf("pwd", "invalid option %q", a)
				return 2
			}
			fatalf("pwd", "extra operand %q", a)
			return 1
		}
	}

	if !physical {
		if pwd := os.Getenv("PWD"); pwd != "" {
			if isSameDir(pwd) {
				if _, err := fmt.Fprintln(os.Stdout, pwd); err != nil {
					fatalf("pwd", "write error: %v", err)
					return 1
				}
				return 0
			}
		}
	}

	dir, err := os.Getwd()
	if err != nil {
		fatalf("pwd", "%v", err)
		return 1
	}
	if _, err := fmt.Fprintln(os.Stdout, dir); err != nil {
		fatalf("pwd", "write error: %v", err)
		return 1
	}
	return 0
}

// isSameDir reports whether path refers to the current working directory.
func isSameDir(path string) bool {
	pi, err := os.Stat(path)
	if err != nil {
		return false
	}
	ci, err := os.Stat(".")
	if err != nil {
		return false
	}
	return os.SameFile(pi, ci)
}
