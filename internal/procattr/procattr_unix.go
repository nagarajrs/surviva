//go:build linux || darwin

// Package procattr builds the OS-specific process attributes surviva needs
// when starting a tracked child (its own process group, so CRIU can later
// dump the whole tree rooted at it).
package procattr

import "syscall"

// New returns SysProcAttr for starting a tracked child in its own process
// group, so its PGID equals its PID.
func New() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
