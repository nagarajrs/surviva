package main

import (
	"fmt"
	"os"
	"os/user"

	"surviva/internal/auditlog"
	"surviva/internal/config"
)

// currentOSUser returns the OS username running this CLI invocation, for
// attribution in store.Job.Owner and store.HistoryEntry.ChangedBy. Falls
// back to "unknown" rather than failing the command outright.
func currentOSUser() string {
	u, err := user.Current()
	if err != nil {
		return "unknown"
	}
	return u.Username
}

// openAuditLogger loads confPath just to find AuditLogPath and opens the
// shared audit log. A failure here is never fatal to the command's actual
// action -- callers get a nil *auditlog.Logger (logCLI silently no-ops on
// nil) and should print the returned error as a warning, not exit on it.
func openAuditLogger(confPath string) (*auditlog.Logger, error) {
	cfg, err := config.Load(confPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", confPath, err)
	}
	al, err := auditlog.Open(cfg.AuditLogPath)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return al, nil
}

// logCLI records one audit entry for a command invocation. Never fatal --
// a logging failure is printed as a warning, nothing more.
func logCLI(al *auditlog.Logger, action, jobID, outcome, detail string) {
	if al == nil {
		return
	}
	if err := al.Log(auditlog.Entry{Component: "cli", Action: action, JobID: jobID, Outcome: outcome, Detail: detail}); err != nil {
		fmt.Fprintf(os.Stderr, "surviva: warning: failed to write audit log: %v\n", err)
	}
}

// warnAuditUnavailable prints openAuditLogger's error as a non-fatal
// warning -- callers proceed with a nil logger.
func warnAuditUnavailable(err error) {
	fmt.Fprintf(os.Stderr, "surviva: warning: audit logging unavailable: %v\n", err)
}
