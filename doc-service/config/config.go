package config

import (
	"log/slog"
	"os"
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
}

func LoadConfig() *Config {
	port := getEnv("SERVER_PORT", "9001")
	return &Config{
		ServerPort: port,
		InstanceID: getEnv("INSTANCE_ID", "doc-"+port),
		GatewayKey: requireEnv("GATEWAY_SHARED_KEY"),
		DBDSN:      getEnv("DB_DSN", "root:root@tcp(localhost:3308)/docs_service_db?parseTime=true"),
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

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
