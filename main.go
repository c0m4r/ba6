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
	"strings"
)

// applet is the signature every command implements. args are the arguments
// following the applet name (i.e. os.Args[2:] or os.Args[1:] depending on how
// the binary was invoked). It returns a process exit code.
type applet func(args []string) int

var applets = map[string]applet{
	"[":           cmdBracket,
	"awk":         cmdAwk,
	"base64":      cmdBase64,
	"basename":    cmdBasename,
	"blkid":       cmdBlkid,
	"blockdev":    cmdBlockdev,
	"cat":         cmdCat,
	"chgrp":       cmdChgrp,
	"chmod":       cmdChmod,
	"chown":       cmdChown,
	"chroot":      cmdChroot,
	"cp":          cmdCp,
	"cmp":         cmdCmp,
	"completion":  cmdCompletion,
	"curl":        cmdCurl,
	"cut":         cmdCut,
	"date":        cmdDate,
	"dd":          cmdDd,
	"df":          cmdDf,
	"diff":        cmdDiff,
	"dig":         cmdDig,
	"dmesg":       cmdDmesg,
	"dirname":     cmdDirname,
	"du":          cmdDu,
	"echo":        cmdEcho,
	"env":         cmdEnv,
	"expr":        cmdExpr,
	"false":       cmdFalse,
	"file":        cmdFile,
	"find":        cmdFind,
	"fdisk":       cmdFdisk,
	"fsck":        cmdFsck,
	"fsck.ext2":   cmdFsckExt,
	"fsck.ext3":   cmdFsckExt,
	"fsck.ext4":   cmdFsckExt,
	"free":        cmdFree,
	"grep":        cmdGrep,
	"gunzip":      cmdGunzip,
	"gzip":        cmdGzip,
	"halt":        cmdHalt,
	"head":        cmdHead,
	"hexdump":     cmdHexdump,
	"help":        cmdHelp,
	"hostname":    cmdHostname,
	"id":          cmdId,
	"iftop":       cmdIftop,
	"init":        cmdInit,
	"insmod":      cmdInsmod,
	"ip":          cmdIP,
	"iptables":    cmdIptables,
	"kill":        cmdKill,
	"ln":          cmdLn,
	"login":       cmdLogin,
	"lsof":        cmdLsof,
	"ls":          cmdLs,
	"lsblk":       cmdLsblk,
	"lsmod":       cmdLsmod,
	"losetup":     cmdLosetup,
	"man":         cmdHelp,
	"mkdir":       cmdMkdir,
	"mkfs":        cmdMkfs,
	"mkfs.ext2":   cmdMkfsExt2,
	"mkswap":      cmdMkswap,
	"mktemp":      cmdMktemp,
	"mknod":       cmdMknod,
	"modprobe":    cmdModprobe,
	"mtr":         cmdMtr,
	"mount":       cmdMount,
	"mv":          cmdMv,
	"nano":        cmdNano,
	"nc":          cmdNc,
	"nslookup":    cmdNslookup,
	"od":          cmdOd,
	"passwd":      cmdPasswd,
	"pgrep":       cmdPgrep,
	"pidof":       cmdPidof,
	"ping":        cmdPing,
	"pkill":       cmdPkill,
	"poweroff":    cmdPoweroff,
	"printenv":    cmdPrintenv,
	"printf":      cmdPrintf,
	"pwd":         cmdPwd,
	"ps":          cmdPs,
	"readlink":    cmdReadlink,
	"realpath":    cmdRealpath,
	"reboot":      cmdReboot,
	"rm":          cmdRm,
	"rmdir":       cmdRmdir,
	"rmmod":       cmdRmmod,
	"sed":         cmdSed,
	"seq":         cmdSeq,
	"sfdisk":      cmdSfdisk,
	"sha256sum":   cmdSha256sum,
	"sh":          cmdSh,
	"sleep":       cmdSleep,
	"sort":        cmdSort,
	"ss":          cmdSs,
	"stat":        cmdStat,
	"strings":     cmdStrings,
	"sync":        cmdSync,
	"switch_root": cmdSwitchRoot,
	"swapoff":     cmdSwapoff,
	"swapon":      cmdSwapon,
	"tail":        cmdTail,
	"tar":         cmdTar,
	"tee":         cmdTee,
	"test":        cmdTest,
	"timeout":     cmdTimeout,
	"touch":       cmdTouch,
	"top":         cmdTop,
	"tr":          cmdTr,
	"traceroute":  cmdTraceroute,
	"true":        cmdTrue,
	"uname":       cmdUname,
	"udhcpc":      cmdUdhcpc,
	"uniq":        cmdUniq,
	"umount":      cmdUmount,
	"uptime":      cmdUptime,
	"wc":          cmdWc,
	"wget":        cmdWget,
	"which":       cmdWhich,
	"whoami":      cmdWhoami,
	"xargs":       cmdXargs,
}

func main() {
	prog := filepath.Base(os.Args[0])
	args, seccompEnabled, err := parseHardeningOptions(os.Args[1:])
	if err != nil {
		fatalf("ba6", "%v", err)
		os.Exit(2)
	}

	// Commands that must create ordinary sockets, execute child processes, or
	// mount filesystems cannot run under the default seccomp denylist. They
	// automatically retain the tier-1 protections while skipping that filter.
	target := prog
	if _, direct := applets[target]; !direct && len(args) > 0 {
		target = args[0]
	}
	applyHardeningProfile(hardeningForApplet(target, os.Getpid(), seccompEnabled))

	// Invoked via symlink (e.g. "cat"): dispatch directly.
	if fn, ok := applets[prog]; ok {
		os.Exit(runApplet(prog, fn, args))
	}

	// Invoked as "ba6 <applet> ...".
	if len(args) < 1 {
		usage()
		os.Exit(1)
	}

	name := args[0]
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
	os.Exit(runApplet(name, fn, args[1:]))
}

func appletNeedsUnrestrictedSyscalls(name string) bool {
	switch name {
	case "chroot", "curl", "dig", "env", "init", "insmod", "login", "modprobe", "mount", "mtr", "nc", "nslookup", "ping",
		"rmmod", "sh", "switch_root", "timeout", "traceroute", "udhcpc", "umount", "wget", "xargs":
		return true
	}
	return false
}

// parseHardeningOptions consumes global hardening flags before applet
// dispatch. Keeping them at the front avoids stealing similarly named applet
// operands and also supports direct symlink invocation.
func parseHardeningOptions(args []string) ([]string, bool, error) {
	seccompEnabled := true
	for len(args) > 0 {
		switch args[0] {
		case "--no-seccomp", "--seccomp=off", "--seccomp=disabled":
			seccompEnabled = false
			args = args[1:]
		case "--seccomp", "--seccomp=on", "--seccomp=enabled":
			seccompEnabled = true
			args = args[1:]
		default:
			if strings.HasPrefix(args[0], "--seccomp=") {
				return nil, false, fmt.Errorf("invalid seccomp mode %q (expected on or off)", strings.TrimPrefix(args[0], "--seccomp="))
			}
			return args, seccompEnabled, nil
		}
	}
	return args, seccompEnabled, nil
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
