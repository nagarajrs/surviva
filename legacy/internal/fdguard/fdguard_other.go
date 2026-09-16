//go:build !linux

// Package fdguard prevents unexpected inherited file descriptors from
// leaking into a tracked child. It is a no-op outside Linux, since
// surviva's CRIU integration only targets Linux.
package fdguard

// CloseInherited is a no-op on this platform.
func CloseInherited() {}
