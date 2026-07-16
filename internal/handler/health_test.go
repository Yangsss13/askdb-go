package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// --- stubs ---

type okPinger struct{}

func (o *okPinger) Ping(_ context.Context) error { return nil }

type failPinger struct{}

func (f *failPinger) Ping(_ context.Context) error { return errors.New("connection refused") }

type okRabbit struct{}

func (o *okRabbit) IsHealthy() bool { return true }

type failRabbit struct{}

func (f *failRabbit) IsHealthy() bool { return false }

// --- tests ---

func TestHealthz_AlwaysOK(t *testing.T) {
	r := gin.New()
	r.GET("/healthz", Healthz)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestReadyz_AllHealthy(t *testing.T) {
	deps := HealthDeps{
		MySQL:  &okPinger{},
		Redis:  &okPinger{},
		Rabbit: &okRabbit{},
	}
	r := gin.New()
	r.GET("/readyz", Readyz(deps))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/readyz", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestReadyz_MySQLDown(t *testing.T) {
	deps := HealthDeps{
		MySQL:  &failPinger{},
		Redis:  &okPinger{},
		Rabbit: &okRabbit{},
	}
	r := gin.New()
	r.GET("/readyz", Readyz(deps))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/readyz", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestReadyz_RabbitDown(t *testing.T) {
	deps := HealthDeps{
		MySQL:  &okPinger{},
		Redis:  &okPinger{},
		Rabbit: &failRabbit{},
	}
	r := gin.New()
	r.GET("/readyz", Readyz(deps))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/readyz", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}
