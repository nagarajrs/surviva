package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const validConf = `
# surviva.conf
CloudProvider=aws
PollIntervalSeconds=5
AuditLogPath=/var/log/surviva/audit.log
CheckpointBaseDir=/var/lib/surviva/checkpoints
DBPath=/var/lib/surviva/jobs.db
`

func writeConf(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "surviva.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	path := writeConf(t, validConf)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		CloudProvider:     "aws",
		PollInterval:      5 * time.Second,
		AuditLogPath:      "/var/log/surviva/audit.log",
		CheckpointBaseDir: "/var/lib/surviva/checkpoints",
		DBPath:            "/var/lib/surviva/jobs.db",
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadCaseInsensitiveKeys(t *testing.T) {
	path := writeConf(t, `
cloudprovider=aws
POLLINTERVALSECONDS=5
AuditLogPath=/var/log/surviva/audit.log
checkpointbasedir=/var/lib/surviva/checkpoints
DbPath=/var/lib/surviva/jobs.db
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CloudProvider != "aws" || cfg.PollInterval != 5*time.Second {
		t.Fatalf("case-insensitive key parse failed: %+v", cfg)
	}
}

func TestLoadMissingRequiredKey(t *testing.T) {
	cases := map[string]string{
		"CloudProvider":       "PollIntervalSeconds=5\nAuditLogPath=/x\nCheckpointBaseDir=/y\nDBPath=/z\n",
		"PollIntervalSeconds": "CloudProvider=aws\nAuditLogPath=/x\nCheckpointBaseDir=/y\nDBPath=/z\n",
		"AuditLogPath":        "CloudProvider=aws\nPollIntervalSeconds=5\nCheckpointBaseDir=/y\nDBPath=/z\n",
		"CheckpointBaseDir":   "CloudProvider=aws\nPollIntervalSeconds=5\nAuditLogPath=/x\nDBPath=/z\n",
		"DBPath":              "CloudProvider=aws\nPollIntervalSeconds=5\nAuditLogPath=/x\nCheckpointBaseDir=/y\n",
	}
	for missing, content := range cases {
		path := writeConf(t, content)
		_, err := Load(path)
		if err == nil {
			t.Errorf("missing %s: expected error, got none", missing)
		}
	}
}

func TestLoadUnknownKey(t *testing.T) {
	path := writeConf(t, validConf+"\nFooBar=baz\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestLoadUnimplementedProvider(t *testing.T) {
	path := writeConf(t, `
CloudProvider=azure
PollIntervalSeconds=5
AuditLogPath=/x
CheckpointBaseDir=/y
DBPath=/z
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unimplemented provider")
	}
}

func TestLoadOptionalMaxConcurrentCheckpoints(t *testing.T) {
	// Absent: defaults to zero (daemon decides its own default).
	cfg, err := Load(writeConf(t, validConf))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxConcurrentCheckpoints != 0 {
		t.Errorf("MaxConcurrentCheckpoints = %d, want 0 (unset)", cfg.MaxConcurrentCheckpoints)
	}

	// Present: parsed and validated like any other int directive.
	cfg, err = Load(writeConf(t, validConf+"\nMaxConcurrentCheckpoints=4\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxConcurrentCheckpoints != 4 {
		t.Errorf("MaxConcurrentCheckpoints = %d, want 4", cfg.MaxConcurrentCheckpoints)
	}

	// Invalid value rejected like any other required int would be.
	if _, err := Load(writeConf(t, validConf+"\nMaxConcurrentCheckpoints=0\n")); err == nil {
		t.Error("expected error for MaxConcurrentCheckpoints=0")
	}
}

func TestLoadUnrecognizedProvider(t *testing.T) {
	path := writeConf(t, `
CloudProvider=digitalocean
PollIntervalSeconds=5
AuditLogPath=/x
CheckpointBaseDir=/y
DBPath=/z
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unrecognized provider")
	}
}
