// Command surviva is the CLI + daemon for checkpoint/restore protection
// against Spot instance interruptions. See `surviva -h`.
package main

import (
	"fmt"
	"os"

	"surviva/internal/version"
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
	case "daemon":
		code = daemonCmd(os.Args[2:])
	case "list":
		code = listCmd(os.Args[2:])
	case "stop":
		code = stopCmd(os.Args[2:])
	case "pause":
		code = pauseCmd(os.Args[2:])
	case "resume":
		code = resumeCmd(os.Args[2:])
	case "restore":
		code = restoreCmd(os.Args[2:])
	case "version", "-v", "-version", "--version":
		fmt.Println("surviva " + version.Version)
		code = 0
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
	fmt.Fprintf(os.Stderr, `surviva %s - checkpoint/restore protection for Spot interruptions

Usage:
  surviva run [flags] -- <command> [args...]   Run and track a command
  surviva daemon [flags]                       Run the surviva daemon
  surviva list [flags]                         List jobs tracked by the daemon
  surviva stop [flags] <job-id>                Terminate and untrack a RUNNING job
  surviva pause [flags] <job-id>               Checkpoint a RUNNING job on demand
  surviva resume [flags] <job-id>              Resume a checkpointed job from local disk
  surviva restore [flags] <job-id>             Restore a checkpointed job on this instance
  surviva version                              Print the surviva version

Run 'surviva <command> -h' for flags on a specific subcommand.
`, version.Version)
}
