package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"surviva/internal/config"
	"surviva/internal/ipc"
	"surviva/internal/store"
)

// showCmd implements `surviva show [-json] [-history] <job-id>`: full detail
// for one job, regardless of status, optionally with its full status-change
// history.
func showCmd(args []string) int {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf (for audit logging)")
	asJSON := fs.Bool("json", false, "print raw JSON instead of a key:value dump")
	history := fs.Bool("history", false, "also show the job's full status-change history")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva show [-json] [-history] <job-id>")
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
	job, hist, err := client.Show(jobID, *history)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva show: %v\n", err)
		logCLI(al, "show", jobID, "error", err.Error())
		return 1
	}
	logCLI(al, "show", jobID, "ok", "")
	return renderJobDetail(os.Stdout, job, hist, *asJSON)
}

// jobDuration reports how long a job has been (or was) running: against
// time.Now() while active, or its last transition time once terminal.
func jobDuration(j store.Job) time.Duration {
	if store.IsTerminal(j.Status) {
		return j.UpdatedAt.Sub(j.RegisteredAt)
	}
	return time.Since(j.RegisteredAt).Round(time.Second)
}

func renderJobDetail(w io.Writer, j store.Job, hist []store.HistoryEntry, asJSON bool) int {
	if asJSON {
		out := struct {
			store.Job
			Duration string               `json:"duration"`
			History  []store.HistoryEntry `json:"history,omitempty"`
		}{Job: j, Duration: jobDuration(j).String(), History: hist}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(os.Stderr, "surviva show: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(w, "ID:              %s\n", j.ID)
	fmt.Fprintf(w, "Status:          %s\n", j.Status)
	fmt.Fprintf(w, "Owner:           %s\n", j.Owner)
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
	fmt.Fprintf(w, "Duration:        %s\n", jobDuration(j))

	if hist != nil {
		fmt.Fprintf(w, "\nHistory:\n")
		for _, h := range hist {
			fmt.Fprintf(w, "  %s  %-24s -> %-24s  by %s\n", h.ChangedAt.Format("2006-01-02T15:04:05Z07:00"), h.FromStatus, h.ToStatus, h.ChangedBy)
		}
	}
	return 0
}
