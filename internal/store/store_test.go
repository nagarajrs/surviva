package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newJob(id string) Job {
	now := time.Now().UTC()
	return Job{
		ID:            id,
		PID:           1234,
		PGID:          1234,
		CheckpointDir: "/var/lib/surviva/checkpoints/" + id,
		Command:       []string{"sleep", "300"},
		WorkDir:       "/tmp",
		Status:        StatusRunning,
		RegisteredAt:  now,
		UpdatedAt:     now,
	}
}

func TestInsertGetRoundTrip(t *testing.T) {
	s := openTestStore(t)
	j := newJob("job-1")
	j.HookCheckpoint = "/opt/ckpt.sh"
	j.HookResume = "/opt/resume.sh"

	if err := s.Insert(j); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Get("job-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != j.ID || got.PID != j.PID || got.PGID != j.PGID || got.CheckpointDir != j.CheckpointDir ||
		got.WorkDir != j.WorkDir || got.HookCheckpoint != j.HookCheckpoint || got.HookResume != j.HookResume ||
		got.Status != j.Status || len(got.Command) != 2 || got.Command[0] != "sleep" || got.Command[1] != "300" {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, j)
	}
}

func TestListExcludesTerminalIncludesActive(t *testing.T) {
	s := openTestStore(t)

	active := []Status{
		StatusRunning, StatusCheckpointInProgress, StatusCheckpointCreated,
		StatusCheckpointCreationFailed, StatusRestorePending, StatusRestoreFailed,
	}
	terminal := []Status{StatusFailed, StatusCanceled, StatusCompleted}

	for _, st := range append(append([]Status{}, active...), terminal...) {
		j := newJob("job-" + string(st))
		j.Status = st
		if err := s.Insert(j); err != nil {
			t.Fatalf("Insert(%s): %v", st, err)
		}
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, j := range got {
		gotIDs[j.ID] = true
	}
	for _, st := range active {
		if !gotIDs["job-"+string(st)] {
			t.Errorf("List() missing active job with status %s", st)
		}
	}
	for _, st := range terminal {
		if gotIDs["job-"+string(st)] {
			t.Errorf("List() included terminal job with status %s", st)
		}
	}

	// ListTerminal is List()'s mirror image.
	gotTerminal, err := s.ListTerminal()
	if err != nil {
		t.Fatalf("ListTerminal: %v", err)
	}
	gotTerminalIDs := map[string]bool{}
	for _, j := range gotTerminal {
		gotTerminalIDs[j.ID] = true
	}
	for _, st := range terminal {
		if !gotTerminalIDs["job-"+string(st)] {
			t.Errorf("ListTerminal() missing terminal job with status %s", st)
		}
	}
	for _, st := range active {
		if gotTerminalIDs["job-"+string(st)] {
			t.Errorf("ListTerminal() included active job with status %s", st)
		}
	}

	// Every terminal job must still be reachable via Get.
	for _, st := range terminal {
		if _, err := s.Get("job-" + string(st)); err != nil {
			t.Errorf("Get(job-%s) after terminal: %v", st, err)
		}
	}
}

func TestValidTransitions(t *testing.T) {
	valid := []struct{ from, to Status }{
		{StatusRunning, StatusCheckpointInProgress},
		{StatusRunning, StatusCanceled},
		{StatusRunning, StatusCompleted},
		{StatusRunning, StatusFailed},
		{StatusCheckpointInProgress, StatusCheckpointCreated},
		{StatusCheckpointInProgress, StatusCheckpointCreationFailed},
		{StatusCheckpointCreationFailed, StatusCheckpointInProgress},
		{StatusCheckpointCreationFailed, StatusCanceled},
		{StatusCheckpointCreated, StatusRestorePending},
		{StatusCheckpointCreated, StatusCanceled},
		{StatusRestorePending, StatusRunning},
		{StatusRestorePending, StatusRestoreFailed},
		{StatusRestoreFailed, StatusRestorePending},
		{StatusRestoreFailed, StatusCanceled},
	}
	for _, tc := range valid {
		if !ValidTransition(tc.from, tc.to) {
			t.Errorf("expected %s -> %s to be valid", tc.from, tc.to)
		}
	}

	invalid := []struct{ from, to Status }{
		{StatusCompleted, StatusRunning},
		{StatusCanceled, StatusCheckpointCreated},
		{StatusFailed, StatusRunning},
		{StatusRunning, StatusRestorePending},
		{StatusCheckpointCreated, StatusRunning},
	}
	for _, tc := range invalid {
		if ValidTransition(tc.from, tc.to) {
			t.Errorf("expected %s -> %s to be invalid", tc.from, tc.to)
		}
	}
}

func TestUpdateStatusRejectsInvalidTransition(t *testing.T) {
	s := openTestStore(t)
	j := newJob("job-1")
	j.Status = StatusCompleted
	if err := s.Insert(j); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.UpdateStatus("job-1", StatusRunning, ""); err == nil {
		t.Fatal("expected UpdateStatus to reject COMPLETED -> RUNNING")
	}
}

func TestUpdateStatusSetsFailureReason(t *testing.T) {
	s := openTestStore(t)
	j := newJob("job-1")
	j.Status = StatusCheckpointInProgress
	if err := s.Insert(j); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.UpdateStatus("job-1", StatusCheckpointCreationFailed, "criu dump: disk full"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err := s.Get("job-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusCheckpointCreationFailed {
		t.Errorf("status = %s, want %s", got.Status, StatusCheckpointCreationFailed)
	}
	if got.FailureReason != "criu dump: disk full" {
		t.Errorf("failure_reason = %q, want %q", got.FailureReason, "criu dump: disk full")
	}
}

func TestUpdatePIDResumesToRunning(t *testing.T) {
	s := openTestStore(t)
	j := newJob("job-1")
	j.Status = StatusRestorePending
	j.PID, j.PGID = 111, 111
	if err := s.Insert(j); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.UpdatePID("job-1", 999, 999); err != nil {
		t.Fatalf("UpdatePID: %v", err)
	}
	got, err := s.Get("job-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusRunning {
		t.Errorf("status = %s, want %s", got.Status, StatusRunning)
	}
	if got.PID != 999 || got.PGID != 999 {
		t.Errorf("pid/pgid = %d/%d, want 999/999", got.PID, got.PGID)
	}
}
