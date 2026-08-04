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

type hardeningProfile struct {
	noNewPrivs bool
	seccomp    bool
}

// hardeningForApplet returns the startup profile for an applet. A real system
// init and the execution frontends must preserve privilege transitions for the
// programs they launch, so those profiles omit no_new_privs as well as seccomp.
func hardeningForApplet(name string, pid int, seccompRequested bool) hardeningProfile {
	if name == "init" && pid == 1 {
		return hardeningProfile{}
	}
	// An execution frontend must not alter the privilege semantics of the
	// program it launches. In particular, distro init uses ba6 sh for inittab
	// commands and interactive consoles that may later invoke login/setuid
	// programs. These applets still disable core dumps at startup.
	switch name {
	case "chroot", "env", "sh", "switch_root", "timeout", "xargs":
		return hardeningProfile{}
	}
	return hardeningProfile{
		noNewPrivs: true,
		seccomp:    seccompRequested && !appletNeedsUnrestrictedSyscalls(name),
	}
}

// applyHardeningProfile installs the kernel-level protections once at startup,
// before any applet runs. It is fail-closed when a requested protection cannot
// be enabled.
//
// The protections assume the canonical static build (CGO_ENABLED=0). That is
// already required for the zero-dependency goal, and it also guarantees the
// pure-Go os/user path (reading /etc/passwd directly) so the seccomp socket
// ban below never trips NSS.
func applyHardeningProfile(profile hardeningProfile) {
	// no_new_privs must be set before installing a seccomp filter without
	// CAP_SYS_ADMIN, and it also prevents regaining privileges via a setuid
	// exec later in the process lifetime.
	if profile.noNewPrivs {
		if err := setNoNewPrivs(); err != nil {
			hardeningFatal("no_new_privs", err)
		}
	}
	disableCoreDumps()
	if profile.seccomp {
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
