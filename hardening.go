//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
)

// Linux prctl options (from <linux/prctl.h>); not all are exported by the
// stdlib syscall package, so define the ones we need.
const (
	prSetDumpable   = 4
	prSetNoNewPrivs = 38
)

// applyHardening installs the kernel-level protections once at startup, before
// any applet runs. It is fail-closed: if a protection cannot be enabled, the
// process refuses to continue rather than running unprotected.
//
// The protections assume the canonical static build (CGO_ENABLED=0). That is
// already required for the zero-dependency goal, and it also guarantees the
// pure-Go os/user path (reading /etc/passwd directly) so the seccomp socket
// ban below never trips NSS.
func applyHardening(enableSeccomp bool) {
	// no_new_privs must be set before installing a seccomp filter without
	// CAP_SYS_ADMIN, and it also prevents regaining privileges via a setuid
	// exec later in the process lifetime.
	if err := setNoNewPrivs(); err != nil {
		hardeningFatal("no_new_privs", err)
	}
	disableCoreDumps()
	if enableSeccomp {
		if err := installSeccompFilter(); err != nil {
			hardeningFatal("seccomp", err)
		}
	}
}

func setNoNewPrivs() error {
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}

// disableCoreDumps stops the kernel from writing a core file, which could spill
// file contents held in memory to disk. Best-effort: failure here is not on its
// own a security hole, so it does not abort startup.
func disableCoreDumps() {
	_, _, _ = syscall.Syscall6(syscall.SYS_PRCTL, prSetDumpable, 0, 0, 0, 0, 0)
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
}

func hardeningFatal(what string, err error) {
	fmt.Fprintf(os.Stderr, "ba6: cannot enable %s protection: %v\n", what, err)
	os.Exit(1)
}
