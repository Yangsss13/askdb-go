package datasource

import (
	"database/sql"
	"time"
)

// TLSMode represents the TLS policy for a data-source connection.
// Only "disabled" and "verify-full" are supported; any silent downgrade
// is rejected at validation time.
type TLSMode string

const (
	TLSDisabled   TLSMode = "disabled"
	TLSVerifyFull TLSMode = "verify-full"
)

// DataSource is the GORM persistence model for the data_sources table.
// password_ciphertext must never appear in HTTP responses, logs, or errors.
type DataSource struct {
	ID                 uint64       `gorm:"column:id;primaryKey;autoIncrement"`
	UserID             uint64       `gorm:"column:user_id"`
	Label              string       `gorm:"column:label"`
	Host               string       `gorm:"column:host"`
	Port               uint16       `gorm:"column:port"`
	DatabaseName       string       `gorm:"column:database_name"`
	Username           string       `gorm:"column:username"`
	PasswordCiphertext string       `gorm:"column:password_ciphertext"`
	TLSMode            string       `gorm:"column:tls_mode"`
	ConnectTimeoutSec  uint8        `gorm:"column:connect_timeout_sec"`
	CreatedAt          time.Time    `gorm:"column:created_at"`
	UpdatedAt          time.Time    `gorm:"column:updated_at"`
	DeletedAt          sql.NullTime `gorm:"column:deleted_at"`
}

// TableName pins the table name so GORM does not pluralize unexpectedly.
func (DataSource) TableName() string { return "data_sources" }
