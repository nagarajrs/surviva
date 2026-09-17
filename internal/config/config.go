// Package config loads surviva.conf: a flat, slurm.conf-style key=value
// file, not YAML/JSON. See docs/specs/config.md for the format and directive list.
package config

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
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
	// it" (runtime.NumCPU() -- see docs/specs/daemon.md).
	MaxConcurrentCheckpoints int

	// DBType selects the job-table backend: "sqlite" (default) or "mysql".
	// See docs/specs/store.md for what each field below is used for.
	DBType     string
	DBPath     string // sqlite
	DBHost     string // mysql
	DBPort     int    // mysql
	DBUser     string // mysql
	DBPassword string // mysql, optional (empty allowed)
	DBName     string // mysql

	// NotifyTargetType selects an optional AWS notification target invoked
	// the moment daemon detects a Spot interruption/rebalance signal:
	// "lambda" or "stepfunction". Empty (the default) disables it entirely.
	// See docs/specs/daemon.md for the payload schema.
	NotifyTargetType string
	NotifyTargetARN  string

	// SocketGroup optionally names a group that should be able to connect
	// to the daemon's Unix socket. Empty (the default) leaves the socket at
	// whatever permissions net.Listen gives it -- root-only in practice,
	// since daemon itself runs as root. Set this when non-root callers
	// (e.g. a Slurm job step running as the submitting user) need direct
	// access instead of going through sudo. See docs/specs/daemon.md.
	SocketGroup string
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

	info, err := f.Stat()
	if err != nil {
		return Config{}, fmt.Errorf("stat %s: %w", path, err)
	}

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

	cfg, err := validate(path, raw)
	if err != nil {
		return Config{}, err
	}
	warnIfWorldReadable(path, info, cfg)
	return cfg, nil
}

// warnIfWorldReadable flags a surviva.conf that's readable by group or other
// when it actually holds a secret (DBPassword) -- non-fatal, since plenty of
// deployments manage this some other way (SELinux, ACLs), and failing an
// otherwise-valid config over file permissions would be a breaking change
// for anyone upgrading. Skipped on Windows, where the permission bits this
// checks don't carry the same meaning.
func warnIfWorldReadable(path string, info os.FileInfo, cfg Config) {
	if runtime.GOOS == "windows" || cfg.DBPassword == "" {
		return
	}
	if info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "surviva: warning: %s is readable by group/other (mode %04o) and contains a DBPassword -- recommend chmod 600 %s\n", path, info.Mode().Perm(), path)
	}
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
	"NOTIFYTARGETTYPE":         true,
	"NOTIFYTARGETARN":          true,
	"SOCKETGROUP":              true,
}

// supportedDBTypes are the recognized DBType directive values.
var supportedDBTypes = map[string]bool{
	"sqlite": true,
	"mysql":  true,
}

// supportedNotifyTargetTypes are the recognized NotifyTargetType values.
var supportedNotifyTargetTypes = map[string]bool{
	"lambda":       true,
	"stepfunction": true,
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

	notifyType, hasNotifyType := raw["NOTIFYTARGETTYPE"]
	notifyARN, hasNotifyARN := raw["NOTIFYTARGETARN"]
	switch {
	case !hasNotifyType && !hasNotifyARN:
		// Notification disabled -- the common case, and every surviva.conf
		// written before this directive existed.
	case hasNotifyType && !hasNotifyARN:
		return Config{}, fmt.Errorf("%s: missing required directive NotifyTargetARN (required when NotifyTargetType is set)", path)
	case !hasNotifyType && hasNotifyARN:
		return Config{}, fmt.Errorf("%s: NotifyTargetARN given without NotifyTargetType", path)
	default:
		notifyType = strings.ToLower(notifyType)
		if !supportedNotifyTargetTypes[notifyType] {
			return Config{}, fmt.Errorf("%s: NotifyTargetType %q is not supported (must be lambda or stepfunction)", path, notifyType)
		}
		cfg.NotifyTargetType = notifyType
		cfg.NotifyTargetARN = notifyARN
	}

	if maxRaw, ok := raw["MAXCONCURRENTCHECKPOINTS"]; ok {
		n, err := strconv.Atoi(maxRaw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("%s: MaxConcurrentCheckpoints must be a positive integer, got %q", path, maxRaw)
		}
		cfg.MaxConcurrentCheckpoints = n
	}

	// Not validated here (e.g. that the group actually exists) -- every CLI
	// command loads config too (see cmd/surviva.openAuditLogger), and this
	// directive is only meaningful to `surviva daemon`. Resolving it there,
	// not here, means a typo'd SocketGroup only ever breaks the daemon
	// startup it actually affects, not every unrelated CLI invocation.
	cfg.SocketGroup = raw["SOCKETGROUP"]

	return cfg, nil
}
