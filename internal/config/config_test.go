package config

import (
	"os"
	"testing"
)

func TestLoad_MissingRequired(t *testing.T) {
	// Ensure required variables are absent.
	os.Unsetenv("MYSQL_DSN")
	os.Unsetenv("RABBITMQ_URL")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when required variables are missing, got nil")
	}
}

func TestLoad_AllPresent(t *testing.T) {
	os.Setenv("MYSQL_DSN", "user:pass@tcp(localhost:3306)/askdb_app?parseTime=true")
	os.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	os.Setenv("API_PORT", "9090")
	t.Cleanup(func() {
		os.Unsetenv("MYSQL_DSN")
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
