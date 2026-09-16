//go:build !linux && !darwin

// Package procattr builds the OS-specific process attributes surviva needs
// when starting a tracked child. CRIU only exists on Linux; non-Linux
// builds exist purely so the CLI/daemon scaffolding can be developed and
// tested on other platforms.
package procattr

import "syscall"

// New returns the default (no special grouping) SysProcAttr on platforms
// without process-group semantics relevant to surviva.
func New() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
