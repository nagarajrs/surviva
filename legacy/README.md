# Legacy surviva (pre-redesign, v1.3)

This is the original surviva implementation, moved here wholesale when the
`dev` branch started a clean-slate redesign (see the repo root for the new
module layout and specs).

It's kept as working reference, not dead weight: the daemon+CLI+CRIU+AWS
storage design here was tightly coupled (one daemon owns process tracking,
IPC, CRIU, and AWS-specific S3/EBS/DynamoDB storage all at once), which made
it hard to build or test any one piece in isolation — the reason for the
redesign. Specific parts of it are being carried forward directly into the
new modules rather than rewritten from scratch:

- `internal/criu`, `internal/checkpoint`, `internal/resume` — CRIU dump/
  restore wrapping, reused as-is (or near enough) in the new `daemon` module
- `internal/procsignal` — process-group signaling, reused as-is in `daemon`
  (`internal/idgen`, job-id generation, was reused briefly but then dropped
  entirely: the new `store` assigns Slurm-style sequential integer ids via
  SQLite `AUTOINCREMENT` instead of random ones — see `SPEC-store.md`)
- `internal/imds` — AWS Spot signal polling, wrapped behind the new
  `daemon`'s pluggable cloud-provider interface
- `internal/procattr`, `internal/fdguard` — CRIU session/fd hygiene needed
  when *starting* a tracked process, so these belong in the future
  `surviva-cli` module (whichever command execs the child), not `daemon`
- `internal/store`, `internal/job` — SQLite job store and status enum, close
  in spirit to the new `store` module
- `docs/design-record`, `docs/limitations` — decisions and gotchas (CRIU
  `--leave-running`, the AWS NVMe device-discovery convention, etc.) that are
  still true regardless of the new architecture

This binary still builds and runs as-is (`go build ./...` from inside this
directory) if you need to compare behavior against the new implementation.
