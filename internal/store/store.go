// Package store persists tracked jobs in a local SQLite database so the
// daemon's job list survives its own restarts.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"surviva/internal/job"
)

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id              TEXT PRIMARY KEY,
	pid             INTEGER NOT NULL,
	pgid            INTEGER NOT NULL,
	command         TEXT NOT NULL,
	work_dir        TEXT NOT NULL,
	hook_checkpoint TEXT NOT NULL DEFAULT '',
	hook_resume     TEXT NOT NULL DEFAULT '',
	priority        INTEGER NOT NULL DEFAULT 0,
	status          TEXT NOT NULL,
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
func (s *Store) Insert(j job.Job) error {
	cmdJSON, err := json.Marshal(j.Command)
	if err != nil {
		return fmt.Errorf("marshal command: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO jobs (id, pid, pgid, command, work_dir, hook_checkpoint, hook_resume, priority, status, registered_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.PID, j.PGID, string(cmdJSON), j.WorkDir, j.HookCheckpoint, j.HookResume, j.Priority, string(j.Status), j.RegisteredAt, j.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert job %s: %w", j.ID, err)
	}
	return nil
}

// Delete removes a job row by id. It is not an error if the id is absent.
func (s *Store) Delete(id string) error {
	if _, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete job %s: %w", id, err)
	}
	return nil
}

// UpdateStatus sets a job's status and refreshes updated_at.
func (s *Store) UpdateStatus(id string, status job.Status) error {
	res, err := s.db.Exec(`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`, string(status), time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("update job %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("job %s not found", id)
	}
	return nil
}

// Get fetches a single job by id.
func (s *Store) Get(id string) (job.Job, error) {
	row := s.db.QueryRow(
		`SELECT id, pid, pgid, command, work_dir, hook_checkpoint, hook_resume, priority, status, registered_at, updated_at
		 FROM jobs WHERE id = ?`, id,
	)
	return scanJob(row)
}

// List returns all tracked jobs, highest priority first.
func (s *Store) List() ([]job.Job, error) {
	rows, err := s.db.Query(
		`SELECT id, pid, pgid, command, work_dir, hook_checkpoint, hook_resume, priority, status, registered_at, updated_at
		 FROM jobs ORDER BY priority DESC, registered_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []job.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (job.Job, error) {
	var (
		j       job.Job
		cmdJSON string
		status  string
	)
	if err := row.Scan(&j.ID, &j.PID, &j.PGID, &cmdJSON, &j.WorkDir, &j.HookCheckpoint, &j.HookResume, &j.Priority, &status, &j.RegisteredAt, &j.UpdatedAt); err != nil {
		return job.Job{}, fmt.Errorf("scan job row: %w", err)
	}
	if err := json.Unmarshal([]byte(cmdJSON), &j.Command); err != nil {
		return job.Job{}, fmt.Errorf("unmarshal command: %w", err)
	}
	j.Status = job.Status(status)
	return j, nil
}
