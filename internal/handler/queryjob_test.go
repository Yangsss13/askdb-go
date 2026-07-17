package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Yangsss13/askdb-go/internal/queryjob"
)

// stubService is a hand-written implementation of queryJobService.
type stubService struct {
	submitResult *queryjob.QueryJob
	submitErr    error
	submitCalled bool
	getResult    *queryjob.QueryJob
	getErr       error
}

func (s *stubService) Submit(_ context.Context, _ string) (*queryjob.QueryJob, error) {
	s.submitCalled = true
	return s.submitResult, s.submitErr
}

func (s *stubService) Get(_ context.Context, _ uint64) (*queryjob.QueryJob, error) {
	return s.getResult, s.getErr
}

func setupRouter(svc queryJobService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewQueryJobHandler(svc)
	v1 := r.Group("/api/v1")
	v1.POST("/query-jobs", h.Submit)
	v1.GET("/query-jobs/:id", h.Get)
	return r
}

func doJSON(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSubmit_Success_202(t *testing.T) {
	now := time.Now()
	svc := &stubService{submitResult: &queryjob.QueryJob{
		ID:        1,
		Question:  "查询所有商品",
		Status:    string(queryjob.StatusQueued),
		CreatedAt: now,
	}}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodPost, "/api/v1/query-jobs", `{"question":"查询所有商品"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body: %s", w.Code, w.Body)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["job_id"].(float64) != 1 {
		t.Errorf("expected job_id=1, got %v", resp["job_id"])
	}
	if resp["status"] != string(queryjob.StatusQueued) {
		t.Errorf("expected status=queued, got %v", resp["status"])
	}

	// 202 response must NOT include columns, rows, or generated_sql.
	for _, forbidden := range []string{"columns", "rows", "generated_sql"} {
		if _, ok := resp[forbidden]; ok {
			t.Errorf("202 response must not include %q", forbidden)
		}
	}
}

func TestSubmit_InvalidBody(t *testing.T) {
	svc := &stubService{}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodPost, "/api/v1/query-jobs", `not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if svc.submitCalled {
		t.Error("service must not be called on invalid body")
	}
}

func TestSubmit_InvalidQuestion_400(t *testing.T) {
	svc := &stubService{
		submitErr: &queryjob.ServiceError{
			Code: queryjob.ErrCodeInvalidQuestion, Message: "question must be 1-500 characters",
		},
	}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodPost, "/api/v1/query-jobs", `{"question":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error_code"] != queryjob.ErrCodeInvalidQuestion {
		t.Errorf("expected INVALID_QUESTION, got %v", resp["error_code"])
	}
	// No job persisted, so no job fields in response.
	if _, ok := resp["job_id"]; ok {
		t.Error("response must not include job_id when no job was created")
	}
}

func TestSubmit_PublishFailed_503(t *testing.T) {
	svc := &stubService{
		submitErr: &queryjob.ServiceError{
			Code: queryjob.ErrCodePublishFailed, Message: "failed to queue the request",
		},
	}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodPost, "/api/v1/query-jobs", `{"question":"查询所有商品"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d; body: %s", w.Code, w.Body)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error_code"] != queryjob.ErrCodePublishFailed {
		t.Errorf("expected PUBLISH_FAILED, got %v", resp["error_code"])
	}
	// Must not leak broker connection details.
	body := w.Body.String()
	for _, sensitive := range []string{"amqp://", "rabbitmq", "connection refused"} {
		if strings.Contains(strings.ToLower(body), sensitive) {
			t.Errorf("response must not expose %q; got: %s", sensitive, body)
		}
	}
}

func TestGet_Queued(t *testing.T) {
	now := time.Now()
	svc := &stubService{getResult: &queryjob.QueryJob{
		ID: 3, Question: "查询所有商品",
		Status: string(queryjob.StatusQueued), CreatedAt: now,
	}}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/3", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != string(queryjob.StatusQueued) {
		t.Errorf("expected queued, got %v", resp["status"])
	}
	// Not finished yet — no generated_sql or rows.
	if _, ok := resp["rows"]; ok {
		t.Error("GET response must not include rows")
	}
}

func TestGet_Succeeded(t *testing.T) {
	now := time.Now()
	var rowCount int64 = 10
	var dur int64 = 7
	job := &queryjob.QueryJob{
		ID: 5, Question: "查询所有商品",
		Status:    string(queryjob.StatusSucceeded),
		CreatedAt: now,
	}
	job.GeneratedSQL.String = "SELECT id FROM products LIMIT 100"
	job.GeneratedSQL.Valid = true
	job.RowCount.Int64 = rowCount
	job.RowCount.Valid = true
	job.ExecutionDurationMs.Int64 = dur
	job.ExecutionDurationMs.Valid = true
	job.FinishedAt.Time = now
	job.FinishedAt.Valid = true

	svc := &stubService{getResult: job}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/5", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["generated_sql"] == nil {
		t.Error("succeeded job must include generated_sql")
	}
	if resp["row_count"] == nil {
		t.Error("succeeded job must include row_count")
	}
	if resp["execution_duration_ms"] == nil {
		t.Error("succeeded job must include execution_duration_ms")
	}
	// Still no rows.
	if _, ok := resp["rows"]; ok {
		t.Error("GET response must not include rows")
	}
}

func TestGet_NotFound(t *testing.T) {
	svc := &stubService{getErr: queryjob.ErrJobNotFound}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/999", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error_code"] != queryjob.ErrCodeJobNotFound {
		t.Errorf("expected JOB_NOT_FOUND, got %v", resp["error_code"])
	}
}

func TestGet_InvalidID(t *testing.T) {
	svc := &stubService{}
	r := setupRouter(svc)

	for _, id := range []string{"abc", "0", "-1"} {
		w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/"+id, "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("id %q: expected 400, got %d", id, w.Code)
		}
	}
}

func TestGet_FailedJob(t *testing.T) {
	now := time.Now()
	job := &queryjob.QueryJob{
		ID: 7, Status: string(queryjob.StatusFailed), CreatedAt: now,
	}
	job.ErrorCode.String = queryjob.ErrCodeUnsupportedQuestion
	job.ErrorCode.Valid = true
	job.ErrorMessage.String = "question is not supported"
	job.ErrorMessage.Valid = true
	job.FinishedAt.Time = now
	job.FinishedAt.Valid = true

	svc := &stubService{getResult: job}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/7", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != string(queryjob.StatusFailed) {
		t.Errorf("expected failed, got %v", resp["status"])
	}
	if resp["error_code"] != queryjob.ErrCodeUnsupportedQuestion {
		t.Errorf("expected UNSUPPORTED_QUESTION, got %v", resp["error_code"])
	}
	// Error message must be safe.
	if msg, _ := resp["error_message"].(string); errors.Is(nil, nil) && msg == "" {
		// just check it's present
		t.Error("expected error_message to be set")
	}
}
