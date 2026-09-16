package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"surviva/internal/config"
	"surviva/internal/ipc"
	"surviva/internal/procgroup"
	"surviva/internal/procinfo"
)

// joinCmd implements `surviva join [flags] <pid>`: adopt an already-running
// external process into tracking. Refuses unless pid is already its own
// process-group leader -- see SPEC-surviva-cli.md for why (cancel signals
// the whole group, which is only safe for a group isolating just this job).
func joinCmd(args []string) int {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	hookCheckpoint := fs.String("hook-checkpoint", "", "path to a custom checkpoint script (used instead of criu)")
	hookResume := fs.String("hook-resume", "", "path to a custom resume script (used instead of criu restore)")
	checkpointDir := fs.String("checkpoint-dir", "", "override where this job's checkpoint is written (default: daemon-computed)")
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf (for audit logging)")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva join [flags] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(rest[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva join: invalid pid %q\n", rest[0])
		return 2
	}

	al, err := openAuditLogger(*confPath)
	if err != nil {
		warnAuditUnavailable(err)
	} else {
		defer al.Close()
	}

	req, err := buildJoinRequest(pid, *checkpointDir, *hookCheckpoint, *hookResume)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva join: %v\n", err)
		logCLI(al, "join", "", "error", err.Error())
		return 1
	}

	client := ipc.NewClient(*socketPath)
	jobID, err := client.Register(req, currentOSUser())
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva join: %v\n", err)
		logCLI(al, "join", "", "error", err.Error())
		return 1
	}

	fmt.Fprintf(os.Stderr, "surviva join: tracking job %s (pid %d)\n", jobID, pid)
	logCLI(al, "join", jobID, "ok", "")
	return 0
}

// buildJoinRequest is the testable core of joinCmd: the process-group
// safety check plus best-effort Command/WorkDir enrichment.
func buildJoinRequest(pid int, checkpointDir, hookCheckpoint, hookResume string) (ipc.RegisterJob, error) {
	isLeader, err := procgroup.IsGroupLeader(pid)
	if err != nil {
		return ipc.RegisterJob{}, fmt.Errorf("check process group for pid %d: %w", pid, err)
	}
	if !isLeader {
		return ipc.RegisterJob{}, fmt.Errorf(
			"pid %d is not its own process-group leader; surviva can only join a process already isolated into its own group (e.g. started with setsid) -- cancel signals the whole group, and joining one that shares a group with other processes risks killing them too",
			pid,
		)
	}
	return ipc.RegisterJob{
		PID:            pid,
		PGID:           pid,
		Command:        procinfo.CommandLine(pid),
		WorkDir:        procinfo.WorkDir(pid),
		CheckpointDir:  checkpointDir,
		HookCheckpoint: hookCheckpoint,
		HookResume:     hookResume,
	}, nil
}
