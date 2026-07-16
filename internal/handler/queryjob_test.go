package handler

import (
	"context"
	"database/sql"
	"encoding/json"
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
	submitResult *queryjob.QueryResult
	submitErr    error
	submitCalled bool
	getResult    *queryjob.QueryJob
	getErr       error
}

func (s *stubService) Submit(_ context.Context, _ string) (*queryjob.QueryResult, error) {
	s.submitCalled = true
	return s.submitResult, s.submitErr
}

func (s *stubService) Get(_ context.Context, _ uint64) (*queryjob.QueryJob, error) {
	return s.getResult, s.getErr
}

func setupRouter(svc queryJobService) *gin.Engine {
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

func TestSubmit_Success(t *testing.T) {
	now := time.Now()
	svc := &stubService{submitResult: &queryjob.QueryResult{
		Job: &queryjob.QueryJob{
			ID:                  1,
			Question:            "查询所有商品",
			Status:              string(queryjob.StatusSucceeded),
			GeneratedSQL:        sql.NullString{String: "SELECT id FROM products LIMIT 100", Valid: true},
			RowCount:            sql.NullInt64{Int64: 2, Valid: true},
			ExecutionDurationMs: sql.NullInt64{Int64: 5, Valid: true},
			CreatedAt:           now,
			FinishedAt:          sql.NullTime{Time: now, Valid: true},
		},
		Columns: []string{"id"},
		Rows:    [][]any{{int64(1)}, {int64(2)}},
	}}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodPost, "/api/v1/query-jobs", `{"question":"查询所有商品"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "succeeded" {
		t.Errorf("expected succeeded, got %v", resp["status"])
	}
	if resp["columns"] == nil || resp["rows"] == nil {
		t.Error("expected columns and rows in success response")
	}
	// Ensure GORM-internal representations do not leak (no updated_at field in DTO).
	if _, ok := resp["updated_at"]; ok {
		t.Error("response must not expose updated_at")
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

func TestSubmit_ValidationErrorNoJob(t *testing.T) {
	svc := &stubService{submitErr: &queryjob.ServiceError{
		Code: queryjob.ErrCodeInvalidQuestion, Message: "question must be 1-500 characters",
	}}
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
}

func TestSubmit_Unsupported422(t *testing.T) {
	svc := &stubService{
		submitResult: &queryjob.QueryResult{Job: &queryjob.QueryJob{
			ID:           2,
			Question:     "unknown",
			Status:       string(queryjob.StatusFailed),
			ErrorCode:    sql.NullString{String: queryjob.ErrCodeUnsupportedQuestion, Valid: true},
			ErrorMessage: sql.NullString{String: "question is not supported", Valid: true},
		}},
		submitErr: &queryjob.ServiceError{Code: queryjob.ErrCodeUnsupportedQuestion, Message: "question is not supported"},
	}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodPost, "/api/v1/query-jobs", `{"question":"unknown"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "failed" {
		t.Errorf("expected failed job in body, got %v", resp["status"])
	}
	if resp["error_code"] != queryjob.ErrCodeUnsupportedQuestion {
		t.Errorf("expected UNSUPPORTED_QUESTION, got %v", resp["error_code"])
	}
}

func TestSubmit_QueryFailure503(t *testing.T) {
	svc := &stubService{
		submitResult: &queryjob.QueryResult{Job: &queryjob.QueryJob{
			ID: 3, Status: string(queryjob.StatusFailed),
			ErrorCode: sql.NullString{String: queryjob.ErrCodeQueryExecution, Valid: true},
		}},
		submitErr: &queryjob.ServiceError{Code: queryjob.ErrCodeQueryExecution, Message: "failed to execute the query"},
	}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodPost, "/api/v1/query-jobs", `{"question":"查询所有商品"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestGet_Success(t *testing.T) {
	now := time.Now()
	svc := &stubService{getResult: &queryjob.QueryJob{
		ID: 5, Question: "查询所有商品", Status: string(queryjob.StatusSucceeded), CreatedAt: now,
	}}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/5", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["job_id"].(float64) != 5 {
		t.Errorf("expected job_id 5, got %v", resp["job_id"])
	}
	// GET must not include full rows.
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
