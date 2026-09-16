// Package store persists tracked jobs in a local SQLite database. daemon is
// its only writer; surviva-cli never opens the database directly, it only
// ever sees job data daemon hands back. See docs/specs/store.md for the full
// design and status-transition table.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
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
// docs/specs/store.md's transition table.
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

// IsTerminal reports whether a status is one List() drops off (FAILED,
// CANCELED, COMPLETED). Exported so callers like surviva-cli can decide
// whether to compute a job's running duration against time.Now() or
// UpdatedAt.
func IsTerminal(s Status) bool {
	return terminalStatuses[s]
}

// Job is a single unit of work surviva is tracking. ID is a sequential
// integer, formatted as a string (Slurm-style: "1", "2", "3", ...) --
// assigned by Insert, not the caller.
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
	Owner          string // OS user who ran `surviva run`/`join`, captured at registration
	RegisteredAt   time.Time
	UpdatedAt      time.Time
}

// HistoryEntry is one append-only record of a job's status transition. Backs
// `surviva show -history`.
type HistoryEntry struct {
	JobID      string
	FromStatus Status
	ToStatus   Status
	ChangedBy  string
	ChangedAt  time.Time
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS jobs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	pid             INTEGER NOT NULL,
	pgid            INTEGER NOT NULL,
	checkpoint_dir  TEXT NOT NULL,
	command         TEXT NOT NULL,
	work_dir        TEXT NOT NULL,
	hook_checkpoint TEXT NOT NULL DEFAULT '',
	hook_resume     TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL,
	failure_reason  TEXT NOT NULL DEFAULT '',
	owner           TEXT NOT NULL DEFAULT '',
	registered_at   DATETIME NOT NULL,
	updated_at      DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS job_history (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id      INTEGER NOT NULL,
	from_status TEXT NOT NULL,
	to_status   TEXT NOT NULL,
	changed_by  TEXT NOT NULL,
	changed_at  DATETIME NOT NULL
);
`

// mysqlSchema differs from sqliteSchema only where MySQL's dialect forces
// it to: AUTO_INCREMENT instead of AUTOINCREMENT, status bounded to
// VARCHAR (always one of the nine known status strings, never unbounded),
// and no DEFAULT ” on the TEXT columns -- older MySQL/MariaDB reject a
// default on TEXT/BLOB, and it's redundant anyway since Insert always
// supplies an explicit value for every column.
const mysqlSchema = `
CREATE TABLE IF NOT EXISTS jobs (
	id              BIGINT PRIMARY KEY AUTO_INCREMENT,
	pid             INTEGER NOT NULL,
	pgid            INTEGER NOT NULL,
	checkpoint_dir  TEXT NOT NULL,
	command         TEXT NOT NULL,
	work_dir        TEXT NOT NULL,
	hook_checkpoint TEXT NOT NULL,
	hook_resume     TEXT NOT NULL,
	status          VARCHAR(64) NOT NULL,
	failure_reason  TEXT NOT NULL,
	owner           VARCHAR(255) NOT NULL,
	registered_at   DATETIME NOT NULL,
	updated_at      DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS job_history (
	id          BIGINT PRIMARY KEY AUTO_INCREMENT,
	job_id      BIGINT NOT NULL,
	from_status VARCHAR(64) NOT NULL,
	to_status   VARCHAR(64) NOT NULL,
	changed_by  VARCHAR(255) NOT NULL,
	changed_at  DATETIME NOT NULL
);
`

// Store wraps a SQLite- or MySQL-backed jobs table (see Options).
type Store struct {
	db *sql.DB
}

// Options selects and configures the job-table backend. The zero value
// (empty Driver) means "sqlite" -- Path is then required; the mysql fields
// are required instead when Driver is "mysql". See docs/specs/store.md.
type Options struct {
	Driver string // "sqlite" (default if empty) or "mysql"

	Path string // sqlite

	Host     string // mysql
	Port     int    // mysql
	User     string // mysql
	Password string // mysql, may be empty
	DBName   string // mysql
}

// Open creates (if needed) and opens the jobs database described by opts.
func Open(opts Options) (*Store, error) {
	driver := opts.Driver
	if driver == "" {
		driver = "sqlite"
	}

	var dsn, ddl string
	switch driver {
	case "sqlite":
		if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
		dsn = opts.Path
		ddl = sqliteSchema
	case "mysql":
		cfg := mysql.NewConfig()
		cfg.User = opts.User
		cfg.Passwd = opts.Password
		cfg.Net = "tcp"
		cfg.Addr = fmt.Sprintf("%s:%d", opts.Host, opts.Port)
		cfg.DBName = opts.DBName
		cfg.ParseTime = true       // RegisteredAt/UpdatedAt scan straight into time.Time, like sqlite already does
		cfg.MultiStatements = true // mysqlSchema is more than one CREATE TABLE in a single Exec
		dsn = cfg.FormatDSN()
		ddl = mysqlSchema
	default:
		return nil, fmt.Errorf("unsupported db driver %q (must be sqlite or mysql)", driver)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s db: %w", driver, err)
	}
	if driver == "sqlite" {
		db.SetMaxOpenConns(1) // modernc.org/sqlite: avoid concurrent-writer lock errors -- not a MySQL concern
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to %s db: %w", driver, err)
	}
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS is a no-op against an already-existing jobs
	// table from before `owner` existed -- an in-place upgrade otherwise
	// fails on the very first Insert ("no such column: owner"). job_history
	// needs no such backfill: it's an entirely new table, so IF NOT EXISTS
	// already creates it correctly on an upgrade.
	if err := addColumnIfMissing(db, driver, "jobs", "owner", "TEXT NOT NULL DEFAULT ''", "VARCHAR(255) NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{db: db}, nil
}

// addColumnIfMissing runs an ALTER TABLE ADD COLUMN if column isn't already
// present on table -- the minimal migration this package needs today (one
// column added to a pre-existing table). sqliteType/mysqlType are the full
// column definition (type + constraints) for each dialect.
func addColumnIfMissing(db *sql.DB, driver, table, column, sqliteType, mysqlType string) error {
	has, err := hasColumn(db, driver, table, column)
	if err != nil {
		return fmt.Errorf("check column %s.%s: %w", table, column, err)
	}
	if has {
		return nil
	}
	colType := sqliteType
	if driver == "mysql" {
		colType = mysqlType
	}
	if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colType)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

func hasColumn(db *sql.DB, driver, table, column string) (bool, error) {
	switch driver {
	case "sqlite":
		rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			return false, err
		}
		defer rows.Close()
		for rows.Next() {
			var cid, notnull, pk int
			var name, ctype string
			var dflt any
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				return false, err
			}
			if strings.EqualFold(name, column) {
				return true, nil
			}
		}
		return false, rows.Err()
	case "mysql":
		var n int
		err := db.QueryRow(
			`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
			table, column,
		).Scan(&n)
		return n > 0, err
	default:
		return false, fmt.Errorf("unsupported driver %q", driver)
	}
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Insert adds a new job row, ignoring any j.ID the caller set, and returns
// the sequential id the database assigned it.
func (s *Store) Insert(j Job) (string, error) {
	cmdJSON, err := json.Marshal(j.Command)
	if err != nil {
		return "", fmt.Errorf("marshal command: %w", err)
	}
	res, err := s.db.Exec(
		`INSERT INTO jobs (pid, pgid, checkpoint_dir, command, work_dir, hook_checkpoint, hook_resume, status, failure_reason, owner, registered_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.PID, j.PGID, j.CheckpointDir, string(cmdJSON), j.WorkDir, j.HookCheckpoint, j.HookResume, string(j.Status), j.FailureReason, j.Owner, j.RegisteredAt, j.UpdatedAt,
	)
	if err != nil {
		return "", fmt.Errorf("insert job: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("get inserted job id: %w", err)
	}
	return strconv.FormatInt(id, 10), nil
}

// Get fetches a single job by id, regardless of status. Backs `surviva show`.
func (s *Store) Get(id string) (Job, error) {
	idInt, err := parseID(id)
	if err != nil {
		return Job{}, err
	}
	row := s.db.QueryRow(
		`SELECT id, pid, pgid, checkpoint_dir, command, work_dir, hook_checkpoint, hook_resume, status, failure_reason, owner, registered_at, updated_at
		 FROM jobs WHERE id = ?`, idInt,
	)
	return scanJob(row)
}

// List returns every active (non-terminal) job. Backs `surviva list`.
func (s *Store) List() ([]Job, error) {
	rows, err := s.db.Query(
		`SELECT id, pid, pgid, checkpoint_dir, command, work_dir, hook_checkpoint, hook_resume, status, failure_reason, owner, registered_at, updated_at
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

// ListTerminal returns every job in a terminal status (FAILED, CANCELED,
// COMPLETED) -- List()'s mirror image. Backs `surviva prune`, which needs to
// find every terminal job's CheckpointDir without touching active jobs.
func (s *Store) ListTerminal() ([]Job, error) {
	rows, err := s.db.Query(
		`SELECT id, pid, pgid, checkpoint_dir, command, work_dir, hook_checkpoint, hook_resume, status, failure_reason, owner, registered_at, updated_at
		 FROM jobs WHERE status IN (?, ?, ?) ORDER BY registered_at ASC`,
		string(StatusFailed), string(StatusCanceled), string(StatusCompleted),
	)
	if err != nil {
		return nil, fmt.Errorf("list terminal jobs: %w", err)
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
// applicable to the target status). changedBy is recorded as-is in the
// append-only job_history table (the OS user for a CLI-driven change, or
// "daemon" for an automatic one) -- see History.
func (s *Store) UpdateStatus(id string, to Status, failureReason, changedBy string) error {
	idInt, err := parseID(id)
	if err != nil {
		return err
	}
	existing, err := s.Get(id)
	if err != nil {
		return err
	}
	if !ValidTransition(existing.Status, to) {
		return fmt.Errorf("invalid transition for job %s: %s -> %s", id, existing.Status, to)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("update job %s: %w", id, err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	res, err := tx.Exec(
		`UPDATE jobs SET status = ?, failure_reason = ?, updated_at = ? WHERE id = ?`,
		string(to), failureReason, now, idInt,
	)
	if err != nil {
		return fmt.Errorf("update job %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("job %s not found", id)
	}
	if err := recordHistory(tx, idInt, existing.Status, to, changedBy, now); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdatePID rewrites a job's pid/pgid and moves it to RUNNING -- used when a
// resume brings the same job id back to life under a new process. changedBy
// is recorded the same way as in UpdateStatus.
func (s *Store) UpdatePID(id string, pid, pgid int, changedBy string) error {
	idInt, err := parseID(id)
	if err != nil {
		return err
	}
	existing, err := s.Get(id)
	if err != nil {
		return err
	}
	if !ValidTransition(existing.Status, StatusRunning) {
		return fmt.Errorf("invalid transition for job %s: %s -> %s", id, existing.Status, StatusRunning)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("update job %s: %w", id, err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	res, err := tx.Exec(
		`UPDATE jobs SET pid = ?, pgid = ?, status = ?, updated_at = ? WHERE id = ?`,
		pid, pgid, string(StatusRunning), now, idInt,
	)
	if err != nil {
		return fmt.Errorf("update job %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("job %s not found", id)
	}
	if err := recordHistory(tx, idInt, existing.Status, StatusRunning, changedBy, now); err != nil {
		return err
	}
	return tx.Commit()
}

// recordHistory appends one job_history row as part of an in-flight
// transaction, so a status change and its history entry always land
// together.
func recordHistory(tx *sql.Tx, jobID int64, from, to Status, changedBy string, at time.Time) error {
	if _, err := tx.Exec(
		`INSERT INTO job_history (job_id, from_status, to_status, changed_by, changed_at) VALUES (?, ?, ?, ?, ?)`,
		jobID, string(from), string(to), changedBy, at,
	); err != nil {
		return fmt.Errorf("record history for job %d: %w", jobID, err)
	}
	return nil
}

// History returns every recorded status transition for a job, oldest first.
// Backs `surviva show -history`.
func (s *Store) History(id string) ([]HistoryEntry, error) {
	idInt, err := parseID(id)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT job_id, from_status, to_status, changed_by, changed_at FROM job_history WHERE job_id = ? ORDER BY id ASC`,
		idInt,
	)
	if err != nil {
		return nil, fmt.Errorf("list history for job %s: %w", id, err)
	}
	defer rows.Close()

	var entries []HistoryEntry
	for rows.Next() {
		var (
			h                    HistoryEntry
			jobID                int64
			fromStatus, toStatus string
		)
		if err := rows.Scan(&jobID, &fromStatus, &toStatus, &h.ChangedBy, &h.ChangedAt); err != nil {
			return nil, fmt.Errorf("scan history row: %w", err)
		}
		h.JobID = strconv.FormatInt(jobID, 10)
		h.FromStatus = Status(fromStatus)
		h.ToStatus = Status(toStatus)
		entries = append(entries, h)
	}
	return entries, rows.Err()
}

// UpdateCheckpointDir sets a job's checkpoint directory. Used once at
// registration time to fill in the default path (<CheckpointBaseDir>/<id>),
// which can't be known until Insert has assigned the id.
func (s *Store) UpdateCheckpointDir(id, dir string) error {
	idInt, err := parseID(id)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE jobs SET checkpoint_dir = ?, updated_at = ? WHERE id = ?`,
		dir, time.Now().UTC(), idInt,
	)
	if err != nil {
		return fmt.Errorf("update job %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("job %s not found", id)
	}
	return nil
}

// parseID converts a job id string (as typed on the command line or sent
// over ipc) into the integer the jobs table actually keys on.
func parseID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid job id %q", id)
	}
	return n, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (Job, error) {
	var (
		j       Job
		id      int64
		cmdJSON string
		status  string
	)
	if err := row.Scan(&id, &j.PID, &j.PGID, &j.CheckpointDir, &cmdJSON, &j.WorkDir, &j.HookCheckpoint, &j.HookResume, &status, &j.FailureReason, &j.Owner, &j.RegisteredAt, &j.UpdatedAt); err != nil {
		return Job{}, fmt.Errorf("scan job row: %w", err)
	}
	j.ID = strconv.FormatInt(id, 10)
	if err := json.Unmarshal([]byte(cmdJSON), &j.Command); err != nil {
		return Job{}, fmt.Errorf("unmarshal command: %w", err)
	}
	j.Status = Status(status)
	return j, nil
}
