# Writing a custom checkpoint/resume hook

See `../SPEC-hooks.md` for the full spec. Short version: a hook is any two
executable scripts (or one script handling both, called with different
purposes) that fully replace CRIU for one job — for anything CRIU can't
checkpoint (GPU state, certain sockets — see `../LIMITATIONS.md`), or when
you'd rather use your own application's native save/restore mechanism.

## The contract

- **Checkpoint hook**: `your-script <job-id> <checkpoint-dir>`, must exit
  `0` on success.
- **Resume hook**: `your-script <job-id> <checkpoint-dir>`, must print the
  resumed process's PID as a single integer line on stdout — nothing else
  on stdout, or surviva can't parse it. Put any diagnostics on stderr
  instead.
- `<checkpoint-dir>` is the same directory across every checkpoint/resume
  call for that job — read/write whatever files you need there.
- You are not given the tracked process's PID. Your application is assumed
  to already have its own way to find/control itself (a PID file it
  maintains, a control socket, etc.) — see `SPEC-hooks.md`'s "responsibility
  split" section for why.
- If you want the same "paused until resumed" semantics a real CRIU dump
  gives you for free, **your checkpoint hook has to stop your process
  itself** — nothing else will do it for you.
- If your resume hook backgrounds a long-running process (the normal
  thing to do), **redirect that process's own stdin/stdout/stderr away**
  (`your-app < /dev/null > app.log 2>&1 &`) rather than a bare `&`. Skip
  this and the resume call silently hangs until that process eventually
  exits — see `SPEC-hooks.md` for exactly why (a real bug found writing
  `examples/resume-example.sh`, not a hypothetical).

## Test your hook standalone, no daemon required

Since the contract is just "run with two arguments, check the exit code /
stdout," you can fully exercise a hook by hand:

```bash
# Checkpoint hook: should exit 0
./my-checkpoint-hook.sh test-job-1 /tmp/test-checkpoint && echo "checkpoint OK"

# Resume hook: should print exactly one integer line
pid=$(./my-resume-hook.sh test-job-1 /tmp/test-checkpoint)
echo "resumed pid: $pid"
kill -0 "$pid" && echo "looks like a real process"
```

If either script doesn't behave exactly like that when run by hand, it
won't behave any differently when the daemon runs it — there's no daemon
magic involved.

## Example

`examples/checkpoint-example.sh` and `examples/resume-example.sh` are a
minimal, runnable, heavily-commented pair demonstrating the contract with
toy state (they don't manage a real application — see the comments in each
for exactly what to replace). Try the whole loop for real:

```bash
surviva run -hook-checkpoint hooks/examples/checkpoint-example.sh \
             -hook-resume hooks/examples/resume-example.sh \
             -- sleep 300

surviva pause <job-id>    # runs checkpoint-example.sh, no CRIU involved
surviva resume <job-id>   # runs resume-example.sh
surviva show <job-id>     # confirm it's RUNNING again with a new pid
```

A registration with a hook path that doesn't exist or isn't executable is
rejected immediately (`surviva run -hook-checkpoint /does/not/exist -- ...`
fails right away, not the next time something tries to checkpoint it).
