package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration parsed from environment variables.
// Fields are validated at startup; missing required values cause Load to return an error.
type Config struct {
	// API
	APIPort string

	// MySQL (askdb_app) — DSN must not be logged
	MySQLDSN string

	// Redis
	RedisAddr string
	RedisPass string

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
		APIPort:     getEnv("API_PORT", "8080"),
		MySQLDSN:    os.Getenv("MYSQL_DSN"),
		RedisAddr:   getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPass:   os.Getenv("REDIS_PASS"),
		RabbitMQURL: os.Getenv("RABBITMQ_URL"),
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
	if c.RabbitMQURL == "" {
		missing = append(missing, "RABBITMQ_URL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: missing required environment variables: %v", missing)
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
