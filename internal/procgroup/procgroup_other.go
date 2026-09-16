//go:build !linux

// Package procgroup answers one question for `surviva join`: is this pid
// already the leader of its own process group? Process-group semantics
// relevant to surviva's `cancel` only apply on Linux (surviva's only
// production target); this stub lets `join` build/run elsewhere too, always
// refusing (safe default) since group membership can't be checked here.
package procgroup

// IsGroupLeader always reports false on this platform: there's no way to
// check, and refusing is the safe default (see the Linux implementation's
// doc comment for why this matters).
func IsGroupLeader(pid int) (bool, error) {
	return false, nil
}
