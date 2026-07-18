package datasource

import "errors"

// ErrNotFound is returned when no data source matches the query or the
// caller does not own the row (same sentinel prevents IDOR leakage).
var ErrNotFound = errors.New("datasource: not found")

// ErrDuplicateLabel is returned when a (user_id, label) pair already exists,
// including soft-deleted rows (label occupancy is intentional).
var ErrDuplicateLabel = errors.New("datasource: label already in use")

// ErrHasActiveJobs is returned when a deletion is attempted while non-terminal
// query jobs still reference the data source.
var ErrHasActiveJobs = errors.New("datasource: has active jobs — cannot delete")

// Stable client-facing error codes.
const (
	ErrCodeNotFound        = "DATA_SOURCE_NOT_FOUND"
	ErrCodeDuplicateLabel  = "DATA_SOURCE_LABEL_CONFLICT"
	ErrCodeHasActiveJobs   = "DATA_SOURCE_HAS_ACTIVE_JOBS"
	ErrCodeInvalidInput    = "DATA_SOURCE_INVALID_INPUT"
	ErrCodeConnectFailed   = "DATA_SOURCE_CONNECT_FAILED"
	ErrCodeInternal        = "DATA_SOURCE_INTERNAL_ERROR"
	ErrCodeTLSNotPermitted = "DATA_SOURCE_TLS_NOT_PERMITTED"
)

// ServiceError carries a stable code and a safe message for HTTP responses.
type ServiceError struct {
	Code    string
	Message string
	Status  int // suggested HTTP status
}

func (e *ServiceError) Error() string { return e.Code + ": " + e.Message }

func newError(code, msg string, status int) *ServiceError {
	return &ServiceError{Code: code, Message: msg, Status: status}
}

// Convenience constructors used by Service.
func errBadInput(msg string) *ServiceError { return newError(ErrCodeInvalidInput, msg, 400) }
func errNotFound() *ServiceError           { return newError(ErrCodeNotFound, "data source not found", 404) }
func errDuplicateLabel() *ServiceError {
	return newError(ErrCodeDuplicateLabel, "label already in use", 409)
}
func errHasActiveJobs() *ServiceError {
	return newError(ErrCodeHasActiveJobs, "data source has active jobs", 422)
}
func errConnectFailed(reason string) *ServiceError {
	return newError(ErrCodeConnectFailed, reason, 422)
}
func errInternal() *ServiceError { return newError(ErrCodeInternal, "internal error", 500) }
