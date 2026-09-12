package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"surviva/internal/job"
)

// Client talks to a running `surviva daemon` over its Unix domain socket.
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
func (c *Client) Register(j RegisterJob) (string, error) {
	resp, err := c.call(Request{Action: ActionRegister, Job: &j})
	if err != nil {
		return "", err
	}
	return resp.JobID, nil
}

// Deregister tells the daemon to stop tracking a job (normal completion).
func (c *Client) Deregister(jobID string) error {
	_, err := c.call(Request{Action: ActionDeregister, JobID: jobID})
	return err
}

// List returns every job currently tracked by the daemon.
func (c *Client) List() ([]job.Job, error) {
	resp, err := c.call(Request{Action: ActionList})
	if err != nil {
		return nil, err
	}
	return resp.Jobs, nil
}
