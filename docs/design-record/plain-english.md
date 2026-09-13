# Why Surviva Works the Way It Does

This document explains the reasoning behind the major decisions made while building Surviva — not just *what* the system does (see [Architecture](../architecture/plain-english.md)) but *why* it was built this way. A recurring theme: many of these decisions weren't guessed in advance. They came from actually testing things for real — running real programs on real cloud machines, watching them fail in specific ways, and fixing the actual problem. That's a good sign, not a bad one: it means the design was shaped by evidence rather than assumption.

## 1. Give each tracked program a clean, independent life

When Surviva takes over a program to protect it, it deliberately gives that program its own fresh "session" — completely detached from whatever terminal or login window started it. Early on, testing revealed that a leftover, invisible connection back to the original terminal could silently break the freeze-and-save process. The fix was to also double-check for and close any such leftover connections before the program even starts. Since a saved program might get revived on a completely different computer that has no idea about the original terminal, it made sense to cut that cord from the very beginning rather than hope it wouldn't matter.

## 2. Don't let two parts of the system trip over each other

Two different parts of Surviva can, at almost the same moment, notice that a protected program has stopped running: the part that's actively freezing it for safekeeping, and the part that's just watching for the program to finish normally. Testing uncovered a real case where these two would race each other and the "watching" part could accidentally erase the tracking record that the "freezing" part still needed. The fix was to give the freezing process clear ownership: once a program has started being saved, only the saving process is allowed to update or remove its record.

## 3. Offer an escape hatch for programs that can't be frozen the normal way

The primary freezing technology (CRIU, described more in [Limitations](../limitations/plain-english.md)) can't handle everything — certain graphics-card work or specialized network connections, for example. Rather than declaring those programs unsupported, Surviva lets a user supply their own custom "save" and "resume" scripts for a specific program. This was a deliberate choice made early on: better to give people an option than to have the tool simply refuse to help.

## 4. Don't try to save everything at once

If many programs need saving at the same moment, saving all of them at full speed simultaneously can actually overwhelm the machine's disk and processing power so badly that nothing finishes in time. Instead, Surviva saves a limited number of programs at once (matched to how many processing cores the machine has) and works through the rest in priority order. This was tested for real: with the limit set to "only one at a time," the higher-priority programs reliably got handled first, exactly as intended.

## 5. Write down "I'm about to try something risky" *before* trying it

For both ways Surviva can store a saved program — sending it to cloud storage, or writing it directly to an attached storage volume — the system records "this is in progress" in a shared tracking table *before* the risky step (the upload, or the save-to-disk itself) begins. If the machine is unexpectedly shut down midway through, the tracking table is left showing "incomplete," never falsely showing "complete." This matters enormously: a system that tries to resume a program from data that only *looks* complete could produce silent, wrong results, which is far worse than clearly failing.

## 6. Refuse to use storage that could vanish

When storage volumes are used to hold saved programs, Surviva checks — before it even starts running — that the storage volume is explicitly protected from being deleted along with the machine, and it refuses to run at all if that protection isn't in place. This was tested directly: attempting to run with an unprotected volume was correctly refused every time, and — reassuringly — actually shutting down the original machine afterward left the volume intact rather than deleting it, confirming the protection genuinely worked.

## 7. Prefer AWS's own documented conventions over clever workarounds

To find a storage volume again after it's moved to a replacement machine, Surviva relies on a naming convention that the cloud provider (AWS) itself documents and guarantees, rather than inventing a more complicated detection method. Simpler, and it was confirmed to work correctly against real hardware.

## 8. Keep orchestration logic simple by using the cloud's built-in step-by-step workflow tool, without extra custom code

The "find a replacement machine and resume the work" logic runs entirely inside AWS's own workflow service, using built-in cloud actions — no extra custom program had to be written and maintained for this part. This was a deliberate choice to keep the moving parts to a minimum. The tradeoff: the workflow tool's own scripting language has some unusual quirks (for example, it sometimes treats "no match found" as an error rather than simply "nothing," and it's easy to accidentally lose track of information as the workflow moves from one step to the next). These quirks were found and fixed by testing the actual workflow against the real cloud service repeatedly, rather than guessing.

## 9. Make the resuming step the one true judge of "did it actually work"

An earlier version of the system marked a job as "successfully resumed" as soon as it *sent the instruction* to resume it — before actually confirming the instruction succeeded. That's an overly optimistic assumption: the instruction could still fail once it arrived. Once the actual "resume" tool was built, that premature "success" marking was removed. Now, a job is only marked successfully resumed once the resuming step itself confirms the program is truly back up and running.

## 10. Test the real disaster scenario, not just the individual pieces

Every piece of this system was, wherever realistically possible, tested against the real thing rather than a stand-in: a real freeze-and-resume tool running on a real virtual Linux machine (since the development computer was Windows and this technology needs Linux), real temporary cloud resources spun up and torn down for each stage of testing, and — as the final, capstone test — a genuinely triggered simulated cloud outage against a real disposable cloud machine. That last test caught two real mistakes (a missing permission, and a command missing required information) and two real setup gotchas that no amount of testing the pieces individually had revealed. See [Testing Strategy](../testing-strategy/plain-english.md) for the full story of that test.

## 11. Even "just a version bump" gets tested for real, not assumed safe

The specific version of the freezing tool (CRIU) the project had settled on wasn't chosen for any deep reason — it was just a recent, real release picked at the time a build-from-source was first needed, and it happened to work once tried. When the question came up later of moving to a newer version, the answer wasn't "sure, newer is probably fine" — it was to rebuild and re-test on the exact same real machine type where a *different* version had once failed in a nasty, silent way (it passed its own health check but crashed when actually used). The newer version passed cleanly, needed one small addition to its build recipe, and no longer needed an earlier workaround at all — but the point is that this was confirmed, not assumed, using the same standard as every other real test in this project.

---

These eleven decisions aren't the complete list of everything considered while building Surviva, but they're the clearest illustrations of a consistent pattern: prefer the documented, simple, well-tested path; verify assumptions against reality whenever possible; and let genuine failures — not guesses — drive what gets fixed and how.
