package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"surviva/internal/fdguard"
	"surviva/internal/ipc"
	"surviva/internal/procattr"
	"surviva/internal/procsignal"
)

// runCmd implements `surviva run [flags] -- <command> [args...]`: start the
// command, register its PID with the daemon so it's checkpointed on a Spot
// interruption, and deregister it on normal exit.
func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	priority := fs.Int("priority", 0, "checkpoint priority: higher runs first when time is short")
	hookCheckpoint := fs.String("hook-checkpoint", "", "path to a custom checkpoint script (used instead of criu)")
	hookResume := fs.String("hook-resume", "", "path to a custom resume script (used instead of criu restore)")
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	_ = fs.Parse(args)

	cmdArgs := fs.Args()
	if len(cmdArgs) > 0 && cmdArgs[0] == "--" {
		cmdArgs = cmdArgs[1:]
	}
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "surviva run: no command given\nusage: surviva run [flags] -- <command> [args...]")
		return 2
	}

	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva run: %v\n", err)
		return 1
	}

	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = procattr.New()

	// Any fd this process happens to have inherited (e.g. a stray pty from
	// an enclosing shell) must not leak into the tracked child: it can
	// silently break CRIU dump/restore later.
	fdguard.CloseInherited()

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "surviva run: failed to start %q: %v\n", cmdArgs[0], err)
		return 1
	}

	// Setpgid makes the child its own process group leader, so its PGID
	// equals its PID; CRIU dumps the whole tree rooted there in a later phase.
	pid := cmd.Process.Pid

	// The child is in its own session (see internal/procattr) so that CRIU
	// can dump/restore it without a controlling terminal -- but that also
	// means the terminal's own Ctrl+C never reaches it: SIGINT only goes to
	// the terminal's foreground process group, which is this wrapper
	// process, not the child's separate one. Without forwarding something
	// ourselves, Go's default handling kills this process outright on
	// SIGINT, before cmd.Wait() ever returns, so the child runs on
	// unattended and this job's daemon record is never deregistered.
	//
	// What gets forwarded is deliberately always SIGTERM, never the literal
	// signal received: confirmed for real that a bash script backgrounded
	// with `&` (exactly how a tracked child ends up running, session-wise)
	// sets its own SIGINT/SIGQUIT disposition to ignored -- a POSIX rule
	// for asynchronous commands, unrelated to surviva -- so forwarding
	// SIGINT verbatim silently does nothing for a shell-script job. SIGTERM
	// carries no such special-cased ignore and reliably terminates it.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for range sigCh {
			_ = procsignal.KillGroup(pid, syscall.SIGTERM)
		}
	}()

	client := ipc.NewClient(*socketPath)
	jobID, regErr := client.Register(ipc.RegisterJob{
		PID:            pid,
		PGID:           pid,
		Command:        cmdArgs,
		WorkDir:        workDir,
		HookCheckpoint: *hookCheckpoint,
		HookResume:     *hookResume,
		Priority:       *priority,
	})
	if regErr != nil {
		fmt.Fprintf(os.Stderr, "surviva run: warning: could not register with daemon: %v\n", regErr)
		fmt.Fprintln(os.Stderr, "surviva run: continuing WITHOUT interruption protection")
	} else {
		fmt.Fprintf(os.Stderr, "surviva run: tracking job %s (pid %d)\n", jobID, pid)
	}

	waitErr := cmd.Wait()

	if regErr == nil {
		if err := client.Deregister(jobID); err != nil {
			fmt.Fprintf(os.Stderr, "surviva run: warning: failed to deregister job %s: %v\n", jobID, err)
		}
	}

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "surviva run: %v\n", waitErr)
		return 1
	}
	return 0
}
