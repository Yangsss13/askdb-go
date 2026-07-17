package queryjob

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Yangsss13/askdb-go/internal/llm"
	"github.com/Yangsss13/askdb-go/internal/queryresult"
	"github.com/Yangsss13/askdb-go/internal/sqlguard"
)

// ProcessService is the interface the Consumer uses to process a queued job.
// Declared here so the Consumer can be tested without a real WorkerService.
type ProcessService interface {
	Process(ctx context.Context, jobID uint64) error
}

// ResultWriter is the interface WorkerService uses to cache query results.
// Declared on the consuming side; queryresult.RedisStore implements it.
// SetRaw writes an already-serialized payload so the worker can serialize once,
// enforce the size limit on the exact bytes, and reuse them for the write.
type ResultWriter interface {
	SetRaw(ctx context.Context, jobID uint64, payload []byte, ttl time.Duration) error
}

// SQLGuard validates and normalizes SQL before execution. Declared on the
// consuming side; sqlguard.Guard implements it. Errors that wrap
// sqlguard.ErrRejected are deterministic rejections (business failures); any
// other error (e.g. ctx cancellation) is a runtime error.
type SQLGuard interface {
	Validate(ctx context.Context, input sqlguard.ValidateInput) (sqlguard.ValidateResult, error)
}

// GuardPolicy is the fixed validation policy applied to every generated query.
type GuardPolicy struct {
	AllowedDatabase string
	AllowedTables   []string
	MaxRows         int
}

// WorkerService executes a query job end-to-end: it reads the job from MySQL,
// calls the Fake LLM, runs the query, caches the result in Redis, and persists
// the terminal state in MySQL. It is called by the Consumer after a message is
// received from RabbitMQ.
type WorkerService struct {
	repo           Repository
	llm            LLMClient
	guard          SQLGuard
	policy         GuardPolicy
	exec           QueryExecutor
	store          ResultWriter
	queryTimeout   time.Duration
	resultTTL      time.Duration
	maxResultBytes int64
	now            func() time.Time
}

// NewWorkerService wires the worker-side dependencies.
func NewWorkerService(
	repo Repository,
	llmClient LLMClient,
	guard SQLGuard,
	policy GuardPolicy,
	exec QueryExecutor,
	store ResultWriter,
	queryTimeout time.Duration,
	resultTTL time.Duration,
	maxResultBytes int64,
) *WorkerService {
	return &WorkerService{
		repo:           repo,
		llm:            llmClient,
		guard:          guard,
		policy:         policy,
		exec:           exec,
		store:          store,
		queryTimeout:   queryTimeout,
		resultTTL:      resultTTL,
		maxResultBytes: maxResultBytes,
		now:            time.Now,
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

	// generating → validating
	if err := s.repo.TransitionStatus(ctx, jobID, StatusGenerating, StatusValidating); err != nil {
		return err
	}

	// Validate and normalize the generated SQL. The executor only ever receives
	// the guard's NormalizedSQL, never the raw LLM output.
	validated, err := s.guard.Validate(ctx, sqlguard.ValidateInput{
		SQL:             generatedSQL,
		AllowedDatabase: s.policy.AllowedDatabase,
		AllowedTables:   s.policy.AllowedTables,
		MaxRows:         s.policy.MaxRows,
	})
	if err != nil {
		if errors.Is(err, sqlguard.ErrRejected) {
			// Deterministic rejection: a business failure. Persist and ACK.
			// Do not log the raw SQL or the parser-level reason.
			now := s.now()
			return s.repo.SetFailed(ctx, jobID, StatusValidating, ErrCodeSQLValidationFailed, msgSQLValidationFailed, now)
		}
		// Runtime error (e.g. ctx cancellation): do not disguise as a rejection.
		// Return it so the Consumer applies normal ACK/NACK semantics.
		return err
	}

	// validating → executing
	if err := s.repo.TransitionStatus(ctx, jobID, StatusValidating, StatusExecuting); err != nil {
		return err
	}

	// Execute the normalized SQL against askdb_demo.
	execCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	start := s.now()
	columns, rows, execErr := s.exec.Execute(execCtx, validated.NormalizedSQL)
	durationMs := s.now().Sub(start).Milliseconds()

	if execErr != nil {
		now := s.now()
		return s.repo.SetFailed(ctx, jobID, StatusExecuting, ErrCodeQueryExecution, msgQueryExecution, now)
	}

	// Build the cache payload and enforce the result-size limit before writing
	// to Redis. Serialize once and reuse the bytes for the size check and write.
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

	payload, err := queryresult.Marshal(cached)
	if err != nil {
		// Marshalling a validated result should not fail; treat as a fatal error.
		return err
	}
	if int64(len(payload)) > s.maxResultBytes {
		// Result too large: do not write to Redis. Persist failed and ACK.
		// Do not log the rows.
		slog.Warn("worker: query result exceeds size limit", "job_id", jobID)
		return s.repo.SetFailed(ctx, jobID, StatusExecuting, ErrCodeResultTooLarge, msgResultTooLarge, now)
	}

	// Cache the full result in Redis before writing the terminal MySQL state.
	// If Redis fails, mark the job as failed rather than leaving clients with
	// a succeeded job they cannot retrieve the result from.
	if err := s.store.SetRaw(ctx, jobID, payload, s.resultTTL); err != nil {
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
	// generated_sql stores the normalized SQL that was actually executed, never
	// the raw LLM output. If this fails, the Redis key is orphaned but cleaned up
	// by TTL, and the Consumer must not ACK — return the error.
	return s.repo.SetSucceeded(ctx, jobID, StatusExecuting, validated.NormalizedSQL, int64(len(rows)), durationMs, now, &expiresAt)
}
