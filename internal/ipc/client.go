package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"surviva/internal/store"
)

// Client talks to a running surviva daemon over its Unix domain socket.
type Client struct {
	socketPath string
	timeout    time.Duration
}

// NewClient returns a client bound to socketPath.
func NewClient(socketPath string) *Client {
	return &Client{socketPath: socketPath, timeout: 3 * time.Second}
}

func (c *Client) call(req Request) (Response, error) {
	conn, err := net.DialTimeout("unix", c.socketPath, c.timeout)
	if err != nil {
		return Response{}, fmt.Errorf("connect to daemon at %s: %w (is `surviva daemon` running?)", c.socketPath, err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("send request: %w", err)
	}

	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("read response: %w", err)
	}
	if !resp.OK {
		return resp, fmt.Errorf("daemon error: %s", resp.Error)
	}
	return resp, nil
}

// Ping checks that the daemon is reachable.
func (c *Client) Ping() error {
	_, err := c.call(Request{Action: ActionPing})
	return err
}

// Register tells the daemon to start tracking a job and returns its id.
// requestedBy is recorded as the job's Owner (see cmd/surviva.currentOSUser).
func (c *Client) Register(j RegisterJob, requestedBy string) (string, error) {
	resp, err := c.call(Request{Action: ActionRegister, Job: &j, RequestedBy: requestedBy})
	if err != nil {
		return "", err
	}
	return resp.JobID, nil
}

// List returns every active (non-terminal) job.
func (c *Client) List() ([]store.Job, error) {
	resp, err := c.call(Request{Action: ActionList})
	if err != nil {
		return nil, err
	}
	return resp.Jobs, nil
}

// Show returns one job regardless of status, and its full transition history
// when includeHistory is set (see `surviva show -history`).
func (c *Client) Show(jobID string, includeHistory bool) (store.Job, []store.HistoryEntry, error) {
	resp, err := c.call(Request{Action: ActionShow, JobID: jobID, IncludeHistory: includeHistory})
	if err != nil {
		return store.Job{}, nil, err
	}
	if resp.Job == nil {
		return store.Job{}, nil, fmt.Errorf("daemon returned no job for %s", jobID)
	}
	return *resp.Job, resp.History, nil
}

// Pause checkpoints a RUNNING job right now.
func (c *Client) Pause(jobID, requestedBy string) (string, error) {
	resp, err := c.call(Request{Action: ActionPause, JobID: jobID, RequestedBy: requestedBy})
	if err != nil {
		return "", err
	}
	return resp.Message, nil
}

// Resume brings a CHECKPOINT_CREATED or RESTORE_FAILED job back to RUNNING.
func (c *Client) Resume(jobID, requestedBy string) (string, error) {
	resp, err := c.call(Request{Action: ActionResume, JobID: jobID, RequestedBy: requestedBy})
	if err != nil {
		return "", err
	}
	return resp.Message, nil
}

// Cancel stops (if running) and marks a job CANCELED.
func (c *Client) Cancel(jobID, requestedBy string) error {
	_, err := c.call(Request{Action: ActionCancel, JobID: jobID, RequestedBy: requestedBy})
	return err
}

// Complete reports that a tracked child exited, moving it to COMPLETED
// (exitCode == 0) or FAILED otherwise.
func (c *Client) Complete(jobID string, exitCode int, errMsg, requestedBy string) error {
	_, err := c.call(Request{Action: ActionComplete, JobID: jobID, ExitCode: exitCode, ErrMsg: errMsg, RequestedBy: requestedBy})
	return err
}

// Prune removes checkpoint files for terminal jobs -- one (jobID != "") or
// every terminal job (jobID == ""). It never deletes store rows.
func (c *Client) Prune(jobID string) (message string, failures []string, err error) {
	resp, err := c.call(Request{Action: ActionPrune, JobID: jobID})
	if err != nil {
		return "", nil, err
	}
	return resp.Message, resp.Failures, nil
}
