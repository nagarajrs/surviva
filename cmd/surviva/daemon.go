package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"surviva/internal/auditlog"
	"surviva/internal/config"
	"surviva/internal/daemon"
	"surviva/internal/daemon/provider/aws"
	"surviva/internal/ipc"
	"surviva/internal/sigset"
	"surviva/internal/store"
)

// daemonCmd implements `surviva daemon [flags]`: wire config/store/audit-log
// and a cloud-provider poller into a running daemon.Daemon and serve it.
func daemonCmd(args []string) int {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	confPath := fs.String("conf", config.DefaultPath(), "path to surviva.conf")
	socketPath := fs.String("socket", ipc.DefaultSocketPath(), "path to listen on")
	_ = fs.Parse(args)

	cfg, err := config.Load(*confPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva daemon: %v\n", err)
		return 1
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva daemon: %v\n", err)
		return 1
	}
	defer st.Close()

	al, err := auditlog.Open(cfg.AuditLogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "surviva daemon: %v\n", err)
		return 1
	}
	defer al.Close()

	// config.Load already refuses any CloudProvider it doesn't implement,
	// so "aws" is the only value that can reach here today.
	var provider daemon.Provider
	switch cfg.CloudProvider {
	case "aws":
		provider = aws.New(cfg.PollInterval)
	default:
		fmt.Fprintf(os.Stderr, "surviva daemon: no provider implementation for %q\n", cfg.CloudProvider)
		return 1
	}

	d := daemon.New(daemon.Config{
		Store:                    st,
		Audit:                    al,
		Provider:                 provider,
		CheckpointBaseDir:        cfg.CheckpointBaseDir,
		MaxConcurrentCheckpoints: cfg.MaxConcurrentCheckpoints,
	})

	ctx, stop := signal.NotifyContext(context.Background(), sigset.TermSignals()...)
	defer stop()

	fmt.Fprintf(os.Stderr, "surviva daemon: listening on %s (provider=%s)\n", *socketPath, cfg.CloudProvider)
	if err := d.Run(ctx, *socketPath); err != nil {
		fmt.Fprintf(os.Stderr, "surviva daemon: %v\n", err)
		return 1
	}
	return 0
}
