package config

import (
	"encoding/base64"
	"fmt"
	"math"
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

	// Phase 7: retry / DLQ reliability configuration.

	// MQConfirmTimeout is the maximum time to wait for a Publisher Confirm ACK.
	// Configured via MQ_CONFIRM_TIMEOUT (default 5s).
	MQConfirmTimeout time.Duration

	// RetryMaxAttempts is the maximum number of retries (not counting the initial
	// execution). 0 disables retries. Configured via RETRY_MAX_ATTEMPTS (default 3).
	RetryMaxAttempts int

	// RetryDelay is the fixed TTL applied to messages on the retry queue.
	// Configured via RETRY_DELAY (default 30s).
	RetryDelay time.Duration

	// Phase 8: Transactional Outbox Dispatcher configuration.

	// OutboxPollInterval is how often the Dispatcher wakes to claim and publish
	// outbox events. Configured via OUTBOX_POLL_INTERVAL (default 2s).
	OutboxPollInterval time.Duration

	// OutboxBatchSize is the maximum number of outbox events claimed per cycle.
	// Must be >= 1. Configured via OUTBOX_BATCH_SIZE (default 10).
	OutboxBatchSize int

	// OutboxLeaseTTL is how long a claimed event is held before another instance
	// may take it over. Must be > OutboxPollInterval.
	// Configured via OUTBOX_LEASE_TTL (default 30s).
	OutboxLeaseTTL time.Duration

	// OutboxBaseBackoff is the initial exponential-backoff delay on publish failure.
	// Configured via OUTBOX_BASE_BACKOFF (default 5s).
	OutboxBaseBackoff time.Duration

	// OutboxMaxBackoff caps the exponential-backoff delay.
	// Configured via OUTBOX_MAX_BACKOFF (default 10m).
	OutboxMaxBackoff time.Duration

	// OutboxPublishedRetain is how long published events are kept before cleanup.
	// Configured via OUTBOX_PUBLISHED_RETAIN (default 24h).
	OutboxPublishedRetain time.Duration

	// OutboxCleanBatch is the maximum number of published events deleted per cycle.
	// Must be >= 1. Configured via OUTBOX_CLEAN_BATCH (default 100).
	OutboxCleanBatch int

	// Phase 9: LLM configuration.

	// LLMProvider selects the SQL-generation backend. "fake" uses the deterministic
	// stub; "openai-compatible" calls a real Chat Completions endpoint.
	// Configured via LLM_PROVIDER (default "fake").
	LLMProvider string

	// LLMBaseURL is the API root for the openai-compatible provider,
	// e.g. "https://api.openai.com/v1". Required when LLMProvider=openai-compatible.
	// Must not be logged under any circumstances.
	LLMBaseURL string

	// LLMAPIKey is the Authorization Bearer token. Required by the Worker when
	// LLMProvider=openai-compatible. Never used by the API process.
	// Must not be logged under any circumstances.
	LLMAPIKey string

	// LLMModel is the model identifier sent in each request.
	// Configured via LLM_MODEL (default "gpt-4o-mini").
	LLMModel string

	// LLMTimeout bounds the total HTTP round-trip to the LLM endpoint.
	// Configured via LLM_TIMEOUT (default 60s).
	LLMTimeout time.Duration

	// LLMTemperature is the sampling temperature (0–2).
	// Configured via LLM_TEMPERATURE (default 0.0).
	LLMTemperature float64

	// LLMMaxTokens caps the response token count.
	// Configured via LLM_MAX_TOKENS (default 512).
	LLMMaxTokens int

	// LLMMaxRespBytes is the response body size limit in bytes.
	// Configured via LLM_MAX_RESP_BYTES (default 524288 = 512 KiB).
	LLMMaxRespBytes int64

	// LLMMaxSchemaBytes caps the serialized schema size in bytes passed in the prompt.
	// Configured via LLM_MAX_SCHEMA_BYTES (default 16384 = 16 KiB).
	LLMMaxSchemaBytes int

	// LLMAllowLocalHTTP permits plain HTTP when every resolved IP is a loopback
	// address. Must be false in production.
	// Configured via LLM_ALLOW_LOCAL_HTTP (default false).
	LLMAllowLocalHTTP bool
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
		MQConfirmTimeout:         getDurationEnv("MQ_CONFIRM_TIMEOUT", 5*time.Second),
		RetryMaxAttempts:         getIntEnv("RETRY_MAX_ATTEMPTS", 3),
		RetryDelay:               getDurationEnv("RETRY_DELAY", 30*time.Second),

		OutboxPollInterval:    getDurationEnv("OUTBOX_POLL_INTERVAL", 2*time.Second),
		OutboxBatchSize:       getIntEnv("OUTBOX_BATCH_SIZE", 10),
		OutboxLeaseTTL:        getDurationEnv("OUTBOX_LEASE_TTL", 30*time.Second),
		OutboxBaseBackoff:     getDurationEnv("OUTBOX_BASE_BACKOFF", 5*time.Second),
		OutboxMaxBackoff:      getDurationEnv("OUTBOX_MAX_BACKOFF", 10*time.Minute),
		OutboxPublishedRetain: getDurationEnv("OUTBOX_PUBLISHED_RETAIN", 24*time.Hour),
		OutboxCleanBatch:      getIntEnv("OUTBOX_CLEAN_BATCH", 100),

		LLMProvider:       getEnv("LLM_PROVIDER", "fake"),
		LLMBaseURL:        os.Getenv("LLM_BASE_URL"),
		LLMAPIKey:         os.Getenv("LLM_API_KEY"),
		LLMModel:          getEnv("LLM_MODEL", "gpt-4o-mini"),
		LLMTimeout:        getDurationEnv("LLM_TIMEOUT", 60*time.Second),
		LLMTemperature:    getFloat64Env("LLM_TEMPERATURE", 0.0),
		LLMMaxTokens:      getIntEnv("LLM_MAX_TOKENS", 512),
		LLMMaxRespBytes:   getInt64Env("LLM_MAX_RESP_BYTES", 524288),
		LLMMaxSchemaBytes: getIntEnv("LLM_MAX_SCHEMA_BYTES", 16384),
		LLMAllowLocalHTTP: getBoolEnv("LLM_ALLOW_LOCAL_HTTP", false),
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
	if c.OutboxPollInterval <= 0 {
		return fmt.Errorf("config: OUTBOX_POLL_INTERVAL must be greater than zero, got %s", c.OutboxPollInterval)
	}
	if c.OutboxBatchSize <= 0 {
		return fmt.Errorf("config: OUTBOX_BATCH_SIZE must be greater than zero, got %d", c.OutboxBatchSize)
	}
	if c.OutboxLeaseTTL <= 0 {
		return fmt.Errorf("config: OUTBOX_LEASE_TTL must be greater than zero, got %s", c.OutboxLeaseTTL)
	}
	if c.OutboxBaseBackoff <= 0 {
		return fmt.Errorf("config: OUTBOX_BASE_BACKOFF must be greater than zero, got %s", c.OutboxBaseBackoff)
	}
	if c.OutboxMaxBackoff <= 0 {
		return fmt.Errorf("config: OUTBOX_MAX_BACKOFF must be greater than zero, got %s", c.OutboxMaxBackoff)
	}
	if c.OutboxPublishedRetain <= 0 {
		return fmt.Errorf("config: OUTBOX_PUBLISHED_RETAIN must be greater than zero, got %s", c.OutboxPublishedRetain)
	}
	if c.OutboxCleanBatch <= 0 {
		return fmt.Errorf("config: OUTBOX_CLEAN_BATCH must be greater than zero, got %d", c.OutboxCleanBatch)
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

// getFloat64Env parses a float64 from the environment, falling back to defaultVal
// when unset, empty, or unparseable.
func getFloat64Env(key string, defaultVal float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return defaultVal
	}
	return f
}

// getBoolEnv parses a bool from the environment ("true"/"1" → true),
// falling back to defaultVal when unset, empty, or unparseable.
func getBoolEnv(key string, defaultVal bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultVal
	}
	return b
}

// ValidateLLM checks the LLM configuration. It is Worker-only when
// provider=openai-compatible; the API process and fake mode never need
// LLM_BASE_URL or LLM_API_KEY.
func (c *Config) ValidateLLM() error {
	switch c.LLMProvider {
	case "fake":
		return nil
	case "openai-compatible":
		// Continue with the real-provider-only checks below.
	default:
		return fmt.Errorf("config: unsupported LLM_PROVIDER")
	}
	if c.LLMBaseURL == "" {
		return fmt.Errorf("config: LLM_BASE_URL is required when LLM_PROVIDER=openai-compatible")
	}
	if c.LLMAPIKey == "" {
		return fmt.Errorf("config: LLM_API_KEY is required when LLM_PROVIDER=openai-compatible")
	}
	if c.LLMTimeout <= 0 || c.LLMMaxTokens <= 0 || c.LLMMaxRespBytes <= 0 || c.LLMMaxSchemaBytes <= 0 {
		return fmt.Errorf("config: LLM limits must be positive")
	}
	if math.IsNaN(c.LLMTemperature) || math.IsInf(c.LLMTemperature, 0) || c.LLMTemperature < 0 || c.LLMTemperature > 2 {
		return fmt.Errorf("config: LLM_TEMPERATURE must be between 0 and 2")
	}
	return nil
}
