# Surviva — Capability Map

Surviva is built as independently specced, buildable modules — the map
below is the index of what exists, the design spec for each, and the
build order they were developed in.

| Module id | Responsibility | Depends on | Spec | Status |
|---|---|---|---|---|
| `store` | Job model + persistence (SQLite by default, MySQL optional — see spec), status enum and transitions, the retention rule (terminal jobs drop off `list`, stay queryable via `show`), an append-only status-change history. | — | [specs/store.md](specs/store.md) | Built (`internal/store`) |
| `config` | `surviva.conf` loader (flat key=value, slurm.conf-style): cloud provider identity, audit-log location, polling interval. Extensible for later directives. | — | [specs/config.md](specs/config.md) | Built (`internal/config`) |
| `audit-log` | Persistent record of every `surviva` command invocation and daemon-side activity. | `config` (wiring only, see spec) | [specs/audit-log.md](specs/audit-log.md) | Built (`internal/auditlog`) |
| `daemon` | Long-running process: sole writer to `store`, does CRIU pause/resume, polls the configured cloud provider's interruption signal (AWS first), writes `audit-log`, optionally notifies a Lambda/Step Function on interruption. Defines the Unix-socket protocol (`internal/ipc`) `surviva-cli` talks to. | `store`, `config`, `audit-log` | [specs/daemon.md](specs/daemon.md) | Built (`internal/daemon`, `internal/ipc`, `internal/daemon/provider/aws`, `internal/daemon/notify/aws`, plus `internal/criu`/`checkpoint`/`resume`/`procsignal`/`imds`) |
| `surviva-cli` | `surviva run`/`pause`/`resume`/`list`/`show`/`cancel`/`prune`/`join <pid>`, plus `surviva daemon` (wires the `daemon` module into a running process). | `daemon`, `audit-log` | [specs/surviva-cli.md](specs/surviva-cli.md) | Built (`cmd/surviva`, plus `internal/procattr`/`fdguard`/`sigset`/`procgroup`/`procinfo`) |

**Build order:** `store` + `config` (parallel) → `audit-log` → `daemon` →
`surviva-cli`.

**Explicitly deferred** (not modules, added later as `surviva.conf`-driven
extensions once the above are solid): durable remote checkpoint storage
(S3/EBS), cross-instance status tracking (DynamoDB), cross-instance
orchestrated restore (Step Functions/SSM) *owned by surviva itself* — the
`NotifyTargetType`/`NotifyTargetARN` notify hook (see
[specs/daemon.md](specs/daemon.md)) is related but distinct: surviva hands
off a JSON payload on interruption, the *user's own* Lambda/Step Function
does any orchestration, surviva still owns none of it.

Each module spec is reviewed and approved before that module's own Plan
phase starts, per `spec-driven-development`. This file is the index — update
the table (spec link, new modules) as each one lands, don't let it drift.

**Cross-cutting specs** (not modules of their own, but formalize a feature
spanning several of the above):

- [specs/hooks.md](specs/hooks.md) — custom checkpoint/resume hooks
  (`-hook-checkpoint`/`-hook-resume`), spanning `store`, `daemon`, and
  `surviva-cli`. See also [../hooks/README.md](../hooks/README.md) and
  `hooks/examples/` for writing one.

See [LIMITATIONS.md](LIMITATIONS.md) for real gotchas found building and
testing this (currently: a tracked process attached to an interactive
terminal can't be checkpointed at all — a CRIU constraint, not a bug).

See [../README.md](../README.md) for a quick start and
[DEPLOYMENT.md](DEPLOYMENT.md) for building CRIU, installing the systemd
unit, and IAM for the optional notify target.

CI (`.github/workflows/ci.yml`) runs `gofmt`/`go build`/`go vet`/`go test
./...` plus a shell-syntax check on every push/PR — a floor, not full
coverage: CRIU/MySQL/AWS all need real infra per each module's own testing
strategy above, so that verification stays manual, same as it's always been.
