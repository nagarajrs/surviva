# Spec: `store` module

## Objective

A Go package giving `daemon` a single place to track jobs surviva is
protecting: assign each one an ID, record its status through its lifecycle,
and answer two questions the rest of the system needs — "what's active right
now" (`surviva list`) and "what happened to this specific job, ever"
(`surviva show <id>`). `daemon` is this package's only writer (per the
capability map); `surviva-cli` only ever reads through `daemon`, never opens
the database directly.

This is almost entirely a trim of `legacy/internal/store` +
`legacy/internal/job`, which already do the "SQLite-backed job table with a
status enum" job correctly — see Boundaries for exactly what changes.

## Data Model

```go
type Status string

const (
    StatusRunning           Status = "RUNNING"
    StatusCheckpointCreated Status = "CHECKPOINT_CREATED"
    StatusRestorePending    Status = "RESTORE_PENDING"
    StatusRestoreFailed     Status = "RESTORE_FAILED"
    StatusFailed            Status = "FAILED"
    StatusCanceled          Status = "CANCELED"
    StatusCompleted         Status = "COMPLETED"
)

type Job struct {
    ID             string
    PID            int
    PGID           int
    Command        []string
    WorkDir        string
    HookCheckpoint string // optional, legacy's escape hatch for non-CRIU-friendly jobs
    HookResume     string
    Status         Status
    FailureReason  string // set on FAILED/RESTORE_FAILED, empty otherwise
    RegisteredAt   time.Time
    UpdatedAt      time.Time
}
```

No `CheckpointDir` column: the checkpoint path stays deterministic —
`filepath.Join(daemonCheckpointBaseDir, jobID)` — computed by `daemon` from
its own config, exactly as `legacy/internal/checkpoint.Dir` already does. One
less thing that can drift out of sync with reality.

### Status transitions

```
RUNNING ──checkpoint──> CHECKPOINT_CREATED
RUNNING ──cancel──────> CANCELED
RUNNING ──exits 0──────> COMPLETED
RUNNING ──dies/errors──> FAILED

CHECKPOINT_CREATED ──resume requested──> RESTORE_PENDING
CHECKPOINT_CREATED ──cancel────────────> CANCELED

RESTORE_PENDING ──resume succeeds──> RUNNING   (same job ID, new pid/pgid)
RESTORE_PENDING ──resume fails─────> RESTORE_FAILED

RESTORE_FAILED ──retry resume──> RESTORE_PENDING
RESTORE_FAILED ──cancel────────> CANCELED
```

`FAILED`, `CANCELED`, `COMPLETED` are terminal — no transition leaves them.
`Store` enforces this table (`ValidTransition(from, to Status) bool`); an
invalid transition is a caller bug, not a recoverable runtime condition, so
`UpdateStatus` returns an error rather than silently applying it.

## API

```go
func Open(path string) (*Store, error)
func (s *Store) Close() error

func (s *Store) Insert(j Job) error
func (s *Store) Get(id string) (Job, error)                          // any status — backs `show`
func (s *Store) List() ([]Job, error)                                 // active only — backs `list`
func (s *Store) UpdateStatus(id string, to Status, failureReason string) error
func (s *Store) UpdatePID(id string, pid, pgid int) error             // resume reusing the same job ID
```

`List()` is `legacy`'s `List()` with one added clause: `WHERE status NOT IN
('FAILED','CANCELED','COMPLETED')`. `Get()` is unchanged from legacy — it
already returns a job regardless of status, which is exactly `show`'s
contract.

## Testing Strategy

`go test ./...` in this package, table-driven, `modernc.org/sqlite` against a
temp-file DB (matches legacy's approach — no new test infra needed):
- Insert → Get round-trips every field.
- `List()` excludes each terminal status, includes each active one.
- Every edge in the transition table above succeeds; a handful of invalid
  ones (e.g. `COMPLETED → RUNNING`, `CANCELED → CHECKPOINT_CREATED`) are
  rejected.
- `UpdatePID` on a `RESTORE_PENDING` job resuming to `RUNNING` — new pid
  readable back via `Get`.

## Boundaries

- **Always:** validate every status transition against the table above;
  never let `List()` and `Get()` diverge from the active/any-status split.
- **Ask first:** changing the status vocabulary itself (adding/renaming a
  status) — that's a contract change every other module reads.
- **Never:** let anything outside `daemon` write to this store (no exported
  constructor for a second writer); no automatic pruning of terminal jobs —
  they stay in the DB forever unless someone explicitly asks for retention
  limits later.

## Success Criteria

- Package compiles standalone, no dependency on `config`, `daemon`, or
  `surviva-cli`.
- All transition-table tests pass.
- A job that reaches `FAILED`/`CANCELED`/`COMPLETED` is absent from `List()`
  but still returned by `Get(id)` with its final status and (for
  `FAILED`/`RESTORE_FAILED`) a non-empty `FailureReason`.

## Open Questions

1. **Is `RESTORE_FAILED` "active" or "terminal" for `List()`?** Taking it as
   *active* here (it's stuck and needs attention, not finished — someone
   should still see it in `surviva list` until they cancel or retry it).
   Say so now if you want it to behave like a terminal state instead.
2. **Job identity for non-process targets.** Right now `PID`/`PGID` assume
   the tracked thing is always an OS process surviva itself started via
   `run`. If pause ever needs to target an *externally* started process (not
   launched via `surviva run`), the model already supports it (just a PID),
   so no change needed unless something else comes up.
