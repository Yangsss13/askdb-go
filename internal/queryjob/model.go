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
}

// TableName pins the table name so GORM does not pluralize unexpectedly.
func (QueryJob) TableName() string { return "query_jobs" }
