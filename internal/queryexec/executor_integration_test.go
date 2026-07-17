//go:build integration

// These tests require the Docker-provided askdb_demo database and the
// askdb_reader account. Run with: go test -tags=integration ./internal/queryexec/
//
// MYSQL_READER_DSN must point at askdb_demo via the askdb_reader user, e.g.:
//
//	askdb_reader:reader_dev_pass@tcp(localhost:3306)/askdb_demo?parseTime=true
package queryexec

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func openReader(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MYSQL_READER_DSN")
	if dsn == "" {
		t.Skip("MYSQL_READER_DSN not set; skipping integration test")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db
}

func TestExecute_SelectAllProducts(t *testing.T) {
	db := openReader(t)
	defer db.Close()

	exec := NewExecutor(db, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	columns, rows, err := exec.Execute(ctx,
		"SELECT id, name, category, price, stock, created_at FROM products ORDER BY id LIMIT 100")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	wantCols := []string{"id", "name", "category", "price", "stock", "created_at"}
	if len(columns) != len(wantCols) {
		t.Fatalf("expected %d columns, got %v", len(wantCols), columns)
	}
	for i, c := range wantCols {
		if columns[i] != c {
			t.Errorf("column %d: expected %q, got %q", i, c, columns[i])
		}
	}
	if len(rows) != 10 {
		t.Fatalf("expected 10 seeded products, got %d", len(rows))
	}

	// Type conversions on the first row: id=int64, name/category=string,
	// price=DECIMAL string, stock=int64, created_at=string.
	first := rows[0]
	if _, ok := first[0].(int64); !ok {
		t.Errorf("id should be int64, got %T", first[0])
	}
	if _, ok := first[1].(string); !ok {
		t.Errorf("name should be string, got %T", first[1])
	}
	if _, ok := first[3].(string); !ok {
		t.Errorf("price (DECIMAL) should be string, got %T", first[3])
	}
	if _, ok := first[4].(int64); !ok {
		t.Errorf("stock should be int64, got %T", first[4])
	}
	if _, ok := first[5].(string); !ok {
		t.Errorf("created_at should be string, got %T", first[5])
	}
}

func TestReader_CannotWrite(t *testing.T) {
	db := openReader(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stmts := map[string]string{
		"insert": "INSERT INTO products (name, category, price, stock) VALUES ('x', 'y', 1.0, 1)",
		"update": "UPDATE products SET stock = stock + 1 WHERE id = 1",
		"delete": "DELETE FROM products WHERE id = 1",
	}
	for name, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err == nil {
			t.Errorf("%s: expected permission error for read-only user, got nil", name)
		}
	}
}
