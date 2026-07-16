package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Yangsss13/askdb-go/internal/config"
	"github.com/Yangsss13/askdb-go/internal/handler"
	"github.com/Yangsss13/askdb-go/internal/infra"
	"github.com/Yangsss13/askdb-go/internal/llm"
	"github.com/Yangsss13/askdb-go/internal/queryexec"
	"github.com/Yangsss13/askdb-go/internal/queryjob"
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

	// --- query-job wiring (synchronous flow, Fake LLM) ---
	repo := queryjob.NewGORMRepository(db.GORM)
	fakeLLM := llm.NewFakeLLMClient()
	executor := queryexec.NewExecutor(readerDB.SQL)
	queryService := queryjob.NewService(repo, fakeLLM, executor, cfg.QueryTimeout)
	queryHandler := handler.NewQueryJobHandler(queryService)

	// --- routes ---
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", handler.Healthz)
	r.GET("/readyz", handler.Readyz(handler.HealthDeps{
		MySQL:  db,
		Redis:  rdb,
		Rabbit: mq,
	}))

	v1 := r.Group("/api/v1")
	{
		v1.POST("/query-jobs", queryHandler.Submit)
		v1.GET("/query-jobs/:id", queryHandler.Get)
	}

	srv := &http.Server{
		Addr:    ":" + cfg.APIPort,
		Handler: r,
	}

	// --- graceful shutdown ---
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("api: listening", "port", cfg.APIPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("api: server error", "err", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("api: shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("api: shutdown error", "err", err)
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

	slog.Info("api: shutdown complete")
}
