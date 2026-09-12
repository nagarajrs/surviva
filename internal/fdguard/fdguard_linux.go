//go:build linux

// Package fdguard prevents file descriptors surviva's own process happens
// to have inherited (for example a stray pty fd left open by an enclosing
// shell) from leaking into a tracked child, where an unexpected tty- or
// socket-backed fd can silently break CRIU dump/restore.
package fdguard

import (
	"os"
	"strconv"
	"syscall"
)

// CloseInherited sets close-on-exec on every open file descriptor above
// stderr (2) in the current process, so the next exec() this process
// performs won't hand them to the child. Best-effort: failures to read
// /proc/self/fd or to flag a single fd are ignored.
func CloseInherited() {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd <= 2 {
			continue
		}
		syscall.CloseOnExec(fd)
	}
}
