package main

import (
	"flag"
	"fmt"
	"os"

	"surviva/internal/ipc"
)

// pauseCmd implements `surviva pause [flags] <job-id>`: checkpoints a
// RUNNING job on demand, exactly like an interruption notice would, but
// triggered by a person rather than an AWS Spot signal. By default the
// checkpoint is stored wherever the daemon is already configured for
// durable storage (S3 or EBS); -local forces it onto local disk only for
// this one job.
func pauseCmd(args []string) int {
	fs := flag.NewFlagSet("pause", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	local := fs.Bool("local", false, "store the checkpoint on local disk only, skipping S3/EBS for this job")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva pause [flags] <job-id>")
		return 2
	}
	jobID := rest[0]

	client := ipc.NewClient(*socketPath)
	msg, err := client.Pause(jobID, *local)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva pause: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "surviva pause: paused job %s: %s\n", jobID, msg)
	return 0
}
