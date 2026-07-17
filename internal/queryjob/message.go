package queryjob

import "time"

// MessageTypeQueryExecutionRequested is the type field value for async query jobs.
const MessageTypeQueryExecutionRequested = "query.execution.requested"

// messageVersion is the current envelope version for query execution messages.
const messageVersion = 1

// QueryJobMessage is the JSON envelope published to RabbitMQ when a query job
// is accepted. It carries only the job_id; the worker reads the full job from
// MySQL. Sensitive fields (question, SQL, DSN, credentials) must never appear
// in this envelope.
type QueryJobMessage struct {
	MessageID  string     `json:"message_id"`
	Type       string     `json:"type"`
	Version    int        `json:"version"`
	OccurredAt time.Time  `json:"occurred_at"`
	Payload    JobPayload `json:"payload"`
}

// JobPayload carries the minimal data needed to locate the job in MySQL.
type JobPayload struct {
	JobID uint64 `json:"job_id"`
}
