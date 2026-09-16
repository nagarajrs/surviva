# Spec: custom checkpoint/resume hooks

Not a module in the capability map — this formalizes a cross-cutting
feature that already exists end-to-end across `store`, `daemon`, and
`surviva-cli` (`legacy`'s ADR-4, carried forward unchanged). This spec
exists because it never got a dedicated design doc of its own, and adds one
piece of real validation that was missing.

## Objective

An escape hatch for jobs CRIU can't handle (GPU state, certain sockets —
see `LIMITATIONS.md`) or where a user simply wants their own
application-specific save/restore mechanism instead of a generic
process-tree dump. A job registered with `-hook-checkpoint`/`-hook-resume`
uses those scripts instead of CRIU for that job, decided once at
registration time.

## Contract (unchanged from `legacy`)

- **Checkpoint hook**: invoked as `<script> <job-id> <checkpoint-dir>`.
  Must exit `0` on success; any other exit code is a checkpoint failure
  (`CHECKPOINT_CREATION_FAILED`, `FailureReason` set from the hook's
  combined output).
- **Resume hook**: invoked as `<script> <job-id> <checkpoint-dir>`. Must
  print the resumed process's PID as a single integer line on stdout; a
  non-integer or missing line is a resume failure (`RESTORE_FAILED`).
- `<checkpoint-dir>` is the job's `store.Job.CheckpointDir` (see
  `SPEC-store.md`) — the hook can read/write whatever files it wants there,
  same directory every time for that job.
- Both fields are optional and independent — a job can set both, either
  one, or neither (neither means "use CRIU" for that operation).

## The responsibility split that's only implicit in the code today

**CRIU's `dump` always stops the process it checkpoints, as a side effect —
a checkpoint hook does not get that for free.** If a hook wants the same
"paused" semantics (the process isn't running between checkpoint and
resume), the hook script itself is responsible for stopping whatever it's
managing, before it exits 0. Nothing in `daemon` does this on the hook's
behalf.

Relatedly: **surviva never passes a hook the tracked PID**, only the job id
and checkpoint dir. This is deliberate, not an oversight — a checkpoint hook
exists specifically for cases where a generic OS-level process dump doesn't
apply (an application with its own internal state, a GPU workload, etc.), so
the hook author's application is assumed to already have its own way to
find and control the thing it's checkpointing (a PID file it maintains, a
control socket, its own signal handler) rather than surviva improvising
process control on an arbitrary application's behalf.

## New: hook path validation at registration

Previously, any string in `-hook-checkpoint`/`-hook-resume` was accepted
with no checks — a typo'd or non-executable path was only discovered when
the job actually needed checkpointing, potentially during a real
interruption, minutes or hours after `surviva run` reported success.

`daemon`'s `Register` handler (`internal/daemon/daemon.go`,
`handleRegister`) now validates: if `HookCheckpoint` and/or `HookResume` is
non-empty, `os.Stat` it and check it's a regular file with at least one
executable bit set. Registration is refused with a clear error (naming
which field and why) if not. Empty fields (meaning "use CRIU" for that
operation) are never checked here — that's `criu.Available()`'s job, at
actual checkpoint/resume time, unchanged.

## Boundaries

- **Always:** validate a non-empty hook path before accepting a
  registration, not just when it's actually invoked.
- **Ask first:** changing the contract itself (extra arguments, env vars,
  JSON I/O instead of positional args/exit-code/stdout) — confirmed with
  the user this stays as-is for now.
- **Never:** pass the tracked PID to a hook, or have `daemon` stop the
  process on a checkpoint hook's behalf — both are the hook's own job.

## Testing Strategy

Unit tests in `internal/daemon` (alongside the existing `Register` tests):
- A nonexistent `HookCheckpoint` path is rejected.
- An existing but non-executable file is rejected.
- A real executable file is accepted.
- Empty `HookCheckpoint`/`HookResume` (the CRIU path) is never validated
  and never rejected on this basis.

The hook contract itself (exit code / stdout parsing) is already exercised
by `internal/checkpoint`/`internal/resume`'s own logic
(`checkpoint.Run`/`resume.Run`) and by `daemon`'s swapped-function tests —
no new tests needed there. See `hooks/README.md` for how a hook author
tests their own script standalone, without any of this.

## Success Criteria

- `go test ./...` covers all four validation cases above.
- Registering a job with a bogus hook path fails immediately with a clear
  error, both via `go test` and manually via `surviva run
  -hook-checkpoint /does/not/exist -- sleep 60`.
- `hooks/examples/*.sh` (see `hooks/README.md`) work end-to-end via
  `surviva run -hook-checkpoint ... -hook-resume ... -- <cmd>` followed by
  `pause`/`resume`, with no CRIU involved.

## Open Questions

None. Two related ideas were raised and explicitly deferred, not part of
this spec: extending the hook contract itself (richer args/env/JSON I/O),
and a matching fail-fast check for CRIU availability at registration time
when no hook is configured.
