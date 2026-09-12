# How Surviva Works (Plain English)

## The problem

Cloud providers like AWS sell "spare" computing capacity at a steep discount — often 70-90% cheaper than normal. This is called a **Spot instance**. The catch: AWS can take that computer back whenever it needs the capacity for someone paying full price. You get about **two minutes of warning** before it's pulled out from under you.

For short tasks, that's a minor annoyance — just restart the job. But for long-running work — a multi-hour data pipeline, a large computation, a bioinformatics analysis — losing hours of progress because a cheap machine got reclaimed is a real problem. Either you pay full price to avoid the risk, or you accept the risk of losing work.

Surviva is built to remove that trade-off: run on the cheap Spot machines, but automatically save your work the moment a reclaim notice arrives, and automatically pick it back up on a new machine, with no one watching a dashboard at 3am.

Think of it like a video game's auto-save feature — except this one also finds you a new console when yours gets unplugged, copies your save file over, and resumes your game exactly where you left off, controller still in hand.

## The four things happening in the background

**1. Something is watching the clock.**
A small background program (the "daemon") runs quietly on every machine, constantly checking in with AWS: "are you about to take this machine back?" AWS actually gives two signals — a soft, earlier heads-up ("you might want to move soon") and a hard, ~2-minute final warning. Surviva listens for both, so it can start saving work as early as possible rather than waiting until the last moment.

**2. The moment a warning arrives, work gets saved.**
Whatever long-running command the user is protecting gets frozen in place — its entire memory and state captured, like a snapshot — so it can be resumed later exactly where it stopped, not restarted from scratch.

**3. That snapshot gets copied somewhere that survives the machine dying.**
The snapshot is written either to cloud storage (S3) or to a separate disk (an EBS volume) that isn't destroyed when the machine is. Either way, a record is kept in a small database (DynamoDB) that says "this job's snapshot is safe and ready to be resumed" — or, if something went wrong partway through, that it's *not* safe, so nothing tries to resume from a half-saved snapshot.

**4. A replacement machine appears automatically, and the work resumes.**
The instant AWS's official "your machine is being reclaimed" notice goes out, a fully automated process (no human involved) requests a brand-new machine, waits for it to be ready, hands it the saved snapshot, and tells it to pick up right where the old machine left off. From the outside, it looks like the job just kept running — it briefly paused, and then continued, possibly on entirely different hardware.

And because that new machine is *also* running the same background watcher, it's protected against the same thing happening again. The safety net re-arms itself automatically.

## Does this actually work?

Yes — this was tested against a **real** simulated AWS Spot reclaim (using a purpose-built AWS testing tool that genuinely interrupts a Spot machine, not a fake or simulated signal), and the whole chain ran automatically end to end: warning received, work saved, replacement machine requested, work resumed, all without anyone touching a keyboard mid-flight. A couple of small gaps were found and fixed along the way (see the project's other documents for details) — that's exactly what this kind of real-world test is for.

## What this doesn't try to solve

Not every running program can be perfectly frozen and resumed — programs using specialized hardware (like GPUs) or holding open network connections in certain ways have real technical limits here. See the project's "limitations" document for the honest list.
