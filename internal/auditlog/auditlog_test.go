package auditlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLogAppendsValidJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	entries := []Entry{
		{Component: "cli", Action: "pause", JobID: "job-1", Outcome: "ok"},
		{Component: "daemon", Action: "checkpoint_started", JobID: "job-1", Outcome: "ok"},
		{Component: "daemon", Action: "checkpoint_failed", JobID: "job-1", Outcome: "error", Detail: "disk full"},
	}
	for _, e := range entries {
		if err := l.Log(e); err != nil {
			t.Fatalf("Log: %v", err)
		}
	}

	lines := readLines(t, path)
	if len(lines) != len(entries) {
		t.Fatalf("got %d lines, want %d", len(lines), len(entries))
	}
	for i, line := range lines {
		var got Entry
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d: invalid JSON: %v", i, err)
		}
		if got.Component != entries[i].Component || got.Action != entries[i].Action ||
			got.JobID != entries[i].JobID || got.Outcome != entries[i].Outcome || got.Detail != entries[i].Detail {
			t.Errorf("line %d: got %+v, want %+v", i, got, entries[i])
		}
		if got.Time.IsZero() {
			t.Errorf("line %d: Time was not auto-filled", i)
		}
	}
}

func TestLogAutoFillsZeroTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	before := time.Now().UTC()
	if err := l.Log(Entry{Component: "cli", Action: "list", Outcome: "ok"}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	after := time.Now().UTC()

	lines := readLines(t, path)
	var got Entry
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.Time.Before(before.Add(-time.Second)) || got.Time.After(after.Add(time.Second)) {
		t.Errorf("auto-filled time %v not within [%v, %v]", got.Time, before, after)
	}
}

func TestConcurrentLoggersDontCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")

	const writers = 8
	const perWriter = 50

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// Each goroutine opens its own Logger against the same path,
			// simulating independent OS processes (daemon + separate cli
			// invocations) writing concurrently.
			l, err := Open(path)
			if err != nil {
				t.Errorf("writer %d: Open: %v", w, err)
				return
			}
			defer l.Close()
			for i := 0; i < perWriter; i++ {
				if err := l.Log(Entry{Component: "cli", Action: "list", Outcome: "ok"}); err != nil {
					t.Errorf("writer %d: Log: %v", w, err)
				}
			}
		}(w)
	}
	wg.Wait()

	lines := readLines(t, path)
	if len(lines) != writers*perWriter {
		t.Fatalf("got %d lines, want %d", len(lines), writers*perWriter)
	}
	for i, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d is not valid JSON (interleaved write?): %v\nline: %q", i, err, line)
		}
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return lines
}
