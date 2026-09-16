// Package config loads surviva.conf: a flat, slurm.conf-style key=value
// file, not YAML/JSON. See SPEC-config.md for the format and directive list.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the validated, typed result of loading surviva.conf.
type Config struct {
	CloudProvider     string
	PollInterval      time.Duration
	AuditLogPath      string
	CheckpointBaseDir string
	// MaxConcurrentCheckpoints bounds how many jobs are checkpointed at once
	// during an interruption fan-out. Optional; 0 means "let daemon default
	// it" (runtime.NumCPU() -- see SPEC-daemon.md).
	MaxConcurrentCheckpoints int

	// DBType selects the job-table backend: "sqlite" (default) or "mysql".
	// See SPEC-store.md for what each field below is used for.
	DBType     string
	DBPath     string // sqlite
	DBHost     string // mysql
	DBPort     int    // mysql
	DBUser     string // mysql
	DBPassword string // mysql, optional (empty allowed)
	DBName     string // mysql
}

// supportedCloudProviders are recognized directive values for CloudProvider.
// Only "aws" is implemented today; the others are reserved names so a
// forward-looking config doesn't fail with a generic parse error.
var supportedCloudProviders = map[string]bool{
	"aws": true,
}

var knownCloudProviderNames = map[string]bool{
	"aws":   true,
	"azure": true,
	"gcp":   true,
}

// DefaultPath returns /etc/surviva/surviva.conf unless overridden by
// SURVIVA_CONF.
func DefaultPath() string {
	if p := os.Getenv("SURVIVA_CONF"); p != "" {
		return p
	}
	return "/etc/surviva/surviva.conf"
}

// Load reads and validates surviva.conf at path.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	raw := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("%s: invalid line (expected Key=Value): %q", path, line)
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if _, known := knownKeys[key]; !known {
			return Config{}, fmt.Errorf("%s: unknown directive %q", path, key)
		}
		raw[key] = value
	}
	if err := scanner.Err(); err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	return validate(path, raw)
}

// knownKeys is the canonical (uppercase) directive name set. Keys are
// matched case-insensitively; this map is the single source of truth for
// "is this a real directive."
var knownKeys = map[string]bool{
	"CLOUDPROVIDER":            true,
	"POLLINTERVALSECONDS":      true,
	"AUDITLOGPATH":             true,
	"CHECKPOINTBASEDIR":        true,
	"DBPATH":                   true,
	"MAXCONCURRENTCHECKPOINTS": true,
	"DBTYPE":                   true,
	"DBHOST":                   true,
	"DBPORT":                   true,
	"DBUSER":                   true,
	"DBPASSWORD":               true,
	"DBNAME":                   true,
}

// supportedDBTypes are the recognized DBType directive values.
var supportedDBTypes = map[string]bool{
	"sqlite": true,
	"mysql":  true,
}

func validate(path string, raw map[string]string) (Config, error) {
	var cfg Config

	provider, ok := raw["CLOUDPROVIDER"]
	if !ok {
		return Config{}, fmt.Errorf("%s: missing required directive CloudProvider", path)
	}
	provider = strings.ToLower(provider)
	if !knownCloudProviderNames[provider] {
		return Config{}, fmt.Errorf("%s: CloudProvider %q is not a recognized cloud provider", path, provider)
	}
	if !supportedCloudProviders[provider] {
		return Config{}, fmt.Errorf("%s: CloudProvider %q is not yet implemented", path, provider)
	}
	cfg.CloudProvider = provider

	pollRaw, ok := raw["POLLINTERVALSECONDS"]
	if !ok {
		return Config{}, fmt.Errorf("%s: missing required directive PollIntervalSeconds", path)
	}
	pollSeconds, err := strconv.Atoi(pollRaw)
	if err != nil || pollSeconds <= 0 {
		return Config{}, fmt.Errorf("%s: PollIntervalSeconds must be a positive integer, got %q", path, pollRaw)
	}
	cfg.PollInterval = time.Duration(pollSeconds) * time.Second

	cfg.AuditLogPath, ok = raw["AUDITLOGPATH"]
	if !ok {
		return Config{}, fmt.Errorf("%s: missing required directive AuditLogPath", path)
	}

	cfg.CheckpointBaseDir, ok = raw["CHECKPOINTBASEDIR"]
	if !ok {
		return Config{}, fmt.Errorf("%s: missing required directive CheckpointBaseDir", path)
	}

	dbType := "sqlite"
	if v, ok := raw["DBTYPE"]; ok {
		dbType = strings.ToLower(v)
	}
	if !supportedDBTypes[dbType] {
		return Config{}, fmt.Errorf("%s: DBType %q is not supported (must be sqlite or mysql)", path, dbType)
	}
	cfg.DBType = dbType

	switch dbType {
	case "sqlite":
		cfg.DBPath, ok = raw["DBPATH"]
		if !ok {
			return Config{}, fmt.Errorf("%s: missing required directive DBPath (required when DBType=sqlite)", path)
		}
	case "mysql":
		cfg.DBHost, ok = raw["DBHOST"]
		if !ok {
			return Config{}, fmt.Errorf("%s: missing required directive DBHost (required when DBType=mysql)", path)
		}
		portRaw, ok := raw["DBPORT"]
		if !ok {
			return Config{}, fmt.Errorf("%s: missing required directive DBPort (required when DBType=mysql)", path)
		}
		port, err := strconv.Atoi(portRaw)
		if err != nil || port <= 0 {
			return Config{}, fmt.Errorf("%s: DBPort must be a positive integer, got %q", path, portRaw)
		}
		cfg.DBPort = port
		cfg.DBUser, ok = raw["DBUSER"]
		if !ok {
			return Config{}, fmt.Errorf("%s: missing required directive DBUser (required when DBType=mysql)", path)
		}
		cfg.DBPassword = raw["DBPASSWORD"] // optional -- empty allowed (e.g. passwordless local MySQL)
		cfg.DBName, ok = raw["DBNAME"]
		if !ok {
			return Config{}, fmt.Errorf("%s: missing required directive DBName (required when DBType=mysql)", path)
		}
	}

	if maxRaw, ok := raw["MAXCONCURRENTCHECKPOINTS"]; ok {
		n, err := strconv.Atoi(maxRaw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("%s: MaxConcurrentCheckpoints must be a positive integer, got %q", path, maxRaw)
		}
		cfg.MaxConcurrentCheckpoints = n
	}

	return cfg, nil
}
