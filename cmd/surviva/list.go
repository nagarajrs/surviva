package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"surviva/internal/config"
	"surviva/internal/ipc"
	"surviva/internal/store"
)

// listCmd implements `surviva list [-json]`: show active (non-terminal)
// jobs.
func listCmd(args []string) int {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf (for audit logging)")
	asJSON := fs.Bool("json", false, "print raw JSON instead of a table")
	_ = fs.Parse(args)

	al, err := openAuditLogger(*confPath)
	if err != nil {
		warnAuditUnavailable(err)
	} else {
		defer al.Close()
	}

	client := ipc.NewClient(*socketPath)
	jobs, err := client.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva list: %v\n", err)
		logCLI(al, "list", "", "error", err.Error())
		return 1
	}
	logCLI(al, "list", "", "ok", "")
	return renderJobList(os.Stdout, jobs, *asJSON)
}

func renderJobList(w io.Writer, jobs []store.Job, asJSON bool) int {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(jobs); err != nil {
			fmt.Fprintf(os.Stderr, "surviva list: %v\n", err)
			return 1
		}
		return 0
	}

	if len(jobs) == 0 {
		fmt.Fprintln(w, "no active jobs")
		return 0
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "JOB ID\tPID\tSTATUS\tDURATION\tCOMMAND")
	for _, j := range jobs {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", j.ID, j.PID, j.Status, jobDuration(j), strings.Join(j.Command, " "))
	}
	tw.Flush()
	return 0
}
