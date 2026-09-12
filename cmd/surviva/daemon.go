package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"surviva/internal/daemon"
	"surviva/internal/ipc"
	"surviva/internal/sigset"
)

// daemonCmd implements `surviva daemon [flags]`: the long-running process
// (systemd service on a real instance) that tracks jobs registered by
// `surviva run` and, in later phases, polls IMDS and drives checkpointing.
func daemonCmd(args []string) int {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to listen on")
	dbPath := fs.String("db", ipc.DefaultDBPath(), "path to the sqlite job database")
	checkpointDir := fs.String("checkpoint-dir", ipc.DefaultCheckpointDir(), "directory to write local checkpoint images under")
	enableIMDS := fs.Bool("imds", true, "poll IMDS for Spot rebalance/interruption signals (disable only for local development off-EC2)")
	_ = fs.Parse(args)

	d, err := daemon.New(daemon.Config{
		SocketPath:    *socketPath,
		DBPath:        *dbPath,
		CheckpointDir: *checkpointDir,
		EnableIMDS:    *enableIMDS,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva daemon: %v\n", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), sigset.TermSignals()...)
	defer cancel()

	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "surviva daemon: %v\n", err)
		return 1
	}
	return 0
}
