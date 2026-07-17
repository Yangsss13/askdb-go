package queryjob

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Yangsss13/askdb-go/internal/llm"
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

	setFailedCalled bool
	setFailedErr    error
	setFailedCode   string
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

func (f *fakeRepo) SetSucceeded(_ context.Context, id uint64, _ Status, _ string, _, _ int64, _ time.Time) error {
	f.setSucceededCalled = true
	return f.setSucceededErr
}

func (f *fakeRepo) SetFailed(_ context.Context, _ uint64, _ Status, code, _ string, _ time.Time) error {
	f.setFailedCalled = true
	f.setFailedCode = code
	return f.setFailedErr
}

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
	columns []string
	rows    [][]any
	err     error
}

func (f *fakeExecutor) Execute(_ context.Context, _ string) ([]string, [][]any, error) {
	return f.columns, f.rows, f.err
}

// --- Service (API side) tests ---

func TestService_Submit_Success(t *testing.T) {
	repo := &fakeRepo{}
	pub := &fakePublisher{}
	svc := NewService(repo, pub)

	job, err := svc.Submit(context.Background(), "  查询所有商品  ")
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
	svc := NewService(repo, pub)

	for _, q := range []string{"", "   "} {
		_, err := svc.Submit(context.Background(), q)
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
	svc := NewService(repo, pub)

	_, err := svc.Submit(context.Background(), "查询所有商品")
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
	svc := NewService(repo, pub)

	_, err := svc.Submit(context.Background(), "查询所有商品")
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
	svc := NewService(repo, pub)

	_, err := svc.Submit(context.Background(), "查询所有商品")
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
	svc := NewService(repo, &fakePublisher{})

	_, err := svc.Get(context.Background(), 99)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound, got %v", err)
	}
}

func TestService_Get_Success(t *testing.T) {
	want := &QueryJob{ID: 7, Status: string(StatusSucceeded)}
	repo := &fakeRepo{findResult: want}
	svc := NewService(repo, &fakePublisher{})

	got, err := svc.Get(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != 7 {
		t.Errorf("expected job 7, got %d", got.ID)
	}
}

// --- WorkerService tests ---

func newWorkerSvc(repo *fakeRepo, l *fakeLLM, e *fakeExecutor) *WorkerService {
	return NewWorkerService(repo, l, e, 2*time.Second)
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
	want := []string{"queued->generating", "generating->executing"}
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
