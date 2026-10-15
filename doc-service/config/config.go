package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ServerPort string

	// Names this process in /health and in X-Doc-Instance, so "which
	// instance answered" is visible from outside.
	InstanceID string

	// How long /health answers 503 after SIGTERM before sockets close. Must
	// be at least the gateway's LB_HEALTH_INTERVAL_SECONDS, or the balancer
	// is still sending new connections when the listener goes away.
	ShutdownDrainSeconds int

	// Proves a request came through the gateway. doc-service never sees a
	// user token; it trusts X-User-ID only when this key accompanies it.
	GatewayKey string

	DBDSN string

	// Live-session limits (README, "Limits").
	DocMaxCodepoints     int
	OpMaxBytes           int
	OpRingSize           int
	SnapshotEveryOps     int
	SnapshotEverySeconds int
	SessionIdleSeconds   int

	// Origins allowed to open a WebSocket. A missing Origin header is
	// allowed (native clients); a present one must be in this list.
	WSAllowedOrigins []string
}

func LoadConfig() *Config {
	port := getEnv("SERVER_PORT", "9001")
	instanceID := getEnv("INSTANCE_ID", "doc-"+port)
	if err := ValidateInstanceID(instanceID); err != nil {
		slog.Error("INSTANCE_ID is not usable", "value", instanceID, "error", err)
		os.Exit(1)
	}
	return &Config{
		ServerPort: port,
		InstanceID: instanceID,
		GatewayKey: requireEnv("GATEWAY_SHARED_KEY"),
		DBDSN:      getEnv("DB_DSN", "root:root@tcp(localhost:3308)/docs_service_db?parseTime=true"),

		DocMaxCodepoints:     positiveInt("DOC_MAX_CODEPOINTS", 1<<20),
		OpMaxBytes:           positiveInt("OP_MAX_BYTES", 65536),
		OpRingSize:           positiveInt("OP_RING_SIZE", 1000),
		SnapshotEveryOps:     positiveInt("SNAPSHOT_EVERY_OPS", 100),
		SnapshotEverySeconds: positiveInt("SNAPSHOT_EVERY_SECONDS", 30),
		SessionIdleSeconds:   positiveInt("SESSION_IDLE_SECONDS", 60),
		ShutdownDrainSeconds: positiveInt("SHUTDOWN_DRAIN_SECONDS", 5),

		WSAllowedOrigins: getEnvList("WS_ALLOWED_ORIGINS", []string{"http://localhost:9000"}),
	}
}

// ValidateInstanceID bounds the value to what a header and a log field can
// carry verbatim: 1-64 characters from [A-Za-z0-9._-]. A space, a colon or
// a CR/LF would otherwise reach X-Doc-Instance unchanged.
func ValidateInstanceID(id string) error {
	if id == "" {
		return errors.New("must not be empty")
	}
	if len(id) > 64 {
		return errors.New("must be at most 64 characters")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("character %q is not allowed (use letters, digits, '-', '_' or '.')", r)
		}
	}
	return nil
}

// requireEnv has no fallback on purpose: a predictable gateway key would let
// anyone who can reach this port claim any user id.
func requireEnv(key string) string {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		slog.Error("required environment variable is not set (no default - a predictable secret would allow forgery)", "name", key)
		os.Exit(1)
	}
	return value
}

// positiveInt fails at startup on a value that is set but unusable: a ring
// of size 0 or a snapshot interval of "30s" would otherwise surface as a
// panic or a silent default much later.
func positiveInt(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		slog.Error("environment variable must be a positive integer", "name", key, "value", raw)
		os.Exit(1)
	}
	return n
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func getEnvList(key string, fallback []string) []string {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}

	var values []string
	for part := range strings.SplitSeq(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	if len(values) == 0 {
		return fallback
	}

	return values
}
