# What Surviva Can't Do (Plain English)

Surviva is built to save your work automatically when a cheap "Spot" cloud machine gets reclaimed, and pick it back up on a new machine. It works — this was proven with a real simulated reclaim, not just a demo. But it's honest to know where the edges are before you rely on it for something important. This is that list, in plain terms.

## Some programs can't be perfectly frozen and resumed

The underlying "freeze the program in place" technology has real limits. Programs that talk directly to specialized hardware (like a graphics card), or that hold certain kinds of network connections open, or use a few unusual filesystem setups, may not freeze cleanly.

Surviva has a fallback for this: instead of the automatic freeze, a program can supply its own "save yourself" and "wake yourself back up" scripts, tailored to how it actually saves progress. But if a program can't be frozen automatically *and* doesn't have one of these custom scripts, surviva will fail loudly and mark the job as failed — it never pretends to have saved something it didn't.

## The program being saved needs to be set up a little carefully

If the program you're protecting writes its output to a specific file on the machine's local disk (instead of, say, discarding it or printing to the screen), that exact file needs to already exist — at the same location, the same size — on the replacement machine before resuming, or the resume will refuse to happen. This was actually discovered during real testing: a test program's log file wasn't present on the new machine, and the freeze-and-resume tool correctly refused to guess. The simple fix is to have protected programs send their output somewhere disposable (like a "throw it away" destination) or to a location that travels along with the saved snapshot, rather than an arbitrary spot on the local disk.

## An early tool version had a real bug

While testing on one common cloud operating system, the version of the freezing tool that came pre-installed through the normal software installer had a genuine bug — it would appear to work, pass its own self-check, but then fail when actually asked to resume a frozen program. Building a newer version from the original source fixed it completely. The lesson: don't just trust a tool's own "everything looks fine" self-report — actually test a full save-and-resume cycle on the exact machine image you plan to use, before depending on it.

## A few timing assumptions worth knowing

- If the saved snapshot lives on a separate disk (rather than cloud storage), that disk can only be attached to a replacement machine in the *same* AWS "zone" (a specific data center location) it was already in. This narrows which replacement machines are available compared to using cloud storage, which has no such restriction.
- A brand-new replacement machine reports itself as "ready" a little before it's actually reachable by AWS's remote-management tooling. Surviva accounts for this by waiting for both signals, but it's a real gap worth knowing if you're building anything similar.
- The replacement machine needs to know which cloud storage location and tracking database to use — that information has to be baked into the machine's standard startup configuration ahead of time, not invented on the fly when a replacement is created.

## Saving many things at once has sensible, deliberate limits

If a machine is running several protected jobs at once and gets reclaimed, surviva saves the most important ones first (you can set priority), and caps how many it tries to save at the same time — trying to save everything simultaneously on limited hardware can mean nothing finishes in time. If the reclaim countdown runs out, lower-priority jobs may simply not get saved.

## This is built for Linux cloud servers, not for general desktop use

Surviva's core saving-and-resuming machinery only works on Linux, and specifically expects to be running on a real cloud server (not, say, a personal laptop). Development and testing were partly done on Windows using a Linux compatibility layer, but that was purely for convenience while building it — real usage is Linux-only.

## Resuming a job doesn't retry itself automatically

If a resume attempt fails (for whatever reason), surviva won't automatically try again on its own — a person needs to look into what went wrong and decide what to do next, rather than the system silently retrying and potentially making things worse.

## One setup per "fleet" of machines

Each deployment of surviva's automatic-replacement system is tied to one specific machine "recipe" (what AWS calls a launch template) — matching one group of machines that are all supposed to be interchangeable. If you run several different kinds of machine fleets, each needs its own separate surviva setup.

## Resuming at the exact same process ID can occasionally lose a race

The freeze-and-resume technology brings a program back using its *exact original* process identifier (a number the operating system hands out) — and that number has to be completely free on the replacement machine, or the resume fails outright. This was discovered for real using the project's own hands-on sandbox (a set of setup/teardown scripts anyone can run to try the whole thing themselves — see the `ansible/` folder): the replacement machine boots up in a similar way to the original one, so it's occasionally already using that exact same number for something else by the time surviva tries to resume the job. Testing showed this doesn't happen most of the time, but when it does, there's currently no automatic workaround — it's treated like any other resume failure (see above: it won't retry itself).

## Attaching a saved disk to a replacement machine can also occasionally lose a race

Also found using the hands-on sandbox: when a job's saved snapshot lives on a separate disk, the replacement machine sometimes tries to attach that disk a little too soon — before AWS has finished detaching it from the machine that just got reclaimed. This is now handled by simply trying again a few times over roughly a minute or two, which comfortably covers the normal delay.

## A program you're actively typing into can't be saved at all

If you start a protected program directly in your own terminal session (rather than setting it running in the background) and it's still connected to that live terminal when an interruption happens, the freeze-and-save step fails outright — the underlying technology can't capture a program that's still hooked up to an interactive terminal window. Worse, this can fail completely silently: when checkpoints are being saved to cloud storage (rather than a separate disk), nothing gets recorded anywhere until the save actually succeeds, so a program that fails to save this way leaves no trace at all — it looks exactly like a program that was never being protected in the first place. The fix is simple: always start a protected program in the background, detached from your terminal (there's a standard way to do this — see the deployment guide), rather than running it directly in front of you.

## Pressing Ctrl+C on a protected program might not actually stop it

Two things compound here, both found through real testing. First, because a protected program is deliberately isolated from the terminal it started in (so it can be safely frozen and resumed on different hardware later), your terminal's Ctrl+C doesn't automatically reach it — it was only reaching surviva's own tracking wrapper, which does nothing to the actual program. Second, and unrelated to surviva: many programs (like shell scripts), when running in the background, are specifically designed by the underlying operating system to ignore Ctrl+C-style interruptions — this is standard, deliberate Unix behavior, not a bug. Together, this meant pressing Ctrl+C could silently do nothing, while surviva's own records kept insisting the program was still running long after you'd tried to stop it. This has been fixed — surviva now properly passes along a stronger "please stop" signal that isn't subject to that background-ignoring behavior, so Ctrl+C reliably stops the program again.

## A protected script itself, not just its output, needs to travel with it

Similar to the "output needs somewhere safe to go" issue above, but for the program's own source file: if what you're protecting is a script (rather than a single ready-to-go program), the freeze-and-resume technology needs that exact script file to still be sitting at the exact same location on the replacement machine — the same way it needs the output destination to already exist there. If it was only ever placed on the original machine by hand, resuming will fail once it's moved to a new machine. Saving still works fine (confirmed for real); it's specifically the resume step that needs the script's home to already be prepared on any machine it might land on, which usually means baking it into the starting "image" ahead of time rather than adding it by hand after the fact.

---

For the deeper technical reasoning behind some of these tradeoffs, see the project's design-record document. For how the pieces fit together, see the architecture document.
