// Package imds talks to the EC2 Instance Metadata Service (IMDSv2) to
// detect Spot rebalance recommendations and interruption notices.
package imds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	defaultBaseURL   = "http://169.254.169.254"
	tokenTTL         = 6 * time.Hour
	tokenRefreshSlop = 30 * time.Second

	pathToken               = "/latest/api/token"
	pathRebalanceRecommend  = "/latest/meta-data/events/recommendations/rebalance"
	pathSpotInstanceAction  = "/latest/meta-data/spot/instance-action"
)

// Client is an IMDSv2 client with automatic token refresh.
type Client struct {
	baseURL    string
	httpClient *http.Client

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

// NewClient returns a Client pointed at the real IMDS endpoint, unless
// overridden by SURVIVA_IMDS_ENDPOINT (used to point at a local mock server
// in tests and in local development off-EC2).
func NewClient() *Client {
	baseURL := defaultBaseURL
	if v := os.Getenv("SURVIVA_IMDS_ENDPOINT"); v != "" {
		baseURL = v
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *Client) getToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExpiry.Add(-tokenRefreshSlop)) {
		return c.token, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+pathToken, nil)
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", fmt.Sprintf("%d", int(tokenTTL.Seconds())))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch imds token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read imds token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("imds token request returned %d: %s", resp.StatusCode, body)
	}

	c.token = string(body)
	c.tokenExpiry = time.Now().Add(tokenTTL)
	return c.token, nil
}

// get performs an authenticated GET against an IMDS path. found is false
// (with no error) when IMDS returns 404, which is how IMDS signals "this
// notice/recommendation does not currently exist" rather than an error.
func (c *Client) get(ctx context.Context, path string) (found bool, body []byte, err error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return false, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return false, nil, fmt.Errorf("build request for %s: %w", path, err)
	}
	req.Header.Set("X-aws-ec2-metadata-token", token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, fmt.Errorf("read response for %s: %w", path, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return true, body, nil
	case http.StatusNotFound:
		return false, nil, nil
	default:
		return false, nil, fmt.Errorf("GET %s returned %d: %s", path, resp.StatusCode, body)
	}
}

// RebalanceRecommendation is the body of a Spot rebalance recommendation.
// It is a best-effort, earlier signal than InstanceAction — AWS does not
// guarantee it precedes every interruption.
type RebalanceRecommendation struct {
	NoticeTime time.Time `json:"noticeTime"`
}

// RebalanceRecommendation checks for a pending rebalance recommendation.
// It returns (nil, nil) when none is present.
func (c *Client) RebalanceRecommendation(ctx context.Context) (*RebalanceRecommendation, error) {
	found, body, err := c.get(ctx, pathRebalanceRecommend)
	if err != nil || !found {
		return nil, err
	}
	var rr RebalanceRecommendation
	if err := json.Unmarshal(body, &rr); err != nil {
		return nil, fmt.Errorf("parse rebalance recommendation: %w", err)
	}
	return &rr, nil
}

// InstanceAction is the body of a Spot interruption notice: the action AWS
// will take (terminate/stop/hibernate) and when.
type InstanceAction struct {
	Action string    `json:"action"`
	Time   time.Time `json:"time"`
}

// SpotInstanceAction checks for a pending Spot interruption notice. This is
// the hard ~2-minute deadline. It returns (nil, nil) when none is present.
func (c *Client) SpotInstanceAction(ctx context.Context) (*InstanceAction, error) {
	found, body, err := c.get(ctx, pathSpotInstanceAction)
	if err != nil || !found {
		return nil, err
	}
	var ia InstanceAction
	if err := json.Unmarshal(body, &ia); err != nil {
		return nil, fmt.Errorf("parse instance action: %w", err)
	}
	return &ia, nil
}
