package main

import (
	"flag"
	"fmt"
	"os"

	"surviva/internal/config"
	"surviva/internal/ipc"
)

// resumeCmd implements `surviva resume <job-id>`: bring a checkpointed job
// back to RUNNING.
func resumeCmd(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf (for audit logging)")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva resume <job-id>")
		return 2
	}
	jobID := rest[0]

	al, err := openAuditLogger(*confPath)
	if err != nil {
		warnAuditUnavailable(err)
	} else {
		defer al.Close()
	}

	client := ipc.NewClient(*socketPath)
	msg, err := client.Resume(jobID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva resume: %v\n", err)
		logCLI(al, "resume", jobID, "error", err.Error())
		return 1
	}
	fmt.Fprintf(os.Stderr, "surviva resume: job %s: %s\n", jobID, msg)
	logCLI(al, "resume", jobID, "ok", "")
	return 0
}
