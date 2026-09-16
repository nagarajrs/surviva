package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"surviva/internal/store"
)

func TestRenderJobListTable(t *testing.T) {
	jobs := []store.Job{
		{ID: "job-1", PID: 111, Status: store.StatusRunning, Command: []string{"sleep", "300"}},
	}
	var buf bytes.Buffer
	if code := renderJobList(&buf, jobs, false); code != 0 {
		t.Fatalf("renderJobList returned %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "job-1") || !strings.Contains(out, "RUNNING") || !strings.Contains(out, "sleep 300") {
		t.Errorf("table output missing expected fields:\n%s", out)
	}
}

func TestRenderJobListEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderJobList(&buf, nil, false)
	if strings.TrimSpace(buf.String()) != "no active jobs" {
		t.Errorf("got %q, want %q", buf.String(), "no active jobs")
	}
}

func TestRenderJobListJSONRoundTrips(t *testing.T) {
	jobs := []store.Job{
		{ID: "job-1", PID: 111, Status: store.StatusRunning, Command: []string{"sleep", "300"}},
		{ID: "job-2", PID: 222, Status: store.StatusCheckpointCreated},
	}
	var buf bytes.Buffer
	if code := renderJobList(&buf, jobs, true); code != 0 {
		t.Fatalf("renderJobList returned %d", code)
	}
	var got []store.Job
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(got) != 2 || got[0].ID != "job-1" || got[1].ID != "job-2" {
		t.Errorf("round-tripped jobs = %+v, want the original two", got)
	}
}
