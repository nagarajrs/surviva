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
    StatusRunning                  Status = "RUNNING"
    StatusCheckpointInProgress     Status = "CHECKPOINT_IN_PROGRESS"
    StatusCheckpointCreated        Status = "CHECKPOINT_CREATED"
    StatusCheckpointCreationFailed Status = "CHECKPOINT_CREATION_FAILED"
    StatusRestorePending           Status = "RESTORE_PENDING"
    StatusRestoreFailed            Status = "RESTORE_FAILED"
    StatusFailed                   Status = "FAILED"
    StatusCanceled                 Status = "CANCELED"
    StatusCompleted                Status = "COMPLETED"
)

type Job struct {
    ID             string
    PID            int
    PGID           int
    CheckpointDir  string
    Command        []string
    WorkDir        string
    HookCheckpoint string // optional, legacy's escape hatch for non-CRIU-friendly jobs
    HookResume     string
    Status         Status
    FailureReason  string // set on FAILED/CHECKPOINT_CREATION_FAILED/RESTORE_FAILED, empty otherwise
    RegisteredAt   time.Time
    UpdatedAt      time.Time
}
```

(Renamed `StatusCheckpointCreation` → `StatusCheckpointInProgress` from the
first draft so the Go identifier actually matches its `"CHECKPOINT_IN_PROGRESS"`
value — no behavior change.)

**`CheckpointDir` is a real column now** (reversing the first draft's "compute
it deterministically, don't store it" call): the caller decides this value at
`Insert` time and `store` just persists it verbatim. `daemon` fills it in as
`filepath.Join(config.CheckpointBaseDir, jobID)` by default, or with whatever
path `surviva run --checkpoint-dir <path>` passed through, if the user
overrode it. `store` itself has no dependency on `config` and doesn't compute
or validate this path — it's an opaque string as far as this module is
concerned, matching the "store depends on nothing" line in the capability
map.

### Status transitions

```
RUNNING ──checkpoint requested──> CHECKPOINT_IN_PROGRESS
RUNNING ──cancel─────────────────> CANCELED
RUNNING ──exits 0────────────────> COMPLETED
RUNNING ──dies/errors────────────> FAILED

CHECKPOINT_IN_PROGRESS ──checkpoint succeeds──> CHECKPOINT_CREATED
CHECKPOINT_IN_PROGRESS ──checkpoint fails─────> CHECKPOINT_CREATION_FAILED

CHECKPOINT_CREATION_FAILED ──retry checkpoint──> CHECKPOINT_IN_PROGRESS   (proposed — see Open Questions)
CHECKPOINT_CREATION_FAILED ──cancel────────────> CANCELED                 (proposed — see Open Questions)

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
func Open(path string) (*Store, error) // creates path's parent directory if missing
func (s *Store) Close() error

func (s *Store) Insert(j Job) error
func (s *Store) Get(id string) (Job, error)                          // any status — backs `show`
func (s *Store) List() ([]Job, error)                                 // active only — backs `list`
func (s *Store) ListTerminal() ([]Job, error)                         // FAILED/CANCELED/COMPLETED only — backs `prune`
func (s *Store) UpdateStatus(id string, to Status, failureReason string) error
func (s *Store) UpdatePID(id string, pid, pgid int) error             // resume reusing the same job ID
```

`ListTerminal()` is `List()`'s mirror image — `WHERE status IN
('FAILED','CANCELED','COMPLETED')` — added for the future `surviva prune`
command (see `SPEC-daemon.md`): it needs to find every terminal job's
`CheckpointDir` to delete, without touching the DB rows themselves (`prune`
clears checkpoint *files*, not job history — a pruned job stays fully
visible via `show`).

`List()` is `legacy`'s `List()` with one added clause: `WHERE status NOT IN
('FAILED','CANCELED','COMPLETED')`. `Get()` is unchanged from legacy — it
already returns a job regardless of status, which is exactly `show`'s
contract.

## Testing Strategy

`go test ./...` in this package, table-driven, `modernc.org/sqlite` against a
temp-file DB (matches legacy's approach — no new test infra needed):
- Insert → Get round-trips every field, `CheckpointDir` included.
- `List()` excludes each terminal status, includes each active one
  (`CHECKPOINT_CREATION_FAILED` and `RESTORE_FAILED` included, per the
  "active" default below).
- `ListTerminal()` returns exactly the inverse set: only
  `FAILED`/`CANCELED`/`COMPLETED`, none of the active statuses.
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
  `FAILED`/`CHECKPOINT_CREATION_FAILED`/`RESTORE_FAILED`) a non-empty
  `FailureReason`.

## Open Questions

None remaining.

1. ~~Are `CHECKPOINT_CREATION_FAILED` and `RESTORE_FAILED` "active" or
   "terminal" for `List()`?~~ **Confirmed active** — both stay in `list`
   until cancelled or retried, per the symmetric retry/cancel transitions
   above.
2. **`surviva join <PID>`, resolved into scope.** Confirmed: a future
   `surviva-cli` command to adopt an already-running, externally-started
   process into tracking (not launched via `surviva run`). `store`'s model
   needs no change for this — `Job` only ever required a `PID`/`PGID`, never
   assumed surviva itself started the process. One real nuance for whichever
   module implements `join`: `Command`/`WorkDir` won't be known from a
   `run`-style invocation, so they'll need to be populated best-effort (e.g.
   reading `/proc/<pid>/cmdline` and `/proc/<pid>/cwd` on Linux) or left
   empty — `store` accepts either, since neither field is validated
   non-empty here. Full command semantics belong in `surviva-cli`'s own spec.
