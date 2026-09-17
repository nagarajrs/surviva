package main

import (
	"flag"
	"fmt"
	"os"

	"surviva/internal/config"
	"surviva/internal/ipc"
)

// adoptCmd implements `surviva adopt [flags] -checkpoint-dir <path>`:
// register a brand-new job against a checkpoint some other surviva-daemon
// instance already produced (e.g. on a since-terminated machine, with the
// checkpoint dir on shared/network storage this daemon can also reach), so
// this daemon can `surviva resume` it.
func adoptCmd(args []string) int {
	fs := flag.NewFlagSet("adopt", flag.ExitOnError)
	checkpointDir := fs.String("checkpoint-dir", "", "path to a pre-existing checkpoint (required)")
	hookResume := fs.String("hook-resume", "", "path to a custom resume script (required if the checkpoint was produced by a checkpoint hook, not criu)")
	hookCheckpoint := fs.String("hook-checkpoint", "", "path to a custom checkpoint script, for if this job is checkpointed again after resuming")
	tag := fs.String("tag", "", "external identifier to correlate this job with (e.g. $SLURM_JOB_ID)")
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf (for audit logging)")
	_ = fs.Parse(args)

	if *checkpointDir == "" {
		fmt.Fprintln(os.Stderr, "usage: surviva adopt -checkpoint-dir <path> [-hook-resume <path>] [-hook-checkpoint <path>] [-tag <value>]")
		return 2
	}

	al, err := openAuditLogger(*confPath)
	if err != nil {
		warnAuditUnavailable(err)
	} else {
		defer al.Close()
	}

	client := ipc.NewClient(*socketPath)
	jobID, err := client.Adopt(ipc.AdoptCheckpoint{
		CheckpointDir:  *checkpointDir,
		HookResume:     *hookResume,
		HookCheckpoint: *hookCheckpoint,
		Tag:            *tag,
	}, currentOSUser())
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva adopt: %v\n", err)
		logCLI(al, "adopt", "", "error", err.Error())
		return 1
	}

	fmt.Fprintf(os.Stderr, "surviva adopt: tracking job %s against %s (status CHECKPOINT_CREATED; run `surviva resume %s` to restore it)\n", jobID, *checkpointDir, jobID)
	logCLI(al, "adopt", jobID, "ok", "")
	return 0
}
