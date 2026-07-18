package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Yangsss13/askdb-go/internal/middleware"
	"github.com/Yangsss13/askdb-go/internal/queryjob"
	"github.com/Yangsss13/askdb-go/internal/queryresult"
)

// queryJobService is the narrow service dependency for job submission and status.
type queryJobService interface {
	Submit(ctx context.Context, userID uint64, question string, dataSourceID uint64) (*queryjob.QueryJob, error)
	Get(ctx context.Context, userID uint64, id uint64) (*queryjob.QueryJob, error)
}

// queryResultService is the narrow service dependency for result retrieval.
type queryResultService interface {
	GetResult(ctx context.Context, userID uint64, jobID uint64) (*queryresult.CachedQueryResult, error)
}

// submitRequest is the POST body.
type submitRequest struct {
	Question     string `json:"question"`
	DataSourceID uint64 `json:"data_source_id"`
}

// submitResponse is the 202 response for POST /api/v1/query-jobs.
// It contains only the fields available at submission time.
type submitResponse struct {
	JobID     uint64    `json:"job_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// queryJobResponse is the DTO returned by GET /api/v1/query-jobs/:id.
// It never includes complete result rows; use GET /result for those.
type queryJobResponse struct {
	JobID               uint64     `json:"job_id"`
	Question            string     `json:"question"`
	Status              string     `json:"status"`
	GeneratedSQL        string     `json:"generated_sql,omitempty"`
	RowCount            *int64     `json:"row_count,omitempty"`
	ExecutionDurationMs *int64     `json:"execution_duration_ms,omitempty"`
	ErrorCode           string     `json:"error_code,omitempty"`
	ErrorMessage        string     `json:"error_message,omitempty"`
	ResultExpiresAt     *time.Time `json:"result_expires_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
}

// queryResultResponse is the 200 response for GET /api/v1/query-jobs/:id/result.
type queryResultResponse struct {
	JobID     uint64    `json:"job_id"`
	Columns   []string  `json:"columns"`
	Rows      [][]any   `json:"rows"`
	RowCount  int64     `json:"row_count"`
	CachedAt  time.Time `json:"cached_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// errorResponse is returned for validation failures and not-found cases.
type errorResponse struct {
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

// QueryJobHandler handles the query-job HTTP endpoints.
type QueryJobHandler struct {
	svc       queryJobService
	resultSvc queryResultService
}

// NewQueryJobHandler returns a handler backed by the given services.
func NewQueryJobHandler(svc queryJobService, resultSvc queryResultService) *QueryJobHandler {
	return &QueryJobHandler{svc: svc, resultSvc: resultSvc}
}

// Submit handles POST /api/v1/query-jobs.
func (h *QueryJobHandler) Submit(c *gin.Context) {
	var req submitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{
			ErrorCode:    queryjob.ErrCodeInvalidQuestion,
			ErrorMessage: "invalid request body",
		})
		return
	}

	job, err := h.svc.Submit(c.Request.Context(), middleware.UserID(c), req.Question, req.DataSourceID)
	if err != nil {
		var svcErr *queryjob.ServiceError
		if errors.As(err, &svcErr) {
			c.JSON(serviceErrorStatus(svcErr.Code), errorResponse{
				ErrorCode:    svcErr.Code,
				ErrorMessage: svcErr.Message,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{
			ErrorCode:    queryjob.ErrCodeInternal,
			ErrorMessage: "internal error",
		})
		return
	}

	c.JSON(http.StatusAccepted, submitResponse{
		JobID:     job.ID,
		Status:    job.Status,
		CreatedAt: job.CreatedAt,
	})
}

// Get handles GET /api/v1/query-jobs/:id.
func (h *QueryJobHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, errorResponse{
			ErrorCode:    queryjob.ErrCodeInvalidJobID,
			ErrorMessage: "invalid job id",
		})
		return
	}

	job, err := h.svc.Get(c.Request.Context(), middleware.UserID(c), id)
	if err != nil {
		if errors.Is(err, queryjob.ErrJobNotFound) {
			c.JSON(http.StatusNotFound, errorResponse{
				ErrorCode:    queryjob.ErrCodeJobNotFound,
				ErrorMessage: "job not found",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{
			ErrorCode:    queryjob.ErrCodeInternal,
			ErrorMessage: "internal error",
		})
		return
	}

	c.JSON(http.StatusOK, toJobResponse(job))
}

// GetResult handles GET /api/v1/query-jobs/:id/result.
// It always checks MySQL first; Redis is never the source of truth for job status.
func (h *QueryJobHandler) GetResult(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, errorResponse{
			ErrorCode:    queryjob.ErrCodeInvalidJobID,
			ErrorMessage: "invalid job id",
		})
		return
	}

	result, err := h.resultSvc.GetResult(c.Request.Context(), middleware.UserID(c), id)
	if err != nil {
		if errors.Is(err, queryjob.ErrJobNotFound) {
			c.JSON(http.StatusNotFound, errorResponse{
				ErrorCode:    queryjob.ErrCodeJobNotFound,
				ErrorMessage: "job not found",
			})
			return
		}
		var svcErr *queryjob.ServiceError
		if errors.As(err, &svcErr) {
			c.JSON(resultErrorStatus(svcErr.Code), errorResponse{
				ErrorCode:    svcErr.Code,
				ErrorMessage: svcErr.Message,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{
			ErrorCode:    queryjob.ErrCodeInternal,
			ErrorMessage: "internal error",
		})
		return
	}

	c.JSON(http.StatusOK, queryResultResponse{
		JobID:     result.JobID,
		Columns:   result.Columns,
		Rows:      result.Rows,
		RowCount:  result.RowCount,
		CachedAt:  result.CachedAt,
		ExpiresAt: result.ExpiresAt,
	})
}

// serviceErrorStatus maps a stable error code to an HTTP status for job operations.
func serviceErrorStatus(code string) int {
	switch code {
	case queryjob.ErrCodeInvalidQuestion, queryjob.ErrCodeMissingDataSource:
		return http.StatusBadRequest
	case queryjob.ErrCodeDataSourceNotFound:
		return http.StatusNotFound
	case queryjob.ErrCodePublishFailed:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// resultErrorStatus maps result-fetch error codes to HTTP statuses.
func resultErrorStatus(code string) int {
	switch code {
	case queryjob.ErrCodeResultNotReady, queryjob.ErrCodeQueryJobFailed:
		return http.StatusConflict
	case queryjob.ErrCodeResultExpired:
		return http.StatusGone
	case queryjob.ErrCodeResultUnavailable,
		queryjob.ErrCodeResultStoreUnavail,
		queryjob.ErrCodeResultCorrupted:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// toJobResponse maps a persisted job to the client DTO (no result rows).
func toJobResponse(job *queryjob.QueryJob) queryJobResponse {
	resp := queryJobResponse{
		JobID:     job.ID,
		Question:  job.Question,
		Status:    job.Status,
		CreatedAt: job.CreatedAt,
	}
	if job.GeneratedSQL.Valid {
		resp.GeneratedSQL = job.GeneratedSQL.String
	}
	if job.RowCount.Valid {
		v := job.RowCount.Int64
		resp.RowCount = &v
	}
	if job.ExecutionDurationMs.Valid {
		v := job.ExecutionDurationMs.Int64
		resp.ExecutionDurationMs = &v
	}
	if job.ErrorCode.Valid {
		resp.ErrorCode = job.ErrorCode.String
	}
	if job.ErrorMessage.Valid {
		resp.ErrorMessage = job.ErrorMessage.String
	}
	if job.FinishedAt.Valid {
		t := job.FinishedAt.Time
		resp.FinishedAt = &t
	}
	if job.ResultExpiresAt.Valid {
		t := job.ResultExpiresAt.Time
		resp.ResultExpiresAt = &t
	}
	return resp
}
