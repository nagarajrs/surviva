# The `surviva` command-line tool — plain-English guide

`surviva` is a single program with eight modes ("subcommands"). You always
start with `surviva`, then tell it which of the eight things you want it to
do.

| Command | What it's for |
|---|---|
| `surviva run` | Wrap the command you actually want to run (e.g. a long bioinformatics job), so it's protected. |
| `surviva daemon` | The background service that watches for interruption warnings and does the actual saving. |
| `surviva list` | Show what jobs are currently being watched on this machine. |
| `surviva stop` | Stop a job and remove it from tracking. |
| `surviva pause` | Save a job's progress right now, on your own terms, instead of waiting for an interruption warning. |
| `surviva resume` | Bring a saved job back to life on this same machine. |
| `surviva restore` | Bring a saved job back to life on a new machine. |
| `surviva version` | Print which version of surviva you're running. |

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
block your command — it just runs it unprotected and warns you. The same
thing happens if you try to start a new job *after* a Spot warning has
already arrived: it's too late for that job to be saved along with
everything else, so surviva says so plainly and runs it unprotected rather
than pretending it's covered.

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

## `surviva stop` — cancel a job

If you started a job with `surviva run` in your own terminal and want to
cancel it, pressing Ctrl+C works — it now correctly stops the actual job, not
just surviva's own wrapper around it. But sometimes a job ends up "orphaned":
whatever was watching it (your terminal session, `surviva run` itself) is
long gone, yet `surviva list` still insists it's running, because nothing
else in the system ever checks whether that's actually still true.
`surviva stop <job-id>` fixes that directly — it stops the actual program and
removes it from the list in one step, no matter what happened to whatever was
originally supervising it. Use the exact ID shown in the `JOB ID` column of
`surviva list` — not the PID next to it — and if that ID doesn't actually
exist, it tells you so rather than pretending it worked. It only works on a
job that's still genuinely running; it refuses to touch one that's in the
middle of being saved, or already saved successfully, since removing that
record could make a legitimate save impossible to recover later.

## `surviva pause` — save a job on your own terms

Everything described so far happens automatically: the daemon decides when
to save your work, because AWS told it the machine is about to disappear.
`surviva pause <job-id>` gives you that same button yourself, whenever you
want it — no warning required. Maybe you want to shrink a machine down for
the night, hand a long job off to someone else, or just make sure a save
exists before you try something risky. It does exactly what an interruption
would do: your job's progress is captured and the program stops right there,
ready to be picked up again later.

By default, the saved copy goes wherever this machine is already set up to
keep saves durably (cloud storage or a special disk, same as normal). Add
`--local` and it stays only on this machine's own disk instead — faster and
simpler, but only recoverable from this exact machine, and gone for good if
this machine is lost before you resume it. Everything else works the same
way `surviva stop` does: use the job's ID from `surviva list`, and a job
that's already being saved or already finished can't be paused a second
time.

## `surviva resume` — bring a job back, right here

This is the self-service partner to `surviva pause`: it un-pauses a job on
the very same machine that saved it, picking up exactly where it left off.
Unlike `surviva restore` (below), it doesn't talk to AWS at all — it just
needs the save data that's still sitting on this machine's disk, which makes
it the natural way to bring back anything you paused with `--local`, as well
as anything saved the normal way that hasn't been moved anywhere else yet.

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
