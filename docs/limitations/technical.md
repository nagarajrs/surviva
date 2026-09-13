# Surviva Limitations (Technical)

Known constraints, verified against real testing (WSL2 with a source-built CRIU, real EC2 instances, and a genuine AWS Fault Injection Simulator Spot interruption). Each entry: what the limit is, why it exists, and the current mitigation. See `../design-record/technical.md` for the deeper rationale behind tradeoffs, and `../architecture/technical.md` for how the pieces referenced here fit together.

## 1. CRIU checkpoint coverage is incomplete by nature

GPU state, many kinds of open network sockets to external services, and some FUSE-backed mounts are known-unsupported by plain CRIU. surviva's mitigation is the `--hook-checkpoint`/`--hook-resume` escape hatch (see `../api-cli/technical.md`): a custom script fully replaces CRIU for a given job. A job with neither CRIU support nor a hook fails loudly — `internal/checkpoint`/`internal/resume` return an error, the daemon marks the job `FAILED` (or the restore's `fail()` helper marks it `FAILED` with a reason) — surviva never reports a checkpoint as complete when it isn't.

## 2. Open regular files must exist at the same path and size on the restore target

Discovered for real during FIS testing: a job's stdout was redirected to `/tmp/run.log` (74 bytes at dump time). Restoring on a genuinely different EC2 instance failed with:
```
Error (criu/files-reg.c:2175): File tmp/run.log has bad size 0 (expect 74)
Error (criu/files.c:1213): Unable to open fd=1 id=0x13
Error (criu/cr-restore.c:2557): Restoring FAILED.
```
This is CRIU's own consistency check on regular-file-backed fds, not a surviva bug — reproduced with a manually pre-created zero-byte file, and only succeeded once a file of the exact recorded size existed at that path. There is no code-level mitigation for this today; the operational guidance is to redirect a tracked job's stdout/stderr to `/dev/null`, or to a path inside the job's own checkpoint directory (which is what actually travels with the checkpoint, whether via S3 tarball or the EBS volume), never to an arbitrary local path with no guaranteed presence on a replacement instance.

## 3. Distro-packaged CRIU can be silently broken

Amazon Linux 2023's `dnf` package `criu-3.17.1-1.amzn2023.0.4` passes `criu check` ("Looks good.") but segfaults on `criu restore`:
```
Error (criu/cr-restore.c:1498): <pid> killed by signal 11: Segmentation fault
Error (criu/cr-restore.c:2536): Restoring FAILED.
```
Reproduced with plain `criu dump`/`criu restore` outside surviva entirely (a trivial `sleep` process), ruling out a surviva-specific cause. A source build of `checkpoint-restore/criu` tag `v4.2` (`make && make install-criu`, installing to `/usr/local/sbin`, which must precede `/usr/bin` on `PATH`) restores correctly on the identical instance/kernel — re-confirmed directly on real AL2023/Nitro hardware after an earlier source build of `v3.19` was superseded (see `../design-record/technical.md`). Mitigation: bake a known-good CRIU build into the AMI (see `../deployment/technical.md`); do not rely on `criu check` alone as a health signal — validate an actual dump+restore cycle on the target AMI/kernel, since the broken 3.17.1 package also printed "Looks good."

## 4. EBS checkpoint volumes are AZ-locked

An EBS volume can only attach within its own Availability Zone. `internal/remote/ebs.go`'s `ValidateEBSVolume` doesn't itself enforce this (it validates attachment/`DeleteOnTermination`), but the Terraform orchestrator's ASL (`DetermineAZ`/`HasAZOverride` states) must pin the replacement instance's subnet to the checkpoint volume's AZ via `az_subnet_map` when any restorable job is EBS-backed. This narrows Spot capacity for the replacement relative to an S3-backed job, which has no AZ constraint (`RunInstanceDefault` path, no subnet override).

## 5. EBS restore device discovery depends on the Nitro NVMe `by-id` convention

`internal/ebsmount` resolves `/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_<volume-id with its one dash removed>` and waits up to 30s for it to appear (`waitForDevice`), confirmed against real hardware (`vol-00e4b49c6dd5aaf88` → `nvme-Amazon_Elastic_Block_Store_vol00e4b49c6dd5aaf88` → resolved to `/dev/nvme1n1`). This is documented, stable AWS behavior on Nitro-based instance types with a modern udev configuration (present by default on current AL2023/Ubuntu AMIs), but is not universal — a non-Nitro instance type or a stripped-down AMI without the right udev rules would break this discovery with no fallback path currently implemented (no NVMe-ioctl-based identify, no raw device enumeration).

## 6. `DescribeVolumes` eventual consistency after `ModifyInstanceAttribute`

Observed directly while testing `DeleteOnTermination` validation: flipping the attribute via `ModifyInstanceAttribute` and immediately re-querying `DescribeVolumes` returned the *old* value for several seconds (`DescribeInstances` reflected the change faster than `DescribeVolumes` did, in the same test). Anything gating logic on a fresh `DescribeVolumes` read right after changing an attachment attribute should tolerate a multi-second propagation delay; `internal/remote/ebs.go`'s `ValidateEBSVolume` runs once at daemon startup, well after any such change would have settled, so it isn't itself affected — this is a note for anyone building similar automation.

## 7. EC2 "running" precedes SSM "Online" by tens of seconds

A real timing gap: the orchestrator's `DescribeInstances`-based wait for `running` succeeds well before the instance's SSM agent registers. An initial version without an explicit SSM-readiness wait failed `SendCommand` with `Ssm.InvalidInstanceIdException: Instances not in a valid state for account`. Fixed by adding a `WaitForSSMOnline`/`DescribeSSMInfo`/`IsSSMOnline` poll loop (`ssm:DescribeInstanceInformation`, checking `PingStatus = 'Online'`) between the running-check and the restore command send. Any similar automation should wait for both signals, not just instance state.

## 8. Replacement instance bootstrap config must be on the launch template's default user-data

`ec2:RunInstances` via a launch template does not carry over the original instance's user-data, and the orchestrator's own `RunInstanceInAZ`/`RunInstanceDefault` states set no `UserData` field. If the bucket/table configuration a booting daemon needs (see the `surviva-boot.sh` pattern in `../deployment/technical.md`) is only supplied as a one-off override on the *original* instance's launch (not the launch template's own default), the automatically-launched replacement gets none of it and its daemon crash-loops (`eval: line 5: syntax error near unexpected token 'newline'` when the boot script evaluates empty user-data content incorrectly, in the exact failure observed). `surviva restore`'s own DynamoDB table/region requirement is independently covered — the orchestrator's `state_machine.tf` appends `--dynamodb-table`/`--aws-region` directly onto the SSM command — but there's no equivalent for the daemon's own S3 bucket/table startup flags; those must live on the launch template.

## 9. JSONata-mode Step Functions has real syntactic sharp edges

Because the orchestrator uses only native `aws-sdk:*` integrations (no Lambda), all filtering/branching logic is JSONata expressions embedded in the ASL. Concretely hit and fixed:
- `arr[cond]` filtering to exactly one match returns a bare object, not a one-element array, unless forced with the `[]` suffix (`arr[cond][]`) — otherwise a `Map` state's `Items` field errors with "expected 'array', but was 'object'".
- `and`/`or` require both operands to already be strict booleans; `$lookup(map, key) != null` yields `undefined` on a miss (not `false`), which fails with `T0410: Argument 2 of function "and" does not match function signature` — use `$exists(...)` instead, and rely on short-circuit evaluation (confirmed working) to avoid ever calling `$lookup` with an invalid argument type.
- An `Assign` field can never resolve to `undefined` — a `$count(...) > 0 ? ... : ''`-style fallback (or `: []` for an array-typed variable) is required wherever the filtered expression might have zero matches.
- `$states.input`/`$states.result` only refer to the *current* state's own input/result — carrying a value across later states (including into and across `Map` iterations) requires an explicit `Assign`, not reliance on default input/output threading (see the `InitItem`/`$item` pattern in `templates/restore_orchestrator.asl.json.tftpl`).

Full narrative in `../design-record/technical.md`.

## 10. Restore is gated, opt-in, and not self-retrying

`surviva restore` refuses anything whose DynamoDB `status` isn't exactly `CHECKPOINT_COMPLETE` (exact message in `../api-cli/technical.md`). This is a deliberate safety property — restoring from `CHECKPOINT_INCOMPLETE`, `RESTORING`, `FAILED`, or an already-`RESTORED` record is never attempted automatically. The flip side: a failed restore attempt (marked `FAILED` with `failure_reason`) does not get retried by anything in the system; recovering requires a human to inspect the reason and, if appropriate, reset the record's status before invoking `surviva restore` again.

## 11. Local job store is single-writer by design

`internal/store` opens SQLite via `modernc.org/sqlite` (pure Go, no cgo — chosen so a cgo-less Windows dev environment could still build and cross-compile) with `SetMaxOpenConns(1)`, avoiding concurrent-writer lock errors at the cost of serializing all local store access. This is sized for the expected small number of concurrently tracked jobs per instance, not high-throughput job registration.

## 12. Checkpoint concurrency is intentionally capped

`--max-concurrent-checkpoints` (default `runtime.NumCPU()`) bounds `internal/daemon`'s worker pool. Fully parallel dumping of many large process trees risks saturating disk/CPU badly enough that none finish inside the ~2-minute interruption window. Jobs are fed to the pool in priority order (`store.List`'s `ORDER BY priority DESC`), so under a tight concurrency cap and a hard deadline, lower-priority jobs may simply never get checkpointed — this is accepted, deliberate behavior, not a bug.

## 13. Linux-only; Windows/other builds are stubs

`internal/procattr`, `internal/fdguard`, `internal/sigset`, and `internal/ebsmount` each have a `_other.go` build-tag variant for non-Linux targets: `procattr`/`sigset` fall back to no-op-equivalent behavior suitable only for local development, while `ebsmount`'s non-Linux `MountForRestore` returns an explicit `"EBS restore is only supported on Linux"` error. The project's own development happened partly on Windows via WSL2, but WSL2 runs a real Linux kernel — this was never a claim that surviva works natively on Windows.

## 14. One orchestrator per launch template

The Terraform module's Step Function/EventBridge rule pair is scoped to a single `launch_template_id` (a required variable) — it does not discover "what launched the interrupted instance" dynamically. Multiple fleets with different launch templates require multiple deployments of the module (distinct `name_prefix`s to avoid resource-name collisions).

## 15. Some orchestrator IAM permissions are necessarily broad

`iam.tf`'s state machine policy grants `ec2:RunInstances`, `ec2:DescribeInstances`, `ec2:DescribeVolumes`, and `ec2:AttachVolume` on `resources = ["*"]` — none of these APIs support scoping to a replacement instance/volume ARN that doesn't exist yet at policy-authoring time. `iam:PassRole` is scoped via the `aws:PassedToService = ec2.amazonaws.com` condition rather than to a specific resource for the same reason. This is a known, accepted least-privilege compromise, not an oversight.
