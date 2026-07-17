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
type queryJobService interface {
	Submit(ctx context.Context, question string) (*queryjob.QueryJob, error)
	Get(ctx context.Context, id uint64) (*queryjob.QueryJob, error)
}

// submitRequest is the POST body.
type submitRequest struct {
	Question string `json:"question"`
}

// submitResponse is the 202 response for POST /api/v1/query-jobs.
// It contains only the fields available at submission time; the full result
// is obtained via GET once the Worker has completed the job.
type submitResponse struct {
	JobID     uint64    `json:"job_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// queryJobResponse is the full DTO returned by GET /api/v1/query-jobs/:id.
// It never includes complete result rows (deferred to a later phase).
type queryJobResponse struct {
	JobID               uint64     `json:"job_id"`
	Question            string     `json:"question"`
	Status              string     `json:"status"`
	GeneratedSQL        string     `json:"generated_sql,omitempty"`
	RowCount            *int64     `json:"row_count,omitempty"`
	ExecutionDurationMs *int64     `json:"execution_duration_ms,omitempty"`
	ErrorCode           string     `json:"error_code,omitempty"`
	ErrorMessage        string     `json:"error_message,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
}

// errorResponse is returned for validation failures and not-found cases.
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

// Submit handles POST /api/v1/query-jobs. On success it returns HTTP 202 with
// the job_id and status=queued. Result rows are not returned here; clients
// poll GET /api/v1/query-jobs/:id.
func (h *QueryJobHandler) Submit(c *gin.Context) {
	var req submitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{
			ErrorCode:    queryjob.ErrCodeInvalidQuestion,
			ErrorMessage: "invalid request body",
		})
		return
	}

	job, err := h.svc.Submit(c.Request.Context(), req.Question)
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

// Get handles GET /api/v1/query-jobs/:id. It returns the persisted job
// snapshot; complete result rows are not included.
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

	c.JSON(http.StatusOK, toJobResponse(job))
}

// serviceErrorStatus maps a stable error code to an HTTP status.
func serviceErrorStatus(code string) int {
	switch code {
	case queryjob.ErrCodeInvalidQuestion:
		return http.StatusBadRequest
	case queryjob.ErrCodePublishFailed:
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
	return resp
}
