package queryjob

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Yangsss13/askdb-go/internal/llm"
)

// Repository persists query jobs. The interface is declared here, on the
// consuming side; the GORM implementation lives in repository.go.
type Repository interface {
	Create(ctx context.Context, job *QueryJob) error
	Update(ctx context.Context, job *QueryJob) error
	FindByID(ctx context.Context, id uint64) (*QueryJob, error)
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

// QueryResult is the service-layer outcome of a Submit call. It carries the
// persisted job snapshot plus the (non-persisted) result set on success.
type QueryResult struct {
	Job     *QueryJob
	Columns []string
	Rows    [][]any
}

// Service orchestrates the synchronous query flow: create job, generate SQL,
// execute against askdb_demo, and persist the terminal state.
type Service struct {
	repo         Repository
	llm          LLMClient
	exec         QueryExecutor
	queryTimeout time.Duration
	now          func() time.Time
}

// NewService wires the service dependencies. queryTimeout bounds query execution.
func NewService(repo Repository, llmClient LLMClient, exec QueryExecutor, queryTimeout time.Duration) *Service {
	return &Service{
		repo:         repo,
		llm:          llmClient,
		exec:         exec,
		queryTimeout: queryTimeout,
		now:          time.Now,
	}
}

// Submit validates the question, creates a job, generates SQL, executes it, and
// persists the terminal state.
//
// On validation failure it returns (nil, *ServiceError) without creating a job.
// On unsupported question or query failure it persists a failed job and returns
// (result, *ServiceError) so the caller can report both the job and the code.
// On success it returns (result, nil).
func (s *Service) Submit(ctx context.Context, question string) (*QueryResult, error) {
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

	// Generate SQL (logical state: generating).
	generatedSQL, err := s.llm.GenerateSQL(ctx, trimmed)
	if err != nil {
		if errors.Is(err, llm.ErrUnsupportedQuestion) {
			return s.fail(ctx, job, ErrCodeUnsupportedQuestion, msgUnsupportedQuestion)
		}
		return s.fail(ctx, job, ErrCodeInternal, msgInternal)
	}
	job.GeneratedSQL = sql.NullString{String: generatedSQL, Valid: true}

	// Execute (logical state: executing).
	execCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	start := s.now()
	columns, rows, err := s.exec.Execute(execCtx, generatedSQL)
	durationMs := s.now().Sub(start).Milliseconds()
	if err != nil {
		return s.fail(ctx, job, ErrCodeQueryExecution, msgQueryExecution)
	}

	// Success.
	finished := s.now()
	job.Status = string(StatusSucceeded)
	job.RowCount = sql.NullInt64{Int64: int64(len(rows)), Valid: true}
	job.ExecutionDurationMs = sql.NullInt64{Int64: durationMs, Valid: true}
	job.UpdatedAt = finished
	job.FinishedAt = sql.NullTime{Time: finished, Valid: true}
	if err := s.repo.Update(ctx, job); err != nil {
		return nil, newServiceError(ErrCodeInternal, msgInternal)
	}

	return &QueryResult{Job: job, Columns: columns, Rows: rows}, nil
}

// fail persists the job as failed with the given code/message and returns the
// snapshot alongside a matching ServiceError.
func (s *Service) fail(ctx context.Context, job *QueryJob, code, message string) (*QueryResult, error) {
	finished := s.now()
	job.Status = string(StatusFailed)
	job.ErrorCode = sql.NullString{String: code, Valid: true}
	job.ErrorMessage = sql.NullString{String: message, Valid: true}
	job.UpdatedAt = finished
	job.FinishedAt = sql.NullTime{Time: finished, Valid: true}
	if err := s.repo.Update(ctx, job); err != nil {
		return nil, newServiceError(ErrCodeInternal, msgInternal)
	}
	return &QueryResult{Job: job}, newServiceError(code, message)
}

// Get returns the persisted job by ID, or ErrJobNotFound when absent.
func (s *Service) Get(ctx context.Context, id uint64) (*QueryJob, error) {
	return s.repo.FindByID(ctx, id)
}
