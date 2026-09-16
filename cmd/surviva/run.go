package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"surviva/internal/config"
	"surviva/internal/fdguard"
	"surviva/internal/ipc"
	"surviva/internal/procattr"
	"surviva/internal/procsignal"
)

// runCmd implements `surviva run [flags] -- <command> [args...]`: start the
// command, register it with the daemon so it's checkpointed on demand or on
// a cloud interruption signal, and report its exit status when it ends.
func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	hookCheckpoint := fs.String("hook-checkpoint", "", "path to a custom checkpoint script (used instead of criu)")
	hookResume := fs.String("hook-resume", "", "path to a custom resume script (used instead of criu restore)")
	checkpointDir := fs.String("checkpoint-dir", "", "override where this job's checkpoint is written (default: daemon-computed)")
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf (for audit logging)")
	_ = fs.Parse(args)

	cmdArgs := fs.Args()
	if len(cmdArgs) > 0 && cmdArgs[0] == "--" {
		cmdArgs = cmdArgs[1:]
	}
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "surviva run: no command given\nusage: surviva run [flags] -- <command> [args...]")
		return 2
	}

	al, err := openAuditLogger(*confPath)
	if err != nil {
		warnAuditUnavailable(err)
	} else {
		defer al.Close()
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

	// Any fd this process happens to have inherited must not leak into the
	// tracked child -- it can silently break CRIU dump/restore later.
	fdguard.CloseInherited()

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "surviva run: failed to start %q: %v\n", cmdArgs[0], err)
		logCLI(al, "run", "", "error", err.Error())
		return 1
	}
	pid := cmd.Process.Pid

	// The child is in its own session (internal/procattr) so a terminal's
	// Ctrl-C never reaches it directly; forward SIGINT/SIGTERM ourselves,
	// always as SIGTERM (a backgrounded shell script ignores SIGINT for
	// itself regardless -- a POSIX rule, not a surviva quirk).
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
		CheckpointDir:  *checkpointDir,
		HookCheckpoint: *hookCheckpoint,
		HookResume:     *hookResume,
	})
	if regErr != nil {
		fmt.Fprintf(os.Stderr, "surviva run: warning: could not register with daemon: %v\n", regErr)
		fmt.Fprintln(os.Stderr, "surviva run: continuing WITHOUT interruption protection")
	} else {
		fmt.Fprintf(os.Stderr, "surviva run: tracking job %s (pid %d)\n", jobID, pid)
	}

	waitErr := cmd.Wait()
	exitCode, errMsg := exitCodeAndErr(waitErr)

	if regErr == nil {
		if err := client.Complete(jobID, exitCode, errMsg); err != nil {
			fmt.Fprintf(os.Stderr, "surviva run: warning: failed to report completion for job %s: %v\n", jobID, err)
		}
	}

	outcome := "ok"
	if exitCode != 0 {
		outcome = "error"
	}
	logCLI(al, "run", jobID, outcome, errMsg)

	return exitCode
}

// exitCodeAndErr computes what to report to the daemon (and this process's
// own exit code) from cmd.Wait()'s error: 0/"" on a clean exit, the child's
// own exit code with no message if it just exited nonzero, or 1 plus the
// error text for anything else (e.g. the process couldn't be waited on).
func exitCodeAndErr(waitErr error) (exitCode int, errMsg string) {
	if waitErr == nil {
		return 0, ""
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode(), ""
	}
	return 1, waitErr.Error()
}
