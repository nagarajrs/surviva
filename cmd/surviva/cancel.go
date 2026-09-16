package main

import (
	"flag"
	"fmt"
	"os"

	"surviva/internal/config"
	"surviva/internal/ipc"
)

// cancelCmd implements `surviva cancel <job-id>`: stop it if running, mark
// it CANCELED either way.
func cancelCmd(args []string) int {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf (for audit logging)")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva cancel <job-id>")
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
	if err := client.Cancel(jobID, currentOSUser()); err != nil {
		fmt.Fprintf(os.Stderr, "surviva cancel: %v\n", err)
		logCLI(al, "cancel", jobID, "error", err.Error())
		return 1
	}
	fmt.Fprintf(os.Stderr, "surviva cancel: canceled job %s\n", jobID)
	logCLI(al, "cancel", jobID, "ok", "")
	return 0
}
