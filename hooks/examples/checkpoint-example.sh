#!/bin/sh
# Example checkpoint hook -- see ../README.md for the contract this must
# satisfy, and ../../docs/specs/hooks.md for the full spec.
#
# Invocation (surviva controls this, always exactly two arguments):
#   checkpoint-example.sh <job-id> <checkpoint-dir>
#
# Contract:
#   - exit 0 on success.
#   - any other exit code means the checkpoint failed; whatever this script
#     writes to stdout/stderr is captured as the failure reason.
#   - <checkpoint-dir> is this job's own directory, stable across
#     checkpoint/resume calls -- read/write whatever files you want there.

set -eu

job_id="$1"
checkpoint_dir="$2"

# --- Replace everything below with your application's real save logic. ---
#
# This toy example just records a timestamp and the job id -- nothing about
# a real process's state. A real hook would serialize whatever your
# application needs to pick back up later (a database snapshot, an
# in-memory state dump your app writes on request, etc.) into files under
# $checkpoint_dir.
mkdir -p "$checkpoint_dir"
{
	echo "job_id=$job_id"
	echo "checkpointed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} > "$checkpoint_dir/checkpoint.state"

# --- Pausing the tracked process is YOUR job, not surviva's. ---
#
# Unlike a real CRIU dump (which always stops the process it checkpoints as
# a side effect), a hook gets no such thing for free -- surviva never even
# tells this script the tracked process's PID (see ../../docs/specs/hooks.md for why).
# If you want the same "paused until resumed" semantics, your application
# needs its own way to find and stop itself -- e.g. reading a PID file it
# maintains on its own:
#
#   if [ -f /var/run/my-app.pid ]; then
#     kill -TERM "$(cat /var/run/my-app.pid)"
#   fi
#
# This example has no real process to stop, so it skips this step entirely.

exit 0
