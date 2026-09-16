// Package store persists tracked jobs in a local SQLite database. daemon is
// its only writer; surviva-cli never opens the database directly, it only
// ever sees job data daemon hands back. See SPEC-store.md for the full
// design and status-transition table.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Status is the lifecycle state of a tracked job.
type Status string

const (
	StatusRunning                  Status = "RUNNING"
	StatusCheckpointInProgress     Status = "CHECKPOINT_IN_PROGRESS"
	StatusCheckpointCreated        Status = "CHECKPOINT_CREATED"
	StatusCheckpointCreationFailed Status = "CHECKPOINT_CREATION_FAILED"
	StatusRestorePending           Status = "RESTORE_PENDING"
	StatusRestoreFailed            Status = "RESTORE_FAILED"
	StatusFailed                   Status = "FAILED"
	StatusCanceled                 Status = "CANCELED"
	StatusCompleted                Status = "COMPLETED"
)

// terminalStatuses drop out of List() once reached; they remain reachable
// via Get(). Every other status (including the two "failed but retryable"
// ones, CHECKPOINT_CREATION_FAILED and RESTORE_FAILED) is active.
var terminalStatuses = map[Status]bool{
	StatusFailed:    true,
	StatusCanceled:  true,
	StatusCompleted: true,
}

// validTransitions enumerates every allowed from->to status change, per
// SPEC-store.md's transition table.
var validTransitions = map[Status][]Status{
	StatusRunning: {
		StatusCheckpointInProgress,
		StatusCanceled,
		StatusCompleted,
		StatusFailed,
	},
	StatusCheckpointInProgress: {
		StatusCheckpointCreated,
		StatusCheckpointCreationFailed,
	},
	StatusCheckpointCreationFailed: {
		StatusCheckpointInProgress,
		StatusCanceled,
	},
	StatusCheckpointCreated: {
		StatusRestorePending,
		StatusCanceled,
	},
	StatusRestorePending: {
		StatusRunning,
		StatusRestoreFailed,
	},
	StatusRestoreFailed: {
		StatusRestorePending,
		StatusCanceled,
	},
}

// ValidTransition reports whether a job may move from one status to another.
func ValidTransition(from, to Status) bool {
	for _, allowed := range validTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// Job is a single unit of work surviva is tracking.
type Job struct {
	ID             string
	PID            int
	PGID           int
	CheckpointDir  string
	Command        []string
	WorkDir        string
	HookCheckpoint string
	HookResume     string
	Status         Status
	FailureReason  string
	RegisteredAt   time.Time
	UpdatedAt      time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id              TEXT PRIMARY KEY,
	pid             INTEGER NOT NULL,
	pgid            INTEGER NOT NULL,
	checkpoint_dir  TEXT NOT NULL,
	command         TEXT NOT NULL,
	work_dir        TEXT NOT NULL,
	hook_checkpoint TEXT NOT NULL DEFAULT '',
	hook_resume     TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL,
	failure_reason  TEXT NOT NULL DEFAULT '',
	registered_at   DATETIME NOT NULL,
	updated_at      DATETIME NOT NULL
);
`

// Store wraps a SQLite-backed jobs table.
type Store struct {
	db *sql.DB
}

// Open creates (if needed) and opens the jobs database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // modernc.org/sqlite: avoid concurrent-writer lock errors
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Insert adds a new job row.
func (s *Store) Insert(j Job) error {
	cmdJSON, err := json.Marshal(j.Command)
	if err != nil {
		return fmt.Errorf("marshal command: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO jobs (id, pid, pgid, checkpoint_dir, command, work_dir, hook_checkpoint, hook_resume, status, failure_reason, registered_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.PID, j.PGID, j.CheckpointDir, string(cmdJSON), j.WorkDir, j.HookCheckpoint, j.HookResume, string(j.Status), j.FailureReason, j.RegisteredAt, j.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert job %s: %w", j.ID, err)
	}
	return nil
}

// Get fetches a single job by id, regardless of status. Backs `surviva show`.
func (s *Store) Get(id string) (Job, error) {
	row := s.db.QueryRow(
		`SELECT id, pid, pgid, checkpoint_dir, command, work_dir, hook_checkpoint, hook_resume, status, failure_reason, registered_at, updated_at
		 FROM jobs WHERE id = ?`, id,
	)
	return scanJob(row)
}

// List returns every active (non-terminal) job. Backs `surviva list`.
func (s *Store) List() ([]Job, error) {
	rows, err := s.db.Query(
		`SELECT id, pid, pgid, checkpoint_dir, command, work_dir, hook_checkpoint, hook_resume, status, failure_reason, registered_at, updated_at
		 FROM jobs WHERE status NOT IN (?, ?, ?) ORDER BY registered_at ASC`,
		string(StatusFailed), string(StatusCanceled), string(StatusCompleted),
	)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// UpdateStatus moves a job to a new status, validating the transition and
// refreshing updated_at. failureReason is stored as-is (pass "" if not
// applicable to the target status).
func (s *Store) UpdateStatus(id string, to Status, failureReason string) error {
	existing, err := s.Get(id)
	if err != nil {
		return err
	}
	if !ValidTransition(existing.Status, to) {
		return fmt.Errorf("invalid transition for job %s: %s -> %s", id, existing.Status, to)
	}
	res, err := s.db.Exec(
		`UPDATE jobs SET status = ?, failure_reason = ?, updated_at = ? WHERE id = ?`,
		string(to), failureReason, time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("update job %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("job %s not found", id)
	}
	return nil
}

// UpdatePID rewrites a job's pid/pgid and moves it to RUNNING -- used when a
// resume brings the same job id back to life under a new process.
func (s *Store) UpdatePID(id string, pid, pgid int) error {
	existing, err := s.Get(id)
	if err != nil {
		return err
	}
	if !ValidTransition(existing.Status, StatusRunning) {
		return fmt.Errorf("invalid transition for job %s: %s -> %s", id, existing.Status, StatusRunning)
	}
	res, err := s.db.Exec(
		`UPDATE jobs SET pid = ?, pgid = ?, status = ?, updated_at = ? WHERE id = ?`,
		pid, pgid, string(StatusRunning), time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("update job %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("job %s not found", id)
	}
	return nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (Job, error) {
	var (
		j       Job
		cmdJSON string
		status  string
	)
	if err := row.Scan(&j.ID, &j.PID, &j.PGID, &j.CheckpointDir, &cmdJSON, &j.WorkDir, &j.HookCheckpoint, &j.HookResume, &status, &j.FailureReason, &j.RegisteredAt, &j.UpdatedAt); err != nil {
		return Job{}, fmt.Errorf("scan job row: %w", err)
	}
	if err := json.Unmarshal([]byte(cmdJSON), &j.Command); err != nil {
		return Job{}, fmt.Errorf("unmarshal command: %w", err)
	}
	j.Status = Status(status)
	return j, nil
}
