package datasource

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// Repository persists data sources. Declared on the consuming side.
type Repository interface {
	// Create inserts a new row and populates its generated ID.
	// Returns ErrDuplicateLabel on (user_id, label) conflict.
	Create(ctx context.Context, ds *DataSource) error
	// FindByID returns the source only when it belongs to userID and is not deleted.
	FindByID(ctx context.Context, id, userID uint64) (*DataSource, error)
	// FindByIDRaw loads a row by ID regardless of deleted_at or owner, used by
	// the Worker which must still execute jobs against a soft-deleted source.
	FindByIDRaw(ctx context.Context, id uint64) (*DataSource, error)
	// List returns all non-deleted sources owned by userID.
	List(ctx context.Context, userID uint64) ([]*DataSource, error)
	// Update applies the non-zero fields of patch to the row identified by id+userID.
	// Returns ErrNotFound when the row is missing, foreign or already deleted.
	// Returns ErrDuplicateLabel on label conflict.
	Update(ctx context.Context, id, userID uint64, patch UpdatePatch) error
	// SoftDelete marks the row deleted; returns ErrNotFound if absent/foreign/already gone.
	SoftDelete(ctx context.Context, id, userID uint64, now time.Time) error
	// HasActiveJobs reports whether any non-terminal query job references this source.
	// Uses a SELECT FOR SHARE lock so the deletion decision is made atomically
	// within the caller's transaction.
	HasActiveJobs(ctx context.Context, dataSourceID uint64) (bool, error)
	// UpdateCiphertext updates password_ciphertext in the same transaction; used
	// after the row is inserted and its ID is known (AAD requires the stable ID).
	UpdateCiphertext(ctx context.Context, id uint64, ciphertext string) error
	// WithTx executes fn inside a transaction. fn receives a Repository scoped to
	// the transaction so all operations share the same connection.
	WithTx(ctx context.Context, fn func(tx Repository) error) error
}

// UpdatePatch carries the fields that may be changed on an existing source.
// Zero values are skipped; use explicit pointers where nil means "no change".
type UpdatePatch struct {
	Label             *string
	Host              *string
	Port              *uint16
	DatabaseName      *string
	Username          *string
	PasswordCipher    *string // nil = keep existing; non-nil = replace
	TLSMode           *string
	ConnectTimeoutSec *uint8
}

// GORMRepository implements Repository against askdb_app via GORM.
type GORMRepository struct {
	db *gorm.DB
}

// NewGORMRepository returns a repository backed by the given GORM handle.
func NewGORMRepository(db *gorm.DB) *GORMRepository {
	return &GORMRepository{db: db}
}

func (r *GORMRepository) Create(ctx context.Context, ds *DataSource) error {
	err := r.db.WithContext(ctx).Create(ds).Error
	if err != nil {
		if isDuplicateEntry(err) {
			return ErrDuplicateLabel
		}
		return fmt.Errorf("datasource: create: %w", err)
	}
	return nil
}

func (r *GORMRepository) FindByID(ctx context.Context, id, userID uint64) (*DataSource, error) {
	var ds DataSource
	err := r.db.WithContext(ctx).
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).
		First(&ds).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("datasource: find: %w", err)
	}
	return &ds, nil
}

func (r *GORMRepository) FindByIDRaw(ctx context.Context, id uint64) (*DataSource, error) {
	var ds DataSource
	err := r.db.WithContext(ctx).
		Unscoped().
		Where("id = ?", id).
		First(&ds).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("datasource: find raw: %w", err)
	}
	return &ds, nil
}

func (r *GORMRepository) List(ctx context.Context, userID uint64) ([]*DataSource, error) {
	var sources []*DataSource
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND deleted_at IS NULL", userID).
		Order("id ASC").
		Find(&sources).Error
	if err != nil {
		return nil, fmt.Errorf("datasource: list: %w", err)
	}
	return sources, nil
}

func (r *GORMRepository) Update(ctx context.Context, id, userID uint64, patch UpdatePatch) error {
	updates := map[string]any{"updated_at": time.Now()}
	if patch.Label != nil {
		updates["label"] = *patch.Label
	}
	if patch.Host != nil {
		updates["host"] = *patch.Host
	}
	if patch.Port != nil {
		updates["port"] = *patch.Port
	}
	if patch.DatabaseName != nil {
		updates["database_name"] = *patch.DatabaseName
	}
	if patch.Username != nil {
		updates["username"] = *patch.Username
	}
	if patch.PasswordCipher != nil {
		updates["password_ciphertext"] = *patch.PasswordCipher
	}
	if patch.TLSMode != nil {
		updates["tls_mode"] = *patch.TLSMode
	}
	if patch.ConnectTimeoutSec != nil {
		updates["connect_timeout_sec"] = *patch.ConnectTimeoutSec
	}

	res := r.db.WithContext(ctx).
		Model(&DataSource{}).
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).
		Updates(updates)
	if res.Error != nil {
		if isDuplicateEntry(res.Error) {
			return ErrDuplicateLabel
		}
		return fmt.Errorf("datasource: update: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *GORMRepository) SoftDelete(ctx context.Context, id, userID uint64, now time.Time) error {
	res := r.db.WithContext(ctx).
		Model(&DataSource{}).
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).
		Updates(map[string]any{
			"deleted_at": now,
			"updated_at": now,
		})
	if res.Error != nil {
		return fmt.Errorf("datasource: soft delete: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *GORMRepository) HasActiveJobs(ctx context.Context, dataSourceID uint64) (bool, error) {
	// non-terminal statuses defined in queryjob.Status
	activeStatuses := []string{"pending", "queued", "generating", "validating", "executing"}
	var count int64
	err := r.db.WithContext(ctx).
		Raw(`SELECT COUNT(*) FROM query_jobs
		     WHERE data_source_id = ? AND status IN ? FOR SHARE`,
			dataSourceID, activeStatuses).
		Scan(&count).Error
	if err != nil {
		return false, fmt.Errorf("datasource: has active jobs: %w", err)
	}
	return count > 0, nil
}

func (r *GORMRepository) UpdateCiphertext(ctx context.Context, id uint64, ciphertext string) error {
	res := r.db.WithContext(ctx).
		Model(&DataSource{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"password_ciphertext": ciphertext,
			"updated_at":          time.Now(),
		})
	if res.Error != nil {
		return fmt.Errorf("datasource: update ciphertext: %w", res.Error)
	}
	return nil
}

// ExistsForUser satisfies queryjob.DataSourceChecker: returns true when a
// non-deleted data source with the given ID is owned by userID.
func (r *GORMRepository) ExistsForUser(ctx context.Context, dataSourceID, userID uint64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&DataSource{}).
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", dataSourceID, userID).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("datasource: exists for user: %w", err)
	}
	return count > 0, nil
}

func (r *GORMRepository) WithTx(ctx context.Context, fn func(tx Repository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&GORMRepository{db: tx})
	})
}

func isDuplicateEntry(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
