# Spec: `daemon` module

## Objective

The long-running process: the only thing that writes to `store`, the thing
that actually shells out to CRIU, and the thing that watches for a cloud
interruption signal and reacts to it. It also defines and serves the
interface `surviva-cli` talks to — per the capability map, that mechanism is
this module's call. It's a Unix domain socket speaking newline-delimited
JSON, exactly like `legacy/internal/ipc` + `legacy/internal/daemon` already
proved out; this spec keeps that shape and adapts it to the new store/status
model.

**Correction to the capability map's original assumption:** `internal/
procattr` (session detachment) and `internal/fdguard` (closing inherited
fds) are `surviva-cli run`-time concerns — whoever `exec`s the tracked
process needs them, and that's the CLI, not the daemon (this matches
`legacy/cmd/surviva/run.go`'s actual use of them, not `legacy/internal/
daemon`). They belong in `surviva-cli`'s future spec. `legacy/README.md` will
be corrected to say so.

## Reused from `legacy/` (as-is, or with a small adaptation noted)

| Package | Change |
|---|---|
| `internal/criu` (`Dump`/`Restore`/`Available`) | None. |
| `internal/checkpoint` (`Run`) | Adapted: takes the checkpoint dir directly (`Run(ctx, dir, job)`) instead of a `baseDir` it joins with the job id itself. `store.Job.CheckpointDir` is now the single source of truth for where a job's images live, so the old `Dir(baseDir, jobID)` join is redundant and dropped. |
| `internal/resume` (`Run`) | None — it already took a full dir. |
| `internal/imds` (`Client`, `Poller`) | Wrapped behind a new `Provider` interface (below) so Azure/GCP can be added later as siblings, not a rewrite. |
| `internal/procsignal` (`KillGroup`) | None — used by `Cancel`. |

Not reused (out of scope per the capability map): `internal/remote`
(S3/DynamoDB), `internal/ebsmount`. `internal/idgen` (used in an earlier
draft of `Register`) was dropped entirely and removed from the tree — job
ids are Slurm-style sequential integers now, assigned by `store.Insert`'s
`INTEGER PRIMARY KEY AUTOINCREMENT` column instead of app-level random
generation. See `SPEC-store.md`.

## Cloud provider abstraction

```go
package provider

// Provider watches for an interruption/rebalance-style signal and calls
// onSignal (at most once per real signal) until ctx is done.
type Provider interface {
    Run(ctx context.Context, onSignal func(daemon.Signal))
}
```

```go
// Signal is what a Provider reports on each interruption/rebalance event.
type Signal struct {
    Trigger string            // "interruption-notice" | "rebalance-recommendation"
    Detail  map[string]string // cloud-specific extra context; may be nil
}
```

(`Signal` replaces the original bare `trigger string` callback — see
"Notify a Lambda/Step Function on interruption" below for why: the AWS
provider needed somewhere to attach instance-id/AZ/action/notice_time for
the notify payload, without `daemon` itself knowing anything AWS-specific.)

`aws.New(...)` (adapting `legacy/internal/imds`) is the only implementation
today; `config.CloudProvider == "aws"` selects it (already the only value
`config.Load` accepts — see `SPEC-config.md`). Azure/GCP become new packages
implementing the same interface once `config` accepts those values too; no
change to `daemon`'s own logic when that happens.

## Notify a Lambda/Step Function on interruption

Optional: `daemon` can invoke a user-owned AWS Lambda function or Step
Functions state machine the instant it detects a Spot interruption/
rebalance signal, handing it a JSON payload. surviva does none of the
downstream logic — the user's Lambda/Step Function does whatever it wants
with the payload (custom recovery orchestration, alerting, anything).
Considerably lighter than `legacy`'s old approach of surviva itself owning
a full Step-Functions-based recovery orchestrator.

```go
// Notifier delivers a JSON payload to an external target. nil means "not
// configured" -- handleInterruption skips this step entirely.
type Notifier interface {
    Notify(ctx context.Context, payload []byte) error
}
```

`Config` gains an optional `Notifier`. `handleInterruption` builds the
payload and — if a `Notifier` is configured — calls `Notify` in its own
goroutine immediately after latching `interrupted` and listing `RUNNING`
jobs, **before** the checkpoint fan-out starts and regardless of whether
there are zero or many `RUNNING` jobs. Firing immediately (not after
checkpointing finishes) means it never eats into the ~2-minute Spot window;
running it in its own goroutine means a slow or unreachable external target
never blocks or delays checkpointing. Bounded by a 10s timeout
(`notifyTimeout`) — this is a network call to an external AWS API, unlike a
local CRIU dump, and must not be allowed to hang indefinitely.

Payload (summary-only by design — not full `Command`/`CheckpointDir`/hook
paths, to avoid handing potentially sensitive command-line arguments or
paths to an external target):

```json
{
  "trigger": "interruption-notice",
  "detected_at": "2026-09-16T20:00:00Z",
  "detail": {
    "instance_id": "i-0123456789abcdef0",
    "availability_zone": "us-east-1a",
    "action": "terminate",
    "notice_time": "2026-09-16T20:02:00Z"
  },
  "jobs": [
    {"id": "1", "status": "RUNNING", "pid": 5806}
  ]
}
```

`detail` is exactly `Signal.Detail`, passed through — for the AWS provider,
`instance_id`/`availability_zone` come from `imds.Client` (best-effort,
omitted on failure, never fatal — the same tolerance the rest of the AWS
integration already gives IMDS calls); `action`/`notice_time` come from
whichever IMDS signal actually fired. `jobs` is exactly the `RUNNING` list
`handleInterruption` already computed for the checkpoint fan-out — no
extra `store` query. Notify success/failure is logged via `audit.Log`
(`notify_started`/`notify_succeeded`/`notify_failed`), same pattern as
`checkpoint_started`/etc.

Two implementations, `internal/daemon/notify/aws` (mirrors `provider/aws`'s
existing shape):

```go
type LambdaNotifier struct{ client *lambda.Client; arn string }
func NewLambdaNotifier(cfg aws.Config, arn string) *LambdaNotifier
// Invoke with InvocationType=Event -- async, fire-and-forget; surviva
// never waits on or inspects what the user's Lambda actually does.

type StepFunctionNotifier struct{ client *sfn.Client; arn string }
func NewStepFunctionNotifier(cfg aws.Config, arn string) *StepFunctionNotifier
// StartExecution -- already async by nature, returns once the execution
// has started, not when it finishes.
```

This is the first feature in the redesign needing the real AWS SDK
(`aws-sdk-go-v2/{config,service/lambda,service/sfn}`) — `internal/imds`
talks to the metadata service over plain `net/http`, no SDK needed there.
Credentials load via `awsconfig.LoadDefaultConfig` in
`cmd/surviva/daemon.go`'s wiring — the same instance-role credential chain
`legacy` already relied on for S3/DynamoDB.

## Config addition

`config`'s five directives don't cover how many jobs get checkpointed at
once when an interruption fires across several `RUNNING` jobs at the same
time. Adding one **optional** sixth directive (not required — every
existing `surviva.conf` stays valid):

| Key | Type | Required | Default | Meaning |
|---|---|---|---|---|
| `MaxConcurrentCheckpoints` | positive int | no | `runtime.NumCPU()` | Bound on simultaneous CRIU dumps during a fan-out (same rationale as `legacy`: dumping many large process trees fully in parallel can starve disk/CPU badly enough that none finish in time). |
| `NotifyTargetType` | `lambda` \| `stepfunction` | no (unset = disabled) | — | Selects the notify target — see "Notify a Lambda/Step Function" above. |
| `NotifyTargetARN` | ARN string | yes, when `NotifyTargetType` is set | — | The Lambda function or state machine ARN to invoke. |

These are the only changes to the already-built `config` module this spec
requires; everything else here is new code in `daemon` itself.

## Job lifecycle operations

All four below share one rule carried forward from `legacy` (ADR-3): a
job's `store` status arbitrates races between daemon-side transitions and
whatever the CLI/interruption path is doing concurrently. Every transition
goes through `store.UpdateStatus`/`UpdatePID`, which already reject an
invalid `from` state — `daemon` doesn't need its own separate guard on top,
just surface `store`'s error back over IPC.

**Checkpoint** (triggered by `Pause` below, or by the interruption fan-out —
identical code path either way):
1. `store.UpdateStatus(id, CHECKPOINT_IN_PROGRESS, "")`
2. `audit.Log({daemon, checkpoint_started, id, ok})`
3. `checkpoint.Run(ctx, job.CheckpointDir, job)`
4. Success → `store.UpdateStatus(id, CHECKPOINT_CREATED, "")`,
   `audit.Log({daemon, checkpoint_succeeded, id, ok})`.
   Failure → `store.UpdateStatus(id, CHECKPOINT_CREATION_FAILED, err.Error())`,
   `audit.Log({daemon, checkpoint_failed, id, error, err.Error()})`.

No separate "storage push" step exists yet (S3/EBS is out of scope), so
"checkpointed" just means "dumped to `job.CheckpointDir` on local disk" —
matching `legacy`'s original local-disk-only mode.

**Resume** (from `CHECKPOINT_CREATED` or `RESTORE_FAILED`):
1. `store.UpdateStatus(id, RESTORE_PENDING, "")`
2. `audit.Log({daemon, restore_started, id, ok})`
3. `resume.Run(ctx, job.CheckpointDir, job.HookResume, job.ID)` → new pid
4. Success → `store.UpdatePID(id, pid, pid)` (moves to `RUNNING` per
   `store`'s own transition table), `audit.Log({daemon, restore_succeeded, id, ok})`.
   Failure → `store.UpdateStatus(id, RESTORE_FAILED, err.Error())`,
   `audit.Log({daemon, restore_failed, id, error, err.Error()})`.

**Cancel:**
- If `RUNNING`: `procsignal.KillGroup(job.PGID, SIGTERM)`, then
  `store.UpdateStatus(id, CANCELED, "")`.
- If `CHECKPOINT_CREATED` / `CHECKPOINT_CREATION_FAILED` / `RESTORE_FAILED`:
  no live process to signal (already stopped or never resumed) — just
  `store.UpdateStatus(id, CANCELED, "")`.
- If `CHECKPOINT_IN_PROGRESS` / `RESTORE_PENDING`: rejected by `store`
  (no valid transition out of those to `CANCELED`) — surfaced as "job is
  mid-checkpoint/restore, try again once it settles."
- Checkpoint files on disk are **not** deleted on cancel (see Open
  Questions) — same "no automatic cleanup" stance `store` already takes on
  terminal jobs.

**Complete** (the CLI reports its tracked child exited — normal end of
`run`/`join`, not a checkpoint):
- Request carries `JobID`, `ExitCode`, optional `ErrMsg`.
- If current status isn't `RUNNING` anymore, this is a no-op (not an error)
  — exactly `legacy`'s deregister race: the checkpoint subsystem may have
  already moved the job past `RUNNING` (CRIU's dump stops the process,
  which can race the CLI noticing its child exited).
- Otherwise: `ExitCode == 0` → `COMPLETED`; else → `FAILED` with
  `FailureReason` set to `ErrMsg` (or `"exited with code N"` if `ErrMsg` is
  empty).

**Register** (backs both a future `run` and `join` in `surviva-cli` — same
daemon-side handling either way, they only differ in how the CLI fills out
the request):
1. Refused if the interruption latch (below) is set — same reasoning as
   `legacy`: a job registered after a signal already fired has no realistic
   path to being saved.
2. `id, err := store.Insert(Job{..., CheckpointDir: req.CheckpointDir, Status: RUNNING})`
   — `store` itself assigns `id` (sequential, see `SPEC-store.md`); if the
   request gave no `CheckpointDir` override, `Insert`'s value is empty at
   this point.
3. If no override was given: `dir := filepath.Join(cfg.CheckpointBaseDir, id)`,
   then `store.UpdateCheckpointDir(id, dir)` — this couldn't happen before
   step 2 because the default path needs the id `Insert` only just assigned.

**Prune** (backs a future `surviva prune [job-id]` — clears checkpoint
*files* off disk, never touches `store` rows; a pruned job stays fully
visible via `show`, per `store`'s "no automatic pruning of history" stance):
- With a `JobID`: `store.Get(id)`; if its status isn't `FAILED`/`CANCELED`/
  `COMPLETED`, reject ("job <id> is not terminal, refusing to prune"— an
  active job's checkpoint is still needed); else `os.RemoveAll(job.CheckpointDir)`.
- Without a `JobID`: `store.ListTerminal()`, `os.RemoveAll` each job's
  `CheckpointDir`, collecting per-job errors rather than aborting on the
  first failure. Response reports how many were pruned and lists any that
  failed (e.g. permission error), it doesn't fail the whole request for one
  bad one.
- Idempotent by construction: `os.RemoveAll` on an already-gone directory is
  a no-op, not an error, so running `prune` twice (or pruning a job whose
  checkpoint was already cleaned by hand) is harmless — no new `store` field
  needed to track "already pruned."

`daemon` does **not** write its own audit-log entry for "a Register/Pause/
Resume/Cancel/Show/List/Prune request arrived" — that's `surviva-cli`'s job
to log about itself (`component: "cli"`). `daemon` only logs its own
internal activity (interruption detection, checkpoint/restore attempts),
avoiding the same event being logged twice from both sides.

## Interruption fan-out

Same shape as `legacy/internal/daemon.handleInterruption`: on `onSignal`,
latch `interrupted` (an `atomic.Bool`, refusing new `Register` calls from
that point on), list every `RUNNING` job from `store`, and run the
Checkpoint flow above for each, capped at `MaxConcurrentCheckpoints`
concurrent via a bounded worker pool. Runs independently of any client
connection — it's driven by the `Provider`, not by `dispatch`.

## Wire protocol (`internal/ipc`, new package — `surviva-cli` imports it too)

```go
type Action string

const (
    ActionPing     Action = "ping"
    ActionRegister Action = "register"
    ActionList     Action = "list"
    ActionShow     Action = "show"
    ActionPause    Action = "pause"
    ActionResume   Action = "resume"
    ActionCancel   Action = "cancel"
    ActionComplete Action = "complete"
    ActionPrune    Action = "prune"
)

type RegisterJob struct {
    PID            int
    PGID           int
    Command        []string
    WorkDir        string
    CheckpointDir  string // optional override; daemon computes the default if empty
    HookCheckpoint string
    HookResume     string
}

type Request struct {
    Action   Action
    Job      *RegisterJob // Register
    JobID    string       // Show/Pause/Resume/Cancel/Complete/Prune (Prune: empty means "all terminal jobs")
    ExitCode int          // Complete
    ErrMsg   string       // Complete, optional
}

type Response struct {
    OK       bool
    Error    string
    JobID    string
    Job      *store.Job  // Show
    Jobs     []store.Job // List
    Message  string      // e.g. "resumed as pid 4821", "pruned 3 job(s)"
    Failures []string    // Prune only: "<job-id>: <error>" for any that failed to clean up
}
```

Transport: Unix domain socket, one JSON object per line, one goroutine per
connection (`go handleConn(conn)`) so a slow `Pause`/`Resume` on one
connection never blocks another client's `List`/`Show`. Socket path: same
`SURVIVA_SOCKET` env var / per-OS default convention as `legacy/internal/
ipc.DefaultSocketPath()` — reused verbatim, not reinvented.

## Testing Strategy

CRIU itself needs a real Linux box (per `legacy`'s own testing-strategy
philosophy — real infra over mocks); that part is manual/integration, not
`go test ./...`. What *is* unit-testable here, against a temp-file `store` +
`auditlog` and a fake `Provider` that never fires:
- `Register` → `List` shows it, `Show` finds it by id.
- `Register` refused once the interruption latch is set.
- `Complete` on a still-`RUNNING` job sets `COMPLETED`/`FAILED` correctly by
  exit code; `Complete` on a job no longer `RUNNING` is a no-op, not an
  error.
- `Cancel` on `RUNNING` vs. `CHECKPOINT_CREATED` vs. `CHECKPOINT_IN_PROGRESS`
  (the last one rejected) each behave as specified above.
- A fake `Provider` firing `onSignal` triggers the interruption fan-out over
  every `RUNNING` job and nothing else (verify via `store` status changes),
  bounded by `MaxConcurrentCheckpoints`.
- `Prune` with a `JobID` on a terminal job removes its `CheckpointDir` and
  succeeds; on a `RUNNING`/`CHECKPOINT_IN_PROGRESS`/etc. job, it's rejected
  and the directory is untouched. `Prune` with no `JobID` removes every
  terminal job's directory, leaves every active job's directory alone, and
  running it a second time in a row is a harmless no-op (nothing left to
  remove, no error).
- A fake `Notifier` (same swappable-dependency pattern as `checkpointFunc`/
  `resumeFunc`) capturing its payload: `handleInterruption` calls it with
  the right trigger/detail/job-summary list when one is configured, fires
  even when there are zero `RUNNING` jobs (empty `jobs` array, not skipped),
  and doesn't panic or error when no `Notifier` is configured (`nil`, the
  default). The real `lambda.Invoke`/`sfn.StartExecution` calls need manual
  verification against real (temporary) AWS resources — this doesn't need a
  full EC2 instance the way CRIU does, just real AWS credentials and a
  throwaway Lambda/state machine with `lambda:InvokeFunction`/
  `states:StartExecution` granted.

## Boundaries

- **Always:** every `store` write goes through the status-transition rules
  it already enforces; refuse new `Register` once interrupted; `Prune`
  never touches a `store` row, only files on disk.
- **Ask first:** adding another config directive beyond
  `MaxConcurrentCheckpoints`; building S3/EBS/DynamoDB support here;
  auto-pruning on a schedule (`Prune` is explicitly operator-triggered only,
  per the decision below — no cron-like behavior inside `daemon` itself).
- **Never:** let one slow client connection block another (per-connection
  goroutines, not a single serialized loop); delete checkpoint files on
  `Cancel` or any other status transition — only `Prune` ever deletes files.

## Success Criteria

- Package compiles and its unit tests (above) pass without CRIU or any
  cloud credentials present.
- On a real Linux box with CRIU installed: `Register` → `Pause` → `Resume`
  round-trips a real process (e.g. `sleep 300`) through the full status
  sequence, checkpoint images land at the job's `CheckpointDir`.
- A fake interruption signal checkpoints every `RUNNING` job and refuses
  further registrations, mirroring `legacy`'s proven behavior.
- `Prune` clears every terminal job's checkpoint files and nothing else,
  and every pruned job is still fully visible via `Show`.

## Resolved

1. ~~Checkpoint-directory cleanup on `Cancel`.~~ **Decided: `Cancel` never
   deletes files.** Cleanup is a separate, explicit, operator-triggered
   `Prune` operation instead (see above) — `surviva prune [job-id]` clears
   checkpoint files for terminal jobs, one or all of them, on demand. No
   automatic/scheduled pruning inside `daemon` itself (not asked for; add
   later if wanted).
2. ~~`Complete` as the action name.~~ **Confirmed: `Complete`.**

## Open Questions

None remaining.
