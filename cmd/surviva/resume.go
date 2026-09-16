package main

import (
	"flag"
	"fmt"
	"os"

	"surviva/internal/ipc"
)

// resumeCmd implements `surviva resume [flags] <job-id>`: brings a
// checkpointed job back to life on THIS instance, straight from the local
// checkpoint directory the daemon dumped it into -- no AWS calls, no
// DynamoDB record required. This is the manual counterpart to `surviva
// pause`, for a job someone paused (or that got checkpointed) on this same
// machine. It is deliberately not `surviva restore`: restore is for
// recovering a job onto a *different*, replacement instance via its S3/EBS
// + DynamoDB record, which a `surviva pause -local` checkpoint never has.
func resumeCmd(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva resume [flags] <job-id>")
		return 2
	}
	jobID := rest[0]

	client := ipc.NewClient(*socketPath)
	msg, err := client.Resume(jobID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva resume: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "surviva resume: job %s %s\n", jobID, msg)
	return 0
}
