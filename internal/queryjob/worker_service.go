package queryjob

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Yangsss13/askdb-go/internal/llm"
	"github.com/Yangsss13/askdb-go/internal/queryresult"
	"github.com/Yangsss13/askdb-go/internal/sqlguard"
)

// ResultWriter is the interface WorkerService uses to cache query results.
type ResultWriter interface {
	SetRaw(ctx context.Context, jobID uint64, payload []byte, ttl time.Duration) error
}

// SQLGuard validates and normalizes SQL before execution.
type SQLGuard interface {
	Validate(ctx context.Context, input sqlguard.ValidateInput) (sqlguard.ValidateResult, error)
}

// DataSourceOpener opens a single-use QueryExecutor and SchemaReader for a given data source.
type DataSourceOpener interface {
	OpenForJob(ctx context.Context, dataSourceID uint64) (dbName string, exec QueryExecutor, schema llm.SchemaReader, closer func(), err error)
}

// GuardPolicy is the fixed validation policy applied to every generated query.
type GuardPolicy struct {
	AllowedTables []string
	MaxRows       int
}

// WorkerService executes a query job end-to-end: reads the job from MySQL,
// calls the LLM (after reading the target-DB schema), runs the query, caches
// the result in Redis, and persists the terminal state. On retryable errors
// it schedules a retry via RabbitMQ.
type WorkerService struct {
	repo               Repository
	llm                LLMClient
	guard              SQLGuard
	policy             GuardPolicy
	exec               QueryExecutor
	staticAllowedDB    string
	staticSchemaReader llm.SchemaReader
	dsOpener           DataSourceOpener
	store              ResultWriter
	retryPub           RetryPublisher
	queryTimeout       time.Duration
	resultTTL          time.Duration
	maxResultBytes     int64
	maxRetries         int
	retryDelay         time.Duration
	now                func() time.Time
}

// NewWorkerService wires the worker-side dependencies.
func NewWorkerService(
	repo Repository,
	llmClient LLMClient,
	guard SQLGuard,
	policy GuardPolicy,
	exec QueryExecutor,
	store ResultWriter,
	retryPub RetryPublisher,
	queryTimeout time.Duration,
	resultTTL time.Duration,
	maxResultBytes int64,
	staticAllowedDB string,
	staticSchemaReader llm.SchemaReader,
	dsOpener DataSourceOpener,
	maxRetries int,
	retryDelay time.Duration,
) *WorkerService {
	return &WorkerService{
		repo:               repo,
		llm:                llmClient,
		guard:              guard,
		policy:             policy,
		exec:               exec,
		staticAllowedDB:    staticAllowedDB,
		staticSchemaReader: staticSchemaReader,
		dsOpener:           dsOpener,
		store:              store,
		retryPub:           retryPub,
		queryTimeout:       queryTimeout,
		resultTTL:          resultTTL,
		maxResultBytes:     maxResultBytes,
		maxRetries:         maxRetries,
		retryDelay:         retryDelay,
		now:                time.Now,
	}
}

// Process executes the full query workflow for the given request.
//
// Return semantics (the Consumer maps these to ACK/NACK):
//   - nil              → terminal state persisted; ACK.
//   - ErrRetryScheduled → retry published and DB updated; ACK.
//   - ErrDLQScheduled  → DLQ published and SetFailed done; ACK.
//   - ErrJobNotFound   → Consumer routes to DLQ.
//   - any other error  → treat as fatal; Consumer NACKs.
func (s *WorkerService) Process(ctx context.Context, req ProcessRequest) error {
	job, err := s.repo.FindByID(ctx, req.JobID)
	if err != nil {
		return err // ErrJobNotFound or DB error
	}

	// Duplicate or stale message: job already reached a terminal state.
	if Status(job.Status).IsTerminal() {
		return nil
	}

	// Determine which status to transition FROM. On the first attempt (or after
	// a retry), the job may be queued or retrying.
	var fromStatus Status
	switch Status(job.Status) {
	case StatusQueued:
		fromStatus = StatusQueued
	case StatusRetrying:
		// Validate that the incoming attempt matches the DB expectation to
		// guard against stale retries (old attempt delivered out of order).
		if int(job.AttemptCount) != req.Attempt {
			slog.Warn("worker: stale retry attempt ignored",
				"job_id", req.JobID,
				"db_attempt", job.AttemptCount,
				"msg_attempt", req.Attempt)
			// Return nil to ACK without re-executing; PM is already completed or
			// will be cleaned up on the next valid retry.
			return nil
		}
		fromStatus = StatusRetrying
	case StatusGenerating, StatusValidating, StatusExecuting:
		// The job is stuck in an intermediate state because a previous worker
		// crashed after transitioning the status but before persisting the
		// terminal state. Treat this as a retryable infra fault: schedule a
		// retry (or DLQ if max attempts reached) using the current status as
		// the CAS `from` value. The validTransitions map allows
		// generating/validating/executing → retrying.
		slog.Warn("worker: job stuck in intermediate state, scheduling retry",
			"job_id", req.JobID, "status", job.Status, "attempt", req.Attempt)
		return s.scheduleRetryOrFail(ctx, req, Status(job.Status),
			fmt.Errorf("stuck in %s after worker crash", job.Status))
	default:
		// Unknown/unexpected status — surface as a conflict so the consumer NACKs.
		return ErrStatusConflict
	}

	// fromStatus → generating
	if err := s.repo.TransitionStatus(ctx, req.JobID, fromStatus, StatusGenerating); err != nil {
		return err
	}

	return s.runPipeline(ctx, job, req, StatusGenerating)
}

// runPipeline executes the generating→validating→executing→succeeded stages.
// currentStatus tracks where the job is so SetRetrying uses the right FROM value.
func (s *WorkerService) runPipeline(ctx context.Context, job *QueryJob, req ProcessRequest, currentStatus Status) error {
	// Resolve executor, schema reader, and allowed database for this job.
	var exec QueryExecutor
	var allowedDB string
	var schemaReader llm.SchemaReader
	if !job.DataSourceID.Valid {
		exec = s.exec
		allowedDB = s.staticAllowedDB
		schemaReader = s.staticSchemaReader
	} else {
		if s.dsOpener == nil {
			slog.Error("worker: job has DataSourceID but dsOpener is nil", "job_id", req.JobID)
			return s.scheduleRetryOrFail(ctx, req, currentStatus, errors.New("dsOpener is nil"))
		}
		dbName, dynExec, dynSchema, closer, openErr := s.dsOpener.OpenForJob(ctx, uint64(job.DataSourceID.Int64))
		if openErr != nil {
			slog.Error("worker: failed to open dynamic data source", "job_id", req.JobID)
			return s.scheduleRetryOrFail(ctx, req, currentStatus, openErr)
		}
		defer closer()
		exec = dynExec
		allowedDB = dbName
		schemaReader = dynSchema
	}

	// Read schema from the target database before calling the LLM.
	var schema llm.SchemaInfo
	if schemaReader != nil {
		var schemaErr error
		schema, schemaErr = schemaReader.ReadSchema(ctx)
		if schemaErr != nil {
			if errors.Is(schemaErr, context.Canceled) {
				return schemaErr
			}
			return s.scheduleRetryOrFail(ctx, req, currentStatus, schemaErr)
		}
	}

	// Generate SQL via LLM.
	generatedSQL, err := s.llm.GenerateSQL(ctx, job.Question, schema)
	if err != nil {
		now := s.now()
		switch {
		case errors.Is(err, llm.ErrUnsupportedQuestion):
			// Deterministic: will not succeed on retry.
			return s.repo.SetFailed(ctx, req.JobID, currentStatus, ErrCodeUnsupportedQuestion, msgUnsupportedQuestion, now)
		case llm.IsDeterministic(err):
			// Other deterministic LLM failure (malformed response, non-compliant, etc.).
			return s.repo.SetFailed(ctx, req.JobID, currentStatus, ErrCodeLLMFailed, msgLLMFailed, now)
		case errors.Is(err, context.Canceled):
			// Shutdown/caller cancellation must not create a new retry record.
			return err
		case llm.IsRetryable(err):
			// Transient failure: network, timeout, rate-limit, server error.
			return s.scheduleRetryOrFail(ctx, req, currentStatus, err)
		default:
			// Preserve the existing infrastructure-failure default: unknown
			// provider errors are retried without inspecting their text.
			return s.scheduleRetryOrFail(ctx, req, currentStatus, err)
		}
	}

	// generating → validating
	if err := s.repo.TransitionStatus(ctx, req.JobID, StatusGenerating, StatusValidating); err != nil {
		return err
	}
	currentStatus = StatusValidating

	validated, err := s.guard.Validate(ctx, sqlguard.ValidateInput{
		SQL:             generatedSQL,
		AllowedDatabase: allowedDB,
		AllowedTables:   s.policy.AllowedTables,
		MaxRows:         s.policy.MaxRows,
	})
	if err != nil {
		if errors.Is(err, sqlguard.ErrRejected) {
			now := s.now()
			return s.repo.SetFailed(ctx, req.JobID, currentStatus, ErrCodeSQLValidationFailed, msgSQLValidationFailed, now)
		}
		// Runtime error (e.g. ctx cancellation).
		return s.scheduleRetryOrFail(ctx, req, currentStatus, err)
	}

	// validating → executing
	if err := s.repo.TransitionStatus(ctx, req.JobID, StatusValidating, StatusExecuting); err != nil {
		return err
	}
	currentStatus = StatusExecuting

	execCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	start := s.now()
	columns, rows, execErr := exec.Execute(execCtx, validated.NormalizedSQL)
	durationMs := s.now().Sub(start).Milliseconds()

	if execErr != nil {
		now := s.now()
		if isRetryableError(execErr) {
			return s.scheduleRetryOrFail(ctx, req, currentStatus, execErr)
		}
		return s.repo.SetFailed(ctx, req.JobID, currentStatus, ErrCodeQueryExecution, msgQueryExecution, now)
	}

	now := s.now()
	expiresAt := now.Add(s.resultTTL)
	cached := queryresult.CachedQueryResult{
		JobID:     req.JobID,
		Columns:   columns,
		Rows:      rows,
		RowCount:  int64(len(rows)),
		CachedAt:  now,
		ExpiresAt: expiresAt,
	}

	payload, err := queryresult.Marshal(cached)
	if err != nil {
		return err
	}
	if int64(len(payload)) > s.maxResultBytes {
		slog.Warn("worker: query result exceeds size limit", "job_id", req.JobID)
		return s.repo.SetFailed(ctx, req.JobID, currentStatus, ErrCodeResultTooLarge, msgResultTooLarge, now)
	}

	if err := s.store.SetRaw(ctx, req.JobID, payload, s.resultTTL); err != nil {
		// Redis write failure is a transient infrastructure error; schedule retry.
		slog.Error("worker: failed to cache query result", "job_id", req.JobID)
		return s.scheduleRetryOrFail(ctx, req, currentStatus, err)
	}

	return s.repo.SetSucceeded(ctx, req.JobID, currentStatus, validated.NormalizedSQL, int64(len(rows)), durationMs, now, &expiresAt)
}

// scheduleRetryOrFail publishes a retry message if attempt < maxRetries;
// otherwise publishes to the DLQ and marks the job as failed.
func (s *WorkerService) scheduleRetryOrFail(ctx context.Context, req ProcessRequest, fromStatus Status, cause error) error {
	nextAttempt := req.Attempt + 1
	if nextAttempt <= s.maxRetries {
		return s.scheduleRetry(ctx, req, fromStatus, uint8(nextAttempt))
	}
	return s.scheduleDLQ(ctx, req, fromStatus)
}

// scheduleRetry publishes attempt+1 to the retry queue (with confirm), then
// updates the job to retrying state. Returns ErrRetryScheduled on success so
// the Consumer marks PM retry_scheduled and ACKs.
func (s *WorkerService) scheduleRetry(ctx context.Context, req ProcessRequest, fromStatus Status, nextAttempt uint8) error {
	nextRetryAt := s.now().Add(s.retryDelay)
	if err := s.retryPub.PublishRetry(ctx, req.JobID, req.MessageID, int(nextAttempt)); err != nil {
		// Retry publish failed: return error so Consumer NACKs.
		slog.Error("worker: retry publish failed", "job_id", req.JobID, "attempt", nextAttempt, "err", err)
		return err
	}
	// Publish confirmed. Now update the DB. If this fails, the retry message
	// is already in the queue (duplicate possible), but the job won't be lost.
	if err := s.repo.SetRetrying(ctx, req.JobID, fromStatus, nextAttempt, nextRetryAt); err != nil {
		slog.Error("worker: set retrying failed after publish", "job_id", req.JobID, "err", err)
		// DB update failed: return error so Consumer NACKs and the message is
		// requeued. The already-published retry message may cause a duplicate,
		// but the idempotency layer will handle it.
		return err
	}
	slog.Info("worker: retry scheduled", "job_id", req.JobID, "next_attempt", nextAttempt)
	return ErrRetryScheduled
}

// scheduleDLQ publishes to the DLQ (with confirm), then marks the job as failed.
// Returns ErrDLQScheduled so the Consumer marks PM completed and ACKs.
func (s *WorkerService) scheduleDLQ(ctx context.Context, req ProcessRequest, fromStatus Status) error {
	if err := s.retryPub.PublishDLQ(ctx, req.JobID, req.MessageID, req.Attempt); err != nil {
		slog.Error("worker: dlq publish failed", "job_id", req.JobID, "err", err)
		return err
	}
	// DLQ confirmed. Persist the terminal failed state.
	now := s.now()
	if err := s.repo.SetFailed(ctx, req.JobID, fromStatus, ErrCodeMaxRetriesExceeded, msgMaxRetriesExceeded, now); err != nil {
		slog.Error("worker: set failed after dlq failed", "job_id", req.JobID, "err", err)
		// DLQ is already published (at-least-once); return error so Consumer NACKs
		// and retries. The DLQ message may be duplicated but is documented.
		return err
	}
	slog.Info("worker: job sent to dlq", "job_id", req.JobID, "attempt", req.Attempt)
	return ErrDLQScheduled
}
