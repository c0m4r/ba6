// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

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
	"adduser":     cmdAdduser,
	"awk":         cmdAwk,
	"base64":      cmdBase64,
	"basename":    cmdBasename,
	"blkid":       cmdBlkid,
	"blockdev":    cmdBlockdev,
	"bunzip2":     cmdBunzip2,
	"bzip2":       cmdBzip2,
	"cat":         cmdCat,
	"cfdisk":      cmdCfdisk,
	"chgrp":       cmdChgrp,
	"chmod":       cmdChmod,
	"chown":       cmdChown,
	"chroot":      cmdChroot,
	"cksum":       cmdCksum,
	"comm":        cmdComm,
	"cp":          cmdCp,
	"cpio":        cmdCpio,
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
	"expand":      cmdExpand,
	"expr":        cmdExpr,
	"false":       cmdFalse,
	"file":        cmdFile,
	"find":        cmdFind,
	"fdisk":       cmdFdisk,
	"fold":        cmdFold,
	"fsck":        cmdFsck,
	"fsck.ext2":   cmdFsckExt2,
	"fsck.ext3":   cmdFsckExt3,
	"fsck.ext4":   cmdFsckExt4,
	"free":        cmdFree,
	"grep":        cmdGrep,
	"groupadd":    cmdGroupadd,
	"gunzip":      cmdGunzip,
	"gzip":        cmdGzip,
	"halt":        cmdHalt,
	"head":        cmdHead,
	"hexdump":     cmdHexdump,
	"help":        cmdHelp,
	"host":        cmdHost,
	"hostname":    cmdHostname,
	"hwclock":     cmdHwclock,
	"id":          cmdId,
	"iftop":       cmdIftop,
	"init":        cmdInit,
	"insmod":      cmdInsmod,
	"ip":          cmdIP,
	"join":        cmdJoin,
	"iptables":    cmdIptables,
	"kill":        cmdKill,
	"less":        cmdLess,
	"ln":          cmdLn,
	"login":       cmdLogin,
	"lsof":        cmdLsof,
	"ls":          cmdLs,
	"lsblk":       cmdLsblk,
	"lsmod":       cmdLsmod,
	"lspci":       cmdLspci,
	"lsusb":       cmdLsusb,
	"losetup":     cmdLosetup,
	"man":         cmdHelp,
	"mkdir":       cmdMkdir,
	"mkfs":        cmdMkfs,
	"mkfs.btrfs":  cmdMkfsBtrfs,
	"mkfs.ext2":   cmdMkfsExt2,
	"mkfs.ext3":   cmdMkfsExt3,
	"mkfs.ext4":   cmdMkfsExt4,
	"mkfs.xfs":    cmdMkfsXfs,
	"mkswap":      cmdMkswap,
	"mktemp":      cmdMktemp,
	"md5sum":      cmdMd5sum,
	"mknod":       cmdMknod,
	"modprobe":    cmdModprobe,
	"mtr":         cmdMtr,
	"mount":       cmdMount,
	"mv":          cmdMv,
	"nano":        cmdNano,
	"nc":          cmdNc,
	"ncdu":        cmdNcdu,
	"netstat":     cmdNetstat,
	"nice":        cmdNice,
	"nl":          cmdNl,
	"nohup":       cmdNohup,
	"nslookup":    cmdNslookup,
	"od":          cmdOd,
	"passwd":      cmdPasswd,
	"paste":       cmdPaste,
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
	"renice":      cmdRenice,
	"rm":          cmdRm,
	"rmdir":       cmdRmdir,
	"rmmod":       cmdRmmod,
	"sed":         cmdSed,
	"seq":         cmdSeq,
	"setsid":      cmdSetsid,
	"sfdisk":      cmdSfdisk,
	"sha256sum":   cmdSha256sum,
	"sha1sum":     cmdSha1sum,
	"sha512sum":   cmdSha512sum,
	"sh":          cmdSh,
	"sleep":       cmdSleep,
	"sort":        cmdSort,
	"split":       cmdSplit,
	"ss":          cmdSs,
	"stat":        cmdStat,
	"strings":     cmdStrings,
	"sync":        cmdSync,
	"sysctl":      cmdSysctl,
	"switch_root": cmdSwitchRoot,
	"swapoff":     cmdSwapoff,
	"swapon":      cmdSwapon,
	"tac":         cmdTac,
	"tail":        cmdTail,
	"tar":         cmdTar,
	"tee":         cmdTee,
	"test":        cmdTest,
	"timeout":     cmdTimeout,
	"touch":       cmdTouch,
	"top":         cmdTop,
	"tr":          cmdTr,
	"traceroute":  cmdTraceroute,
	"tree":        cmdTree,
	"true":        cmdTrue,
	"tty":         cmdTty,
	"uname":       cmdUname,
	"udhcpc":      cmdUdhcpc,
	"uniq":        cmdUniq,
	"umount":      cmdUmount,
	"unexpand":    cmdUnexpand,
	"unxz":        cmdUnxz,
	"unzip":       cmdUnzip,
	"unzstd":      cmdUnzstd,
	"uptime":      cmdUptime,
	"useradd":     cmdUseradd,
	"watch":       cmdWatch,
	"wc":          cmdWc,
	"wget":        cmdWget,
	"which":       cmdWhich,
	"whoami":      cmdWhoami,
	"xargs":       cmdXargs,
	"xz":          cmdXz,
	"zip":         cmdZip,
	"zstd":        cmdZstd,
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
	case "chroot", "curl", "dig", "env", "host", "init", "insmod", "login", "modprobe", "mount", "mtr", "nc",
		"nice", "nohup", "nslookup", "ping", "rmmod", "setsid", "sh", "switch_root", "timeout", "traceroute", "udhcpc", "umount", "watch",
		"wget", "xargs":
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
	if !appletsWithOwnVersion[name] && versionRequested(args) {
		fmt.Fprintln(os.Stdout, name+" from ba6")
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
