package config

import (
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
}

// Load reads the .env file (if present) as a fallback, then reads environment
// variables. Real environment variables always override .env values.
// Returns an error if any required variable is missing.
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
