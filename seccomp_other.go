//go:build linux && !amd64

package main

import "syscall"

// installSeccompFilter is a no-op on architectures whose syscall-number table
// has not been encoded yet (only amd64 is implemented today). The Tier-1
// protections (no_new_privs and core-dump disabling) still apply everywhere.
// To extend coverage, add a build-tagged file with that arch's denied syscall
// numbers, mirroring seccomp_amd64.go.
func installSeccompFilter() error { return syscall.ENOTSUP }
