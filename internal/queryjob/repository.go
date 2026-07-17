package queryjob

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ErrStatusConflict is returned when a conditional status update finds no
// matching row — either the job's current status differed from the expected
// "from" state, or the job no longer exists.
var ErrStatusConflict = errors.New("queryjob: status conflict")

// GORMRepository persists query jobs in askdb_app via GORM.
// It satisfies the Repository interface declared in service.go.
type GORMRepository struct {
	db *gorm.DB
}

// NewGORMRepository returns a repository backed by the given GORM handle.
func NewGORMRepository(db *gorm.DB) *GORMRepository {
	return &GORMRepository{db: db}
}

// Create inserts a new job and populates its generated ID.
func (r *GORMRepository) Create(ctx context.Context, job *QueryJob) error {
	if err := r.db.WithContext(ctx).Create(job).Error; err != nil {
		return fmt.Errorf("queryjob: create: %w", err)
	}
	return nil
}

// FindByID loads a job by primary key, returning ErrJobNotFound when absent.
func (r *GORMRepository) FindByID(ctx context.Context, id uint64) (*QueryJob, error) {
	var job QueryJob
	err := r.db.WithContext(ctx).First(&job, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("queryjob: find: %w", err)
	}
	return &job, nil
}

// TransitionStatus updates a job's status from `from` to `to` only when the
// current DB status equals `from`. Returns ErrStatusConflict when no rows
// were affected (status mismatch or job missing).
func (r *GORMRepository) TransitionStatus(ctx context.Context, id uint64, from, to Status) error {
	res := r.db.WithContext(ctx).
		Model(&QueryJob{}).
		Where("id = ? AND status = ?", id, string(from)).
		Updates(map[string]any{
			"status":     string(to),
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return fmt.Errorf("queryjob: transition %s→%s: %w", from, to, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("queryjob: transition %s→%s id=%d: %w", from, to, id, ErrStatusConflict)
	}
	return nil
}

// SetSucceeded atomically marks a job as succeeded (from `from` status) and
// writes the final execution metadata. Returns ErrStatusConflict when no rows
// were affected.
func (r *GORMRepository) SetSucceeded(
	ctx context.Context,
	id uint64,
	from Status,
	generatedSQL string,
	rowCount int64,
	durationMs int64,
	finishedAt time.Time,
) error {
	res := r.db.WithContext(ctx).
		Model(&QueryJob{}).
		Where("id = ? AND status = ?", id, string(from)).
		Updates(map[string]any{
			"status":                string(StatusSucceeded),
			"generated_sql":         sql.NullString{String: generatedSQL, Valid: true},
			"row_count":             sql.NullInt64{Int64: rowCount, Valid: true},
			"execution_duration_ms": sql.NullInt64{Int64: durationMs, Valid: true},
			"finished_at":           sql.NullTime{Time: finishedAt, Valid: true},
			"updated_at":            finishedAt,
		})
	if res.Error != nil {
		return fmt.Errorf("queryjob: set succeeded id=%d: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("queryjob: set succeeded id=%d: %w", id, ErrStatusConflict)
	}
	return nil
}

// SetFailed atomically marks a job as failed (from `from` status) and writes
// the safe error information. Returns ErrStatusConflict when no rows were
// affected.
func (r *GORMRepository) SetFailed(
	ctx context.Context,
	id uint64,
	from Status,
	errorCode string,
	errorMessage string,
	finishedAt time.Time,
) error {
	res := r.db.WithContext(ctx).
		Model(&QueryJob{}).
		Where("id = ? AND status = ?", id, string(from)).
		Updates(map[string]any{
			"status":        string(StatusFailed),
			"error_code":    sql.NullString{String: errorCode, Valid: true},
			"error_message": sql.NullString{String: errorMessage, Valid: true},
			"finished_at":   sql.NullTime{Time: finishedAt, Valid: true},
			"updated_at":    finishedAt,
		})
	if res.Error != nil {
		return fmt.Errorf("queryjob: set failed id=%d: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("queryjob: set failed id=%d: %w", id, ErrStatusConflict)
	}
	return nil
}
