# Surviva Testing Strategy (Technical)

How this project was actually verified, phase by phase, and what's recommended going
forward. For what the system does, see [`../architecture/technical.md`](../architecture/technical.md).
For deployment steps, see [`../deployment/technical.md`](../deployment/technical.md). For the
bugs this testing found and *why* specific design choices exist, see
[`../design-record/technical.md`](../design-record/technical.md). For what remains a known
limitation rather than something fixable, see [`../limitations/technical.md`](../limitations/technical.md).

## Guiding philosophy

Verify against real behavior wherever practical, escalating in realism as the stakes rose:
mocked/local → real-but-temporary AWS resources → real cross-instance behavior → a genuine
AWS Fault Injection Simulator (FIS) Spot interruption against a real running instance. No
phase relied on assuming a mock's behavior matched the real service; every AWS-touching
piece was, at some point, exercised against the real API.

## Phase 1-3: CLI, daemon, checkpointing core

Development happened on Windows, which has no Linux kernel for CRIU to run against.
Rather than skip real CRIU testing, **WSL2** (a genuine Linux kernel) was used instead of
mocking `criu` itself. Ubuntu 24.04's own apt repos didn't carry a `criu` package at all for
that release, so CRIU 3.19 was built from source:

```
git clone --depth 1 --branch v3.19 https://github.com/checkpoint-restore/criu.git
make WERROR=0   # newer GCC's stricter -Werror=format-truncation breaks the older build otherwise
```

Verified: `criu check` passing, then a real `dump`/`restore` cycle against a plain `sleep`
process. This surfaced two real bugs, both fixed as a direct result of this testing (see
`../design-record/technical.md` items 1-3):

- A stray inherited pty fd (leaked in from the invoking shell/`nohup` chain, lacking
  `CLOEXEC`) silently broke `criu dump` ("Task attached to shell terminal") — fixed by
  `internal/fdguard`.
- A race between `surviva run`'s wait loop (seeing its child die because `criu dump`
  stopped it) and the daemon's own concurrent checkpoint bookkeeping for the same job —
  fixed by gating the deregister handler on job status.

A tiny mock IMDS server (a standalone throwaway Go program, never committed to the repo)
simulated the EC2 IMDSv2 token endpoint plus the rebalance-recommendation and
interruption-notice endpoints, toggle-controlled by touching flag files. This let the
daemon's real IMDS-polling code (`internal/imds`) run unmodified against a fake backend,
without needing a real EC2 instance for early-phase iteration.

Parallel checkpointing (Phase 3) was verified with a deliberately slow hook script logging
start/end timestamps to a shared file, proving genuine wall-clock overlap: three jobs
completed in ~3s total, not ~9s sequential. Concurrency capped at 1 was used separately to
prove strict priority ordering (highest-priority job's log lines appeared first, in order).

## Phase 5-6: S3/DynamoDB, EBS

Mocking S3/DynamoDB would only prove the mock worked, so real (but deliberately
**temporary**) AWS resources were created for every test run and deleted immediately after:

- **Multipart upload correctness**: pushed an incompressible ~30MB payload (large enough to
  force real multipart, not a single `PutObject`) and confirmed via `head-object`'s `ETag`
  having a `"<hash>-N"` suffix — proof of N real parts, not a coincidence of a single-part
  upload's plain 32-hex-char ETag.
- **Incomplete-upload marking**: pointed the daemon at a nonexistent bucket to force a real
  `NoSuchBucket` failure, confirmed the DynamoDB record lands on `CHECKPOINT_INCOMPLETE`
  with a `failure_reason`, never silently `CHECKPOINT_COMPLETE`.
- **EBS `DeleteOnTermination` validation**: a real temporary EC2 instance + EBS volume,
  testing all four real states by actually creating/attaching/modifying the volume via the
  AWS CLI (never by mocking the `DescribeVolumes` response): unattached (rejected —
  "not attached to any instance"); attached with `DeleteOnTermination=true` (rejected);
  attached to a different instance id (rejected — mismatch); attached with
  `DeleteOnTermination=false` (accepted). A real `DescribeVolumes`-vs-`ModifyInstanceAttribute`
  eventual-consistency gap was observed here (see `../limitations/technical.md`).
  Terminating the instance afterward and confirming the volume survived as `available`
  (not deleted) was used as final proof the flag had actually worked.

## Phase 7: Step Functions orchestrator

The full orchestrator (real Terraform-deployed DynamoDB table + GSI, IAM roles, state
machine, EventBridge rule) was deployed for real, then driven via direct
`aws stepfunctions start-execution` calls with synthetic input mimicking the EventBridge
event shape — deliberately bypassing the need for an actual Spot interruption at this
phase, since the goal was validating the **state machine's own logic**, not the trigger
path (that's what the FIS capstone test was for). The JSONata iteration bugs documented in
`../design-record/technical.md` (array-vs-object unwrapping, strict-boolean `and`/`or`,
`Assign` rejecting `undefined`, `$states.input`/`$states.result` scoping) were each caught
by real `create-state-machine`/`start-execution`/`update-state-machine` API errors, then
fixed and re-verified against the same real API — not guessed blind from documentation.

Both storage-mode branches were exercised for real: EBS (AZ pinning via `az_subnet_map`,
real `AttachVolume`, the attach-state wait loop) and S3 (default AZ, `AttachVolume` never
called). The zero-restorable-jobs no-op path (`NoRestorableJobs` → `Succeed`) was also
exercised directly.

## Phase 8: `surviva restore`

**S3-mode** restore was fully verified in WSL2 by literally simulating "a different
machine": after a real checkpoint had been pushed to a real (temporary) S3 bucket, a
completely separate daemon process, Unix socket, SQLite database, and checkpoint directory
were used to run `surviva restore` — confirming the full round trip for both the CRIU path
and the hook-resume path, plus the safety-refusal paths (restoring a nonexistent job id;
restoring a job whose status had already moved past `CHECKPOINT_COMPLETE`).

**EBS-mode** restore needed a real EC2 instance — WSL cannot attach real EBS volumes. The
test: dump a real checkpoint directly onto a real mounted+attached volume; seed its
DynamoDB record by hand; **unmount** the volume (forcing genuinely blind rediscovery, not
reuse of an already-mounted path); run `surviva restore` and confirm it resolved
`/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_<volume-id-without-its-dash>` to the
correct device, remounted it, and restored correctly.

This is also where the **AL2023 distro-CRIU segfault** was found and root-caused: `criu
restore` segfaulted (`killed by signal 11`) using the `dnf`-packaged criu 3.17.1. This was
reproduced with plain `criu dump`/`criu restore` shell commands entirely outside surviva,
proving it wasn't a surviva bug, then resolved by building criu 3.19 from source on that
same instance and confirming `criu.LookPath` picks up `/usr/local/sbin/criu` ahead of the
distro's `/usr/sbin/criu` (verified via `which -a criu`). This also gave the
`RestoreFailed` path a real exercise: the genuine CRIU crash was correctly recorded as
`FAILED` with its restore-log-derived reason, never silently left looking safe.

## Capstone: full AWS Fault Injection Simulator test

Built a proper AMI (CRIU 3.19 + the surviva binary + a systemd unit + a small boot script
reading config from EC2 user-data) rather than risk a boot-time install/compile race
against the orchestrator's SSM restore command arriving first. Deployed the real Terraform
orchestrator, launched a genuine Spot instance from that AMI/launch template, tracked a
real job, then ran an FIS experiment template:

```json
{
  "targets": {
    "instance-target": {
      "resourceType": "aws:ec2:spot-instance",
      "resourceArns": ["arn:aws:ec2:...:instance/<id>"],
      "selectionMode": "ALL"
    }
  },
  "actions": {
    "send-interruption": {
      "actionId": "aws:ec2:send-spot-instance-interruptions",
      "parameters": { "durationBeforeInterruption": "PT2M" },
      "targets": { "SpotInstances": "instance-target" }
    }
  },
  "stopConditions": [{ "source": "none" }],
  "roleArn": "arn:aws:iam::...:role/<fis-role>"
}
```

with the FIS role using the AWS-managed `AWSFaultInjectionSimulatorEC2Access` policy.

This produced, for the first time, entirely genuine unscripted signals:

- A real IMDS **rebalance recommendation**, handled proactively — the daemon's checkpoint
  completed and was pushed to S3 *before* the harder interruption notice even arrived.
  This validated the "listen to both signals" design (see `../design-record/technical.md`)
  under real conditions for the first time ever — every earlier test had only ever toggled
  one mock flag at a time.
- A real interruption notice arriving afterward, correctly resulting in a no-op
  ("no tracked jobs" — the job was already past `RUNNING`), proving the status-arbitration
  logic holds under genuinely concurrent real signals, not just simulated ones.
- A real native `EC2 Spot Instance Interruption Warning` EventBridge event that triggered
  the Step Functions execution **completely automatically** — no manual
  `start-execution` this time.
- A real replacement instance launched automatically by the state machine.
- An SSM-delivered restore command, automatically dispatched.

Two real bugs were found here that no piecewise phase test had caught, because each
piecewise test had (necessarily) supplied by hand something that, in the fully automatic
path, was missing:

1. The instance role only granted `s3:PutObject`, not `s3:GetObject` — the replacement
   instance (same role, same launch template) couldn't download what the daemon had
   pushed. Fixed in `infra/terraform/iam.tf`.
2. The orchestrator's SSM command was just `surviva restore <job_id>`, missing the
   `-dynamodb-table`/`-aws-region` flags restore requires — the replacement instance has no
   other way to learn which table to read. Fixed in `infra/terraform/state_machine.tf`.

Two further, non-bug operational findings were confirmed (detailed in
`../limitations/technical.md`): the replacement instance's bootstrap config must live on
the launch template's own default user-data (not something the orchestrator can inject
per-replacement), and CRIU's exact-file-size check on a tracked job's open regular files
(e.g. a redirected stdout log) means that file must exist, with matching size, on the
restore target.

After applying the two fixes (and working around the two operational findings for that
specific run — resetting DynamoDB status to retry, and creating a same-sized placeholder
log file), the loop closed for real: the process resumed under its **original PID** on a
genuinely separate instance, and the job was marked `RESTORED`.

Every real resource created for this test — 2 EC2 instances, 1 AMI + its backing snapshot,
1 launch template, the FIS experiment template + its IAM role, all Terraform-managed infra
(DynamoDB table, S3 bucket, IAM roles, state machine, EventBridge rule), and the S3 binary
staging bucket — was deleted afterward and confirmed gone via an account-wide
`describe-instances`/`describe-images`/`list-tables`/`list-buckets`/`iam list-roles` sweep.

## Post-launch: CRIU version upgrade (3.19 → 4.2)

After the project was otherwise complete, CRIU was re-evaluated at tag `v4.2` (upstream's
current stable release; `v3.19` was over a year old by then) to check whether the pin was
still the right call. Done on a dedicated branch, with the same "verify for real, not just
`criu check`" standard as everywhere else in this project:

- **WSL2**: `v4.2` built cleanly **without** `WERROR=0` — the GCC-13 `-Werror=format-truncation`
  issue that forced that flag on `v3.19` appears fixed upstream. A real dump+restore cycle of
  a plain `sleep` process succeeded, both via raw `criu` commands and through the actual
  `surviva daemon`/`surviva run` code path (tracked a job, triggered a real checkpoint via
  the mock IMDS server, restored it, original PID preserved).
- **Real AL2023/Nitro EC2** — the exact environment the 3.17.1 segfault was originally found
  on: the `v4.2` build failed at first with a new required dependency not needed by `v3.19`:
  `libuuid-devel` (RPM) / `uuid-dev` (Debian), surfaced by CRIU's own
  `criu/Makefile.packages` dependency check, not a cryptic compiler error. Once added, the
  build succeeded, `criu check` passed, and — the test that actually matters, since `criu
  check` also passed on the broken 3.17.1 package — a real dump+restore cycle succeeded on
  this exact hardware/kernel, again both via raw `criu` commands and through the real daemon
  checkpoint pipeline. No regression versus `v3.19`'s behavior on the same instance type.
- One incidental finding, not yet exercised further: `v4.2`'s build now produces a
  `cuda_plugin.so` that `v3.19` did not — CRIU has gained some CUDA/GPU-related capability
  since 3.19, though this project has not tested or made any claim about actual GPU
  checkpoint support (see `../limitations/technical.md`, item 1).

All temporary AWS resources created for this round (1 EC2 instance, IAM role/profile, S3
staging bucket) were deleted and confirmed gone afterward. Full rationale for the version
choice and switch is in `../design-record/technical.md`.

## What is explicitly NOT covered yet

- **No automated regression test suite or CI pipeline.** Every verification above was a
  manual, real-infrastructure exercise run once during development — not a repeatable
  automated test that would catch a future regression in this code.
- **No load/scale testing** — not exercised with hundreds of concurrently tracked jobs, or
  checkpoints in the many-GB range (the largest tested checkpoint was ~30MB, chosen
  specifically to force multipart upload, not to probe an upper bound).
- **No multi-fleet / multiple-launch-template deployment test** — the orchestrator's
  one-Step-Function-per-launch-template design (see `../limitations/technical.md`) has
  only been exercised with exactly one launch template.
- **No test of a real non-trivial hook script** — `--hook-checkpoint`/`--hook-resume` were
  only exercised with simple test scripts (a marker-file writer, a plain `sleep`
  restarter), not a real complex external checkpoint mechanism (e.g. a GPU-aware one).
- **No chaos/failure-injection testing of the orchestrator's own failure modes** — e.g.
  DynamoDB throttling, the Step Function's own execution timeout being hit, `RunInstances`
  failing due to capacity.
- **FIS testing was done once**, against a single instance/job, in a single AZ/instance
  type; not repeated across multiple AZs, instance types, or under concurrent multi-job
  load.

## Recommendations going forward

- **Unit tests for the pure-logic pieces** that don't need a real Linux kernel or AWS:
  `internal/idgen`, `internal/store` (SQLite CRUD), `internal/ipc` (protocol encode/decode),
  `internal/checkpoint`/`internal/resume` (path construction, hook-invocation argument
  shape) — these can run on any CI runner, including Windows, with no CRIU/AWS dependency.
- **A CI job with CRIU installed** (a Linux container or a Firecracker/WSL2-like runner),
  exercising a real `criu dump`/`restore` of a trivial process on every change to
  `internal/criu`/`internal/checkpoint`/`internal/resume`, to catch regressions without
  needing AWS at all.
- **A scheduled (e.g. monthly) real FIS-based end-to-end test** against a throwaway
  environment, kept as the ultimate regression check — this project's own history shows
  piecewise/mocked testing structurally cannot catch the class of bug the FIS test caught
  (cross-service IAM gaps, timing gaps between real AWS signals, orchestrator-vs-instance
  config propagation).
- **`terraform validate`/`plan` in CI** for `infra/terraform/`, plus a static IAM-policy
  linter (e.g. `tflint`, `checkov`) given the intentionally broad `resources = ["*"]` on a
  few orchestrator actions (documented in `../limitations/technical.md`).
