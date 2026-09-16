//go:build linux

// Package procinfo does best-effort process introspection for `surviva
// join`, which (unlike `run`) doesn't already know a process's command line
// or working directory. Never required to succeed -- store.Job accepts
// empty Command/WorkDir (see SPEC-store.md), so a read failure here just
// means a less informative `show`, not a failed join.
package procinfo

import (
	"os"
	"strconv"
	"strings"
)

// CommandLine best-effort reads pid's argv from /proc. Returns nil if
// unavailable.
func CommandLine(pid int) []string {
	raw, err := os.ReadFile(procPath(pid, "cmdline"))
	if err != nil || len(raw) == 0 {
		return nil
	}
	// /proc/<pid>/cmdline is NUL-separated argv, with a trailing NUL.
	return strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
}

// WorkDir best-effort reads pid's current working directory from /proc.
// Returns "" if unavailable.
func WorkDir(pid int) string {
	dir, err := os.Readlink(procPath(pid, "cwd"))
	if err != nil {
		return ""
	}
	return dir
}

func procPath(pid int, leaf string) string {
	return "/proc/" + strconv.Itoa(pid) + "/" + leaf
}
