package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// checkTimeout is the maximum time each dependency check is allowed to take.
const checkTimeout = 3 * time.Second

// depChecker is any type that can report its health.
type depChecker interface {
	Ping(ctx context.Context) error
}

// rabbitChecker matches infra.RabbitMQ without importing the infra package.
type rabbitChecker interface {
	IsHealthy() bool
}

// HealthDeps groups the dependencies checked by /readyz.
type HealthDeps struct {
	MySQL  depChecker
	Redis  depChecker
	Rabbit rabbitChecker
}

// Healthz handles GET /healthz.
// It only reports that the process is alive; it never checks external dependencies.
func Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Readyz handles GET /readyz.
// It checks each dependency with a timeout and returns 503 if any check fails.
// Error messages never include DSNs or passwords.
func Readyz(deps HealthDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), checkTimeout)
		defer cancel()

		result := gin.H{}
		allOK := true

		// MySQL check
		if err := deps.MySQL.Ping(ctx); err != nil {
			result["mysql"] = "unreachable"
			allOK = false
		} else {
			result["mysql"] = "ok"
		}

		// Redis check
		if err := deps.Redis.Ping(ctx); err != nil {
			result["redis"] = "unreachable"
			allOK = false
		} else {
			result["redis"] = "ok"
		}

		// RabbitMQ check (channel-level; no network call needed)
		if !deps.Rabbit.IsHealthy() {
			result["rabbitmq"] = "unreachable"
			allOK = false
		} else {
			result["rabbitmq"] = "ok"
		}

		status := "ok"
		code := http.StatusOK
		if !allOK {
			status = "degraded"
			code = http.StatusServiceUnavailable
		}
		result["status"] = status
		c.JSON(code, result)
	}
}
