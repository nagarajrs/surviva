// Package daemon implements the surviva daemon: it listens on a Unix
// domain socket for `surviva run`/`surviva list` clients and tracks jobs in
// a SQLite store. Phase 1 only covers registration/deregistration/listing;
// IMDS polling and CRIU checkpointing land in later phases.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"surviva/internal/idgen"
	"surviva/internal/ipc"
	"surviva/internal/job"
	"surviva/internal/store"
)

// Daemon owns the job store and the socket clients connect to.
type Daemon struct {
	store      *store.Store
	socketPath string
}

// New opens the job store at dbPath and prepares a daemon to listen on
// socketPath, creating parent directories for both as needed.
func New(socketPath, dbPath string) (*Daemon, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	return &Daemon{store: st, socketPath: socketPath}, nil
}

// Run listens for client connections until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	defer d.store.Close()

	if err := os.Remove(d.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", d.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", d.socketPath, err)
	}
	defer ln.Close()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	log.Printf("surviva daemon listening on %s", d.socketPath)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("accept error: %v", err)
			continue
		}
		go d.handleConn(conn)
	}
}

func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()

	var req ipc.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(ipc.Response{OK: false, Error: err.Error()})
		return
	}

	resp := d.dispatch(req)
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func (d *Daemon) dispatch(req ipc.Request) ipc.Response {
	switch req.Action {
	case ipc.ActionPing:
		return ipc.Response{OK: true}

	case ipc.ActionRegister:
		if req.Job == nil {
			return ipc.Response{OK: false, Error: "missing job payload"}
		}
		id := idgen.New()
		now := time.Now().UTC()
		j := job.Job{
			ID:             id,
			PID:            req.Job.PID,
			PGID:           req.Job.PGID,
			Command:        req.Job.Command,
			WorkDir:        req.Job.WorkDir,
			HookCheckpoint: req.Job.HookCheckpoint,
			HookResume:     req.Job.HookResume,
			Priority:       req.Job.Priority,
			Status:         job.StatusRunning,
			RegisteredAt:   now,
			UpdatedAt:      now,
		}
		if err := d.store.Insert(j); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		log.Printf("tracking job %s (pid %d): %v", id, j.PID, j.Command)
		return ipc.Response{OK: true, JobID: id}

	case ipc.ActionDeregister:
		if req.JobID == "" {
			return ipc.Response{OK: false, Error: "missing job_id"}
		}
		if err := d.store.Delete(req.JobID); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		log.Printf("stopped tracking job %s", req.JobID)
		return ipc.Response{OK: true}

	case ipc.ActionList:
		jobs, err := d.store.List()
		if err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Jobs: jobs}

	default:
		return ipc.Response{OK: false, Error: fmt.Sprintf("unknown action %q", req.Action)}
	}
}
