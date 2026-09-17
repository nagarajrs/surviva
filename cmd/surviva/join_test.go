package main

import (
	"os"
	"testing"

	"surviva/internal/procgroup"
)

// TestBuildJoinRequestRefusesNonGroupLeader exercises the safety check that
// makes buildJoinRequest refuse to adopt a process that isn't already its
// own process-group leader. This process (the test binary itself) is not a
// group leader on any platform surviva runs tests on, so the refusal path
// is always exercised; the Linux group-leader-is-true path is covered by
// internal/procgroup's own trivial syscall wrapper, not re-tested here.
func TestBuildJoinRequestRefusesNonGroupLeader(t *testing.T) {
	pid := os.Getpid()
	isLeader, err := procgroup.IsGroupLeader(pid)
	if err != nil {
		t.Fatalf("IsGroupLeader: %v", err)
	}
	if isLeader {
		t.Skip("test process happens to be its own group leader on this platform; refusal path not exercised here")
	}

	_, err = buildJoinRequest(pid, "", "", "", "")
	if err == nil {
		t.Fatal("expected buildJoinRequest to refuse a non-group-leader pid")
	}
}
