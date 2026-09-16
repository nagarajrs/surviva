// Package checkpoint orchestrates checkpointing a single tracked job: a
// custom hook if the job registered one, otherwise CRIU. No storage push --
// this only ever writes to job.CheckpointDir on local disk (S3/EBS are out
// of scope for this redesign).
package checkpoint

import (
	"context"
	"fmt"
	"os/exec"

	"surviva/internal/criu"
	"surviva/internal/store"
)

// Run checkpoints j into dir (job.CheckpointDir -- the caller decides this
// path, Run just dumps into it) and returns an error describing what
// failed; the caller translates that into the job's stored status.
func Run(ctx context.Context, dir string, j store.Job) error {
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
