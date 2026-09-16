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
	DBPath            string
	// MaxConcurrentCheckpoints bounds how many jobs are checkpointed at once
	// during an interruption fan-out. Optional; 0 means "let daemon default
	// it" (runtime.NumCPU() -- see SPEC-daemon.md).
	MaxConcurrentCheckpoints int
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

	cfg.DBPath, ok = raw["DBPATH"]
	if !ok {
		return Config{}, fmt.Errorf("%s: missing required directive DBPath", path)
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
