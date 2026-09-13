package main

import (
	"flag"
	"fmt"
	"os"

	"surviva/internal/ipc"
)

// stopCmd implements `surviva stop [flags] <job-id>`: sends SIGTERM to a
// tracked job's process group and removes it from tracking, whether or not
// the process that registered it (e.g. `surviva run`) is still around to
// deregister it itself -- e.g. its `surviva run` wrapper already died some
// other way (killed directly, a dropped session), leaving the job stuck
// showing RUNNING in `surviva list` forever with nothing left to stop it.
func stopCmd(args []string) int {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to the surviva daemon socket")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: surviva stop [flags] <job-id>")
		return 2
	}
	jobID := rest[0]

	client := ipc.NewClient(*socketPath)
	if err := client.Stop(jobID); err != nil {
		fmt.Fprintf(os.Stderr, "surviva stop: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "surviva stop: stopped job %s\n", jobID)
	return 0
}
