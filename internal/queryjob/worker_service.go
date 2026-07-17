package queryjob

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Yangsss13/askdb-go/internal/llm"
	"github.com/Yangsss13/askdb-go/internal/queryresult"
)

// ProcessService is the interface the Consumer uses to process a queued job.
// Declared here so the Consumer can be tested without a real WorkerService.
type ProcessService interface {
	Process(ctx context.Context, jobID uint64) error
}

// ResultWriter is the interface WorkerService uses to cache query results.
// Declared on the consuming side; queryresult.RedisStore implements it.
type ResultWriter interface {
	Set(ctx context.Context, result queryresult.CachedQueryResult, ttl time.Duration) error
}

// WorkerService executes a query job end-to-end: it reads the job from MySQL,
// calls the Fake LLM, runs the query, caches the result in Redis, and persists
// the terminal state in MySQL. It is called by the Consumer after a message is
// received from RabbitMQ.
type WorkerService struct {
	repo         Repository
	llm          LLMClient
	exec         QueryExecutor
	store        ResultWriter
	queryTimeout time.Duration
	resultTTL    time.Duration
	now          func() time.Time
}

// NewWorkerService wires the worker-side dependencies.
func NewWorkerService(
	repo Repository,
	llmClient LLMClient,
	exec QueryExecutor,
	store ResultWriter,
	queryTimeout time.Duration,
	resultTTL time.Duration,
) *WorkerService {
	return &WorkerService{
		repo:         repo,
		llm:          llmClient,
		exec:         exec,
		store:        store,
		queryTimeout: queryTimeout,
		resultTTL:    resultTTL,
		now:          time.Now,
	}
}

// Process executes the full query workflow for the given job ID.
//
// Return semantics (the Consumer maps these to ACK/NACK/fatal):
//   - nil            → job reached a terminal state and was persisted; ACK.
//   - ErrJobNotFound → job does not exist in MySQL; NACK no-requeue.
//   - ErrStatusConflict → unexpected status mismatch; treat as fatal (stop consumer).
//   - any other error → MySQL write failure; treat as fatal (stop consumer, do NOT ACK).
func (s *WorkerService) Process(ctx context.Context, jobID uint64) error {
	job, err := s.repo.FindByID(ctx, jobID)
	if err != nil {
		return err // ErrJobNotFound or DB error
	}

	// Duplicate or stale message: job already reached a terminal state.
	if Status(job.Status).IsTerminal() {
		return nil
	}

	// queued → generating
	if err := s.repo.TransitionStatus(ctx, jobID, StatusQueued, StatusGenerating); err != nil {
		return err
	}

	// Call Fake LLM.
	generatedSQL, err := s.llm.GenerateSQL(ctx, job.Question)
	if err != nil {
		now := s.now()
		code, msg := ErrCodeUnsupportedQuestion, msgUnsupportedQuestion
		if !errors.Is(err, llm.ErrUnsupportedQuestion) {
			code, msg = ErrCodeInternal, msgInternal
		}
		return s.repo.SetFailed(ctx, jobID, StatusGenerating, code, msg, now)
	}

	// generating → executing
	if err := s.repo.TransitionStatus(ctx, jobID, StatusGenerating, StatusExecuting); err != nil {
		return err
	}

	// Execute query against askdb_demo.
	execCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	start := s.now()
	columns, rows, execErr := s.exec.Execute(execCtx, generatedSQL)
	durationMs := s.now().Sub(start).Milliseconds()

	if execErr != nil {
		now := s.now()
		return s.repo.SetFailed(ctx, jobID, StatusExecuting, ErrCodeQueryExecution, msgQueryExecution, now)
	}

	// Cache the full result in Redis before writing the terminal MySQL state.
	// If Redis fails, mark the job as failed rather than leaving clients with
	// a succeeded job they cannot retrieve the result from.
	now := s.now()
	expiresAt := now.Add(s.resultTTL)
	cached := queryresult.CachedQueryResult{
		JobID:     jobID,
		Columns:   columns,
		Rows:      rows,
		RowCount:  int64(len(rows)),
		CachedAt:  now,
		ExpiresAt: expiresAt,
	}

	if err := s.store.Set(ctx, cached, s.resultTTL); err != nil {
		// Redis write failed. Do not log rows (may contain sensitive business data).
		slog.Error("worker: failed to cache query result", "job_id", jobID)
		failErr := s.repo.SetFailed(ctx, jobID, StatusExecuting, ErrCodeResultCacheFailed, msgResultCacheFailed, now)
		if failErr != nil {
			// SetFailed also failed: return the error so Consumer does not ACK.
			return failErr
		}
		// SetFailed succeeded: return nil so Consumer ACKs.
		return nil
	}

	// Redis write succeeded. Now atomically persist the terminal success state.
	// If this fails, the Redis key is orphaned but will be cleaned up by TTL.
	// The Consumer must not ACK — return the error.
	return s.repo.SetSucceeded(ctx, jobID, StatusExecuting, generatedSQL, int64(len(rows)), durationMs, now, &expiresAt)
}
