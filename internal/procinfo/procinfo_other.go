//go:build !linux

// Package procinfo does best-effort process introspection for `surviva
// join`. /proc only exists on Linux; this stub keeps other platforms
// buildable, always returning "unknown."
package procinfo

// CommandLine always returns nil on this platform.
func CommandLine(pid int) []string { return nil }

// WorkDir always returns "" on this platform.
func WorkDir(pid int) string { return "" }
