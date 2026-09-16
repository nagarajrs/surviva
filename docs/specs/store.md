# Spec: `store` module

## Objective

A Go package giving `daemon` a single place to track jobs surviva is
protecting: assign each one an ID, record its status through its lifecycle,
and answer two questions the rest of the system needs — "what's active right
now" (`surviva list`) and "what happened to this specific job, ever"
(`surviva show <id>`). `daemon` is this package's only writer (per the
capability map); `surviva-cli` only ever reads through `daemon`, never opens
the database directly.

A SQL-backed job table with a status enum, deliberately simple.

SQLite (`modernc.org/sqlite`, pure Go, no cgo — no C toolchain needed to
cross-compile) is the default and the only backend most deployments will
ever need. MySQL is also supported, for the same reason Slurm's
`slurmdbd.conf` lets an admin point accounting storage at an external
database instead of a local file — see "Pluggable backend" below.

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

// ID is a sequential integer formatted as a string ("1", "2", "3", ...) --
// Slurm-style, easy to remember and type, assigned by Insert (SQLite
// AUTOINCREMENT), never chosen by the caller.
type Job struct {
    ID             string
    PID            int
    PGID           int
    CheckpointDir  string
    Command        []string
    WorkDir        string
    HookCheckpoint string // optional escape hatch for non-CRIU-friendly jobs
    HookResume     string
    Status         Status
    FailureReason  string // set on FAILED/CHECKPOINT_CREATION_FAILED/RESTORE_FAILED, empty otherwise
    Owner          string // OS user who ran `surviva run`/`join`, captured at registration
    RegisteredAt   time.Time
    UpdatedAt      time.Time
}

// HistoryEntry is one append-only record of a status transition. Backs
// `surviva show -history`.
type HistoryEntry struct {
    JobID      string
    FromStatus Status
    ToStatus   Status
    ChangedBy  string    // OS user for a CLI-driven change, "daemon" for an automatic one
    ChangedAt  time.Time
}
```

(Renamed `StatusCheckpointCreation` → `StatusCheckpointInProgress` from the
first draft so the Go identifier actually matches its `"CHECKPOINT_IN_PROGRESS"`
value — no behavior change.)

**`CheckpointDir` is a real column now** (reversing the first draft's "compute
it deterministically, don't store it" call): the caller decides this value and
`store` just persists it verbatim. `daemon` fills it in as
`filepath.Join(config.CheckpointBaseDir, jobID)` by default, or with whatever
path `surviva run --checkpoint-dir <path>` passed through, if the user
overrode it. `store` itself has no dependency on `config` and doesn't compute
or validate this path — it's an opaque string as far as this module is
concerned, matching the "store depends on nothing" line in the capability
map.

**Chicken-and-egg with sequential IDs:** the default `CheckpointDir` needs
the job's id, but the id isn't known until `Insert` assigns it. `Insert`
therefore takes a `Job` with `CheckpointDir` set only when the caller has an
explicit override; if not, the caller inserts first, gets the id back, then
calls the new `UpdateCheckpointDir` (below) with the now-computable default
path. Two round trips instead of one, but keeps `Insert`'s contract simple
(one `Job` in, one id out) rather than teaching it to compute paths itself.

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

## Pluggable backend (SQLite default, MySQL optional)

```go
type Options struct {
    Driver string // "sqlite" (default if empty) or "mysql"

    Path string // sqlite

    Host     string // mysql
    Port     int    // mysql
    User     string // mysql
    Password string // mysql, may be empty
    DBName   string // mysql
}

func Open(opts Options) (*Store, error)
```

`daemon` builds `Options` from `config.Config`'s `DBType`/`DBPath`/`DBHost`/
`DBPort`/`DBUser`/`DBPassword`/`DBName` (see [config.md](config.md)) —
`store` itself still has no dependency on `config`, it just takes a plain
struct.

MySQL was chosen over Postgres or a generic driver because it's the
lowest-friction option, not just because it matches Slurm's own real-world
choice: `github.com/go-sql-driver/mysql` uses the same `?` placeholders and
`Result.LastInsertId()` support SQLite already relies on, so every existing
query in this package (`Insert`/`Get`/`List`/`ListTerminal`/`UpdateStatus`/
`UpdatePID`/`UpdateCheckpointDir`) is completely unchanged — only `Open`'s
DSN construction and the `CREATE TABLE` DDL differ per driver:
- `id INTEGER PRIMARY KEY AUTOINCREMENT` (sqlite) vs.
  `id BIGINT PRIMARY KEY AUTO_INCREMENT` (mysql).
- `status` is `VARCHAR(64)` in the MySQL DDL instead of `TEXT` (it's always
  one of the nine known status strings), and no `DEFAULT ''` on the MySQL
  `TEXT` columns (older MySQL/MariaDB reject a default on `TEXT`/`BLOB`,
  and it's redundant — `Insert` always supplies an explicit value for every
  column, defaulted to `""` by Go's own zero value when unset).
- The MySQL DSN is built via the driver's own `mysql.Config{...}.FormatDSN()`
  (with `ParseTime: true`, so `RegisteredAt`/`UpdatedAt` scan straight into
  `time.Time` like they already do for SQLite) — never manual string
  concatenation, which breaks on a password containing `@`/`:`/`/`.
- `db.SetMaxOpenConns(1)` stays SQLite-only (`modernc.org/sqlite`'s own
  concurrent-writer limitation) — MySQL is built for real concurrent access
  and gets no such cap.
- `Open` calls `db.Ping()` for both drivers before applying the schema, so a
  bad MySQL host/credentials/network fails loudly at daemon startup, not on
  the first query later.

## API

```go
func (s *Store) Close() error

func (s *Store) Insert(j Job) (string, error)                         // ignores j.ID, returns the assigned sequential id
func (s *Store) Get(id string) (Job, error)                          // any status — backs `show`
func (s *Store) List() ([]Job, error)                                 // active only — backs `list`
func (s *Store) ListTerminal() ([]Job, error)                         // FAILED/CANCELED/COMPLETED only — backs `prune`
func (s *Store) UpdateStatus(id string, to Status, failureReason, changedBy string) error
func (s *Store) UpdatePID(id string, pid, pgid int, changedBy string) error // resume reusing the same job ID
func (s *Store) UpdateCheckpointDir(id, dir string) error             // fills in the default path once id is known
func (s *Store) History(id string) ([]HistoryEntry, error)            // every transition, oldest first — backs `show -history`

func IsTerminal(s Status) bool // exported so callers (surviva-cli) can decide how to compute a running duration
```

**Job audit trail** (who started a job, who/what changed its status, and
when): three questions the redesign wasn't originally answering that came up
in review — "are we capturing who ran/changed a job, and for how long has it
been running?" `Owner` answers "who initiated" (captured once, at `Insert`).
`changedBy` on `UpdateStatus`/`UpdatePID` answers "who/what changed it" — the
OS user for a CLI-driven pause/resume/cancel/complete, or the literal string
`"daemon"` for a status change the daemon makes on its own (the interruption
fan-out). Both `UpdateStatus` and `UpdatePID` write their `job_history` row
in the same SQL transaction as the status update itself, so every transition
is recorded automatically — no call site can forget to log one, since the
history write isn't a separate step callers have to remember. "How long has
it been running" is deliberately **not** a stored column: `surviva-cli`
computes it at display time (`time.Since(RegisteredAt)` while active,
`UpdatedAt.Sub(RegisteredAt)` once terminal — `IsTerminal` tells it which)
— a value this cheap to derive doesn't need its own update path.

`job_history` (both schemas): `id` (auto-increment PK), `job_id`,
`from_status`, `to_status`, `changed_by`, `changed_at`. Append-only — nothing
in this package ever updates or deletes a row here.

**In-place upgrade from before this existed:** `job_history` is a brand new
table, so `CREATE TABLE IF NOT EXISTS` creates it correctly on an upgrade.
`jobs.owner` is not — it's a column added to a table that already existed,
and `CREATE TABLE IF NOT EXISTS` is a no-op against an existing table, so it
never adds a missing column. `Open` runs one small migration after applying
the schema: `ALTER TABLE jobs ADD COLUMN owner ...` (per-dialect type), but
only if the column isn't already there (checked via `PRAGMA table_info`
for sqlite, `information_schema.columns` for mysql) — so a fresh database
and a fresh column both leave `Open` idempotent. This is deliberately the
minimal fix for the one column that needs it, not a general migrations
framework (versioned migration files, a schema-version table) — add one if
a second such column ever needs backfilling and this stops being a
one-off.

`Get`/`UpdateStatus`/`UpdatePID`/`UpdateCheckpointDir` all parse `id` into an
integer before querying (a job id that isn't a plain number is rejected with
a clear error, not a cryptic SQL failure) — the jobs table's `id` column is
`INTEGER PRIMARY KEY AUTOINCREMENT`, not `TEXT`; `Job.ID` stays a Go `string`
throughout the rest of the system (`ipc`, `daemon`, `surviva-cli`) purely so
nothing above `store` has to care that it's numeric underneath.

`ListTerminal()` is `List()`'s mirror image — `WHERE status IN
('FAILED','CANCELED','COMPLETED')` — backing `surviva prune` (see
[daemon.md](daemon.md)): it needs to find every terminal job's
`CheckpointDir` to delete, without touching the DB rows themselves (`prune`
clears checkpoint *files*, not job history — a pruned job stays fully
visible via `show`).

`List()` filters with `WHERE status NOT IN
('FAILED','CANCELED','COMPLETED')`. `Get()` returns a job regardless of
status, which is exactly `show`'s contract.

## Testing Strategy

`go test ./...` in this package, table-driven, `modernc.org/sqlite` against a
temp-file DB (no new test infra needed):
- Successive `Insert` calls return "1", "2", "3", ... in order.
- Insert → Get round-trips every field, `CheckpointDir` included.
- `Get`/`UpdateStatus`/`UpdatePID` reject a non-numeric job id cleanly.
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
- `Open` with an unrecognized `Options.Driver` fails immediately, before
  attempting any connection; `Options{}` (empty `Driver`) behaves exactly
  like an explicit `"sqlite"`.
- `UpdateStatus`/`UpdatePID` each append exactly one `job_history` row per
  call, in order, with the right `from`/`to`/`changed_by`; `History()`
  returns them oldest-first. `IsTerminal` agrees with the terminal-status set
  `List()`/`ListTerminal()` already use.
- `Open` against a jobs table seeded with the pre-`owner`-column schema
  (simulating an in-place upgrade) adds the column and lets `Insert` succeed
  — reproduced as a real failure against SQLite before the migration step
  existed, and separately verified by hand against real MySQL.

No real MySQL server is available for `go test` in this environment (the
project already accepts this constraint for CRIU/AWS — real-infra testing
happens manually). The MySQL DDL/DSN path is verified by hand against a
real MySQL instance instead — see [config.md](config.md)'s testing note and
the project's own WSL2-based manual verification practice.

## Boundaries

- **Always:** validate every status transition against the table above;
  never let `List()` and `Get()` diverge from the active/any-status split.
- **Ask first:** changing the status vocabulary itself (adding/renaming a
  status) — that's a contract change every other module reads; adding a
  third database backend beyond sqlite/mysql (confirmed scope with the
  user — Postgres and a generic-driver escape hatch were both explicitly
  deferred, not just unconsidered).
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
