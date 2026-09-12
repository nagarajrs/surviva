# Deploying surviva — Plain English Guide

This guide explains, in plain terms, what it takes to actually stand up surviva in a real AWS account. It doesn't cover what surviva *is* (see `../architecture/plain-english.md`) or the exact commands and flags (see `../api-cli/plain-english.md`) — just the sequence of steps to get it running for real.

## Why deployment takes some up-front preparation

The whole point of surviva is that when a cheap "Spot" computer is about to be taken away, it saves its work and a *replacement* computer picks up right where it left off — automatically, within a couple of minutes.

For that to work, the replacement computer can't spend its first few minutes installing software. It needs to already have everything it needs the moment it boots up, like a hotel room that's already made up rather than one that needs cleaning before a guest can check in.

That means most of the deployment effort happens **once, ahead of time**: building a ready-to-go "image" (a snapshot of a fully set-up computer) that any new replacement is launched from.

## The stages, in order

1. **Build the starter image.** Start with a plain computer, install the checkpoint/resume software (CRIU) and the surviva program on it, and set it up so surviva starts automatically every time a computer boots from this image. Save that as a reusable image. This is done once per version of the software, not every time a computer is replaced.

2. **Set up the shared infrastructure.** Create the shared filing cabinet (a database that tracks the status of every saved job) and, optionally, shared cold storage (for saving large checkpoint files). Also set up the "automatic responder" — the piece that watches for interruption warnings and reacts by launching a replacement and telling it to resume the saved work.

3. **Create a launch template.** This is the recipe a new computer is built from: which starter image to use, and a small piece of configuration telling it which filing cabinet and storage to use. This same recipe is used both for the very first computer and for every automatic replacement afterward — so it's important the configuration is baked into the recipe itself, not something you have to remember to set by hand each time.

4. **Launch the real computer** from that recipe, as a Spot (discounted, interruptible) instance.

5. **Start your actual work on it**, wrapped with the surviva command so it's protected. From this point on, everything else is automatic — no one has to watch it or intervene.

6. **What happens if it gets interrupted** is covered in the architecture guide — short version: nothing manual is needed.

7. **Test it before you trust it.** Before relying on this for real, valuable work, it's strongly recommended to simulate a real interruption using AWS's own fault-testing tool and confirm the whole cycle — save, replace, resume — actually completes. This was done for this project and it caught two real configuration mistakes that a "looks correct on paper" review would have missed. Testing it for real, not just reading the setup, is what actually builds confidence.

8. **Tearing it down** when you're done is the reverse of setup: remove the shared infrastructure, the recipe, the starter image, and any computers still running.

## The big lesson from testing this for real

When this exact process was tested against AWS using a real simulated interruption, two things went wrong that wouldn't have been obvious without actually running it:

- The replacement computer didn't know which "filing cabinet" (database) to check, because that piece of configuration had only been given to the *first* computer by hand, not baked into the shared recipe every computer uses.
- The replacement computer wasn't allowed to read back the saved checkpoint file it needed, because a permission had only been granted for saving files, not for reading them back.

Both were quick fixes once found — but they're exactly the kind of thing that only shows up when you actually run the drill, not when you just read the plan. That's why step 7 (testing with a real simulated interruption) isn't optional if you want to trust this in production.
