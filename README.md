# surviva

Checkpoint/restore protection for AWS Spot instance interruptions — built
module by module, Slurm-inspired (a `surviva.conf` you configure like
`slurm.conf`, sequential job ids, a pluggable job-table backend), instead
of one tightly-coupled daemon that does everything at once.

`surviva run` wraps a long-running command; when a Spot interruption
signal arrives, `surviva daemon` checkpoints it (via CRIU, or your own
custom hook for anything CRIU can't handle) so it can be resumed later —
by hand (`surviva pause`/`resume`), or by wiring `NotifyTargetType` in
`surviva.conf` to hand the interruption off to your own AWS Lambda or Step
Functions logic.

## Quick start

```bash
go build -o surviva ./cmd/surviva

cat > surviva.conf <<EOF
CloudProvider=aws
PollIntervalSeconds=5
AuditLogPath=./audit.log
CheckpointBaseDir=./checkpoints
DBPath=./jobs.db
EOF

SURVIVA_CONF=./surviva.conf SURVIVA_SOCKET=./surviva.sock ./surviva daemon &

export SURVIVA_CONF=./surviva.conf SURVIVA_SOCKET=./surviva.sock
./surviva run -- sleep 300 &
./surviva list
./surviva pause <job-id>
./surviva resume <job-id>
```

(CRIU checkpoint/resume needs Linux + root — see
[`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) for installing CRIU itself and
running this as a real systemd service, or run
[`deploy/install.sh`](deploy/install.sh) to automate it end to end.)

## Learn more

- [`docs/CAPABILITY_MAP.md`](docs/CAPABILITY_MAP.md) — the module index:
  what each piece (`store`, `config`, `audit-log`, `daemon`, `surviva-cli`)
  does and its full design spec.
- [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) — building CRIU, installing
  the systemd unit, IAM for the optional Lambda/Step Functions notify.
- [`docs/LIMITATIONS.md`](docs/LIMITATIONS.md) — real gotchas found
  building and testing this (e.g. why a process attached to an interactive
  terminal can't be checkpointed at all).
- [`hooks/README.md`](hooks/README.md) — writing a custom checkpoint/resume
  hook for anything CRIU can't handle (GPU state, application-specific
  save/restore).
