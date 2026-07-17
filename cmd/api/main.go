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

	// Publisher uses a dedicated channel, separate from the health-check channel.
	pubCh, err := mq.NewChannel()
	if err != nil {
		slog.Error("rabbitmq: open publisher channel failed", "err", err)
		os.Exit(1)
	}

	publisher, err := queryjob.NewRabbitMQPublisher(pubCh)
	if err != nil {
		slog.Error("publisher init failed", "err", err)
		os.Exit(1)
	}

	// --- query-job wiring ---
	repo := queryjob.NewGORMRepository(db.GORM)
	resultStore := queryresult.NewRedisStore(rdb)
	queryService := queryjob.NewService(repo, publisher)
	resultService := queryjob.NewResultService(repo, resultStore)
	queryHandler := handler.NewQueryJobHandler(queryService, resultService)

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
		v1.GET("/query-jobs/:id/result", queryHandler.GetResult)
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
	if err := publisher.Close(); err != nil {
		slog.Error("publisher: close error", "err", err)
	}
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
