package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Yangsss13/askdb-go/internal/config"
	"github.com/Yangsss13/askdb-go/internal/infra"
)

func main() {
	// Structured JSON logging; level is Info by default.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	// --- infrastructure ---
	db, err := infra.NewMySQL(cfg.MySQLDSN)
	if err != nil {
		slog.Error("mysql init failed", "err", err)
		os.Exit(1)
	}

	rdb, err := infra.NewRedis(cfg.RedisAddr, cfg.RedisPass)
	if err != nil {
		slog.Error("redis init failed", "err", err)
		os.Exit(1)
	}

	mq, err := infra.NewRabbitMQ(cfg.RabbitMQURL)
	if err != nil {
		slog.Error("rabbitmq init failed", "err", err)
		os.Exit(1)
	}

	// --- graceful shutdown ---
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	slog.Info("worker: started, waiting for messages (business logic not yet implemented)")

	<-quit
	slog.Info("worker: shutdown signal received")

	// Close infrastructure in reverse-init order.
	if err := mq.Close(); err != nil {
		slog.Error("rabbitmq: close error", "err", err)
	}
	if err := rdb.Close(); err != nil {
		slog.Error("redis: close error", "err", err)
	}
	if err := db.Close(); err != nil {
		slog.Error("mysql: close error", "err", err)
	}

	slog.Info("worker: shutdown complete")
}
