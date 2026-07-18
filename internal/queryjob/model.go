// Package queryjob defines the data model and business logic for query jobs.
package queryjob

import (
	"database/sql"
	"time"
)

// QueryJob is the GORM persistence model for the query_jobs table in askdb_app.
// It is a storage type only and must never be serialized directly as an HTTP
// response; the handler maps it to a dedicated DTO.
type QueryJob struct {
	ID                  uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	Question            string         `gorm:"column:question"`
	GeneratedSQL        sql.NullString `gorm:"column:generated_sql"`
	Status              string         `gorm:"column:status"`
	ErrorCode           sql.NullString `gorm:"column:error_code"`
	ErrorMessage        sql.NullString `gorm:"column:error_message"`
	RowCount            sql.NullInt64  `gorm:"column:row_count"`
	ExecutionDurationMs sql.NullInt64  `gorm:"column:execution_duration_ms"`
	CreatedAt           time.Time      `gorm:"column:created_at"`
	UpdatedAt           time.Time      `gorm:"column:updated_at"`
	FinishedAt          sql.NullTime   `gorm:"column:finished_at"`
	// ResultExpiresAt is when the Redis-cached result expires.
	// NULL means the job has not succeeded yet, or caching was not written.
	ResultExpiresAt sql.NullTime `gorm:"column:result_expires_at"`
	// UserID is the owner of this job. NULL for pre-auth legacy rows;
	// the application layer enforces non-NULL for all new jobs.
	UserID sql.NullInt64 `gorm:"column:user_id"`
	// DataSourceID references the data source against which this job runs.
	// NULL for pre-6B legacy rows; new jobs must provide a non-NULL value.
	DataSourceID sql.NullInt64 `gorm:"column:data_source_id"`
}

// TableName pins the table name so GORM does not pluralize unexpectedly.
func (QueryJob) TableName() string { return "query_jobs" }
