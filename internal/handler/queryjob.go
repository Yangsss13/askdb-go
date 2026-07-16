package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Yangsss13/askdb-go/internal/queryjob"
)

// queryJobService is the narrow service dependency the handler consumes.
// Declared here so the handler does not couple to the concrete Service type.
type queryJobService interface {
	Submit(ctx context.Context, question string) (*queryjob.QueryResult, error)
	Get(ctx context.Context, id uint64) (*queryjob.QueryJob, error)
}

// submitRequest is the POST body.
type submitRequest struct {
	Question string `json:"question"`
}

// queryJobResponse is the client-facing DTO. It is intentionally separate from
// the GORM model so storage fields never leak into the API.
type queryJobResponse struct {
	JobID               uint64     `json:"job_id"`
	Question            string     `json:"question"`
	Status              string     `json:"status"`
	GeneratedSQL        string     `json:"generated_sql,omitempty"`
	Columns             []string   `json:"columns,omitempty"`
	Rows                [][]any    `json:"rows,omitempty"`
	RowCount            *int64     `json:"row_count,omitempty"`
	ExecutionDurationMs *int64     `json:"execution_duration_ms,omitempty"`
	ErrorCode           string     `json:"error_code,omitempty"`
	ErrorMessage        string     `json:"error_message,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
}

// errorResponse is returned when no job could be created (validation, not found).
type errorResponse struct {
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

// QueryJobHandler handles the query-job HTTP endpoints.
type QueryJobHandler struct {
	svc queryJobService
}

// NewQueryJobHandler returns a handler backed by the given service.
func NewQueryJobHandler(svc queryJobService) *QueryJobHandler {
	return &QueryJobHandler{svc: svc}
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

	result, err := h.svc.Submit(c.Request.Context(), req.Question)
	if err != nil {
		var svcErr *queryjob.ServiceError
		if errors.As(err, &svcErr) {
			h.writeServiceError(c, svcErr, result)
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{
			ErrorCode:    queryjob.ErrCodeInternal,
			ErrorMessage: "internal error",
		})
		return
	}

	c.JSON(http.StatusOK, toResponse(result.Job, result.Columns, result.Rows))
}

// writeServiceError maps a ServiceError code to an HTTP status. When a job was
// created (unsupported question, query failure) the persisted job is included.
func (h *QueryJobHandler) writeServiceError(c *gin.Context, svcErr *queryjob.ServiceError, result *queryjob.QueryResult) {
	var status int
	switch svcErr.Code {
	case queryjob.ErrCodeInvalidQuestion:
		status = http.StatusBadRequest
	case queryjob.ErrCodeUnsupportedQuestion:
		status = http.StatusUnprocessableEntity
	case queryjob.ErrCodeQueryExecution:
		status = http.StatusServiceUnavailable
	default:
		status = http.StatusInternalServerError
	}

	// If a job was persisted, return the full job snapshot; otherwise a bare error.
	if result != nil && result.Job != nil {
		c.JSON(status, toResponse(result.Job, nil, nil))
		return
	}
	c.JSON(status, errorResponse{ErrorCode: svcErr.Code, ErrorMessage: svcErr.Message})
}

// Get handles GET /api/v1/query-jobs/:id. It returns persisted job info only,
// never the full result rows.
func (h *QueryJobHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, errorResponse{
			ErrorCode:    queryjob.ErrCodeInvalidQuestion,
			ErrorMessage: "invalid job id",
		})
		return
	}

	job, err := h.svc.Get(c.Request.Context(), id)
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

	c.JSON(http.StatusOK, toResponse(job, nil, nil))
}

// toResponse maps a persisted job (and optional result set) to the client DTO.
func toResponse(job *queryjob.QueryJob, columns []string, rows [][]any) queryJobResponse {
	resp := queryJobResponse{
		JobID:     job.ID,
		Question:  job.Question,
		Status:    job.Status,
		Columns:   columns,
		Rows:      rows,
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
	return resp
}
