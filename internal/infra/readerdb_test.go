package infra

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestReaderDB_Lifecycle verifies that NewReaderDB connects and that Close
// releases the pool so subsequent use fails. It is the same Close the API's
// graceful-shutdown sequence invokes. Skipped unless MYSQL_READER_DSN is set.
func TestReaderDB_Lifecycle(t *testing.T) {
	dsn := os.Getenv("MYSQL_READER_DSN")
	if dsn == "" {
		t.Skip("MYSQL_READER_DSN not set; skipping reader DB lifecycle test")
	}

	rdb, err := NewReaderDB(dsn)
	if err != nil {
		t.Fatalf("NewReaderDB: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx); err != nil {
		t.Fatalf("ping before close: %v", err)
	}

	if err := rdb.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// After Close the pool must reject further use.
	if err := rdb.Ping(ctx); err == nil {
		t.Error("expected ping to fail after Close, got nil")
	}
}
