// Package resume restores a checkpointed job from a local checkpoint
// directory: a custom hook if one was registered, otherwise CRIU.
package resume

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"surviva/internal/criu"
)

// Run resumes the job whose checkpoint images are in checkpointDir and
// returns the resumed root process's PID.
//
// hookResume, if non-empty, is invoked as `hookResume <jobID> <checkpointDir>`
// and must print the resumed PID as a single integer line on stdout — the
// same contract HookCheckpoint's caller uses in reverse.
func Run(ctx context.Context, checkpointDir, hookResume, jobID string) (int, error) {
	if hookResume != "" {
		cmd := exec.CommandContext(ctx, hookResume, jobID, checkpointDir)
		out, err := cmd.Output()
		if err != nil {
			return 0, fmt.Errorf("resume hook %s: %w", hookResume, err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			return 0, fmt.Errorf("resume hook %s: must print the resumed pid, got %q: %w", hookResume, out, err)
		}
		return pid, nil
	}

	if !criu.Available() {
		return 0, fmt.Errorf("criu not found on PATH and job %s registered no --hook-resume", jobID)
	}
	return criu.Restore(ctx, checkpointDir)
}
