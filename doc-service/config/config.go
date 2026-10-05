package config

import (
	"log/slog"
	"os"
	"strconv"
)

type Config struct {
	ServerPort string

	// Names this process in /health and (month 3) in X-Doc-Instance, so
	// "which instance answered" is visible from outside.
	InstanceID string

	// Proves a request came through the gateway. doc-service never sees a
	// user token; it trusts X-User-ID only when this key accompanies it.
	GatewayKey string

	DBDSN string

	// Live-session limits (README, "Limits").
	DocMaxCodepoints     int
	OpRingSize           int
	SnapshotEveryOps     int
	SnapshotEverySeconds int
	SessionIdleSeconds   int
}

func LoadConfig() *Config {
	port := getEnv("SERVER_PORT", "9001")
	return &Config{
		ServerPort: port,
		InstanceID: getEnv("INSTANCE_ID", "doc-"+port),
		GatewayKey: requireEnv("GATEWAY_SHARED_KEY"),
		DBDSN:      getEnv("DB_DSN", "root:root@tcp(localhost:3308)/docs_service_db?parseTime=true"),

		DocMaxCodepoints:     positiveInt("DOC_MAX_CODEPOINTS", 1<<20),
		OpRingSize:           positiveInt("OP_RING_SIZE", 1000),
		SnapshotEveryOps:     positiveInt("SNAPSHOT_EVERY_OPS", 100),
		SnapshotEverySeconds: positiveInt("SNAPSHOT_EVERY_SECONDS", 30),
		SessionIdleSeconds:   positiveInt("SESSION_IDLE_SECONDS", 60),
	}
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
