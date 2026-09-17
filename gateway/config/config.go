package config

import (
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	ServerPort string

	// Signs and validates user tokens. The gateway is the ONLY JWT validator in
	// the system: backends receive identity headers, never the credential.
	JWTSecret string

	// Proves to doc-service (and, later, notification- and publish-service)
	// that a request came through here; they trust X-User-ID on that basis.
	GatewayKey string

	DBDSN string

	// Comma-separated. One entry in months 1-2; two or more from month 3, where
	// the gateway load-balances across them.
	DocServiceURLs []string

	StaticDir string
}

func LoadConfig() *Config {
	return &Config{
		ServerPort:     getEnv("SERVER_PORT", "9000"),
		JWTSecret:      requireEnv("JWT_SECRET"),
		GatewayKey:     requireEnv("GATEWAY_SHARED_KEY"),
		DBDSN:          getEnv("DB_DSN", "root:root@tcp(localhost:3308)/docs_gateway_db?parseTime=true"),
		DocServiceURLs: getEnvList("DOC_SERVICE_URLS", []string{"http://localhost:9001"}),
		StaticDir:      getEnv("STATIC_DIR", "../static"),
	}
}

// requireEnv has no fallback on purpose: a predictable default signing key
// would let anyone forge valid tokens for any user_id/role, and a predictable
// shared key would let anyone at :9001 claim any user id.
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
