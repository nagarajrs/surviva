package main

import (
	"flag"
	"fmt"
	"os"

	"surviva/internal/config"
	"surviva/internal/ipc"
)

// pruneCmd implements `surviva prune [job-id]`: clear checkpoint files for
// terminal jobs -- one (job-id given) or every terminal job (no args).
// Never touches store rows; a pruned job stays fully visible via `show`.
func pruneCmd(args []string) int {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf (for audit logging)")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) > 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva prune [job-id]")
		return 2
	}
	var jobID string
	if len(rest) == 1 {
		jobID = rest[0]
	}

	al, err := openAuditLogger(*confPath)
	if err != nil {
		warnAuditUnavailable(err)
	} else {
		defer al.Close()
	}

	client := ipc.NewClient(*socketPath)
	msg, failures, err := client.Prune(jobID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva prune: %v\n", err)
		logCLI(al, "prune", jobID, "error", err.Error())
		return 1
	}
	fmt.Fprintf(os.Stderr, "surviva prune: %s\n", msg)
	for _, f := range failures {
		fmt.Fprintf(os.Stderr, "surviva prune: warning: %s\n", f)
	}
	outcome := "ok"
	if len(failures) > 0 {
		outcome = "error"
	}
	logCLI(al, "prune", jobID, outcome, fmt.Sprintf("%d failure(s)", len(failures)))
	return 0
}
