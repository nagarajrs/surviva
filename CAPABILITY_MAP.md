# Surviva Redesign — Capability Map

Clean-slate redesign of surviva as independently specced and buildable
modules, replacing the single AWS-coupled daemon in `legacy/` (see
`legacy/README.md` for why, and what's being carried forward).

| Module id | Responsibility | Depends on | Spec |
|---|---|---|---|
| `store` | Job model + SQLite persistence: job ID generation, status enum and transitions, the retention rule (terminal jobs drop off `list`, stay queryable via `show`). | — | [SPEC-store.md](SPEC-store.md) |
| `config` | `surviva.conf` loader (flat key=value, slurm.conf-style): cloud provider identity, audit-log location, polling interval. Extensible for later directives. | — | [SPEC-config.md](SPEC-config.md) |
| `audit-log` | Persistent record of every `surviva` command invocation and daemon-side activity. | `config` | not yet written |
| `daemon` | Long-running process: sole writer to `store`, does CRIU pause/resume, polls the configured cloud provider's interruption signal (AWS first), writes `audit-log`. | `store`, `config`, `audit-log` | not yet written |
| `surviva-cli` | `surviva run`/`pause`/`resume`/`list`/`show` (and likely `cancel`, and `join <pid>` to adopt an already-running external process into tracking). Talks to the running `daemon` for anything job-related. | `daemon`, `audit-log` | not yet written |

**Build order:** `store` + `config` (parallel) → `audit-log` → `daemon` →
`surviva-cli`.

**Explicitly deferred** (not modules, added later as `surviva.conf`-driven
extensions once the above are solid): durable remote checkpoint storage
(S3/EBS), cross-instance status tracking (DynamoDB), cross-instance
orchestrated restore (Step Functions/SSM).

Each module spec is reviewed and approved before that module's own Plan
phase starts, per `spec-driven-development`. This file is the index — update
the table (spec link, new modules) as each one lands, don't let it drift.
