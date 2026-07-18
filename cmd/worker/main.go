package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Yangsss13/askdb-go/internal/config"
	"github.com/Yangsss13/askdb-go/internal/crypto"
	"github.com/Yangsss13/askdb-go/internal/datasource"
	"github.com/Yangsss13/askdb-go/internal/infra"
	"github.com/Yangsss13/askdb-go/internal/llm"
	"github.com/Yangsss13/askdb-go/internal/netguard"
	"github.com/Yangsss13/askdb-go/internal/queryexec"
	"github.com/Yangsss13/askdb-go/internal/queryjob"
	"github.com/Yangsss13/askdb-go/internal/queryresult"
	"github.com/Yangsss13/askdb-go/internal/sqlguard"
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

	// DATA_SOURCE_KEY is required by both processes.
	if err := cfg.ValidateDataSourceKey(); err != nil {
		slog.Error("data source key invalid", "err", err)
		os.Exit(1)
	}
	cipher, err := crypto.NewCipher(cfg.DataSourceKey)
	if err != nil {
		slog.Error("crypto cipher init failed", "err", err)
		os.Exit(1)
	}
	allowedPorts, err := netguard.ParseAllowedPorts(cfg.AllowedDBPorts)
	if err != nil {
		slog.Error("netguard: invalid ALLOWED_DB_PORTS", "err", err)
		os.Exit(1)
	}
	ngValidator, err := netguard.NewValidator(netguard.Config{
		AllowedPorts:     allowedPorts,
		PrivateAllowlist: netguard.ParsePrivateAllowlist(cfg.PrivateHostAllowlist),
	})
	if err != nil {
		slog.Error("netguard: validator init failed", "err", err)
		os.Exit(1)
	}
	dsRepo := datasource.NewGORMRepository(db.GORM)
	dsSvc := datasource.NewService(dsRepo, cipher, ngValidator)
	dsOpener := &dsServiceOpener{svc: dsSvc, maxRows: cfg.MaxQueryRows}

	fakeLLM := llm.NewFakeLLMClient()
	guard := sqlguard.New()
	policy := queryjob.GuardPolicy{
		AllowedTables: []string{"products", "orders", "order_items"},
		MaxRows:       cfg.MaxQueryRows,
	}
	executor := queryexec.NewExecutor(readerDB.SQL, cfg.MaxQueryRows)
	resultStore := queryresult.NewRedisStore(rdb)
	workerSvc := queryjob.NewWorkerService(
		repo, fakeLLM, guard, policy, executor, resultStore,
		cfg.QueryTimeout, cfg.QueryResultTTL, cfg.MaxResultBytes,
		"askdb_demo", dsOpener,
	)

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

// dsServiceOpener adapts datasource.Service to queryjob.DataSourceOpener.
type dsServiceOpener struct {
	svc     *datasource.Service
	maxRows int
}

func (o *dsServiceOpener) OpenForJob(ctx context.Context, dataSourceID uint64) (string, queryjob.QueryExecutor, func(), error) {
	ds, err := o.svc.GetByIDRaw(ctx, dataSourceID)
	if err != nil {
		return "", nil, nil, err
	}
	sqlDB, err := o.svc.OpenDB(ctx, ds)
	if err != nil {
		return "", nil, nil, err
	}
	exec := queryexec.NewExecutor(sqlDB, o.maxRows)
	closer := func() { closeSilently(sqlDB) }
	return ds.DatabaseName, exec, closer, nil
}

// closeSilently closes db, logging any error without propagating it.
func closeSilently(db *sql.DB) {
	if err := db.Close(); err != nil {
		slog.Error("worker: close dynamic db", "err", err)
	}
}
