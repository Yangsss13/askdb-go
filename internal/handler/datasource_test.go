package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Yangsss13/askdb-go/internal/datasource"
)

func init() { gin.SetMode(gin.TestMode) }

// --- stub service ---

type stubDSService struct {
	createFn   func(ctx context.Context, userID uint64, in datasource.CreateInput) (*datasource.DataSource, error)
	getByIDFn  func(ctx context.Context, id, userID uint64) (*datasource.DataSource, error)
	listFn     func(ctx context.Context, userID uint64) ([]*datasource.DataSource, error)
	updateFn   func(ctx context.Context, id, userID uint64, in datasource.UpdateInput) (*datasource.DataSource, error)
	deleteFn   func(ctx context.Context, id, userID uint64) error
	testConnFn func(ctx context.Context, id, userID uint64) error
}

func (s *stubDSService) Create(ctx context.Context, userID uint64, in datasource.CreateInput) (*datasource.DataSource, error) {
	return s.createFn(ctx, userID, in)
}
func (s *stubDSService) GetByID(ctx context.Context, id, userID uint64) (*datasource.DataSource, error) {
	return s.getByIDFn(ctx, id, userID)
}
func (s *stubDSService) List(ctx context.Context, userID uint64) ([]*datasource.DataSource, error) {
	return s.listFn(ctx, userID)
}
func (s *stubDSService) Update(ctx context.Context, id, userID uint64, in datasource.UpdateInput) (*datasource.DataSource, error) {
	return s.updateFn(ctx, id, userID, in)
}
func (s *stubDSService) Delete(ctx context.Context, id, userID uint64) error {
	return s.deleteFn(ctx, id, userID)
}
func (s *stubDSService) TestConnection(ctx context.Context, id, userID uint64) error {
	return s.testConnFn(ctx, id, userID)
}

func testDSRouter(svc dataSourceService, userID uint64) *gin.Engine {
	r := gin.New()
	h := NewDataSourceHandler(svc)
	// Inject a fixed user ID without real JWT verification.
	injectUser := func(c *gin.Context) {
		c.Set("userID", userID)
		c.Next()
	}
	g := r.Group("/api/v1/data-sources", injectUser)
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:id", h.GetByID)
	g.PUT("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	g.POST("/:id/test", h.TestConnection)
	return r
}

func sampleDS(id uint64) *datasource.DataSource {
	return &datasource.DataSource{
		ID:                 id,
		UserID:             1,
		Label:              "prod",
		Host:               "db.example.com",
		Port:               3306,
		DatabaseName:       "mydb",
		Username:           "reader",
		PasswordCiphertext: "v1:xxxx", // never returned in responses
		TLSMode:            "verify-full",
		ConnectTimeoutSec:  5,
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}
}

func TestCreateDataSource_Success(t *testing.T) {
	svc := &stubDSService{
		createFn: func(_ context.Context, _ uint64, _ datasource.CreateInput) (*datasource.DataSource, error) {
			return sampleDS(1), nil
		},
	}
	body, _ := json.Marshal(map[string]any{
		"label": "prod", "host": "db.example.com", "port": 3306,
		"database_name": "mydb", "username": "reader", "password": "secret",
		"tls_mode": "verify-full",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/data-sources", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	testDSRouter(svc, 1).ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status %d, want %d: %s", w.Code, http.StatusCreated, w.Body)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	// password_ciphertext must never appear in the response.
	if _, ok := resp["password_ciphertext"]; ok {
		t.Error("response must not contain password_ciphertext")
	}
	if _, ok := resp["password"]; ok {
		t.Error("response must not contain password")
	}
}

func TestCreateDataSource_InvalidBody(t *testing.T) {
	svc := &stubDSService{}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/data-sources", bytes.NewReader([]byte("notjson")))
	req.Header.Set("Content-Type", "application/json")
	testDSRouter(svc, 1).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

func TestGetByID_NotFound(t *testing.T) {
	svc := &stubDSService{
		getByIDFn: func(_ context.Context, _, _ uint64) (*datasource.DataSource, error) {
			return nil, &datasource.ServiceError{Code: datasource.ErrCodeNotFound, Message: "not found", Status: 404}
		},
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/data-sources/99", nil)
	testDSRouter(svc, 1).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}

// TestGetByID_CrossUser verifies that a cross-user lookup yields 404 (IDOR protection).
func TestGetByID_CrossUser(t *testing.T) {
	svc := &stubDSService{
		// Service enforces ownership; always returns 404 for wrong user.
		getByIDFn: func(_ context.Context, _, _ uint64) (*datasource.DataSource, error) {
			return nil, &datasource.ServiceError{Code: datasource.ErrCodeNotFound, Message: "not found", Status: 404}
		},
	}
	w := httptest.NewRecorder()
	// Request as user 2 for a source owned by user 1.
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/data-sources/1", nil)
	testDSRouter(svc, 2).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("IDOR: status %d, want 404", w.Code)
	}
}

func TestList_Empty(t *testing.T) {
	svc := &stubDSService{
		listFn: func(_ context.Context, _ uint64) ([]*datasource.DataSource, error) {
			return nil, nil
		},
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/data-sources", nil)
	testDSRouter(svc, 1).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
}

func TestDelete_HasActiveJobs(t *testing.T) {
	svc := &stubDSService{
		deleteFn: func(_ context.Context, _, _ uint64) error {
			return &datasource.ServiceError{Code: datasource.ErrCodeHasActiveJobs, Message: "active jobs", Status: 422}
		},
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/data-sources/1", nil)
	testDSRouter(svc, 1).ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", w.Code)
	}
}

func TestDelete_Success(t *testing.T) {
	svc := &stubDSService{
		deleteFn: func(_ context.Context, _, _ uint64) error { return nil },
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/data-sources/1", nil)
	testDSRouter(svc, 1).ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", w.Code)
	}
}

func TestTestConnection_Fail(t *testing.T) {
	svc := &stubDSService{
		testConnFn: func(_ context.Context, _, _ uint64) error {
			return &datasource.ServiceError{Code: datasource.ErrCodeConnectFailed, Message: "connection test failed", Status: 422}
		},
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/data-sources/1/test", nil)
	testDSRouter(svc, 1).ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", w.Code)
	}
	// Raw error must not contain hostname/IP/password.
	body := w.Body.String()
	if contains(body, "db.example.com") || contains(body, "password") || contains(body, "secret") {
		t.Errorf("response leaks sensitive data: %s", body)
	}
}

func TestUpdate_NilPasswordKeepsCurrent(t *testing.T) {
	var capturedInput datasource.UpdateInput
	svc := &stubDSService{
		updateFn: func(_ context.Context, _, _ uint64, in datasource.UpdateInput) (*datasource.DataSource, error) {
			capturedInput = in
			return sampleDS(1), nil
		},
	}
	newLabel := "updated"
	body, _ := json.Marshal(map[string]any{"label": newLabel})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/data-sources/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	testDSRouter(svc, 1).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	if capturedInput.Password != nil {
		t.Error("Password must be nil when not provided in request")
	}
}

func TestInvalidIDParam(t *testing.T) {
	svc := &stubDSService{}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/data-sources/abc", nil)
	testDSRouter(svc, 1).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

func TestResponseNeverContainsCiphertext(t *testing.T) {
	svc := &stubDSService{
		getByIDFn: func(_ context.Context, _, _ uint64) (*datasource.DataSource, error) {
			ds := sampleDS(1)
			ds.PasswordCiphertext = "v1:SUPERSECRETCIPHERTEXT"
			return ds, nil
		},
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/data-sources/1", nil)
	testDSRouter(svc, 1).ServeHTTP(w, req)
	if contains(w.Body.String(), "SUPERSECRETCIPHERTEXT") {
		t.Error("response must not contain password_ciphertext value")
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) &&
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}()
}

// TestServiceError_NonServiceError verifies internal errors yield 500.
func TestServiceError_NonServiceError(t *testing.T) {
	svc := &stubDSService{
		getByIDFn: func(_ context.Context, _, _ uint64) (*datasource.DataSource, error) {
			return nil, errors.New("unexpected db failure")
		},
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/data-sources/1", nil)
	testDSRouter(svc, 1).ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", w.Code)
	}
}
