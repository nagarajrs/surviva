# Known Limitations

Real gotchas found while building/testing surviva, written down so they
don't get rediscovered from scratch.

## 1. A tracked process attached to an interactive terminal can't be checkpointed at all

**Symptom**, found testing `pause` on WSL2 against both a `surviva run`-started
`sleep 360` and a separately-started process adopted via `join`:

```
(00.426498) Error (criu/tty.c:410): tty: Found slave peer index 3 without correspond master peer
(00.426613) Error (criu/cr-dump.c:2128): Dumping FAILED.
```

**Root cause:** if a process's stdin/stdout/stderr are connected to a pty
(any process still attached to a live terminal session), CRIU can't dump it
without also dumping the pty's *master* side — which lives in the terminal
emulator/shell, entirely outside the process tree surviva ever tracks. This
is not specific to `pause`, `run`, or `join`, and not a WSL quirk — it's
about whether the *target process itself* has an open pty fd at the moment
of dump, regardless of which command registered it:

- `surviva run`'s `Setsid` (`internal/procattr`) detaches the child into its
  own session so a terminal's Ctrl-C doesn't reach it, but it does **not**
  close or redirect stdin/stdout/stderr — `run` wires those straight to its
  own (`os.Stdin`/`Stdout`/`Stderr`), so a `run` launched from an interactive
  shell still leaves the child's stdio pointed at that shell's pty.
- `join` adopts a process exactly as it finds it — if it was started
  attached to a terminal, joining doesn't change that.

This is a deliberate design assumption, not a regression:
`internal/criu/criu.go`'s own comment says a tracked child needs no
shell/tty to reattach to on dump or restore, and `--shell-job` is
deliberately not used to work around it.

**Mitigation — redirect stdio away from any terminal before checkpointing
matters:**

```bash
# run: redirect at launch
surviva run -- sleep 360 < /dev/null > /path/to/sleep.log 2>&1 &

# join: start it detached AND already its own process-group leader in one
# step (setsid satisfies join's other requirement too -- see specs/surviva-cli.md)
setsid sleep 360 < /dev/null > /path/to/sleep.log 2>&1 &
surviva join <pid>
```

There is no way to fix this after the fact for an already-running process
still attached to a terminal — it has to be restarted redirected (or
`setsid`'d, for `join`).

**Deliberately not changed:** `surviva run` still wires the child's stdio
straight to its own by default, rather than defaulting to `/dev/null` to
avoid this footgun automatically. Revisit if this proves too easy to hit
by accident in practice.
