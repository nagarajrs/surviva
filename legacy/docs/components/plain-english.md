# surviva: What Are the Pieces? (Plain English)

surviva is made of a handful of cooperating pieces, a bit like departments in a
company. Each one has a single job. This page describes what each piece does
without any code detail — for the full technical breakdown, see
[`technical.md`](technical.md); for how they all work together as a system,
see [`../architecture/plain-english.md`](../architecture/plain-english.md).

## The four "departments"

| Plain-English name | What it does |
|---|---|
| **The wrapper** | Starts your long-running job and tells the local minder about it. |
| **The minder** | Runs quietly on every machine, watches for interruption warnings, and saves your job's progress the moment one arrives. |
| **The filing cabinet** | A shared, durable record (in the cloud) of every saved job and where its saved data lives — so it survives even if the machine that made it disappears. |
| **The rescue crew** | Notices a machine is about to be taken away, orders a replacement, and tells it to pick the job back up. |

## The wrapper: `surviva run`

You put `surviva run --` in front of the command you'd normally type. It
starts your job exactly as you asked, then quietly tells the minder "please
watch this one." If your job finishes normally, the wrapper tells the minder
to forget about it. If the minder isn't running for some reason, your job
still runs fine — it just won't be protected.

## The minder: `surviva daemon`

This is the one background service that runs on a machine the whole time it's
alive. It does two things on a loop:

1. **Listens** for the wrapper (and a couple of other helpers) to tell it
   about jobs to watch, or to ask what it's currently watching.
2. **Watches** for the two warnings a cloud provider gives before taking a
   machine away — an early, soft heads-up, and a harder "you have about two
   minutes" notice. The moment either one shows up, it saves every job it's
   watching, as many at once as the machine can comfortably handle, saving
   the most important ones first if there isn't time for all of them.

Saving a job means: freeze it exactly as it is (its memory, its open files,
everything), and copy that frozen snapshot somewhere durable — either a
cloud storage bucket, or a disk drive that's built to survive even if the
machine itself is destroyed.

## The filing cabinet: the shared status table

Every time a job is saved, a record goes into a shared table that lives
independently of any one machine — think of it as a filing cabinet in a
different building that can't burn down with the office. That record says:
which job, is the save actually finished (not half-done), and where exactly
the saved copy lives. This is the single source of truth the rescue crew
checks before it ever tries to bring a job back — if the record doesn't say
"fully saved," nothing gets restored from it, full stop.

## The rescue crew: the automatic replacement system

This piece isn't software running on any one machine — it's a small,
automatic pipeline that watches for the cloud provider's own "this machine is
about to be taken away" announcement. The moment that announcement fires, it:

1. Waits a bit for the minder to finish saving everything.
2. Checks the filing cabinet for what was actually saved successfully.
3. Orders a brand-new replacement machine, built from the exact same recipe
   as the one being taken away.
4. Waits for the new machine to be ready and reachable.
5. Tells the new machine, one saved job at a time, "please pick this one back
   up" — attaching the right saved disk first, if that's where the save
   lives.

## The last piece: `surviva restore`

This is what the new machine actually runs when the rescue crew tells it to
pick a job back up. It double-checks the filing cabinet really does say
"fully saved" (refusing anything less), fetches the saved copy, un-freezes
the job so it starts running again exactly where it left off, and tells the
new machine's own minder to start watching it — so if *this* machine also
gets a warning someday, the same whole process happens again automatically.

## How they connect

```
your job
   │
   ▼
the wrapper  ──tells──▶  the minder  ──saves to──▶  filing cabinet + storage
                              ▲                              │
                              │                     (interruption warning)
                              │                              ▼
                     new minder starts        the rescue crew notices, builds
                     watching again  ◀──────── a replacement, and tells it to
                                               run "surviva restore"
```
