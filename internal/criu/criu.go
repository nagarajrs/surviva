// Package criu wraps the `criu` CLI to checkpoint and restore process trees.
// It shells out rather than binding libcriu, keeping the daemon dependency
// on CRIU limited to a PATH lookup.
package criu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// BinaryName is the executable surviva looks for on PATH.
const BinaryName = "criu"

// Dump checkpoints the process tree rooted at pid into imagesDir using
// `criu dump`. The tree is stopped as part of the dump (no --leave-running):
// once an interruption notice has fired there is nothing to leave running
// for, and stopping it avoids the tree mutating state after its memory was
// already captured.
//
// surviva's tracked children are started in their own session (see
// internal/procattr), so no external shell/tty needs to be reattached on
// dump or restore and --shell-job is deliberately not used.
func Dump(ctx context.Context, pid int, imagesDir string) error {
	if err := os.MkdirAll(imagesDir, 0o700); err != nil {
		return fmt.Errorf("create images dir %s: %w", imagesDir, err)
	}

	logFile := filepath.Join(imagesDir, "dump.log")
	cmd := exec.CommandContext(ctx, BinaryName,
		"dump",
		"--tree", strconv.Itoa(pid),
		"--images-dir", imagesDir,
		"--log-file", logFile,
		"-v4",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("criu dump pid %d: %w\n%s\n(see %s)", pid, err, out, logFile)
	}
	return nil
}

// Restore resumes a process tree previously dumped into imagesDir and
// returns the restored root task's PID. --restore-detached returns control
// to the caller once the tree is running rather than blocking for its
// lifetime; --pidfile is how a detached restore reports which PID it
// resumed as.
func Restore(ctx context.Context, imagesDir string) (int, error) {
	logFile := filepath.Join(imagesDir, "restore.log")
	pidFile := filepath.Join(imagesDir, "restore.pid")
	cmd := exec.CommandContext(ctx, BinaryName,
		"restore",
		"--images-dir", imagesDir,
		"--restore-detached",
		"--pidfile", pidFile,
		"--log-file", logFile,
		"-v4",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("criu restore from %s: %w\n%s\n(see %s)", imagesDir, err, out, logFile)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, fmt.Errorf("criu restore from %s: succeeded but couldn't read %s: %w", imagesDir, pidFile, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("criu restore from %s: invalid pid in %s: %w", imagesDir, pidFile, err)
	}
	return pid, nil
}

// Available reports whether the criu binary can be found on PATH.
func Available() bool {
	_, err := exec.LookPath(BinaryName)
	return err == nil
}
