package queryjob

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

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

// Update writes the full job record back to storage.
func (r *GORMRepository) Update(ctx context.Context, job *QueryJob) error {
	if err := r.db.WithContext(ctx).Save(job).Error; err != nil {
		return fmt.Errorf("queryjob: update: %w", err)
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
