// Package job defines the tracked-job data model shared by the daemon,
// its SQLite store, and the CLI.
package job

import "time"

// Status is the lifecycle state of a tracked job.
type Status string

const (
	StatusRunning              Status = "RUNNING"
	StatusCheckpointInProgress Status = "CHECKPOINT_IN_PROGRESS"
	StatusCheckpointComplete   Status = "CHECKPOINT_COMPLETE"
	StatusCheckpointIncomplete Status = "CHECKPOINT_INCOMPLETE"
	StatusRestoring            Status = "RESTORING"
	StatusRestored             Status = "RESTORED"
	StatusFailed               Status = "FAILED"
)

// Job is a single process tracked by the surviva daemon between
// `surviva run` registering it and either normal exit or checkpoint/restore.
type Job struct {
	ID             string    `json:"id"`
	PID            int       `json:"pid"`
	PGID           int       `json:"pgid"`
	Command        []string  `json:"command"`
	WorkDir        string    `json:"work_dir"`
	HookCheckpoint string    `json:"hook_checkpoint,omitempty"`
	HookResume     string    `json:"hook_resume,omitempty"`
	Priority       int       `json:"priority"`
	Status         Status    `json:"status"`
	RegisteredAt   time.Time `json:"registered_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
