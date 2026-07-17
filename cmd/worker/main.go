package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Yangsss13/askdb-go/internal/config"
	"github.com/Yangsss13/askdb-go/internal/infra"
	"github.com/Yangsss13/askdb-go/internal/llm"
	"github.com/Yangsss13/askdb-go/internal/queryexec"
	"github.com/Yangsss13/askdb-go/internal/queryjob"
	"github.com/Yangsss13/askdb-go/internal/queryresult"
)

func main() {
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

	readerDB, err := infra.NewReaderDB(cfg.MySQLReaderDSN)
	if err != nil {
		slog.Error("reader db init failed", "err", err)
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

	// Consumer uses a dedicated channel, separate from the health-check channel.
	conCh, err := mq.NewChannel()
	if err != nil {
		slog.Error("rabbitmq: open consumer channel failed", "err", err)
		os.Exit(1)
	}

	// --- worker wiring ---
	repo := queryjob.NewGORMRepository(db.GORM)
	fakeLLM := llm.NewFakeLLMClient()
	executor := queryexec.NewExecutor(readerDB.SQL)
	resultStore := queryresult.NewRedisStore(rdb)
	workerSvc := queryjob.NewWorkerService(repo, fakeLLM, executor, resultStore, cfg.QueryTimeout, cfg.QueryResultTTL)

	consumer, err := queryjob.NewConsumer(conCh, workerSvc)
	if err != nil {
		slog.Error("consumer init failed", "err", err)
		os.Exit(1)
	}

	if err := consumer.Start(); err != nil {
		slog.Error("consumer start failed", "err", err)
		os.Exit(1)
	}
	slog.Info("worker: started, consuming from queue")

	// --- graceful shutdown ---
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("worker: shutdown signal received")

	// Allow up to 30 seconds for in-flight message processing.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		consumer.Stop()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("worker: consumer stopped")
	case <-shutdownCtx.Done():
		slog.Warn("worker: shutdown timeout exceeded")
	}

	// Close infrastructure in reverse-init order.
	if err := mq.Close(); err != nil {
		slog.Error("rabbitmq: close error", "err", err)
	}
	if err := rdb.Close(); err != nil {
		slog.Error("redis: close error", "err", err)
	}
	if err := readerDB.Close(); err != nil {
		slog.Error("reader db: close error", "err", err)
	}
	if err := db.Close(); err != nil {
		slog.Error("mysql: close error", "err", err)
	}

	slog.Info("worker: shutdown complete")
}
