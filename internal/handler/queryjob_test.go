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
	"github.com/Yangsss13/askdb-go/internal/queryresult"
)

// stubService is a hand-written implementation of queryJobService.
type stubService struct {
	submitResult *queryjob.QueryJob
	submitErr    error
	submitCalled bool
	getResult    *queryjob.QueryJob
	getErr       error
}

func (s *stubService) Submit(_ context.Context, _ uint64, _ string, _ uint64) (*queryjob.QueryJob, error) {
	s.submitCalled = true
	return s.submitResult, s.submitErr
}

func (s *stubService) Get(_ context.Context, _ uint64, _ uint64) (*queryjob.QueryJob, error) {
	return s.getResult, s.getErr
}

// stubResultService is a hand-written implementation of queryResultService.
type stubResultService struct {
	result *queryresult.CachedQueryResult
	err    error
}

func (s *stubResultService) GetResult(_ context.Context, _ uint64, _ uint64) (*queryresult.CachedQueryResult, error) {
	return s.result, s.err
}

func setupRouter(svc queryJobService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectUserID(1))
	h := NewQueryJobHandler(svc, &stubResultService{})
	v1 := r.Group("/api/v1")
	v1.POST("/query-jobs", h.Submit)
	v1.GET("/query-jobs/:id", h.Get)
	v1.GET("/query-jobs/:id/result", h.GetResult)
	return r
}

func setupRouterWithResult(svc queryJobService, rSvc queryResultService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectUserID(1))
	h := NewQueryJobHandler(svc, rSvc)
	v1 := r.Group("/api/v1")
	v1.POST("/query-jobs", h.Submit)
	v1.GET("/query-jobs/:id", h.Get)
	v1.GET("/query-jobs/:id/result", h.GetResult)
	return r
}

// injectUserID is a test-only middleware that sets a fixed user ID in context,
// replacing the Bearer middleware without needing a real JWT.
func injectUserID(uid uint64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("userID", uid)
		c.Next()
	}
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
	job.ResultExpiresAt.Time = now.Add(15 * time.Minute)
	job.ResultExpiresAt.Valid = true

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
	if resp["result_expires_at"] == nil {
		t.Error("succeeded job must include result_expires_at")
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
	if msg, _ := resp["error_message"].(string); errors.Is(nil, nil) && msg == "" {
		t.Error("expected error_message to be set")
	}
}

// --- Phase 4: GetResult handler tests ---

func TestGetResult_InvalidID_Returns400(t *testing.T) {
	r := setupRouterWithResult(&stubService{}, &stubResultService{})
	for _, id := range []string{"abc", "0", "-1"} {
		w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/"+id+"/result", "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("id %q: expected 400, got %d", id, w.Code)
		}
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["error_code"] != queryjob.ErrCodeInvalidJobID {
			t.Errorf("id %q: expected INVALID_JOB_ID, got %v", id, resp["error_code"])
		}
	}
}

func TestGetResult_NotFound_Returns404(t *testing.T) {
	rSvc := &stubResultService{err: queryjob.ErrJobNotFound}
	r := setupRouterWithResult(&stubService{}, rSvc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/999/result", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGetResult_ResultNotReady_Returns409(t *testing.T) {
	rSvc := &stubResultService{err: &queryjob.ServiceError{Code: queryjob.ErrCodeResultNotReady}}
	r := setupRouterWithResult(&stubService{}, rSvc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/1/result", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestGetResult_JobFailed_Returns409(t *testing.T) {
	rSvc := &stubResultService{err: &queryjob.ServiceError{Code: queryjob.ErrCodeQueryJobFailed}}
	r := setupRouterWithResult(&stubService{}, rSvc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/1/result", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestGetResult_Succeeded_Returns200(t *testing.T) {
	now := time.Now().UTC()
	cached := &queryresult.CachedQueryResult{
		JobID:     42,
		Columns:   []string{"id", "name"},
		Rows:      [][]any{{int64(1), "商品A"}},
		RowCount:  1,
		CachedAt:  now,
		ExpiresAt: now.Add(15 * time.Minute),
	}
	rSvc := &stubResultService{result: cached}
	r := setupRouterWithResult(&stubService{}, rSvc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/42/result", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["job_id"].(float64) != 42 {
		t.Errorf("expected job_id=42, got %v", resp["job_id"])
	}
	if resp["columns"] == nil {
		t.Error("response must include columns")
	}
	if resp["rows"] == nil {
		t.Error("response must include rows")
	}
	if resp["row_count"] == nil {
		t.Error("response must include row_count")
	}
	if resp["cached_at"] == nil {
		t.Error("response must include cached_at")
	}
	if resp["expires_at"] == nil {
		t.Error("response must include expires_at")
	}
	// Redis key must never appear in response.
	body := w.Body.String()
	if strings.Contains(body, "askdb:query-result") {
		t.Errorf("response must not contain Redis key; got: %s", body)
	}
}

func TestGetResult_Expired_Returns410(t *testing.T) {
	rSvc := &stubResultService{err: &queryjob.ServiceError{Code: queryjob.ErrCodeResultExpired}}
	r := setupRouterWithResult(&stubService{}, rSvc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/1/result", "")
	if w.Code != http.StatusGone {
		t.Fatalf("expected 410, got %d", w.Code)
	}
}

func TestGetResult_Unavailable_Returns503(t *testing.T) {
	rSvc := &stubResultService{err: &queryjob.ServiceError{Code: queryjob.ErrCodeResultUnavailable}}
	r := setupRouterWithResult(&stubService{}, rSvc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/1/result", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestGetResult_StoreUnavailable_Returns503(t *testing.T) {
	rSvc := &stubResultService{err: &queryjob.ServiceError{Code: queryjob.ErrCodeResultStoreUnavail}}
	r := setupRouterWithResult(&stubService{}, rSvc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/1/result", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	// Must not expose Redis internals.
	body := w.Body.String()
	for _, s := range []string{"redis://", "localhost:6379", "connection refused"} {
		if strings.Contains(strings.ToLower(body), s) {
			t.Errorf("response must not expose %q; got: %s", s, body)
		}
	}
}

func TestGet_Succeeded_NoRows(t *testing.T) {
	job := &queryjob.QueryJob{ID: 1, Status: string(queryjob.StatusSucceeded)}
	job.GeneratedSQL.Valid = true
	job.GeneratedSQL.String = "SELECT 1"
	svc := &stubService{getResult: job}
	r := setupRouter(svc)

	w := doJSON(r, http.MethodGet, "/api/v1/query-jobs/1", "")
	body := w.Body.String()
	if strings.Contains(body, `"rows"`) {
		t.Errorf("GET job response must never include rows; got: %s", body)
	}
}
