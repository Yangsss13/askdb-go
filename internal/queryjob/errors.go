package queryjob

import "errors"

// Stable error codes returned to clients. They never expose driver details,
// DSNs, credentials, addresses, or stack traces.
const (
	ErrCodeInvalidQuestion     = "INVALID_QUESTION"
	ErrCodeUnsupportedQuestion = "UNSUPPORTED_QUESTION"
	ErrCodeJobNotFound         = "JOB_NOT_FOUND"
	ErrCodeQueryExecution      = "QUERY_EXECUTION_FAILED"
	ErrCodeInternal            = "INTERNAL_ERROR"
)

// Safe, client-facing messages paired with the codes above.
const (
	msgUnsupportedQuestion = "question is not supported"
	msgQueryExecution      = "failed to execute the query"
	msgInternal            = "internal error"
)

// ErrJobNotFound is returned by the repository when no job matches the given ID.
var ErrJobNotFound = errors.New("queryjob: not found")

// maxQuestionLen bounds the accepted question length (also enforced by the
// query_jobs.question column width).
const maxQuestionLen = 500

// ServiceError carries a stable error code alongside the outcome of a Submit
// call. The Service uses it to let the handler pick the right HTTP status
// without inspecting underlying errors.
type ServiceError struct {
	Code    string
	Message string
}

func (e *ServiceError) Error() string { return e.Code + ": " + e.Message }

func newServiceError(code, msg string) *ServiceError {
	return &ServiceError{Code: code, Message: msg}
}
