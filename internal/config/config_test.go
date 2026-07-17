package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_MissingRequired(t *testing.T) {
	// Ensure required variables are absent.
	os.Unsetenv("MYSQL_DSN")
	os.Unsetenv("MYSQL_READER_DSN")
	os.Unsetenv("RABBITMQ_URL")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when required variables are missing, got nil")
	}
}

func TestLoad_AllPresent(t *testing.T) {
	os.Setenv("MYSQL_DSN", "user:pass@tcp(localhost:3306)/askdb_app?parseTime=true")
	os.Setenv("MYSQL_READER_DSN", "reader:pass@tcp(localhost:3306)/askdb_demo?parseTime=true")
	os.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	os.Setenv("API_PORT", "9090")
	t.Cleanup(func() {
		os.Unsetenv("MYSQL_DSN")
		os.Unsetenv("MYSQL_READER_DSN")
		os.Unsetenv("RABBITMQ_URL")
		os.Unsetenv("API_PORT")
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIPort != "9090" {
		t.Errorf("expected APIPort=9090, got %s", cfg.APIPort)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("expected default RedisAddr, got %s", cfg.RedisAddr)
	}
	if cfg.QueryTimeout != 5*time.Second {
		t.Errorf("expected default QueryTimeout=5s, got %s", cfg.QueryTimeout)
	}
	if cfg.QueryResultTTL != 15*time.Minute {
		t.Errorf("expected default QueryResultTTL=15m, got %s", cfg.QueryResultTTL)
	}
}

func TestLoad_QueryResultTTL_Custom(t *testing.T) {
	os.Setenv("MYSQL_DSN", "user:pass@tcp(localhost:3306)/db")
	os.Setenv("MYSQL_READER_DSN", "r:p@tcp(localhost:3306)/db")
	os.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	os.Setenv("QUERY_RESULT_TTL", "30m")
	t.Cleanup(func() {
		os.Unsetenv("MYSQL_DSN")
		os.Unsetenv("MYSQL_READER_DSN")
		os.Unsetenv("RABBITMQ_URL")
		os.Unsetenv("QUERY_RESULT_TTL")
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.QueryResultTTL != 30*time.Minute {
		t.Errorf("expected QueryResultTTL=30m, got %s", cfg.QueryResultTTL)
	}
}

func TestLoad_QueryResultTTL_InvalidValue_UsesDefault(t *testing.T) {
	os.Setenv("MYSQL_DSN", "user:pass@tcp(localhost:3306)/db")
	os.Setenv("MYSQL_READER_DSN", "r:p@tcp(localhost:3306)/db")
	os.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	os.Setenv("QUERY_RESULT_TTL", "not-a-duration")
	t.Cleanup(func() {
		os.Unsetenv("MYSQL_DSN")
		os.Unsetenv("MYSQL_READER_DSN")
		os.Unsetenv("RABBITMQ_URL")
		os.Unsetenv("QUERY_RESULT_TTL")
	})

	// getDurationEnv falls back to default on parse error → 15m → valid
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.QueryResultTTL != 15*time.Minute {
		t.Errorf("expected fallback 15m, got %s", cfg.QueryResultTTL)
	}
}

func TestLoad_QueryResultTTL_Zero_ReturnsError(t *testing.T) {
	os.Setenv("MYSQL_DSN", "user:pass@tcp(localhost:3306)/db")
	os.Setenv("MYSQL_READER_DSN", "r:p@tcp(localhost:3306)/db")
	os.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	os.Setenv("QUERY_RESULT_TTL", "0s")
	t.Cleanup(func() {
		os.Unsetenv("MYSQL_DSN")
		os.Unsetenv("MYSQL_READER_DSN")
		os.Unsetenv("RABBITMQ_URL")
		os.Unsetenv("QUERY_RESULT_TTL")
	})

	if _, err := Load(); err == nil {
		t.Fatal("expected error when QUERY_RESULT_TTL=0s, got nil")
	}
}

func TestLoad_MissingReaderDSN(t *testing.T) {
	os.Setenv("MYSQL_DSN", "user:pass@tcp(localhost:3306)/askdb_app")
	os.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	os.Unsetenv("MYSQL_READER_DSN")
	t.Cleanup(func() {
		os.Unsetenv("MYSQL_DSN")
		os.Unsetenv("RABBITMQ_URL")
	})

	if _, err := Load(); err == nil {
		t.Fatal("expected error when MYSQL_READER_DSN is missing, got nil")
	}
}

func TestGetDurationEnv(t *testing.T) {
	os.Setenv("TEST_DUR_XYZ", "2500ms")
	t.Cleanup(func() { os.Unsetenv("TEST_DUR_XYZ") })
	if got := getDurationEnv("TEST_DUR_XYZ", time.Second); got != 2500*time.Millisecond {
		t.Errorf("expected 2.5s, got %s", got)
	}

	os.Setenv("TEST_DUR_XYZ", "not-a-duration")
	if got := getDurationEnv("TEST_DUR_XYZ", time.Second); got != time.Second {
		t.Errorf("expected fallback on parse error, got %s", got)
	}

	os.Unsetenv("TEST_DUR_XYZ")
	if got := getDurationEnv("TEST_DUR_XYZ", 7*time.Second); got != 7*time.Second {
		t.Errorf("expected fallback when unset, got %s", got)
	}
}

func TestGetEnv_Default(t *testing.T) {
	os.Unsetenv("TEST_KEY_XYZ")
	if got := getEnv("TEST_KEY_XYZ", "fallback"); got != "fallback" {
		t.Errorf("expected fallback, got %s", got)
	}
}

func TestGetEnv_Override(t *testing.T) {
	os.Setenv("TEST_KEY_XYZ", "override")
	t.Cleanup(func() { os.Unsetenv("TEST_KEY_XYZ") })
	if got := getEnv("TEST_KEY_XYZ", "fallback"); got != "override" {
		t.Errorf("expected override, got %s", got)
	}
}
