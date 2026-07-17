package sqlguard

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// policy returns the standard test policy (askdb_demo + demo tables, max 100).
func policy(sql string) ValidateInput {
	return ValidateInput{
		SQL:             sql,
		AllowedDatabase: "askdb_demo",
		AllowedTables:   []string{"products", "orders", "order_items"},
		MaxRows:         100,
	}
}

func TestGuard_Allowed(t *testing.T) {
	g := New()
	cases := []struct {
		name string
		sql  string
	}{
		{"simple select", "SELECT id FROM products"},
		{"select with limit", "SELECT id FROM products LIMIT 10"},
		{"count star", "SELECT COUNT(*) FROM orders"},
		{"join", "SELECT p.id, oi.quantity FROM products p JOIN order_items oi ON oi.product_id = p.id"},
		{"group by aggregate", "SELECT category, COUNT(*) FROM products GROUP BY category"},
		{"order by", "SELECT id FROM products ORDER BY price DESC"},
		{"non-recursive cte", "WITH recent AS (SELECT id, product_id FROM order_items) SELECT id FROM recent"},
		{"subquery", "SELECT id FROM products WHERE id IN (SELECT product_id FROM order_items)"},
		{"union", "SELECT id FROM products UNION SELECT product_id FROM order_items"},
		{"union all", "SELECT id FROM products UNION ALL SELECT product_id FROM order_items"},
		{"schema qualified", "SELECT id FROM askdb_demo.products"},
		{"constant no table", "SELECT 1"},
		{"case insensitive", "sElEcT Id FrOm PRODUCTS"},
		{"backticks", "SELECT `id` FROM `products`"},
		{"comment", "SELECT id FROM products -- trailing comment\n"},
		{"whitespace", "  SELECT   id\n\tFROM products  "},
		{"derived table", "SELECT t.id FROM (SELECT id FROM products) t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := g.Validate(context.Background(), policy(tc.sql))
			if err != nil {
				t.Fatalf("expected allow, got error: %v", err)
			}
			if res.NormalizedSQL == "" {
				t.Error("NormalizedSQL must not be empty")
			}
			if res.Limit <= 0 || res.Limit > 100 {
				t.Errorf("effective limit out of range: %d", res.Limit)
			}
			// The normalized SQL must itself pass a re-validation (already done
			// internally) and carry a LIMIT.
			if !strings.Contains(strings.ToUpper(res.NormalizedSQL), "LIMIT") {
				t.Errorf("normalized SQL missing LIMIT: %q", res.NormalizedSQL)
			}
		})
	}
}

func TestGuard_Rejected(t *testing.T) {
	g := New()
	cases := []struct {
		name string
		sql  string
	}{
		{"multi statement", "SELECT 1; DROP TABLE products"},
		{"multi select", "SELECT id FROM products; SELECT id FROM orders"},
		{"delete", "DELETE FROM products"},
		{"update", "UPDATE products SET stock = 0"},
		{"insert", "INSERT INTO products (id) VALUES (1)"},
		{"replace", "REPLACE INTO products (id) VALUES (1)"},
		{"drop", "DROP TABLE products"},
		{"create", "CREATE TABLE x (id INT)"},
		{"alter", "ALTER TABLE products ADD COLUMN x INT"},
		{"truncate", "TRUNCATE TABLE products"},
		{"rename", "RENAME TABLE products TO p2"},
		{"call", "CALL some_proc()"},
		{"set", "SET @x = 1"},
		{"use", "USE askdb_demo"},
		{"show", "SHOW TABLES"},
		{"explain", "EXPLAIN SELECT id FROM products"},
		{"sleep", "SELECT SLEEP(5)"},
		{"benchmark", "SELECT BENCHMARK(1000000, MD5('x'))"},
		{"load_file", "SELECT LOAD_FILE('/etc/passwd')"},
		{"get_lock", "SELECT GET_LOCK('x', 10)"},
		{"release_lock", "SELECT RELEASE_LOCK('x')"},
		{"into outfile", "SELECT id FROM products INTO OUTFILE '/tmp/x'"},
		{"into dumpfile", "SELECT id FROM products INTO DUMPFILE '/tmp/x'"},
		{"into var", "SELECT id FROM products INTO @x"},
		{"for update", "SELECT id FROM products FOR UPDATE"},
		{"lock in share mode", "SELECT id FROM products LOCK IN SHARE MODE"},
		{"user variable", "SELECT @x FROM products"},
		{"system variable", "SELECT @@version"},
		{"session variable", "SELECT @@session.autocommit"},
		{"mysql.user", "SELECT user FROM mysql.user"},
		{"information_schema", "SELECT table_name FROM information_schema.tables"},
		{"performance_schema", "SELECT * FROM performance_schema.threads"},
		{"sys schema", "SELECT * FROM sys.version"},
		{"askdb_app schema", "SELECT id FROM askdb_app.query_jobs"},
		{"non-whitelist table", "SELECT id FROM customers"},
		{"with recursive", "WITH RECURSIVE cte AS (SELECT 1 AS n UNION ALL SELECT n+1 FROM cte WHERE n < 5) SELECT n FROM cte"},
		{"cte body illegal table", "WITH c AS (SELECT id FROM mysql.user) SELECT id FROM c"},
		{"subquery illegal table", "SELECT id FROM products WHERE id IN (SELECT id FROM mysql.user)"},
		{"union branch illegal", "SELECT id FROM products UNION SELECT user FROM mysql.user"},
		{"disallowed function", "SELECT VERSION()"},
		{"window function", "SELECT id, ROW_NUMBER() OVER (ORDER BY id) FROM products"},
		{"garbage", "NOT SQL AT ALL @#$%"},
		{"empty", ""},
		{"only comment", "-- just a comment"},
		{"limit param", "SELECT id FROM products LIMIT ?"},
		{"limit negative", "SELECT id FROM products LIMIT -1"},
		{"limit expression", "SELECT id FROM products LIMIT 2+3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := g.Validate(context.Background(), policy(tc.sql))
			if err == nil {
				t.Fatalf("expected rejection, got allow")
			}
			if !errors.Is(err, ErrRejected) {
				t.Errorf("expected ErrRejected, got %v", err)
			}
		})
	}
}

func TestGuard_LimitInjection(t *testing.T) {
	g := New()
	res, err := g.Validate(context.Background(), policy("SELECT id FROM products"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Limit != 100 {
		t.Errorf("expected injected LIMIT 100, got %d", res.Limit)
	}
	if !strings.Contains(res.NormalizedSQL, "LIMIT 100") {
		t.Errorf("expected LIMIT 100 in %q", res.NormalizedSQL)
	}
}

func TestGuard_LimitPreserved(t *testing.T) {
	g := New()
	res, err := g.Validate(context.Background(), policy("SELECT id FROM products LIMIT 10"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Limit != 10 {
		t.Errorf("expected LIMIT 10 preserved, got %d", res.Limit)
	}
}

func TestGuard_LimitCompressed(t *testing.T) {
	g := New()
	for _, sql := range []string{
		"SELECT id FROM products LIMIT 101",
		"SELECT id FROM products LIMIT 10000",
	} {
		res, err := g.Validate(context.Background(), policy(sql))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", sql, err)
		}
		if res.Limit != 100 {
			t.Errorf("%s: expected compressed to 100, got %d", sql, res.Limit)
		}
	}
}

func TestGuard_LimitBoundary(t *testing.T) {
	g := New()
	res, err := g.Validate(context.Background(), policy("SELECT id FROM products LIMIT 100"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Limit != 100 {
		t.Errorf("expected LIMIT 100 preserved, got %d", res.Limit)
	}
}

func TestGuard_LimitOffsetForms(t *testing.T) {
	g := New()
	// LIMIT offset, count  and  LIMIT count OFFSET offset — both with count 20 > 0
	// and a small offset should be accepted and compressed to max.
	cases := []struct {
		sql       string
		wantLimit int
	}{
		{"SELECT id FROM products LIMIT 5, 20", 20},
		{"SELECT id FROM products LIMIT 20 OFFSET 5", 20},
		{"SELECT id FROM products LIMIT 5, 500", 100}, // count compressed
	}
	for _, tc := range cases {
		res, err := g.Validate(context.Background(), policy(tc.sql))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.sql, err)
		}
		if res.Limit != tc.wantLimit {
			t.Errorf("%s: expected limit %d, got %d", tc.sql, tc.wantLimit, res.Limit)
		}
	}
}

func TestGuard_HugeOffsetRejected(t *testing.T) {
	g := New()
	// OFFSET greater than MaxRows must be rejected.
	_, err := g.Validate(context.Background(), policy("SELECT id FROM products LIMIT 10 OFFSET 100000"))
	if !errors.Is(err, ErrRejected) {
		t.Errorf("expected rejection for huge offset, got %v", err)
	}
}

func TestGuard_NormalizedSQLReExecutable(t *testing.T) {
	g := New()
	// The normalized output of one pass must itself validate cleanly (idempotence).
	res, err := g.Validate(context.Background(), policy("select p.id from products p join order_items oi on oi.product_id = p.id where p.price > 10"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res2, err := g.Validate(context.Background(), policy(res.NormalizedSQL))
	if err != nil {
		t.Fatalf("normalized SQL failed re-validation: %v", err)
	}
	if res2.NormalizedSQL != res.NormalizedSQL {
		t.Errorf("re-normalization not idempotent:\n first: %q\nsecond: %q", res.NormalizedSQL, res2.NormalizedSQL)
	}
}

func TestGuard_ContextCancelled(t *testing.T) {
	g := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := g.Validate(ctx, policy("SELECT id FROM products"))
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	// A cancelled context is a runtime error, NOT a rejection.
	if errors.Is(err, ErrRejected) {
		t.Error("cancelled context must not be reported as ErrRejected")
	}
}

func TestGuard_CTENameShadowingRejected(t *testing.T) {
	g := New()
	// A CTE named after a physical whitelist table is rejected to avoid ambiguity.
	_, err := g.Validate(context.Background(), policy("WITH products AS (SELECT 1 AS id) SELECT id FROM products"))
	if !errors.Is(err, ErrRejected) {
		t.Errorf("expected rejection for CTE shadowing physical table, got %v", err)
	}
}

func TestGuard_TablesReported(t *testing.T) {
	g := New()
	res, err := g.Validate(context.Background(), policy("SELECT p.id FROM products p JOIN order_items oi ON oi.product_id = p.id"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Tables) != 2 {
		t.Fatalf("expected 2 tables, got %v", res.Tables)
	}
	// tableList is sorted.
	if res.Tables[0] != "order_items" || res.Tables[1] != "products" {
		t.Errorf("unexpected tables: %v", res.Tables)
	}
}
