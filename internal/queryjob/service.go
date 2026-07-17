package queryjob

import (
	"context"
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
	repo Repository
	pub  Publisher
	now  func() time.Time
}

// NewService wires the API-side service dependencies.
func NewService(repo Repository, pub Publisher) *Service {
	return &Service{repo: repo, pub: pub, now: time.Now}
}

// Submit validates the question, creates a pending job, conditionally advances
// it to queued, and publishes a message. It returns the job snapshot on
// success (HTTP 202) or a ServiceError on any failure.
//
// Order of operations (prevents the Worker from winning a race against the API):
//
//  1. Validate question → 400 on fail, no job created
//  2. Create job (pending) → 500 on fail, no message published
//  3. TransitionStatus pending→queued → 500 on fail, no message published
//  4. Publish → on fail: SetFailed queued→failed, return 503
//  5. Return job snapshot (status=queued)
func (s *Service) Submit(ctx context.Context, question string) (*QueryJob, error) {
	trimmed := strings.TrimSpace(question)
	if trimmed == "" || len([]rune(trimmed)) > maxQuestionLen {
		return nil, newServiceError(ErrCodeInvalidQuestion, "question must be 1-500 characters")
	}

	now := s.now()
	job := &QueryJob{
		Question:  trimmed,
		Status:    string(StatusPending),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.Create(ctx, job); err != nil {
		return nil, newServiceError(ErrCodeInternal, msgInternal)
	}

	// Atomically advance to queued before publishing. This prevents the Worker
	// from updating a job that the API has not yet finished setting up.
	if err := s.repo.TransitionStatus(ctx, job.ID, StatusPending, StatusQueued); err != nil {
		return nil, newServiceError(ErrCodeInternal, msgInternal)
	}
	job.Status = string(StatusQueued)

	// Publish the message. On failure, mark the job as failed so the client
	// can observe the outcome via GET.
	if err := s.pub.Publish(ctx, job.ID); err != nil {
		finished := s.now()
		_ = s.repo.SetFailed(ctx, job.ID, StatusQueued, ErrCodePublishFailed, msgPublishFailed, finished)
		return nil, newServiceError(ErrCodePublishFailed, msgPublishFailed)
	}

	return job, nil
}

// Get returns the persisted job by ID, or ErrJobNotFound when absent.
func (s *Service) Get(ctx context.Context, id uint64) (*QueryJob, error) {
	return s.repo.FindByID(ctx, id)
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

// GetResult fetches the cached query result for a succeeded job.
// It checks MySQL for the job status before reading Redis.
func (s *ResultService) GetResult(ctx context.Context, jobID uint64) (*queryresult.CachedQueryResult, error) {
	job, err := s.repo.FindByID(ctx, jobID)
	if err != nil {
		return nil, err // ErrJobNotFound propagates to handler
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
