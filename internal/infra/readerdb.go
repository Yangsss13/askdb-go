package infra

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// ReaderDB wraps the read-only *sql.DB connection to askdb_demo.
// It is intentionally separate from the GORM-managed askdb_app pool so the two
// databases have fully isolated connection pools and access patterns.
type ReaderDB struct {
	SQL *sql.DB
}

// NewReaderDB opens a database/sql connection to askdb_demo using the SELECT-only
// askdb_reader account and verifies connectivity. dsn must not be logged by the caller.
func NewReaderDB(dsn string) (*ReaderDB, error) {
	sqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("readerdb: open: %w", err)
	}

	// Isolated pool, smaller than the app pool since reads are the only workload.
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("readerdb: ping: %w", err)
	}

	slog.Info("readerdb: connected", "db", dbNameFromDSN(dsn))
	return &ReaderDB{SQL: sqlDB}, nil
}

// Close releases the connection pool.
func (r *ReaderDB) Close() error {
	return r.SQL.Close()
}

// Ping checks the reader connection. ctx is forwarded to the underlying driver.
func (r *ReaderDB) Ping(ctx context.Context) error {
	return r.SQL.PingContext(ctx)
}
