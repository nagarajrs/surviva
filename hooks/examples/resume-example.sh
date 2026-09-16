#!/bin/sh
# Example resume hook -- see ../README.md for the contract this must
# satisfy, and SPEC-hooks.md at the repo root for the full spec.
#
# Invocation (surviva controls this, always exactly two arguments):
#   resume-example.sh <job-id> <checkpoint-dir>
#
# Contract:
#   - on success, print the resumed process's PID as a single integer line
#     on stdout -- nothing else on stdout, or surviva can't parse it.
#   - exit 0 on that success.
#   - any other exit code (or unparseable stdout) means the resume failed;
#     stderr/combined output is captured as the failure reason.

set -eu

job_id="$1"
checkpoint_dir="$2"

state_file="$checkpoint_dir/checkpoint.state"
if [ ! -f "$state_file" ]; then
	echo "resume-example: no checkpoint state found at $state_file" >&2
	exit 1
fi

# --- Replace everything below with your application's real restore logic. ---
#
# A real hook would read whatever checkpoint-example.sh wrote and use it to
# bring your application back up in whatever state it left off in. This
# toy example doesn't have a real application to restore, so it just starts
# a harmless placeholder process to stand in for "the resumed job" and
# reports its PID -- replace this with actually launching your app.
sleep 3600 &
resumed_pid=$!

# Nothing but the bare PID goes to stdout -- put any diagnostics on stderr
# instead, or they'll break surviva's parsing of this line.
echo "resume-example: resumed job $job_id from $state_file" >&2
echo "$resumed_pid"
