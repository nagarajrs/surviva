//go:build linux || darwin

// Package procattr builds the OS-specific process attributes surviva needs
// when starting a tracked child: its own session, so CRIU can dump/restore
// the whole tree rooted at it without needing to reattach to an external
// shell or controlling terminal (which won't exist on the replacement
// instance restore runs on anyway).
package procattr

import "syscall"

// New returns SysProcAttr for starting a tracked child as the leader of a
// new session (Setsid implies a new process group too, so PGID == PID).
func New() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
