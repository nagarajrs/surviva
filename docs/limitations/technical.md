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

## 16. Restoring at the exact original PID can lose a race against the replacement instance's own boot

CRIU restores a checkpointed process at its *original* PID — that PID number must be completely unused on the restore target at restore time, or restore fails outright:
```
Error (criu/cr-restore.c:1230): Can't fork for 1771: File exists
Error (criu/cr-restore.c:2324): Restoring FAILED.
```
Discovered for real via the ansible sandbox (`../../ansible/`) firing a genuine FIS Spot interruption in EBS storage mode: the replacement instance boots from the same AMI as the original, so it tends to allocate PIDs to its own early services (SSM agent, udev, the checkpoint-volume mount step, etc.) in a similar range to whatever the original had already reached by the time the tracked job forked — a low-PID job (something that forked early in the original instance's own boot) has a real, non-negligible chance of colliding with whatever the replacement's boot has, by then, assigned that same number to. Repeated tests of the identical scenario showed this is a probabilistic collision, not a deterministic one: most restores succeed. There is no code-level mitigation today (CRIU offers no "restore at a different PID" mode for an unprivileged single-process restore of this kind) — a failed restore surfaces exactly like any other restore failure (`FAILED` status, `failure_reason` populated, see limitation 10) and is not automatically retried.

## 17. Attaching an EBS checkpoint volume to a replacement instance can race the original instance's own detach

Found the same way as limitation 16: the orchestrator's `AttachVolume` state can run before EC2 has finished detaching the volume from the just-terminated original instance, since Spot interruption → instance termination → volume detach isn't instant relative to the orchestrator's own `WaitForCheckpoints` timer:
```
Ec2.Ec2Exception: vol-xxxxxxxx is already attached to an instance
```
Mitigated with a bounded `Retry` on that state (`ErrorEquals: ["Ec2.Ec2Exception"]`, 10 attempts, 10s interval, 1.3x backoff — `templates/restore_orchestrator.asl.json.tftpl`); Step Functions' direct AWS SDK integrations don't expose a more specific error code than the service-level exception class, so the retry is necessarily broader than just this one transient condition, trading a slightly slower failure for a much higher success rate on the common case.

## 18. A tracked command attached to an interactive terminal can't be checkpointed at all

Found for real: a user ran `surviva run -- ./some-script.sh` directly in an interactive SSM/SSH session (not backgrounded), then triggered a real interruption. `criu dump` failed outright:
```
Error (criu/tty.c:410): tty: Found slave peer index 2 without correspond master peer
Error (criu/cr-dump.c:2128): Dumping FAILED.
```
`surviva run` (`cmd/surviva/run.go`) wires the tracked child's stdin/stdout/stderr directly to its own (inherited from whatever invoked it); putting the child in its own session (`internal/procattr`) detaches it from a *controlling* terminal, but doesn't change what those fds actually point to. If they're still a live pty, CRIU refuses to dump it. Reproduced directly against a real instance, independent of surviva, with a plain `pty.spawn()`-backed process.

Worse, in S3 storage mode this fails completely silently from the outside: `internal/daemon`'s `pushRemote` only writes anything to DynamoDB *after* the local dump succeeds (see limitation 19), so a dump failure here leaves no DynamoDB record and no S3 object at all -- indistinguishable from a job that was simply never tracked. There's no code-level fix (CRIU has no supported way to dump/restore an inherited live tty as a regular fd); the mitigation is operational: always redirect a tracked command's stdio away from an interactive terminal before it can be checkpointed, e.g. `nohup surviva run -- ./script.sh < /dev/null > /dev/null 2>&1 & disown`.

## 19. S3 storage mode gives no visibility into a checkpoint that fails before the upload starts

EBS mode writes an `IN_PROGRESS` DynamoDB record before its local dump even begins (`recordEBSStart`), specifically so a mid-dump failure is still visible as something other than silence. S3 mode's `pushRemote` has no equivalent: `baseRecord`/`Put` only happens after `checkpoint.Run` (the local CRIU dump) already succeeded. A dump failure in S3 mode -- for any reason, not just limitation 18's tty case -- leaves zero trace in DynamoDB. The orchestrator's `QueryJobs` step, and anyone inspecting the table, cannot distinguish "this job was never tracked" from "this job's checkpoint failed before upload." Matching EBS mode's early record write would close this gap but hasn't been done.

## 20. Ctrl+C to a tracked command may not reach it, and a bash script child ignores SIGINT by default

Two compounding issues, found together for real: a user pressed Ctrl+C on a tracked `./counter.sh`, intending to cancel it; the script ran to completion anyway, and `surviva list` kept showing it `RUNNING` long after the process had actually exited.

First: the tracked child runs in its own session (`internal/procattr`), so a terminal's Ctrl+C (SIGINT) — delivered only to the terminal's foreground process group — reaches `surviva run` itself, never the child. Go's default action for an unhandled SIGINT terminates the receiving process immediately, so without a handler, `surviva run` died before `cmd.Wait()` could return and deregister the job, leaving the child running unattended and the daemon's record stuck at `RUNNING` forever (nothing else in the system reconciles a tracked PID against whether it's still actually alive).

Second, confirmed by direct reproduction independent of surviva entirely: a bash script run as a backgrounded/asynchronous command (`cmd &` — which is exactly how a session-detached tracked child ends up running) sets its own SIGINT/SIGQUIT disposition to ignored. This is a documented POSIX shell rule, not a surviva or CRIU quirk — `kill -INT` against such a script's process group genuinely does nothing, while `kill -TERM` against the identical process terminates it immediately.

Fixed in `cmd/surviva/run.go`: catches SIGINT/SIGTERM and forwards **SIGTERM** (never the literal signal received) to the child's process group (`internal/procsignal`). Verified for real: a tracked bash script now dies and is cleanly deregistered on SIGINT to the wrapper, matching what a user pressing Ctrl+C expects.

## 21. A checkpointed *script*, not just its stdout/stderr, must exist at the same path on the restore target

A specific, easy-to-hit case of limitation 2's general rule, found for real: a user tracked `./counter.sh` (a bash script, invoked by relative path) whose stdio was already correctly backgrounded to `/dev/null`. Checkpoint succeeded — S3 upload confirmed, DynamoDB showed the record durable — but restore on the replacement instance still failed:
```
Error (criu/files-reg.c:2353): Can't open file root/counter.sh on restore: No such file or directory
Error (criu/cr-restore.c:1262): <pid> killed by signal 9: Killed
Error (criu/cr-restore.c:2324): Restoring FAILED.
```
Bash keeps the script file itself open as a file descriptor for as long as it's executing it, exactly like any other open regular file CRIU records — the replacement instance's own copy of the AMI simply never had `/root/counter.sh` on it (the user had created it by hand on the original instance only). The single DynamoDB `status` field only ever shows the *latest* transition (here, `FAILED`, from the restore step) — it's not a history log, so "was this a checkpoint or a restore failure" has to be read from `failure_reason` (here, clearly naming `criu restore`) and whether `s3_uri`/`size_bytes` are populated (they were, confirming the checkpoint itself succeeded), not from the status value alone.

No code-level fix — same operational mitigation as limitation 2: anything a tracked command needs on disk (its own script file included, if invoked by path rather than baked into the AMI as a compiled binary) must already exist identically on any instance that might ever restore it, not just the one it started on.

## 22. SSM RunCommand's CloudWatch output needs `logs:CreateLogGroup` on the bare log-group ARN, even when the group already exists

Found wiring up CloudWatch Logs for troubleshooting (`infra/terraform/cloudwatch.tf`): granting the instance role `logs:CreateLogStream`/`PutLogEvents`/`DescribeLogStreams` scoped to `<log-group-arn>:*` (the correct scoping for those, stream-level, actions) was not sufficient for SSM's `CloudWatchOutputConfig` — commands still ran successfully, but no log stream for their output ever appeared, with no error visible from `send-command`/`get-command-invocation` at all. The actual cause only showed up in the instance's own `/var/log/amazon/ssm/amazon-ssm-agent.log`:
```
ERROR ... Error Creating Log Group for CloudWatchLogs output: AccessDeniedException: ... not authorized to perform: logs:CreateLogGroup on resource: arn:aws:logs:<region>:<account>:log-group:<name> because no identity-based policy allows the logs:CreateLogGroup action
```
The SSM agent unconditionally attempts `logs:CreateLogGroup` against the *bare* group ARN (no `:*` suffix) before writing anything, regardless of whether the group already exists — and a resource pattern ending `:*` does not match the bare ARN (the literal `:` immediately before the wildcard has nothing to match against). Fixed by granting `logs:CreateLogGroup` explicitly against the bare ARN alongside the existing stream-level grant against the `:*` form. Worth knowing for any similar SSM/CloudWatch wiring: check the *agent's own* local log when delivery silently doesn't happen, not just the command's own result.

## 23. A locally-paused checkpoint only survives on the instance that made it

`surviva pause -local <job-id>` writes the checkpoint to local disk only: no S3 upload, no EBS durability record, and no DynamoDB row. That trades away exactly the durability the default (S3/EBS) storage mode exists to provide, in exchange for a checkpoint/resume round trip that needs no AWS configuration at all. If the instance is terminated, reclaimed, or otherwise lost before `surviva resume` runs, that checkpoint is gone — there is no record anywhere else that it ever existed, and nothing for `surviva restore` to find on a replacement instance.

This is a deliberate tradeoff, not a bug: `-local` is for a person who wants to pause and resume a job themselves, on their own timeline, on the same box — not for surviving Spot reclamation, which is what the default storage mode (and automatic, interruption-triggered checkpointing) is for. `surviva resume` itself has the mirror-image limitation: it only ever reads the local checkpoint directory on the instance it's run on, so it cannot help a job whose local images were already cleaned up or that was already resumed on a different instance via `surviva restore` — `surviva restore` is the only path that works once a different instance is involved, and only for checkpoints that actually made it to S3/EBS+DynamoDB.
