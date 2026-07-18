package queryjob

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Yangsss13/askdb-go/internal/queryresult"
)

// Repository persists query jobs. The interface is declared here, on the
// consuming side; the GORM implementation lives in repository.go.
type Repository interface {
	Create(ctx context.Context, job *QueryJob) error
	FindByID(ctx context.Context, id uint64) (*QueryJob, error)
	// TransitionStatus conditionally updates status from `from` to `to`.
	// Returns ErrStatusConflict when no rows were affected.
	TransitionStatus(ctx context.Context, id uint64, from, to Status) error
	// SetSucceeded atomically writes the success terminal state.
	// resultExpiresAt is the Redis cache expiry time; pass nil if no cache was written.
	// Returns ErrStatusConflict when no rows were affected.
	SetSucceeded(ctx context.Context, id uint64, from Status, generatedSQL string, rowCount, durationMs int64, finishedAt time.Time, resultExpiresAt *time.Time) error
	// SetFailed atomically writes the failure terminal state.
	// Returns ErrStatusConflict when no rows were affected.
	SetFailed(ctx context.Context, id uint64, from Status, errorCode, errorMessage string, finishedAt time.Time) error
}

// DataSourceChecker verifies data-source ownership. Declared on the consuming
// side to keep the queryjob package free of datasource imports.
// ExistsForUser returns true when a non-deleted source with dataSourceID is
// owned by userID. Returns false for missing, cross-user, or soft-deleted rows.
type DataSourceChecker interface {
	ExistsForUser(ctx context.Context, dataSourceID, userID uint64) (bool, error)
}

// LLMClient turns a natural-language question into SQL. Implementations must
// return llm.ErrUnsupportedQuestion for questions they do not recognize.
type LLMClient interface {
	GenerateSQL(ctx context.Context, question string) (string, error)
}

// QueryExecutor runs a read-only query and returns columns (in order) and rows.
type QueryExecutor interface {
	Execute(ctx context.Context, query string) (columns []string, rows [][]any, err error)
}

// ResultReader is the interface used by ResultService to read cached results.
// Declared on the consuming side; queryresult.RedisStore implements it.
type ResultReader interface {
	Get(ctx context.Context, jobID uint64) (*queryresult.CachedQueryResult, error)
}

// Service handles the API side of the query job lifecycle: validate the
// question, create the job, update it to queued, and publish a message.
// The worker side is handled by WorkerService.
type Service struct {
	repo    Repository
	pub     Publisher
	dsCheck DataSourceChecker
	now     func() time.Time
}

// NewService wires the API-side service dependencies.
func NewService(repo Repository, pub Publisher, dsCheck DataSourceChecker) *Service {
	return &Service{repo: repo, pub: pub, dsCheck: dsCheck, now: time.Now}
}

// Submit validates the question and dataSourceID, creates a pending job owned
// by userID, advances it to queued, and publishes a message.
//
// Order of operations:
//  1. Validate question → 400
//  2. Verify dataSourceID ownership → 400 (missing) or 404 (wrong user/deleted)
//  3. Create job (pending) → 500
//  4. TransitionStatus pending→queued → 500
//  5. Publish → on fail: SetFailed, return 503
func (s *Service) Submit(ctx context.Context, userID uint64, question string, dataSourceID uint64) (*QueryJob, error) {
	trimmed := strings.TrimSpace(question)
	if trimmed == "" || len([]rune(trimmed)) > maxQuestionLen {
		return nil, newServiceError(ErrCodeInvalidQuestion, "question must be 1-500 characters")
	}
	if dataSourceID == 0 {
		return nil, newServiceError(ErrCodeMissingDataSource, "data_source_id is required")
	}

	// Verify the caller owns the data source (prevents IDOR on submit).
	exists, err := s.dsCheck.ExistsForUser(ctx, dataSourceID, userID)
	if err != nil {
		return nil, newServiceError(ErrCodeInternal, msgInternal)
	}
	if !exists {
		return nil, newServiceError(ErrCodeDataSourceNotFound, "data source not found")
	}

	now := s.now()
	job := &QueryJob{
		Question:     trimmed,
		Status:       string(StatusPending),
		CreatedAt:    now,
		UpdatedAt:    now,
		UserID:       sql.NullInt64{Int64: int64(userID), Valid: true},
		DataSourceID: sql.NullInt64{Int64: int64(dataSourceID), Valid: true},
	}
	if err := s.repo.Create(ctx, job); err != nil {
		return nil, newServiceError(ErrCodeInternal, msgInternal)
	}

	// Atomically advance to queued before publishing.
	if err := s.repo.TransitionStatus(ctx, job.ID, StatusPending, StatusQueued); err != nil {
		return nil, newServiceError(ErrCodeInternal, msgInternal)
	}
	job.Status = string(StatusQueued)

	if err := s.pub.Publish(ctx, job.ID); err != nil {
		finished := s.now()
		_ = s.repo.SetFailed(ctx, job.ID, StatusQueued, ErrCodePublishFailed, msgPublishFailed, finished)
		return nil, newServiceError(ErrCodePublishFailed, msgPublishFailed)
	}

	return job, nil
}

// Get returns the persisted job by ID for the given caller.
// Returns ErrJobNotFound when the job is absent or belongs to a different user
// (including NULL legacy rows), preventing IDOR leakage.
func (s *Service) Get(ctx context.Context, callerID uint64, id uint64) (*QueryJob, error) {
	job, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ownsJob(job, callerID) {
		return nil, ErrJobNotFound
	}
	return job, nil
}

// ResultService retrieves cached query results. It always checks MySQL first
// to ensure Redis is never used as the source of truth for job status.
type ResultService struct {
	repo  Repository
	store ResultReader
	now   func() time.Time
}

// NewResultService wires the result-fetch dependencies.
func NewResultService(repo Repository, store ResultReader) *ResultService {
	return &ResultService{repo: repo, store: store, now: time.Now}
}

// GetResult fetches the cached query result for a succeeded job owned by callerID.
// Ownership is checked against MySQL before any Redis access is attempted.
func (s *ResultService) GetResult(ctx context.Context, callerID uint64, jobID uint64) (*queryresult.CachedQueryResult, error) {
	job, err := s.repo.FindByID(ctx, jobID)
	if err != nil {
		return nil, err // ErrJobNotFound propagates to handler
	}

	// Ownership check: NULL user_id (legacy rows) and mismatched owner both return 404.
	if !ownsJob(job, callerID) {
		return nil, ErrJobNotFound
	}

	status := Status(job.Status)

	if status == StatusFailed {
		return nil, newServiceError(ErrCodeQueryJobFailed, "query job failed")
	}

	if !status.IsTerminal() {
		// pending, queued, generating, executing
		return nil, newServiceError(ErrCodeResultNotReady, "result is not ready yet")
	}

	// status == succeeded from here.

	// result_expires_at NULL means this job succeeded before caching was
	// introduced, or the cache write failed.
	if !job.ResultExpiresAt.Valid {
		return nil, newServiceError(ErrCodeResultUnavailable, "result is not available")
	}

	result, err := s.store.Get(ctx, jobID)
	if err != nil {
		return nil, s.mapStoreError(err, job)
	}
	return result, nil
}

// mapStoreError translates queryresult sentinel errors into ServiceErrors,
// using the job's result_expires_at to distinguish expiry from loss.
func (s *ResultService) mapStoreError(err error, job *QueryJob) *ServiceError {
	switch {
	case errors.Is(err, queryresult.ErrResultNotFound):
		if s.now().Before(job.ResultExpiresAt.Time) {
			return newServiceError(ErrCodeResultUnavailable, "result is not available")
		}
		return newServiceError(ErrCodeResultExpired, "result has expired")
	case errors.Is(err, queryresult.ErrResultCorrupted):
		return newServiceError(ErrCodeResultCorrupted, "result data is corrupted")
	default:
		return newServiceError(ErrCodeResultStoreUnavail, "result store is unavailable")
	}
}

// ownsJob reports whether callerID is the owner of job.
// Returns false for legacy rows (NULL user_id) and mismatched owners.
func ownsJob(job *QueryJob, callerID uint64) bool {
	return job.UserID.Valid && uint64(job.UserID.Int64) == callerID
}
