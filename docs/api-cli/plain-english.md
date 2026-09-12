# The `surviva` command-line tool — plain-English guide

`surviva` is a single program with four modes ("subcommands"). You always start
with `surviva`, then tell it which of the four things you want it to do.

| Command | What it's for |
|---|---|
| `surviva run` | Wrap the command you actually want to run (e.g. a long bioinformatics job), so it's protected. |
| `surviva daemon` | The background service that watches for interruption warnings and does the actual saving. |
| `surviva list` | Show what jobs are currently being watched on this machine. |
| `surviva restore` | Bring a saved job back to life on a new machine. |

A quick refresher on the vocabulary: a **checkpoint** is a snapshot of a running
program — its memory, open files, everything it needs to pick up exactly where
it left off. The **daemon** is a small background process that's always
running, watching for trouble. **Restoring** means taking a checkpoint and
turning it back into a live, running program again, usually on a different
computer.

## `surviva run` — protect a command

This is the one most people will actually type. Instead of running your
command directly, you put `surviva run --` in front of it:

```
surviva run -- my-analysis-pipeline --input data.bam
```

`surviva` starts `my-analysis-pipeline` for you, tells the background daemon
"please watch this one," and then just waits for it to finish — exactly like
running it normally. If your command finishes on its own, `surviva run` quietly
steps out of the way. If the machine is about to be taken away (a Spot
interruption), the daemon takes it from there.

Two optional extras:
- **Priority**: if several jobs are running and time is short, you can mark
  some as more important so they get saved first.
- **Custom save/resume scripts**: for jobs that can't be saved with the
  default method, you can hand `surviva` your own "how to save this" and
  "how to bring it back" scripts instead.

If the background daemon isn't running for some reason, `surviva run` doesn't
block your command — it just runs it unprotected and warns you.

## `surviva daemon` — the background watcher

This runs once, continuously, usually started automatically when the machine
boots (see the deployment guide for how that's normally wired up — you
shouldn't need to start this by hand in a real deployment). It's the piece
that actually:
- watches for interruption warnings,
- takes the checkpoints when one arrives,
- and (if configured) saves those checkpoints somewhere durable — either to
  cloud storage (S3) or to a special disk (EBS) that survives even if the
  machine itself is destroyed.

Most people won't need to touch its settings directly — those are usually
baked into the machine's startup configuration by whoever deployed the system
(see `../deployment/plain-english.md`).

## `surviva list` — what's being watched right now

Run this on a machine to see every job currently tracked, its status (still
running, being saved, saved successfully, failed, etc.), and what command it
is. Useful for checking "is my job actually protected?"

## `surviva restore` — bring a job back

This one isn't normally something a person types by hand — it's what the
automatic recovery system runs on the replacement machine once one is created
(see `../architecture/plain-english.md` for the full recovery story). Given
the ID of a saved job, it fetches the saved data, brings the process back to
life, and starts watching it again — so if that new machine also gets
interrupted later, the same protection kicks in again.

It refuses to restore anything that wasn't saved successfully and completely
— if a save was interrupted partway through, `surviva restore` won't try to
use it, since a partial save could produce a broken, silently-wrong result.

## More detail

For exact flags, defaults, and error messages, see `technical.md` in this
same folder. For how these commands fit into the bigger recovery system, see
`../architecture/plain-english.md`. For how a real deployment is set up, see
`../deployment/plain-english.md`.
