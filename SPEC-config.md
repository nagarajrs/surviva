# Spec: `config` module

## Objective

A Go package that reads `surviva.conf` and hands `daemon` (and later,
`audit-log`) a validated, typed settings struct. Modeled directly on
`slurm.conf`: one plain-text file, `Key=Value` per line, read once at daemon
startup — not YAML/JSON, and not split across multiple files. New directives
get added to this same file as later modules need them; this module's job is
the parser and the extensibility, not the full eventual directive list.

## File Format

```
# surviva.conf — lines starting with # are comments, blank lines ignored
CloudProvider=aws
PollIntervalSeconds=5
AuditLogPath=/var/log/surviva/audit.log
CheckpointBaseDir=/var/lib/surviva/checkpoints
DBPath=/var/lib/surviva/jobs.db
```

- One `Key=Value` per line; whitespace around `=` trimmed.
- `#` as the first non-whitespace character makes the whole line a comment.
- Keys are **case-insensitive** (`cloudprovider`, `CloudProvider`, and
  `CLOUDPROVIDER` all match the same directive), matching Slurm's own
  convention. Matching is done by uppercasing both the parsed key and the
  known-key table before comparing; values are left exactly as written (no
  case-folding on values).
- An unknown key is a load-time error, not a warning — this file is only
  ever read by the exact binary version that ships with it, so a typo'd or
  stale key should fail loudly, not silently no-op.

## Directives (phase 1-2 set — only what `daemon`/`audit-log` need today)

| Key | Type | Required | Meaning |
|---|---|---|---|
| `CloudProvider` | string, one of `aws` (only implemented value today; `azure`/`gcp` are reserved names that parse but are rejected with "not yet implemented" until those daemon providers exist) | yes | Which interruption-signal poller `daemon` uses. |
| `PollIntervalSeconds` | positive int | yes | How often `daemon` checks the cloud provider's interruption endpoint. |
| `AuditLogPath` | path | yes | Where `audit-log` writes. |
| `CheckpointBaseDir` | path | yes | Base dir `daemon` computes each job's checkpoint dir under (`<base>/<job-id>`, same convention `legacy/internal/checkpoint.Dir` already used). |
| `DBPath` | path | yes | SQLite file `store` opens. |

All five are required — no defaults silently filled in for a daemon-critical
setting; missing any one is a load-time error naming which key is missing.

## API

```go
type Config struct {
    CloudProvider     string
    PollInterval      time.Duration
    AuditLogPath      string
    CheckpointBaseDir string
    DBPath            string
}

func Load(path string) (Config, error)

// DefaultPath returns /etc/surviva/surviva.conf unless overridden by
// SURVIVA_CONF — same env-var-override convention legacy/internal/ipc used
// for its own default paths.
func DefaultPath() string
```

`Load` parses, validates every key against the table above (unknown key →
error; missing required key → error; `CloudProvider` not in the supported
set → error), and returns a ready-to-use `Config` — never a partially-valid
one.

## Testing Strategy

`go test ./...`, table-driven against literal `surviva.conf` fixtures as Go
string constants (no temp files needed for the happy path, one temp file for
the actual-file-read path):
- A complete valid file loads correctly, all five fields populated.
- Each individually-missing required key produces a load error naming it.
- An unknown key produces a load error naming it.
- `CloudProvider=azure` (unimplemented) produces a clear "not yet
  implemented" error, not a generic parse failure.
- Comments and blank lines are ignored; a value with surrounding whitespace
  is trimmed.
- `cloudprovider=aws` / `CLOUDPROVIDER=aws` / `CloudProvider=aws` all parse
  identically (case-insensitive key match); the value itself is untouched.

## Boundaries

- **Always:** fail loudly and specifically (which key, why) rather than
  guessing or defaulting a daemon-critical setting.
- **Ask first:** adding support for a second config file / include
  mechanism, or switching the format away from flat key=value.
- **Never:** silently ignore an unknown key (masks typos); never read
  `surviva.conf` more than once per daemon run (no live-reload) unless
  asked.

## Success Criteria

- Package compiles standalone, no dependency on any other module.
- All fixture-based tests pass.
- Loading the example file above produces the exact struct shown.

## Open Questions

None remaining — both prior questions are resolved: keys are
case-insensitive, and `PollIntervalSeconds` stays a bare integer (no
duration-string support).
