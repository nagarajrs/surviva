# surviva: Component Reference (Technical)

A file/package-by-package map of the codebase. For overall data/control flow
see [`../architecture/technical.md`](../architecture/technical.md); for the
CLI flag reference see [`../api-cli/technical.md`](../api-cli/technical.md);
for the reasoning behind the non-obvious choices noted below see
[`../design-record/technical.md`](../design-record/technical.md).

## Contents

- [cmd/surviva — CLI entrypoints](#cmdsurviva--cli-entrypoints)
- [internal/job — shared data model](#internaljob--shared-data-model)
- [internal/store — local SQLite job store](#internalstore--local-sqlite-job-store)
- [internal/ipc — daemon protocol](#internalipc--daemon-protocol)
- [internal/idgen — ID generation](#internalidgen--id-generation)
- [internal/procattr — child process attributes](#internalprocattr--child-process-attributes)
- [internal/fdguard — inherited fd hygiene](#internalfdguard--inherited-fd-hygiene)
- [internal/sigset — shutdown signals](#internalsigset--shutdown-signals)
- [internal/imds — EC2 metadata polling](#internalimds--ec2-metadata-polling)
- [internal/criu — checkpoint/restore engine binding](#internalcriu--checkpointrestore-engine-binding)
- [internal/checkpoint — checkpoint orchestration](#internalcheckpoint--checkpoint-orchestration)
- [internal/resume — resume orchestration](#internalresume--resume-orchestration)
- [internal/remote — durable status + object storage](#internalremote--durable-status--object-storage)
- [internal/ebsmount — EBS volume discovery/mount](#internalebsmount--ebs-volume-discoverymount)
- [internal/daemon — the daemon](#internaldaemon--the-daemon)
- [infra/terraform — orchestrator infrastructure](#infraterraform--orchestrator-infrastructure)

---

## `cmd/surviva` — CLI entrypoints

| File | Responsibility |
|---|---|
| `main.go` | Subcommand dispatch (`run`/`daemon`/`list`/`stop`/`restore`/`version`), top-level usage text. |
| `run.go` | `surviva run` — wraps and tracks a command. |
| `daemon.go` | `surviva daemon` — flag parsing, builds `daemon.Config`. |
| `list.go` | `surviva list` — queries the daemon over IPC and prints a table or JSON. |
| `stop.go` | `surviva stop <job-id>` — asks the daemon to SIGTERM a job's process group and untrack it, whether or not whatever registered it (`surviva run`) is still around to do so itself. |
| `restore.go` | `surviva restore` — the restore flow driven by the orchestrator's SSM command; the `fail()` helper marks a job `FAILED` with a reason on any error, used at every failure point in the flow. |

`internal/version` holds the single `Version` constant (`MAJOR.MINOR`)
`surviva version`/`-v`/`--version` and the usage banner print.

## `internal/job` — shared data model

`job.go` defines `Job` (ID, PID, PGID, Command, WorkDir, HookCheckpoint,
HookResume, Priority, Status, RegisteredAt, UpdatedAt) and the `Status` enum:
`RUNNING`, `CHECKPOINT_IN_PROGRESS`, `CHECKPOINT_COMPLETE`,
`CHECKPOINT_INCOMPLETE`, `RESTORING`, `RESTORED`, `FAILED`. Shared by the
daemon, its SQLite store, and the CLI — this is the one place the status
vocabulary is defined.

## `internal/store` — local SQLite job store

`store.go` wraps `modernc.org/sqlite` (pure-Go, no cgo — chosen so the
Windows dev machine could build and cross-compile to Linux without a C
toolchain). CRUD surface: `Insert`, `Delete`, `Get`, `List` (ordered priority
`DESC`, `registered_at ASC` — this ordering is what makes the daemon's
priority-based checkpoint scheduling work with zero extra sorting logic),
`UpdateStatus`. This is purely local-instance bookkeeping; it has nothing to
do with the durable DynamoDB status store in `internal/remote`.

## `internal/ipc` — daemon protocol

- `protocol.go` — the request/response types for a newline-delimited JSON
  protocol over a Unix domain socket (`ActionRegister`, `ActionDeregister`,
  `ActionList`, `ActionStop`, `ActionPing`). Also owns the three environment-driven default
  paths: `DefaultSocketPath` (`SURVIVA_SOCKET`), `DefaultDBPath`
  (`SURVIVA_DB_PATH`), `DefaultCheckpointDir` (`SURVIVA_CHECKPOINT_DIR`) —
  each falls back to a Linux path under `/var/{run,lib}/surviva` or an OS
  temp-dir path on non-Linux (dev/test only).
- `client.go` — `Client.Register/Deregister/List/Ping`, used by `surviva run`
  (register/deregister) and `surviva restore` (register the resumed PID).

## `internal/idgen` — ID generation

`idgen.go` is a self-contained random UUIDv4 generator (crypto/rand-backed),
avoiding an external UUID dependency for the one thing it's needed for: job
IDs.

## `internal/procattr` — child process attributes

Build-tag split (`procattr_unix.go` / `procattr_other.go`). On Linux, a
tracked child is started as the leader of a **new session**
(`syscall.SysProcAttr{Setsid: true}`), not just a new process group. This is
deliberate: a session leader has no controlling terminal to reattach to,
which matters because CRIU restore on a *different* instance could never
reattach to the original shell/tty anyway.

## `internal/fdguard` — inherited fd hygiene

Build-tag split (`fdguard_linux.go` / `fdguard_other.go`). Before `surviva
run` starts the tracked child, it walks its own open file descriptors above
stderr (via `/proc/self/fd` on Linux) and sets `FD_CLOEXEC` on each one, so
none of them leak into the child across `exec`. This exists because of a real
bug found during development: a stray pty fd inherited from an enclosing
shell silently caused CRIU to refuse the dump (`"Task attached to shell
terminal"`), even though the tracked process had nothing to do with a
terminal itself.

## `internal/sigset` — shutdown signals

Build-tag split (`sigset_unix.go` / `sigset_other.go`). Returns the OS
signals that should trigger a clean daemon shutdown: `os.Interrupt` +
`syscall.SIGTERM` on Unix, just `os.Interrupt` elsewhere.

## `internal/imds` — EC2 metadata polling

- `client.go` — an IMDSv2 client: fetches and caches a token
  (`PUT /latest/api/token`), then issues token-authenticated `GET`s for
  `RebalanceRecommendation`, `SpotInstanceAction`, `InstanceID`,
  `AvailabilityZone`. `SURVIVA_IMDS_ENDPOINT` overrides the base URL —
  used throughout development to point at a mock IMDS server for testing
  off-EC2 (see [`../testing-strategy/technical.md`](../testing-strategy/technical.md)).
- `poller.go` — `Poller.Run` polls every 5 seconds. `OnRebalanceRecommendation`
  and `OnInterruptionNotice` are each edge-triggered (fire at most once per
  `Run` call); the poll loop exits entirely once the interruption notice
  fires, since that's the hard deadline and there's nothing further to watch
  for.

## `internal/criu` — checkpoint/restore engine binding

`criu.go` shells out to the `criu` binary on `PATH` rather than binding
libcriu — keeps the dependency surface to "is a compatible `criu` installed."
- `Dump(ctx, pid, imagesDir)` — no `--shell-job`, since tracked children are
  always session leaders (see `procattr` above), so there's no external shell
  context to reattach to.
- `Restore(ctx, imagesDir) (pid int, err error)` — uses `--restore-detached`
  (return once running rather than blocking for the tree's lifetime) and
  `--pidfile` to recover the resumed root task's PID.
- `Available()` — a `PATH` lookup, used to decide whether the CRIU path or a
  hook is viable.

## `internal/checkpoint` — checkpoint orchestration

`checkpoint.go`: `Run(ctx, baseDir, job)` runs the job's `--hook-checkpoint`
script if one was registered (contract: invoked as
`<script> <job-id> <output-dir>`, exit 0 = success), otherwise `criu.Dump`.
`Dir(baseDir, jobID)` computes the per-job checkpoint directory
(`<baseDir>/<jobID>`) — this exact convention is what lets EBS-mode restore
later derive the volume's mount point as `filepath.Dir(ebs_path)`.

## `internal/resume` — resume orchestration

`resume.go` is the mirror image of `checkpoint.go`: `Run(ctx, checkpointDir,
hookResume, jobID) (pid int, err error)` runs the hook-resume script
(contract: must print the resumed PID as a single integer line on stdout) or
`criu.Restore`.

## `internal/remote` — durable status + object storage

- `dynamo.go` — the `Record` struct mirrored into DynamoDB (`job_id`,
  `instance_id`, `az`, `command`, `work_dir`, `priority`,
  `hook_checkpoint`/`hook_resume` — carried forward so a restored job keeps
  its custom hooks — `checkpoint_method`, `storage_type`, `s3_uri`,
  `size_bytes`, `ebs_volume_id`, `ebs_path`, `status`, `failure_reason`,
  `updated_at`) and `StatusStore` methods: `Put` (initial write, before the
  risky step), `Complete` (S3), `CompleteEBS`, `Fail` (checkpoint failure →
  `CHECKPOINT_INCOMPLETE`), `Restoring`/`Restored`/`RestoreFailed` (restore
  lifecycle → `RESTORING`/`RESTORED`/`FAILED`), `Get`, and a shared internal
  `setStatus` helper.
- `s3.go` — `ObjectStore.PushDir` tars+gzips a checkpoint directory and
  streams it through an `io.Pipe` directly into `manager.Uploader`'s
  multipart upload (the tarball is never fully buffered on disk or in
  memory — matters for large checkpoints). The package-level `PullFromURI`
  does the reverse (streamed gunzip+untar on download), with a
  path-traversal ("zip-slip") guard on tar entry names.
- `ebs.go` — `ValidateEBSVolume` calls `ec2:DescribeVolumes` and checks the
  volume is attached to the expected instance (when known) with
  `DeleteOnTermination=false`; called by the daemon at startup when
  `-ebs-volume-id` is set, and the daemon refuses to start if validation
  fails.

## `internal/ebsmount` — EBS volume discovery/mount

Build-tag split (`ebsmount_linux.go` / `ebsmount_other.go`).
`MountForRestore(ctx, volumeID, mountPoint)` resolves
`/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_<volumeID with its one dash
removed>` — AWS's documented, stable NVMe device-identification symlink,
confirmed against real Nitro hardware during testing — waits up to 30s for
it to appear (udev can lag slightly behind the kernel registering the
device), and mounts it unless something is already mounted at `mountPoint`
(checked via `/proc/self/mounts`). This is the only place in the codebase
that deals with raw device names; everywhere else operates on the mount
point path.

## `internal/daemon` — the daemon

`daemon.go` is the largest package; its pieces:
- `Config`/`New` — validates storage-mode mutual exclusivity (`-s3-bucket`
  xor `-ebs-volume-id`, both requiring `-dynamodb-table`), loads the AWS SDK
  config once, and (EBS mode) validates the volume via
  `internal/remote.ValidateEBSVolume` — a failure here means the daemon
  **refuses to start**.
- `Run` — the Unix socket accept loop, plus starting the IMDS poller
  goroutine (unless `-imds=false`).
- `dispatch` — handles `register`/`deregister`/`list`/`ping` over the socket.
  `deregister` only deletes a job row while its status is still `RUNNING` —
  this closes a real race: when CRIU's dump stops a process, `surviva run`'s
  own wait-loop sees the child exit and tries to deregister it; without this
  status check that would delete the row out from under the daemon's own
  in-flight checkpoint bookkeeping for the same job.
- `handleInterruption` — checkpoints every `RUNNING` job through a bounded
  worker pool (`-max-concurrent-checkpoints`, default = CPU count), fed in
  the store's priority order so higher-priority jobs claim a worker slot
  first when concurrency is the binding constraint.
- `checkpointJob` — branches on storage mode. In both S3 and EBS mode, the
  DynamoDB "in progress" record is written **before** whichever step is
  actually risky for that mode: before the S3 upload in S3 mode, but before
  the local dump itself in EBS mode (since there the dump *is* the durable
  write — no separate upload step exists).
- `baseRecord`/`recordEBSStart`/`pushRemote` — helpers building and writing
  `remote.Record`s for the two storage modes.

## `infra/terraform` — orchestrator infrastructure

A self-contained Terraform module (`infra/terraform/`), no external module
dependencies beyond the `hashicorp/aws` provider.

| File | Responsibility |
|---|---|
| `versions.tf` | Provider/version constraints. |
| `variables.tf` | All inputs: `aws_region`, `name_prefix`, `launch_template_id` (required — the replacement instance always launches from the same template as the original, so AMI/kernel/instance-profile match), `launch_template_version`, `az_subnet_map` (AZ→subnet, for pinning replacement launches when a checkpoint volume constrains the AZ), `checkpoint_wait_seconds` (default 110), `ebs_attach_device`, `restore_ssm_document`, `restore_command_prefix`, `replacement_market_type` (`"on-demand"` default or `"spot"` — whether the orchestrator's replacement instance launches On-Demand, so it isn't immediately Spot-interruptible again right after a restore, or Spot, keeping the cost saving at the risk of a repeated interruption/restore cycle), `enable_s3_bucket`, `tags`. |
| `dynamodb.tf` | The jobs table (`PAY_PER_REQUEST`, `job_id` hash key) plus an `instance_id-index` GSI. **Only the orchestrator queries this GSI** — the daemon and `surviva restore` only ever look up by `job_id` directly. |
| `s3.tf` | Optional checkpoint bucket (`enable_s3_bucket`), with a lifecycle rule aborting incomplete multipart uploads after 1 day. |
| `cloudwatch.tf` | One log group (`/surviva/<name_prefix>`, `log_retention_days`) troubleshooting flows into: the daemon's own ongoing log (via the CloudWatch Agent, `ansible/`'s AMI bake) from every instance the launch template ever produces, and each restore attempt's command output (via `state_machine.tf`'s `SendRestoreCommand` `CloudWatchOutputConfig`). |
| `iam.tf` | Three roles. **Instance role/profile**: `dynamodb:PutItem/UpdateItem/GetItem`, `ec2:DescribeVolumes`, `s3:PutObject`+`s3:GetObject` if S3 is enabled (both directions — the same role/profile is shared by the original *and* replacement instance via the shared launch template, so it needs to both push and later pull), `logs:CreateLogGroup`+`CreateLogStream`+`PutLogEvents`+`DescribeLogStreams` on the log group (both the bare ARN *and* its `:*` form — SSM RunCommand's CloudWatch output attempts `CreateLogGroup` against the bare ARN even when the group already exists, and fails that check silently from the caller's perspective if only the `:*` form is granted), `AmazonSSMManagedInstanceCore`. **State-machine role**: `dynamodb:Query` (table + GSI), `ec2:RunInstances/DescribeInstances/DescribeVolumes/AttachVolume`, `ssm:SendCommand`+`ssm:DescribeInstanceInformation`, `iam:PassRole` scoped by the `aws:PassedToService=ec2.amazonaws.com` condition (needed to launch an instance carrying the instance profile). **EventBridge role**: `states:StartExecution` on just this state machine. |
| `state_machine.tf` | The `aws_sfn_state_machine` resource; renders the ASL template via `templatefile()`. Notably computes the final `restore_command_prefix` by appending `--dynamodb-table <table> --aws-region <region>` to the user-supplied prefix — the replacement instance has no other way to learn which table to read, so the orchestrator (which already knows both) bakes them in. |
| `templates/restore_orchestrator.asl.json.tftpl` | The state machine definition itself — see below. |
| `eventbridge.tf` | The rule matching `{"source":["aws.ec2"],"detail-type":["EC2 Spot Instance Interruption Warning"]}` — the real, native AWS event (confirmed firing for real during FIS testing), targeting the state machine via the EventBridge role. |
| `outputs.tf` | `jobs_table_name`, `checkpoints_bucket_name`, `instance_profile_name`/`arn`, `state_machine_arn`, `cloudwatch_log_group_name`. |
| `terraform.tfvars.example` | Documented starting point for a deployer's own `terraform.tfvars`. |

### The state machine (JSONata mode, no Lambda)

Written in **JSONata-mode** Amazon States Language (not classic JSONPath
mode) — chosen specifically because the flow needs array filtering and
conditional logic that classic ASL handles poorly without a Lambda, and the
team wanted zero Lambda functions. Every step is a native `aws-sdk:*`
integration.

Flow: `InitVariables` (seeds the AZ→subnet map as a JSONata variable) →
`WaitForCheckpoints` (fixed wait, default 110s) → `QueryJobs`
(`dynamodb:query` via the GSI, keyed by the interrupted instance's ID) →
`AnyRestorableJobs` (Choice: any job reached `CHECKPOINT_COMPLETE`?) →
`DetermineAZ`/`HasAZOverride` (does any restorable job need a specific AZ
for its EBS volume?) → `RunInstanceInAZ`/`RunInstanceDefault`
(`ec2:runInstances` from the same launch template) → `WaitForInstanceRunning`
loop (`ec2:describeInstances`) → `WaitForSSMOnline`/`DescribeSSMInfo`/
`IsSSMOnline` loop (`ssm:describeInstanceInformation` — added after
discovering in testing that EC2 "running" precedes SSM agent "Online" by
tens of seconds; sending the restore command too early fails with
`Ssm.InvalidInstanceIdException`) → `AttachAndRestore`, a `Map` state over
the restorable jobs, whose `ItemProcessor` starts with an `InitItem` Pass
state that does `Assign: {"item": "{% $states.input %}"}` — necessary
because JSONata's `$states.input`/`$states.result` only ever refer to the
*current* state, so per-item context has to be explicitly carried forward
via a named variable rather than relied on implicitly — then `IsEBSJob` →
`AttachVolume`/wait-loop if applicable → `SendRestoreCommand`
(`ssm:sendCommand`, running
`surviva restore --dynamodb-table <table> --aws-region <region> <job_id>`).

The state machine does **not** call `dynamodb:updateItem` itself. An earlier
version had it mark the job `RESTORED` immediately after sending the SSM
command; this was removed once `surviva restore` existed, since that command
firing successfully doesn't mean the restore actually succeeded —
`surviva restore` is now the sole owner of the `RESTORING`/`RESTORED`/
`FAILED` transition, only marking `RESTORED` once the process is genuinely
back up.

Three JSONata gotchas worth knowing if you touch the template (full
rationale in [`../design-record/technical.md`](../design-record/technical.md)):
1. Filtering an array to exactly one match unwraps it to a bare object, not a
   one-element array — force it back to an array with the trailing `[]`
   operator.
2. `and`/`or` require both operands to already be strict booleans; comparing
   a `$lookup` miss to `null` yields `undefined`, not `false` — use
   `$exists(...)` instead, and rely on `and`'s short-circuit to avoid ever
   evaluating an invalid second operand.
3. An `Assign` field can never resolve to `undefined` — any expression that
   might not match anything needs an explicit fallback (`: ''` or `: []`).
