// Package checkpoint orchestrates checkpointing a single tracked job: a
// custom hook if the job registered one, otherwise CRIU. Phase 2 only
// writes to local disk — pushing to S3/EBS lands in later phases.
package checkpoint

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	"surviva/internal/criu"
	"surviva/internal/job"
)

// Dir returns the on-disk checkpoint directory for a job under baseDir.
func Dir(baseDir, jobID string) string {
	return filepath.Join(baseDir, jobID)
}

// Run checkpoints j into baseDir and returns an error describing what
// failed; the caller translates that into the job's stored status.
func Run(ctx context.Context, baseDir string, j job.Job) error {
	dir := Dir(baseDir, j.ID)

	if j.HookCheckpoint != "" {
		cmd := exec.CommandContext(ctx, j.HookCheckpoint, j.ID, dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("checkpoint hook %s: %w\n%s", j.HookCheckpoint, err, out)
		}
		return nil
	}

	if !criu.Available() {
		return fmt.Errorf("criu not found on PATH and job %s registered no --hook-checkpoint", j.ID)
	}
	return criu.Dump(ctx, j.PID, dir)
}
