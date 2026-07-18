package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Yangsss13/askdb-go/internal/datasource"
	"github.com/Yangsss13/askdb-go/internal/middleware"
)

// dataSourceService is the narrow service dependency for data-source operations.
type dataSourceService interface {
	Create(ctx context.Context, userID uint64, in datasource.CreateInput) (*datasource.DataSource, error)
	GetByID(ctx context.Context, id, userID uint64) (*datasource.DataSource, error)
	List(ctx context.Context, userID uint64) ([]*datasource.DataSource, error)
	Update(ctx context.Context, id, userID uint64, in datasource.UpdateInput) (*datasource.DataSource, error)
	Delete(ctx context.Context, id, userID uint64) error
	TestConnection(ctx context.Context, id, userID uint64) error
}

// createDataSourceRequest is the POST /api/v1/data-sources body.
type createDataSourceRequest struct {
	Label             string `json:"label"`
	Host              string `json:"host"`
	Port              uint16 `json:"port"`
	DatabaseName      string `json:"database_name"`
	Username          string `json:"username"`
	Password          string `json:"password"`
	TLSMode           string `json:"tls_mode"`            // "disabled" or "verify-full"
	ConnectTimeoutSec uint8  `json:"connect_timeout_sec"` // 0 = default (5s)
}

// updateDataSourceRequest is the PUT body; all fields are optional.
type updateDataSourceRequest struct {
	Label             *string `json:"label"`
	Host              *string `json:"host"`
	Port              *uint16 `json:"port"`
	DatabaseName      *string `json:"database_name"`
	Username          *string `json:"username"`
	Password          *string `json:"password"` // nil = keep current
	TLSMode           *string `json:"tls_mode"`
	ConnectTimeoutSec *uint8  `json:"connect_timeout_sec"`
}

// dataSourceResponse is the DTO returned for all data-source reads.
// password_ciphertext is explicitly excluded.
type dataSourceResponse struct {
	ID                uint64    `json:"id"`
	Label             string    `json:"label"`
	Host              string    `json:"host"`
	Port              uint16    `json:"port"`
	DatabaseName      string    `json:"database_name"`
	Username          string    `json:"username"`
	TLSMode           string    `json:"tls_mode"`
	ConnectTimeoutSec uint8     `json:"connect_timeout_sec"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// DataSourceHandler handles data-source HTTP endpoints.
type DataSourceHandler struct {
	svc dataSourceService
}

// NewDataSourceHandler returns a handler backed by svc.
func NewDataSourceHandler(svc dataSourceService) *DataSourceHandler {
	return &DataSourceHandler{svc: svc}
}

// Create handles POST /api/v1/data-sources.
func (h *DataSourceHandler) Create(c *gin.Context) {
	var req createDataSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dsErrorResp(datasource.ErrCodeInvalidInput, "invalid request body"))
		return
	}

	tlsMode := datasource.TLSVerifyFull
	if req.TLSMode != "" {
		tlsMode = datasource.TLSMode(req.TLSMode)
	}

	ds, err := h.svc.Create(c.Request.Context(), middleware.UserID(c), datasource.CreateInput{
		Label:             req.Label,
		Host:              req.Host,
		Port:              req.Port,
		DatabaseName:      req.DatabaseName,
		Username:          req.Username,
		Password:          req.Password,
		TLSMode:           tlsMode,
		ConnectTimeoutSec: req.ConnectTimeoutSec,
	})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toDataSourceResponse(ds))
}

// GetByID handles GET /api/v1/data-sources/:id.
func (h *DataSourceHandler) GetByID(c *gin.Context) {
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	ds, err := h.svc.GetByID(c.Request.Context(), id, middleware.UserID(c))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, toDataSourceResponse(ds))
}

// List handles GET /api/v1/data-sources.
func (h *DataSourceHandler) List(c *gin.Context) {
	sources, err := h.svc.List(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	resp := make([]dataSourceResponse, len(sources))
	for i, ds := range sources {
		resp[i] = toDataSourceResponse(ds)
	}
	c.JSON(http.StatusOK, resp)
}

// Update handles PUT /api/v1/data-sources/:id.
func (h *DataSourceHandler) Update(c *gin.Context) {
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}

	var req updateDataSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dsErrorResp(datasource.ErrCodeInvalidInput, "invalid request body"))
		return
	}

	var tlsMode *datasource.TLSMode
	if req.TLSMode != nil {
		m := datasource.TLSMode(*req.TLSMode)
		tlsMode = &m
	}

	ds, err := h.svc.Update(c.Request.Context(), id, middleware.UserID(c), datasource.UpdateInput{
		Label:             req.Label,
		Host:              req.Host,
		Port:              req.Port,
		DatabaseName:      req.DatabaseName,
		Username:          req.Username,
		Password:          req.Password,
		TLSMode:           tlsMode,
		ConnectTimeoutSec: req.ConnectTimeoutSec,
	})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, toDataSourceResponse(ds))
}

// Delete handles DELETE /api/v1/data-sources/:id.
func (h *DataSourceHandler) Delete(c *gin.Context) {
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id, middleware.UserID(c)); err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// TestConnection handles POST /api/v1/data-sources/:id/test.
func (h *DataSourceHandler) TestConnection(c *gin.Context) {
	id, ok := parseUint64Param(c, "id")
	if !ok {
		return
	}
	if err := h.svc.TestConnection(c.Request.Context(), id, middleware.UserID(c)); err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// writeServiceError maps a datasource.ServiceError to an HTTP response.
func (h *DataSourceHandler) writeServiceError(c *gin.Context, err error) {
	var svcErr *datasource.ServiceError
	if errors.As(err, &svcErr) {
		c.JSON(svcErr.Status, dsErrorResp(svcErr.Code, svcErr.Message))
		return
	}
	c.JSON(http.StatusInternalServerError, dsErrorResp(datasource.ErrCodeInternal, "internal error"))
}

// toDataSourceResponse maps the model to the safe DTO (no password fields).
func toDataSourceResponse(ds *datasource.DataSource) dataSourceResponse {
	return dataSourceResponse{
		ID:                ds.ID,
		Label:             ds.Label,
		Host:              ds.Host,
		Port:              ds.Port,
		DatabaseName:      ds.DatabaseName,
		Username:          ds.Username,
		TLSMode:           ds.TLSMode,
		ConnectTimeoutSec: ds.ConnectTimeoutSec,
		CreatedAt:         ds.CreatedAt,
		UpdatedAt:         ds.UpdatedAt,
	}
}

func dsErrorResp(code, msg string) gin.H {
	return gin.H{"error_code": code, "error_message": msg}
}

// parseUint64Param parses a named URL parameter as uint64.
// On failure it writes a 400 response and returns false.
func parseUint64Param(c *gin.Context, name string) (uint64, bool) {
	v, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || v == 0 {
		c.JSON(http.StatusBadRequest, dsErrorResp(datasource.ErrCodeInvalidInput, "invalid "+name))
		return 0, false
	}
	return v, true
}
