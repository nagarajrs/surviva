// Command surviva is the CLI + daemon for the redesigned, phased
// checkpoint/restore tool. See `surviva -h`.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var code int
	switch os.Args[1] {
	case "run":
		code = runCmd(os.Args[2:])
	case "join":
		code = joinCmd(os.Args[2:])
	case "pause":
		code = pauseCmd(os.Args[2:])
	case "resume":
		code = resumeCmd(os.Args[2:])
	case "list":
		code = listCmd(os.Args[2:])
	case "show":
		code = showCmd(os.Args[2:])
	case "cancel":
		code = cancelCmd(os.Args[2:])
	case "prune":
		code = pruneCmd(os.Args[2:])
	case "daemon":
		code = daemonCmd(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
		code = 0
	default:
		fmt.Fprintf(os.Stderr, "surviva: unknown command %q\n\n", os.Args[1])
		printUsage()
		code = 1
	}
	os.Exit(code)
}

func printUsage() {
	fmt.Fprint(os.Stderr, `surviva - checkpoint/restore protection, built module by module

Usage:
  surviva run [flags] -- <command> [args...]   Run and track a command
  surviva join [flags] <pid>                   Adopt an already-running process into tracking
  surviva pause <job-id>                       Checkpoint a RUNNING job now
  surviva resume <job-id>                      Resume a checkpointed job
  surviva list [-json]                         List active jobs
  surviva show [-json] <job-id>                Show one job, any status
  surviva cancel <job-id>                      Stop (if running) and mark a job CANCELED
  surviva prune [job-id]                       Clear checkpoint files for terminal job(s)
  surviva daemon [flags]                       Run the surviva daemon

Run 'surviva <command> -h' for flags on a specific subcommand.
`)
}
