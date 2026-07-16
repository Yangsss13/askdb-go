package queryexec

import (
	"database/sql"
	"testing"
)

func TestConvertCell(t *testing.T) {
	cases := []struct {
		name   string
		raw    sql.RawBytes
		dbType string
		want   any
	}{
		{"null", nil, "VARCHAR", nil},
		{"null int", nil, "BIGINT", nil},
		{"varchar", sql.RawBytes("Wireless Mouse"), "VARCHAR", "Wireless Mouse"},
		{"text bytes not base64", sql.RawBytes("hello"), "TEXT", "hello"},
		{"int", sql.RawBytes("150"), "INT", int64(150)},
		{"bigint", sql.RawBytes("9007199254740993"), "BIGINT", int64(9007199254740993)},
		{"unsigned int", sql.RawBytes("42"), "UNSIGNED INT", int64(42)},
		{"tinyint", sql.RawBytes("1"), "TINYINT", int64(1)},
		{"float", sql.RawBytes("3.5"), "FLOAT", float64(3.5)},
		{"double", sql.RawBytes("2.25"), "DOUBLE", float64(2.25)},
		{"decimal keeps precision string", sql.RawBytes("29.99"), "DECIMAL", "29.99"},
		{"newdecimal string", sql.RawBytes("149.00"), "NEWDECIMAL", "149.00"},
		{"datetime string", sql.RawBytes("2024-01-10 09:15:00"), "DATETIME", "2024-01-10 09:15:00"},
		{"enum string", sql.RawBytes("delivered"), "ENUM", "delivered"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := convertCell(c.raw, c.dbType)
			if got != c.want {
				t.Errorf("convertCell(%q, %q) = %#v (%T), want %#v (%T)",
					c.raw, c.dbType, got, got, c.want, c.want)
			}
		})
	}
}

func TestConvertCell_UnsignedOverflowFallsBackToString(t *testing.T) {
	// 2^64 - 1 overflows int64; expect the string form rather than a wrong number.
	got := convertCell(sql.RawBytes("18446744073709551615"), "UNSIGNED BIGINT")
	if got != "18446744073709551615" {
		t.Errorf("expected string fallback on overflow, got %#v (%T)", got, got)
	}
}
