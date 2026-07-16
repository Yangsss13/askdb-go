// Package queryexec runs read-only SQL against the askdb_demo database via
// database/sql and converts result rows into JSON-friendly Go values.
package queryexec

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// maxRows is a defensive hard cap on rows read from a result set. The fixed SQL
// already carries LIMIT 100; this guards against any statement that does not.
const maxRows = 100

// Executor runs read-only queries against askdb_demo. It holds a *sql.DB whose
// pool is isolated from the application's GORM pool.
type Executor struct {
	db *sql.DB
}

// NewExecutor returns an Executor backed by the given read-only *sql.DB.
func NewExecutor(db *sql.DB) *Executor {
	return &Executor{db: db}
}

// Execute runs query under ctx and returns the column names (in result order)
// and rows. Each cell is converted to a JSON-friendly value: NULL becomes nil,
// integers become int64, FLOAT/DOUBLE become float64, DECIMAL and datetime
// become strings, and all other text/binary becomes string (never Base64).
//
// The returned error wraps the underlying driver error for internal logging;
// callers must not expose it directly to clients.
func (e *Executor) Execute(ctx context.Context, query string) ([]string, [][]any, error) {
	rows, err := e.db.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, fmt.Errorf("queryexec: query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, fmt.Errorf("queryexec: columns: %w", err)
	}

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, nil, fmt.Errorf("queryexec: column types: %w", err)
	}

	result := make([][]any, 0)
	for rows.Next() {
		if len(result) >= maxRows {
			break
		}

		// Scan into RawBytes so NULLs are distinguishable (nil) and no premature
		// driver-side type coercion occurs; we convert explicitly below.
		raw := make([]sql.RawBytes, len(columns))
		dest := make([]any, len(columns))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, fmt.Errorf("queryexec: scan: %w", err)
		}

		row := make([]any, len(columns))
		for i, rb := range raw {
			row[i] = convertCell(rb, colTypes[i].DatabaseTypeName())
		}
		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("queryexec: rows: %w", err)
	}

	return columns, result, nil
}

// convertCell maps a raw column value to a JSON-friendly Go value based on the
// MySQL database type name. A nil RawBytes means SQL NULL.
func convertCell(rb sql.RawBytes, dbType string) any {
	if rb == nil {
		return nil
	}
	s := string(rb)

	switch strings.ToUpper(dbType) {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT",
		"UNSIGNED TINYINT", "UNSIGNED SMALLINT", "UNSIGNED MEDIUMINT",
		"UNSIGNED INT", "UNSIGNED BIGINT", "YEAR":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		// Unsigned values that overflow int64 fall back to their string form.
		return s
	case "FLOAT", "DOUBLE":
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return s
	case "DECIMAL", "NEWDECIMAL":
		// Preserve exact precision by keeping the string representation.
		return s
	default:
		// VARCHAR, CHAR, TEXT, DATETIME, TIMESTAMP, DATE, TIME, ENUM, etc.
		// []byte is converted to string so it is not Base64-encoded in JSON.
		return s
	}
}
