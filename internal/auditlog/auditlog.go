// Package auditlog appends one JSON line per audit-worthy event (a CLI
// command invocation, a daemon-side activity) to a shared file. Multiple OS
// processes -- the long-running daemon and a fresh surviva-cli process per
// invocation -- write to the same file concurrently; see SPEC-audit-log.md
// for why that's safe here and how the file is expected to be rotated
// (logrotate + copytruncate, not this package).
package auditlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one audit record.
type Entry struct {
	Time      time.Time `json:"time"`
	Component string    `json:"component"` // "cli" or "daemon"
	Action    string    `json:"action"`
	JobID     string    `json:"job_id,omitempty"`
	Outcome   string    `json:"outcome"` // "ok" or "error"
	Detail    string    `json:"detail,omitempty"`
}

// Logger appends Entry values to a single file.
type Logger struct {
	mu sync.Mutex
	f  *os.File
}

// Open creates parent directories as needed and opens path for append,
// creating the file if it doesn't already exist.
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create audit log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}
	return &Logger{f: f}, nil
}

// Close closes the underlying file.
func (l *Logger) Close() error {
	return l.f.Close()
}

// Log appends e as one JSON line. e.Time is set to time.Now().UTC() if zero.
// The whole line is written in a single Write call so it stays one atomic
// O_APPEND write from the OS's point of view -- see the package doc comment.
func (l *Logger) Log(e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal audit entry: %w", err)
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(line); err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}
