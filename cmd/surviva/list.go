package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"surviva/internal/ipc"
)

// listCmd implements `surviva list [flags]`: show jobs currently tracked by
// the daemon.
func listCmd(args []string) int {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	asJSON := fs.Bool("json", false, "print raw JSON instead of a table")
	_ = fs.Parse(args)

	client := ipc.NewClient(*socketPath)
	jobs, err := client.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva list: %v\n", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(jobs); err != nil {
			fmt.Fprintf(os.Stderr, "surviva list: %v\n", err)
			return 1
		}
		return 0
	}

	if len(jobs) == 0 {
		fmt.Println("no tracked jobs")
		return 0
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "JOB ID\tPID\tSTATUS\tPRIORITY\tCOMMAND")
	for _, j := range jobs {
		fmt.Fprintf(w, "%s\t%d\t%s\t%d\t%s\n", j.ID, j.PID, j.Status, j.Priority, strings.Join(j.Command, " "))
	}
	w.Flush()
	return 0
}
