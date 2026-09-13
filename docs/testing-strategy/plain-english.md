# How We Know Surviva Actually Works (Plain English)

This document explains how surviva was tested while it was being built, and how confident
you should be in it. For what the system does, see [`../architecture/plain-english.md`](../architecture/plain-english.md).
For how to deploy it, see [`../deployment/plain-english.md`](../deployment/plain-english.md).

## The guiding idea: test it for real, not just "in theory"

It would have been much faster to write the code, run it against fake/simulated versions
of Amazon Web Services, and call it done. Instead, at every stage, the building team asked
"does this actually work against the real thing?" — and kept escalating how real the test
was as the stakes got higher. The process looked roughly like this:

1. **Start small and cheap.** Early pieces (the command-line tool, the background helper
   process, the "save the program's state" mechanism) were tested using a real Linux
   system running locally, not a fake one, even though it meant extra setup work.
2. **Move to real cloud services, but temporary ones.** Once the system needed to talk to
   actual Amazon cloud storage and databases, real (but short-lived) versions of those
   services were created, used for the test, and deleted immediately afterward — so every
   test proved the real thing worked, not just a stand-in for it.
3. **Test with real computers, not simulations of computers.** When the system needed to
   move a saved program from one computer to another, real (temporary) cloud computers
   were used for that test, because some problems genuinely only show up when you're
   dealing with two separate machines instead of one.
4. **Finish with the real disaster scenario.** The last and biggest test simulated the
   actual event this whole system exists to survive: Amazon reclaiming a cheap "Spot"
   computer with only a couple of minutes' notice. Rather than fake that notice, the team
   used Amazon's own official tool for deliberately triggering that exact event (called
   "Fault Injection Simulator," or FIS) against a real computer that was really running a
   real tracked job.

## Why the final real-disaster test mattered so much

Every earlier test was useful, but each one only checked one piece of the puzzle at a
time. The final test was different: it let the *entire* chain of events happen exactly as
it would in real life, with no test-only shortcuts.

That test caught two genuine mistakes that every earlier, more careful, piece-by-piece
test had missed:

- The "helper" computer that gets automatically created to take over a job wasn't allowed
  to download the saved program data — a permissions setting had been forgotten.
- The automatic instruction sent to that helper computer, telling it which job to resume,
  was missing two pieces of information it actually needed to find that job's saved data.

Neither of these showed up in any smaller test, because those smaller tests each
manually supplied the pieces that were, in reality, missing. Only by letting the *whole*
process run untouched, start to finish, did the gaps become visible. This is the main
argument for this kind of testing: some mistakes are only visible when you stop helping
the system along and let it either succeed or fail entirely on its own.

The test also confirmed two things worth knowing about, which aren't mistakes exactly but
important details for anyone using this system in practice:

- The replacement computer needs to be told, in advance, which storage locations to use —
  that information has to be baked into the "template" used to create it, not handed to it
  after the fact.
- If the program being saved was writing its output to a particular file, that same file
  needs to still exist (with the right size) on the replacement computer, or the "resume"
  step will refuse to proceed. This is a real and understandable safety check, not a bug.

Both of these are explained in more detail in [`../limitations/plain-english.md`](../limitations/plain-english.md).

## Checking a newer version of the freezing tool, the same way

Some time after the system was otherwise finished, a newer version of the underlying
freeze/resume tool (CRIU) became available, and the question came up: should the project
switch to it? Rather than just assuming a newer version is automatically fine — especially
given that an *older* packaged version of this same tool had once passed its own built-in
health check while actually being broken — the team rebuilt the newer version and ran it
through the same real freeze-then-resume test, on the very same type of real cloud computer
where that earlier broken version had been found. It passed cleanly, needed one small
addition to how it's built, and no longer needed an earlier workaround at all. The switch
was made only after that real test passed, not before.

## What hasn't been tested yet (being honest about it)

This system has been proven to work, end to end, for real — once. That's meaningfully
different from having an ongoing safety net that automatically re-checks it every time
something changes. Specifically, right now:

- There's no automated test suite that re-runs itself whenever the code changes. Every
  test described above was done by hand, one time, during development.
- It hasn't been tested with a large number of jobs running at once, or with very large
  saved-program files.
- It's only been tried with one kind of computer setup in one region — not across many
  different configurations.
- The custom "checkpoint"/"resume" scripts (an escape hatch for programs the automatic
  save/restore mechanism can't handle on its own) were only tested with very simple
  examples, not a real complex program.
- Nobody has deliberately tested what happens if the cloud database or the automation
  service itself has a hiccup at the worst possible moment.

None of this means the system is unreliable — it means the kind of confidence that exists
today came from thorough one-time testing, not from an ongoing automatic checking process.
Building that ongoing process is the natural next step, and is described in the technical
version of this document for anyone who wants to set it up.
