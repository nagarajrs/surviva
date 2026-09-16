# Spec: `audit-log` module

## Objective

A small Go package giving both `surviva-cli` and `daemon` one place to
record "what happened": every CLI command a person runs, and every
daemon-side activity (interruption detected, checkpoint attempted, restore
attempted). Append-only, plain-text-adjacent (JSON Lines), no query
interface — capturing the trail is the requirement; reading it back is
`cat`/`jq`'s job, not this module's, unless asked for later.

**Dependency note:** the capability map lists `audit-log` as depending on
`config` — that's a *wiring* dependency, not a Go import. This package takes
a plain file path in `Open`; whichever caller wires it up (`daemon`,
`surviva-cli`) is the one that reads `config.AuditLogPath` and passes it
through. `audit-log` itself imports nothing from `config` or `store`, so it
stays as independently testable as they are.

## Format

One JSON object per line (JSONL), appended to the file at
`config.AuditLogPath`:

```json
{"time":"2026-09-16T04:12:03Z","component":"cli","action":"pause","job_id":"job-abc123","outcome":"ok","detail":""}
{"time":"2026-09-16T04:12:05Z","component":"daemon","action":"checkpoint_started","job_id":"job-abc123","outcome":"ok","detail":""}
{"time":"2026-09-16T04:12:07Z","component":"daemon","action":"checkpoint_failed","job_id":"job-abc123","outcome":"error","detail":"criu dump: disk full"}
```

- `component`: `"cli"` or `"daemon"` — which side emitted the entry.
- `action`: a short, free-form string the caller chooses (`run`, `pause`,
  `resume`, `list`, `show`, `cancel`, `join`, `checkpoint_started`,
  `checkpoint_succeeded`, `checkpoint_failed`, `interruption_detected`,
  `restore_started`, `restore_succeeded`, `restore_failed`, etc.) — not an
  enum here, since `daemon` and `surviva-cli` aren't specced yet and
  shouldn't be locked to a vocabulary this module invents on their behalf.
- `job_id`: empty string for anything not tied to a specific job (e.g. "daemon
  started", "config loaded").
- `outcome`: `"ok"` or `"error"`.
- `detail`: free text — an error message, a one-line args summary, whatever
  the caller finds useful. Empty string if nothing to add.

No rotation, no compression, no remote sink (syslog/CloudWatch/etc.) built
into this package — see Rotation below for why that's a deliberate choice,
not just deferral.

## Rotation (sustainability)

The file is expected to grow forever unless something external rotates it.
That something is **OS-level `logrotate`, not code in this package**, and
specifically its `copytruncate` mode:

```
/var/log/surviva/audit.log {
    daily
    rotate 14
    copytruncate
    missingok
    notifempty
}
```

Why not rotate in-app (rename-and-start-fresh on a size/time trigger): this
file has multiple independent OS-process writers at once — the long-running
`daemon` and a brand-new `surviva-cli` process on every single invocation.
Any rename-based rotation scheme requires every writer to notice the
rotation and reopen the file; that's manageable for one long-lived daemon
but not for arbitrary short-lived CLI processes without adding real
machinery (a lock file, a signal, an identity check before every write) —
exactly the complexity this module exists to avoid. `copytruncate` sidesteps
the problem entirely: it copies the file out, then truncates the *original*
in place, so every already-open file descriptor — including one a CLI
process opened a second ago — keeps appending to the same inode with zero
interruption or lost lines, no reopen or signal required anywhere. Sizing
and scheduling ("100MB or daily, whichever first") is then just a
`logrotate` config, not something `audit-log` needs to know about.

This means: nothing in `Open`/`Log` changes to support rotation, and a
sample `logrotate` stanza (as above) belongs in deployment docs once those
exist for the new design, not in this package.

## API

```go
type Entry struct {
    Time      time.Time // auto-filled with time.Now().UTC() if zero
    Component string    // "cli" or "daemon"
    Action    string
    JobID     string // optional
    Outcome   string // "ok" or "error"
    Detail    string // optional
}

func Open(path string) (*Logger, error) // creates parent dirs if needed, opens for append
func (l *Logger) Close() error
func (l *Logger) Log(e Entry) error     // marshals e as one JSON line, appends
```

`Open` uses `O_APPEND|O_CREATE|O_WRONLY`. Multiple OS processes (a
long-running `daemon` and short-lived `surviva-cli` invocations) each hold
their own `*Logger` against the same file — POSIX guarantees an `O_APPEND`
write under `PIPE_BUF` (4096 bytes on Linux) doesn't interleave with another
process's concurrent append, so entries never get corrupted or spliced
together as long as one JSON line stays under that. No cross-process locking
is implemented; a single audit entry is never expected to approach 4KB.

`Log`'s error is the caller's to interpret — e.g. `daemon` should log-and
continue on an audit-write failure (a full disk logging audit trail
shouldn't block an actual checkpoint), while that's each caller's call, not
this module's.

## Testing Strategy

`go test ./...`, temp-file-backed:
- `Log` appends a well-formed JSON line; reading the file back and
  unmarshaling each line succeeds.
- Multiple `Log` calls on one `Logger` append multiple lines, in order.
- An `Entry` with a zero `Time` gets stamped with (approximately) `now`.
- Two independent `Logger`s opened against the same path, appending
  interleaved, produce a file where every line is still individually valid
  JSON (no splicing) — the concurrent-writer guarantee above, exercised for
  real rather than just asserted.

## Boundaries

- **Always:** append-only — never rewrite, truncate, or reorder existing
  lines.
- **Ask first:** adding a remote sink or a query/read API. (Rotation is
  already decided — see above — not something to revisit without a concrete
  reason `logrotate`/`copytruncate` stops being sufficient.)
- **Never:** block a caller for long (this is a single buffered append, not
  a network call); never make an audit-write failure implicitly fatal
  inside this package — that's the caller's decision.

## Success Criteria

- Package compiles standalone, no dependency on `config`, `store`,
  `daemon`, or `surviva-cli`.
- All tests above pass, including the concurrent-append one.
- A file opened by two processes at once never ends up with a malformed
  (unparseable) line under normal entry sizes.

## Open Questions

None — this module is small and self-contained enough that the format and
API above should be final. Flag anything during implementation if it turns
out otherwise.
