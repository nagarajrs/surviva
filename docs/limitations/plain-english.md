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

---

For the deeper technical reasoning behind some of these tradeoffs, see the project's design-record document. For how the pieces fit together, see the architecture document.
