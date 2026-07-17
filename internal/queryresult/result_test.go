package queryresult

import (
	"encoding/json"
	"testing"
	"time"
)

func TestQueryResultKey(t *testing.T) {
	if got := QueryResultKey(42); got != "askdb:query-result:42:v1" {
		t.Errorf("QueryResultKey(42) = %q, want %q", got, "askdb:query-result:42:v1")
	}
	if got := QueryResultKey(1); got != "askdb:query-result:1:v1" {
		t.Errorf("QueryResultKey(1) = %q, want %q", got, "askdb:query-result:1:v1")
	}
}

func TestCachedQueryResult_MarshalRoundtrip(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	original := CachedQueryResult{
		JobID:   42,
		Columns: []string{"id", "name", "price", "score", "deleted_at"},
		Rows: [][]any{
			{int64(1), "商品A", "99.90", float64(3.14), nil},
			{int64(2), "商品B", "199.00", float64(2.71), nil},
		},
		RowCount:  2,
		CachedAt:  now,
		ExpiresAt: now.Add(15 * time.Minute),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := decodeCachedResult(data)
	if err != nil {
		t.Fatalf("decodeCachedResult: %v", err)
	}

	if decoded.JobID != original.JobID {
		t.Errorf("JobID: got %d, want %d", decoded.JobID, original.JobID)
	}
	if len(decoded.Columns) != len(original.Columns) {
		t.Fatalf("columns length: got %d, want %d", len(decoded.Columns), len(original.Columns))
	}
	for i, c := range original.Columns {
		if decoded.Columns[i] != c {
			t.Errorf("Columns[%d]: got %q, want %q", i, decoded.Columns[i], c)
		}
	}
	if decoded.RowCount != original.RowCount {
		t.Errorf("RowCount: got %d, want %d", decoded.RowCount, original.RowCount)
	}
}

// TestCachedQueryResult_Int64Preserved verifies that integer values survive
// JSON round-trip as int64, not float64 (json.Number normalization).
func TestCachedQueryResult_Int64Preserved(t *testing.T) {
	result := CachedQueryResult{
		JobID:     1,
		Columns:   []string{"id"},
		Rows:      [][]any{{int64(99)}},
		RowCount:  1,
		CachedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	data, _ := json.Marshal(result)
	decoded, err := decodeCachedResult(data)
	if err != nil {
		t.Fatalf("decodeCachedResult: %v", err)
	}

	v := decoded.Rows[0][0]
	if _, ok := v.(int64); !ok {
		t.Errorf("int64 round-trip: got %T(%v), want int64", v, v)
	}
	if v.(int64) != 99 {
		t.Errorf("int64 value: got %d, want 99", v.(int64))
	}
}

// TestCachedQueryResult_Float64Preserved verifies that float values survive
// JSON round-trip as float64.
func TestCachedQueryResult_Float64Preserved(t *testing.T) {
	result := CachedQueryResult{
		JobID:     1,
		Columns:   []string{"score"},
		Rows:      [][]any{{float64(3.14)}},
		RowCount:  1,
		CachedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	data, _ := json.Marshal(result)
	decoded, err := decodeCachedResult(data)
	if err != nil {
		t.Fatalf("decodeCachedResult: %v", err)
	}

	v := decoded.Rows[0][0]
	if _, ok := v.(float64); !ok {
		t.Errorf("float64 round-trip: got %T(%v), want float64", v, v)
	}
}

// TestCachedQueryResult_DecimalStringPreserved verifies DECIMAL strings are
// not changed by the round-trip.
func TestCachedQueryResult_DecimalStringPreserved(t *testing.T) {
	result := CachedQueryResult{
		JobID:     1,
		Columns:   []string{"price"},
		Rows:      [][]any{{"99.9900"}},
		RowCount:  1,
		CachedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	data, _ := json.Marshal(result)
	decoded, err := decodeCachedResult(data)
	if err != nil {
		t.Fatalf("decodeCachedResult: %v", err)
	}

	v := decoded.Rows[0][0]
	if s, ok := v.(string); !ok || s != "99.9900" {
		t.Errorf("decimal string round-trip: got %T(%v), want string(99.9900)", v, v)
	}
}

// TestCachedQueryResult_NullPreserved verifies SQL NULL (nil) survives the round-trip.
func TestCachedQueryResult_NullPreserved(t *testing.T) {
	result := CachedQueryResult{
		JobID:     1,
		Columns:   []string{"deleted_at"},
		Rows:      [][]any{{nil}},
		RowCount:  1,
		CachedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	data, _ := json.Marshal(result)
	decoded, err := decodeCachedResult(data)
	if err != nil {
		t.Fatalf("decodeCachedResult: %v", err)
	}

	if decoded.Rows[0][0] != nil {
		t.Errorf("nil round-trip: got %v, want nil", decoded.Rows[0][0])
	}
}

// TestCachedQueryResult_DatetimeStringPreserved verifies datetime strings are
// not altered by the round-trip.
func TestCachedQueryResult_DatetimeStringPreserved(t *testing.T) {
	const dt = "2026-07-17 08:00:00"
	result := CachedQueryResult{
		JobID:     1,
		Columns:   []string{"created_at"},
		Rows:      [][]any{{dt}},
		RowCount:  1,
		CachedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}

	data, _ := json.Marshal(result)
	decoded, err := decodeCachedResult(data)
	if err != nil {
		t.Fatalf("decodeCachedResult: %v", err)
	}

	if v, ok := decoded.Rows[0][0].(string); !ok || v != dt {
		t.Errorf("datetime string round-trip: got %T(%v), want string(%s)", decoded.Rows[0][0], decoded.Rows[0][0], dt)
	}
}
