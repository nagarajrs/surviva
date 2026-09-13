//go:build linux || darwin

// Package procsignal forwards a signal to a tracked child's whole process
// group, needed because that child runs in its own session (see
// internal/procattr) and so never receives a terminal's own Ctrl+C: SIGINT
// from a tty only goes to the terminal's foreground process group, which is
// surviva run's own, not the child's separate one.
package procsignal

import "syscall"

// KillGroup sends sig to every process in pid's process group (pid is
// assumed to be its own process group leader, i.e. PGID == PID, which is
// how internal/procattr starts tracked children).
func KillGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}
