package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"surviva/internal/store"
)

func TestRenderJobDetailKeyValue(t *testing.T) {
	j := store.Job{
		ID:            "job-1",
		PID:           111,
		PGID:          111,
		Status:        store.StatusCheckpointCreationFailed,
		Command:       []string{"sleep", "300"},
		WorkDir:       "/tmp",
		CheckpointDir: "/var/lib/surviva/checkpoints/job-1",
		FailureReason: "criu dump: disk full",
		RegisteredAt:  time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 9, 16, 0, 1, 0, 0, time.UTC),
	}
	var buf bytes.Buffer
	renderJobDetail(&buf, j, false)
	out := buf.String()
	for _, want := range []string{"job-1", "CHECKPOINT_CREATION_FAILED", "sleep 300", "criu dump: disk full"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderJobDetailOmitsEmptyOptionalFields(t *testing.T) {
	j := store.Job{ID: "job-1", Status: store.StatusRunning}
	var buf bytes.Buffer
	renderJobDetail(&buf, j, false)
	out := buf.String()
	for _, absent := range []string{"HookCheckpoint", "HookResume", "FailureReason"} {
		if strings.Contains(out, absent) {
			t.Errorf("output should omit empty %s:\n%s", absent, out)
		}
	}
}

func TestRenderJobDetailJSONRoundTrips(t *testing.T) {
	j := store.Job{ID: "job-1", PID: 111, Status: store.StatusRunning, Command: []string{"sleep"}}
	var buf bytes.Buffer
	renderJobDetail(&buf, j, true)
	var got store.Job
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if got.ID != j.ID || got.Status != j.Status {
		t.Errorf("round-tripped job = %+v, want %+v", got, j)
	}
}
