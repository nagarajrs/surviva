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

	"surviva/internal/checkpoint"
	"surviva/internal/idgen"
	"surviva/internal/imds"
	"surviva/internal/ipc"
	"surviva/internal/job"
	"surviva/internal/store"
)

// checkpointTimeout bounds a single job's checkpoint attempt. Jobs are
// checkpointed sequentially in this phase, all within the Spot interruption
// notice's ~2-minute window; parallel checkpointing lands in a later phase.
const checkpointTimeout = 100 * time.Second

// Config configures a Daemon.
type Config struct {
	SocketPath    string
	DBPath        string
	CheckpointDir string
	// EnableIMDS controls whether the daemon polls IMDS for Spot signals.
	// Disabling it is only useful off-EC2 (local development).
	EnableIMDS bool
}

// Daemon owns the job store, the socket clients connect to, and (when
// enabled) the IMDS poller that triggers checkpointing.
type Daemon struct {
	store         *store.Store
	socketPath    string
	checkpointDir string
	poller        *imds.Poller
}

// New opens the job store and prepares a daemon to listen on cfg.SocketPath,
// creating parent directories for the socket, database, and checkpoint
// directory as needed.
func New(cfg Config) (*Daemon, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), 0o755); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	if err := os.MkdirAll(cfg.CheckpointDir, 0o755); err != nil {
		return nil, fmt.Errorf("create checkpoint dir: %w", err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	d := &Daemon{store: st, socketPath: cfg.SocketPath, checkpointDir: cfg.CheckpointDir}

	if cfg.EnableIMDS {
		d.poller = imds.NewPoller(imds.NewClient())
		d.poller.OnRebalanceRecommendation = func(*imds.RebalanceRecommendation) {
			d.handleInterruption("rebalance-recommendation")
		}
		d.poller.OnInterruptionNotice = func(*imds.InstanceAction) {
			d.handleInterruption("interruption-notice")
		}
	}

	return d, nil
}

// Run listens for client connections and (if enabled) polls IMDS, until ctx
// is cancelled.
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

	if d.poller != nil {
		go d.poller.Run(ctx)
	}

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
		// A job's status arbitrates a race between `surviva run` seeing its
		// child exit (because checkpointing stopped it) and this deregister
		// call: only delete the row while it's still RUNNING. Once the
		// daemon has moved it into a checkpoint state, the checkpoint
		// subsystem owns the record and it must survive for restore.
		existing, err := d.store.Get(req.JobID)
		if err != nil {
			return ipc.Response{OK: true} // already gone; deregister is idempotent
		}
		if existing.Status != job.StatusRunning {
			return ipc.Response{OK: true}
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

// handleInterruption checkpoints every tracked job, sequentially, in
// response to an IMDS signal. It is called at most once per signal type per
// daemon run (see imds.Poller) but a rebalance recommendation followed by an
// actual interruption notice would call it twice; jobs already past
// StatusRunning are skipped so the second call is a no-op.
func (d *Daemon) handleInterruption(trigger string) {
	jobs, err := d.store.List()
	if err != nil {
		log.Printf("checkpoint: failed to list jobs (trigger=%s): %v", trigger, err)
		return
	}
	if len(jobs) == 0 {
		log.Printf("checkpoint: %s received, no tracked jobs", trigger)
		return
	}

	log.Printf("checkpoint: %s received, checkpointing %d job(s)", trigger, len(jobs))
	for _, j := range jobs {
		if j.Status != job.StatusRunning {
			continue
		}
		d.checkpointJob(j)
	}
}

func (d *Daemon) checkpointJob(j job.Job) {
	if err := d.store.UpdateStatus(j.ID, job.StatusCheckpointInProgress); err != nil {
		log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()

	if err := checkpoint.Run(ctx, d.checkpointDir, j); err != nil {
		log.Printf("checkpoint: job %s FAILED: %v", j.ID, err)
		if err := d.store.UpdateStatus(j.ID, job.StatusFailed); err != nil {
			log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, err)
		}
		return
	}

	log.Printf("checkpoint: job %s complete (%s)", j.ID, checkpoint.Dir(d.checkpointDir, j.ID))
	if err := d.store.UpdateStatus(j.ID, job.StatusCheckpointComplete); err != nil {
		log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, err)
	}
}
