package queryjob

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Yangsss13/askdb-go/internal/llm"
	"github.com/Yangsss13/askdb-go/internal/queryresult"
	"github.com/Yangsss13/askdb-go/internal/sqlguard"
)

// --- hand-written fakes ---

type fakeRepo struct {
	created   *QueryJob
	nextID    uint64
	createErr error

	findResult *QueryJob
	findErr    error

	transitions         []string // "from->to" log
	transitionErr       error
	transitionErrOnCall int // 0 = always, 1 = first call, etc.
	transitionCallCount int

	setSucceededCalled bool
	setSucceededErr    error
	succeededSQL       string

	setFailedCalled bool
	setFailedErr    error
	setFailedCode   string
	setFailedFrom   Status
}

func (f *fakeRepo) Create(_ context.Context, job *QueryJob) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.nextID++
	job.ID = f.nextID
	f.created = job
	return nil
}

func (f *fakeRepo) FindByID(_ context.Context, id uint64) (*QueryJob, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.findResult, nil
}

func (f *fakeRepo) TransitionStatus(_ context.Context, id uint64, from, to Status) error {
	f.transitionCallCount++
	key := string(from) + "->" + string(to)
	f.transitions = append(f.transitions, key)
	if f.transitionErr != nil {
		if f.transitionErrOnCall == 0 || f.transitionCallCount == f.transitionErrOnCall {
			return f.transitionErr
		}
	}
	// Reflect transition in findResult so subsequent FindByID calls see new state.
	if f.findResult != nil && f.findResult.ID == id {
		f.findResult.Status = string(to)
	}
	return nil
}

func (f *fakeRepo) SetSucceeded(_ context.Context, id uint64, _ Status, generatedSQL string, _, _ int64, _ time.Time, _ *time.Time) error {
	f.setSucceededCalled = true
	f.succeededSQL = generatedSQL
	return f.setSucceededErr
}

func (f *fakeRepo) SetFailed(_ context.Context, _ uint64, from Status, code, _ string, _ time.Time) error {
	f.setFailedCalled = true
	f.setFailedCode = code
	f.setFailedFrom = from
	return f.setFailedErr
}

type fakeDataSourceChecker struct {
	exists bool
	err    error
}

func (f *fakeDataSourceChecker) ExistsForUser(_ context.Context, _, _ uint64) (bool, error) {
	return f.exists, f.err
}

// defaultDSChecker is a convenient default that accepts all data sources.
var defaultDSChecker = &fakeDataSourceChecker{exists: true}

type fakePublisher struct {
	published   []uint64
	publishErr  error
	closeCalled bool
}

func (f *fakePublisher) Publish(_ context.Context, jobID uint64) error {
	if f.publishErr != nil {
		return f.publishErr
	}
	f.published = append(f.published, jobID)
	return nil
}

func (f *fakePublisher) Close() error {
	f.closeCalled = true
	return nil
}

type fakeLLM struct {
	sql string
	err error
}

func (f *fakeLLM) GenerateSQL(_ context.Context, _ string) (string, error) {
	return f.sql, f.err
}

type fakeExecutor struct {
	columns  []string
	rows     [][]any
	err      error
	called   bool
	gotQuery string
}

func (f *fakeExecutor) Execute(_ context.Context, query string) ([]string, [][]any, error) {
	f.called = true
	f.gotQuery = query
	return f.columns, f.rows, f.err
}

// --- Service (API side) tests ---

func TestService_Submit_Success(t *testing.T) {
	repo := &fakeRepo{}
	pub := &fakePublisher{}
	svc := NewService(repo, pub, defaultDSChecker)

	job, err := svc.Submit(context.Background(), 1, "  查询所有商品  ", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Question != "查询所有商品" {
		t.Errorf("question not trimmed: %q", job.Question)
	}
	if job.Status != string(StatusQueued) {
		t.Errorf("expected queued, got %s", job.Status)
	}
	// Must have transitioned pending→queued before publishing.
	if len(repo.transitions) == 0 || repo.transitions[0] != "pending->queued" {
		t.Errorf("expected pending->queued transition, got %v", repo.transitions)
	}
	if len(pub.published) != 1 || pub.published[0] != repo.created.ID {
		t.Errorf("expected publish of job_id=%d, got %v", repo.created.ID, pub.published)
	}
}

func TestService_Submit_InvalidQuestion(t *testing.T) {
	repo := &fakeRepo{}
	pub := &fakePublisher{}
	svc := NewService(repo, pub, defaultDSChecker)

	for _, q := range []string{"", "   "} {
		_, err := svc.Submit(context.Background(), 1, q, 1)
		var svcErr *ServiceError
		if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeInvalidQuestion {
			t.Errorf("question %q: expected INVALID_QUESTION, got %v", q, err)
		}
	}
	if repo.created != nil {
		t.Error("no job should be created on validation failure")
	}
	if len(pub.published) != 0 {
		t.Error("must not publish on validation failure")
	}
}

func TestService_Submit_CreateFailure(t *testing.T) {
	repo := &fakeRepo{createErr: errors.New("db down")}
	pub := &fakePublisher{}
	svc := NewService(repo, pub, defaultDSChecker)

	_, err := svc.Submit(context.Background(), 1, "查询所有商品", 1)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeInternal {
		t.Errorf("expected INTERNAL_ERROR, got %v", err)
	}
	if len(pub.published) != 0 {
		t.Error("must not publish when create fails")
	}
}

func TestService_Submit_TransitionQueuedFailure(t *testing.T) {
	repo := &fakeRepo{transitionErr: errors.New("lock fail")}
	pub := &fakePublisher{}
	svc := NewService(repo, pub, defaultDSChecker)

	_, err := svc.Submit(context.Background(), 1, "查询所有商品", 1)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeInternal {
		t.Errorf("expected INTERNAL_ERROR, got %v", err)
	}
	if len(pub.published) != 0 {
		t.Error("must not publish when queued transition fails")
	}
}

func TestService_Submit_PublishFailure(t *testing.T) {
	repo := &fakeRepo{}
	pub := &fakePublisher{publishErr: errors.New("broker unavailable")}
	svc := NewService(repo, pub, defaultDSChecker)

	_, err := svc.Submit(context.Background(), 1, "查询所有商品", 1)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodePublishFailed {
		t.Errorf("expected PUBLISH_FAILED, got %v", err)
	}
	if !repo.setFailedCalled {
		t.Error("job must be marked failed when publish fails")
	}
	if repo.setFailedCode != ErrCodePublishFailed {
		t.Errorf("failed error_code: got %q, want %q", repo.setFailedCode, ErrCodePublishFailed)
	}
	// Error message must not mention broker internals.
	if svcErr.Message != msgPublishFailed {
		t.Errorf("error message must be safe: %q", svcErr.Message)
	}
}

func TestService_Get_NotFound(t *testing.T) {
	repo := &fakeRepo{findErr: ErrJobNotFound}
	svc := NewService(repo, &fakePublisher{}, defaultDSChecker)

	_, err := svc.Get(context.Background(), 1, 99)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound, got %v", err)
	}
}

func TestService_Get_Success(t *testing.T) {
	want := &QueryJob{ID: 7, Status: string(StatusSucceeded), UserID: sql.NullInt64{Int64: 7, Valid: true}}
	repo := &fakeRepo{findResult: want}
	svc := NewService(repo, &fakePublisher{}, defaultDSChecker)

	got, err := svc.Get(context.Background(), 7, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != 7 {
		t.Errorf("expected job 7, got %d", got.ID)
	}
}

// fakeResultWriter is a hand-written fake of ResultWriter.
type fakeResultWriter struct {
	setCalled bool
	setErr    error
	callOrder []string // records "redis" or "mysql" for ordering assertions
}

func (f *fakeResultWriter) SetRaw(_ context.Context, _ uint64, _ []byte, _ time.Duration) error {
	f.setCalled = true
	f.callOrder = append(f.callOrder, "redis")
	return f.setErr
}

// fakeGuard is a hand-written fake of SQLGuard. By default it accepts the SQL,
// echoing it back as NormalizedSQL. Set err to simulate a rejection or runtime
// error, or normalizedSQL to assert the executor receives the guard's output.
type fakeGuard struct {
	err           error
	normalizedSQL string
	called        bool
}

func (f *fakeGuard) Validate(_ context.Context, input sqlguard.ValidateInput) (sqlguard.ValidateResult, error) {
	f.called = true
	if f.err != nil {
		return sqlguard.ValidateResult{}, f.err
	}
	out := f.normalizedSQL
	if out == "" {
		out = input.SQL
	}
	return sqlguard.ValidateResult{NormalizedSQL: out, StatementType: "SELECT", Limit: input.MaxRows}, nil
}

// fakeRepoWithOrder wraps fakeRepo to record SetSucceeded call order.
type fakeRepoWithOrder struct {
	fakeRepo
	writer *fakeResultWriter
}

func (f *fakeRepoWithOrder) SetSucceeded(ctx context.Context, id uint64, from Status, sqlStr string, rowCount, durationMs int64, finishedAt time.Time, resultExpiresAt *time.Time) error {
	if f.writer != nil {
		f.writer.callOrder = append(f.writer.callOrder, "mysql")
	}
	return f.fakeRepo.SetSucceeded(ctx, id, from, sqlStr, rowCount, durationMs, finishedAt, resultExpiresAt)
}

// --- WorkerService tests ---

// testPolicy is the fixed guard policy used in worker tests.
var testPolicy = GuardPolicy{
	AllowedTables: []string{"products", "orders", "order_items"},
	MaxRows:       100,
}

func newWorkerSvc(repo Repository, l *fakeLLM, e *fakeExecutor) *WorkerService {
	store := &fakeResultWriter{}
	return NewWorkerService(repo, l, &fakeGuard{}, testPolicy, e, store, 2*time.Second, 15*time.Minute, 1048576, "askdb_demo", nil)
}

func newWorkerSvcWithStore(repo Repository, l *fakeLLM, e *fakeExecutor, store ResultWriter) *WorkerService {
	return NewWorkerService(repo, l, &fakeGuard{}, testPolicy, e, store, 2*time.Second, 15*time.Minute, 1048576, "askdb_demo", nil)
}

func newWorkerSvcFull(repo Repository, l *fakeLLM, g SQLGuard, e *fakeExecutor, store ResultWriter, maxResultBytes int64) *WorkerService {
	return NewWorkerService(repo, l, g, testPolicy, e, store, 2*time.Second, 15*time.Minute, maxResultBytes, "askdb_demo", nil)
}

func TestWorkerService_Process_Success(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	repo := &fakeRepo{findResult: job}
	l := &fakeLLM{sql: "SELECT id FROM products LIMIT 100"}
	e := &fakeExecutor{columns: []string{"id"}, rows: [][]any{{int64(1)}, {int64(2)}}}
	svc := newWorkerSvc(repo, l, e)

	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"queued->generating", "generating->validating", "validating->executing"}
	for i, tr := range want {
		if i >= len(repo.transitions) || repo.transitions[i] != tr {
			t.Errorf("transition[%d]: got %v, want %q", i, repo.transitions, tr)
		}
	}
	if !repo.setSucceededCalled {
		t.Error("expected SetSucceeded to be called")
	}
}

func TestWorkerService_Process_TerminalJob_ACK(t *testing.T) {
	for _, status := range []Status{StatusSucceeded, StatusFailed} {
		job := &QueryJob{ID: 1, Status: string(status)}
		repo := &fakeRepo{findResult: job}
		svc := newWorkerSvc(repo, &fakeLLM{}, &fakeExecutor{})

		err := svc.Process(context.Background(), 1)
		if err != nil {
			t.Errorf("status %s: expected nil (ACK), got %v", status, err)
		}
		if len(repo.transitions) != 0 {
			t.Errorf("status %s: must not transition terminal job", status)
		}
	}
}

func TestWorkerService_Process_JobNotFound(t *testing.T) {
	repo := &fakeRepo{findErr: ErrJobNotFound}
	svc := newWorkerSvc(repo, &fakeLLM{}, &fakeExecutor{})

	err := svc.Process(context.Background(), 99)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound, got %v", err)
	}
}

func TestWorkerService_Process_UnsupportedQuestion(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "删除所有", Status: string(StatusQueued)}
	repo := &fakeRepo{findResult: job}
	l := &fakeLLM{err: llm.ErrUnsupportedQuestion}
	svc := newWorkerSvc(repo, l, &fakeExecutor{})

	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repo.setFailedCalled || repo.setFailedCode != ErrCodeUnsupportedQuestion {
		t.Errorf("expected UNSUPPORTED_QUESTION, got code=%q", repo.setFailedCode)
	}
}

func TestWorkerService_Process_QueryExecutionFailure(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	repo := &fakeRepo{findResult: job}
	l := &fakeLLM{sql: "SELECT 1"}
	e := &fakeExecutor{err: errors.New("driver: connection refused")}
	svc := newWorkerSvc(repo, l, e)

	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repo.setFailedCalled || repo.setFailedCode != ErrCodeQueryExecution {
		t.Errorf("expected QUERY_EXECUTION_FAILED, got code=%q", repo.setFailedCode)
	}
}

func TestWorkerService_Process_FinalUpdateFailure_NoACK(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	repo := &fakeRepo{
		findResult:      job,
		setSucceededErr: errors.New("db write failed"),
	}
	l := &fakeLLM{sql: "SELECT 1"}
	e := &fakeExecutor{columns: []string{"id"}, rows: [][]any{{int64(1)}}}
	svc := newWorkerSvc(repo, l, e)

	err := svc.Process(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error when SetSucceeded fails (must not ACK)")
	}
}

// --- Phase 4 WorkerService tests ---

func TestWorkerService_Process_RedisBeforeMySQL(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	store := &fakeResultWriter{}
	repo := &fakeRepoWithOrder{
		fakeRepo: fakeRepo{findResult: job},
		writer:   store,
	}
	l := &fakeLLM{sql: "SELECT id FROM products LIMIT 100"}
	e := &fakeExecutor{columns: []string{"id"}, rows: [][]any{{int64(1)}}}
	svc := newWorkerSvcWithStore(repo, l, e, store)

	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.callOrder) < 2 {
		t.Fatalf("expected at least 2 calls, got %v", store.callOrder)
	}
	if store.callOrder[0] != "redis" {
		t.Errorf("first call must be redis, got %q", store.callOrder[0])
	}
	if store.callOrder[1] != "mysql" {
		t.Errorf("second call must be mysql, got %q", store.callOrder[1])
	}
}

func TestWorkerService_Process_RedisSetFails_TaskMarkedFailed(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	store := &fakeResultWriter{setErr: errors.New("redis unavailable")}
	repo := &fakeRepo{findResult: job}
	l := &fakeLLM{sql: "SELECT 1"}
	e := &fakeExecutor{columns: []string{"id"}, rows: [][]any{{int64(1)}}}
	svc := newWorkerSvcWithStore(repo, l, e, store)

	// Process must return nil (Consumer ACKs) even when Redis fails, as long as
	// SetFailed succeeds.
	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("expected nil when Redis fails and SetFailed succeeds, got %v", err)
	}
	if !repo.setFailedCalled {
		t.Error("SetFailed must be called when Redis write fails")
	}
	if repo.setFailedCode != ErrCodeResultCacheFailed {
		t.Errorf("expected RESULT_CACHE_FAILED, got %q", repo.setFailedCode)
	}
	if repo.setSucceededCalled {
		t.Error("SetSucceeded must not be called when Redis write fails")
	}
}

func TestWorkerService_Process_RedisSetFails_SetFailedAlsoFails_NoACK(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	store := &fakeResultWriter{setErr: errors.New("redis unavailable")}
	repo := &fakeRepo{
		findResult:   job,
		setFailedErr: errors.New("db write failed"),
	}
	l := &fakeLLM{sql: "SELECT 1"}
	e := &fakeExecutor{columns: []string{"id"}, rows: [][]any{{int64(1)}}}
	svc := newWorkerSvcWithStore(repo, l, e, store)

	err := svc.Process(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error when both Redis and SetFailed fail (must not ACK)")
	}
}

func TestWorkerService_Process_RedisSucceeds_MySQLFails_NoACK(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	store := &fakeResultWriter{}
	repo := &fakeRepo{
		findResult:      job,
		setSucceededErr: errors.New("db write failed"),
	}
	l := &fakeLLM{sql: "SELECT 1"}
	e := &fakeExecutor{columns: []string{"id"}, rows: [][]any{{int64(1)}}}
	svc := newWorkerSvcWithStore(repo, l, e, store)

	err := svc.Process(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error when Redis succeeds but MySQL SetSucceeded fails (must not ACK)")
	}
	if !store.setCalled {
		t.Error("Redis Set must have been called")
	}
}

func TestWorkerService_Process_QueryFails_NoRedisWrite(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	store := &fakeResultWriter{}
	repo := &fakeRepo{findResult: job}
	l := &fakeLLM{sql: "SELECT 1"}
	e := &fakeExecutor{err: errors.New("query failed")}
	svc := newWorkerSvcWithStore(repo, l, e, store)

	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.setCalled {
		t.Error("Redis Set must not be called when query execution fails")
	}
}

func TestWorkerService_Process_UnsupportedQuestion_NoRedisWrite(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "删除所有", Status: string(StatusQueued)}
	store := &fakeResultWriter{}
	repo := &fakeRepo{findResult: job}
	l := &fakeLLM{err: llm.ErrUnsupportedQuestion}
	svc := newWorkerSvcWithStore(repo, l, &fakeExecutor{}, store)

	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.setCalled {
		t.Error("Redis Set must not be called when question is unsupported")
	}
}

// --- ResultService tests ---

// fakeResultReader is a hand-written fake of ResultReader.
type fakeResultReader struct {
	getResult *queryresult.CachedQueryResult
	getErr    error
	getCalled bool
}

func (f *fakeResultReader) Get(_ context.Context, _ uint64) (*queryresult.CachedQueryResult, error) {
	f.getCalled = true
	return f.getResult, f.getErr
}

func succeededJob(resultExpiresAt *time.Time) *QueryJob {
	job := &QueryJob{ID: 5, Status: string(StatusSucceeded)}
	job.GeneratedSQL.String = "SELECT 1"
	job.GeneratedSQL.Valid = true
	// owner=5; ownership checks in service tests use callerID=5.
	job.UserID = sql.NullInt64{Int64: 5, Valid: true}
	if resultExpiresAt != nil {
		job.ResultExpiresAt.Time = *resultExpiresAt
		job.ResultExpiresAt.Valid = true
	}
	return job
}

func TestResultService_GetResult_PendingTask(t *testing.T) {
	job := &QueryJob{ID: 1, Status: string(StatusQueued), UserID: sql.NullInt64{Int64: 1, Valid: true}}
	repo := &fakeRepo{findResult: job}
	svc := NewResultService(repo, &fakeResultReader{})

	_, err := svc.GetResult(context.Background(), 1, 1)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeResultNotReady {
		t.Errorf("expected RESULT_NOT_READY, got %v", err)
	}
}

func TestResultService_GetResult_FailedTask(t *testing.T) {
	store := &fakeResultReader{}
	job := &QueryJob{ID: 1, Status: string(StatusFailed), UserID: sql.NullInt64{Int64: 1, Valid: true}}
	repo := &fakeRepo{findResult: job}
	svc := NewResultService(repo, store)

	_, err := svc.GetResult(context.Background(), 1, 1)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeQueryJobFailed {
		t.Errorf("expected QUERY_JOB_FAILED, got %v", err)
	}
	// Must not have read Redis.
	if store.getResult != nil {
		t.Error("must not read Redis when job is failed")
	}
}

func TestResultService_GetResult_Succeeded_NullExpiresAt(t *testing.T) {
	repo := &fakeRepo{findResult: succeededJob(nil)}
	svc := NewResultService(repo, &fakeResultReader{})

	_, err := svc.GetResult(context.Background(), 5, 5)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeResultUnavailable {
		t.Errorf("expected RESULT_UNAVAILABLE when result_expires_at is NULL, got %v", err)
	}
}

func TestResultService_GetResult_Succeeded_CacheHit(t *testing.T) {
	exp := time.Now().UTC().Add(10 * time.Minute)
	repo := &fakeRepo{findResult: succeededJob(&exp)}
	cached := &queryresult.CachedQueryResult{
		JobID: 5, Columns: []string{"id"}, Rows: [][]any{{int64(1)}}, RowCount: 1,
	}
	store := &fakeResultReader{getResult: cached}
	svc := NewResultService(repo, store)

	got, err := svc.GetResult(context.Background(), 5, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.JobID != 5 {
		t.Errorf("expected JobID=5, got %d", got.JobID)
	}
}

func TestResultService_GetResult_CacheExpired(t *testing.T) {
	past := time.Now().UTC().Add(-time.Minute) // already expired
	repo := &fakeRepo{findResult: succeededJob(&past)}
	store := &fakeResultReader{getErr: queryresult.ErrResultNotFound}
	svc := NewResultService(repo, store)

	_, err := svc.GetResult(context.Background(), 5, 5)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeResultExpired {
		t.Errorf("expected RESULT_EXPIRED, got %v", err)
	}
}

func TestResultService_GetResult_CachePrematureLoss(t *testing.T) {
	future := time.Now().UTC().Add(10 * time.Minute) // not yet expired
	repo := &fakeRepo{findResult: succeededJob(&future)}
	store := &fakeResultReader{getErr: queryresult.ErrResultNotFound}
	svc := NewResultService(repo, store)

	_, err := svc.GetResult(context.Background(), 5, 5)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeResultUnavailable {
		t.Errorf("expected RESULT_UNAVAILABLE on premature cache loss, got %v", err)
	}
}

func TestResultService_GetResult_StoreUnavailable(t *testing.T) {
	exp := time.Now().UTC().Add(10 * time.Minute)
	repo := &fakeRepo{findResult: succeededJob(&exp)}
	store := &fakeResultReader{getErr: queryresult.ErrResultStoreUnavailable}
	svc := NewResultService(repo, store)

	_, err := svc.GetResult(context.Background(), 5, 5)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeResultStoreUnavail {
		t.Errorf("expected RESULT_STORE_UNAVAILABLE, got %v", err)
	}
}

func TestResultService_GetResult_CorruptedCache(t *testing.T) {
	exp := time.Now().UTC().Add(10 * time.Minute)
	repo := &fakeRepo{findResult: succeededJob(&exp)}
	store := &fakeResultReader{getErr: queryresult.ErrResultCorrupted}
	svc := NewResultService(repo, store)

	_, err := svc.GetResult(context.Background(), 5, 5)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeResultCorrupted {
		t.Errorf("expected RESULT_CORRUPTED, got %v", err)
	}
}

func TestResultService_GetResult_NotFound(t *testing.T) {
	repo := &fakeRepo{findErr: ErrJobNotFound}
	svc := NewResultService(repo, &fakeResultReader{})

	_, err := svc.GetResult(context.Background(), 1, 99)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound, got %v", err)
	}
}

// TestResultService_GetResult_NoRedisKey_NotExposed verifies that the Redis key
// never appears in the ServiceError message.
func TestResultService_GetResult_NoRedisKey_NotExposed(t *testing.T) {
	exp := time.Now().UTC().Add(10 * time.Minute)
	repo := &fakeRepo{findResult: succeededJob(&exp)}
	store := &fakeResultReader{getErr: queryresult.ErrResultNotFound}
	svc := NewResultService(repo, store)

	_, err := svc.GetResult(context.Background(), 5, 5)
	if err == nil {
		t.Fatal("expected error")
	}
	var svcErr *ServiceError
	if errors.As(err, &svcErr) {
		if strings.Contains(svcErr.Message, "askdb:query-result") {
			t.Errorf("error message must not contain Redis key: %q", svcErr.Message)
		}
	}
}

// TestFakeRepo_SetSucceeded_NullExpiresAt ensures the test fake handles
// nil resultExpiresAt without panicking (used in several tests above).
func TestFakeRepo_SetSucceeded_NullExpiresAt(t *testing.T) {
	repo := &fakeRepo{}
	err := repo.SetSucceeded(context.Background(), 1, StatusExecuting, "SELECT 1", 1, 10, time.Now(), nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- Phase 5 SQL Guard WorkerService tests ---

// TestWorkerService_Process_GuardTransitions verifies the validating state is
// entered and left on the success path.
func TestWorkerService_Process_GuardTransitions(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	repo := &fakeRepo{findResult: job}
	l := &fakeLLM{sql: "SELECT id FROM products"}
	e := &fakeExecutor{columns: []string{"id"}, rows: [][]any{{int64(1)}}}
	store := &fakeResultWriter{}
	svc := newWorkerSvcFull(repo, l, &fakeGuard{normalizedSQL: "SELECT `id` FROM `products` LIMIT 100"}, e, store, 1048576)

	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"queued->generating", "generating->validating", "validating->executing"}
	if len(repo.transitions) != len(want) {
		t.Fatalf("transitions: got %v, want %v", repo.transitions, want)
	}
	for i, tr := range want {
		if repo.transitions[i] != tr {
			t.Errorf("transition[%d]: got %q, want %q", i, repo.transitions[i], tr)
		}
	}
}

// TestWorkerService_Process_ExecutesNormalizedSQL verifies the executor receives
// the guard's NormalizedSQL, not the raw LLM output, and that generated_sql is
// persisted as the normalized form.
func TestWorkerService_Process_ExecutesNormalizedSQL(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	repo := &fakeRepo{findResult: job}
	l := &fakeLLM{sql: "select id from products"}
	e := &fakeExecutor{columns: []string{"id"}, rows: [][]any{{int64(1)}}}
	store := &fakeResultWriter{}
	normalized := "SELECT `id` FROM `products` LIMIT 100"
	svc := newWorkerSvcFull(repo, l, &fakeGuard{normalizedSQL: normalized}, e, store, 1048576)

	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.gotQuery != normalized {
		t.Errorf("executor received %q, want normalized %q", e.gotQuery, normalized)
	}
	if repo.succeededSQL != normalized {
		t.Errorf("persisted generated_sql=%q, want normalized %q", repo.succeededSQL, normalized)
	}
}

// TestWorkerService_Process_GuardRejects verifies a guard rejection marks the job
// failed with SQL_VALIDATION_FAILED, does not call the executor or the store,
// and returns nil (ACK).
func TestWorkerService_Process_GuardRejects(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	repo := &fakeRepo{findResult: job}
	l := &fakeLLM{sql: "DROP TABLE products"}
	e := &fakeExecutor{}
	store := &fakeResultWriter{}
	guard := &fakeGuard{err: fmt.Errorf("%w: disallowed", sqlguard.ErrRejected)}
	svc := newWorkerSvcFull(repo, l, guard, e, store, 1048576)

	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("expected nil (ACK) on rejection, got %v", err)
	}
	if e.called {
		t.Error("executor must not be called when guard rejects")
	}
	if store.setCalled {
		t.Error("store must not be called when guard rejects")
	}
	if !repo.setFailedCalled || repo.setFailedCode != ErrCodeSQLValidationFailed {
		t.Errorf("expected SQL_VALIDATION_FAILED, got code=%q", repo.setFailedCode)
	}
	if repo.setFailedFrom != StatusValidating {
		t.Errorf("SetFailed from=%q, want validating", repo.setFailedFrom)
	}
}

// TestWorkerService_Process_GuardRejects_SetFailedFails_NoACK verifies that if
// the guard rejects but persisting the failure fails, Process returns an error
// (no ACK).
func TestWorkerService_Process_GuardRejects_SetFailedFails_NoACK(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	repo := &fakeRepo{findResult: job, setFailedErr: errors.New("db write failed")}
	l := &fakeLLM{sql: "DROP TABLE products"}
	guard := &fakeGuard{err: fmt.Errorf("%w: disallowed", sqlguard.ErrRejected)}
	svc := newWorkerSvcFull(repo, l, guard, &fakeExecutor{}, &fakeResultWriter{}, 1048576)

	if err := svc.Process(context.Background(), 1); err == nil {
		t.Fatal("expected error when SetFailed fails after rejection (must not ACK)")
	}
}

// TestWorkerService_Process_GuardRuntimeError_NoACK verifies a non-rejection
// guard error (e.g. ctx cancellation) is returned as-is (not disguised as a
// business failure).
func TestWorkerService_Process_GuardRuntimeError_NoACK(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	repo := &fakeRepo{findResult: job}
	guard := &fakeGuard{err: context.Canceled}
	svc := newWorkerSvcFull(repo, l0(), guard, &fakeExecutor{}, &fakeResultWriter{}, 1048576)

	err := svc.Process(context.Background(), 1)
	if err == nil {
		t.Fatal("expected runtime error to be returned")
	}
	if repo.setFailedCalled {
		t.Error("must not persist failed on a runtime (non-rejection) guard error")
	}
}

// TestWorkerService_Process_ResultTooLarge verifies that a result exceeding
// MAX_RESULT_BYTES is rejected with RESULT_TOO_LARGE and never written to Redis.
func TestWorkerService_Process_ResultTooLarge(t *testing.T) {
	job := &QueryJob{ID: 1, Question: "查询所有商品", Status: string(StatusQueued)}
	repo := &fakeRepo{findResult: job}
	l := &fakeLLM{sql: "SELECT id FROM products"}
	// A wide row that will exceed a tiny byte limit once serialized.
	e := &fakeExecutor{columns: []string{"data"}, rows: [][]any{{strings.Repeat("x", 500)}}}
	store := &fakeResultWriter{}
	svc := newWorkerSvcFull(repo, l, &fakeGuard{}, e, store, 64) // 64-byte limit

	if err := svc.Process(context.Background(), 1); err != nil {
		t.Fatalf("expected nil (ACK) on RESULT_TOO_LARGE, got %v", err)
	}
	if store.setCalled {
		t.Error("store must not be called when result exceeds size limit")
	}
	if !repo.setFailedCalled || repo.setFailedCode != ErrCodeResultTooLarge {
		t.Errorf("expected RESULT_TOO_LARGE, got code=%q", repo.setFailedCode)
	}
}

// l0 returns a fakeLLM producing a benign SELECT for tests that don't care.
func l0() *fakeLLM { return &fakeLLM{sql: "SELECT id FROM products"} }

// --- Phase 6A: ownership (IDOR) tests ---

func TestService_Submit_SetsUserID(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo, &fakePublisher{}, defaultDSChecker)

	_, err := svc.Submit(context.Background(), 42, "查询所有商品", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.created == nil {
		t.Fatal("expected Create to be called")
	}
	if !repo.created.UserID.Valid || uint64(repo.created.UserID.Int64) != 42 {
		t.Errorf("UserID: got %+v, want {Int64:42 Valid:true}", repo.created.UserID)
	}
}

func TestService_Get_WrongOwner_NotFound(t *testing.T) {
	job := &QueryJob{ID: 3, Status: string(StatusSucceeded), UserID: sql.NullInt64{Int64: 10, Valid: true}}
	repo := &fakeRepo{findResult: job}
	svc := NewService(repo, &fakePublisher{}, defaultDSChecker)

	// callerID=99 != job.UserID=10 → must return ErrJobNotFound
	_, err := svc.Get(context.Background(), 99, 3)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound on wrong owner, got %v", err)
	}
}

func TestService_Get_NullUserID_NotFound(t *testing.T) {
	// Legacy row with NULL user_id must be inaccessible to any caller.
	job := &QueryJob{ID: 3, Status: string(StatusSucceeded)}
	repo := &fakeRepo{findResult: job}
	svc := NewService(repo, &fakePublisher{}, defaultDSChecker)

	_, err := svc.Get(context.Background(), 10, 3)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound for legacy NULL user_id, got %v", err)
	}
}

func TestResultService_GetResult_WrongOwner_NoRedis(t *testing.T) {
	store := &fakeResultReader{}
	exp := time.Now().UTC().Add(10 * time.Minute)
	job := succeededJob(&exp) // UserID=5
	repo := &fakeRepo{findResult: job}
	svc := NewResultService(repo, store)

	// callerID=99 != job.UserID=5 → must return 404 without reading Redis
	_, err := svc.GetResult(context.Background(), 99, 5)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound on wrong owner, got %v", err)
	}
	if store.getCalled {
		t.Error("Redis must not be accessed when ownership check fails")
	}
}

func TestResultService_GetResult_NullUserID_NoRedis(t *testing.T) {
	store := &fakeResultReader{}
	exp := time.Now().UTC().Add(10 * time.Minute)
	// Legacy job: no UserID set.
	job := &QueryJob{ID: 5, Status: string(StatusSucceeded)}
	job.GeneratedSQL = sql.NullString{String: "SELECT 1", Valid: true}
	job.ResultExpiresAt = sql.NullTime{Time: exp, Valid: true}
	repo := &fakeRepo{findResult: job}
	svc := NewResultService(repo, store)

	_, err := svc.GetResult(context.Background(), 5, 5)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound for legacy NULL user_id, got %v", err)
	}
	if store.getCalled {
		t.Error("Redis must not be accessed for legacy NULL user_id jobs")
	}
}

// --- Phase 6B: data source validation tests ---

func TestService_Submit_MissingDataSource(t *testing.T) {
	repo := &fakeRepo{}
	pub := &fakePublisher{}
	svc := NewService(repo, pub, defaultDSChecker)

	_, err := svc.Submit(context.Background(), 1, "查询所有商品", 0)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeMissingDataSource {
		t.Errorf("expected MISSING_DATA_SOURCE, got %v", err)
	}
	if repo.created != nil {
		t.Error("no job should be created when data_source_id is missing")
	}
	if len(pub.published) != 0 {
		t.Error("must not publish when data source is missing")
	}
}

func TestService_Submit_DataSourceNotOwned(t *testing.T) {
	repo := &fakeRepo{}
	pub := &fakePublisher{}
	dsCheck := &fakeDataSourceChecker{exists: false}
	svc := NewService(repo, pub, dsCheck)

	_, err := svc.Submit(context.Background(), 1, "查询所有商品", 1)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeDataSourceNotFound {
		t.Errorf("expected DATA_SOURCE_NOT_FOUND, got %v", err)
	}
	if repo.created != nil {
		t.Error("no job should be created when data source does not belong to caller")
	}
	if len(pub.published) != 0 {
		t.Error("must not publish when data source ownership check fails")
	}
}
