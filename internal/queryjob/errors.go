package queryjob

import "errors"

// Stable error codes returned to clients. They never expose driver details,
// DSNs, credentials, addresses, or stack traces.
const (
	ErrCodeInvalidQuestion     = "INVALID_QUESTION"
	ErrCodeUnsupportedQuestion = "UNSUPPORTED_QUESTION"
	ErrCodeJobNotFound         = "JOB_NOT_FOUND"
	ErrCodeInvalidJobID        = "INVALID_JOB_ID"
	ErrCodeQueryExecution      = "QUERY_EXECUTION_FAILED"
	ErrCodePublishFailed       = "PUBLISH_FAILED"
	ErrCodeInternal            = "INTERNAL_ERROR"

	// Phase 4: result caching error codes.
	ErrCodeResultCacheFailed  = "RESULT_CACHE_FAILED"
	ErrCodeResultNotReady     = "RESULT_NOT_READY"
	ErrCodeQueryJobFailed     = "QUERY_JOB_FAILED"
	ErrCodeResultExpired      = "RESULT_EXPIRED"
	ErrCodeResultUnavailable  = "RESULT_UNAVAILABLE"
	ErrCodeResultStoreUnavail = "RESULT_STORE_UNAVAILABLE"
	ErrCodeResultCorrupted    = "RESULT_CORRUPTED"

	// Phase 5: SQL Guard and resource-limit error codes.
	ErrCodeSQLValidationFailed = "SQL_VALIDATION_FAILED"
	ErrCodeResultTooLarge      = "RESULT_TOO_LARGE"

	// Phase 6A: ownership / auth error codes.
	ErrCodeJobNotOwned = "JOB_NOT_FOUND" // same as not-found to avoid IDOR leakage

	// Phase 6B: data-source error codes used by Service.Submit.
	ErrCodeMissingDataSource  = "MISSING_DATA_SOURCE"
	ErrCodeDataSourceNotFound = "DATA_SOURCE_NOT_FOUND"
)

// Safe, client-facing messages paired with the codes above.
const (
	msgUnsupportedQuestion = "question is not supported"
	msgQueryExecution      = "failed to execute the query"
	msgPublishFailed       = "failed to queue the request"
	msgInternal            = "internal error"
	msgResultCacheFailed   = "failed to cache query result"
	msgSQLValidationFailed = "generated query failed validation"
	msgResultTooLarge      = "query result is too large"
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
