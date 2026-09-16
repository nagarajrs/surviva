package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"surviva/internal/auditlog"
	"surviva/internal/ipc"
	"surviva/internal/store"
)

// fakeProvider never fires unless told to; used by every test that isn't
// specifically exercising the interruption fan-out.
type fakeProvider struct{}

func (fakeProvider) Run(ctx context.Context, onSignal func(string)) { <-ctx.Done() }

func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "jobs.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	al, err := auditlog.Open(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatalf("auditlog.Open: %v", err)
	}
	t.Cleanup(func() { al.Close() })

	d := New(Config{
		Store:                    st,
		Audit:                    al,
		Provider:                 fakeProvider{},
		CheckpointBaseDir:        t.TempDir(),
		MaxConcurrentCheckpoints: 2,
	})
	// Swap the real CRIU-backed functions for fakes -- see SPEC-daemon.md's
	// testing strategy: daemon's orchestration is unit-tested, CRIU itself
	// needs a real Linux box.
	d.checkpointFunc = func(ctx context.Context, dir string, j store.Job) error { return nil }
	d.resumeFunc = func(ctx context.Context, dir, hookResume, jobID string) (int, error) { return 4242, nil }
	return d
}

func registerJob(t *testing.T, d *Daemon) string {
	t.Helper()
	resp := d.dispatch(ipc.Request{Action: ipc.ActionRegister, Job: &ipc.RegisterJob{
		PID: 987654321, PGID: 987654321, Command: []string{"sleep", "300"}, WorkDir: "/tmp",
	}})
	if !resp.OK {
		t.Fatalf("register: %s", resp.Error)
	}
	return resp.JobID
}

func TestRegisterListShow(t *testing.T) {
	d := newTestDaemon(t)
	id := registerJob(t, d)

	listResp := d.dispatch(ipc.Request{Action: ipc.ActionList})
	if !listResp.OK {
		t.Fatalf("list: %s", listResp.Error)
	}
	found := false
	for _, j := range listResp.Jobs {
		if j.ID == id {
			found = true
		}
	}
	if !found {
		t.Errorf("list does not contain registered job %s", id)
	}

	showResp := d.dispatch(ipc.Request{Action: ipc.ActionShow, JobID: id})
	if !showResp.OK {
		t.Fatalf("show: %s", showResp.Error)
	}
	if showResp.Job == nil || showResp.Job.ID != id || showResp.Job.Status != store.StatusRunning {
		t.Errorf("show returned unexpected job: %+v", showResp.Job)
	}
}

func TestRegisterRefusedAfterInterrupted(t *testing.T) {
	d := newTestDaemon(t)
	d.interrupted.Store(true)

	resp := d.dispatch(ipc.Request{Action: ipc.ActionRegister, Job: &ipc.RegisterJob{PID: 1, PGID: 1}})
	if resp.OK {
		t.Fatal("expected Register to be refused after interruption latch")
	}
}

func TestRegisterValidatesHookPaths(t *testing.T) {
	d := newTestDaemon(t)

	t.Run("nonexistent path rejected", func(t *testing.T) {
		resp := d.dispatch(ipc.Request{Action: ipc.ActionRegister, Job: &ipc.RegisterJob{
			PID: 1, PGID: 1, HookCheckpoint: filepath.Join(t.TempDir(), "does-not-exist.sh"),
		}})
		if resp.OK {
			t.Error("expected Register to reject a nonexistent hook-checkpoint path")
		}
	})

	t.Run("non-executable file rejected", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("executable-bit semantics don't apply on Windows")
		}
		nonExec := filepath.Join(t.TempDir(), "not-executable.sh")
		if err := os.WriteFile(nonExec, []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
			t.Fatalf("write non-exec file: %v", err)
		}
		resp := d.dispatch(ipc.Request{Action: ipc.ActionRegister, Job: &ipc.RegisterJob{
			PID: 1, PGID: 1, HookResume: nonExec,
		}})
		if resp.OK {
			t.Error("expected Register to reject a non-executable hook-resume path")
		}
	})

	t.Run("real executable accepted", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("executable-bit semantics don't apply on Windows")
		}
		exec := filepath.Join(t.TempDir(), "real-hook.sh")
		if err := os.WriteFile(exec, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
			t.Fatalf("write exec file: %v", err)
		}
		resp := d.dispatch(ipc.Request{Action: ipc.ActionRegister, Job: &ipc.RegisterJob{
			PID: 1, PGID: 1, HookCheckpoint: exec,
		}})
		if !resp.OK {
			t.Errorf("expected Register to accept a real executable hook path: %s", resp.Error)
		}
	})

	t.Run("empty hook fields never validated", func(t *testing.T) {
		resp := d.dispatch(ipc.Request{Action: ipc.ActionRegister, Job: &ipc.RegisterJob{PID: 1, PGID: 1}})
		if !resp.OK {
			t.Errorf("expected Register with no hooks to succeed: %s", resp.Error)
		}
	})
}

func TestCompleteTransitionsByExitCode(t *testing.T) {
	d := newTestDaemon(t)

	okID := registerJob(t, d)
	resp := d.dispatch(ipc.Request{Action: ipc.ActionComplete, JobID: okID, ExitCode: 0})
	if !resp.OK {
		t.Fatalf("complete(0): %s", resp.Error)
	}
	got, _ := d.store.Get(okID)
	if got.Status != store.StatusCompleted {
		t.Errorf("status = %s, want %s", got.Status, store.StatusCompleted)
	}

	failID := registerJob(t, d)
	resp = d.dispatch(ipc.Request{Action: ipc.ActionComplete, JobID: failID, ExitCode: 1, ErrMsg: "boom"})
	if !resp.OK {
		t.Fatalf("complete(1): %s", resp.Error)
	}
	got, _ = d.store.Get(failID)
	if got.Status != store.StatusFailed || got.FailureReason != "boom" {
		t.Errorf("got status=%s reason=%q, want %s / %q", got.Status, got.FailureReason, store.StatusFailed, "boom")
	}
}

func TestCompleteIsNoOpOnceNotRunning(t *testing.T) {
	d := newTestDaemon(t)
	id := registerJob(t, d)

	pauseResp := d.dispatch(ipc.Request{Action: ipc.ActionPause, JobID: id})
	if !pauseResp.OK {
		t.Fatalf("pause: %s", pauseResp.Error)
	}

	resp := d.dispatch(ipc.Request{Action: ipc.ActionComplete, JobID: id, ExitCode: 0})
	if !resp.OK {
		t.Fatalf("complete after pause should be a no-op, not an error: %s", resp.Error)
	}
	got, _ := d.store.Get(id)
	if got.Status != store.StatusCheckpointCreated {
		t.Errorf("stale Complete overwrote status to %s, want unchanged %s", got.Status, store.StatusCheckpointCreated)
	}
}

func TestCancelRunningJob(t *testing.T) {
	d := newTestDaemon(t)
	id := registerJob(t, d)

	resp := d.dispatch(ipc.Request{Action: ipc.ActionCancel, JobID: id})
	if !resp.OK {
		t.Fatalf("cancel: %s", resp.Error)
	}
	got, _ := d.store.Get(id)
	if got.Status != store.StatusCanceled {
		t.Errorf("status = %s, want %s", got.Status, store.StatusCanceled)
	}
}

func TestCancelCheckpointCreatedJob(t *testing.T) {
	d := newTestDaemon(t)
	id := registerJob(t, d)
	if resp := d.dispatch(ipc.Request{Action: ipc.ActionPause, JobID: id}); !resp.OK {
		t.Fatalf("pause: %s", resp.Error)
	}

	resp := d.dispatch(ipc.Request{Action: ipc.ActionCancel, JobID: id})
	if !resp.OK {
		t.Fatalf("cancel: %s", resp.Error)
	}
	got, _ := d.store.Get(id)
	if got.Status != store.StatusCanceled {
		t.Errorf("status = %s, want %s", got.Status, store.StatusCanceled)
	}
}

func TestCancelRejectedMidCheckpoint(t *testing.T) {
	d := newTestDaemon(t)
	id := registerJob(t, d)
	if err := d.store.UpdateStatus(id, store.StatusCheckpointInProgress, ""); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	resp := d.dispatch(ipc.Request{Action: ipc.ActionCancel, JobID: id})
	if resp.OK {
		t.Fatal("expected Cancel to be rejected while CHECKPOINT_IN_PROGRESS")
	}
	got, _ := d.store.Get(id)
	if got.Status != store.StatusCheckpointInProgress {
		t.Errorf("status changed to %s despite rejected cancel", got.Status)
	}
}

func TestPauseCheckspointsAndRejectsWrongState(t *testing.T) {
	d := newTestDaemon(t)
	id := registerJob(t, d)

	resp := d.dispatch(ipc.Request{Action: ipc.ActionPause, JobID: id})
	if !resp.OK {
		t.Fatalf("pause: %s", resp.Error)
	}
	got, _ := d.store.Get(id)
	if got.Status != store.StatusCheckpointCreated {
		t.Errorf("status = %s, want %s", got.Status, store.StatusCheckpointCreated)
	}

	// Already checkpointed -- pausing again must be rejected, not re-run.
	resp = d.dispatch(ipc.Request{Action: ipc.ActionPause, JobID: id})
	if resp.OK {
		t.Fatal("expected second Pause on a CHECKPOINT_CREATED job to be rejected")
	}
}

func TestPauseRefusedAfterInterrupted(t *testing.T) {
	d := newTestDaemon(t)
	id := registerJob(t, d)
	d.interrupted.Store(true)

	resp := d.dispatch(ipc.Request{Action: ipc.ActionPause, JobID: id})
	if resp.OK {
		t.Fatal("expected Pause to be refused once interrupted")
	}
}

func TestPauseSurfacesCheckpointFailure(t *testing.T) {
	d := newTestDaemon(t)
	d.checkpointFunc = func(ctx context.Context, dir string, j store.Job) error {
		return errors.New("criu dump: disk full")
	}
	id := registerJob(t, d)

	resp := d.dispatch(ipc.Request{Action: ipc.ActionPause, JobID: id})
	if resp.OK {
		t.Fatal("expected Pause to surface the checkpoint failure")
	}
	got, _ := d.store.Get(id)
	if got.Status != store.StatusCheckpointCreationFailed || got.FailureReason != "criu dump: disk full" {
		t.Errorf("got status=%s reason=%q", got.Status, got.FailureReason)
	}
}

func TestResumeRestoresAndRejectsWrongState(t *testing.T) {
	d := newTestDaemon(t)
	id := registerJob(t, d)

	// Resuming a still-RUNNING job makes no sense.
	if resp := d.dispatch(ipc.Request{Action: ipc.ActionResume, JobID: id}); resp.OK {
		t.Fatal("expected Resume on a RUNNING job to be rejected")
	}

	if resp := d.dispatch(ipc.Request{Action: ipc.ActionPause, JobID: id}); !resp.OK {
		t.Fatalf("pause: %s", resp.Error)
	}

	resp := d.dispatch(ipc.Request{Action: ipc.ActionResume, JobID: id})
	if !resp.OK {
		t.Fatalf("resume: %s", resp.Error)
	}
	got, _ := d.store.Get(id)
	if got.Status != store.StatusRunning || got.PID != 4242 {
		t.Errorf("got status=%s pid=%d, want %s / 4242", got.Status, got.PID, store.StatusRunning)
	}
}

func TestResumeSurfacesRestoreFailure(t *testing.T) {
	d := newTestDaemon(t)
	d.resumeFunc = func(ctx context.Context, dir, hookResume, jobID string) (int, error) {
		return 0, errors.New("criu restore: image corrupt")
	}
	id := registerJob(t, d)
	if resp := d.dispatch(ipc.Request{Action: ipc.ActionPause, JobID: id}); !resp.OK {
		t.Fatalf("pause: %s", resp.Error)
	}

	resp := d.dispatch(ipc.Request{Action: ipc.ActionResume, JobID: id})
	if resp.OK {
		t.Fatal("expected Resume to surface the restore failure")
	}
	got, _ := d.store.Get(id)
	if got.Status != store.StatusRestoreFailed {
		t.Errorf("status = %s, want %s", got.Status, store.StatusRestoreFailed)
	}

	// RESTORE_FAILED -> retry is allowed.
	resp = d.dispatch(ipc.Request{Action: ipc.ActionResume, JobID: id})
	if resp.OK {
		t.Fatal("expected the retried resume to fail again with the same fake")
	}
}

func TestPruneSingleJob(t *testing.T) {
	d := newTestDaemon(t)

	activeID := registerJob(t, d)
	if resp := d.dispatch(ipc.Request{Action: ipc.ActionPrune, JobID: activeID}); resp.OK {
		t.Fatal("expected Prune on a RUNNING job to be rejected")
	}

	termID := registerJob(t, d)
	if resp := d.dispatch(ipc.Request{Action: ipc.ActionComplete, JobID: termID, ExitCode: 0}); !resp.OK {
		t.Fatalf("complete: %s", resp.Error)
	}
	job, _ := d.store.Get(termID)
	writeFile(t, filepath.Join(job.CheckpointDir, "inventory.img"))

	resp := d.dispatch(ipc.Request{Action: ipc.ActionPrune, JobID: termID})
	if !resp.OK {
		t.Fatalf("prune: %s", resp.Error)
	}
	if fileExists(job.CheckpointDir) {
		t.Errorf("CheckpointDir %s still exists after prune", job.CheckpointDir)
	}
	// The job itself must still be fully visible.
	if _, err := d.store.Get(termID); err != nil {
		t.Errorf("Get after prune: %v", err)
	}
}

func TestPruneAllLeavesActiveJobsAlone(t *testing.T) {
	d := newTestDaemon(t)

	var terminal, active []string
	for i := 0; i < 2; i++ {
		id := registerJob(t, d)
		d.dispatch(ipc.Request{Action: ipc.ActionComplete, JobID: id, ExitCode: 0})
		terminal = append(terminal, id)
	}
	for i := 0; i < 2; i++ {
		active = append(active, registerJob(t, d))
	}
	for _, id := range append(append([]string{}, terminal...), active...) {
		j, _ := d.store.Get(id)
		writeFile(t, filepath.Join(j.CheckpointDir, "inventory.img"))
	}

	resp := d.dispatch(ipc.Request{Action: ipc.ActionPrune})
	if !resp.OK {
		t.Fatalf("prune: %s", resp.Error)
	}
	if resp.Message != "pruned 2 job(s)" {
		t.Errorf("message = %q, want %q", resp.Message, "pruned 2 job(s)")
	}
	for _, id := range terminal {
		j, _ := d.store.Get(id)
		if fileExists(j.CheckpointDir) {
			t.Errorf("terminal job %s's checkpoint dir survived prune", id)
		}
	}
	for _, id := range active {
		j, _ := d.store.Get(id)
		if !fileExists(j.CheckpointDir) {
			t.Errorf("active job %s's checkpoint dir was pruned", id)
		}
	}

	// Idempotent: running again finds nothing left to remove, no error.
	resp = d.dispatch(ipc.Request{Action: ipc.ActionPrune})
	if !resp.OK {
		t.Fatalf("second prune: %s", resp.Error)
	}
}

func TestInterruptionFanOutChecksAllRunningJobsOnly(t *testing.T) {
	d := newTestDaemon(t)

	running := []string{registerJob(t, d), registerJob(t, d), registerJob(t, d)}
	untouched := registerJob(t, d)
	d.dispatch(ipc.Request{Action: ipc.ActionCancel, JobID: untouched})

	d.handleInterruption("test-trigger")

	if !d.interrupted.Load() {
		t.Error("expected interrupted latch to be set")
	}
	for _, id := range running {
		j, _ := d.store.Get(id)
		if j.Status != store.StatusCheckpointCreated {
			t.Errorf("job %s status = %s, want %s", id, j.Status, store.StatusCheckpointCreated)
		}
	}
	j, _ := d.store.Get(untouched)
	if j.Status != store.StatusCanceled {
		t.Errorf("already-canceled job was touched by fan-out: status = %s", j.Status)
	}

	// The latch also blocks further registrations, per Register's own rule.
	resp := d.dispatch(ipc.Request{Action: ipc.ActionRegister, Job: &ipc.RegisterJob{PID: 1, PGID: 1}})
	if resp.OK {
		t.Error("expected Register to be refused after the fan-out latched interrupted")
	}
}

func TestInterruptionFanOutBoundedByMaxConcurrent(t *testing.T) {
	d := newTestDaemon(t)
	d.maxConcurrentCheckpoints = 2

	var current, maxSeen int64
	d.checkpointFunc = func(ctx context.Context, dir string, j store.Job) error {
		n := atomic.AddInt64(&current, 1)
		for {
			old := atomic.LoadInt64(&maxSeen)
			if n <= old || atomic.CompareAndSwapInt64(&maxSeen, old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt64(&current, -1)
		return nil
	}

	for i := 0; i < 6; i++ {
		registerJob(t, d)
	}
	d.handleInterruption("test-trigger")

	if maxSeen > 2 {
		t.Errorf("max concurrent checkpoints observed = %d, want <= 2", maxSeen)
	}
	if maxSeen < 2 {
		t.Errorf("max concurrent checkpoints observed = %d, want == 2 (pool should saturate)", maxSeen)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("fake checkpoint image"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
