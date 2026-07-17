package sqlguard

import (
	"context"
	"testing"
)

// FuzzValidate feeds arbitrary input to the guard. The only invariant asserted
// is that Validate never panics and never returns a nil error together with an
// empty NormalizedSQL. Deterministic accept/reject correctness is covered by the
// table-driven tests; this fuzz target guards against parser-driven panics and
// pathological input.
func FuzzValidate(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"-- only a comment",
		"/* block comment */",
		"SELECT id FROM products",
		"SELECT id FROM products LIMIT 10",
		"SELECT 1; DROP TABLE products",
		"WITH c AS (SELECT id FROM orders) SELECT id FROM c",
		"WITH RECURSIVE c AS (SELECT 1) SELECT * FROM c",
		"SELECT id FROM products UNION SELECT product_id FROM order_items",
		"SELECT id FROM products WHERE id IN (SELECT product_id FROM order_items WHERE quantity > (SELECT AVG(quantity) FROM order_items))",
		"SELECT SLEEP(5)",
		"SELECT id FROM products INTO OUTFILE '/tmp/x'",
		"\x00\x01\x02 not valid utf8 \xff\xfe",
		"SELECT `id`,`name` FROM `products` /*! MySQL hint */ LIMIT 100",
		"SELECT id FROM products LIMIT 999999999999999999999999",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	g := New()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, sql string) {
		in := ValidateInput{
			SQL:             sql,
			AllowedDatabase: "askdb_demo",
			AllowedTables:   []string{"products", "orders", "order_items"},
			MaxRows:         100,
		}
		res, err := g.Validate(ctx, in)
		if err == nil && res.NormalizedSQL == "" {
			t.Errorf("accepted SQL with empty NormalizedSQL: input=%q", sql)
		}
	})
}
