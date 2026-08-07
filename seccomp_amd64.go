// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux && amd64

package main

import (
	"syscall"
	"unsafe"
)

// seccomp / BPF constants (from <linux/seccomp.h>, <linux/audit.h>, <linux/bpf_common.h>).
const (
	auditArchX86_64        = 0xC000003E
	seccompSetModeFilter   = 1
	seccompFilterFlagTSYNC = 1
	seccompRetKillProcess  = 0x80000000
	seccompRetAllow        = 0x7FFF0000
	sysSeccomp             = 317 // amd64 __NR_seccomp

	bpfLD  = 0x00
	bpfALU = 0x04
	bpfW   = 0x00
	bpfABS = 0x20
	bpfAND = 0x50
	bpfJMP = 0x05
	bpfJEQ = 0x10
	bpfRET = 0x06
	bpfK   = 0x00

	x32SyscallBit = 0x40000000
	sysSocket     = 41
)

// deniedSyscalls lists amd64 syscall numbers a coreutils binary never has a
// legitimate reason to make. Invoking any of them kills the whole process.
// The list is a denylist on purpose: it cannot accidentally block the syscalls
// the Go runtime relies on (futex, mmap, clone, epoll, ...).
var deniedSyscalls = []uint32{
	59,  // execve          - spawn another program
	322, // execveat        - spawn another program (fd-relative)
	101, // ptrace          - attach to / inject into processes
	53,  // socketpair      - create a local IPC socket pair
	42,  // connect
	43,  // accept
	288, // accept4
	50,  // listen
	165, // mount
	166, // umount2
	155, // pivot_root
	175, // init_module
	313, // finit_module
	176, // delete_module
	246, // kexec_load
	320, // kexec_file_load
	310, // process_vm_readv  - read another process's memory
	311, // process_vm_writev - write another process's memory
}

// sockFilter mirrors struct sock_filter (a classic-BPF instruction).
type sockFilter struct {
	code uint16
	jt   uint8
	jf   uint8
	k    uint32
}

// sockFprog mirrors struct sock_fprog (a BPF program passed to seccomp).
type sockFprog struct {
	length uint16
	filter *sockFilter
}

// installSeccompFilter builds and installs the syscall denylist across all
// runtime threads (TSYNC). The program is:
//
//	A = arch;  if A != X86_64 -> KILL          (blocks 32-bit/x32 ABI bypass)
//	A = nr;    for each denied nr: if A == nr -> KILL
//	-> ALLOW
func installSeccompFilter() error {
	prog := make([]sockFilter, 0, 16+len(deniedSyscalls)+2)
	prog = append(prog,
		sockFilter{bpfLD | bpfW | bpfABS, 0, 0, 4}, // A = seccomp_data.arch (offset 4)
		sockFilter{bpfJMP | bpfJEQ | bpfK, 1, 0, auditArchX86_64},
		sockFilter{bpfRET | bpfK, 0, 0, seccompRetKillProcess},  // wrong arch
		sockFilter{bpfLD | bpfW | bpfABS, 0, 0, 0},              // A = seccomp_data.nr
		sockFilter{bpfALU | bpfAND | bpfK, 0, 0, x32SyscallBit}, // reject the x32 syscall namespace
		sockFilter{bpfJMP | bpfJEQ | bpfK, 1, 0, 0},
		sockFilter{bpfRET | bpfK, 0, 0, seccompRetKillProcess},
		sockFilter{bpfLD | bpfW | bpfABS, 0, 0, 0}, // reload nr after masking

		// ip(8) needs NETLINK_ROUTE and iptables(8) needs NETLINK_NETFILTER.
		// Allow only those AF_NETLINK protocols; all other newly-created socket
		// families and netlink protocols are killed.
		sockFilter{bpfJMP | bpfJEQ | bpfK, 0, 7, sysSocket},
		sockFilter{bpfLD | bpfW | bpfABS, 0, 0, 16}, // args[0]: domain
		sockFilter{bpfJMP | bpfJEQ | bpfK, 0, 4, uint32(syscall.AF_NETLINK)},
		sockFilter{bpfLD | bpfW | bpfABS, 0, 0, 32}, // args[2]: protocol
		sockFilter{bpfJMP | bpfJEQ | bpfK, 1, 0, uint32(syscall.NETLINK_ROUTE)},
		sockFilter{bpfJMP | bpfJEQ | bpfK, 0, 1, netlinkNetfilter},
		sockFilter{bpfRET | bpfK, 0, 0, seccompRetAllow},
		sockFilter{bpfRET | bpfK, 0, 0, seccompRetKillProcess},
		sockFilter{bpfLD | bpfW | bpfABS, 0, 0, 0}, // reload nr for the denylist
	)

	// Layout after the comparisons: ALLOW is the fallthrough (a syscall that
	// matched none of the denied numbers), and KILL sits last as the jump
	// target each matching comparison branches to.
	killIdx := len(prog) + len(deniedSyscalls) + 1
	for _, nr := range deniedSyscalls {
		jt := byte(killIdx - len(prog) - 1) //nolint:gosec // G115: jump distance <= len(deniedSyscalls) < 256
		prog = append(prog, sockFilter{bpfJMP | bpfJEQ | bpfK, jt, 0, nr})
	}
	prog = append(prog,
		sockFilter{bpfRET | bpfK, 0, 0, seccompRetAllow},       // fallthrough: allowed
		sockFilter{bpfRET | bpfK, 0, 0, seccompRetKillProcess}, // jump target: denied
	)

	fprog := sockFprog{
		length: uint16(len(prog)), //nolint:gosec // G115: fixed small program length
		filter: &prog[0],
	}
	_, _, errno := syscall.Syscall(sysSeccomp, seccompSetModeFilter,
		seccompFilterFlagTSYNC, uintptr(unsafe.Pointer(&fprog))) //nolint:gosec // G103: fixed-size BPF program passed to the kernel
	if errno != 0 {
		return errno
	}
	return nil
}
