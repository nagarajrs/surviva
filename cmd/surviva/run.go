package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"surviva/internal/ipc"
	"surviva/internal/procattr"
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

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "surviva run: failed to start %q: %v\n", cmdArgs[0], err)
		return 1
	}

	// Setpgid makes the child its own process group leader, so its PGID
	// equals its PID; CRIU dumps the whole tree rooted there in a later phase.
	pid := cmd.Process.Pid
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
