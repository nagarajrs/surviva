//go:build !linux && !darwin

// Package sigset lists the OS signals that should trigger daemon shutdown.
package sigset

import "os"

// TermSignals returns the signals surviva daemon shuts down on.
func TermSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
