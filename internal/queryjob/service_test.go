package queryjob

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Yangsss13/askdb-go/internal/llm"
)

// --- hand-written fakes (no mock framework, no sqlite) ---

type fakeRepo struct {
	created    *QueryJob
	updated    *QueryJob
	nextID     uint64
	createErr  error
	updateErr  error
	findResult *QueryJob
	findErr    error
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

func (f *fakeRepo) Update(_ context.Context, job *QueryJob) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = job
	return nil
}

func (f *fakeRepo) FindByID(_ context.Context, id uint64) (*QueryJob, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.findResult, nil
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

func newTestService(repo Repository, l LLMClient, e QueryExecutor) *Service {
	return NewService(repo, l, e, 2*time.Second)
}

// --- tests ---

func TestSubmit_Success(t *testing.T) {
	repo := &fakeRepo{}
	l := &fakeLLM{sql: "SELECT id FROM products LIMIT 100"}
	e := &fakeExecutor{columns: []string{"id"}, rows: [][]any{{int64(1)}, {int64(2)}}}
	svc := newTestService(repo, l, e)

	result, err := svc.Submit(context.Background(), "  查询所有商品  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Job.Status != string(StatusSucceeded) {
		t.Errorf("expected succeeded, got %s", result.Job.Status)
	}
	if result.Job.Question != "查询所有商品" {
		t.Errorf("expected trimmed question, got %q", result.Job.Question)
	}
	if !result.Job.RowCount.Valid || result.Job.RowCount.Int64 != 2 {
		t.Errorf("expected row_count=2, got %+v", result.Job.RowCount)
	}
	if !result.Job.ExecutionDurationMs.Valid {
		t.Error("expected execution_duration_ms to be set")
	}
	if !result.Job.FinishedAt.Valid {
		t.Error("expected finished_at to be set")
	}
	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows in result, got %d", len(result.Rows))
	}
}

func TestSubmit_InvalidQuestion(t *testing.T) {
	repo := &fakeRepo{}
	svc := newTestService(repo, &fakeLLM{}, &fakeExecutor{})

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
}

func TestSubmit_TooLong(t *testing.T) {
	repo := &fakeRepo{}
	svc := newTestService(repo, &fakeLLM{}, &fakeExecutor{})

	long := make([]rune, 501)
	for i := range long {
		long[i] = 'a'
	}
	_, err := svc.Submit(context.Background(), string(long))
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeInvalidQuestion {
		t.Errorf("expected INVALID_QUESTION, got %v", err)
	}
	if repo.created != nil {
		t.Error("no job should be created for too-long question")
	}
}

func TestSubmit_Unsupported(t *testing.T) {
	repo := &fakeRepo{}
	l := &fakeLLM{err: llm.ErrUnsupportedQuestion}
	svc := newTestService(repo, l, &fakeExecutor{})

	result, err := svc.Submit(context.Background(), "unknown")
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeUnsupportedQuestion {
		t.Fatalf("expected UNSUPPORTED_QUESTION, got %v", err)
	}
	if result == nil || result.Job.Status != string(StatusFailed) {
		t.Fatalf("expected a failed job, got %+v", result)
	}
	if !result.Job.ErrorCode.Valid || result.Job.ErrorCode.String != ErrCodeUnsupportedQuestion {
		t.Errorf("expected error_code set, got %+v", result.Job.ErrorCode)
	}
	if !result.Job.FinishedAt.Valid {
		t.Error("failed job must set finished_at")
	}
}

func TestSubmit_ExecutionFailure(t *testing.T) {
	repo := &fakeRepo{}
	l := &fakeLLM{sql: "SELECT id FROM products LIMIT 100"}
	e := &fakeExecutor{err: errors.New("driver: connection refused at 10.0.0.5:3306")}
	svc := newTestService(repo, l, e)

	result, err := svc.Submit(context.Background(), "查询所有商品")
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeQueryExecution {
		t.Fatalf("expected QUERY_EXECUTION_FAILED, got %v", err)
	}
	if result.Job.Status != string(StatusFailed) {
		t.Errorf("expected failed job, got %s", result.Job.Status)
	}
	// The safe message must not leak the underlying driver/address detail.
	if svcErr.Message != msgQueryExecution {
		t.Errorf("error message must be safe, got %q", svcErr.Message)
	}
	if result.Job.ErrorMessage.String != msgQueryExecution {
		t.Errorf("persisted error message must be safe, got %q", result.Job.ErrorMessage.String)
	}
}

func TestSubmit_CreateFailure(t *testing.T) {
	repo := &fakeRepo{createErr: errors.New("db down")}
	svc := newTestService(repo, &fakeLLM{}, &fakeExecutor{})

	_, err := svc.Submit(context.Background(), "查询所有商品")
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeInternal {
		t.Errorf("expected INTERNAL_ERROR, got %v", err)
	}
}

func TestSubmit_UpdateFailureOnSuccess(t *testing.T) {
	repo := &fakeRepo{updateErr: errors.New("update failed")}
	l := &fakeLLM{sql: "SELECT id FROM products LIMIT 100"}
	e := &fakeExecutor{columns: []string{"id"}, rows: [][]any{{int64(1)}}}
	svc := newTestService(repo, l, e)

	_, err := svc.Submit(context.Background(), "查询所有商品")
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != ErrCodeInternal {
		t.Errorf("expected INTERNAL_ERROR on update failure, got %v", err)
	}
}

func TestGet_NotFound(t *testing.T) {
	repo := &fakeRepo{findErr: ErrJobNotFound}
	svc := newTestService(repo, &fakeLLM{}, &fakeExecutor{})

	_, err := svc.Get(context.Background(), 999)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound, got %v", err)
	}
}

func TestGet_Success(t *testing.T) {
	want := &QueryJob{ID: 7, Status: string(StatusSucceeded)}
	repo := &fakeRepo{findResult: want}
	svc := newTestService(repo, &fakeLLM{}, &fakeExecutor{})

	got, err := svc.Get(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != 7 {
		t.Errorf("expected job 7, got %d", got.ID)
	}
}
