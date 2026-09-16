# Spec: `surviva-cli` module

## Objective

The user-facing command surface: `surviva run`/`pause`/`resume`/`list`/
`show`/`cancel`/`prune`/`join`, plus `surviva daemon` (wiring the `daemon`
module into an actual running process). Everything job-related talks to the
running `daemon` over `internal/ipc`; this module owns no state of its own
beyond what a single command invocation needs in memory, and it does its own
audit logging (`component: "cli"`) per `SPEC-audit-log.md`'s division of
responsibility.

## Reused from `legacy/` (as-is)

| Package | Used by |
|---|---|
| `internal/procattr` (`New() *syscall.SysProcAttr`, `Setsid: true`) | `run` — starts the tracked child as its own session/process-group leader, PGID == PID. |
| `internal/fdguard` (`CloseInherited()`) | `run` — closes this process's own inherited fds (close-on-exec) before starting the child, so a stray pty/socket fd can't leak in and break CRIU later. |
| `internal/sigset` (`TermSignals()`) | `daemon` subcommand — which OS signals mean "shut down." |

`internal/procsignal.KillGroup` is also used here (forwarding Ctrl-C/SIGTERM
to the tracked child while `run` waits on it) — already reused by `daemon`
too, no change.

## Command surface

| Command | Daemon action | Notes |
|---|---|---|
| `surviva run [flags] -- <cmd> [args...]` | `Register`, then `Complete` on exit | Starts `<cmd>` itself; see below. |
| `surviva join [flags] <pid>` | `Register` | Adopts an already-running external process; see below. |
| `surviva pause <job-id>` | `Pause` | |
| `surviva resume <job-id>` | `Resume` | |
| `surviva list [-json]` | `List` | Active jobs only. |
| `surviva show [-json] [-history] <job-id>` | `Show` | Any status; `-history` also prints every recorded status transition. |
| `surviva cancel <job-id>` | `Cancel` | |
| `surviva prune [job-id]` | `Prune` | No id = all terminal jobs. |
| `surviva daemon [flags]` | — | Runs the daemon itself; see below. |

All job-id-taking commands share `-socket` (default `ipc.DefaultSocketPath()`)
and exit codes `0`/`1`/`2` (success/failure/usage), matching `legacy`'s
convention.

## `surviva run`

Same sequence `legacy/cmd/surviva/run.go` already proved out, with one
change (`Complete` needs an exit code, `legacy`'s `Deregister` didn't):

1. Parse flags: `-hook-checkpoint`, `-hook-resume`, `-checkpoint-dir`
   (optional override, forwarded as `RegisterJob.CheckpointDir`), `-socket`.
2. `cmd.SysProcAttr = procattr.New()`; `fdguard.CloseInherited()`; `cmd.Start()`.
3. Forward SIGINT/SIGTERM to the child's process group as SIGTERM (same
   reasoning as `legacy`: the child's own session means a terminal's Ctrl-C
   never reaches it directly, and a backgrounded shell script ignores
   SIGINT for itself regardless — a POSIX rule, not a surviva quirk).
4. `client.Register(..., requestedBy)`, where `requestedBy` is
   `currentOSUser()` (`os/user.Current().Username`, falling back to
   `"unknown"` on error) — recorded by `daemon` as the job's `Owner` and as
   the first `job_history` entry's `changed_by`. Daemon unreachable or
   refusing (already interrupted) → warn to stderr, continue running the
   command unprotected — never block it.
5. `cmd.Wait()`.
6. If registered: `client.Complete(jobID, exitCode, errMsg, requestedBy)` — `exitCode` from
   `exec.ExitError.ExitCode()` if the command exited nonzero, `0` otherwise;
   `errMsg` empty unless `cmd.Wait()` itself failed for a reason other than
   the child's own exit status (e.g. an I/O error), in which case that
   error's text goes in `errMsg`.
7. Exit with the same code the child exited with.

**Dropped from `legacy`, deliberately: `-priority`.** `store.Job` carries no
priority field in this redesign (checkpoint fan-out ordering isn't a
concern this phase — see Open Questions). Not adding the flag back until
`store` grows one; add both together if it turns out to matter.

## `surviva join <pid>`

Adopts a process surviva did not start. Same `Register` action, differently
built request:
- `Command`/`WorkDir`: best-effort from `/proc/<pid>/cmdline` and
  `/proc/<pid>/cwd` (Linux only — this is already a Linux-only production
  tool, per `legacy`'s own stance). Empty if unreadable; `store` doesn't
  require them non-empty (per `SPEC-store.md`'s note on this exact case).
- `PID`/`PGID`: **`join` refuses unless the process is already its own
  process-group leader** (`PGID == PID`, checked via `syscall.Getpgid`).
  Reason: `Cancel` signals `-PGID` (the whole group) via
  `procsignal.KillGroup`, which is safe only because `run`-started jobs are
  always isolated into their own session (`procattr.Setsid`). A `join`ed
  process that shares a group with an unrelated shell or job would put
  those processes at risk of being killed too on `cancel`. See Open
  Questions — this materially limits what `join` can adopt.

## `surviva list` / `surviva show`

Table for `list` (`JOB ID`, `PID`, `STATUS`, `DURATION`, `COMMAND`), a full
key:value dump for `show` (every `store.Job` field including `Owner`,
`CheckpointDir` and `FailureReason` when set, plus a computed `Duration`
line). Both take `-json` for the raw `store.Job`/`[]store.Job` the daemon
returned (`show -json` additionally wraps in `duration` and, with
`-history`, `history`), same convention as `legacy`'s `list -json`.

`Duration` is computed by the CLI, not stored: `time.Since(RegisteredAt)`
while the job is active, `UpdatedAt.Sub(RegisteredAt)` once it's terminal
(`store.IsTerminal` tells `jobDuration` which) — see `SPEC-store.md`'s job
audit trail section for why this isn't a `store` column.

`surviva show -history <job-id>` additionally requests
`Request.IncludeHistory` and prints every recorded transition (timestamp,
from → to status, changed by) below the regular detail dump, oldest first.

## `surviva daemon [flags]`

Wires the `daemon` module into a running process:
1. `config.Load(*confPath)` (flag default `config.DefaultPath()`).
2. `store.Open(cfg.DBPath)`, `auditlog.Open(cfg.AuditLogPath)`.
3. Build the provider: only `cfg.CloudProvider == "aws"` exists today →
   `aws.New(cfg.PollInterval)`.
4. `daemon.New(daemon.Config{Store, Audit, Provider, cfg.CheckpointBaseDir, cfg.MaxConcurrentCheckpoints})`.
5. `signal.NotifyContext(context.Background(), sigset.TermSignals()...)`,
   `d.Run(ctx, ipc.DefaultSocketPath())`.

## Audit logging

Every subcommand opens its own `auditlog.Logger` against
`cfg.AuditLogPath` (so it needs to `config.Load` too, just for that one
field — everything else in `Config` is `daemon`-only) and logs exactly one
entry per invocation: `{component: "cli", action: "<subcommand>", job_id,
outcome, detail}`. `detail` carries the error message on failure, empty on
success. This is the CLI's own record of "a command was run" — separate
from and never duplicating `daemon`'s own activity log entries (checkpoint/
restore attempts, interruption detection), per `SPEC-audit-log.md`.

## Testing Strategy

**As implemented, simpler than first specced:** rather than a fake
`ipc.Client`-shaped interface, each command's non-trivial logic was
extracted into a small pure function tested directly, without needing a
daemon connection at all — `exitCodeAndErr` (`run`'s exit-code/message
computation, exercised via a real re-exec'd subprocess for both a clean and
a nonzero exit), `buildJoinRequest` (`join`'s group-leader refusal), and
`renderJobList`/`renderJobDetail` (table and `-json` output, including a
round-trip through `encoding/json`, and — for `renderJobDetail` — that
history entries print when given and are absent otherwise). Every other command
(`pause`/`resume`/`cancel`/`prune`) is a straight-line "parse flags, call
the client, print one line" with no branching worth a dedicated unit test —
a fake-client interface for those would be untested scaffolding, so it
wasn't added (see `daemonClient`'s removal from `common.go`: written per
this spec's original wording, then deleted once nothing used it). The full
loop (start a daemon, run a command against it, checkpoint/resume for real)
is manual/integration on a real Linux box with CRIU, same as `daemon`'s own
testing strategy.

## Boundaries

- **Always:** every job-related command reaches the daemon only through
  `internal/ipc` — never opens `store`'s database or `audit-log`'s file for
  job data directly (audit-log writes are the one exception, by design).
- **Ask first:** adding back a priority/ordering flag (touches `store` too);
  relaxing `join`'s process-group-leader requirement.
- **Never:** block a tracked command's own execution on the daemon being
  reachable (`run` degrades to unprotected + a warning, never refuses to
  run the command).

## Success Criteria

- `surviva run -- sleep 5` tracks, waits, and reports exit code 0, with a
  job visible in `list` while it runs and gone (but `show`-able) once it
  exits.
- `surviva join <pid>` succeeds for a `nohup`'d/setsid'd process and refuses
  with a clear message for an ordinary shell-job process.
- `surviva daemon` started against a real `surviva.conf` accepts
  connections and answers `ping`.

## Open Questions

None remaining.

1. ~~`join`'s process-group-leader requirement.~~ **Confirmed:** `join`
   refuses to adopt a process that isn't already its own process-group
   leader, exactly as specced above.
2. ~~No priority/ordering flag.~~ **Confirmed: left out** for this phase.
