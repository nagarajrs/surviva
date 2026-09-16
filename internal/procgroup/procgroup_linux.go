//go:build linux

// Package procgroup answers one question for `surviva join`: is this pid
// already the leader of its own process group? See SPEC-surviva-cli.md --
// `cancel` signals a job's whole process group, which is only safe to do
// for a group that isolates just this job (as `run`-started jobs always
// are, via internal/procattr's Setsid). Joining a pid that shares a group
// with unrelated processes would put them at risk on cancel, so join
// refuses unless this reports true.
package procgroup

import "syscall"

// IsGroupLeader reports whether pid's process group id equals pid itself.
func IsGroupLeader(pid int) (bool, error) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return false, err
	}
	return pgid == pid, nil
}
