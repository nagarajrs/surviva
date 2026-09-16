//go:build linux || darwin

// Package procsignal forwards a signal to a tracked child's whole process
// group, needed because that child runs in its own session (see
// internal/procattr): a terminal's Ctrl+C never reaches it directly (SIGINT
// from a tty only goes to the terminal's foreground process group, which is
// surviva run's own, not the child's separate one), and the daemon's own
// `surviva stop` handling has no other handle on the process at all beyond
// its tracked pid/pgid.
package procsignal

import (
	"errors"
	"syscall"
)

// KillGroup sends sig to every process in pid's process group (pid is
// assumed to be its own process group leader, i.e. PGID == PID, which is
// how internal/procattr starts tracked children). A group that no longer
// exists (the process already exited on its own) is treated as success,
// not an error -- there's simply nothing left to signal.
func KillGroup(pid int, sig syscall.Signal) error {
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
