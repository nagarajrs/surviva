# Technical Design Record

An ADR-style log of the key decisions made while building Surviva, in chronological/phase order. Each entry gives the Context (what prompted the decision), the Decision itself, and its Consequences (what it costs, what it buys, and where it's exercised in the codebase). For what the system does overall, see [Architecture](../architecture/technical.md); for the catalogue of resulting constraints, see [Limitations](../limitations/technical.md); for the file-by-file breakdown, see [Components](../components/technical.md).

---

## ADR-1: Tracked children run as full session leaders (`Setsid`, not just `Setpgid`)

**Context.** A tracked child originally only got `Setpgid: true` (its own process group). CRIU dump/restore for a process with a controlling terminal either requires `--shell-job` (which tries to reattach to the *same* shell/session at restore time) or fails outright with "Task attached to shell terminal."

**Decision.** Start every tracked child with `Setsid: true` instead (`internal/procattr/procattr_unix.go`), which implies its own process group as a side effect (PGID == PID). No `--shell-job` flag is ever passed to `criu dump`/`criu restore` (`internal/criu/criu.go`).

**Consequences.** A tracked child's checkpoint/restore never depends on an external shell or terminal existing at restore time — which a replacement instance couldn't provide anyway even if the original process had a real tty. Operationally this means a job's stdin/stdout/stderr should be redirected away from an interactive terminal for reliable checkpointing (see Limitations #2).

---

## ADR-2: `internal/fdguard` — close inherited fds before starting a tracked child

**Context.** During Phase 2 testing (WSL2), a `criu dump` on a process started via `surviva run` under a `nohup`'d shell chain failed with "Task attached to shell terminal," *despite* the child already being a session leader via ADR-1. Inspecting `/proc/<pid>/fd` showed two extra file descriptors (fd 7 and 10) open to `/dev/ptmx` — inherited from the invoking shell/nohup chain, which had not set `FD_CLOEXEC` on them.

**Decision.** Before `cmd.Start()` in `surviva run`, scan `/proc/self/fd` and call `syscall.CloseOnExec` on every fd above 2 (`internal/fdguard/fdguard_linux.go`). This is best-effort and Linux-only (no-op stub elsewhere).

**Consequences.** Any fd `surviva run`'s own process happens to have inherited — regardless of source — is prevented from leaking into the tracked child. This was verified to eliminate the tty-attachment failure: after the fix, `ls -la /proc/<pid>/fd/` showed only fds 0/1/2 and `criu dump` succeeded.

---

## ADR-3: Job status arbitrates the deregister race

**Context.** `criu dump` stops the process it's checkpointing (no `--leave-running`). `surviva run`'s own `cmd.Wait()` then observes the child exit and calls `Deregister` — but by this point the daemon may already be mid-checkpoint on the *same job*, or may be about to update its status. The two code paths raced on the same SQLite row, and the original deregister handler deleted rows unconditionally.

**Decision.** `internal/daemon/daemon.go`'s `ActionDeregister` handler first reads the job's current status; it deletes the row only if status is still `RUNNING`. Once the daemon has moved a job into any checkpoint-related state, the row is left alone (checkpoint bookkeeping owns it from that point).

**Consequences.** No more `"job %s not found"` errors from a deregister racing a checkpoint. This is a pure status-based lock with no additional mutex; the SQLite store already serializes access via `SetMaxOpenConns(1)` (ADR-13), so the read-then-conditional-delete is not itself racy at the storage layer.

---

## ADR-4: Pluggable hooks as a first-class alternative to CRIU

**Context.** Raised and decided during initial project scoping (user request), anticipating that CRIU cannot checkpoint everything (GPU state, certain sockets — see Limitations #1).

**Decision.** `surviva run --hook-checkpoint <script> --hook-resume <script>` registers per-job scripts that fully replace CRIU for that job. Contract: the checkpoint hook is invoked as `<script> <job-id> <output-dir>` and must exit 0 on success (`internal/checkpoint/checkpoint.go`); the resume hook is invoked as `<script> <job-id> <checkpoint-dir>` and must print the resumed PID as a single integer line on stdout (`internal/resume/resume.go`).

**Consequences.** A job with neither CRIU support nor a hook fails loudly (marked `FAILED`) rather than being silently unsupported. `HookCheckpoint`/`HookResume` paths are carried through the DynamoDB `Record` (`internal/remote/dynamo.go`) so a restored job on a replacement instance keeps its hooks — the protection is recursive.

---

## ADR-5: Bounded, priority-ordered concurrency for checkpointing (Phase 3)

**Context.** Sequential checkpointing (Phase 2) doesn't scale to many tracked jobs within the ~2-minute interruption window. Fully unbounded parallel dumps risk saturating disk/CPU badly enough that none finish in time.

**Decision.** `handleInterruption` in `internal/daemon/daemon.go` feeds runnable jobs (already returned priority-DESC by `store.List`) into a bounded worker pool sized by `--max-concurrent-checkpoints` (default `runtime.NumCPU()`), via a buffered channel semaphore + `sync.WaitGroup`.

**Consequences.** Under constrained concurrency, higher-priority jobs claim a worker slot first; lower-priority jobs queue behind them and may not complete if time runs out (see Limitations #12). Verified empirically: with concurrency capped at 1, three jobs of priority 5/3/1 checkpointed in strictly that order; with unconstrained concurrency, a slow hook script proved genuine overlap (three ~3s jobs completing in ~3s total wall-clock, not 9s sequential); three concurrent real `criu dump` calls all succeeded and all three later restored correctly.

---

## ADR-6: "Mark durable status before the risky step," applied per storage mode

**Context.** Phase 5 (S3) established the pattern: write a DynamoDB `CHECKPOINT_IN_PROGRESS` record before the (slow, large) upload starts, so an instance dying mid-upload leaves the table showing `IN_PROGRESS`/`CHECKPOINT_INCOMPLETE`, never falsely `CHECKPOINT_COMPLETE`. Phase 6 (EBS) initially risked applying this asymmetrically, since EBS has no separate upload step — the local `criu dump` writes directly to the already-durable, EBS-backed disk.

**Decision.** For EBS mode, the daemon writes the `IN_PROGRESS` record via `recordEBSStart` *before* `checkpoint.Run` (the dump) starts, not after — the dump itself is EBS mode's risky step. `internal/daemon/daemon.go`'s `checkpointJob` branches on storage mode specifically to place this write correctly for each.

**Consequences.** Both storage modes satisfy the same invariant (a mid-failure instance death is always visible as `IN_PROGRESS`/`CHECKPOINT_INCOMPLETE`, never mistaken for done) despite having structurally different risky steps. Verified for both modes: an induced S3 upload failure (nonexistent bucket) and an induced EBS dump failure (failing hook script) each correctly left the record `CHECKPOINT_INCOMPLETE` with a `failure_reason`, not silently `CHECKPOINT_COMPLETE`.

---

## ADR-7: EBS volume validated once at daemon startup, not per-checkpoint

**Context.** The entire value of the EBS storage path depends on the volume surviving the instance's termination — which requires its attachment's `DeleteOnTermination` to be `false`. This is a launch-time/attach-time property, not something that changes per-checkpoint under normal operation.

**Decision.** `internal/remote/ebs.go`'s `ValidateEBSVolume` is called once, in `daemon.New`, before the daemon starts accepting connections: it calls `ec2:DescribeVolumes`, confirms the volume is attached to *this* instance (when instance ID is known via IMDS) and that `DeleteOnTermination` is `false`. Any failure aborts daemon startup entirely.

**Consequences.** Fail fast and loud rather than silently checkpointing to a volume that would be deleted along with the instance. Verified for real against a genuine EC2 instance + volume: unattached → rejected ("not attached to any instance"); attached with `DeleteOnTermination=true` → daemon refuses to start; attached to a different instance ID → rejected as a mismatch; correct configuration → daemon starts, later checkpoints/restores succeed. A confirming detail: terminating the original instance afterward left the volume `available` (not deleted), proving the flag had real effect — not just that the check believed it.

---

## ADR-8: EBS device discovery via AWS's documented `/dev/disk/by-id` NVMe convention

**Context.** `AttachVolume` only attaches the raw block device; nothing mounts it, and the kernel device name (`/dev/nvme1n1`, etc.) is not guaranteed stable across attach cycles or instances. Alternatives considered: requiring the operator to pre-establish a fixed mount via some external convention (fragile, pushes complexity onto every deployer); raw NVMe vendor-specific "identify controller" ioctls to map device → volume ID directly (more code, more failure modes, no benefit over the simpler documented path).

**Decision.** `internal/ebsmount/ebsmount_linux.go`'s `MountForRestore` resolves `/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_<volume-id-with-its-one-dash-removed>` — a stable symlink AWS's own NVMe udev rules create on Nitro-based instances — waiting up to 30s for it to appear, then mounts the resolved device at the target mount point unless something is already mounted there (checked via `/proc/self/mounts`).

**Consequences.** Confirmed for real against a genuine attached EBS volume: the symlink name computed by the code (`strings.Replace(volumeID, "-", "", 1)`) matched AWS's actual symlink exactly, and resolved to the correct `/dev/nvme1n1`. This only works on Nitro instance types with the relevant udev rules present (see Limitations #5); non-Linux platforms get an explicit "not supported" stub (`ebsmount_other.go`).

---

## ADR-9: Restore's EBS mount point is `filepath.Dir(ebs_path)`

**Context.** `surviva restore` needs to know *where* to mount the resolved device. The daemon's own EBS mode already treats `--checkpoint-dir` as the volume's mount point, with each job's images in a subdirectory named by job ID (`checkpoint.Dir(baseDir, jobID)`).

**Decision.** Rather than recording a separate "mount point" field, `cmd/surviva/restore.go` derives it as `filepath.Dir(rec.EBSPath)` — i.e., the EBS volume's filesystem root IS the checkpoint directory tree, and nothing else is expected to live on it.

**Consequences.** No extra schema field needed; the convention is simple and was exercised successfully end-to-end (dump → unmount → blind restore → correct mount + resume). It does mean an EBS checkpoint volume should be dedicated to surviva's own use, not shared with other data.

---

## ADR-10: Step Functions with native AWS SDK integrations only — no Lambda

**Context.** Explicitly offered as a choice during Phase 7 scoping: Lambda functions for custom orchestration logic, or Step Functions' native `aws-sdk:<service>:<action>` integrations exclusively.

**Decision.** No Lambda. All orchestrator logic — querying DynamoDB via the `instance_id` GSI, filtering to restorable jobs, determining AZ pinning, launching the replacement instance, waiting for it to be running and SSM-online, attaching EBS volumes, and sending the restore command — is expressed directly in the state machine definition (`infra/terraform/templates/restore_orchestrator.asl.json.tftpl`), written in **JSONata-mode** ASL (`"QueryLanguage": "JSONata"`) rather than classic JSONPath mode, since JSONata natively supports the array filtering/arithmetic/conditional logic this flow needs.

**Consequences.** Fewer moving parts to build, test, and deploy (no Lambda runtime, execution role, or cold-start concerns) — at the cost of real JSONata-mode quirks, each root-caused against the actual AWS Step Functions API during iterative testing (`create-state-machine`/`update-state-machine` validation, then real `start-execution` runs), not guessed blind:

- **Single-match array unwrapping.** `$states.result.Items[status.S = 'CHECKPOINT_COMPLETE']` returns a bare object, not a one-element array, when exactly one item matches. First surfaced as: `"The JSONata expression '$restorableJobs' specified for the field 'Items' returned an unexpected result type. Expected 'array', but was 'object'"` in the `AttachAndRestore` Map state. Fixed with the `[]` array-constructor suffix: `...[status.S = 'CHECKPOINT_COMPLETE'][]`.
- **`and`/`or` require strict booleans; a `$lookup` miss is `undefined`, not falsy.** `$targetAz != '' and $lookup($azSubnetMap, $targetAz) != null` threw `"T0410: Argument 2 of function \"and\" does not match function signature"` — `!= null` against `undefined` does not yield a normal boolean the way one might expect from a "truthy" language. Fixed by wrapping in `$exists(...)` (which always returns a strict boolean) and relying on JSONata's genuine short-circuit evaluation of `and`, restructured as `$exists($targetAz) and $exists($lookup($azSubnetMap, $targetAz))` — critically, `$targetAz` itself first had to be guaranteed non-`undefined` (see next point), or `$lookup` would still throw trying to evaluate its second argument (`$exists` does not guard evaluation of its own argument from throwing).
- **`Assign` can never resolve to `undefined`.** `$restorableJobs[storage_type.S = 'ebs'][0].az.S` legitimately evaluates to `undefined` when no job is EBS-backed — a perfectly normal "no match" case — but assigning it to a variable threw `"...returned nothing (undefined)"`. Fixed with an explicit `$count(...) > 0 ? ... : ''` guard so the assignment is always a defined string (empty when not applicable). The same fix was needed for the `restorableJobs` assignment itself, guarding the zero-match case with `... : []`.
- **`$states.input`/`$states.result` are local to the current state.** Inside the Map's `ItemProcessor` (per-job: attach volume → wait → describe → choice → send restore command), each Task's own result silently becomes the *next* state's `$states.input`, discarding the original per-item job record (job ID, EBS volume ID, hook paths). Fixed by adding an initial `InitItem` Pass state that `Assign`s the whole incoming item to a variable (`$item`) up front, referenced by every later state in that iteration instead of `$states.input`.

Each fix was validated by re-running the actual state machine (see [Testing Strategy](../testing-strategy/technical.md)) rather than reasoned about purely from documentation.

---

## ADR-11: Terraform, not CDK/CloudFormation/SAM

**Context.** Explicitly offered as a choice during Phase 7 scoping: AWS CDK for Go (keeps the whole repo in one language), Terraform (portable, widely known), or plain CloudFormation/SAM (no extra tooling dependency, more verbose).

**Decision.** Terraform, per explicit user preference.

**Consequences.** A second language/toolchain alongside the all-Go application code, versus the alternative of staying single-language with CDK for Go. The module (`infra/terraform/`) is self-contained: DynamoDB table + GSI, optional S3 bucket, three IAM roles (instance, state machine, EventBridge), the state machine itself, and the EventBridge rule — see [Components](../components/technical.md) for the file-by-file breakdown.

---

## ADR-12: `surviva restore` — not the orchestrator — owns the RESTORING/RESTORED/FAILED transition

**Context.** The Phase 7 orchestrator originally included a `MarkRestored` state: a `dynamodb:updateItem` call setting status to `RESTORED` immediately after `SendCommand` succeeded — i.e., as soon as the SSM command was *dispatched*, not once it had actually succeeded on the instance. This was an intentional simplification at the time (Phase 8, `surviva restore` itself, didn't exist yet).

**Decision.** Once Phase 8 built `surviva restore` (with its own `Restoring`/`Restored`/`RestoreFailed` transitions in `internal/remote/dynamo.go`), the orchestrator's `MarkRestored` state and its now-unused `dynamodb:UpdateItem` IAM permission were removed from `infra/terraform/templates/restore_orchestrator.asl.json.tftpl` and `iam.tf`. `SendRestoreCommand` is now the terminal state of that Map iteration (`"End": true`).

**Consequences.** `RESTORED` now means the process is *actually confirmed* running again — `surviva restore` marks it only after `resume.Run` and daemon re-registration succeed. A genuinely failed restore is caught and marked `FAILED` with a reason, rather than a false-positive `RESTORED` masking it. This was directly confirmed during the FIS test: an initial restore attempt (before an unrelated IAM fix landed) correctly left the job `FAILED`, not `RESTORED`, and a later genuine CRIU failure (a missing/wrong-sized regular file — see Limitations #2) was likewise correctly recorded as `FAILED` rather than silently reported as success.

---

## ADR-13: `modernc.org/sqlite` (pure Go), not `mattn/go-sqlite3` (cgo)

**Context.** Development happened on a Windows machine without a configured C toolchain, and the daemon binary needed to cross-compile cleanly to Linux for deployment/testing (WSL2, then real EC2 instances).

**Decision.** Use the pure-Go `modernc.org/sqlite` driver for the local job store (`internal/store/store.go`), and explicitly set `db.SetMaxOpenConns(1)` to avoid this driver's known concurrent-writer lock errors.

**Consequences.** No cgo dependency anywhere in the build; `GOOS=linux GOARCH=amd64 go build` from Windows works without any C cross-compiler. The store is single-writer by design (see Limitations #11) — acceptable for the expected number of concurrently tracked jobs per instance, not designed for high registration throughput.

---

## ADR-14: Verification philosophy — real infrastructure over mocks, culminating in a genuine FIS test

**Context.** A checkpoint/restore tool built around CRIU, IMDS, and several AWS services has many places where "should work in theory" and "actually works" diverge — several of the ADRs above exist specifically *because* something failed for real that wouldn't have been caught by unit tests or code review alone.

**Decision.** Wherever practically possible, verify against the real thing rather than a mock or assumption:
- CRIU itself was built from source and run in WSL2 (Windows has no Linux kernel for CRIU to target; Ubuntu 24.04's own apt repos didn't carry a `criu` package either) for Phases 2–6/8's checkpoint/restore testing.
- A minimal mock IMDS HTTP server (a throwaway Go program, never committed to the repo) simulated Spot signals for local/WSL2 testing, but every AWS service integration (S3, DynamoDB, EC2, IAM, Step Functions, EventBridge, SSM, FIS) was exercised against real, temporary AWS resources — created, used, and torn down per phase.
- The capstone test used AWS Fault Injection Simulator's `aws:ec2:send-spot-instance-interruptions` action against a real Spot EC2 instance, letting the *entire* chain fire for real: genuine IMDS rebalance-recommendation and interruption-notice signals, a genuine native `EC2 Spot Instance Interruption Warning` EventBridge event auto-triggering the state machine (no manual `start-execution`), a genuine replacement instance launch, and a genuine SSM-delivered restore command.

**Consequences.** This test found two real code defects (missing `s3:GetObject` on the instance role; the orchestrator's SSM restore command missing required `--dynamodb-table`/`--aws-region` flags) and two real operational gaps (replacement-instance bootstrap config must live on the launch template's default user-data, not a per-instance override; CRIU's exact-size regular-file check) that no combination of the earlier, piecewise-mocked phase tests had surfaced — see [Testing Strategy](../testing-strategy/technical.md) for the full account and [Limitations](../limitations/technical.md) for the resulting constraints.

## ADR-15: CRIU pinned version bumped from `v3.19` to `v4.2`

**Context.** The `v3.19` pin dated to Phase 2, when it was reached for as "a recent, real, tagged release" while working around Ubuntu 24.04's apt repo not carrying a `criu` package at all — not the result of a deliberate version comparison. Asked later why `3.19` specifically, the honest answer was exactly that: it worked once validated, so it stuck, but it was never actually the newest available release (`checkpoint-restore/criu` had moved on to a `4.x` series in the meantime).

**Decision.** Re-validate at `v4.2` (upstream's current stable tag at the time of this check) on a dedicated branch, using the same standard as ADR-14 — a real dump+restore cycle on both WSL2 and the exact real AL2023/Nitro EC2 hardware the original 3.17.1 segfault was found on, not just `criu check`. Concrete findings:
- `v4.2` builds **without** `WERROR=0` on both environments — the GCC-13 `-Werror=format-truncation` issue that forced that flag for `v3.19` appears to be fixed upstream.
- `v4.2` requires a **new** build dependency not needed by `v3.19`: `libuuid-devel` (RPM-based distros) / `uuid-dev` (Debian-based) — surfaced cleanly by CRIU's own `Makefile.packages` dependency check, not a cryptic failure.
- A real dump+restore cycle succeeded on both environments, via both raw `criu` commands and the actual `surviva daemon`/`surviva run`/`criu restore` code path, with no regressions found versus `v3.19`'s previously-verified behavior — including on the specific AL2023 kernel where the distro-packaged 3.17.1 had segfaulted.
- `v4.2`'s build additionally produces a `cuda_plugin.so` that `v3.19`'s did not; this project has not tested or made any claim about actual CUDA/GPU checkpoint support as a result — see [Limitations](../limitations/technical.md).

**Consequences.** `internal/criu/criu.go` required zero code changes — it shells out to whatever `criu` binary is on `PATH` using long-stable flags (`--tree`, `--images-dir`, `--log-file`, `--restore-detached`, `--pidfile`) that neither version changed. The only artifacts that needed updating were the three docs that named a specific version and build recipe ([Deployment](../deployment/technical.md), [Limitations](../limitations/technical.md), [Testing Strategy](../testing-strategy/technical.md)) and any AMI-baking automation built from those instructions. This ADR exists mainly to record that the original `3.19` pin was never a considered choice, and that bumping a pinned dependency version — even a "just a version bump" one — still warrants the same real dump+restore verification as picking it the first time, precisely because that is exactly how the 3.17.1 regression was found in the first place.
