// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func cmdStat(args []string) int {
	var format string
	formatSet := false
	dereference := false
	var files []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			files = append(files, args[i+1:]...)
			i = len(args)
		case arg == "-L" || arg == "--dereference":
			dereference = true
		case arg == "-c" || arg == "--format":
			i++
			if i >= len(args) {
				fatalf("stat", "option requires an argument -- 'c'")
				return 1
			}
			format = args[i]
			formatSet = true
		case strings.HasPrefix(arg, "--format="):
			format = strings.TrimPrefix(arg, "--format=")
			formatSet = true
		case len(arg) > 1 && arg[0] == '-':
			fatalf("stat", "invalid option %q", arg)
			return 1
		default:
			files = append(files, arg)
		}
	}
	if len(files) == 0 {
		fatalf("stat", "missing operand")
		return 1
	}
	status := 0
	for _, file := range files {
		var info os.FileInfo
		var err error
		if dereference {
			info, err = os.Stat(file)
		} else {
			info, err = os.Lstat(file)
		}
		if err != nil {
			fatalf("stat", "cannot stat '%s': %s", file, errText(err))
			status = 1
			continue
		}
		if err := writeStat(file, info, format, formatSet); err != nil {
			fatalf("stat", "write error: %s", errText(err))
			return 1
		}
	}
	return status
}

func writeStat(name string, info os.FileInfo, format string, formatSet bool) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("unsupported file metadata")
	}
	if formatSet {
		_, err := fmt.Fprintln(os.Stdout, expandStatFormat(format, name, info, st))
		return err
	}
	displayName := fmt.Sprintf("%q", name)
	if info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(name); err == nil {
			displayName += " -> " + fmt.Sprintf("%q", target)
		}
	}
	_, err := fmt.Fprintf(os.Stdout,
		"  File: %s\n  Size: %d\tBlocks: %d\tIO Block: %d\t%s\nDevice: %d\tInode: %d\tLinks: %d\nAccess: (%04o/%s)  Uid: (%d/%s)  Gid: (%d/%s)\nAccess: %s\nModify: %s\nChange: %s\n",
		displayName, info.Size(), st.Blocks, st.Blksize, fileTypeName(info.Mode()),
		st.Dev, st.Ino, st.Nlink, st.Mode&0o7777, info.Mode().String(),
		st.Uid, userName(st.Uid), st.Gid, groupName(st.Gid),
		formatStatTime(st.Atim), formatStatTime(st.Mtim), formatStatTime(st.Ctim))
	return err
}

func expandStatFormat(format, name string, info os.FileInfo, st *syscall.Stat_t) string {
	var out strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			out.WriteByte(format[i])
			continue
		}
		i++
		var value string
		switch format[i] {
		case '%':
			value = "%"
		case 'n':
			value = name
		case 'N':
			value = fmt.Sprintf("%q", name)
			if info.Mode()&os.ModeSymlink != 0 {
				if target, err := os.Readlink(name); err == nil {
					value += " -> " + fmt.Sprintf("%q", target)
				}
			}
		case 's':
			value = strconv.FormatInt(info.Size(), 10)
		case 'b':
			value = strconv.FormatInt(st.Blocks, 10)
		case 'B':
			value = "512"
		case 'o':
			value = strconv.FormatInt(st.Blksize, 10)
		case 'a':
			value = fmt.Sprintf("%o", st.Mode&0o7777)
		case 'A':
			value = info.Mode().String()
		case 'f':
			value = fmt.Sprintf("%x", st.Mode)
		case 'F':
			value = fileTypeName(info.Mode())
		case 'u':
			value = strconv.FormatUint(uint64(st.Uid), 10)
		case 'U':
			value = userName(st.Uid)
		case 'g':
			value = strconv.FormatUint(uint64(st.Gid), 10)
		case 'G':
			value = groupName(st.Gid)
		case 'i':
			value = strconv.FormatUint(st.Ino, 10)
		case 'h':
			value = strconv.FormatUint(st.Nlink, 10)
		case 'x':
			value = formatStatTime(st.Atim)
		case 'y':
			value = formatStatTime(st.Mtim)
		case 'z':
			value = formatStatTime(st.Ctim)
		case 'X':
			value = strconv.FormatInt(st.Atim.Sec, 10)
		case 'Y':
			value = strconv.FormatInt(st.Mtim.Sec, 10)
		case 'Z':
			value = strconv.FormatInt(st.Ctim.Sec, 10)
		default:
			out.WriteByte('%')
			out.WriteByte(format[i])
			continue
		}
		out.WriteString(value)
	}
	return out.String()
}

func fileTypeName(mode os.FileMode) string {
	switch {
	case mode.IsRegular():
		if mode&0o111 != 0 {
			return "regular file"
		}
		return "regular file"
	case mode.IsDir():
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symbolic link"
	case mode&os.ModeNamedPipe != 0:
		return "fifo"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeDevice != 0 && mode&os.ModeCharDevice != 0:
		return "character special file"
	case mode&os.ModeDevice != 0:
		return "block special file"
	default:
		return "unknown"
	}
}

func formatStatTime(ts syscall.Timespec) string {
	return time.Unix(ts.Sec, ts.Nsec).Format("2006-01-02 15:04:05.000000000 -0700")
}

func userName(uid uint32) string {
	value := strconv.FormatUint(uint64(uid), 10)
	if u, err := user.LookupId(value); err == nil {
		return u.Username
	}
	return value
}

func groupName(gid uint32) string {
	value := strconv.FormatUint(uint64(gid), 10)
	if g, err := user.LookupGroupId(value); err == nil {
		return g.Name
	}
	return value
}
