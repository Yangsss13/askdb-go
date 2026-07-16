package infra

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB wraps the GORM DB instance.
// The underlying *sql.DB is configured here and must be closed on shutdown.
type DB struct {
	GORM *gorm.DB
}

// NewMySQL opens a GORM connection to askdb_app and verifies connectivity.
// dsn must not be logged by the caller.
func NewMySQL(dsn string) (*DB, error) {
	gormDB, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		// Silence GORM's own query logger in production; structured app logging
		// handles observability separately.
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("mysql: open: %w", err)
	}

	// Retrieve the underlying *sql.DB to configure the connection pool.
	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("mysql: get sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("mysql: ping: %w", err)
	}

	slog.Info("mysql: connected", "db", dbNameFromDSN(dsn))
	return &DB{GORM: gormDB}, nil
}

// Close releases the underlying connection pool.
func (d *DB) Close() error {
	sqlDB, err := d.GORM.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping checks the database connection. ctx is forwarded to the underlying driver.
func (d *DB) Ping(ctx context.Context) error {
	sqlDB, err := d.GORM.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// dbNameFromDSN extracts the database name from a MySQL DSN for safe logging.
// MySQL DSN format: user:pass@protocol(host:port)/dbname?params
// Returns "<unknown>" on any parse failure. Never logs credentials.
func dbNameFromDSN(dsn string) string {
	// Find the last '/' before any '?'
	queryStart := strings.Index(dsn, "?")
	relevant := dsn
	if queryStart != -1 {
		relevant = dsn[:queryStart]
	}
	lastSlash := strings.LastIndex(relevant, "/")
	if lastSlash == -1 || lastSlash == len(relevant)-1 {
		return "<unknown>"
	}
	return relevant[lastSlash+1:]
}
