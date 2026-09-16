// Package daemon is the long-running surviva process: the sole writer to
// store, the thing that shells out to CRIU, and the thing that watches for
// a cloud interruption signal and reacts to it. See SPEC-daemon.md.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"surviva/internal/auditlog"
	"surviva/internal/checkpoint"
	"surviva/internal/idgen"
	"surviva/internal/ipc"
	"surviva/internal/procsignal"
	"surviva/internal/resume"
	"surviva/internal/store"
)

// Provider is satisfied by internal/daemon/provider.Provider -- redeclared
// here (rather than imported) so this package doesn't force every caller to
// also import the provider package just to construct a Daemon; any type
// with this method works, including the real providers and test fakes.
type Provider interface {
	Run(ctx context.Context, onSignal func(trigger string))
}

// Config configures a Daemon.
type Config struct {
	Store                    *store.Store
	Audit                    *auditlog.Logger
	Provider                 Provider
	CheckpointBaseDir        string
	MaxConcurrentCheckpoints int // <= 0 defaults to runtime.NumCPU()
}

// Daemon owns the job store, the audit log, and the cloud-signal provider.
type Daemon struct {
	store                    *store.Store
	audit                    *auditlog.Logger
	provider                 Provider
	checkpointBaseDir        string
	maxConcurrentCheckpoints int

	// interrupted latches true the moment a rebalance recommendation or
	// interruption notice is ever received (see handleInterruption). A job
	// registered after that point has no realistic path to being
	// checkpointed before the instance is actually reclaimed.
	interrupted atomic.Bool

	// Swappable for tests so daemon's orchestration logic (status
	// transitions, audit entries, error handling) can be exercised without
	// a real CRIU binary. Default to the real implementations in New.
	checkpointFunc func(ctx context.Context, dir string, j store.Job) error
	resumeFunc     func(ctx context.Context, checkpointDir, hookResume, jobID string) (int, error)
}

// New builds a Daemon from cfg.
func New(cfg Config) *Daemon {
	max := cfg.MaxConcurrentCheckpoints
	if max <= 0 {
		max = runtime.NumCPU()
	}
	return &Daemon{
		store:                    cfg.Store,
		audit:                    cfg.Audit,
		provider:                 cfg.Provider,
		checkpointBaseDir:        cfg.CheckpointBaseDir,
		maxConcurrentCheckpoints: max,
		checkpointFunc:           checkpoint.Run,
		resumeFunc:               resume.Run,
	}
}

// Run listens on socketPath and polls the cloud provider for interruption
// signals until ctx is done.
func (d *Daemon) Run(ctx context.Context, socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	_ = os.Remove(socketPath) // stale socket from a previous run

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	defer ln.Close()

	go d.provider.Run(ctx, d.handleInterruption)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("accept: %v", err)
				continue
			}
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
		return d.handleRegister(req)
	case ipc.ActionList:
		return d.handleList()
	case ipc.ActionShow:
		return d.handleShow(req)
	case ipc.ActionPause:
		return d.handlePause(req)
	case ipc.ActionResume:
		return d.handleResume(req)
	case ipc.ActionCancel:
		return d.handleCancel(req)
	case ipc.ActionComplete:
		return d.handleComplete(req)
	case ipc.ActionPrune:
		return d.handlePrune(req)
	default:
		return ipc.Response{OK: false, Error: fmt.Sprintf("unknown action %q", req.Action)}
	}
}

func (d *Daemon) handleRegister(req ipc.Request) ipc.Response {
	if req.Job == nil {
		return ipc.Response{OK: false, Error: "missing job payload"}
	}
	if d.interrupted.Load() {
		return ipc.Response{OK: false, Error: "daemon has already received an interruption signal; refusing new registrations"}
	}

	id := idgen.New()
	dir := req.Job.CheckpointDir
	if dir == "" {
		dir = filepath.Join(d.checkpointBaseDir, id)
	}
	now := time.Now().UTC()
	j := store.Job{
		ID:             id,
		PID:            req.Job.PID,
		PGID:           req.Job.PGID,
		CheckpointDir:  dir,
		Command:        req.Job.Command,
		WorkDir:        req.Job.WorkDir,
		HookCheckpoint: req.Job.HookCheckpoint,
		HookResume:     req.Job.HookResume,
		Status:         store.StatusRunning,
		RegisteredAt:   now,
		UpdatedAt:      now,
	}
	if err := d.store.Insert(j); err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}
	return ipc.Response{OK: true, JobID: id}
}

func (d *Daemon) handleList() ipc.Response {
	jobs, err := d.store.List()
	if err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}
	return ipc.Response{OK: true, Jobs: jobs}
}

func (d *Daemon) handleShow(req ipc.Request) ipc.Response {
	if req.JobID == "" {
		return ipc.Response{OK: false, Error: "missing job_id"}
	}
	j, err := d.store.Get(req.JobID)
	if err != nil {
		return ipc.Response{OK: false, Error: fmt.Sprintf("no such job: %s", req.JobID)}
	}
	return ipc.Response{OK: true, Job: &j}
}

func (d *Daemon) handlePause(req ipc.Request) ipc.Response {
	if req.JobID == "" {
		return ipc.Response{OK: false, Error: "missing job_id"}
	}
	if d.interrupted.Load() {
		return ipc.Response{OK: false, Error: "daemon has already received an interruption signal; automatic checkpointing is in progress, refusing manual pause"}
	}
	j, err := d.store.Get(req.JobID)
	if err != nil {
		return ipc.Response{OK: false, Error: fmt.Sprintf("no such job: %s", req.JobID)}
	}
	if j.Status != store.StatusRunning {
		return ipc.Response{OK: false, Error: fmt.Sprintf("refusing to pause job %s: status is %s, not %s", req.JobID, j.Status, store.StatusRunning)}
	}
	if err := d.checkpointJob(context.Background(), j); err != nil {
		return ipc.Response{OK: false, Error: fmt.Sprintf("pause job %s: %v", req.JobID, err)}
	}
	return ipc.Response{OK: true, JobID: req.JobID, Message: "checkpoint created"}
}

func (d *Daemon) handleResume(req ipc.Request) ipc.Response {
	if req.JobID == "" {
		return ipc.Response{OK: false, Error: "missing job_id"}
	}
	j, err := d.store.Get(req.JobID)
	if err != nil {
		return ipc.Response{OK: false, Error: fmt.Sprintf("no such job: %s", req.JobID)}
	}
	if j.Status != store.StatusCheckpointCreated && j.Status != store.StatusRestoreFailed {
		return ipc.Response{OK: false, Error: fmt.Sprintf("refusing to resume job %s: status is %s, not %s or %s", req.JobID, j.Status, store.StatusCheckpointCreated, store.StatusRestoreFailed)}
	}
	if err := d.resumeJob(context.Background(), j); err != nil {
		return ipc.Response{OK: false, Error: fmt.Sprintf("resume job %s: %v", req.JobID, err)}
	}
	updated, err := d.store.Get(req.JobID)
	if err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}
	return ipc.Response{OK: true, JobID: req.JobID, Message: fmt.Sprintf("resumed as pid %d", updated.PID)}
}

func (d *Daemon) handleCancel(req ipc.Request) ipc.Response {
	if req.JobID == "" {
		return ipc.Response{OK: false, Error: "missing job_id"}
	}
	j, err := d.store.Get(req.JobID)
	if err != nil {
		return ipc.Response{OK: false, Error: fmt.Sprintf("no such job: %s", req.JobID)}
	}
	if j.Status == store.StatusRunning {
		if err := procsignal.KillGroup(j.PGID, syscall.SIGTERM); err != nil {
			return ipc.Response{OK: false, Error: fmt.Sprintf("signal job %s: %v", req.JobID, err)}
		}
	}
	if err := d.store.UpdateStatus(req.JobID, store.StatusCanceled, ""); err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}
	return ipc.Response{OK: true, JobID: req.JobID}
}

func (d *Daemon) handleComplete(req ipc.Request) ipc.Response {
	if req.JobID == "" {
		return ipc.Response{OK: false, Error: "missing job_id"}
	}
	j, err := d.store.Get(req.JobID)
	if err != nil {
		return ipc.Response{OK: false, Error: fmt.Sprintf("no such job: %s", req.JobID)}
	}
	if j.Status != store.StatusRunning {
		// Race with the checkpoint subsystem (legacy ADR-3): the job already
		// moved past RUNNING by the time this completion report arrived.
		// Not an error -- the checkpoint/cancel path already owns it.
		return ipc.Response{OK: true, JobID: req.JobID, Message: "job already past RUNNING, ignoring stale completion report"}
	}
	if req.ExitCode == 0 {
		if err := d.store.UpdateStatus(req.JobID, store.StatusCompleted, ""); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, JobID: req.JobID}
	}
	reason := req.ErrMsg
	if reason == "" {
		reason = fmt.Sprintf("exited with code %d", req.ExitCode)
	}
	if err := d.store.UpdateStatus(req.JobID, store.StatusFailed, reason); err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}
	return ipc.Response{OK: true, JobID: req.JobID}
}

func (d *Daemon) handlePrune(req ipc.Request) ipc.Response {
	if req.JobID != "" {
		j, err := d.store.Get(req.JobID)
		if err != nil {
			return ipc.Response{OK: false, Error: fmt.Sprintf("no such job: %s", req.JobID)}
		}
		if !isTerminal(j.Status) {
			return ipc.Response{OK: false, Error: fmt.Sprintf("job %s is not terminal (status %s), refusing to prune", req.JobID, j.Status)}
		}
		if err := os.RemoveAll(j.CheckpointDir); err != nil {
			return ipc.Response{OK: false, Error: fmt.Sprintf("prune job %s: %v", req.JobID, err)}
		}
		return ipc.Response{OK: true, JobID: req.JobID, Message: "pruned"}
	}

	jobs, err := d.store.ListTerminal()
	if err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}
	var failures []string
	pruned := 0
	for _, j := range jobs {
		if err := os.RemoveAll(j.CheckpointDir); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", j.ID, err))
			continue
		}
		pruned++
	}
	return ipc.Response{OK: true, Message: fmt.Sprintf("pruned %d job(s)", pruned), Failures: failures}
}

func isTerminal(s store.Status) bool {
	return s == store.StatusFailed || s == store.StatusCanceled || s == store.StatusCompleted
}

// checkpointJob runs the Checkpoint operation from SPEC-daemon.md, shared by
// a manual Pause and the interruption fan-out below.
func (d *Daemon) checkpointJob(ctx context.Context, j store.Job) error {
	if err := d.store.UpdateStatus(j.ID, store.StatusCheckpointInProgress, ""); err != nil {
		log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, err)
	}
	d.logDaemon("checkpoint_started", j.ID, "ok", "")

	if err := d.checkpointFunc(ctx, j.CheckpointDir, j); err != nil {
		if serr := d.store.UpdateStatus(j.ID, store.StatusCheckpointCreationFailed, err.Error()); serr != nil {
			log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, serr)
		}
		d.logDaemon("checkpoint_failed", j.ID, "error", err.Error())
		return err
	}

	if err := d.store.UpdateStatus(j.ID, store.StatusCheckpointCreated, ""); err != nil {
		log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, err)
	}
	d.logDaemon("checkpoint_succeeded", j.ID, "ok", "")
	return nil
}

// resumeJob runs the Resume operation from SPEC-daemon.md.
func (d *Daemon) resumeJob(ctx context.Context, j store.Job) error {
	if err := d.store.UpdateStatus(j.ID, store.StatusRestorePending, ""); err != nil {
		log.Printf("resume: job %s: failed to update status: %v", j.ID, err)
	}
	d.logDaemon("restore_started", j.ID, "ok", "")

	pid, err := d.resumeFunc(ctx, j.CheckpointDir, j.HookResume, j.ID)
	if err != nil {
		if serr := d.store.UpdateStatus(j.ID, store.StatusRestoreFailed, err.Error()); serr != nil {
			log.Printf("resume: job %s: failed to update status: %v", j.ID, serr)
		}
		d.logDaemon("restore_failed", j.ID, "error", err.Error())
		return err
	}

	if err := d.store.UpdatePID(j.ID, pid, pid); err != nil {
		log.Printf("resume: job %s: failed to update pid: %v", j.ID, err)
	}
	d.logDaemon("restore_succeeded", j.ID, "ok", fmt.Sprintf("pid=%d", pid))
	return nil
}

// handleInterruption checkpoints every tracked RUNNING job in response to a
// cloud provider signal, up to maxConcurrentCheckpoints at once. Runs
// independently of any client connection.
func (d *Daemon) handleInterruption(trigger string) {
	// Latched immediately, before anything else: refusing new registrations
	// matters most in exactly the window this function is about to spend
	// checkpointing, not after it returns.
	d.interrupted.Store(true)
	d.logDaemon("interruption_detected", "", "ok", trigger)

	jobs, err := d.store.List()
	if err != nil {
		log.Printf("checkpoint: failed to list jobs (trigger=%s): %v", trigger, err)
		return
	}

	var runnable []store.Job
	for _, j := range jobs {
		if j.Status == store.StatusRunning {
			runnable = append(runnable, j)
		}
	}
	if len(runnable) == 0 {
		log.Printf("checkpoint: %s received, no tracked jobs", trigger)
		return
	}

	log.Printf("checkpoint: %s received, checkpointing %d job(s) (max %d concurrent)", trigger, len(runnable), d.maxConcurrentCheckpoints)

	var wg sync.WaitGroup
	sem := make(chan struct{}, d.maxConcurrentCheckpoints)
	for _, j := range runnable {
		wg.Add(1)
		sem <- struct{}{}
		go func(j store.Job) {
			defer wg.Done()
			defer func() { <-sem }()
			_ = d.checkpointJob(context.Background(), j) // already logged internally on failure
		}(j)
	}
	wg.Wait()

	log.Printf("checkpoint: %s complete: %d job(s) processed", trigger, len(runnable))
}

func (d *Daemon) logDaemon(action, jobID, outcome, detail string) {
	if d.audit == nil {
		return
	}
	if err := d.audit.Log(auditlog.Entry{Component: "daemon", Action: action, JobID: jobID, Outcome: outcome, Detail: detail}); err != nil {
		log.Printf("audit log: %v", err)
	}
}
