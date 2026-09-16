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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"surviva/internal/checkpoint"
	"surviva/internal/idgen"
	"surviva/internal/imds"
	"surviva/internal/ipc"
	"surviva/internal/job"
	"surviva/internal/procsignal"
	"surviva/internal/remote"
	"surviva/internal/resume"
	"surviva/internal/store"
)

// checkpointTimeout bounds a single job's checkpoint attempt, all within
// the Spot interruption notice's ~2-minute window.
const checkpointTimeout = 100 * time.Second

// resumeTimeout bounds a single ActionResume attempt. Unlike checkpointing,
// resuming isn't racing an instance reclamation deadline, so it gets a more
// generous budget.
const resumeTimeout = 2 * time.Minute

// Config configures a Daemon.
type Config struct {
	SocketPath    string
	DBPath        string
	CheckpointDir string
	// EnableIMDS controls whether the daemon polls IMDS for Spot signals.
	// Disabling it is only useful off-EC2 (local development).
	EnableIMDS bool
	// MaxConcurrentCheckpoints bounds how many jobs are checkpointed at
	// once. Dumping many large process trees fully in parallel can
	// saturate disk/CPU badly enough that none finish inside the
	// interruption window, so concurrency is capped rather than
	// unbounded. Defaults to 1 (sequential) if <= 0.
	MaxConcurrentCheckpoints int
	// S3Bucket and DynamoDBTable enable durable remote storage via S3: a
	// local checkpoint is only considered complete once it has also been
	// pushed to S3 and recorded in DynamoDB. Both must be set together.
	S3Bucket string
	S3Prefix string
	// EBSVolumeID enables durable remote storage via EBS instead of S3:
	// CheckpointDir is expected to already be on this volume, which must
	// be attached to this instance with DeleteOnTermination=false (checked
	// at startup — surviva refuses to start otherwise, since a volume that
	// would be deleted along with the instance defeats the entire point).
	// Requires DynamoDBTable too; mutually exclusive with S3Bucket.
	EBSVolumeID string
	// DynamoDBTable records checkpoint durability status independent of
	// this instance's own disks. Required by both S3Bucket and
	// EBSVolumeID; leaving all three unset keeps checkpoints
	// local-disk-only (as in earlier phases).
	DynamoDBTable string
	// AWSRegion overrides the AWS SDK's default region resolution.
	// Leave empty to use the environment/instance's normal region config.
	AWSRegion string
}

// Daemon owns the job store, the socket clients connect to, and (when
// enabled) the IMDS poller that triggers checkpointing.
type Daemon struct {
	store                    *store.Store
	socketPath               string
	checkpointDir            string
	poller                   *imds.Poller
	imdsClient               *imds.Client
	maxConcurrentCheckpoints int
	statusStore              *remote.StatusStore
	objectStore              *remote.ObjectStore
	ebsVolumeID              string
	// interrupted latches true the moment a rebalance recommendation or
	// interruption notice is ever received (see handleInterruption). A job
	// registered after that point has no realistic path to being
	// checkpointed: each IMDS signal only ever triggers handleInterruption
	// once per daemon run (see imds.Poller), so a newly-tracked job would
	// simply be silently lost when the instance is actually reclaimed,
	// despite `surviva run` having reported it as protected.
	interrupted atomic.Bool
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

	maxConcurrent := cfg.MaxConcurrentCheckpoints
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	d := &Daemon{
		store:                    st,
		socketPath:               cfg.SocketPath,
		checkpointDir:            cfg.CheckpointDir,
		maxConcurrentCheckpoints: maxConcurrent,
	}

	if cfg.EnableIMDS {
		d.imdsClient = imds.NewClient()
		d.poller = imds.NewPoller(d.imdsClient)
		d.poller.OnRebalanceRecommendation = func(*imds.RebalanceRecommendation) {
			d.handleInterruption("rebalance-recommendation")
		}
		d.poller.OnInterruptionNotice = func(*imds.InstanceAction) {
			d.handleInterruption("interruption-notice")
		}
	}

	hasS3 := cfg.S3Bucket != ""
	hasEBS := cfg.EBSVolumeID != ""
	hasTable := cfg.DynamoDBTable != ""
	switch {
	case hasS3 && hasEBS:
		return nil, fmt.Errorf("configure at most one of -s3-bucket or -ebs-volume-id, not both")
	case (hasS3 || hasEBS) && !hasTable:
		return nil, fmt.Errorf("-dynamodb-table is required when -s3-bucket or -ebs-volume-id is set")
	case hasTable && !hasS3 && !hasEBS:
		return nil, fmt.Errorf("-dynamodb-table requires either -s3-bucket or -ebs-volume-id to be set")
	}

	if hasTable {
		awsCfgOpts := []func(*awsconfig.LoadOptions) error{}
		if cfg.AWSRegion != "" {
			awsCfgOpts = append(awsCfgOpts, awsconfig.WithRegion(cfg.AWSRegion))
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsCfgOpts...)
		if err != nil {
			return nil, fmt.Errorf("load AWS config: %w", err)
		}
		d.statusStore = remote.NewStatusStore(awsCfg, cfg.DynamoDBTable)

		if hasS3 {
			d.objectStore = remote.NewObjectStore(awsCfg, cfg.S3Bucket, cfg.S3Prefix)
		}

		if hasEBS {
			var instanceID string
			if d.imdsClient != nil {
				instanceID, _ = d.imdsClient.InstanceID(context.Background())
			}
			if err := remote.ValidateEBSVolume(context.Background(), awsCfg, cfg.EBSVolumeID, instanceID); err != nil {
				return nil, fmt.Errorf("EBS checkpoint volume validation failed: %w", err)
			}
			d.ebsVolumeID = cfg.EBSVolumeID
		}
	}

	return d, nil
}

// remoteEnabled reports whether checkpoints are tracked in durable remote
// storage (DynamoDB, plus S3 or a validated EBS volume) rather than being
// considered complete once they're only on local disk.
func (d *Daemon) remoteEnabled() bool {
	return d.statusStore != nil
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
		if d.interrupted.Load() {
			// A rebalance recommendation or interruption notice has already
			// fired this daemon run -- handleInterruption only ever runs
			// once per signal type, so a job registered from this point on
			// would never actually get checkpointed before the instance is
			// reclaimed, despite `surviva run` reporting it as tracked.
			return ipc.Response{OK: false, Error: "daemon has already received a Spot interruption/rebalance signal; refusing new job registrations"}
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

	case ipc.ActionStop:
		if req.JobID == "" {
			return ipc.Response{OK: false, Error: "missing job_id"}
		}
		existing, err := d.store.Get(req.JobID)
		if err != nil {
			// Unlike deregister, an unknown id here is reported as a real
			// error, not silently treated as success: `stop` is a one-off
			// command a person typed, and a wrong or mistyped job id
			// (a PID, a truncated id, one that never existed) silently
			// "succeeding" is actively misleading -- it looks exactly like
			// the job really was stopped when nothing happened at all.
			return ipc.Response{OK: false, Error: fmt.Sprintf("no such job: %s", req.JobID)}
		}
		// Unlike deregister (which only ever races a job's own `surviva run`
		// wrapper over a job that's still RUNNING), stop is a deliberate,
		// unconditional cancellation -- refuse it once the checkpoint
		// subsystem owns the record (mid-checkpoint or already durable),
		// since ripping that out from under a restore in flight, or a
		// completed checkpoint someone might still want to restore from,
		// would be actively harmful rather than just a no-op.
		if existing.Status != job.StatusRunning {
			return ipc.Response{OK: false, Error: fmt.Sprintf("refusing to stop job %s: status is %s, not %s", req.JobID, existing.Status, job.StatusRunning)}
		}
		// KillGroup treats an already-dead process group (e.g. it exited
		// right as this request arrived) as success, not an error.
		if err := procsignal.KillGroup(existing.PGID, syscall.SIGTERM); err != nil {
			return ipc.Response{OK: false, Error: fmt.Sprintf("signal job %s: %v", req.JobID, err)}
		}
		if err := d.store.Delete(req.JobID); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		log.Printf("stopped job %s (pgid %d)", req.JobID, existing.PGID)
		return ipc.Response{OK: true}

	case ipc.ActionPause:
		if req.JobID == "" {
			return ipc.Response{OK: false, Error: "missing job_id"}
		}
		if d.interrupted.Load() {
			// handleInterruption may already be checkpointing this exact job
			// right now (or is about to) -- letting a manual pause run
			// concurrently would mean two criu dumps racing the same pid.
			return ipc.Response{OK: false, Error: "daemon has already received a Spot interruption/rebalance signal; automatic checkpointing is in progress, refusing manual pause"}
		}
		existing, err := d.store.Get(req.JobID)
		if err != nil {
			return ipc.Response{OK: false, Error: fmt.Sprintf("no such job: %s", req.JobID)}
		}
		if existing.Status != job.StatusRunning {
			return ipc.Response{OK: false, Error: fmt.Sprintf("refusing to pause job %s: status is %s, not %s", req.JobID, existing.Status, job.StatusRunning)}
		}
		if err := d.checkpointJob(existing, req.Local); err != nil {
			return ipc.Response{OK: false, Error: fmt.Sprintf("pause job %s: %v", req.JobID, err)}
		}
		msg := fmt.Sprintf("checkpoint stored locally at %s", checkpoint.Dir(d.checkpointDir, req.JobID))
		switch {
		case req.Local:
			// msg already says "stored locally"
		case d.ebsVolumeID != "":
			msg = fmt.Sprintf("checkpoint durable on EBS volume %s", d.ebsVolumeID)
		case d.remoteEnabled():
			msg = "checkpoint pushed to S3"
		}
		log.Printf("paused job %s: %s", req.JobID, msg)
		return ipc.Response{OK: true, JobID: req.JobID, Message: msg}

	case ipc.ActionResume:
		if req.JobID == "" {
			return ipc.Response{OK: false, Error: "missing job_id"}
		}
		existing, err := d.store.Get(req.JobID)
		if err != nil {
			return ipc.Response{OK: false, Error: fmt.Sprintf("no such job: %s", req.JobID)}
		}
		if existing.Status != job.StatusCheckpointComplete {
			return ipc.Response{OK: false, Error: fmt.Sprintf("refusing to resume job %s: status is %s, not %s", req.JobID, existing.Status, job.StatusCheckpointComplete)}
		}
		dir := checkpoint.Dir(d.checkpointDir, existing.ID)
		if _, err := os.Stat(dir); err != nil {
			return ipc.Response{OK: false, Error: fmt.Sprintf("no local checkpoint for job %s at %s -- use `surviva restore` on the target instance instead", req.JobID, dir)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), resumeTimeout)
		defer cancel()
		pid, err := resume.Run(ctx, dir, existing.HookResume, existing.ID)
		if err != nil {
			return ipc.Response{OK: false, Error: fmt.Sprintf("resume job %s: %v", req.JobID, err)}
		}
		if err := d.store.UpdateResumed(existing.ID, pid, pid); err != nil {
			log.Printf("resume: job %s: failed to update store after resuming as pid %d: %v", req.JobID, pid, err)
		}
		log.Printf("resumed job %s as pid %d", req.JobID, pid)
		return ipc.Response{OK: true, JobID: existing.ID, Message: fmt.Sprintf("resumed as pid %d", pid)}

	default:
		return ipc.Response{OK: false, Error: fmt.Sprintf("unknown action %q", req.Action)}
	}
}

// handleInterruption checkpoints every tracked job in response to an IMDS
// signal, up to maxConcurrentCheckpoints at once. It is called at most once
// per signal type per daemon run (see imds.Poller) but a rebalance
// recommendation followed by an actual interruption notice would call it
// twice; jobs already past StatusRunning are skipped so the second call is
// a no-op.
//
// store.List returns jobs highest-priority first, and that order is
// preserved when handing work to the bounded pool of workers below: with
// concurrency capped below the number of runnable jobs, higher-priority
// jobs claim a worker slot first and lower-priority ones queue behind them,
// so priority determines who gets checkpointed first if time runs out
// mid-way rather than which goroutine happens to be scheduled first.
func (d *Daemon) handleInterruption(trigger string) {
	// Latched immediately, before anything else: refusing new registrations
	// (see ActionRegister) matters most in exactly the window this function
	// is about to spend checkpointing, not after it returns.
	d.interrupted.Store(true)

	jobs, err := d.store.List()
	if err != nil {
		log.Printf("checkpoint: failed to list jobs (trigger=%s): %v", trigger, err)
		return
	}

	runnable := jobs[:0]
	for _, j := range jobs {
		if j.Status == job.StatusRunning {
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
		go func(j job.Job) {
			defer wg.Done()
			defer func() { <-sem }()
			_ = d.checkpointJob(j, false) // already logged internally on failure
		}(j)
	}
	wg.Wait()

	log.Printf("checkpoint: %s complete: %d job(s) processed", trigger, len(runnable))
}

// checkpointJob checkpoints j and returns the terminal error, if any (also
// logged internally so handleInterruption's fire-and-forget callers don't
// need to inspect it). forceLocal skips the EBS/S3 branches below even if
// the daemon is otherwise configured for one of them, storing the checkpoint
// on local disk only for this one job -- used by a manual `surviva pause
// -local`.
func (d *Daemon) checkpointJob(j job.Job, forceLocal bool) error {
	if err := d.store.UpdateStatus(j.ID, job.StatusCheckpointInProgress); err != nil {
		log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()

	useEBS := !forceLocal && d.ebsVolumeID != ""
	useRemote := !forceLocal && d.remoteEnabled()

	// In EBS mode the local dump writes directly to the EBS-backed
	// checkpoint disk, so the dump itself is the durable step that could
	// be interrupted — write the IN_PROGRESS record before it starts, the
	// same way the S3 path writes one before its upload starts.
	if useEBS {
		if err := d.recordEBSStart(ctx, j); err != nil {
			log.Printf("checkpoint: job %s: %v", j.ID, err)
		}
	}

	if err := checkpoint.Run(ctx, d.checkpointDir, j); err != nil {
		log.Printf("checkpoint: job %s FAILED: %v", j.ID, err)
		if useEBS {
			if failErr := d.statusStore.Fail(ctx, j.ID, err.Error()); failErr != nil {
				log.Printf("checkpoint: job %s: failed to mark remote status incomplete: %v", j.ID, failErr)
			}
		}
		if serr := d.store.UpdateStatus(j.ID, job.StatusFailed); serr != nil {
			log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, serr)
		}
		return err
	}
	log.Printf("checkpoint: job %s dumped locally (%s)", j.ID, checkpoint.Dir(d.checkpointDir, j.ID))

	// "Complete" means durably stored, not just dumped to local disk.
	switch {
	case useEBS:
		dir := checkpoint.Dir(d.checkpointDir, j.ID)
		if err := d.statusStore.CompleteEBS(ctx, j.ID, d.ebsVolumeID, dir); err != nil {
			log.Printf("checkpoint: job %s: %v", j.ID, err)
			if serr := d.store.UpdateStatus(j.ID, job.StatusFailed); serr != nil {
				log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, serr)
			}
			return err
		}
		log.Printf("checkpoint: job %s durable on EBS volume %s", j.ID, d.ebsVolumeID)

	case useRemote: // S3 mode
		if err := d.pushRemote(ctx, j); err != nil {
			log.Printf("checkpoint: job %s: remote push FAILED: %v", j.ID, err)
			if serr := d.store.UpdateStatus(j.ID, job.StatusFailed); serr != nil {
				log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, serr)
			}
			return err
		}
	}

	log.Printf("checkpoint: job %s complete", j.ID)
	if err := d.store.UpdateStatus(j.ID, job.StatusCheckpointComplete); err != nil {
		log.Printf("checkpoint: job %s: failed to update status: %v", j.ID, err)
	}
	return nil
}

// baseRecord builds a DynamoDB record's common fields for a job about to be
// checkpointed to storageType ("s3" or "ebs"), tagging it with this
// instance's id/AZ when IMDS is available (best-effort — empty off-EC2).
func (d *Daemon) baseRecord(ctx context.Context, j job.Job, storageType string) remote.Record {
	method := "criu"
	if j.HookCheckpoint != "" {
		method = "hook"
	}

	var instanceID, az string
	if d.imdsClient != nil {
		instanceID, _ = d.imdsClient.InstanceID(ctx)
		az, _ = d.imdsClient.AvailabilityZone(ctx)
	}

	return remote.Record{
		JobID:            j.ID,
		InstanceID:       instanceID,
		AZ:               az,
		Command:          j.Command,
		WorkDir:          j.WorkDir,
		Priority:         j.Priority,
		HookCheckpoint:   j.HookCheckpoint,
		HookResume:       j.HookResume,
		CheckpointMethod: method,
		StorageType:      storageType,
		Status:           string(job.StatusCheckpointInProgress),
		UpdatedAt:        time.Now().UTC(),
	}
}

// recordEBSStart writes the initial DynamoDB record for a job about to be
// checkpointed to the configured EBS volume.
func (d *Daemon) recordEBSStart(ctx context.Context, j job.Job) error {
	rec := d.baseRecord(ctx, j, "ebs")
	rec.EBSVolumeID = d.ebsVolumeID
	if err := d.statusStore.Put(ctx, rec); err != nil {
		return fmt.Errorf("write initial remote status: %w", err)
	}
	return nil
}

// pushRemote uploads a locally-dumped checkpoint to S3 and records its
// status in DynamoDB. It writes an IN_PROGRESS record before the (possibly
// slow, possibly large) upload starts, so that if the instance dies
// mid-upload the table is left showing IN_PROGRESS/INCOMPLETE rather than
// nothing — an orchestrator must never restore from anything but a
// CHECKPOINT_COMPLETE record.
func (d *Daemon) pushRemote(ctx context.Context, j job.Job) error {
	rec := d.baseRecord(ctx, j, "s3")
	if err := d.statusStore.Put(ctx, rec); err != nil {
		return fmt.Errorf("write initial remote status: %w", err)
	}

	size, err := d.objectStore.PushDir(ctx, j.ID, checkpoint.Dir(d.checkpointDir, j.ID))
	if err != nil {
		if failErr := d.statusStore.Fail(ctx, j.ID, err.Error()); failErr != nil {
			log.Printf("checkpoint: job %s: failed to mark remote status incomplete: %v", j.ID, failErr)
		}
		return err
	}

	if err := d.statusStore.Complete(ctx, j.ID, d.objectStore.URI(j.ID), size); err != nil {
		return fmt.Errorf("mark remote status complete: %w", err)
	}
	log.Printf("checkpoint: job %s pushed to %s (%d bytes)", j.ID, d.objectStore.URI(j.ID), size)
	return nil
}
