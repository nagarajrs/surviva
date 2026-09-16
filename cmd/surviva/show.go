package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"surviva/internal/config"
	"surviva/internal/ipc"
	"surviva/internal/store"
)

// showCmd implements `surviva show [-json] <job-id>`: full detail for one
// job, regardless of status.
func showCmd(args []string) int {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf (for audit logging)")
	asJSON := fs.Bool("json", false, "print raw JSON instead of a key:value dump")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva show [-json] <job-id>")
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
	job, err := client.Show(jobID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva show: %v\n", err)
		logCLI(al, "show", jobID, "error", err.Error())
		return 1
	}
	logCLI(al, "show", jobID, "ok", "")
	return renderJobDetail(os.Stdout, job, *asJSON)
}

func renderJobDetail(w io.Writer, j store.Job, asJSON bool) int {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(j); err != nil {
			fmt.Fprintf(os.Stderr, "surviva show: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(w, "ID:              %s\n", j.ID)
	fmt.Fprintf(w, "Status:          %s\n", j.Status)
	fmt.Fprintf(w, "PID:             %d\n", j.PID)
	fmt.Fprintf(w, "PGID:            %d\n", j.PGID)
	fmt.Fprintf(w, "Command:         %s\n", strings.Join(j.Command, " "))
	fmt.Fprintf(w, "WorkDir:         %s\n", j.WorkDir)
	fmt.Fprintf(w, "CheckpointDir:   %s\n", j.CheckpointDir)
	if j.HookCheckpoint != "" {
		fmt.Fprintf(w, "HookCheckpoint:  %s\n", j.HookCheckpoint)
	}
	if j.HookResume != "" {
		fmt.Fprintf(w, "HookResume:      %s\n", j.HookResume)
	}
	if j.FailureReason != "" {
		fmt.Fprintf(w, "FailureReason:   %s\n", j.FailureReason)
	}
	fmt.Fprintf(w, "RegisteredAt:    %s\n", j.RegisteredAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(w, "UpdatedAt:       %s\n", j.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	return 0
}
