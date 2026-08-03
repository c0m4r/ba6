// ba6 is a multicall binary: a single static executable that bundles several
// basic Unix utilities. The applet to run is selected either by the basename
// of argv[0] (when invoked through a symlink, e.g. /bin/cat -> ba6) or by the
// first command-line argument (e.g. "ba6 cat file.txt").
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// applet is the signature every command implements. args are the arguments
// following the applet name (i.e. os.Args[2:] or os.Args[1:] depending on how
// the binary was invoked). It returns a process exit code.
type applet func(args []string) int

var applets = map[string]applet{
	"[":        cmdBracket,
	"basename": cmdBasename,
	"cat":      cmdCat,
	"chgrp":    cmdChgrp,
	"chmod":    cmdChmod,
	"chown":    cmdChown,
	"cp":       cmdCp,
	"cut":      cmdCut,
	"date":     cmdDate,
	"dirname":  cmdDirname,
	"echo":     cmdEcho,
	"false":    cmdFalse,
	"grep":     cmdGrep,
	"head":     cmdHead,
	"help":     cmdHelp,
	"ip":       cmdIP,
	"ln":       cmdLn,
	"ls":       cmdLs,
	"mkdir":    cmdMkdir,
	"mv":       cmdMv,
	"pwd":      cmdPwd,
	"readlink": cmdReadlink,
	"realpath": cmdRealpath,
	"rm":       cmdRm,
	"rmdir":    cmdRmdir,
	"sleep":    cmdSleep,
	"sort":     cmdSort,
	"stat":     cmdStat,
	"tail":     cmdTail,
	"tee":      cmdTee,
	"test":     cmdTest,
	"touch":    cmdTouch,
	"tr":       cmdTr,
	"true":     cmdTrue,
	"uniq":     cmdUniq,
	"wc":       cmdWc,
}

func main() {
	// Lock down the process at the kernel level before running any applet.
	applyHardening()

	prog := filepath.Base(os.Args[0])

	// Invoked via symlink (e.g. "cat"): dispatch directly.
	if fn, ok := applets[prog]; ok {
		os.Exit(runApplet(prog, fn, os.Args[1:]))
	}

	// Invoked as "ba6 <applet> ...".
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	name := os.Args[1]
	if name == "--help" || name == "-h" {
		if err := writeGeneralHelp(os.Stdout); err != nil {
			fatalf("ba6", "write error: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if name == "--list" {
		if err := listApplets(os.Stdout); err != nil {
			fatalf("ba6", "write error: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	fn, ok := applets[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "ba6: unknown applet %q\n", name)
		usage()
		os.Exit(127)
	}
	os.Exit(runApplet(name, fn, os.Args[2:]))
}

func runApplet(name string, fn applet, args []string) int {
	if helpRequested(args) {
		if err := writeAppletHelp(os.Stdout, name); err != nil {
			fatalf(name, "write error: %v", err)
			return 1
		}
		return 0
	}
	return fn(args)
}

func usage() {
	_ = writeGeneralHelp(os.Stderr)
}

func listApplets(w io.Writer) error {
	names := make([]string, 0, len(applets))
	for n := range applets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := fmt.Fprintf(w, "  %s\n", n); err != nil {
			return err
		}
	}
	return nil
}

// fatalf prints a "<applet>: ..." style error to stderr. Helper for applets.
func fatalf(prog, format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, prog+": "+format+"\n", a...)
}
