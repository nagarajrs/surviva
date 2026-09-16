package main

import (
	"flag"
	"fmt"
	"os"

	"surviva/internal/config"
	"surviva/internal/ipc"
)

// pauseCmd implements `surviva pause <job-id>`: checkpoint a RUNNING job now.
func pauseCmd(args []string) int {
	fs := flag.NewFlagSet("pause", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf (for audit logging)")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva pause <job-id>")
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
	msg, err := client.Pause(jobID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva pause: %v\n", err)
		logCLI(al, "pause", jobID, "error", err.Error())
		return 1
	}
	fmt.Fprintf(os.Stderr, "surviva pause: job %s: %s\n", jobID, msg)
	logCLI(al, "pause", jobID, "ok", "")
	return 0
}
