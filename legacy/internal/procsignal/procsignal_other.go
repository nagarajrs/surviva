//go:build !linux && !darwin

// Package procsignal forwards a signal to a tracked child's process group.
// Non-Linux builds exist purely so the CLI/daemon scaffolding can be
// developed and tested on other platforms; process-group signaling isn't
// meaningful there for surviva's purposes.
package procsignal

import "syscall"

// KillGroup is a no-op on platforms without process-group signal semantics
// relevant to surviva.
func KillGroup(pid int, sig syscall.Signal) error {
	return nil
}
