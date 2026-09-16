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
		DBType:            "sqlite",
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

func TestLoadDBTypeDefaultsToSQLite(t *testing.T) {
	cfg, err := Load(writeConf(t, validConf))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBType != "sqlite" {
		t.Errorf("DBType = %q, want %q", cfg.DBType, "sqlite")
	}
}

func TestLoadMySQL(t *testing.T) {
	base := `
CloudProvider=aws
PollIntervalSeconds=5
AuditLogPath=/x
CheckpointBaseDir=/y
DBType=mysql
DBHost=db.example.com
DBPort=3306
DBUser=surviva
DBName=surviva_jobs
`
	cfg, err := Load(writeConf(t, base))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBType != "mysql" || cfg.DBHost != "db.example.com" || cfg.DBPort != 3306 ||
		cfg.DBUser != "surviva" || cfg.DBName != "surviva_jobs" || cfg.DBPassword != "" {
		t.Fatalf("got %+v", cfg)
	}
	if cfg.DBPath != "" {
		t.Errorf("DBPath = %q, want empty when DBType=mysql", cfg.DBPath)
	}
}

func TestLoadMySQLMissingRequiredField(t *testing.T) {
	cases := map[string]string{
		"DBHost": "CloudProvider=aws\nPollIntervalSeconds=5\nAuditLogPath=/x\nCheckpointBaseDir=/y\nDBType=mysql\nDBPort=3306\nDBUser=u\nDBName=d\n",
		"DBPort": "CloudProvider=aws\nPollIntervalSeconds=5\nAuditLogPath=/x\nCheckpointBaseDir=/y\nDBType=mysql\nDBHost=h\nDBUser=u\nDBName=d\n",
		"DBUser": "CloudProvider=aws\nPollIntervalSeconds=5\nAuditLogPath=/x\nCheckpointBaseDir=/y\nDBType=mysql\nDBHost=h\nDBPort=3306\nDBName=d\n",
		"DBName": "CloudProvider=aws\nPollIntervalSeconds=5\nAuditLogPath=/x\nCheckpointBaseDir=/y\nDBType=mysql\nDBHost=h\nDBPort=3306\nDBUser=u\n",
	}
	for missing, content := range cases {
		if _, err := Load(writeConf(t, content)); err == nil {
			t.Errorf("missing %s: expected error, got none", missing)
		}
	}
}

func TestLoadMySQLPasswordOptional(t *testing.T) {
	content := "CloudProvider=aws\nPollIntervalSeconds=5\nAuditLogPath=/x\nCheckpointBaseDir=/y\nDBType=mysql\nDBHost=h\nDBPort=3306\nDBUser=u\nDBName=d\n"
	cfg, err := Load(writeConf(t, content))
	if err != nil {
		t.Fatalf("Load without DBPassword should succeed: %v", err)
	}
	if cfg.DBPassword != "" {
		t.Errorf("DBPassword = %q, want empty", cfg.DBPassword)
	}
}

func TestLoadInvalidDBType(t *testing.T) {
	path := writeConf(t, validConf+"\nDBType=postgres\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unsupported DBType")
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
