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

// Stop asks the daemon to send SIGTERM to a tracked job's process group and
// remove it from tracking, regardless of whether the process that
// registered it (e.g. `surviva run`) is still around to do so itself.
func (c *Client) Stop(jobID string) error {
	_, err := c.call(Request{Action: ActionStop, JobID: jobID})
	return err
}

// Pause asks the daemon to checkpoint a RUNNING job right now. If local is
// true, the checkpoint is stored on local disk only for this job, even if
// the daemon is otherwise configured for durable S3/EBS storage. It returns
// a human-readable description of where the checkpoint ended up.
func (c *Client) Pause(jobID string, local bool) (string, error) {
	resp, err := c.call(Request{Action: ActionPause, JobID: jobID, Local: local})
	if err != nil {
		return "", err
	}
	return resp.Message, nil
}

// Resume asks the daemon to resume a CHECKPOINT_COMPLETE job from its local
// checkpoint directory on this same instance. Unlike `surviva restore`, this
// never touches S3, EBS, or DynamoDB -- it only works if the local
// checkpoint images this daemon dumped are still on disk. It returns a
// human-readable description of the resumed process.
func (c *Client) Resume(jobID string) (string, error) {
	resp, err := c.call(Request{Action: ActionResume, JobID: jobID})
	if err != nil {
		return "", err
	}
	return resp.Message, nil
}
