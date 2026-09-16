// Package ipc defines the newline-delimited JSON protocol spoken between
// surviva-cli (client) and surviva daemon (server) over a local Unix domain
// socket. See docs/specs/daemon.md for the full action list and semantics.
package ipc

import (
	"os"
	"path/filepath"
	"runtime"

	"surviva/internal/store"
)

// Action identifies the requested daemon operation.
type Action string

const (
	ActionPing     Action = "ping"
	ActionRegister Action = "register"
	ActionList     Action = "list"
	ActionShow     Action = "show"
	ActionPause    Action = "pause"
	ActionResume   Action = "resume"
	ActionCancel   Action = "cancel"
	ActionComplete Action = "complete"
	ActionPrune    Action = "prune"
)

// RegisterJob is the payload for ActionRegister -- backs both a future
// `surviva run` (starts the process) and `surviva join` (adopts one already
// running); the daemon handles both identically.
type RegisterJob struct {
	PID            int      `json:"pid"`
	PGID           int      `json:"pgid"`
	Command        []string `json:"command"`
	WorkDir        string   `json:"work_dir"`
	CheckpointDir  string   `json:"checkpoint_dir,omitempty"` // optional override; daemon computes the default if empty
	HookCheckpoint string   `json:"hook_checkpoint,omitempty"`
	HookResume     string   `json:"hook_resume,omitempty"`
}

// Request is one client -> daemon message.
type Request struct {
	Action Action       `json:"action"`
	Job    *RegisterJob `json:"job,omitempty"`
	JobID  string       `json:"job_id,omitempty"` // Show/Pause/Resume/Cancel/Complete/Prune (Prune: empty means "all terminal jobs")
	// RequestedBy is the OS user running the CLI, captured by surviva-cli
	// (see cmd/surviva.currentOSUser) for Register/Pause/Resume/Cancel/
	// Complete. Recorded as store.Job.Owner (Register) and
	// store.HistoryEntry.ChangedBy (every status transition).
	RequestedBy string `json:"requested_by,omitempty"`
	// IncludeHistory asks Show to also return the job's full status-change
	// history (see Response.History).
	IncludeHistory bool   `json:"include_history,omitempty"`
	ExitCode       int    `json:"exit_code,omitempty"`
	ErrMsg         string `json:"err_msg,omitempty"`
}

// Response is one daemon -> client message.
type Response struct {
	OK       bool                 `json:"ok"`
	Error    string               `json:"error,omitempty"`
	JobID    string               `json:"job_id,omitempty"`
	Job      *store.Job           `json:"job,omitempty"`     // Show
	Jobs     []store.Job          `json:"jobs,omitempty"`    // List
	History  []store.HistoryEntry `json:"history,omitempty"` // Show, when Request.IncludeHistory is set
	Message  string               `json:"message,omitempty"`
	Failures []string             `json:"failures,omitempty"` // Prune only
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
