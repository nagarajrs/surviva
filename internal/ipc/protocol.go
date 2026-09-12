// Package ipc defines the newline-delimited JSON protocol spoken between
// `surviva run`/`surviva list` (clients) and `surviva daemon` (server) over
// a local Unix domain socket.
package ipc

import (
	"os"
	"path/filepath"
	"runtime"

	"surviva/internal/job"
)

// Action identifies the requested daemon operation.
type Action string

const (
	ActionPing       Action = "ping"
	ActionRegister   Action = "register"
	ActionDeregister Action = "deregister"
	ActionList       Action = "list"
)

// RegisterJob is the payload for ActionRegister.
type RegisterJob struct {
	PID            int      `json:"pid"`
	PGID           int      `json:"pgid"`
	Command        []string `json:"command"`
	WorkDir        string   `json:"work_dir"`
	HookCheckpoint string   `json:"hook_checkpoint,omitempty"`
	HookResume     string   `json:"hook_resume,omitempty"`
	Priority       int      `json:"priority"`
}

// Request is one client -> daemon message.
type Request struct {
	Action Action       `json:"action"`
	Job    *RegisterJob `json:"job,omitempty"`
	JobID  string       `json:"job_id,omitempty"`
}

// Response is one daemon -> client message.
type Response struct {
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	JobID string    `json:"job_id,omitempty"`
	Jobs  []job.Job `json:"jobs,omitempty"`
}

// DefaultSocketPath returns the socket path clients and the daemon agree on
// unless overridden by SURVIVA_SOCKET. Production deployments (Linux) use
// /var/run/surviva/surviva.sock; a temp-dir path is used elsewhere for local
// development.
func DefaultSocketPath() string {
	if p := os.Getenv("SURVIVA_SOCKET"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "surviva.sock")
	}
	return "/var/run/surviva/surviva.sock"
}

// DefaultDBPath returns the SQLite job-store path unless overridden by
// SURVIVA_DB_PATH.
func DefaultDBPath() string {
	if p := os.Getenv("SURVIVA_DB_PATH"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "surviva.db")
	}
	return "/var/lib/surviva/jobs.db"
}

// DefaultCheckpointDir returns the local directory checkpoint images are
// written under (one subdirectory per job id) unless overridden by
// SURVIVA_CHECKPOINT_DIR.
func DefaultCheckpointDir() string {
	if p := os.Getenv("SURVIVA_CHECKPOINT_DIR"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "surviva-checkpoints")
	}
	return "/var/lib/surviva/checkpoints"
}
