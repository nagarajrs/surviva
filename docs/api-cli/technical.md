# `surviva` CLI reference

Single binary, five subcommands: `run`, `daemon`, `list`, `stop`, `restore`.
Flags use Go's standard `flag` package (single-dash, `-h` per subcommand for a
live list).

```
surviva - checkpoint/restore protection for Spot interruptions

Usage:
  surviva run [flags] -- <command> [args...]   Run and track a command
  surviva daemon [flags]                       Run the surviva daemon
  surviva list [flags]                         List jobs tracked by the daemon
  surviva stop [flags] <job-id>                Terminate and untrack a RUNNING job
  surviva restore [flags] <job-id>             Restore a checkpointed job on this instance

Run 'surviva <command> -h' for flags on a specific subcommand.
```

Exit codes, all subcommands: `0` success; `1` general failure (also used to
propagate the wrapped command's own nonzero exit code in `run`); `2` usage
error (missing job-id, wrong argument count, etc.).

Default paths and env var overrides (shared across subcommands, resolved in
`internal/ipc/protocol.go`):

| Setting | Env var override | Linux default | Windows default (dev only) |
|---|---|---|---|
| daemon socket | `SURVIVA_SOCKET` | `/var/run/surviva/surviva.sock` | `%TEMP%\surviva.sock` |
| sqlite job db | `SURVIVA_DB_PATH` | `/var/lib/surviva/jobs.db` | `%TEMP%\surviva.db` |
| checkpoint dir | `SURVIVA_CHECKPOINT_DIR` | `/var/lib/surviva/checkpoints` | `%TEMP%\surviva-checkpoints` |
| IMDS base URL | `SURVIVA_IMDS_ENDPOINT` | `http://169.254.169.254` | same (used to point at a mock server for testing) |

This is a Linux-only production tool; Windows paths exist only so the code
builds/runs for local development.

---

## `surviva run [flags] -- <command> [args...]`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-priority` | int | `0` | Checkpoint priority: higher runs first when time is short. |
| `-hook-checkpoint` | string | `""` | Path to a custom checkpoint script, used instead of `criu dump`. |
| `-hook-resume` | string | `""` | Path to a custom resume script, used instead of `criu restore`. |
| `-socket` | string | `DefaultSocketPath()` | Daemon socket to register with. |

Behavior:
1. Starts `<command>` with `SysProcAttr{Setsid: true}` on Linux (its own
   session, so CRIU never needs to reattach to an external shell/tty — see
   `internal/procattr`).
2. Before `Start()`, closes close-on-exec any fd above 2 the `surviva run`
   process itself inherited (`internal/fdguard`) — an inherited pty or socket
   fd would otherwise leak into the child and can silently break `criu dump`.
3. Registers the child (PID, PGID = PID, full argv, cwd, hook paths, priority)
   with the daemon over the Unix socket (`ipc.Client.Register`).
4. Blocks in `cmd.Wait()`, exactly like `time`.
5. On normal exit, calls `Deregister` — but the daemon only actually deletes
   the row if the job's status is still `RUNNING` (see race note below) —
   then exits with the child's own exit code (extracted via
   `errors.As(waitErr, &exec.ExitError)`).
6. If the daemon is unreachable at registration time, prints a warning to
   stderr and proceeds anyway — the command still runs, just unprotected.

Race note: if the daemon starts checkpointing the job at the same instant
`surviva run` sees `cmd.Wait()` return (because `criu dump` stopped the
process), `Deregister` and the daemon's own status transition race. The
daemon resolves this by only honoring `Deregister` while status is still
`RUNNING`; once it's moved past that (`CHECKPOINT_IN_PROGRESS` etc.) the row
survives for the checkpoint subsystem.

```
surviva run --priority 5 --hook-checkpoint /opt/app/ckpt.sh --hook-resume /opt/app/resume.sh -- ./pipeline --input data.bam
```

---

## `surviva daemon [flags]`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-socket` | string | `DefaultSocketPath()` | Path to listen on. |
| `-db` | string | `DefaultDBPath()` | SQLite job database path. |
| `-checkpoint-dir` | string | `DefaultCheckpointDir()` | Local dir for checkpoint images (also the EBS mount point in EBS mode). |
| `-imds` | bool | `true` | Poll IMDS for Spot rebalance/interruption signals. Disable only for local dev off-EC2. |
| `-max-concurrent-checkpoints` | int | `runtime.NumCPU()` | Bound on simultaneous checkpoint jobs; large checkpoints can saturate disk/CPU if set too high. |
| `-s3-bucket` | string | `""` | S3 bucket for checkpoint pushes. Requires `-dynamodb-table`. Mutually exclusive with `-ebs-volume-id`. |
| `-s3-prefix` | string | `"checkpoints"` | Key prefix under `-s3-bucket`. |
| `-ebs-volume-id` | string | `""` | EBS volume id backing `-checkpoint-dir`. Requires `-dynamodb-table`. Mutually exclusive with `-s3-bucket`. Validated at startup (see below). |
| `-dynamodb-table` | string | `""` | Table recording checkpoint status. Required by either storage flag. |
| `-aws-region` | string | `""` | AWS region override; empty uses normal SDK resolution. |

Config validation (`internal/daemon.New`), exact error strings, checked before
any AWS calls:
- `"configure at most one of -s3-bucket or -ebs-volume-id, not both"`
- `"-dynamodb-table is required when -s3-bucket or -ebs-volume-id is set"`
- `"-dynamodb-table requires either -s3-bucket or -ebs-volume-id to be set"`

If `-ebs-volume-id` is set, `remote.ValidateEBSVolume` runs at startup via
`ec2:DescribeVolumes` and the daemon **refuses to start** if any of:
volume not found; not attached to any instance; attached to a different
instance than this one (compared against IMDS instance-id when `-imds`
is enabled); or `DeleteOnTermination=true` on that attachment. Error wraps as
`"EBS checkpoint volume validation failed: <reason>"`.

Job status values (`internal/job.Status`, stored in sqlite and, when remote
storage is enabled, mirrored to DynamoDB by `internal/remote.StatusStore`):
`RUNNING`, `CHECKPOINT_IN_PROGRESS`, `CHECKPOINT_COMPLETE`,
`CHECKPOINT_INCOMPLETE`, `RESTORING`, `RESTORED`, `FAILED`.

```
# S3 mode
surviva daemon -s3-bucket my-checkpoints -dynamodb-table surviva-jobs -aws-region us-east-1

# EBS mode (checkpoint-dir must already be the volume's mount point)
surviva daemon -checkpoint-dir /mnt/surviva-ckpt -ebs-volume-id vol-0123456789abcdef0 \
  -dynamodb-table surviva-jobs -aws-region us-east-1

# Local-disk-only (no durable remote store — phase 1-4 behavior)
surviva daemon
```

See `../deployment/technical.md` for how these flags are normally supplied via
systemd + instance user-data rather than typed by hand.

---

## `surviva list [flags]`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-socket` | string | `DefaultSocketPath()` | Daemon socket to query. |
| `-json` | bool | `false` | Print raw JSON (`internal/job.Job` array) instead of a table. |

Table columns: `JOB ID`, `PID`, `STATUS`, `PRIORITY`, `COMMAND`. With no
tracked jobs, prints `no tracked jobs` and exits 0.

```
surviva list
surviva list -json | jq '.[] | select(.status == "FAILED")'
```

---

## `surviva stop [flags] <job-id>`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-socket` | string | `DefaultSocketPath()` | Daemon socket to talk to. |

Sends `SIGTERM` to the job's whole process group and removes it from
tracking — the daemon does this directly, so it works even if the `surviva
run` that registered the job is no longer around to deregister it itself
(killed directly, a dropped session, or anything else that orphaned the
record). Deliberately **not the same code path** as a normal exit: refuses
(exit 1) once the job's status is anything other than `RUNNING` — a
checkpoint in progress or already durable is owned by the checkpoint/restore
subsystem from that point on, and ripping the record out from under it would
be actively harmful, not just redundant. Idempotent: stopping an
already-gone job id is a no-op success (exit 0), and a process group that no
longer exists is treated the same way — there's simply nothing left to
signal.

```
surviva stop 6a81d8ec-6d1a-4d99-bd8e-259b44ebab53
```

---

## `surviva restore [flags] <job-id>`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-dynamodb-table` | string | `""` (required) | Table to read the job's status record from. |
| `-aws-region` | string | `""` | AWS region override. |
| `-checkpoint-dir` | string | `DefaultCheckpointDir()` | Local staging dir (S3 mode) — ignored for EBS mode, see below. |
| `-socket` | string | `DefaultSocketPath()` | Daemon socket to re-register the resumed process with. |
| `-timeout` | duration | `5m` | Overall time budget for the whole restore. |

Sequence (`cmd/surviva/restore.go`):
1. `remote.StatusStore.Get(jobID)`. Not found → exit 1. Found but
   `status != CHECKPOINT_COMPLETE` → refuses, exact message:
   `"surviva restore: refusing to restore job <id>: status is <status>, not CHECKPOINT_COMPLETE"`.
   This is the hard safety gate: nothing but a confirmed-complete checkpoint
   is ever restored from.
2. `statusStore.Restoring(jobID)` — best-effort, logged if it fails.
3. Fetch the checkpoint data, branching on `record.StorageType`:
   - `"s3"`: `remote.PullFromURI` downloads `record.S3URI` via `s3:GetObject`
     and streams it through gunzip+untar into
     `<checkpoint-dir>/<job-id>` (path-traversal-guarded on entry names).
     Requires the instance role to have `s3:GetObject` on that bucket — the
     **same** role used for the daemon's `s3:PutObject`, since original and
     replacement instances share one instance profile.
   - `"ebs"`: `internal/ebsmount.MountForRestore` resolves
     `/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_<volumeID with first
     dash removed>` (AWS's documented Nitro/NVMe udev convention), waits up
     to 30s for it to appear, and mounts it at `filepath.Dir(record.EBSPath)`
     unless something's already mounted there. `localDir` becomes
     `record.EBSPath` directly (the `-checkpoint-dir` flag is not used in
     this branch). The orchestrator only ever attaches the raw volume —
     nothing mounts it automatically.
   - anything else: fails with `"job <id> has unknown storage_type %q"`.
4. `internal/resume.Run(checkpointDir, record.HookResume, jobID)`: if
   `HookResume` is set, execs it as `<script> <job-id> <checkpoint-dir>` and
   parses its stdout as a single integer PID (same contract as
   hook-checkpoint, in reverse); otherwise `criu restore` (with
   `--restore-detached --pidfile <dir>/restore.pid`), returning the resumed
   PID.
5. Re-registers with the local daemon via `ipc.Client.Register`, carrying
   forward `record.Command`, `WorkDir`, `HookCheckpoint`, `HookResume`,
   `Priority` — so the resumed process is protected again on this
   (potentially also-interruptible) instance. Failure to register is logged
   as a warning, not fatal — the process is still running, just unprotected.
6. `statusStore.Restored(jobID)`.

On failure at step 2 onward, `fail()` calls
`statusStore.RestoreFailed(jobID, err.Error())` (sets status `FAILED` with a
`failure_reason`) and returns exit 1. Log line format:
`"surviva restore: job <id> FAILED: <error>"`.

This is exactly the command the Step Functions orchestrator sends via SSM —
see `../deployment/technical.md` for the orchestrator's exact
`restore_command_prefix` construction
(`surviva restore --dynamodb-table <table> --aws-region <region> <job-id>`)
and `../architecture/technical.md` for where this fits in the full recovery
flow.

```
surviva restore --dynamodb-table surviva-jobs --aws-region us-east-1 3798b53e-8986-4ab0-b69e-743431278e69
```
