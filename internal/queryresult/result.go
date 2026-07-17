package queryresult

import (
	"fmt"
	"time"
)

// CachedQueryResult is the payload stored in Redis for a completed query job.
// It is serialized as JSON. Rows use the same type conventions as QueryExecutor:
// int64 for integers, float64 for floats, string for DECIMAL and datetime,
// and nil for SQL NULL. []byte values are never present (executor converts to string).
type CachedQueryResult struct {
	JobID     uint64    `json:"job_id"`
	Columns   []string  `json:"columns"`
	Rows      [][]any   `json:"rows"`
	RowCount  int64     `json:"row_count"`
	CachedAt  time.Time `json:"cached_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// QueryResultKey returns the Redis key for the given job ID.
// The key embeds a version suffix (:v1) so future key formats can coexist
// during a migration without colliding with existing entries.
func QueryResultKey(jobID uint64) string {
	return fmt.Sprintf("askdb:query-result:%d:v1", jobID)
}
