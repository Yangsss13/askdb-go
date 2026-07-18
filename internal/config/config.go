package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration parsed from environment variables.
// Fields are validated at startup; missing required values cause Load to return an error.
type Config struct {
	// API
	APIPort string

	// MySQL (askdb_app) — DSN must not be logged
	MySQLDSN string

	// MySQL reader (askdb_demo, askdb_reader user) — DSN must not be logged.
	// Used by the read-only QueryExecutor via database/sql, isolated from GORM.
	MySQLReaderDSN string

	// QueryTimeout bounds each demo-database read query.
	QueryTimeout time.Duration

	// Redis
	RedisAddr string
	RedisPass string

	// QueryResultTTL is the Redis TTL for cached query results.
	// Must be greater than zero. Configured via QUERY_RESULT_TTL (e.g. "15m").
	QueryResultTTL time.Duration

	// MaxQueryRows is the maximum number of result rows a query may return.
	// The SQL Guard enforces this as the outermost LIMIT, and QueryExecutor
	// enforces it again as a second layer of defense. Must be greater than zero.
	// Configured via MAX_QUERY_ROWS (default 100).
	MaxQueryRows int

	// MaxResultBytes bounds the JSON payload size of a cached result.
	// A result whose serialized size exceeds this is rejected (RESULT_TOO_LARGE).
	// Must be greater than zero. Configured via MAX_RESULT_BYTES (default 1 MiB).
	MaxResultBytes int64

	// RabbitMQ — URL must not be logged
	RabbitMQURL string

	// JWTSecret is the HS256 signing key. Configured via JWT_SECRET.
	// Required only by the API (see ValidateJWT); the Worker never uses it.
	JWTSecret []byte

	// JWTIssuer is the "iss" claim value. Configured via JWT_ISSUER (default "askdb-api").
	JWTIssuer string

	// JWTAccessTTL is the token validity window. Configured via JWT_ACCESS_TTL (default 24h).
	JWTAccessTTL time.Duration

	// Phase 6B: data-source management.

	// DataSourceKey is the 32-byte AES-256 master key for password encryption.
	// Stored as base64 in DATA_SOURCE_KEY. Required by both API and Worker.
	// Must not be logged under any circumstances.
	DataSourceKey []byte

	// AllowedDBPorts is the comma-separated port whitelist for outbound DB connections.
	// Configured via ALLOWED_DB_PORTS (default "3306").
	AllowedDBPorts string

	// PrivateHostAllowlist is a comma-separated CIDR list of private address ranges
	// that are explicitly permitted despite the default block list.
	// Configured via PRIVATE_HOST_ALLOWLIST (default ""). Docker dev: "172.17.0.0/16".
	PrivateHostAllowlist string

	// DataSourceConnectTimeout is the default TCP connect timeout for data-source
	// connections when no per-source override is set. Configured via
	// DATA_SOURCE_CONNECT_TIMEOUT (default 5s).
	DataSourceConnectTimeout time.Duration
}

// Load reads the .env file (if present) as a fallback, then reads environment
// variables. Real environment variables always override .env values.
// Returns an error if any required variable is missing.
//
// Load validates only the configuration shared by both processes. JWT settings
// are validated separately by ValidateJWT, which the API calls but the Worker
// does not — the Worker never touches JWT_SECRET.
func Load() (*Config, error) {
	// godotenv.Load silently ignores a missing .env file, which is intentional:
	// production environments supply variables through the process environment.
	_ = godotenv.Load()

	cfg := &Config{
		APIPort:        getEnv("API_PORT", "8080"),
		MySQLDSN:       os.Getenv("MYSQL_DSN"),
		MySQLReaderDSN: os.Getenv("MYSQL_READER_DSN"),
		QueryTimeout:   getDurationEnv("QUERY_TIMEOUT", 5*time.Second),
		RedisAddr:      getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPass:      os.Getenv("REDIS_PASS"),
		QueryResultTTL: getDurationEnv("QUERY_RESULT_TTL", 15*time.Minute),
		MaxQueryRows:   getIntEnv("MAX_QUERY_ROWS", 100),
		MaxResultBytes: getInt64Env("MAX_RESULT_BYTES", 1048576),
		RabbitMQURL:    os.Getenv("RABBITMQ_URL"),
		JWTSecret:      []byte(os.Getenv("JWT_SECRET")),
		JWTIssuer:      getEnv("JWT_ISSUER", "askdb-api"),
		JWTAccessTTL:   getDurationEnv("JWT_ACCESS_TTL", 24*time.Hour),

		AllowedDBPorts:           getEnv("ALLOWED_DB_PORTS", "3306"),
		PrivateHostAllowlist:     os.Getenv("PRIVATE_HOST_ALLOWLIST"),
		DataSourceConnectTimeout: getDurationEnv("DATA_SOURCE_CONNECT_TIMEOUT", 5*time.Second),
	}

	// DATA_SOURCE_KEY is base64-encoded; decode and validate length here so
	// both API and Worker fail fast with a clear message, without logging the key.
	if raw := os.Getenv("DATA_SOURCE_KEY"); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("config: DATA_SOURCE_KEY is not valid base64: %w", err)
		}
		cfg.DataSourceKey = key
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate returns an error listing every missing required variable.
func (c *Config) validate() error {
	missing := []string{}
	if c.MySQLDSN == "" {
		missing = append(missing, "MYSQL_DSN")
	}
	if c.MySQLReaderDSN == "" {
		missing = append(missing, "MYSQL_READER_DSN")
	}
	if c.RabbitMQURL == "" {
		missing = append(missing, "RABBITMQ_URL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: missing required environment variables: %v", missing)
	}
	if c.QueryResultTTL <= 0 {
		return fmt.Errorf("config: QUERY_RESULT_TTL must be greater than zero, got %s", c.QueryResultTTL)
	}
	if c.MaxQueryRows <= 0 {
		return fmt.Errorf("config: MAX_QUERY_ROWS must be greater than zero, got %d", c.MaxQueryRows)
	}
	if c.MaxResultBytes <= 0 {
		return fmt.Errorf("config: MAX_RESULT_BYTES must be greater than zero, got %d", c.MaxResultBytes)
	}
	return nil
}

// ValidateJWT checks the JWT configuration. It is API-only: the worker never
// signs or verifies tokens and must start without JWT_SECRET set. The API calls
// this after Load and exits on error.
func (c *Config) ValidateJWT() error {
	if len(c.JWTSecret) == 0 {
		return fmt.Errorf("config: missing required environment variable: JWT_SECRET")
	}
	if len(c.JWTSecret) < 32 {
		return fmt.Errorf("config: JWT_SECRET must be at least 32 bytes, got %d", len(c.JWTSecret))
	}
	return nil
}

// ValidateDataSourceKey checks that DATA_SOURCE_KEY is present and exactly
// 32 bytes after base64 decoding. Both the API and the Worker call this —
// neither process should start if the key is absent or malformed.
// The key value is never included in the returned error.
func (c *Config) ValidateDataSourceKey() error {
	if len(c.DataSourceKey) == 0 {
		return fmt.Errorf("config: missing required environment variable: DATA_SOURCE_KEY")
	}
	if len(c.DataSourceKey) != 32 {
		return fmt.Errorf("config: DATA_SOURCE_KEY must decode to exactly 32 bytes, got %d", len(c.DataSourceKey))
	}
	return nil
}

// getEnv returns the environment variable value or a default if it is unset/empty.
func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// getDurationEnv parses a Go duration string (e.g. "5s") from the environment,
// falling back to defaultVal when unset, empty, or unparseable.
func getDurationEnv(key string, defaultVal time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return defaultVal
	}
	return d
}

// getIntEnv parses an int from the environment, falling back to defaultVal when
// unset, empty, or unparseable. Range validation is left to Config.validate.
func getIntEnv(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

// getInt64Env parses an int64 from the environment, falling back to defaultVal
// when unset, empty, or unparseable. Range validation is left to Config.validate.
func getInt64Env(key string, defaultVal int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return defaultVal
	}
	return n
}
