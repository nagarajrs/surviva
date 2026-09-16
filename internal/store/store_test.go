package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenCreatesMissingParentDir(t *testing.T) {
	// Regression: Open must create its db file's parent directory itself
	// (like auditlog.Open does), not assume the caller already did --
	// daemon's own wiring only ensures the socket's dir exists, not the
	// store's.
	path := filepath.Join(t.TempDir(), "nested", "does", "not", "exist", "jobs.db")
	s, err := Open(Options{Path: path})
	if err != nil {
		t.Fatalf("Open with missing parent dirs: %v", err)
	}
	s.Close()
}

func TestOpenMigratesPreOwnerColumnDatabase(t *testing.T) {
	// Regression: an in-place upgrade from before Owner/job_history existed
	// left behind a jobs table with no owner column. CREATE TABLE IF NOT
	// EXISTS is a no-op against it, so without a real migration step Insert
	// used to fail outright with "no such column: owner" on first use after
	// upgrading -- reproduced against real SQLite before this test existed.
	path := filepath.Join(t.TempDir(), "jobs.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE jobs (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			pid             INTEGER NOT NULL,
			pgid            INTEGER NOT NULL,
			checkpoint_dir  TEXT NOT NULL,
			command         TEXT NOT NULL,
			work_dir        TEXT NOT NULL,
			hook_checkpoint TEXT NOT NULL DEFAULT '',
			hook_resume     TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL,
			failure_reason  TEXT NOT NULL DEFAULT '',
			registered_at   DATETIME NOT NULL,
			updated_at      DATETIME NOT NULL
		)`); err != nil {
		t.Fatalf("seed pre-upgrade schema: %v", err)
	}
	raw.Close()

	s, err := Open(Options{Path: path})
	if err != nil {
		t.Fatalf("Open against a pre-owner-column database: %v", err)
	}
	defer s.Close()

	id := insertJob(t, s, newJob())
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Owner != "alice" {
		t.Errorf("Owner = %q, want %q", got.Owner, "alice")
	}
}

func TestOpenRejectsUnsupportedDriver(t *testing.T) {
	_, err := Open(Options{Driver: "postgres", Host: "localhost"})
	if err == nil {
		t.Fatal("expected Open to reject an unsupported driver before attempting any connection")
	}
}

func TestOpenDefaultsToSQLite(t *testing.T) {
	// Driver left empty (the zero value) must behave exactly like an
	// explicit "sqlite" -- this is what every existing caller relies on.
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "jobs.db")})
	if err != nil {
		t.Fatalf("Open with empty Driver: %v", err)
	}
	s.Close()
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "jobs.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newJob() Job {
	now := time.Now().UTC()
	return Job{
		PID:           1234,
		PGID:          1234,
		CheckpointDir: "/var/lib/surviva/checkpoints/pending",
		Command:       []string{"sleep", "300"},
		WorkDir:       "/tmp",
		Status:        StatusRunning,
		Owner:         "alice",
		RegisteredAt:  now,
		UpdatedAt:     now,
	}
}

func insertJob(t *testing.T, s *Store, j Job) string {
	t.Helper()
	id, err := s.Insert(j)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return id
}

func TestInsertAssignsSequentialIDs(t *testing.T) {
	s := openTestStore(t)
	first := insertJob(t, s, newJob())
	second := insertJob(t, s, newJob())
	third := insertJob(t, s, newJob())

	// Slurm-style: plain increasing integers, not UUIDs.
	if first != "1" || second != "2" || third != "3" {
		t.Errorf("got ids %q, %q, %q, want \"1\", \"2\", \"3\"", first, second, third)
	}
}

func TestInsertGetRoundTrip(t *testing.T) {
	s := openTestStore(t)
	j := newJob()
	j.HookCheckpoint = "/opt/ckpt.sh"
	j.HookResume = "/opt/resume.sh"

	id := insertJob(t, s, j)
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != id || got.PID != j.PID || got.PGID != j.PGID || got.CheckpointDir != j.CheckpointDir ||
		got.WorkDir != j.WorkDir || got.HookCheckpoint != j.HookCheckpoint || got.HookResume != j.HookResume ||
		got.Status != j.Status || got.Owner != j.Owner || len(got.Command) != 2 || got.Command[0] != "sleep" || got.Command[1] != "300" {
		t.Fatalf("round-trip mismatch: got %+v, want %+v (id %s)", got, j, id)
	}
}

func TestGetRejectsNonNumericID(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Get("not-a-number"); err == nil {
		t.Fatal("expected Get to reject a non-numeric job id")
	}
}

func TestListExcludesTerminalIncludesActive(t *testing.T) {
	s := openTestStore(t)

	active := []Status{
		StatusRunning, StatusCheckpointInProgress, StatusCheckpointCreated,
		StatusCheckpointCreationFailed, StatusRestorePending, StatusRestoreFailed,
	}
	terminal := []Status{StatusFailed, StatusCanceled, StatusCompleted}

	activeIDs := map[string]bool{}
	for _, st := range active {
		j := newJob()
		j.Status = st
		activeIDs[insertJob(t, s, j)] = true
	}
	terminalIDs := map[string]bool{}
	for _, st := range terminal {
		j := newJob()
		j.Status = st
		terminalIDs[insertJob(t, s, j)] = true
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, j := range got {
		gotIDs[j.ID] = true
	}
	for id := range activeIDs {
		if !gotIDs[id] {
			t.Errorf("List() missing active job %s", id)
		}
	}
	for id := range terminalIDs {
		if gotIDs[id] {
			t.Errorf("List() included terminal job %s", id)
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
	for id := range terminalIDs {
		if !gotTerminalIDs[id] {
			t.Errorf("ListTerminal() missing terminal job %s", id)
		}
	}
	for id := range activeIDs {
		if gotTerminalIDs[id] {
			t.Errorf("ListTerminal() included active job %s", id)
		}
	}

	// Every terminal job must still be reachable via Get.
	for id := range terminalIDs {
		if _, err := s.Get(id); err != nil {
			t.Errorf("Get(%s) after terminal: %v", id, err)
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
	j := newJob()
	j.Status = StatusCompleted
	id := insertJob(t, s, j)
	if err := s.UpdateStatus(id, StatusRunning, "", "tester"); err == nil {
		t.Fatal("expected UpdateStatus to reject COMPLETED -> RUNNING")
	}
}

func TestUpdateStatusSetsFailureReason(t *testing.T) {
	s := openTestStore(t)
	j := newJob()
	j.Status = StatusCheckpointInProgress
	id := insertJob(t, s, j)
	if err := s.UpdateStatus(id, StatusCheckpointCreationFailed, "criu dump: disk full", "tester"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err := s.Get(id)
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
	j := newJob()
	j.Status = StatusRestorePending
	j.PID, j.PGID = 111, 111
	id := insertJob(t, s, j)
	if err := s.UpdatePID(id, 999, 999, "tester"); err != nil {
		t.Fatalf("UpdatePID: %v", err)
	}
	got, err := s.Get(id)
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

func TestUpdateCheckpointDir(t *testing.T) {
	s := openTestStore(t)
	id := insertJob(t, s, newJob())

	dir := "/var/lib/surviva/checkpoints/" + id
	if err := s.UpdateCheckpointDir(id, dir); err != nil {
		t.Fatalf("UpdateCheckpointDir: %v", err)
	}
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CheckpointDir != dir {
		t.Errorf("CheckpointDir = %q, want %q", got.CheckpointDir, dir)
	}
}

func TestHistoryRecordsEveryTransition(t *testing.T) {
	s := openTestStore(t)
	id := insertJob(t, s, newJob())

	if err := s.UpdateStatus(id, StatusCheckpointInProgress, "", "alice"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if err := s.UpdateStatus(id, StatusCheckpointCreated, "", "daemon"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	hist, err := s.History(id)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	want := []struct {
		from, to  Status
		changedBy string
	}{
		{StatusRunning, StatusCheckpointInProgress, "alice"},
		{StatusCheckpointInProgress, StatusCheckpointCreated, "daemon"},
	}
	if len(hist) != len(want) {
		t.Fatalf("got %d history entries, want %d: %+v", len(hist), len(want), hist)
	}
	for i, w := range want {
		if hist[i].JobID != id || hist[i].FromStatus != w.from || hist[i].ToStatus != w.to || hist[i].ChangedBy != w.changedBy {
			t.Errorf("entry %d = %+v, want from=%s to=%s changedBy=%s", i, hist[i], w.from, w.to, w.changedBy)
		}
		if hist[i].ChangedAt.IsZero() {
			t.Errorf("entry %d: ChangedAt is zero", i)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	for _, st := range []Status{StatusFailed, StatusCanceled, StatusCompleted} {
		if !IsTerminal(st) {
			t.Errorf("IsTerminal(%s) = false, want true", st)
		}
	}
	for _, st := range []Status{StatusRunning, StatusCheckpointInProgress, StatusCheckpointCreated} {
		if IsTerminal(st) {
			t.Errorf("IsTerminal(%s) = true, want false", st)
		}
	}
}
