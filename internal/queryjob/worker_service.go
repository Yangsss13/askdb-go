package queryjob

import (
	"context"
	"errors"
	"time"

	"github.com/Yangsss13/askdb-go/internal/llm"
)

// ProcessService is the interface the Consumer uses to process a queued job.
// Declared here so the Consumer can be tested without a real WorkerService.
type ProcessService interface {
	Process(ctx context.Context, jobID uint64) error
}

// WorkerService executes a query job end-to-end: it reads the job from MySQL,
// calls the Fake LLM, runs the query, and persists the terminal state.
// It is called by the Consumer after a message is received from RabbitMQ.
type WorkerService struct {
	repo         Repository
	llm          LLMClient
	exec         QueryExecutor
	queryTimeout time.Duration
	now          func() time.Time
}

// NewWorkerService wires the worker-side dependencies.
func NewWorkerService(repo Repository, llmClient LLMClient, exec QueryExecutor, queryTimeout time.Duration) *WorkerService {
	return &WorkerService{
		repo:         repo,
		llm:          llmClient,
		exec:         exec,
		queryTimeout: queryTimeout,
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
	_, rows, execErr := s.exec.Execute(execCtx, generatedSQL)
	durationMs := s.now().Sub(start).Milliseconds()

	if execErr != nil {
		now := s.now()
		return s.repo.SetFailed(ctx, jobID, StatusExecuting, ErrCodeQueryExecution, msgQueryExecution, now)
	}

	// executing → succeeded
	now := s.now()
	return s.repo.SetSucceeded(ctx, jobID, StatusExecuting, generatedSQL, int64(len(rows)), durationMs, now)
}
