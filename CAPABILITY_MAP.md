# Surviva Redesign — Capability Map

Clean-slate redesign of surviva as independently specced and buildable
modules, replacing the single AWS-coupled daemon in `legacy/` (see
`legacy/README.md` for why, and what's being carried forward).

| Module id | Responsibility | Depends on | Spec | Status |
|---|---|---|---|---|
| `store` | Job model + SQLite persistence: job ID generation, status enum and transitions, the retention rule (terminal jobs drop off `list`, stay queryable via `show`). | — | [SPEC-store.md](SPEC-store.md) | Built (`internal/store`) |
| `config` | `surviva.conf` loader (flat key=value, slurm.conf-style): cloud provider identity, audit-log location, polling interval. Extensible for later directives. | — | [SPEC-config.md](SPEC-config.md) | Built (`internal/config`) |
| `audit-log` | Persistent record of every `surviva` command invocation and daemon-side activity. | `config` (wiring only, see spec) | [SPEC-audit-log.md](SPEC-audit-log.md) | Built (`internal/auditlog`) |
| `daemon` | Long-running process: sole writer to `store`, does CRIU pause/resume, polls the configured cloud provider's interruption signal (AWS first), writes `audit-log`. Defines the Unix-socket protocol (`internal/ipc`) `surviva-cli` talks to. | `store`, `config`, `audit-log` | [SPEC-daemon.md](SPEC-daemon.md) | Built (`internal/daemon`, `internal/ipc`, `internal/daemon/provider/aws`, plus reused `internal/criu`/`checkpoint`/`resume`/`procsignal`/`idgen`/`imds`) |
| `surviva-cli` | `surviva run`/`pause`/`resume`/`list`/`show`/`cancel`/`prune`/`join <pid>`, plus `surviva daemon` (wires the `daemon` module into a running process). | `daemon`, `audit-log` | [SPEC-surviva-cli.md](SPEC-surviva-cli.md) | Spec approved, not yet implemented |

**Build order:** `store` + `config` (parallel) → `audit-log` → `daemon` →
`surviva-cli`.

**Explicitly deferred** (not modules, added later as `surviva.conf`-driven
extensions once the above are solid): durable remote checkpoint storage
(S3/EBS), cross-instance status tracking (DynamoDB), cross-instance
orchestrated restore (Step Functions/SSM).

Each module spec is reviewed and approved before that module's own Plan
phase starts, per `spec-driven-development`. This file is the index — update
the table (spec link, new modules) as each one lands, don't let it drift.
